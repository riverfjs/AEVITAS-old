package channel

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	sdklogger "github.com/riverfjs/agentsdk-go/pkg/logger"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	telegramify "github.com/riverfjs/telegramify-go"
	"github.com/riverfjs/aevitas/internal/bus"
	"github.com/riverfjs/aevitas/internal/config"
	"github.com/riverfjs/aevitas/internal/protocol"
)

// ===== Telegram MessageEntity 转换测试 =====
// 使用telegramify库进行Markdown到MessageEntity的转换

// Helper function to find entity by type
func findEntity(entities []telegramify.MessageEntity, entityType string) *telegramify.MessageEntity {
	for i := range entities {
		if entities[i].Type == entityType {
			return &entities[i]
		}
	}
	return nil
}

func TestTelegramMarkdownToEntities(t *testing.T) {
	tests := []struct {
		name             string
		input            string
		wantContains     string // Text should contain this
		requiredEntities []string // Must have these entity types
		forbiddenEntities []string // Must not have these entity types
	}{
		{
			name:              "plain text",
			input:             "hello",
			wantContains:      "hello",
			requiredEntities:  []string{},
			forbiddenEntities: []string{},
		},
		{
			name:             "bold",
			input:            "**bold**",
			wantContains:     "bold",
			requiredEntities: []string{"bold"},
		},
		{
			name:             "italic",
			input:            "*italic*",
			wantContains:     "italic",
			requiredEntities: []string{"italic"},
		},
		{
			name:             "code",
			input:            "`code`",
			wantContains:     "code",
			requiredEntities: []string{"code"},
		},
		{
			name:             "link",
			input:            "[link](https://example.com)",
			wantContains:     "link",
			requiredEntities: []string{"text_link"},
		},
		{
			name:             "header 1",
			input:            "# Header 1",
			wantContains:     "📌",
			requiredEntities: []string{"bold", "underline"},
		},
		{
			name:             "header 2",
			input:            "## Header 2",
			wantContains:     "📝",
			requiredEntities: []string{"bold", "underline"},
		},
		{
			name:              "header 3",
			input:             "### Header 3",
			wantContains:      "📋",
			requiredEntities:  []string{"bold"},
			forbiddenEntities: []string{"underline"}, // H3 should not have underline
		},
		{
			name:             "mixed",
			input:            "**bold** and *italic*",
			wantContains:     "bold",
			requiredEntities: []string{"bold", "italic"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, entities := telegramify.Convert(tt.input, false, nil)
			
			// Check text contains expected string
			if tt.wantContains != "" && !strings.Contains(text, tt.wantContains) {
				t.Errorf("Convert(%q) text = %q, should contain %q", tt.input, text, tt.wantContains)
			}
			
			// Check required entities exist
			for _, requiredType := range tt.requiredEntities {
				if findEntity(entities, requiredType) == nil {
					t.Errorf("Convert(%q) should have %q entity", tt.input, requiredType)
				}
			}
			
			// Check forbidden entities don't exist
			for _, forbiddenType := range tt.forbiddenEntities {
				if findEntity(entities, forbiddenType) != nil {
					t.Errorf("Convert(%q) should not have %q entity", tt.input, forbiddenType)
				}
			}
		})
	}
}

func TestTelegramMarkdownToEntities_CodeBlocks(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		wantEntityType string
	}{
		{
			name:  "code block without language",
			input: "```\nfunc main() {}\n```",
			wantEntityType: "pre",
		},
		{
			name:  "code block with language",
			input: "```go\nfunc main() {}\n```",
			wantEntityType: "pre",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, entities := telegramify.Convert(tt.input, false, nil)
			found := false
			for _, ent := range entities {
				if ent.Type == tt.wantEntityType {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("Convert(%q) did not produce %q entity", tt.input, tt.wantEntityType)
			}
		})
	}
}

// ===== Telegram Channel 基础测试 =====

func TestNewTelegramChannel_NoToken(t *testing.T) {
	b := bus.NewMessageBus(10)
	logger := sdklogger.NewDefault()
	_, err := NewTelegramChannel(config.TelegramConfig{}, b, logger)
	if err == nil {
		t.Error("expected error for empty token")
	}
}

