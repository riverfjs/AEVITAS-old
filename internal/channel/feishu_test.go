package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	"github.com/riverfjs/agentsdk-go/pkg/api"
	sdklogger "github.com/riverfjs/agentsdk-go/pkg/logger"
	"github.com/riverfjs/aevitas/internal/bus"
	"github.com/riverfjs/aevitas/internal/config"
)

type mockFeishuAdvancedClient struct {
	sendErr error
	audioUploadCount int
	lastAudioUploadDuration int

	typedMessages []struct {
		chatID  string
		msgType string
		content map[string]string
		replyTo string
	}
	editedTexts []struct {
		messageID string
		text      string
	}
	editedCards []struct {
		messageID string
		cardJSON  string
	}
	deletedMessageIDs []string

	downloadData []byte
	downloadMIME string
}

func (m *mockFeishuAdvancedClient) SendMessage(ctx context.Context, chatID, content string) error {
	_, err := m.SendTypedMessage(ctx, chatID, "text", map[string]string{"text": content}, "")
	return err
}

func (m *mockFeishuAdvancedClient) GetTenantAccessToken(ctx context.Context) (string, error) {
	return "mock-token", nil
}

func (m *mockFeishuAdvancedClient) SendTypedMessage(ctx context.Context, chatID, msgType string, content map[string]string, replyTo string) (string, error) {
	cp := map[string]string{}
	for k, v := range content {
		cp[k] = v
	}
	m.typedMessages = append(m.typedMessages, struct {
		chatID  string
		msgType string
		content map[string]string
		replyTo string
	}{chatID: chatID, msgType: msgType, content: cp, replyTo: replyTo})
	if m.sendErr != nil {
		return "", m.sendErr
	}
	return fmt.Sprintf("om_%d", len(m.typedMessages)), nil
}

func (m *mockFeishuAdvancedClient) EditTextMessage(ctx context.Context, messageID, text string) error {
	m.editedTexts = append(m.editedTexts, struct {
		messageID string
		text      string
	}{messageID: messageID, text: text})
	return nil
}

func (m *mockFeishuAdvancedClient) EditCardMessage(ctx context.Context, messageID, cardJSON string) error {
	m.editedCards = append(m.editedCards, struct {
		messageID string
		cardJSON  string
	}{messageID: messageID, cardJSON: cardJSON})
	return nil
}

func (m *mockFeishuAdvancedClient) DeleteMessage(ctx context.Context, messageID string) error {
	m.deletedMessageIDs = append(m.deletedMessageIDs, messageID)
	return nil
}

func (m *mockFeishuAdvancedClient) UploadImage(ctx context.Context, fileName string, data []byte) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("empty image data")
	}
	return "img_key_mock", nil
}

func (m *mockFeishuAdvancedClient) UploadFile(ctx context.Context, fileName string, data []byte) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("empty file data")
	}
	return "file_key_mock", nil
}

func (m *mockFeishuAdvancedClient) UploadAudio(ctx context.Context, fileName string, data []byte, durationMillis int) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("empty audio data")
	}
	m.audioUploadCount++
	m.lastAudioUploadDuration = durationMillis
	return "audio_key_mock", nil
}

func (m *mockFeishuAdvancedClient) DownloadResource(ctx context.Context, messageID, fileKey, resourceType string) ([]byte, string, error) {
	data := m.downloadData
	if len(data) == 0 {
		data = []byte{0x89, 0x50, 0x4E, 0x47}
	}
	return data, m.downloadMIME, nil
}

type mockFeishuWSClient struct {
	started chan struct{}
}

func (m *mockFeishuWSClient) Start(ctx context.Context) error {
	select {
	case <-m.started:
	default:
		close(m.started)
	}
	<-ctx.Done()
	return nil
}

