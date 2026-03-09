package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/riverfjs/aevitas/internal/bus"
	"github.com/riverfjs/aevitas/internal/config"
	"github.com/riverfjs/aevitas/internal/protocol"
	"github.com/riverfjs/agentsdk-go/pkg/api"
	sdklogger "github.com/riverfjs/agentsdk-go/pkg/logger"
	telegramify "github.com/riverfjs/telegramify-go"
)

const telegramChannelName = "telegram"

const telegramInboundReactionEmoji = "👾"

// TelegramBot interface for mocking telegram bot API
type TelegramBot interface {
	GetUpdatesChan(config tgbotapi.UpdateConfig) tgbotapi.UpdatesChannel
	StopReceivingUpdates()
	Send(c tgbotapi.Chattable) (tgbotapi.Message, error)
	EditMessageText(chatID int64, messageID int, text string) (tgbotapi.Message, error)
	DeleteMessage(chatID int64, messageID int) error
	SetMessageReaction(chatID int64, messageID int, emoji string) error
	GetSelf() tgbotapi.User
	GetFileDirectURL(fileID string) (string, error)
}

// tgBotWrapper wraps tgbotapi.BotAPI to implement TelegramBot interface
type tgBotWrapper struct {
	bot *tgbotapi.BotAPI
}

func (w *tgBotWrapper) GetUpdatesChan(config tgbotapi.UpdateConfig) tgbotapi.UpdatesChannel {
	return w.bot.GetUpdatesChan(config)
}

func (w *tgBotWrapper) StopReceivingUpdates() {
	w.bot.StopReceivingUpdates()
}

func (w *tgBotWrapper) Send(c tgbotapi.Chattable) (tgbotapi.Message, error) {
	return w.bot.Send(c)
}

func (w *tgBotWrapper) EditMessageText(chatID int64, messageID int, text string) (tgbotapi.Message, error) {
	edit := tgbotapi.NewEditMessageText(chatID, messageID, text)
	return w.bot.Send(edit)
}

func (w *tgBotWrapper) DeleteMessage(chatID int64, messageID int) error {
	del := tgbotapi.NewDeleteMessage(chatID, messageID)
	_, err := w.bot.Request(del)
	return err
}

func (w *tgBotWrapper) SetMessageReaction(chatID int64, messageID int, emoji string) error {
	reaction, err := json.Marshal([]map[string]string{
		{
			"type":  "emoji",
			"emoji": strings.TrimSpace(emoji),
		},
	})
	if err != nil {
		return fmt.Errorf("marshal telegram reaction: %w", err)
	}
	resp, err := w.bot.MakeRequest("setMessageReaction", tgbotapi.Params{
		"chat_id":    strconv.FormatInt(chatID, 10),
		"message_id": strconv.Itoa(messageID),
		"reaction":   string(reaction),
	})
	if err != nil {
		return err
	}
	if !resp.Ok {
		return fmt.Errorf("telegram setMessageReaction failed: %s", resp.Description)
	}
	return nil
}

func (w *tgBotWrapper) GetSelf() tgbotapi.User {
	return w.bot.Self
}

func (w *tgBotWrapper) GetFileDirectURL(fileID string) (string, error) {
	return w.bot.GetFileDirectURL(fileID)
}

// BotFactory creates TelegramBot instances (allows mocking)
type BotFactory func(token, apiEndpoint string, client *http.Client) (TelegramBot, error)

// defaultBotFactory creates real telegram bot
var defaultBotFactory BotFactory = func(token, apiEndpoint string, client *http.Client) (TelegramBot, error) {
	bot, err := tgbotapi.NewBotAPIWithClient(token, apiEndpoint, client)
	if err != nil {
		return nil, err
	}
	return &tgBotWrapper{bot: bot}, nil
}

type TelegramChannel struct {
	BaseChannel
	token      string
	bot        TelegramBot
	proxy      string
	cancel     context.CancelFunc
	botFactory BotFactory
}