func TestNewTelegramChannel_Valid(t *testing.T) {
	b := bus.NewMessageBus(10)
	logger := sdklogger.NewDefault()
	ch, err := NewTelegramChannel(config.TelegramConfig{Token: "fake-token"}, b, logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ch.Name() != "telegram" {
		t.Errorf("Name = %q, want telegram", ch.Name())
	}
}

func TestTelegramChannel_Stop_NotStarted(t *testing.T) {
	b := bus.NewMessageBus(10)
	ch, _ := NewTelegramChannel(config.TelegramConfig{Token: "fake-token"}, b, sdklogger.NewDefault())

	err := ch.Stop()
	if err != nil {
		t.Errorf("Stop error: %v", err)
	}
}

func TestTelegramChannel_Send_NilBot(t *testing.T) {
	b := bus.NewMessageBus(10)
	ch, _ := NewTelegramChannel(config.TelegramConfig{Token: "fake-token"}, b, sdklogger.NewDefault())

	err := ch.Send(bus.OutboundMessage{ChatID: "123", Content: "test"})
	if err == nil {
		t.Error("expected error when bot is nil")
	}
}

func TestTelegramChannel_Send_InvalidChatID(t *testing.T) {
	b := bus.NewMessageBus(10)
	ch, _ := NewTelegramChannel(config.TelegramConfig{Token: "fake-token"}, b, sdklogger.NewDefault())
	ch.SetBot(newMockBot())

	err := ch.Send(bus.OutboundMessage{ChatID: "not-a-number", Content: "test"})
	if err == nil {
		t.Error("expected error for invalid chat ID")
	}
}

// ===== Telegram Message Handling 测试 =====

func TestTelegramChannel_HandleMessage_Allowed(t *testing.T) {
	b := bus.NewMessageBus(10)
	mockBot := newMockBot()
	
	ch, _ := NewTelegramChannel(config.TelegramConfig{Token: "fake-token"}, b, sdklogger.NewDefault())
	ch.SetBot(mockBot)

	msg := &tgbotapi.Message{
		From: &tgbotapi.User{ID: 123, UserName: "testuser"},
		Chat: &tgbotapi.Chat{ID: 456},
		Text: "hello",
		Date: 1234567890,
	}

	ch.handleMessage(msg)

	select {
	case inbound := <-b.Inbound:
		if inbound.Content != "hello" {
			t.Errorf("content = %q, want hello", inbound.Content)
		}
		if inbound.SenderID != "123" {
			t.Errorf("senderID = %q, want 123", inbound.SenderID)
		}
		if inbound.ChatID != "456" {
			t.Errorf("chatID = %q, want 456", inbound.ChatID)
		}
	default:
		t.Error("expected inbound message")
	}
}

func TestTelegramChannel_HandleMessage_EmptyText(t *testing.T) {
	b := bus.NewMessageBus(10)
	ch, _ := NewTelegramChannel(config.TelegramConfig{Token: "fake-token"}, b, sdklogger.NewDefault())

	msg := &tgbotapi.Message{
		From: &tgbotapi.User{ID: 123},
		Chat: &tgbotapi.Chat{ID: 456},
		Text: "",
	}

	ch.handleMessage(msg)

	select {
	case <-b.Inbound:
		t.Error("should not send message with empty content")
	default:
		// OK
	}
}

func TestTelegramChannel_HandleMessage_Caption(t *testing.T) {
	b := bus.NewMessageBus(10)
	mockBot := newMockBot()
	
	ch, _ := NewTelegramChannel(config.TelegramConfig{Token: "fake-token"}, b, sdklogger.NewDefault())
	ch.SetBot(mockBot)

	msg := &tgbotapi.Message{
		From:    &tgbotapi.User{ID: 123},
		Chat:    &tgbotapi.Chat{ID: 456},
		Text:    "",
		Caption: "image caption",
	}

	ch.handleMessage(msg)

	select {
	case inbound := <-b.Inbound:
		if inbound.Content != "image caption" {
			t.Errorf("content = %q, want 'image caption'", inbound.Content)
		}
	default:
		t.Error("expected inbound message")
	}
}

// ===== Mock Bot for Testing =====

type mockTelegramBot struct {
	updatesChan chan tgbotapi.Update
	stopped     bool
	sentMsgs    []tgbotapi.Chattable
	edited      []tgbotapi.EditMessageTextConfig
	deleted     []tgbotapi.DeleteMessageConfig
	reactions   []string
	sendErr     error
	sendEditErr error
	editErr     error
	deleteErr   error
	reactionErr error
	self        tgbotapi.User
}

func newMockBot() *mockTelegramBot {
	return &mockTelegramBot{
		updatesChan: make(chan tgbotapi.Update, 10),
		self:        tgbotapi.User{UserName: "testbot"},
	}
}

func (m *mockTelegramBot) GetUpdatesChan(config tgbotapi.UpdateConfig) tgbotapi.UpdatesChannel {
	return m.updatesChan
}

func (m *mockTelegramBot) StopReceivingUpdates() {
	m.stopped = true
}

func (m *mockTelegramBot) Send(c tgbotapi.Chattable) (tgbotapi.Message, error) {
	if edit, ok := c.(tgbotapi.EditMessageTextConfig); ok {
		if m.sendEditErr != nil {
			return tgbotapi.Message{}, m.sendEditErr
		}
		m.edited = append(m.edited, edit)
	}
	m.sentMsgs = append(m.sentMsgs, c)
	if m.sendErr != nil {
		return tgbotapi.Message{}, m.sendErr
	}
	return tgbotapi.Message{MessageID: 1}, nil
}

func (m *mockTelegramBot) EditMessageText(chatID int64, messageID int, text string) (tgbotapi.Message, error) {
	if m.editErr != nil {
		return tgbotapi.Message{}, m.editErr
	}
	if m.sendErr != nil {
		return tgbotapi.Message{}, m.sendErr
	}
	m.edited = append(m.edited, tgbotapi.NewEditMessageText(chatID, messageID, text))
	return tgbotapi.Message{MessageID: messageID}, nil
}

func (m *mockTelegramBot) DeleteMessage(chatID int64, messageID int) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	m.deleted = append(m.deleted, tgbotapi.NewDeleteMessage(chatID, messageID))
	return nil
}