func newFeishuWithMocks(t *testing.T) (*FeishuChannel, *bus.MessageBus, *mockFeishuAdvancedClient) {
	t.Helper()
	b := bus.NewMessageBus(20)
	mockClient := &mockFeishuAdvancedClient{}
	ch, err := NewFeishuChannelWithFactory(config.FeishuConfig{
		AppID: "cli_test", AppSecret: "secret",
	}, b, func(appID, appSecret string) FeishuClient { return mockClient }, sdklogger.NewDefault())
	if err != nil {
		t.Fatalf("new feishu channel: %v", err)
	}
	ch.client = mockClient
	return ch, b, mockClient
}

func TestNewFeishuChannel_Valid(t *testing.T) {
	b := bus.NewMessageBus(10)
	ch, err := NewFeishuChannel(config.FeishuConfig{
		AppID: "cli_test", AppSecret: "secret",
	}, b, sdklogger.NewDefault())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ch.Name() != "feishu" {
		t.Fatalf("name = %q, want feishu", ch.Name())
	}
}

func TestNewFeishuChannel_MissingCredential(t *testing.T) {
	b := bus.NewMessageBus(10)
	if _, err := NewFeishuChannel(config.FeishuConfig{AppSecret: "x"}, b, sdklogger.NewDefault()); err == nil {
		t.Fatal("expected error when appId missing")
	}
	if _, err := NewFeishuChannel(config.FeishuConfig{AppID: "x"}, b, sdklogger.NewDefault()); err == nil {
		t.Fatal("expected error when appSecret missing")
	}
}