func NewTelegramChannel(cfg config.TelegramConfig, b *bus.MessageBus, logger sdklogger.Logger) (*TelegramChannel, error) {
	return NewTelegramChannelWithFactory(cfg, b, defaultBotFactory, logger)
}

// NewTelegramChannelWithFactory creates a TelegramChannel with custom bot factory (for testing)
func NewTelegramChannelWithFactory(cfg config.TelegramConfig, b *bus.MessageBus, factory BotFactory, logger sdklogger.Logger) (*TelegramChannel, error) {
	if cfg.Token == "" {
		return nil, fmt.Errorf("telegram token is required")
	}

	ch := &TelegramChannel{
		BaseChannel: NewBaseChannel(telegramChannelName, b, cfg.AllowFrom, logger),
		token:       cfg.Token,
		proxy:       cfg.Proxy,
		botFactory:  factory,
	}
	return ch, nil
}

func (t *TelegramChannel) initBot() error {
	var client *http.Client
	if t.proxy != "" {
		proxyURL, err := url.Parse(t.proxy)
		if err != nil {
			return fmt.Errorf("parse proxy url: %w", err)
		}
		client = &http.Client{
			Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
		}
	} else {
		client = http.DefaultClient
	}

	bot, err := t.botFactory(t.token, tgbotapi.APIEndpoint, client)
	if err != nil {
		return fmt.Errorf("create telegram bot: %w", err)
	}
	t.bot = bot
	t.logger.Infof("[telegram] authorized as @%s", bot.GetSelf().UserName)
	return nil
}