func (m *mockTelegramBot) SetMessageReaction(chatID int64, messageID int, emoji string) error {
	if m.reactionErr != nil {
		return m.reactionErr
	}
	m.reactions = append(m.reactions, fmt.Sprintf("%d:%d:%s", chatID, messageID, emoji))
	return nil
}

func (m *mockTelegramBot) GetSelf() tgbotapi.User {
	return m.self
}

func (m *mockTelegramBot) GetFileDirectURL(fileID string) (string, error) {
	return "https://api.telegram.org/file/bot/test.jpg", nil
}

// ===== Telegram Send 测试 =====

func TestTelegramChannel_Send_Success(t *testing.T) {
	b := bus.NewMessageBus(10)
	mockBot := newMockBot()

	ch, _ := NewTelegramChannel(config.TelegramConfig{Token: "fake-token"}, b, sdklogger.NewDefault())
	ch.SetBot(mockBot)

	err := ch.Send(bus.OutboundMessage{ChatID: "123", Content: "hello"})
	if err != nil {
		t.Errorf("Send error: %v", err)
	}

	if len(mockBot.sentMsgs) < 1 {
		t.Errorf("expected at least 1 sent message, got %d", len(mockBot.sentMsgs))
	}
}

func TestTelegramChannel_Send_ReplyToUserMessage(t *testing.T) {
	b := bus.NewMessageBus(10)
	mockBot := newMockBot()

	ch, _ := NewTelegramChannel(config.TelegramConfig{Token: "fake-token"}, b, sdklogger.NewDefault())
	ch.SetBot(mockBot)

	err := ch.Send(bus.OutboundMessage{ChatID: "123", ReplyTo: "42", Content: "hello"})
	if err != nil {
		t.Fatalf("Send error: %v", err)
	}
	if len(mockBot.sentMsgs) < 1 {
		t.Fatalf("expected at least 1 sent message, got %d", len(mockBot.sentMsgs))
	}
	msg, ok := mockBot.sentMsgs[0].(tgbotapi.MessageConfig)
	if !ok {
		t.Fatalf("expected first outbound to be MessageConfig, got %T", mockBot.sentMsgs[0])
	}
	if msg.ReplyToMessageID != 42 {
		t.Fatalf("expected ReplyToMessageID=42, got %d", msg.ReplyToMessageID)
	}
}