func TestFeishuChannel_StartStop_LongConnection(t *testing.T) {
	b := bus.NewMessageBus(10)
	mockClient := &mockFeishuAdvancedClient{}
	mockWS := &mockFeishuWSClient{started: make(chan struct{})}

	ch, err := NewFeishuChannelWithFactory(config.FeishuConfig{
		AppID: "cli_test", AppSecret: "secret",
	}, b, func(appID, appSecret string) FeishuClient { return mockClient }, sdklogger.NewDefault())
	if err != nil {
		t.Fatalf("new channel: %v", err)
	}
	ch.wsFactory = func(appID, appSecret string, onEvent func(context.Context, *larkevent.EventReq) error) (feishuWSClient, error) {
		return mockWS, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := ch.Start(ctx); err != nil {
		t.Fatalf("start error: %v", err)
	}
	select {
	case <-mockWS.started:
	case <-time.After(time.Second):
		t.Fatal("ws client did not start")
	}
	if err := ch.Stop(); err != nil {
		t.Fatalf("stop error: %v", err)
	}
}

func TestFeishuChannel_Send_NilClient(t *testing.T) {
	b := bus.NewMessageBus(10)
	ch, _ := NewFeishuChannel(config.FeishuConfig{
		AppID: "cli_test", AppSecret: "secret",
	}, b, sdklogger.NewDefault())
	if err := ch.Send(bus.OutboundMessage{ChatID: "oc_chat", Content: "hello"}); err == nil {
		t.Fatal("expected error when client is nil")
	}
}

func TestFeishuChannel_Send_ReplyToUserMessage(t *testing.T) {
	ch, _, mockClient := newFeishuWithMocks(t)
	if err := ch.Send(bus.OutboundMessage{
		ChatID:  "oc_chat",
		ReplyTo: "om_user_1",
		Content: "hello",
	}); err != nil {
		t.Fatalf("send error: %v", err)
	}
	if len(mockClient.typedMessages) == 0 {
		t.Fatal("expected typed messages")
	}
	if mockClient.typedMessages[0].msgType != "interactive" {
		t.Fatalf("expected interactive message, got %s", mockClient.typedMessages[0].msgType)
	}
	if mockClient.typedMessages[0].replyTo != "om_user_1" {
		t.Fatalf("replyTo = %q, want om_user_1", mockClient.typedMessages[0].replyTo)
	}
	if card, ok := mockClient.typedMessages[0].content["card"]; !ok || !json.Valid([]byte(card)) {
		t.Fatalf("interactive content should carry valid card json, got=%q", card)
	}
}

func TestFeishuChannel_Send_PreviewUpdateThenFinal(t *testing.T) {
	ch, _, mockClient := newFeishuWithMocks(t)

	if err := ch.Send(bus.OutboundMessage{
		ChatID:   "oc_chat",
		ReplyTo:  "om_user_2",
		Content:  "partial",
		Metadata: map[string]any{telegramEventKey: telegramEventPreviewUpdate},
	}); err != nil {
		t.Fatalf("preview update error: %v", err)
	}
	if len(mockClient.typedMessages) != 2 {
		t.Fatalf("expected tool + draft, got %d", len(mockClient.typedMessages))
	}
	if mockClient.typedMessages[0].replyTo != "" {
		t.Fatal("tool block should be standalone")
	}
	if mockClient.typedMessages[1].replyTo != "om_user_2" {
		t.Fatal("draft should reply to user message")
	}
	if mockClient.typedMessages[1].msgType != "interactive" {
		t.Fatalf("expected draft as interactive card, got %s", mockClient.typedMessages[1].msgType)
	}

	if err := ch.Send(bus.OutboundMessage{
		ChatID:   "oc_chat",
		Content:  "final answer",
		Metadata: map[string]any{telegramEventKey: telegramEventPreviewFinal},
	}); err != nil {
		t.Fatalf("preview final error: %v", err)
	}
	if len(mockClient.editedCards) == 0 {
		t.Fatal("expected final to edit draft card")
	}
	if len(mockClient.deletedMessageIDs) == 0 {
		t.Fatal("expected empty tool placeholder to be deleted")
	}
}

func TestFeishuChannel_Send_ToolProgressStandalone(t *testing.T) {
	ch, _, mockClient := newFeishuWithMocks(t)
	if err := ch.Send(bus.OutboundMessage{
		ChatID:   "oc_chat",
		Content:  `{"query":"abc"}`,
		Metadata: map[string]any{telegramEventKey: telegramEventToolProgress, "tool_name": "WebSearch"},
	}); err != nil {
		t.Fatalf("tool progress error: %v", err)
	}
	if len(mockClient.typedMessages) < 2 {
		t.Fatalf("expected tool placeholder + draft, got %d", len(mockClient.typedMessages))
	}
	if mockClient.typedMessages[0].msgType != "interactive" {
		t.Fatalf("expected tool block as interactive card, got %s", mockClient.typedMessages[0].msgType)
	}
	card := mockClient.typedMessages[0].content["card"]
	if !strings.Contains(card, `"tag":"markdown"`) {
		t.Fatalf("expected tool card to use markdown element, got card=%s", card)
	}
	if len(mockClient.editedCards) == 0 {
		t.Fatal("expected tool block card to be edited with tool payload")
	}
	editedCard := mockClient.editedCards[len(mockClient.editedCards)-1].cardJSON
	if !strings.Contains(editedCard, "```json") {
		t.Fatalf("expected edited tool card markdown to include code block, got card=%s", editedCard)
	}
}

func TestFeishuChannel_Send_ToolProgressEditsCard(t *testing.T) {
	ch, _, mockClient := newFeishuWithMocks(t)
	if err := ch.Send(bus.OutboundMessage{
		ChatID:   "oc_chat",
		Content:  `{"query":"abc"}`,
		Metadata: map[string]any{telegramEventKey: telegramEventToolProgress, "tool_name": "WebSearch"},
	}); err != nil {
		t.Fatalf("first tool progress error: %v", err)
	}
	if err := ch.Send(bus.OutboundMessage{
		ChatID:   "oc_chat",
		Content:  `{"query":"def"}`,
		Metadata: map[string]any{telegramEventKey: telegramEventToolProgress, "tool_name": "WebSearch"},
	}); err != nil {
		t.Fatalf("second tool progress error: %v", err)
	}
	if len(mockClient.editedCards) == 0 {
		t.Fatal("expected tool progress to edit tool card")
	}
}

func TestFeishuChannel_Send_PreviewFinal_WithToolProgress_KeepToolBlock(t *testing.T) {
	ch, _, mockClient := newFeishuWithMocks(t)
	if err := ch.Send(bus.OutboundMessage{
		ChatID:   "oc_chat",
		Content:  "draft",
		Metadata: map[string]any{telegramEventKey: telegramEventPreviewUpdate},
	}); err != nil {
		t.Fatalf("preview update error: %v", err)
	}
	if err := ch.Send(bus.OutboundMessage{
		ChatID:   "oc_chat",
		Content:  `{"q":"x"}`,
		Metadata: map[string]any{telegramEventKey: telegramEventToolProgress, "tool_name": "WebSearch"},
	}); err != nil {
		t.Fatalf("tool progress error: %v", err)
	}
	if err := ch.Send(bus.OutboundMessage{
		ChatID:   "oc_chat",
		Content:  "final",
		Metadata: map[string]any{telegramEventKey: telegramEventPreviewFinal},
	}); err != nil {
		t.Fatalf("preview final error: %v", err)
	}
	if len(mockClient.deletedMessageIDs) != 0 {
		t.Fatalf("expected tool block to remain when had tool progress")
	}
}

func TestFeishuChannel_Send_MediaPathUsesMetadata(t *testing.T) {
	ch, _, mockClient := newFeishuWithMocks(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "sample.png")
	if err := os.WriteFile(p, []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A}, 0644); err != nil {
		t.Fatalf("write sample image: %v", err)
	}
	err := ch.Send(bus.OutboundMessage{
		ChatID: "oc_chat",
		Media:  []string{p},
		Attachments: []api.Attachment{{
			FilePath: p,
			Type:     "image",
			MimeType: "image/png",
		}},
	})
	if err != nil {
		t.Fatalf("send media error: %v", err)
	}
	hasImage := false
	for _, m := range mockClient.typedMessages {
		if m.msgType == "image" {
			hasImage = true
			break
		}
	}
	if !hasImage {
		t.Fatal("expected image typed message")
	}
}

