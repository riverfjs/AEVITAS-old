package channel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	larkdispatcher "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
	"github.com/riverfjs/agentsdk-go/pkg/api"
	sdklogger "github.com/riverfjs/agentsdk-go/pkg/logger"
	telegramify "github.com/riverfjs/telegramify-go"
	"github.com/riverfjs/aevitas/internal/bus"
	"github.com/riverfjs/aevitas/internal/config"
)

const feishuChannelName = "feishu"

type FeishuClient interface {
	SendMessage(ctx context.Context, chatID, content string) error
	GetTenantAccessToken(ctx context.Context) (string, error)
}

type feishuAdvancedClient interface {
	FeishuClient
	SendTypedMessage(ctx context.Context, chatID, msgType string, content map[string]string, replyTo string) (string, error)
	EditTextMessage(ctx context.Context, messageID, text string) error
	EditCardMessage(ctx context.Context, messageID, cardJSON string) error
	DeleteMessage(ctx context.Context, messageID string) error
	UploadImage(ctx context.Context, fileName string, data []byte) (string, error)
	UploadFile(ctx context.Context, fileName string, data []byte) (string, error)
	UploadAudio(ctx context.Context, fileName string, data []byte, durationMillis int) (string, error)
	DownloadResource(ctx context.Context, messageID, fileKey, resourceType string) ([]byte, string, error)
}

type feishuWSClient interface {
	Start(ctx context.Context) error
}

type FeishuWSFactory func(appID, appSecret string, onEvent func(context.Context, *larkevent.EventReq) error) (feishuWSClient, error)

type defaultFeishuClient struct {
	appID     string
	appSecret string
	mu        sync.RWMutex
	token     string
	tokenExp  time.Time
}

