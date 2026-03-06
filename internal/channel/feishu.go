package channel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcard "github.com/larksuite/oapi-sdk-go/v3/card"
	larkauth "github.com/larksuite/oapi-sdk-go/v3/service/auth/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkdispatcher "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
	"github.com/riverfjs/agentsdk-go/pkg/api"
	sdklogger "github.com/riverfjs/agentsdk-go/pkg/logger"
	telegramify "github.com/riverfjs/telegramify-go"
	"github.com/riverfjs/aevitas/internal/bus"
	"github.com/riverfjs/aevitas/internal/config"
	"github.com/tidwall/gjson"
)

const feishuChannelName = "feishu"

type FeishuClient interface {
	SendMessage(ctx context.Context, chatID, content string) error
	GetTenantAccessToken(ctx context.Context) (string, error)
}

type feishuAdvancedClient interface {
	FeishuClient
	SendTypedMessage(ctx context.Context, chatID, msgType, content string, replyTo string) (string, error)
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

type FeishuWSFactory func(
	appID, appSecret string,
	onEvent func(context.Context, *larkim.P2MessageReceiveV1) error,
	onCardAction func(context.Context, *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error),
) (feishuWSClient, error)

type defaultFeishuClient struct {
	appID     string
	appSecret string
	sdk       *lark.Client
}

func buildFeishuTextContent(text string) (string, error) {
	return larkim.NewTextMsgBuilder().Text(text).Build(), nil
}

func buildFeishuInteractiveContent(cardJSON string) (string, error) {
	cardJSON = strings.TrimSpace(cardJSON)
	if cardJSON == "" {
		return "", fmt.Errorf("interactive content missing card")
	}
	if !json.Valid([]byte(cardJSON)) {
		return "", fmt.Errorf("interactive card is not valid json")
	}
	return cardJSON, nil
}

func buildFeishuImageContent(imageKey string) (string, error) {
	body, err := (&larkim.MessageImage{ImageKey: strings.TrimSpace(imageKey)}).String()
	if err != nil {
		return "", fmt.Errorf("marshal image content: %w", err)
	}
	return body, nil
}

func buildFeishuFileContent(fileKey string) (string, error) {
	body, err := (&larkim.MessageFile{FileKey: strings.TrimSpace(fileKey)}).String()
	if err != nil {
		return "", fmt.Errorf("marshal file content: %w", err)
	}
	return body, nil
}

func buildFeishuAudioContent(fileKey string) (string, error) {
	body, err := (&larkim.MessageAudio{FileKey: strings.TrimSpace(fileKey)}).String()
	if err != nil {
		return "", fmt.Errorf("marshal audio content: %w", err)
	}
	return body, nil
}

func (c *defaultFeishuClient) GetTenantAccessToken(ctx context.Context) (string, error) {
	req := larkauth.NewInternalTenantAccessTokenReqBuilder().
		Body(
			larkauth.NewInternalTenantAccessTokenReqBodyBuilder().
				AppId(c.appID).
				AppSecret(c.appSecret).
				Build(),
		).
		Build()
	resp, err := c.sdk.Auth.V3.TenantAccessToken.Internal(ctx, req)
	if err != nil {
		return "", fmt.Errorf("get tenant token: %w", err)
	}
	if !resp.Success() {
		return "", fmt.Errorf("feishu token error: %w", resp.CodeError)
	}

	if resp.ApiResp == nil {
		return "", fmt.Errorf("feishu token response missing raw body")
	}
	token := strings.TrimSpace(gjson.GetBytes(resp.ApiResp.RawBody, "tenant_access_token").String())
	if token == "" {
		return "", fmt.Errorf("feishu token response missing tenant_access_token")
	}
	return token, nil
}

func (c *defaultFeishuClient) SendMessage(ctx context.Context, chatID, content string) error {
	textContent, err := buildFeishuTextContent(content)
	if err != nil {
		return err
	}
	_, err = c.SendTypedMessage(ctx, chatID, larkim.MsgTypeText, textContent, "")
	return err
}

func (c *defaultFeishuClient) SendTypedMessage(ctx context.Context, chatID, msgType, content string, replyTo string) (string, error) {
	msgType = strings.TrimSpace(msgType)
	content = strings.TrimSpace(content)
	if msgType == "" {
		return "", fmt.Errorf("empty feishu message type")
	}
	if content == "" {
		return "", fmt.Errorf("empty feishu message content")
	}
	if replyTo != "" {
		resp, err := c.sdk.Im.V1.Message.Reply(
			ctx,
			larkim.NewReplyMessageReqBuilder().
				MessageId(strings.TrimSpace(replyTo)).
				Body(
					larkim.NewReplyMessageReqBodyBuilder().
						Content(content).
						MsgType(msgType).
						Build(),
				).
				Build(),
		)
		if err != nil {
			return "", fmt.Errorf("reply feishu message type=%s reply_to=%s: %w", msgType, strings.TrimSpace(replyTo), err)
		}
		if !resp.Success() {
			return "", fmt.Errorf("reply feishu message type=%s reply_to=%s: %w", msgType, strings.TrimSpace(replyTo), resp.CodeError)
		}
		if resp.Data == nil {
			return "", nil
		}
		return stringPtr(resp.Data.MessageId), nil
	}

	resp, err := c.sdk.Im.V1.Message.Create(
		ctx,
		larkim.NewCreateMessageReqBuilder().
			ReceiveIdType(larkim.ReceiveIdTypeChatId).
			Body(
				larkim.NewCreateMessageReqBodyBuilder().
					ReceiveId(strings.TrimSpace(chatID)).
					MsgType(msgType).
					Content(content).
					Build(),
			).
			Build(),
	)
	if err != nil {
		return "", fmt.Errorf("send feishu message type=%s chat_id=%s: %w", msgType, chatID, err)
	}
	if !resp.Success() {
		return "", fmt.Errorf("send feishu message type=%s chat_id=%s: %w", msgType, chatID, resp.CodeError)
	}
	if resp.Data == nil {
		return "", nil
	}
	return stringPtr(resp.Data.MessageId), nil
}

func (c *defaultFeishuClient) EditTextMessage(ctx context.Context, messageID, text string) error {
	contentJSON, err := buildFeishuTextContent(text)
	if err != nil {
		return fmt.Errorf("marshal edit content: %w", err)
	}
	resp, err := c.sdk.Im.V1.Message.Update(
		ctx,
		larkim.NewUpdateMessageReqBuilder().
			MessageId(strings.TrimSpace(messageID)).
			Body(
				larkim.NewUpdateMessageReqBodyBuilder().
						MsgType(larkim.MsgTypeText).
						Content(contentJSON).
					Build(),
			).
			Build(),
	)
	if err != nil {
		return fmt.Errorf("edit text message: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("edit text message: %w", resp.CodeError)
	}
	return nil
}

func (c *defaultFeishuClient) EditCardMessage(ctx context.Context, messageID, cardJSON string) error {
	cardJSON = strings.TrimSpace(cardJSON)
	if cardJSON == "" {
		return fmt.Errorf("empty card content")
	}
	if !json.Valid([]byte(cardJSON)) {
		return fmt.Errorf("invalid card json content")
	}
	resp, err := c.sdk.Im.V1.Message.Patch(
		ctx,
		larkim.NewPatchMessageReqBuilder().
			MessageId(strings.TrimSpace(messageID)).
			Body(
				larkim.NewPatchMessageReqBodyBuilder().
					Content(cardJSON).
					Build(),
			).
			Build(),
	)
	if err != nil {
		return fmt.Errorf("edit card message: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("edit card message: %w", resp.CodeError)
	}
	return nil
}

func (c *defaultFeishuClient) DeleteMessage(ctx context.Context, messageID string) error {
	resp, err := c.sdk.Im.V1.Message.Delete(
		ctx,
		larkim.NewDeleteMessageReqBuilder().
			MessageId(strings.TrimSpace(messageID)).
			Build(),
	)
	if err != nil {
		return fmt.Errorf("delete message: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("delete message: %w", resp.CodeError)
	}
	return nil
}

func (c *defaultFeishuClient) UploadImage(ctx context.Context, fileName string, data []byte) (string, error) {
	resp, err := c.sdk.Im.V1.Image.Create(
		ctx,
		larkim.NewCreateImageReqBuilder().
			Body(
				larkim.NewCreateImageReqBodyBuilder().
					ImageType(larkim.ImageTypeMessage).
					Image(bytes.NewReader(data)).
					Build(),
			).
			Build(),
	)
	if err != nil {
		return "", fmt.Errorf("upload image: %w", err)
	}
	if !resp.Success() {
		return "", fmt.Errorf("upload image: %w", resp.CodeError)
	}
	imageKey := ""
	if resp.Data != nil {
		imageKey = stringPtr(resp.Data.ImageKey)
	}
	if imageKey == "" {
		return "", fmt.Errorf("empty image_key from feishu upload")
	}
	return imageKey, nil
}

func (c *defaultFeishuClient) UploadFile(ctx context.Context, fileName string, data []byte) (string, error) {
	resp, err := c.sdk.Im.V1.File.Create(
		ctx,
		larkim.NewCreateFileReqBuilder().
			Body(
				larkim.NewCreateFileReqBodyBuilder().
					FileType(larkim.FileTypeStream).
					FileName(strings.TrimSpace(fileName)).
					File(bytes.NewReader(data)).
					Build(),
			).
			Build(),
	)
	if err != nil {
		return "", fmt.Errorf("upload file: %w", err)
	}
	if !resp.Success() {
		return "", fmt.Errorf("upload file: %w", resp.CodeError)
	}
	fileKey := ""
	if resp.Data != nil {
		fileKey = stringPtr(resp.Data.FileKey)
	}
	if fileKey == "" {
		return "", fmt.Errorf("empty file_key from feishu upload")
	}
	return fileKey, nil
}

func (c *defaultFeishuClient) UploadAudio(ctx context.Context, fileName string, data []byte, durationMillis int) (string, error) {
	if durationMillis <= 0 {
		durationMillis = audioDurationFallbackMillis
	}
	resp, err := c.sdk.Im.V1.File.Create(
		ctx,
		larkim.NewCreateFileReqBuilder().
			Body(
				larkim.NewCreateFileReqBodyBuilder().
					FileType(larkim.FileTypeOpus).
					FileName(strings.TrimSpace(fileName)).
					Duration(durationMillis).
					File(bytes.NewReader(data)).
					Build(),
			).
			Build(),
	)
	if err != nil {
		return "", fmt.Errorf("upload audio: %w", err)
	}
	if !resp.Success() {
		return "", fmt.Errorf("upload audio: %w", resp.CodeError)
	}
	fileKey := ""
	if resp.Data != nil {
		fileKey = stringPtr(resp.Data.FileKey)
	}
	if fileKey == "" {
		return "", fmt.Errorf("empty file_key from feishu audio upload")
	}
	return fileKey, nil
}

func (c *defaultFeishuClient) DownloadResource(ctx context.Context, messageID, fileKey, resourceType string) ([]byte, string, error) {
	resourceType = strings.TrimSpace(resourceType)
	if resourceType == "" {
		resourceType = "file"
	}
	resp, err := c.sdk.Im.V1.MessageResource.Get(
		ctx,
		larkim.NewGetMessageResourceReqBuilder().
			MessageId(strings.TrimSpace(messageID)).
			FileKey(strings.TrimSpace(fileKey)).
			Type(resourceType).
			Build(),
	)
	if err != nil {
		return nil, "", fmt.Errorf("download resource: %w", err)
	}
	if !resp.Success() {
		return nil, "", fmt.Errorf("download resource: %w", resp.CodeError)
	}
	data, err := io.ReadAll(resp.File)
	if err != nil {
		return nil, "", fmt.Errorf("read resource body: %w", err)
	}
	contentType := ""
	if resp.ApiResp != nil && resp.ApiResp.Header != nil {
		contentType = strings.TrimSpace(resp.ApiResp.Header.Get("Content-Type"))
	}
	return data, contentType, nil
}

func stringPtr(v *string) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(*v)
}

type FeishuClientFactory func(appID, appSecret string) FeishuClient

var defaultFeishuClientFactory FeishuClientFactory = func(appID, appSecret string) FeishuClient {
	return &defaultFeishuClient{
		appID:     appID,
		appSecret: appSecret,
		sdk:       lark.NewClient(appID, appSecret, lark.WithAppType(larkcore.AppTypeSelfBuilt)),
	}
}

func defaultFeishuWSFactory(
	appID, appSecret string,
	onEvent func(context.Context, *larkim.P2MessageReceiveV1) error,
	onCardAction func(context.Context, *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error),
) (feishuWSClient, error) {
	handler := larkdispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(onEvent).
		OnP2CardActionTrigger(onCardAction)
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
	wsClient, err := f.wsFactory(f.cfg.AppID, f.cfg.AppSecret, f.processMessageReceiveEvent, f.processCardActionEvent)
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

func (f *FeishuChannel) processMessageReceiveEvent(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
	if event == nil || event.Event == nil || event.Event.Message == nil {
		return nil
	}
	senderID := ""
	if event.Event.Sender != nil && event.Event.Sender.SenderId != nil {
		senderID = stringPtr(event.Event.Sender.SenderId.OpenId)
	}
	f.processInboundEvent(
		senderID,
		stringPtr(event.Event.Message.ChatId),
		stringPtr(event.Event.Message.MessageId),
		stringPtr(event.Event.Message.MessageType),
		stringPtr(event.Event.Message.Content),
	)
	return nil
}

func (f *FeishuChannel) processEventReq(ctx context.Context, body []byte) error {
	if len(body) == 0 {
		return nil
	}
	var event larkim.P2MessageReceiveV1
	if err := json.Unmarshal(body, &event); err != nil {
		return fmt.Errorf("parse feishu event body: %w", err)
	}
	return f.processMessageReceiveEvent(ctx, &event)
}

func (f *FeishuChannel) processCardActionEvent(_ context.Context, event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
	if event == nil || event.Event == nil || event.Event.Action == nil || event.Event.Context == nil {
		return &callback.CardActionTriggerResponse{}, nil
	}
	action := ""
	approvalID := ""
	if event.Event.Action.Value != nil {
		if v, ok := event.Event.Action.Value["approval_action"].(string); ok {
			action = strings.ToLower(strings.TrimSpace(v))
		}
		if v, ok := event.Event.Action.Value["approval_id"].(string); ok {
			approvalID = strings.TrimSpace(v)
		}
	}
	if action != "allow" && action != "deny" {
		return &callback.CardActionTriggerResponse{}, nil
	}
	chatID := strings.TrimSpace(event.Event.Context.OpenChatID)
	if chatID == "" {
		return &callback.CardActionTriggerResponse{}, nil
	}
	senderID := ""
	if event.Event.Operator != nil {
		senderID = strings.TrimSpace(event.Event.Operator.OpenID)
	}
	if senderID == "" || !f.IsAllowed(senderID) {
		return &callback.CardActionTriggerResponse{}, nil
	}
	if rc, ok := f.client.(feishuAdvancedClient); ok {
		if messageID := strings.TrimSpace(event.Event.Context.OpenMessageID); messageID != "" {
			_ = rc.DeleteMessage(context.Background(), messageID)
		}
	}
	f.bus.Inbound <- bus.InboundMessage{
		Channel:   feishuChannelName,
		SenderID:  senderID,
		ChatID:    chatID,
		Content:   "",
		Timestamp: time.Now(),
		Metadata: map[string]any{
			"approval_action": action,
			"approval_id":     approvalID,
		},
	}
	return &callback.CardActionTriggerResponse{}, nil
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
	case larkim.MsgTypeText:
		var textContent struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(contentRaw), &textContent); err != nil {
			f.logger.Errorf("[feishu] parse text content error: %v", err)
			return
		}
		content = strings.TrimSpace(textContent.Text)
	case larkim.MsgTypeImage, larkim.MsgTypeFile, larkim.MsgTypeAudio:
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
	if msg.Metadata != nil {
		if isApproval, _ := msg.Metadata["approval_prompt"].(bool); isApproval {
			if strings.TrimSpace(msg.Content) != "" {
				approvalID, _ := msg.Metadata["approval_id"].(string)
				card := buildApprovalCardJSON(msg.Content, approvalID)
				content, err := buildFeishuInteractiveContent(card)
				if err != nil {
					return err
				}
				_, err = rc.SendTypedMessage(context.Background(), msg.ChatID, larkim.MsgTypeInteractive, content, msg.ReplyTo)
				return err
			}
		}
	}
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

func buildApprovalCardJSON(text, approvalID string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		text = "命令需要审批。"
	}
	approvalID = strings.TrimSpace(approvalID)
	allowBtn := larkcard.NewMessageCardEmbedButton().
		Text(larkcard.NewMessageCardPlainText().Content("允许").Build()).
		Type(larkcard.MessageCardButtonTypePrimary).
		Value(map[string]interface{}{
			"approval_action": "allow",
			"approval_id":     approvalID,
		}).Build()
	denyBtn := larkcard.NewMessageCardEmbedButton().
		Text(larkcard.NewMessageCardPlainText().Content("拒绝").Build()).
		Type(larkcard.MessageCardButtonTypeDanger).
		Value(map[string]interface{}{
			"approval_action": "deny",
			"approval_id":     approvalID,
		}).Build()
	card := larkcard.NewMessageCard().
		Config(larkcard.NewMessageCardConfig().WideScreenMode(true).Build()).
		Elements([]larkcard.MessageCardElement{
			larkcard.NewMessageCardMarkdown().Content(text).Build(),
			larkcard.NewMessageCardAction().Actions([]larkcard.MessageCardActionElement{allowBtn, denyBtn}).Build(),
		}).Build()
	raw, err := card.JSON()
	if err != nil {
		return buildV2MarkdownCardJSON(text)
	}
	return raw
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
		cardContent, contentErr := buildFeishuInteractiveContent(cardJSON)
		if contentErr != nil {
			return contentErr
		}
		newID, sendErr := rc.SendTypedMessage(context.Background(), chatID, larkim.MsgTypeInteractive, cardContent, replyTo)
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
			cardContent, err := buildFeishuInteractiveContent(cardJSON)
			if err != nil {
				return err
			}
			if _, err := rc.SendTypedMessage(context.Background(), chatID, larkim.MsgTypeInteractive, cardContent, replyTo); err != nil {
				return err
			}
			usedPreview = true
		case *telegramify.File:
			fileKey, err := rc.UploadFile(context.Background(), c.FileName, c.FileData)
			if err != nil {
				return err
			}
			fileContent, err := buildFeishuFileContent(fileKey)
			if err != nil {
				return err
			}
			if _, err := rc.SendTypedMessage(context.Background(), chatID, larkim.MsgTypeFile, fileContent, replyTo); err != nil {
				return err
			}
			usedPreview = true
		case *telegramify.Photo:
			imageKey, err := rc.UploadImage(context.Background(), c.FileName, c.FileData)
			if err != nil {
				return err
			}
			imageContent, err := buildFeishuImageContent(imageKey)
			if err != nil {
				return err
			}
			if _, err := rc.SendTypedMessage(context.Background(), chatID, larkim.MsgTypeImage, imageContent, replyTo); err != nil {
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
	toolContent, err := buildFeishuInteractiveContent(toolCard)
	if err != nil {
		return feishuPreviewState{}, err
	}
	toolID, err := rc.SendTypedMessage(context.Background(), chatID, larkim.MsgTypeInteractive, toolContent, "")
	if err != nil {
		return feishuPreviewState{}, fmt.Errorf("send tool block: %w", err)
	}
	draftCard := buildReplyCardJSON("⌛ 正在生成回复...")
	draftContent, err := buildFeishuInteractiveContent(draftCard)
	if err != nil {
		return feishuPreviewState{}, err
	}
	draftID, err := rc.SendTypedMessage(context.Background(), chatID, larkim.MsgTypeInteractive, draftContent, replyTo)
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
		blockContent, contentErr := buildFeishuInteractiveContent(newBlock)
		if contentErr != nil {
			return contentErr
		}
		id, sendErr := rc.SendTypedMessage(context.Background(), chatID, larkim.MsgTypeInteractive, blockContent, "")
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
	content, err := buildFeishuTextContent(text)
	if err != nil {
		return err
	}
	_, err = rc.SendTypedMessage(context.Background(), chatID, larkim.MsgTypeText, content, "")
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
			cardContent, err := buildFeishuInteractiveContent(cardJSON)
			if err != nil {
				return err
			}
			if _, err := rc.SendTypedMessage(ctx, chatID, larkim.MsgTypeInteractive, cardContent, curReply); err != nil {
				return err
			}
		case *telegramify.File:
			key, err := rc.UploadFile(ctx, c.FileName, c.FileData)
			if err != nil {
				return err
			}
			fileContent, err := buildFeishuFileContent(key)
			if err != nil {
				return err
			}
			if _, err := rc.SendTypedMessage(ctx, chatID, larkim.MsgTypeFile, fileContent, curReply); err != nil {
				return err
			}
		case *telegramify.Photo:
			key, err := rc.UploadImage(ctx, c.FileName, c.FileData)
			if err != nil {
				return err
			}
			imageContent, err := buildFeishuImageContent(key)
			if err != nil {
				return err
			}
			if _, err := rc.SendTypedMessage(ctx, chatID, larkim.MsgTypeImage, imageContent, curReply); err != nil {
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
	type feishuCardConfig struct {
		WideScreenMode bool `json:"wide_screen_mode"`
	}
	type feishuCardPlainText struct {
		Tag     string `json:"tag"`
		Content string `json:"content"`
	}
	type feishuCardElement struct {
		Tag     string               `json:"tag"`
		Content string               `json:"content,omitempty"`
		Text    *feishuCardPlainText `json:"text,omitempty"`
	}
	type feishuCardPayload struct {
		Config   feishuCardConfig   `json:"config,omitempty"`
		Elements []feishuCardElement `json:"elements"`
	}
	payload := feishuCardPayload{
		Config: feishuCardConfig{WideScreenMode: true},
		Elements: []feishuCardElement{
			{
				Tag:     "markdown",
				Content: text,
			},
		},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		// Fallback to plain text when markdown payload build fails.
		fallback, _ := json.Marshal(feishuCardPayload{
			Elements: []feishuCardElement{
				{
					Tag: "div",
					Text: &feishuCardPlainText{
						Tag:     "plain_text",
						Content: text,
					},
				},
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
	case larkim.MsgTypeImage:
		key, err := rc.UploadImage(context.Background(), name, data)
		if err != nil {
			return fmt.Errorf("upload image: %w", err)
		}
		imageContent, err := buildFeishuImageContent(key)
		if err != nil {
			return err
		}
		_, err = rc.SendTypedMessage(context.Background(), chatID, larkim.MsgTypeImage, imageContent, "")
		return err
	case larkim.MsgTypeAudio:
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
		audioContent, err := buildFeishuAudioContent(key)
		if err != nil {
			cleanupAudio()
			return err
		}
		_, err = rc.SendTypedMessage(context.Background(), chatID, larkim.MsgTypeAudio, audioContent, "")
		cleanupAudio()
		if err == nil {
			return nil
		}
		fileContent, contentErr := buildFeishuFileContent(key)
		if contentErr != nil {
			return contentErr
		}
		_, err2 := rc.SendTypedMessage(context.Background(), chatID, larkim.MsgTypeFile, fileContent, "")
		if err2 != nil {
			return fmt.Errorf("send audio/file fallback: %w / %v", err, err2)
		}
		return nil
	default:
		key, err := rc.UploadFile(context.Background(), name, data)
		if err != nil {
			return fmt.Errorf("upload file: %w", err)
		}
		fileContent, err := buildFeishuFileContent(key)
		if err != nil {
			return err
		}
		_, err = rc.SendTypedMessage(context.Background(), chatID, larkim.MsgTypeFile, fileContent, "")
		return err
	}
}

func (f *FeishuChannel) downloadInboundMedia(messageID, messageType, contentRaw string) (string, string, string, error) {
	rc, ok := f.client.(feishuAdvancedClient)
	if !ok {
		return "", "", "", fmt.Errorf("advanced feishu client unavailable")
	}
	fileKey := ""
	resourceType := larkim.MsgTypeFile
	explicitKind := ""
	switch strings.ToLower(strings.TrimSpace(messageType)) {
	case larkim.MsgTypeImage:
		var payload larkim.MessageImage
		if err := json.Unmarshal([]byte(contentRaw), &payload); err != nil {
			return "", "", "", fmt.Errorf("parse media content: %w", err)
		}
		fileKey = strings.TrimSpace(payload.ImageKey)
		resourceType = larkim.MsgTypeImage
		explicitKind = larkim.MsgTypeImage
	case larkim.MsgTypeAudio:
		var payload larkim.MessageAudio
		if err := json.Unmarshal([]byte(contentRaw), &payload); err != nil {
			return "", "", "", fmt.Errorf("parse media content: %w", err)
		}
		fileKey = strings.TrimSpace(payload.FileKey)
		explicitKind = larkim.MsgTypeAudio
	default:
		var payload larkim.MessageFile
		if err := json.Unmarshal([]byte(contentRaw), &payload); err != nil {
			return "", "", "", fmt.Errorf("parse media content: %w", err)
		}
		fileKey = strings.TrimSpace(payload.FileKey)
		explicitKind = larkim.MsgTypeFile
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