func TestFeishuChannel_Send_AudioIncludesDuration(t *testing.T) {
	ch, _, mockClient := newFeishuWithMocks(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "sample.mp3")
	if err := os.WriteFile(p, []byte("fake-audio"), 0644); err != nil {
		t.Fatalf("write sample audio: %v", err)
	}
	err := ch.Send(bus.OutboundMessage{
		ChatID: "oc_chat",
		Media:  []string{p},
		Attachments: []api.Attachment{{
			FilePath: p,
			Type:     "audio",
			MimeType: "audio/mpeg",
		}},
	})
	if err != nil {
		t.Fatalf("send audio error: %v", err)
	}
	for _, m := range mockClient.typedMessages {
		if m.msgType == "audio" {
			if strings.TrimSpace(m.content["file_key"]) == "" {
				t.Fatal("audio message missing file_key")
			}
			if _, ok := m.content["duration"]; ok {
				t.Fatal("audio message content should not include duration")
			}
			return
		}
	}
	if mockClient.audioUploadCount == 0 {
		t.Fatal("expected audio upload API to be used")
	}
	if mockClient.lastAudioUploadDuration <= 0 {
		t.Fatal("expected positive duration when uploading audio")
	}
	t.Fatal("expected audio typed message")
}

func TestFeishuChannel_ProcessInboundEvent_Text(t *testing.T) {
	ch, b, _ := newFeishuWithMocks(t)
	ch.processInboundEvent("ou_user", "oc_chat", "om_1", "text", `{"text":"hello aevitas"}`)
	select {
	case msg := <-b.Inbound:
		if msg.Content != "hello aevitas" {
			t.Fatalf("content = %q", msg.Content)
		}
		if msg.Metadata["message_id"] != "om_1" {
			t.Fatalf("message_id metadata mismatch: %#v", msg.Metadata["message_id"])
		}
	case <-time.After(time.Second):
		t.Fatal("expected inbound message")
	}
}