func (t *TelegramChannel) Start(ctx context.Context) error {
	if err := t.initBot(); err != nil {
		return err
	}

	ctx, t.cancel = context.WithCancel(ctx)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 30
	updates := t.bot.GetUpdatesChan(u)

	go func() {
		for {
			select {
			case update := <-updates:
				if update.CallbackQuery != nil {
					t.handleCallbackQuery(update.CallbackQuery)
					continue
				}
				if update.Message != nil {
					t.handleMessage(update.Message)
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	t.logger.Infof("[telegram] polling started")
	return nil
}

func (t *TelegramChannel) handleMessage(msg *tgbotapi.Message) {
	senderID := strconv.FormatInt(msg.From.ID, 10)

	if !t.IsAllowed(senderID) {
		t.logger.Warnf("[telegram] rejected message from %s (%s)", senderID, msg.From.UserName)
		return
	}

	content := msg.Text
	if content == "" && msg.Caption != "" {
		content = msg.Caption
	}

	// Download media if present
	var media []string
	var attachments []api.Attachment
	if msg.Photo != nil && len(msg.Photo) > 0 {
		// Get largest photo
		photo := msg.Photo[len(msg.Photo)-1]
		localPath, err := t.downloadFile(photo.FileID, "photo")
		if err != nil {
			t.logger.Warnf("failed to download photo: %v", err)
		} else {
			media = append(media, localPath)
			attachments = append(attachments, api.Attachment{
				FilePath: localPath,
				Type:     "image",
				MimeType: "image/jpeg",
			})
			t.logger.Debugf("downloaded photo to %s", localPath)
		}
	}
	if msg.Voice != nil && strings.TrimSpace(msg.Voice.FileID) != "" {
		localPath, err := t.downloadFile(msg.Voice.FileID, "voice")
		if err != nil {
			t.logger.Warnf("failed to download voice: %v", err)
		} else {
			media = append(media, localPath)
			attachments = append(attachments, api.Attachment{
				FilePath: localPath,
				Type:     "audio",
				MimeType: strings.TrimSpace(msg.Voice.MimeType),
			})
			t.logger.Debugf("downloaded voice to %s", localPath)
		}
	}
	if msg.Audio != nil && strings.TrimSpace(msg.Audio.FileID) != "" {
		localPath, err := t.downloadFile(msg.Audio.FileID, "audio")
		if err != nil {
			t.logger.Warnf("failed to download audio: %v", err)
		} else {
			media = append(media, localPath)
			attachments = append(attachments, api.Attachment{
				FilePath: localPath,
				Type:     "audio",
				MimeType: strings.TrimSpace(msg.Audio.MimeType),
			})
			t.logger.Debugf("downloaded audio to %s", localPath)
		}
	}
	if msg.Document != nil && strings.TrimSpace(msg.Document.FileID) != "" {
		mime := strings.ToLower(strings.TrimSpace(msg.Document.MimeType))
		kind := ""
		switch {
		case strings.HasPrefix(mime, "audio/"):
			kind = "audio"
		case strings.HasPrefix(mime, "image/"):
			kind = "image"
		}
		if kind != "" {
			localPath, err := t.downloadFile(msg.Document.FileID, kind)
			if err != nil {
				t.logger.Warnf("failed to download %s document: %v", kind, err)
			} else {
				media = append(media, localPath)
				attachments = append(attachments, api.Attachment{
					FilePath: localPath,
					Type:     kind,
					MimeType: mime,
				})
				t.logger.Debugf("downloaded %s document to %s", kind, localPath)
			}
		}
	}

	// Skip messages with no content and no media
	if content == "" && len(media) == 0 {
		return
	}

	chatID := strconv.FormatInt(msg.Chat.ID, 10)
	go t.sendInboundReaction(msg.Chat.ID, msg.MessageID)

	// Start continuous typing indicator (stops when message is received in Inbound channel)
	// Telegram typing indicator lasts 5 seconds, so we resend every 4 seconds
	stopTyping := make(chan struct{})
	go func() {
		ticker := time.NewTicker(4 * time.Second)
		defer ticker.Stop()

		// Send first typing immediately
		typing := tgbotapi.NewChatAction(msg.Chat.ID, tgbotapi.ChatTyping)
		t.bot.Send(typing)

		for {
			select {
			case <-stopTyping:
				return
			case <-ticker.C:
				typing := tgbotapi.NewChatAction(msg.Chat.ID, tgbotapi.ChatTyping)
				t.bot.Send(typing)
			}
		}
	}()

	t.bus.Inbound <- bus.InboundMessage{
		Channel:     telegramChannelName,
		SenderID:    senderID,
		ChatID:      chatID,
		Content:     content,
		Media:       media,
		Attachments: attachments,
		Timestamp:   time.Unix(int64(msg.Date), 0),
		Metadata: map[string]any{
			"username":    msg.From.UserName,
			"first_name":  msg.From.FirstName,
			"message_id":  msg.MessageID,
			"stop_typing": stopTyping, // Pass channel to gateway to stop typing
		},
	}
}

func (t *TelegramChannel) sendInboundReaction(chatID int64, messageID int) {
	if t == nil || t.bot == nil || chatID == 0 || messageID == 0 {
		return
	}
	if err := t.bot.SetMessageReaction(chatID, messageID, telegramInboundReactionEmoji); err != nil {
		t.logger.Warnf("[telegram] set inbound reaction failed chat=%d message_id=%d err=%v", chatID, messageID, err)
	}
}

func (t *TelegramChannel) handleCallbackQuery(cb *tgbotapi.CallbackQuery) {
	if cb == nil || cb.From == nil || cb.Message == nil {
		return
	}
	senderID := strconv.FormatInt(cb.From.ID, 10)
	if !t.IsAllowed(senderID) {
		return
	}
	action, approvalID, ok := parseApprovalCallbackData(cb.Data)
	if !ok {
		return
	}
	chatID := strconv.FormatInt(cb.Message.Chat.ID, 10)
	_ = t.bot.DeleteMessage(cb.Message.Chat.ID, cb.Message.MessageID)
	t.bus.Inbound <- bus.InboundMessage{
		Channel:   telegramChannelName,
		SenderID:  senderID,
		ChatID:    chatID,
		Content:   "",
		Timestamp: time.Now(),
		Metadata: map[string]any{
			"approval_action": action,
			"approval_id":     approvalID,
			"message_id":      cb.Message.MessageID,
		},
	}
}

func (t *TelegramChannel) Stop() error {
	if t.cancel != nil {
		t.cancel()
	}
	if t.bot != nil {
		t.bot.StopReceivingUpdates()
	}
	return nil
}

// downloadFile downloads a Telegram file to local temp directory
func (t *TelegramChannel) downloadFile(fileID, prefix string) (string, error) {
	fileURL, err := t.bot.GetFileDirectURL(fileID)
	if err != nil {
		return "", fmt.Errorf("get file url: %w", err)
	}

	// Create temp directory
	tempDir := filepath.Join(os.TempDir(), "aevitas-telegram-media")
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}

	// Download file
	resp, err := http.Get(fileURL)
	if err != nil {
		return "", fmt.Errorf("download file: %w", err)
	}
	defer resp.Body.Close()

	// Generate unique filename
	filename := fmt.Sprintf("%s-%d-%s", prefix, time.Now().Unix(), filepath.Base(fileURL))
	localPath := filepath.Join(tempDir, filename)

	// Save to disk
	outFile, err := os.Create(localPath)
	if err != nil {
		return "", fmt.Errorf("create local file: %w", err)
	}
	defer outFile.Close()

	if _, err := io.Copy(outFile, resp.Body); err != nil {
		return "", fmt.Errorf("save file: %w", err)
	}

	return localPath, nil
}

// SetBot sets the bot (for testing)
func (t *TelegramChannel) SetBot(bot TelegramBot) {
	t.bot = bot
}

func (t *TelegramChannel) Send(msg bus.OutboundMessage) error {
	if t.bot == nil {
		return fmt.Errorf("telegram bot not initialized")
	}

	chatID, err := strconv.ParseInt(msg.ChatID, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid chat id %q: %w", msg.ChatID, err)
	}
	event := protocol.EventType(msg.Metadata)
	replyToMessageID := parseReplyToMessageID(msg.ReplyTo)

	// Send media files first (if any)
	attachments := msg.Attachments
	if len(attachments) == 0 && len(msg.Media) > 0 {
		for _, p := range msg.Media {
			attachments = append(attachments, api.Attachment{FilePath: p})
		}
	}
	for _, att := range attachments {
		if err := t.sendMediaFile(chatID, att); err != nil {
			t.logger.Warnf("failed to send media file %s: %v", att.FilePath, err)
			// Continue with other files
		}
	}

	// Send text content if present
	if msg.Content != "" {
		if msg.Metadata != nil {
			if isApproval, _ := msg.Metadata["approval_prompt"].(bool); isApproval {
				if approvalID, _ := msg.Metadata["approval_id"].(string); strings.TrimSpace(approvalID) != "" {
					return t.sendApprovalPrompt(chatID, msg.Content, approvalID)
				}
			}
		}
		switch event {
		case protocol.EventPreviewUpdate:
			return t.sendOrEditEvent(chatID, msg, replyToMessageID, protocol.EventPreviewUpdate)
		case protocol.EventPreviewFinal:
			return t.sendOrEditEvent(chatID, msg, replyToMessageID, protocol.EventPreviewFinal)
		case protocol.EventUsageHUD:
			return t.sendUsageHUD(chatID, msg.Content)
		case protocol.EventToolProgress:
			return t.sendOrEditEvent(chatID, msg, replyToMessageID, protocol.EventToolProgress)
		}
		return t.sendNewMessage(chatID, msg.Content, replyToMessageID)
	}

	return nil
}

func (t *TelegramChannel) sendApprovalPrompt(chatID int64, content string, approvalID string) error {
	msg := tgbotapi.NewMessage(chatID, truncateTelegramText(content, 4000))
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("✅ 允许", "approval:allow:"+approvalID),
			tgbotapi.NewInlineKeyboardButtonData("❌ 拒绝", "approval:deny:"+approvalID),
		),
	)
	_, err := t.bot.Send(msg)
	return err
}

func parseApprovalCallbackData(data string) (action string, approvalID string, ok bool) {
	parts := strings.Split(strings.TrimSpace(data), ":")
	if len(parts) != 3 || parts[0] != "approval" {
		return "", "", false
	}
	act := strings.ToLower(strings.TrimSpace(parts[1]))
	switch act {
	case "allow", "deny":
	default:
		return "", "", false
	}
	id := strings.TrimSpace(parts[2])
	if id == "" {
		return "", "", false
	}
	return act, id, true
}

func parseReplyToMessageID(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	id, err := strconv.Atoi(raw)
	if err != nil || id <= 0 {
		return 0
	}
	return id
}

func (t *TelegramChannel) sendOrEditEvent(chatID int64, msg bus.OutboundMessage, replyToMessageID int, eventType string) error {
	text := strings.TrimSpace(msg.Content)
	if text == "" {
		return nil
	}
	requestID := protocol.MetaString(msg.Metadata, protocol.RequestIDKey)
	if requestID == "" {
		return fmt.Errorf("missing request_id for outbound_result event=%s", eventType)
	}
	targetID := parseReplyToMessageID(protocol.MetaString(msg.Metadata, "message_id"))
	if targetID > 0 {
		err := t.editMarkdownMessage(chatID, targetID, text)
		if err == nil {
			t.PublishOutboundResult(strconv.FormatInt(chatID, 10), eventType, requestID, strconv.Itoa(targetID))
			return nil
		}
		if eventType == protocol.EventPreviewFinal {
			if strings.Contains(err.Error(), "message is not modified") {
				// no-op: content already matches
			} else {
				t.logger.Warnf("[telegram] preview_final edit failed, skip fallback request_id=%s err=%v", requestID, err)
			}
			return nil
		}
		t.logger.Warnf("[telegram] event edit failed event=%s request_id=%s; fallback to send", eventType, requestID)
	}
	sentID, err := t.sendMarkdownText(chatID, text, replyToMessageID)
	if err != nil {
		return err
	}
	t.PublishOutboundResult(strconv.FormatInt(chatID, 10), eventType, requestID, strconv.Itoa(sentID))
	return nil
}

func (t *TelegramChannel) sendUsageHUD(chatID int64, content string) error {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	// Usage HUD is always sent as standalone message.
	return t.sendPlainText(chatID, content)
}

func (t *TelegramChannel) sendPlainText(chatID int64, content string) error {
	text := truncateTelegramText(content, 4000)
	if text == "" {
		return nil
	}
	_, err := t.bot.Send(tgbotapi.NewMessage(chatID, text))
	return err
}

func closeOpenMarkdown(s string) string {
	if s == "" {
		return s
	}
	closed := s
	if strings.Count(closed, "```")%2 == 1 {
		closed += "\n```"
	}
	if strings.Count(closed, "`")%2 == 1 {
		closed += "`"
	}
	if strings.Count(closed, "**")%2 == 1 {
		closed += "**"
	}
	return closed
}

func truncateTelegramText(text string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	r := []rune(text)
	if len(r) <= maxRunes {
		return text
	}
	return string(r[:maxRunes]) + "..."
}

// sendPhoto sends a photo to Telegram (kept for backward compatibility)
func (t *TelegramChannel) sendPhoto(chatID int64, imagePath string) error {
	// Check if file exists
	if _, err := os.Stat(imagePath); os.IsNotExist(err) {
		return fmt.Errorf("image file not found: %s", imagePath)
	}

	photo := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(imagePath))
	_, err := t.bot.Send(photo)
	if err != nil {
		return fmt.Errorf("send telegram photo: %w", err)
	}

	t.logger.Infof("sent photo to telegram chat_id=%d path=%s", chatID, imagePath)
	return nil
}