func (c *defaultFeishuClient) GetTenantAccessToken(ctx context.Context) (string, error) {
	c.mu.RLock()
	if c.token != "" && time.Now().Before(c.tokenExp) {
		token := c.token
		c.mu.RUnlock()
		return token, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.tokenExp) {
		return c.token, nil
	}

	body := fmt.Sprintf(`{"app_id":"%s","app_secret":"%s"}`, c.appID, c.appSecret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://open.feishu.cn/open-apis/auth/v3/tenant_access_token/internal",
		strings.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("get tenant token: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
		Expire            int    `json:"expire"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}
	if result.Code != 0 {
		return "", fmt.Errorf("feishu token error: %s", result.Msg)
	}
	c.token = result.TenantAccessToken
	c.tokenExp = time.Now().Add(time.Duration(result.Expire-60) * time.Second)
	return c.token, nil
}

func (c *defaultFeishuClient) SendMessage(ctx context.Context, chatID, content string) error {
	_, err := c.SendTypedMessage(ctx, chatID, "text", map[string]string{"text": content}, "")
	return err
}

func (c *defaultFeishuClient) SendTypedMessage(ctx context.Context, chatID, msgType string, content map[string]string, replyTo string) (string, error) {
	token, err := c.GetTenantAccessToken(ctx)
	if err != nil {
		return "", err
	}
	contentStr, err := encodeFeishuContent(msgType, content)
	if err != nil {
		return "", err
	}

	var endpoint string
	payload := map[string]any{
		"msg_type": msgType,
		"content":  contentStr,
	}
	if replyTo != "" {
		endpoint = fmt.Sprintf("https://open.feishu.cn/open-apis/im/v1/messages/%s/reply", replyTo)
	} else {
		endpoint = "https://open.feishu.cn/open-apis/im/v1/messages?receive_id_type=chat_id"
		payload["receive_id"] = chatID
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal send payload: %w", err)
	}
	resp, err := c.doJSON(ctx, token, http.MethodPost, endpoint, body)
	if err != nil {
		return "", fmt.Errorf("send feishu message type=%s chat_id=%s reply_to=%s: %w", msgType, chatID, strings.TrimSpace(replyTo), err)
	}
	var out struct {
		MessageID string `json:"message_id"`
	}
	_ = json.Unmarshal(resp.Data, &out)
	msgID := strings.TrimSpace(out.MessageID)
	return msgID, nil
}

func (c *defaultFeishuClient) EditTextMessage(ctx context.Context, messageID, text string) error {
	token, err := c.GetTenantAccessToken(ctx)
	if err != nil {
		return err
	}
	contentJSON, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return fmt.Errorf("marshal edit content: %w", err)
	}
	body, err := json.Marshal(map[string]any{
		"msg_type": "text",
		"content":  string(contentJSON),
	})
	if err != nil {
		return fmt.Errorf("marshal edit payload: %w", err)
	}
	_, err = c.doJSON(ctx, token, http.MethodPut,
		fmt.Sprintf("https://open.feishu.cn/open-apis/im/v1/messages/%s", strings.TrimSpace(messageID)),
		body)
	return err
}

func (c *defaultFeishuClient) EditCardMessage(ctx context.Context, messageID, cardJSON string) error {
	token, err := c.GetTenantAccessToken(ctx)
	if err != nil {
		return err
	}
	cardJSON = strings.TrimSpace(cardJSON)
	if cardJSON == "" {
		return fmt.Errorf("empty card content")
	}
	if !json.Valid([]byte(cardJSON)) {
		return fmt.Errorf("invalid card json content")
	}
	body, err := json.Marshal(map[string]any{
		"content": cardJSON,
	})
	if err != nil {
		return fmt.Errorf("marshal card patch payload: %w", err)
	}
	_, err = c.doJSON(ctx, token, http.MethodPatch,
		fmt.Sprintf("https://open.feishu.cn/open-apis/im/v1/messages/%s", strings.TrimSpace(messageID)),
		body)
	return err
}

func (c *defaultFeishuClient) DeleteMessage(ctx context.Context, messageID string) error {
	token, err := c.GetTenantAccessToken(ctx)
	if err != nil {
		return err
	}
	_, err = c.doJSON(ctx, token, http.MethodDelete,
		fmt.Sprintf("https://open.feishu.cn/open-apis/im/v1/messages/%s", strings.TrimSpace(messageID)),
		nil)
	return err
}

func (c *defaultFeishuClient) UploadImage(ctx context.Context, fileName string, data []byte) (string, error) {
	token, err := c.GetTenantAccessToken(ctx)
	if err != nil {
		return "", err
	}
	fields := map[string]string{"image_type": "message"}
	respData, err := c.uploadMultipart(ctx, token, "https://open.feishu.cn/open-apis/im/v1/images", "image", fileName, data, fields)
	if err != nil {
		return "", err
	}
	var out struct {
		ImageKey string `json:"image_key"`
	}
	if err := json.Unmarshal(respData, &out); err != nil {
		return "", fmt.Errorf("decode image upload response: %w", err)
	}
	if strings.TrimSpace(out.ImageKey) == "" {
		return "", fmt.Errorf("empty image_key from feishu upload")
	}
	return out.ImageKey, nil
}

func (c *defaultFeishuClient) UploadFile(ctx context.Context, fileName string, data []byte) (string, error) {
	token, err := c.GetTenantAccessToken(ctx)
	if err != nil {
		return "", err
	}
	fields := map[string]string{
		"file_type": "stream",
		"file_name": strings.TrimSpace(fileName),
	}
	respData, err := c.uploadMultipart(ctx, token, "https://open.feishu.cn/open-apis/im/v1/files", "file", fileName, data, fields)
	if err != nil {
		return "", err
	}
	var out struct {
		FileKey string `json:"file_key"`
	}
	if err := json.Unmarshal(respData, &out); err != nil {
		return "", fmt.Errorf("decode file upload response: %w", err)
	}
	if strings.TrimSpace(out.FileKey) == "" {
		return "", fmt.Errorf("empty file_key from feishu upload")
	}
	return out.FileKey, nil
}

func (c *defaultFeishuClient) UploadAudio(ctx context.Context, fileName string, data []byte, durationMillis int) (string, error) {
	token, err := c.GetTenantAccessToken(ctx)
	if err != nil {
		return "", err
	}
	if durationMillis <= 0 {
		durationMillis = audioDurationFallbackMillis
	}
	fields := map[string]string{
		"file_type": "opus",
		"file_name": strings.TrimSpace(fileName),
		"duration":  strconv.Itoa(durationMillis),
	}
	respData, err := c.uploadMultipart(ctx, token, "https://open.feishu.cn/open-apis/im/v1/files", "file", fileName, data, fields)
	if err != nil {
		return "", err
	}
	var out struct {
		FileKey string `json:"file_key"`
	}
	if err := json.Unmarshal(respData, &out); err != nil {
		return "", fmt.Errorf("decode audio upload response: %w", err)
	}
	if strings.TrimSpace(out.FileKey) == "" {
		return "", fmt.Errorf("empty file_key from feishu audio upload")
	}
	return out.FileKey, nil
}

func (c *defaultFeishuClient) DownloadResource(ctx context.Context, messageID, fileKey, resourceType string) ([]byte, string, error) {
	token, err := c.GetTenantAccessToken(ctx)
	if err != nil {
		return nil, "", err
	}
	resourceType = strings.TrimSpace(resourceType)
	if resourceType == "" {
		resourceType = "file"
	}
	url := fmt.Sprintf("https://open.feishu.cn/open-apis/im/v1/messages/%s/resources/%s?type=%s",
		strings.TrimSpace(messageID), strings.TrimSpace(fileKey), resourceType)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", fmt.Errorf("create download request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("download resource: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		return nil, "", fmt.Errorf("download resource status=%d body=%s", resp.StatusCode, string(body))
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("read resource body: %w", err)
	}
	return data, strings.TrimSpace(resp.Header.Get("Content-Type")), nil
}

type feishuJSONResponse struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

func (c *defaultFeishuClient) doJSON(ctx context.Context, token, method, endpoint string, body []byte) (*feishuJSONResponse, error) {
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	var out feishuJSONResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if out.Code != 0 {
		return nil, fmt.Errorf("feishu api error: %s", out.Msg)
	}
	return &out, nil
}

func (c *defaultFeishuClient) uploadMultipart(ctx context.Context, token, endpoint, fileField, fileName string, data []byte, fields map[string]string) (json.RawMessage, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			return nil, fmt.Errorf("write field %s: %w", k, err)
		}
	}
	part, err := w.CreateFormFile(fileField, fileName)
	if err != nil {
		return nil, fmt.Errorf("create form file: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return nil, fmt.Errorf("write form file: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("close multipart writer: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &buf)
	if err != nil {
		return nil, fmt.Errorf("create upload request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("upload request: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read upload response: %w", err)
	}
	var out feishuJSONResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode upload response: %w", err)
	}
	if out.Code != 0 {
		return nil, fmt.Errorf("feishu upload error: %s", out.Msg)
	}
	return out.Data, nil
}

type FeishuClientFactory func(appID, appSecret string) FeishuClient

var defaultFeishuClientFactory FeishuClientFactory = func(appID, appSecret string) FeishuClient {
	return &defaultFeishuClient{appID: appID, appSecret: appSecret}
}

func defaultFeishuWSFactory(appID, appSecret string, onEvent func(context.Context, *larkevent.EventReq) error) (feishuWSClient, error) {
	handler := larkdispatcher.NewEventDispatcher("", "").
		OnCustomizedEvent("im.message.receive_v1", onEvent)
	client := larkws.NewClient(appID, appSecret,
		larkws.WithEventHandler(handler),
		larkws.WithLogLevel(larkcore.LogLevelInfo))
	return client, nil
}

type feishuPreviewState struct {
	draftMessageID   string
	toolMessageID    string
	replyToMessageID string
	lastDraftText    string
	lastEditAt       time.Time
	toolBlockIndex   int
	toolEntries      []toolEntry
	hadToolProgress  bool
	finalized        bool
}

type FeishuChannel struct {
	BaseChannel
	cfg           config.FeishuConfig
	client        FeishuClient
	wsClient      feishuWSClient
	cancel        context.CancelFunc
	clientFactory FeishuClientFactory
	wsFactory     FeishuWSFactory

	previewMu  sync.Mutex
	previewMsg map[string]feishuPreviewState
}

func NewFeishuChannel(cfg config.FeishuConfig, b *bus.MessageBus, logger sdklogger.Logger) (*FeishuChannel, error) {
	return NewFeishuChannelWithFactory(cfg, b, defaultFeishuClientFactory, logger)
}

func NewFeishuChannelWithFactory(cfg config.FeishuConfig, b *bus.MessageBus, factory FeishuClientFactory, logger sdklogger.Logger) (*FeishuChannel, error) {
	if cfg.AppID == "" || cfg.AppSecret == "" {
		return nil, fmt.Errorf("feishu app_id and app_secret are required")
	}
	ch := &FeishuChannel{
		BaseChannel:   NewBaseChannel(feishuChannelName, b, cfg.AllowFrom, logger),
		cfg:           cfg,
		clientFactory: factory,
		wsFactory:     defaultFeishuWSFactory,
		previewMsg:    make(map[string]feishuPreviewState),
	}
	return ch, nil
}

func (f *FeishuChannel) Start(ctx context.Context) error {
	f.client = f.clientFactory(f.cfg.AppID, f.cfg.AppSecret)
	wsClient, err := f.wsFactory(f.cfg.AppID, f.cfg.AppSecret, f.processEventReq)
	if err != nil {
		return fmt.Errorf("create feishu ws client: %w", err)
	}
	f.wsClient = wsClient
	ctx, f.cancel = context.WithCancel(ctx)

	go func() {
		f.logger.Infof("[feishu] long connection started")
		if err := f.wsClient.Start(ctx); err != nil && ctx.Err() == nil {
			f.logger.Errorf("[feishu] ws client exited: %v", err)
		}
	}()
	return nil
}

func (f *FeishuChannel) Stop() error {
	if f.cancel != nil {
		f.cancel()
	}
	f.logger.Infof("[feishu] stopped")
	return nil
}

func (f *FeishuChannel) processEventReq(ctx context.Context, req *larkevent.EventReq) error {
	if req == nil || len(req.Body) == 0 {
		return nil
	}
	var envelope struct {
		Header struct {
			EventType string `json:"event_type"`
		} `json:"header"`
		Event struct {
			Sender struct {
				SenderID struct {
					OpenID string `json:"open_id"`
				} `json:"sender_id"`
			} `json:"sender"`
			Message struct {
				MessageID   string `json:"message_id"`
				ChatID      string `json:"chat_id"`
				MessageType string `json:"message_type"`
				Content     string `json:"content"`
			} `json:"message"`
		} `json:"event"`
	}
	if err := json.Unmarshal(req.Body, &envelope); err != nil {
		return fmt.Errorf("parse feishu event body: %w", err)
	}
	if strings.TrimSpace(envelope.Header.EventType) != "im.message.receive_v1" {
		return nil
	}
	f.processInboundEvent(
		envelope.Event.Sender.SenderID.OpenID,
		envelope.Event.Message.ChatID,
		envelope.Event.Message.MessageID,
		envelope.Event.Message.MessageType,
		envelope.Event.Message.Content,
	)
	return nil
}

func (f *FeishuChannel) processInboundEvent(senderID, chatID, messageID, messageType, contentRaw string) {
	senderID = strings.TrimSpace(senderID)
	if senderID == "" || !f.IsAllowed(senderID) {
		return
	}
	messageType = strings.ToLower(strings.TrimSpace(messageType))
	content := ""
	var attachments []api.Attachment

	switch messageType {
	case "text":
		var textContent struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(contentRaw), &textContent); err != nil {
			f.logger.Errorf("[feishu] parse text content error: %v", err)
			return
		}
		content = strings.TrimSpace(textContent.Text)
	case "image", "file", "audio":
		if p, kind, mime, err := f.downloadInboundMedia(messageID, messageType, contentRaw); err == nil && p != "" {
			attachments = append(attachments, api.Attachment{
				FilePath: p,
				Type:     kind,
				MimeType: mime,
			})
		} else if err != nil {
			f.logger.Warnf("[feishu] download media failed: %v", err)
		}
	default:
		return
	}
	if content == "" && len(attachments) == 0 {
		return
	}
	meta := map[string]any{
		"message_type": messageType,
		"message_id":   strings.TrimSpace(messageID),
	}
	media := make([]string, 0, len(attachments))
	for _, att := range attachments {
		media = append(media, att.FilePath)
	}
	f.bus.Inbound <- bus.InboundMessage{
		Channel:     feishuChannelName,
		SenderID:    senderID,
		ChatID:      strings.TrimSpace(chatID),
		Content:     content,
		Media:       media,
		Attachments: attachments,
		Timestamp:   time.Now(),
		Metadata:    meta,
	}
}

func (f *FeishuChannel) Send(msg bus.OutboundMessage) error {
	if f.client == nil {
		return fmt.Errorf("feishu client not initialized")
	}
	rc, ok := f.client.(feishuAdvancedClient)
	if !ok {
		return f.client.SendMessage(context.Background(), msg.ChatID, msg.Content)
	}

	event := telegramEvent(msg.Metadata)
	if msg.Content != "" {
		switch event {
		case telegramEventPreviewUpdate:
			return f.sendPreview(msg.ChatID, msg.Content, "update", msg.ReplyTo, rc)
		case telegramEventPreviewFinal:
			return f.sendPreview(msg.ChatID, msg.Content, "final", msg.ReplyTo, rc)
		case telegramEventToolProgress:
			return f.sendToolProgress(msg.ChatID, msg, rc)
		case telegramEventUsageHUD:
			return f.sendStandaloneText(msg.ChatID, msg.Content, rc)
		}
	}

	attachments := msg.Attachments
	if len(attachments) == 0 && len(msg.Media) > 0 {
		for _, p := range msg.Media {
			attachments = append(attachments, api.Attachment{FilePath: p})
		}
	}
	for _, att := range attachments {
		if err := f.sendMediaPath(msg.ChatID, att.FilePath, att.Type, att.MimeType, rc); err != nil {
			f.logger.Warnf("[feishu] send media failed path=%s err=%v", att.FilePath, err)
		}
	}
	if strings.TrimSpace(msg.Content) == "" {
		return nil
	}
	return f.sendNewMessage(msg.ChatID, msg.Content, msg.ReplyTo, rc)
}

func (f *FeishuChannel) sendPreview(chatID, content, mode, replyTo string, rc feishuAdvancedClient) error {
	if mode == "final" {
		return f.finalizePreview(chatID, content, rc)
	}
	text := strings.TrimSpace(renderDraftText(content))
	if text == "" {
		return nil
	}
	state, err := f.ensureTurnState(chatID, replyTo, rc)
	if err != nil {
		return err
	}
	if state.lastDraftText == text {
		return nil
	}
	cardJSON := buildReplyCardJSON(text)
	if err := rc.EditCardMessage(context.Background(), state.draftMessageID, cardJSON); err != nil {
		newID, sendErr := rc.SendTypedMessage(context.Background(), chatID, "interactive", map[string]string{"card": cardJSON}, replyTo)
		if sendErr != nil {
			return fmt.Errorf("edit preview: %w; fallback send: %v", err, sendErr)
		}
		state.draftMessageID = newID
	}
	state.lastDraftText = text
	state.lastEditAt = time.Now()
	f.previewMu.Lock()
	f.previewMsg[chatID] = state
	f.previewMu.Unlock()
	return nil
}

func (f *FeishuChannel) finalizePreview(chatID, content string, rc feishuAdvancedClient) error {
	f.previewMu.Lock()
	state := f.previewMsg[chatID]
	f.previewMu.Unlock()
	if state.finalized && state.draftMessageID == "" {
		return nil
	}
	ctx := context.Background()
	const maxUTF16Len = 4090
	contents, err := telegramify.Telegramify(ctx, content, maxUTF16Len, false, nil)
	if err != nil {
		return fmt.Errorf("telegramify process: %w", err)
	}
	if len(contents) == 0 {
		return nil
	}
	if err := f.applyFinalContents(chatID, state, contents, rc); err != nil {
		return err
	}
	f.previewMu.Lock()
	cur := f.previewMsg[chatID]
	if !state.hadToolProgress {
		if state.toolMessageID != "" {
			if err := rc.DeleteMessage(context.Background(), state.toolMessageID); err != nil {
				f.logger.Warnf("[feishu] delete empty tool block failed: %v", err)
			}
		}
		cur.toolMessageID = ""
		cur.toolBlockIndex = 0
		cur.toolEntries = nil
		cur.hadToolProgress = false
	}
	cur.draftMessageID = ""
	cur.lastDraftText = ""
	cur.lastEditAt = time.Now()
	cur.finalized = true
	f.previewMsg[chatID] = cur
	f.previewMu.Unlock()
	return nil
}

func (f *FeishuChannel) applyFinalContents(chatID string, state feishuPreviewState, contents []telegramify.Content, rc feishuAdvancedClient) error {
	usedPreview := false
	for _, item := range contents {
		replyTo := ""
		if !usedPreview {
			replyTo = state.replyToMessageID
		}
		switch c := item.(type) {
		case *telegramify.Text:
			text := strings.TrimSpace(c.Text)
			if text == "" {
				continue
			}
			cardJSON := buildReplyCardJSON(text)
			if !usedPreview && state.draftMessageID != "" {
				if err := rc.EditCardMessage(context.Background(), state.draftMessageID, cardJSON); err == nil {
					usedPreview = true
					continue
				}
			}
			if _, err := rc.SendTypedMessage(context.Background(), chatID, "interactive", map[string]string{"card": cardJSON}, replyTo); err != nil {
				return err
			}
			usedPreview = true
		case *telegramify.File:
			fileKey, err := rc.UploadFile(context.Background(), c.FileName, c.FileData)
			if err != nil {
				return err
			}
			if _, err := rc.SendTypedMessage(context.Background(), chatID, "file", map[string]string{"file_key": fileKey}, replyTo); err != nil {
				return err
			}
			usedPreview = true
		case *telegramify.Photo:
			imageKey, err := rc.UploadImage(context.Background(), c.FileName, c.FileData)
			if err != nil {
				return err
			}
			if _, err := rc.SendTypedMessage(context.Background(), chatID, "image", map[string]string{"image_key": imageKey}, replyTo); err != nil {
				return err
			}
			usedPreview = true
		}
	}
	return nil
}

func (f *FeishuChannel) ensureTurnState(chatID, replyTo string, rc feishuAdvancedClient) (feishuPreviewState, error) {
	f.previewMu.Lock()
	state, ok := f.previewMsg[chatID]
	f.previewMu.Unlock()
	if ok && state.draftMessageID != "" && state.toolMessageID != "" {
		if state.replyToMessageID == "" && replyTo != "" {
			state.replyToMessageID = replyTo
			f.previewMu.Lock()
			f.previewMsg[chatID] = state
			f.previewMu.Unlock()
		}
		return state, nil
	}
	toolCard := buildToolCardJSON(formatFeishuToolBlock(1, nil))
	toolID, err := rc.SendTypedMessage(context.Background(), chatID, "interactive", map[string]string{"card": toolCard}, "")
	if err != nil {
		return feishuPreviewState{}, fmt.Errorf("send tool block: %w", err)
	}
	draftCard := buildReplyCardJSON("⌛ 正在生成回复...")
	draftID, err := rc.SendTypedMessage(context.Background(), chatID, "interactive", map[string]string{"card": draftCard}, replyTo)
	if err != nil {
		return feishuPreviewState{}, fmt.Errorf("send draft block: %w", err)
	}
	state = feishuPreviewState{
		draftMessageID:   draftID,
		toolMessageID:    toolID,
		replyToMessageID: replyTo,
		toolBlockIndex:   1,
	}
	f.previewMu.Lock()
	f.previewMsg[chatID] = state
	f.previewMu.Unlock()
	return state, nil
}

func (f *FeishuChannel) sendToolProgress(chatID string, msg bus.OutboundMessage, rc feishuAdvancedClient) error {
	state, err := f.ensureTurnState(chatID, "", rc)
	if err != nil {
		return err
	}
	entry := buildToolEntry(msg)
	if entry.Name == "" && strings.TrimSpace(entry.Raw) == "" {
		return nil
	}
	tryEntries := append(append([]toolEntry{}, state.toolEntries...), entry)
	block := formatFeishuToolBlock(state.toolBlockIndex, tryEntries)
	if len([]rune(block)) > maxToolBlockChars {
		state.toolBlockIndex++
		state.toolEntries = []toolEntry{entry}
		newBlock := buildToolCardJSON(formatFeishuToolBlock(state.toolBlockIndex, state.toolEntries))
		id, sendErr := rc.SendTypedMessage(context.Background(), chatID, "interactive", map[string]string{"card": newBlock}, "")
		if sendErr != nil {
			return fmt.Errorf("send tool block rollover: %w", sendErr)
		}
		state.toolMessageID = id
	} else {
		toolCard := buildToolCardJSON(block)
		if err := rc.EditCardMessage(context.Background(), state.toolMessageID, toolCard); err != nil {
			return fmt.Errorf("edit tool block: %w", err)
		}
		state.toolEntries = tryEntries
	}
	state.hadToolProgress = true
	f.previewMu.Lock()
	cur := f.previewMsg[chatID]
	cur.toolMessageID = state.toolMessageID
	cur.toolBlockIndex = state.toolBlockIndex
	cur.toolEntries = state.toolEntries
	cur.hadToolProgress = state.hadToolProgress
	f.previewMsg[chatID] = cur
	f.previewMu.Unlock()
	return nil
}

func (f *FeishuChannel) sendStandaloneText(chatID, text string, rc feishuAdvancedClient) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	_, err := rc.SendTypedMessage(context.Background(), chatID, "text", map[string]string{"text": text}, "")
	return err
}

func (f *FeishuChannel) sendNewMessage(chatID, content, replyTo string, rc feishuAdvancedClient) error {
	ctx := context.Background()
	const maxUTF16Len = 4090
	contents, err := telegramify.Telegramify(ctx, content, maxUTF16Len, false, nil)
	if err != nil {
		return fmt.Errorf("telegramify process: %w", err)
	}
	first := true
	for _, item := range contents {
		curReply := ""
		if first {
			curReply = replyTo
			first = false
		}
		switch c := item.(type) {
		case *telegramify.Text:
			text := strings.TrimSpace(c.Text)
			if text == "" {
				continue
			}
			cardJSON := buildReplyCardJSON(text)
			if _, err := rc.SendTypedMessage(ctx, chatID, "interactive", map[string]string{"card": cardJSON}, curReply); err != nil {
				return err
			}
		case *telegramify.File:
			key, err := rc.UploadFile(ctx, c.FileName, c.FileData)
			if err != nil {
				return err
			}
			if _, err := rc.SendTypedMessage(ctx, chatID, "file", map[string]string{"file_key": key}, curReply); err != nil {
				return err
			}
		case *telegramify.Photo:
			key, err := rc.UploadImage(ctx, c.FileName, c.FileData)
			if err != nil {
				return err
			}
			if _, err := rc.SendTypedMessage(ctx, chatID, "image", map[string]string{"image_key": key}, curReply); err != nil {
				return err
			}
		}
	}
	return nil
}

func buildReplyCardJSON(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		text = " "
	}
	return buildV2MarkdownCardJSON(text)
}

func buildToolCardJSON(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		text = " "
	}
	return buildV2MarkdownCardJSON(text)
}

func buildV2MarkdownCardJSON(text string) string {
	payload := map[string]any{
		"config": map[string]any{
			"wide_screen_mode": true,
		},
		"elements": []map[string]any{
			{
				"tag":     "markdown",
				"content": text,
			},
		},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		// Fallback to plain text when markdown payload build fails.
		fallback, _ := json.Marshal(map[string]any{
			"elements": []map[string]any{
				{"tag": "div", "text": map[string]string{"tag": "plain_text", "content": text}},
			},
		})
		return string(fallback)
	}
	return string(b)
}

func formatFeishuToolBlock(blockIndex int, entries []toolEntry) string {
	var b strings.Builder
	if blockIndex <= 1 {
		b.WriteString("🧰 Tool Calls")
	} else {
		b.WriteString(fmt.Sprintf("🧰 Tool Calls (续 %d)", blockIndex))
	}
	if len(entries) == 0 {
		b.WriteString("\n(等待工具调用)")
		return b.String()
	}
	for _, e := range entries {
		name := strings.TrimSpace(e.Name)
		if name == "" {
			name = "Tool"
		}
		payload := normalizeToolPayload(e)
		if payload == "" {
			payload = "{}"
		}
		lang := "json"
		if !json.Valid([]byte(payload)) {
			lang = "shell"
		} else {
			var pretty bytes.Buffer
			if err := json.Indent(&pretty, []byte(payload), "", "  "); err == nil {
				payload = pretty.String()
			}
		}
		b.WriteString("\n\n⏳ ")
		b.WriteString(name)
		b.WriteString("\n")
		b.WriteString("```")
		b.WriteString(lang)
		b.WriteString("\n")
		b.WriteString(payload)
		b.WriteString("\n```")
	}
	return strings.TrimSpace(b.String())
}

func (f *FeishuChannel) sendMediaPath(chatID, mediaPath, explicitType, explicitMIME string, rc feishuAdvancedClient) error {
	data, err := os.ReadFile(mediaPath)
	if err != nil {
		return fmt.Errorf("read media file: %w", err)
	}
	mime := strings.TrimSpace(explicitMIME)
	if mime == "" {
		mime = api.DetectAttachmentMIME(explicitType, mediaPath)
	}
	kind := strings.ToLower(strings.TrimSpace(explicitType))
	switch kind {
	case "image", "audio", "file":
	default:
		return fmt.Errorf("unsupported media type %q", explicitType)
	}
	name := filepath.Base(mediaPath)
	switch kind {
	case "image":
		key, err := rc.UploadImage(context.Background(), name, data)
		if err != nil {
			return fmt.Errorf("upload image: %w", err)
		}
		_, err = rc.SendTypedMessage(context.Background(), chatID, "image", map[string]string{"image_key": key}, "")
		return err
	case "audio":
		audioPath := mediaPath
		cleanupAudio := func() {}
		if converted, convErr := transcodeToFeishuOpus(mediaPath); convErr != nil {
			// Keep running with original path when local transcoding is unavailable.
			f.logger.Warnf("[feishu] audio->opus transcode skipped: %v", convErr)
		} else {
			audioPath = converted
			cleanupAudio = func() { _ = os.Remove(converted) }
		}
		audioData, readErr := os.ReadFile(audioPath)
		if readErr != nil {
			cleanupAudio()
			return fmt.Errorf("read audio file: %w", readErr)
		}
		audioName := filepath.Base(audioPath)
		durationMillis := detectAudioDurationMillis(audioPath)
		key, err := rc.UploadAudio(context.Background(), audioName, audioData, durationMillis)
		if err != nil {
			cleanupAudio()
			return fmt.Errorf("upload audio file: %w", err)
		}
		audioContent := map[string]string{"file_key": key}
		_, err = rc.SendTypedMessage(context.Background(), chatID, "audio", audioContent, "")
		cleanupAudio()
		if err == nil {
			return nil
		}
		_, err2 := rc.SendTypedMessage(context.Background(), chatID, "file", map[string]string{"file_key": key}, "")
		if err2 != nil {
			return fmt.Errorf("send audio/file fallback: %w / %v", err, err2)
		}
		return nil
	default:
		key, err := rc.UploadFile(context.Background(), name, data)
		if err != nil {
			return fmt.Errorf("upload file: %w", err)
		}
		_, err = rc.SendTypedMessage(context.Background(), chatID, "file", map[string]string{"file_key": key}, "")
		return err
	}
}

func (f *FeishuChannel) downloadInboundMedia(messageID, messageType, contentRaw string) (string, string, string, error) {
	rc, ok := f.client.(feishuAdvancedClient)
	if !ok {
		return "", "", "", fmt.Errorf("advanced feishu client unavailable")
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(contentRaw), &payload); err != nil {
		return "", "", "", fmt.Errorf("parse media content: %w", err)
	}
	fileKey := ""
	resourceType := "file"
	explicitKind := ""
	switch strings.ToLower(strings.TrimSpace(messageType)) {
	case "image":
		if v, ok := payload["image_key"].(string); ok {
			fileKey = strings.TrimSpace(v)
		}
		resourceType = "image"
		explicitKind = "image"
	case "audio":
		if v, ok := payload["file_key"].(string); ok {
			fileKey = strings.TrimSpace(v)
		}
		resourceType = "file"
		explicitKind = "audio"
	default:
		if v, ok := payload["file_key"].(string); ok {
			fileKey = strings.TrimSpace(v)
		}
		resourceType = "file"
		explicitKind = "file"
	}
	if fileKey == "" {
		return "", "", "", fmt.Errorf("empty media key")
	}
	data, contentType, err := rc.DownloadResource(context.Background(), messageID, fileKey, resourceType)
	if err != nil {
		return "", "", "", err
	}
	tempDir := filepath.Join(os.TempDir(), "aevitas-feishu-media")
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		return "", "", "", fmt.Errorf("create temp dir: %w", err)
	}
	filename := fmt.Sprintf("media-%d.bin", time.Now().UnixNano())
	localPath := filepath.Join(tempDir, filename)
	if err := os.WriteFile(localPath, data, 0644); err != nil {
		return "", "", "", fmt.Errorf("save media: %w", err)
	}
	mime := strings.TrimSpace(contentType)
	if mime == "" {
		mime = api.DetectAttachmentMIME(explicitKind, localPath)
	}
	return localPath, explicitKind, mime, nil
}