func TestTelegramChannel_Send_LongMessage(t *testing.T) {
	b := bus.NewMessageBus(10)
	mockBot := newMockBot()

	ch, _ := NewTelegramChannel(config.TelegramConfig{Token: "fake-token"}, b, sdklogger.NewDefault())
	ch.SetBot(mockBot)

	longContent := ""
	for i := 0; i < 100; i++ {
		longContent += "This is a long line of text that will be repeated.\n"
	}

	err := ch.Send(bus.OutboundMessage{ChatID: "123", Content: longContent})
	if err != nil {
		t.Errorf("Send error: %v", err)
	}

	// Telegramify will split long content into multiple messages
	if len(mockBot.sentMsgs) < 1 {
		t.Errorf("expected at least 1 sent message for long content, got %d", len(mockBot.sentMsgs))
	}
}

func TestTelegramChannel_Send_BothFail(t *testing.T) {
	b := bus.NewMessageBus(10)
	mockBot := newMockBot()
	mockBot.sendErr = fmt.Errorf("send failed")

	ch, _ := NewTelegramChannel(config.TelegramConfig{Token: "fake-token"}, b, sdklogger.NewDefault())
	ch.SetBot(mockBot)

	err := ch.Send(bus.OutboundMessage{ChatID: "123", Content: "test"})
	if err == nil {
		t.Error("expected error when send fails")
	}
}

func TestTelegramChannel_Send_WithCodeBlock(t *testing.T) {
	b := bus.NewMessageBus(10)
	mockBot := newMockBot()

	ch, _ := NewTelegramChannel(config.TelegramConfig{Token: "fake-token"}, b, sdklogger.NewDefault())
	ch.SetBot(mockBot)

	// Content with code block (should be extracted as file)
	content := "Here's some code:\n```go\npackage main\n\nfunc main() {\n\tprintln(\"Hello\")\n}\n```"

	err := ch.Send(bus.OutboundMessage{ChatID: "123", Content: content})
	if err != nil {
		t.Errorf("Send error: %v", err)
	}

	// Should have sent at least 1 message (text or file)
	if len(mockBot.sentMsgs) < 1 {
		t.Errorf("expected at least 1 sent message, got %d", len(mockBot.sentMsgs))
	}
}

func TestTelegramChannel_Send_WithMermaid(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping mermaid network test in short mode")
	}

	b := bus.NewMessageBus(10)
	mockBot := newMockBot()

	ch, _ := NewTelegramChannel(config.TelegramConfig{Token: "fake-token"}, b, sdklogger.NewDefault())
	ch.SetBot(mockBot)

	// Content with Mermaid diagram
	content := "Here's a diagram:\n```mermaid\ngraph TD\n    A[Start] --> B[End]\n```"

	err := ch.Send(bus.OutboundMessage{ChatID: "123", Content: content})
	if err != nil {
		t.Errorf("Send error: %v", err)
	}

	// Should have sent at least 1 message
	// If mermaid.ink is available: text + photo
	// If mermaid.ink fails: text + file (fallback)
	if len(mockBot.sentMsgs) < 1 {
		t.Errorf("expected at least 1 sent message, got %d", len(mockBot.sentMsgs))
	}

	t.Logf("Sent %d messages (includes mermaid content)", len(mockBot.sentMsgs))
}

func TestTelegramChannel_Send_PreviewUpdateThenFinal(t *testing.T) {
	b := bus.NewMessageBus(10)
	mockBot := newMockBot()

	ch, _ := NewTelegramChannel(config.TelegramConfig{Token: "fake-token"}, b, sdklogger.NewDefault())
	ch.SetBot(mockBot)

	err := ch.Send(bus.OutboundMessage{
		ChatID:   "123",
		Content:  "partial",
		Metadata: map[string]any{protocol.EventTypeKey: protocol.EventPreviewUpdate, protocol.RequestIDKey: "req-1"},
	})
	if err != nil {
		t.Fatalf("update preview error: %v", err)
	}
	if len(mockBot.sentMsgs) != 1 {
		t.Fatalf("expected first preview to send one message, got %d", len(mockBot.sentMsgs))
	}

	err = ch.Send(bus.OutboundMessage{
		ChatID:   "123",
		Content:  "final text",
		Metadata: map[string]any{protocol.EventTypeKey: protocol.EventPreviewFinal, protocol.RequestIDKey: "req-2"},
	})
	if err != nil {
		t.Fatalf("final preview error: %v", err)
	}
	if len(mockBot.sentMsgs) != 2 {
		t.Fatalf("expected final preview to send one more message, got %d", len(mockBot.sentMsgs))
	}
}