// sendMediaFile sends a file (document, image, etc.) to Telegram
func (t *TelegramChannel) sendMediaFile(chatID int64, att api.Attachment) error {
	filePath := strings.TrimSpace(att.FilePath)
	// Check if file exists
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return fmt.Errorf("media file not found: %s", filePath)
	}

	kind := strings.ToLower(strings.TrimSpace(att.Type))
	if kind == "" {
		mime := strings.TrimSpace(att.MimeType)
		if mime == "" {
			mime = strings.TrimSpace(api.DetectAttachmentMIME("", filePath))
		}
		kind = api.DetectAttachmentTypeFromMIME(mime)
	}

	if kind == "image" {
		// Send as photo; fall back to document when Telegram rejects the image
		// (e.g. PHOTO_INVALID_DIMENSIONS for very tall/wide screenshots).
		photo := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(filePath))
		photo.Caption = filepath.Base(filePath)
		if _, err := t.bot.Send(photo); err != nil {
			t.logger.Warnf("photo upload failed (%v), retrying as document: %s", err, filePath)
			doc := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(filePath))
			doc.Caption = filepath.Base(filePath)
			if _, docErr := t.bot.Send(doc); docErr != nil {
				return fmt.Errorf("send telegram document (photo fallback): %w", docErr)
			}
			t.logger.Infof("sent file as document (photo fallback) chat_id=%d path=%s", chatID, filePath)
		} else {
			t.logger.Infof("sent photo to telegram chat_id=%d path=%s", chatID, filePath)
		}
	} else if kind == "audio" {
		voicePath := filePath
		voice := tgbotapi.NewVoice(chatID, tgbotapi.FilePath(voicePath))
		if _, err := t.bot.Send(voice); err == nil {
			t.logger.Infof("sent voice to telegram chat_id=%d path=%s", chatID, filePath)
			return nil
		}
		t.logger.Warnf("[telegram] send voice failed, fallback to audio path=%s", filePath)
		audio := tgbotapi.NewAudio(chatID, tgbotapi.FilePath(filePath))
		audio.Caption = filepath.Base(filePath)
		if _, err := t.bot.Send(audio); err == nil {
			t.logger.Infof("sent audio to telegram chat_id=%d path=%s", chatID, filePath)
			return nil
		}
		// Fallback to document
		doc := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(filePath))
		doc.Caption = filepath.Base(filePath)
		if _, err := t.bot.Send(doc); err != nil {
			return fmt.Errorf("send telegram audio/document fallback: %w", err)
		}
		t.logger.Infof("sent audio as document to telegram chat_id=%d path=%s", chatID, filePath)
	} else {
		// Send as document using FileBytes so the display name is always the
		// symlink name (e.g. "aevitas.log") rather than the symlink target
		// (e.g. "aevitas-20260224.log") which tgbotapi.FilePath would resolve.
		data, err := os.ReadFile(filePath)
		if err != nil {
			return fmt.Errorf("read file for telegram: %w", err)
		}
		doc := tgbotapi.NewDocument(chatID, tgbotapi.FileBytes{
			Name:  filepath.Base(filePath),
			Bytes: data,
		})
		if _, err := t.bot.Send(doc); err != nil {
			return fmt.Errorf("send telegram document: %w", err)
		}
		t.logger.Infof("sent document to telegram chat_id=%d path=%s", chatID, filePath)
	}

	return nil
}