func encodeFeishuContent(msgType string, content map[string]string) (string, error) {
	msgType = strings.ToLower(strings.TrimSpace(msgType))
	switch msgType {
	case "interactive":
		card := strings.TrimSpace(content["card"])
		if card == "" {
			return "", fmt.Errorf("interactive content missing card")
		}
		if !json.Valid([]byte(card)) {
			return "", fmt.Errorf("interactive card is not valid json")
		}
		return card, nil
	case "text":
		b, err := json.Marshal(map[string]string{"text": content["text"]})
		if err != nil {
			return "", fmt.Errorf("marshal text content: %w", err)
		}
		return string(b), nil
	case "image":
		b, err := json.Marshal(map[string]string{"image_key": content["image_key"]})
		if err != nil {
			return "", fmt.Errorf("marshal image content: %w", err)
		}
		return string(b), nil
	case "file":
		b, err := json.Marshal(map[string]string{"file_key": content["file_key"]})
		if err != nil {
			return "", fmt.Errorf("marshal file/audio content: %w", err)
		}
		return string(b), nil
	case "audio":
		b, err := json.Marshal(map[string]string{"file_key": content["file_key"]})
		if err != nil {
			return "", fmt.Errorf("marshal audio content: %w", err)
		}
		return string(b), nil
	default:
		b, err := json.Marshal(content)
		if err != nil {
			return "", fmt.Errorf("marshal generic content: %w", err)
		}
		return string(b), nil
	}
}