func TestTelegramChannel_Send_PreviewUpdate_RepliesToUserMessage(t *testing.T) {
	b := bus.NewMessageBus(10)
	mockBot := newMockBot()

	ch, _ := NewTelegramChannel(config.TelegramConfig{Token: "fake-token"}, b, sdklogger.NewDefault())
	ch.SetBot(mockBot)

	err := ch.Send(bus.OutboundMessage{
		ChatID:   "123",
		ReplyTo:  "99",
		Content:  "partial",
		Metadata: map[string]any{protocol.EventTypeKey: protocol.EventPreviewUpdate, protocol.RequestIDKey: "req-3"},
	})
	if err != nil {
		t.Fatalf("preview update error: %v", err)
	}
	if len(mockBot.sentMsgs) != 1 {
		t.Fatalf("expected first preview to send one message, got %d", len(mockBot.sentMsgs))
	}
	draftMsg, ok := mockBot.sentMsgs[0].(tgbotapi.MessageConfig)
	if !ok {
		t.Fatalf("expected draft block to be MessageConfig, got %T", mockBot.sentMsgs[0])
	}
	if draftMsg.ReplyToMessageID != 99 {
		t.Fatalf("expected draft block ReplyToMessageID=99, got %d", draftMsg.ReplyToMessageID)
	}
}

func TestTelegramChannel_Send_PreviewFinal_WithToolProgress_DoesNotDeleteToolBlock(t *testing.T) {
	b := bus.NewMessageBus(10)
	mockBot := newMockBot()

	ch, _ := NewTelegramChannel(config.TelegramConfig{Token: "fake-token"}, b, sdklogger.NewDefault())
	ch.SetBot(mockBot)

	if err := ch.Send(bus.OutboundMessage{
		ChatID:   "123",
		Content:  "draft",
		Metadata: map[string]any{protocol.EventTypeKey: protocol.EventPreviewUpdate, protocol.RequestIDKey: "req-4"},
	}); err != nil {
		t.Fatalf("preview update failed: %v", err)
	}
	if err := ch.Send(bus.OutboundMessage{
		ChatID:   "123",
		Content:  `{"query":"abc"}`,
		Metadata: map[string]any{protocol.EventTypeKey: protocol.EventToolProgress, protocol.RequestIDKey: "req-5", "tool_name": "WebSearch"},
	}); err != nil {
		t.Fatalf("tool progress failed: %v", err)
	}
	if err := ch.Send(bus.OutboundMessage{
		ChatID:   "123",
		Content:  "final",
		Metadata: map[string]any{protocol.EventTypeKey: protocol.EventPreviewFinal, protocol.RequestIDKey: "req-6"},
	}); err != nil {
		t.Fatalf("preview final failed: %v", err)
	}
	if len(mockBot.deleted) != 0 {
		t.Fatalf("expected tool block to remain when had tool progress")
	}
}

func TestTelegramChannel_Send_PreviewFinal_NotModified_DoesNotFallbackSend(t *testing.T) {
	b := bus.NewMessageBus(10)
	mockBot := newMockBot()

	ch, _ := NewTelegramChannel(config.TelegramConfig{Token: "fake-token"}, b, sdklogger.NewDefault())
	ch.SetBot(mockBot)

	if err := ch.Send(bus.OutboundMessage{
		ChatID:   "123",
		Content:  "same final text",
		Metadata: map[string]any{protocol.EventTypeKey: protocol.EventPreviewUpdate, protocol.RequestIDKey: "req-7"},
	}); err != nil {
		t.Fatalf("preview update failed: %v", err)
	}

	beforeSends := len(mockBot.sentMsgs)
	mockBot.sendEditErr = fmt.Errorf("Bad Request: message is not modified")
	if err := ch.Send(bus.OutboundMessage{
		ChatID:   "123",
		Content:  "same final text",
		Metadata: map[string]any{protocol.EventTypeKey: protocol.EventPreviewFinal, protocol.RequestIDKey: "req-8"},
	}); err != nil {
		t.Fatalf("preview final failed: %v", err)
	}
	if len(mockBot.sentMsgs) != beforeSends+1 {
		t.Fatalf("expected final preview to send exactly one new message, got %d -> %d", beforeSends, len(mockBot.sentMsgs))
	}
}