// sendNewMessage sends a new message using Telegramify pipeline for Telegram presentation.
// Channel-agnostic Mermaid rendering should be handled by skill layer and sent as image attachments.
func (t *TelegramChannel) sendNewMessage(chatID int64, content string, replyToMessageID int) error {
	ctx := context.Background()

	// Process markdown with full pipeline (split, code extraction, Telegram rendering)
	const maxUTF16Len = 4090 // Leave some margin (Telegram limit is 4096)
	contents, err := telegramify.Telegramify(ctx, content, maxUTF16Len, false, nil)
	if err != nil {
		return fmt.Errorf("telegramify process: %w", err)
	}

	// Send each content piece in order; only the first piece replies to user message.
	firstPiece := true
	for _, item := range contents {
		currentReplyTo := 0
		if firstPiece {
			currentReplyTo = replyToMessageID
			firstPiece = false
		}
		switch c := item.(type) {
		case *telegramify.Text:
			if err := t.sendTextContent(chatID, c, currentReplyTo); err != nil {
				return err
			}
		case *telegramify.File:
			if err := t.sendFileContent(chatID, c, currentReplyTo); err != nil {
				return err
			}
		case *telegramify.Photo:
			if err := t.sendPhotoContent(chatID, c, currentReplyTo); err != nil {
				return err
			}
		default:
			t.logger.Warnf("[telegram] unknown content type: %T", item)
		}
	}

	return nil
}