func TestFeishuChannel_ProcessInboundEvent_Image_MIMEFromAgentSDK(t *testing.T) {
	ch, b, mockClient := newFeishuWithMocks(t)
	// PNG magic bytes; downloadMIME intentionally empty to force agentsdk DetectAttachmentMIME.
	mockClient.downloadData = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	mockClient.downloadMIME = ""

	ch.processInboundEvent("ou_user", "oc_chat", "om_img", "image", `{"image_key":"img_xxx"}`)
	select {
	case msg := <-b.Inbound:
		if len(msg.Media) != 1 {
			t.Fatalf("expected one media file, got %d", len(msg.Media))
		}
		if len(msg.Attachments) != 1 {
			t.Fatalf("expected one attachment, got %d", len(msg.Attachments))
		}
		att := msg.Attachments[0]
		if att.Type != "image" {
			t.Fatalf("kind = %q, want image", att.Type)
		}
		if !strings.HasPrefix(strings.ToLower(att.MimeType), "image/") {
			t.Fatalf("mime = %q, want image/*", att.MimeType)
		}
	case <-time.After(time.Second):
		t.Fatal("expected inbound image")
	}
}

func TestFeishuChannel_ProcessEventReq(t *testing.T) {
	ch, b, _ := newFeishuWithMocks(t)
	payload := map[string]any{
		"header": map[string]any{
			"event_type": "im.message.receive_v1",
		},
		"event": map[string]any{
			"sender": map[string]any{
				"sender_id": map[string]any{"open_id": "ou_test"},
			},
			"message": map[string]any{
				"message_id":   "om_77",
				"chat_id":      "oc_chat_77",
				"message_type": "text",
				"content":      `{"text":"hi"}`,
			},
		},
	}
	body, _ := json.Marshal(payload)
	if err := ch.processEventReq(context.Background(), &larkevent.EventReq{Body: body}); err != nil {
		t.Fatalf("processEventReq error: %v", err)
	}
	select {
	case msg := <-b.Inbound:
		if msg.ChatID != "oc_chat_77" {
			t.Fatalf("chatID = %q", msg.ChatID)
		}
	case <-time.After(time.Second):
		t.Fatal("expected inbound from processEventReq")
	}
}

func TestEncodeFeishuContent_InteractiveUsesRawCardJSON(t *testing.T) {
	card := `{"config":{"wide_screen_mode":true},"elements":[{"tag":"div","text":{"tag":"lark_md","content":"ok"}}]}`
	got, err := encodeFeishuContent("interactive", map[string]string{"card": card})
	if err != nil {
		t.Fatalf("encodeFeishuContent interactive error: %v", err)
	}
	if got != card {
		t.Fatalf("got %q, want raw card json %q", got, card)
	}
}

func TestEncodeFeishuContent_InteractiveRejectsInvalidCard(t *testing.T) {
	_, err := encodeFeishuContent("interactive", map[string]string{"card": "{bad json"})
	if err == nil {
		t.Fatal("expected invalid card json error")
	}
}

func TestEncodeFeishuContent_AudioUsesFileKeyOnly(t *testing.T) {
	got, err := encodeFeishuContent("audio", map[string]string{
		"file_key": "file_xxx",
		"duration": "2345",
	})
	if err != nil {
		t.Fatalf("encodeFeishuContent audio error: %v", err)
	}
	if !strings.Contains(got, `"file_key":"file_xxx"`) {
		t.Fatalf("unexpected audio content json: %s", got)
	}
	if strings.Contains(got, `"duration"`) {
		t.Fatalf("audio content should not include duration, got: %s", got)
	}
}

func TestChannelManager_FeishuEnabled(t *testing.T) {
	b := bus.NewMessageBus(10)
	m, err := NewChannelManager(config.ChannelsConfig{
		Feishu: config.FeishuConfig{
			Enabled:   true,
			AppID:     "cli_test",
			AppSecret: "secret",
		},
	}, b, sdklogger.NewDefault())
	if err != nil {
		t.Fatalf("NewChannelManager error: %v", err)
	}
	channels := m.EnabledChannels()
	if len(channels) != 1 || channels[0] != "feishu" {
		t.Fatalf("EnabledChannels = %v, want [feishu]", channels)
	}
}