func TestTelegramChannel_Send_DuplicatePreviewFinal_IsIdempotent(t *testing.T) {
	b := bus.NewMessageBus(10)
	mockBot := newMockBot()

	ch, _ := NewTelegramChannel(config.TelegramConfig{Token: "fake-token"}, b, sdklogger.NewDefault())
	ch.SetBot(mockBot)

	if err := ch.Send(bus.OutboundMessage{
		ChatID:   "123",
		Content:  "draft",
		Metadata: map[string]any{protocol.EventTypeKey: protocol.EventPreviewUpdate, protocol.RequestIDKey: "req-9"},
	}); err != nil {
		t.Fatalf("preview update failed: %v", err)
	}
	if err := ch.Send(bus.OutboundMessage{
		ChatID:   "123",
		Content:  "final once",
		Metadata: map[string]any{protocol.EventTypeKey: protocol.EventPreviewFinal, protocol.RequestIDKey: "req-10"},
	}); err != nil {
		t.Fatalf("first preview final failed: %v", err)
	}

	sentCount := len(mockBot.sentMsgs)
	editedCount := len(mockBot.edited)
	deletedCount := len(mockBot.deleted)

	if err := ch.Send(bus.OutboundMessage{
		ChatID:   "123",
		Content:  "final once",
		Metadata: map[string]any{protocol.EventTypeKey: protocol.EventPreviewFinal, protocol.RequestIDKey: "req-11"},
	}); err != nil {
		t.Fatalf("duplicate preview final failed: %v", err)
	}

	if len(mockBot.sentMsgs) != sentCount+1 || len(mockBot.edited) != editedCount || len(mockBot.deleted) != deletedCount {
		t.Fatalf("duplicate final should send another final message only, got sent=%d->%d edited=%d->%d deleted=%d->%d",
			sentCount, len(mockBot.sentMsgs), editedCount, len(mockBot.edited), deletedCount, len(mockBot.deleted))
	}
}

func TestTelegramChannel_Send_PreviewUpdateEditsExisting(t *testing.T) {
	b := bus.NewMessageBus(10)
	mockBot := newMockBot()

	ch, _ := NewTelegramChannel(config.TelegramConfig{Token: "fake-token"}, b, sdklogger.NewDefault())
	ch.SetBot(mockBot)

	first := bus.OutboundMessage{
		ChatID:   "123",
		Content:  "chunk-1",
		Metadata: map[string]any{protocol.EventTypeKey: protocol.EventPreviewUpdate, protocol.RequestIDKey: "req-12"},
	}
	second := bus.OutboundMessage{
		ChatID:   "123",
		Content:  "chunk-1 chunk-2",
		Metadata: map[string]any{protocol.EventTypeKey: protocol.EventPreviewUpdate, protocol.RequestIDKey: "req-13"},
	}
	if err := ch.Send(first); err != nil {
		t.Fatalf("first preview send: %v", err)
	}
	ctrl := <-b.Inbound
	mid, _ := ctrl.Metadata["message_id"].(string)
	second.Metadata["message_id"] = mid
	if err := ch.Send(second); err != nil {
		t.Fatalf("second preview send: %v", err)
	}
	if len(mockBot.edited) < 1 {
		t.Fatalf("expected at least one draft edit for second preview, got %d", len(mockBot.edited))
	}
}

func TestTelegramChannel_Send_PreviewFinalCodeBlockUsesFinalizePipeline(t *testing.T) {
	b := bus.NewMessageBus(10)
	mockBot := newMockBot()

	ch, _ := NewTelegramChannel(config.TelegramConfig{Token: "fake-token"}, b, sdklogger.NewDefault())
	ch.SetBot(mockBot)

	if err := ch.Send(bus.OutboundMessage{
		ChatID:   "123",
		Content:  "draft text",
		Metadata: map[string]any{protocol.EventTypeKey: protocol.EventPreviewUpdate, protocol.RequestIDKey: "req-14"},
	}); err != nil {
		t.Fatalf("preview update failed: %v", err)
	}

	finalContent := "最终代码如下：\n```go\npackage main\nfunc main(){}\n```"
	if err := ch.Send(bus.OutboundMessage{
		ChatID:   "123",
		Content:  finalContent,
		Metadata: map[string]any{protocol.EventTypeKey: protocol.EventPreviewFinal, protocol.RequestIDKey: "req-15"},
	}); err != nil {
		t.Fatalf("preview final failed: %v", err)
	}

	if len(mockBot.sentMsgs) < 2 {
		t.Fatalf("expected final pipeline to send another message, got %d", len(mockBot.sentMsgs))
	}
}