// sendTextContent sends a text message with entities
func (t *TelegramChannel) sendTextContent(chatID int64, text *telegramify.Text, replyToMessageID int) error {
	tgMsg := tgbotapi.NewMessage(chatID, text.Text)
	if replyToMessageID > 0 {
		tgMsg.ReplyToMessageID = replyToMessageID
	}

	// Convert MessageEntity to Telegram's format
	tgMsg.Entities = toTelegramEntities(text.Entities)

	// Send the message
	if _, err := t.bot.Send(tgMsg); err != nil {
		// Fallback to plain text if entity parsing fails
		t.logger.Warnf("[telegram] failed to send with entities, falling back to plain text: %v", err)
		fallbackMsg := tgbotapi.NewMessage(chatID, text.Text)
		if replyToMessageID > 0 {
			fallbackMsg.ReplyToMessageID = replyToMessageID
		}
		if _, err2 := t.bot.Send(fallbackMsg); err2 != nil {
			return fmt.Errorf("send telegram message: %w", err2)
		}
	}

	return nil
}

func (t *TelegramChannel) sendMarkdownText(chatID int64, markdown string, replyToMessageID int) (int, error) {
	text, entities := telegramify.Convert(markdown, false, nil)
	msg := tgbotapi.NewMessage(chatID, text)
	if replyToMessageID > 0 {
		msg.ReplyToMessageID = replyToMessageID
	}
	msg.Entities = toTelegramEntities(entities)
	sent, err := t.bot.Send(msg)
	if err != nil {
		return 0, err
	}
	return sent.MessageID, nil
}

func (t *TelegramChannel) editMarkdownMessage(chatID int64, messageID int, markdown string) error {
	text, entities := telegramify.Convert(markdown, false, nil)
	edit := tgbotapi.NewEditMessageText(chatID, messageID, text)
	edit.Entities = toTelegramEntities(entities)
	_, err := t.bot.Send(edit)
	return err
}

func toTelegramEntities(entities []telegramify.MessageEntity) []tgbotapi.MessageEntity {
	if len(entities) == 0 {
		return nil
	}
	tgEntities := make([]tgbotapi.MessageEntity, 0, len(entities))
	for _, ent := range entities {
		tgEnt := tgbotapi.MessageEntity{
			Type:   ent.Type,
			Offset: ent.Offset,
			Length: ent.Length,
		}
		if ent.URL != "" {
			tgEnt.URL = ent.URL
		}
		if ent.Language != "" {
			tgEnt.Language = ent.Language
		}
		tgEntities = append(tgEntities, tgEnt)
	}
	return tgEntities
}

func (t *TelegramChannel) editPreviewWithTextContent(chatID int64, messageID int, text *telegramify.Text) (bool, error) {
	if text == nil {
		return false, nil
	}
	msgText := strings.TrimSpace(text.Text)
	if msgText == "" {
		return false, nil
	}
	edit := tgbotapi.NewEditMessageText(chatID, messageID, msgText)
	edit.Entities = toTelegramEntities(text.Entities)
	_, err := t.bot.Send(edit)
	if err != nil {
		if isIgnorableEditError(err) {
			return false, nil
		}
		return false, fmt.Errorf("edit preview text: %w", err)
	}
	edited := true
	if len(edit.Entities) == 0 {
		// No entities on final text is still valid; keep behavior explicit.
		edited = true
	}
	return edited, nil
}