func TestTelegramChannel_Send_ToolProgressCreatesAndKeepsToolBlocks(t *testing.T) {
	b := bus.NewMessageBus(10)
	mockBot := newMockBot()

	ch, _ := NewTelegramChannel(config.TelegramConfig{Token: "fake-token"}, b, sdklogger.NewDefault())
	ch.SetBot(mockBot)

	// First progress message creates tool+draft blocks and updates tool block.
	if err := ch.Send(bus.OutboundMessage{ChatID: "123", Content: "⏳ WebSearch"}); err != nil {
		t.Fatalf("first tool progress failed: %v", err)
	}

	// Many progress messages should rollover to a new tool block (without dropping old block).
	for i := 0; i < 80; i++ {
		long := fmt.Sprintf("⏳ WebFetch %d", i)
		if err := ch.Send(bus.OutboundMessage{
			ChatID:  "123",
			Content: long,
			Metadata: map[string]any{
				protocol.EventTypeKey: protocol.EventToolProgress,
				protocol.RequestIDKey: fmt.Sprintf("req-tool-%d", i),
				"tool_name":      "WebFetch",
				"tool_params":    fmt.Sprintf(`{"url":"https://example.com/%d","prompt":"%s"}`, i, strings.Repeat("x", 120)),
			},
		}); err != nil {
			t.Fatalf("tool progress #%d failed: %v", i, err)
		}
	}

	// Expect at least 3 sends: tool slot + draft slot + rollover tool block.
	if len(mockBot.sentMsgs) < 3 {
		t.Fatalf("expected rollover to create new tool block, got %d sends", len(mockBot.sentMsgs))
	}
}

func TestTelegramChannel_Send_ToolProgressRendersStructuredSummary(t *testing.T) {
	b := bus.NewMessageBus(10)
	mockBot := newMockBot()

	ch, _ := NewTelegramChannel(config.TelegramConfig{Token: "fake-token"}, b, sdklogger.NewDefault())
	ch.SetBot(mockBot)

	msg := bus.OutboundMessage{
		ChatID:  "123",
		Content: "⏳ WebSearch",
		Metadata: map[string]any{
			protocol.EventTypeKey: protocol.EventToolProgress,
			protocol.RequestIDKey: "req-16",
			"tool_name":      "WebSearch",
			"tool_params":    `{"query":"Iran situation March 2026 latest news","url":"https://example.com"}`,
		},
	}
	if err := ch.Send(msg); err != nil {
		t.Fatalf("tool progress send failed: %v", err)
	}
	if len(mockBot.sentMsgs) == 0 {
		t.Fatalf("expected tool progress send")
	}
	gotMsg, ok := mockBot.sentMsgs[len(mockBot.sentMsgs)-1].(tgbotapi.MessageConfig)
	if !ok {
		t.Fatalf("expected message config, got %T", mockBot.sentMsgs[len(mockBot.sentMsgs)-1])
	}
	got := gotMsg.Text
	if !strings.Contains(got, "WebSearch") {
		t.Fatalf("expected tool name in rendered block, got %q", got)
	}
}