func (t *TelegramChannel) editPreviewText(chatID int64, messageID int, text string) (bool, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return false, nil
	}
	_, err := t.bot.EditMessageText(chatID, messageID, text)
	if err != nil {
		if isIgnorableEditError(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func isIgnorableEditError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(msg, "message is not modified")
}

// sendFileContent sends a file (e.g., code block)
func (t *TelegramChannel) sendFileContent(chatID int64, file *telegramify.File, replyToMessageID int) error {
	// Create FileBytes from data
	fileBytes := tgbotapi.FileBytes{
		Name:  file.FileName,
		Bytes: file.FileData,
	}

	doc := tgbotapi.NewDocument(chatID, fileBytes)
	if replyToMessageID > 0 {
		doc.ReplyToMessageID = replyToMessageID
	}

	// Add caption if present
	if file.CaptionText != "" {
		doc.Caption = file.CaptionText

		// Add caption entities
		if len(file.CaptionEntities) > 0 {
			tgEntities := make([]tgbotapi.MessageEntity, 0, len(file.CaptionEntities))
			for _, ent := range file.CaptionEntities {
				tgEntities = append(tgEntities, tgbotapi.MessageEntity{
					Type:     ent.Type,
					Offset:   ent.Offset,
					Length:   ent.Length,
					URL:      ent.URL,
					Language: ent.Language,
				})
			}
			doc.CaptionEntities = tgEntities
		}
	}

	if _, err := t.bot.Send(doc); err != nil {
		return fmt.Errorf("send file: %w", err)
	}

	t.logger.Debugf("[telegram] sent file: %s", file.FileName)
	return nil
}

// sendPhotoContent sends a photo attachment.
// Mermaid cross-channel rendering is handled by skills (for example render-mermaid)
// before the content reaches channel send logic.
func (t *TelegramChannel) sendPhotoContent(chatID int64, photo *telegramify.Photo, replyToMessageID int) error {
	// Create FileBytes from image data
	fileBytes := tgbotapi.FileBytes{
		Name:  photo.FileName,
		Bytes: photo.FileData,
	}

	photoMsg := tgbotapi.NewPhoto(chatID, fileBytes)
	if replyToMessageID > 0 {
		photoMsg.ReplyToMessageID = replyToMessageID
	}

	// Add caption if present
	if photo.CaptionText != "" {
		photoMsg.Caption = photo.CaptionText

		// Add caption entities
		if len(photo.CaptionEntities) > 0 {
			tgEntities := make([]tgbotapi.MessageEntity, 0, len(photo.CaptionEntities))
			for _, ent := range photo.CaptionEntities {
				tgEntities = append(tgEntities, tgbotapi.MessageEntity{
					Type:     ent.Type,
					Offset:   ent.Offset,
					Length:   ent.Length,
					URL:      ent.URL,
					Language: ent.Language,
				})
			}
			photoMsg.CaptionEntities = tgEntities
		}
	}

	if _, err := t.bot.Send(photoMsg); err != nil {
		return fmt.Errorf("send photo: %w", err)
	}

	t.logger.Debugf("[telegram] sent photo: %s", photo.FileName)
	return nil
}