func TestTelegramChannel_Send_UsageHUDAfterToolProgress_SendsStandalone(t *testing.T) {
	b := bus.NewMessageBus(10)
	mockBot := newMockBot()

	ch, _ := NewTelegramChannel(config.TelegramConfig{Token: "fake-token"}, b, sdklogger.NewDefault())
	ch.SetBot(mockBot)

	// Prepare a turn with preview + tool progress.
	if err := ch.Send(bus.OutboundMessage{
		ChatID:   "123",
		Content:  "draft text",
		Metadata: map[string]any{protocol.EventTypeKey: protocol.EventPreviewUpdate, protocol.RequestIDKey: "req-17"},
	}); err != nil {
		t.Fatalf("preview update failed: %v", err)
	}
	if err := ch.Send(bus.OutboundMessage{
		ChatID:  "123",
		Content: "⏳ WebSearch",
		Metadata: map[string]any{
			protocol.EventTypeKey: protocol.EventToolProgress,
			protocol.RequestIDKey: "req-18",
			"tool_name":      "WebSearch",
			"tool_params":    `{"query":"abc"}`,
		},
	}); err != nil {
		t.Fatalf("tool progress failed: %v", err)
	}
	if err := ch.Send(bus.OutboundMessage{
		ChatID:   "123",
		Content:  "final answer",
		Metadata: map[string]any{protocol.EventTypeKey: protocol.EventPreviewFinal, protocol.RequestIDKey: "req-19"},
	}); err != nil {
		t.Fatalf("preview final failed: %v", err)
	}

	beforeEdits := len(mockBot.edited)
	beforeSent := len(mockBot.sentMsgs)
	if err := ch.Send(bus.OutboundMessage{
		ChatID:   "123",
		Content:  "📊 Usage\nTotal billed tokens: 123",
		Metadata: map[string]any{protocol.EventTypeKey: protocol.EventUsageHUD},
	}); err != nil {
		t.Fatalf("usage hud send failed: %v", err)
	}
	if len(mockBot.sentMsgs) <= beforeSent {
		t.Fatalf("expected usage hud standalone send")
	}
	if len(mockBot.edited) != beforeEdits {
		t.Fatalf("expected usage hud not to edit tool block")
	}
}

func TestTelegramChannel_Send_UsageHUDWithoutToolProgress_SendsStandalone(t *testing.T) {
	b := bus.NewMessageBus(10)
	mockBot := newMockBot()

	ch, _ := NewTelegramChannel(config.TelegramConfig{Token: "fake-token"}, b, sdklogger.NewDefault())
	ch.SetBot(mockBot)

	beforeSent := len(mockBot.sentMsgs)
	if err := ch.Send(bus.OutboundMessage{
		ChatID:   "123",
		Content:  "📊 Usage\nTotal billed tokens: 456",
		Metadata: map[string]any{protocol.EventTypeKey: protocol.EventUsageHUD},
	}); err != nil {
		t.Fatalf("usage hud send failed: %v", err)
	}
	if len(mockBot.sentMsgs) <= beforeSent {
		t.Fatalf("expected standalone usage hud send")
	}
}

func TestTelegramChannel_Send_UsageHUD_WithToolProgress_NoToolBlockEdit(t *testing.T) {
	b := bus.NewMessageBus(10)
	mockBot := newMockBot()

	ch, _ := NewTelegramChannel(config.TelegramConfig{Token: "fake-token"}, b, sdklogger.NewDefault())
	ch.SetBot(mockBot)

	if err := ch.Send(bus.OutboundMessage{
		ChatID:  "123",
		Content: "⏳ WebSearch",
		Metadata: map[string]any{
			protocol.EventTypeKey: protocol.EventToolProgress,
			protocol.RequestIDKey: "req-20",
			"tool_name":      "WebSearch",
			"tool_params":    `{"query":"abc"}`,
		},
	}); err != nil {
		t.Fatalf("tool progress failed: %v", err)
	}

	beforeEdits := len(mockBot.edited)
	beforeSent := len(mockBot.sentMsgs)
	if err := ch.Send(bus.OutboundMessage{
		ChatID:  "123",
		Content: "📊 Usage\nTotal billed tokens: 789",
		Metadata: map[string]any{
			protocol.EventTypeKey: protocol.EventUsageHUD,
		},
	}); err != nil {
		t.Fatalf("usage hud html send failed: %v", err)
	}
	if len(mockBot.sentMsgs) <= beforeSent {
		t.Fatalf("expected html usage to send standalone message")
	}
	if len(mockBot.edited) != beforeEdits {
		t.Fatalf("expected usage not to edit tool block")
	}
}

// ===== Telegram Start/Stop 测试 =====

func TestTelegramChannel_Start_NilMessage(t *testing.T) {
	b := bus.NewMessageBus(10)
	mockBot := newMockBot()

	factory := func(token, apiEndpoint string, client *http.Client) (TelegramBot, error) {
		return mockBot, nil
	}

	ch, _ := NewTelegramChannelWithFactory(config.TelegramConfig{Token: "fake-token"}, b, factory, sdklogger.NewDefault())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch.Start(ctx)

	mockBot.updatesChan <- tgbotapi.Update{Message: nil}
	time.Sleep(50 * time.Millisecond)

	select {
	case <-b.Inbound:
		t.Error("should not receive message for nil update")
	default:
		// OK
	}
}

