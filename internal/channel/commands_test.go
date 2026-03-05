package channel

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/riverfjs/aevitas/internal/bus"
	"github.com/riverfjs/agentsdk-go/pkg/api"
)

// mockSessionResetter implements SessionResetter interface
type mockSessionResetter struct {
	clearFunc func(sessionID string) error
}

func (m *mockSessionResetter) ClearSession(sessionID string) error {
	if m.clearFunc != nil {
		return m.clearFunc(sessionID)
	}
	return nil
}

type mockUsageReporter struct {
	mockSessionResetter
	session *api.SessionTokenStats
	total   *api.SessionTokenStats
}

func (m *mockUsageReporter) GetSessionStats(sessionID string) *api.SessionTokenStats {
	return m.session
}

func (m *mockUsageReporter) GetTotalStats() *api.SessionTokenStats {
	return m.total
}

func TestCommandHandler_HandleStart(t *testing.T) {
	handler := NewCommandHandler(nil, "", 200000)
	
	msg := bus.InboundMessage{
		Channel:  "test",
		ChatID:   "123",
		SenderID: "user1",
		Content:  "/start",
	}
	
	result := handler.HandleCommand(msg)
	
	if !result.Handled {
		t.Error("Expected /start to be handled")
	}
	
	if result.Response == "" {
		t.Error("Expected non-empty response")
	}
	
	if !contains(result.Response, "Aevitas") {
		t.Errorf("Expected 'Aevitas' in response, got: %s", result.Response)
	}
}

func TestCommandHandler_HandleHelp(t *testing.T) {
	handler := NewCommandHandler(nil, "", 200000)
	
	msg := bus.InboundMessage{
		Channel:  "test",
		ChatID:   "123",
		SenderID: "user1",
		Content:  "/help",
	}
	
	result := handler.HandleCommand(msg)
	
	if !result.Handled {
		t.Error("Expected /help to be handled")
	}
	
	if result.Response == "" {
		t.Error("Expected non-empty response")
	}
	
	if !contains(result.Response, "/start") || !contains(result.Response, "/reset") {
		t.Errorf("Expected command list in response, got: %s", result.Response)
	}
	if !contains(result.Response, "/cleanup - Clean temp/tts/var files") {
		t.Errorf("Expected updated cleanup help text, got: %s", result.Response)
	}
}

func TestCommandHandler_HandleReset_Success(t *testing.T) {
	resetCalled := false
	var capturedSessionKey string
	
	resetter := &mockSessionResetter{
		clearFunc: func(sessionID string) error {
			resetCalled = true
			capturedSessionKey = sessionID
			return nil
		},
	}
	
	handler := NewCommandHandler(resetter, "", 200000)
	
	msg := bus.InboundMessage{
		Channel:  "telegram",
		ChatID:   "123",
		SenderID: "user1",
		Content:  "/reset",
	}
	
	result := handler.HandleCommand(msg)
	
	if !result.Handled {
		t.Error("Expected /reset to be handled")
	}
	
	if !resetCalled {
		t.Error("Expected reset function to be called")
	}
	
	expectedSessionKey := "telegram:123"
	if capturedSessionKey != expectedSessionKey {
		t.Errorf("Expected session key %s, got %s", expectedSessionKey, capturedSessionKey)
	}
	
	if !contains(result.Response, "✅") || !contains(result.Response, "Reset") {
		t.Errorf("Expected success message, got: %s", result.Response)
	}
}

func TestCommandHandler_HandleReset_NoFunction(t *testing.T) {
	handler := NewCommandHandler(nil, "", 200000)
	
	msg := bus.InboundMessage{
		Channel:  "test",
		ChatID:   "123",
		SenderID: "user1",
		Content:  "/reset",
	}
	
	result := handler.HandleCommand(msg)
	
	if !result.Handled {
		t.Error("Expected /reset to be handled")
	}
	
	if !contains(result.Response, "not available") {
		t.Errorf("Expected unavailable message, got: %s", result.Response)
	}
}

func TestCommandHandler_HandleReset_Error(t *testing.T) {
	testErr := errors.New("reset failed")
	
	resetter := &mockSessionResetter{
		clearFunc: func(sessionID string) error {
			return testErr
		},
	}
	
	handler := NewCommandHandler(resetter, "", 200000)
	
	msg := bus.InboundMessage{
		Channel:  "test",
		ChatID:   "123",
		SenderID: "user1",
		Content:  "/reset",
	}
	
	result := handler.HandleCommand(msg)
	
	if !result.Handled {
		t.Error("Expected /reset to be handled")
	}
	
	if !contains(result.Response, "❌") || !contains(result.Response, "Failed") {
		t.Errorf("Expected error message, got: %s", result.Response)
	}
}

func TestCommandHandler_UnknownCommand(t *testing.T) {
	handler := NewCommandHandler(nil, "", 200000)
	
	msg := bus.InboundMessage{
		Channel:  "test",
		ChatID:   "123",
		SenderID: "user1",
		Content:  "/unknown",
	}
	
	result := handler.HandleCommand(msg)
	
	if !result.Handled {
		t.Error("Expected unknown command to be handled with error message")
	}
	
	if !contains(result.Response, "Unknown command") || !contains(result.Response, "/unknown") {
		t.Errorf("Expected unknown command error message, got: %s", result.Response)
	}
}

func TestCommandHandler_NotACommand(t *testing.T) {
	handler := NewCommandHandler(nil, "", 200000)
	
	msg := bus.InboundMessage{
		Channel:  "test",
		ChatID:   "123",
		SenderID: "user1",
		Content:  "hello world",
	}
	
	result := handler.HandleCommand(msg)
	
	if result.Handled {
		t.Error("Expected regular message not to be handled as command")
	}
	
	if result.Response != "" {
		t.Errorf("Expected empty response for regular message, got: %s", result.Response)
	}
}

func TestCommandHandler_EmptyContent(t *testing.T) {
	handler := NewCommandHandler(nil, "", 200000)
	
	msg := bus.InboundMessage{
		Channel:  "test",
		ChatID:   "123",
		SenderID: "user1",
		Content:  "",
	}
	
	result := handler.HandleCommand(msg)
	
	if result.Handled {
		t.Error("Expected empty content not to be handled")
	}
}

func TestCommandHandler_CaseInsensitive(t *testing.T) {
	handler := NewCommandHandler(nil, "", 200000)
	
	testCases := []string{"/START", "/Start", "/StArT", "/HELP", "/Help", "/RESET", "/Reset"}
	
	for _, cmd := range testCases {
		msg := bus.InboundMessage{
			Channel:  "test",
			ChatID:   "123",
			SenderID: "user1",
			Content:  cmd,
		}
		
		result := handler.HandleCommand(msg)
		
		if !result.Handled {
			t.Errorf("Expected %s to be handled (case insensitive)", cmd)
		}
	}
}

func TestCommandHandler_WithWhitespace(t *testing.T) {
	handler := NewCommandHandler(nil, "", 200000)
	
	msg := bus.InboundMessage{
		Channel:  "test",
		ChatID:   "123",
		SenderID: "user1",
		Content:  "  /start  ",
	}
	
	result := handler.HandleCommand(msg)
	
	if !result.Handled {
		t.Error("Expected /start with whitespace to be handled")
	}
}

func TestCommandHandler_CommandWithArgs(t *testing.T) {
	handler := NewCommandHandler(nil, "", 200000)
	
	msg := bus.InboundMessage{
		Channel:  "test",
		ChatID:   "123",
		SenderID: "user1",
		Content:  "/start some extra args",
	}
	
	result := handler.HandleCommand(msg)
	
	if !result.Handled {
		t.Error("Expected /start with args to be handled")
	}
}

func TestCommandHandler_HandleUsage_Default(t *testing.T) {
	reporter := &mockUsageReporter{
		session: &api.SessionTokenStats{
			TotalInput:          100,
			TotalOutput:         50,
			TotalTokens:         150,
			CacheCreated:        10,
			CacheRead:           20,
		},
	}
	handler := NewCommandHandler(reporter, "", 200000)
	msg := bus.InboundMessage{Channel: "telegram", ChatID: "1", SenderID: "u", Content: "/usage"}
	result := handler.HandleCommand(msg)
	if !result.Handled {
		t.Fatal("expected /usage handled")
	}
	if result.Event != "usage_hud" {
		t.Fatalf("expected usage_hud event, got %q", result.Event)
	}
	if !strings.Contains(result.Response, "Usage (Current Session)") ||
		!strings.Contains(result.Response, "Context window:") ||
		!strings.Contains(result.Response, "⬜") ||
		!strings.Contains(result.Response, "(100/200000)") ||
		!strings.Contains(result.Response, "Total billed tokens: 150") ||
		!strings.Contains(result.Response, "Input: 100 | Output: 50 | Cache: 30 | Total: 150") {
		t.Fatalf("unexpected response: %s", result.Response)
	}
}

func TestCommandHandler_HandleUsage_Total(t *testing.T) {
	reporter := &mockUsageReporter{
		total: &api.SessionTokenStats{
			TotalInput:   500,
			TotalOutput:  200,
			TotalTokens:  700,
		},
	}
	handler := NewCommandHandler(reporter, "", 200000)
	msg := bus.InboundMessage{Channel: "telegram", ChatID: "1", SenderID: "u", Content: "/usage total"}
	result := handler.HandleCommand(msg)
	if !result.Handled {
		t.Fatal("expected /usage total handled")
	}
	if result.Event != "usage_hud" {
		t.Fatalf("expected usage_hud event, got %q", result.Event)
	}
	if !strings.Contains(result.Response, "Usage (Total)") ||
		!strings.Contains(result.Response, "⬜") ||
		!strings.Contains(result.Response, "(500/200000)") ||
		!strings.Contains(result.Response, "Total billed tokens: 700") ||
		!strings.Contains(result.Response, "Input: 500 | Output: 200 | Cache: 0 | Total: 700") {
		t.Fatalf("unexpected response: %s", result.Response)
	}
}

func TestCommandHandler_HandleUsage_NoReporter(t *testing.T) {
	handler := NewCommandHandler(&mockSessionResetter{}, "", 200000)
	msg := bus.InboundMessage{Channel: "telegram", ChatID: "1", SenderID: "u", Content: "/usage"}
	result := handler.HandleCommand(msg)
	if !result.Handled {
		t.Fatal("expected /usage handled")
	}
	if !strings.Contains(result.Response, "not available") {
		t.Fatalf("unexpected response: %s", result.Response)
	}
}

func TestCommandHandler_CleanupScanIncludesTempTTSAndVar(t *testing.T) {
	workspace := t.TempDir()
	handler := NewCommandHandler(nil, workspace, 200000)

	chatID := fmt.Sprintf("cleanup-test-%d", os.Getpid())
	tempScreenshot := filepath.Join(os.TempDir(), fmt.Sprintf("screenshot-%s.png", chatID))
	nestedTempFile := filepath.Join(os.TempDir(), fmt.Sprintf("aevitas-%s", chatID), "agentsdk-nested.tmp")
	ttsFile := filepath.Join(workspace, ".claude", "voice", "tts", "sample.mp3")
	varFile := filepath.Join(workspace, "var", "python-conda-workspace", "work1", "generated.py")

	if err := os.WriteFile(tempScreenshot, []byte("x"), 0644); err != nil {
		t.Fatalf("failed to create temp screenshot: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(tempScreenshot) })

	if err := os.MkdirAll(filepath.Dir(nestedTempFile), 0755); err != nil {
		t.Fatalf("failed to create nested temp dir: %v", err)
	}
	if err := os.WriteFile(nestedTempFile, []byte("nested"), 0644); err != nil {
		t.Fatalf("failed to create nested temp file: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Remove(nestedTempFile)
		_ = os.Remove(filepath.Dir(nestedTempFile))
	})

	if err := os.MkdirAll(filepath.Dir(ttsFile), 0755); err != nil {
		t.Fatalf("failed to create tts dir: %v", err)
	}
	if err := os.WriteFile(ttsFile, []byte("audio"), 0644); err != nil {
		t.Fatalf("failed to create tts cache file: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(ttsFile) })

	if err := os.MkdirAll(filepath.Dir(varFile), 0755); err != nil {
		t.Fatalf("failed to create var dir: %v", err)
	}
	if err := os.WriteFile(varFile, []byte("print('ok')"), 0644); err != nil {
		t.Fatalf("failed to create var file: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(varFile) })

	result := handler.handleCleanupScan(chatID)
	if !result.Handled {
		t.Fatal("expected cleanup scan to be handled")
	}
	if !contains(result.Response, "Temporary Files Found") {
		t.Fatalf("unexpected cleanup scan response: %s", result.Response)
	}

	cleanupFile := filepath.Join(os.TempDir(), fmt.Sprintf("cleanup_%s.txt", chatID))
	t.Cleanup(func() { _ = os.Remove(cleanupFile) })

	data, err := os.ReadFile(cleanupFile)
	if err != nil {
		t.Fatalf("failed to read cleanup list: %v", err)
	}
	list := string(data)
	if !contains(list, tempScreenshot) {
		t.Fatalf("cleanup list missing temp screenshot: %s", list)
	}
	if !contains(list, nestedTempFile) {
		t.Fatalf("cleanup list missing nested temp file: %s", list)
	}
	if !contains(list, ttsFile) {
		t.Fatalf("cleanup list missing tts file: %s", list)
	}
	if !contains(list, varFile) {
		t.Fatalf("cleanup list missing var file: %s", list)
	}
}

func TestCommandHandler_CleanupConfirmScopeVar(t *testing.T) {
	workspace := t.TempDir()
	handler := NewCommandHandler(nil, workspace, 200000)
	chatID := fmt.Sprintf("cleanup-scope-%d", os.Getpid())

	ttsFile := filepath.Join(workspace, ".claude", "voice", "tts", "keep.mp3")
	varFile := filepath.Join(workspace, "var", "python-conda-workspace", "work2", "delete.py")
	if err := os.MkdirAll(filepath.Dir(ttsFile), 0755); err != nil {
		t.Fatalf("mkdir tts dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(varFile), 0755); err != nil {
		t.Fatalf("mkdir var dir: %v", err)
	}
	if err := os.WriteFile(ttsFile, []byte("audio"), 0644); err != nil {
		t.Fatalf("write tts file: %v", err)
	}
	if err := os.WriteFile(varFile, []byte("print('x')"), 0644); err != nil {
		t.Fatalf("write var file: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Remove(ttsFile)
		_ = os.Remove(varFile)
		_ = os.Remove(filepath.Join(os.TempDir(), fmt.Sprintf("cleanup_%s.txt", chatID)))
	})

	scan := handler.handleCleanupScan(chatID)
	if !scan.Handled {
		t.Fatal("expected cleanup scan to be handled")
	}

	confirm := handler.handleCleanupConfirm(chatID, "var")
	if !confirm.Handled {
		t.Fatal("expected cleanup confirm to be handled")
	}
	if !contains(confirm.Response, "Scope: var") {
		t.Fatalf("unexpected confirm response: %s", confirm.Response)
	}
	if _, err := os.Stat(varFile); !os.IsNotExist(err) {
		t.Fatalf("expected var file deleted, got err=%v", err)
	}
	if _, err := os.Stat(ttsFile); err != nil {
		t.Fatalf("expected tts file kept, stat err=%v", err)
	}
}

func TestCommandHandler_HandleRestart_PreparesTrigger(t *testing.T) {
	tmpHome := t.TempDir()
	oldHome := os.Getenv("HOME")
	if err := os.Setenv("HOME", tmpHome); err != nil {
		t.Fatalf("set HOME: %v", err)
	}
	defer func() { _ = os.Setenv("HOME", oldHome) }()

	handler := NewCommandHandler(nil, "", 200000)
	scriptPath := handler.RestartScriptPath()
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0755); err != nil {
		t.Fatalf("mkdir script dir: %v", err)
	}
	if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\n"), 0755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	msg := bus.InboundMessage{
		Channel:  "feishu",
		ChatID:   "oc_123",
		SenderID: "u1",
		Content:  "/restart",
	}
	res := handler.HandleCommand(msg)
	if !res.Handled {
		t.Fatal("expected /restart handled")
	}
	if !res.Restart {
		t.Fatal("expected restart action")
	}
	want := "🔄 Restarting Gateway\n\nThe gateway will restart in a few seconds. You'll receive a notification when it's back online."
	if res.Response != want {
		t.Fatalf("unexpected response: %q", res.Response)
	}
	trigger := filepath.Join(tmpHome, ".aevitas", "restart_trigger.txt")
	data, err := os.ReadFile(trigger)
	if err != nil {
		t.Fatalf("read trigger: %v", err)
	}
	if strings.TrimSpace(string(data)) != "feishu:oc_123" {
		t.Fatalf("unexpected trigger content: %q", string(data))
	}
}

func TestCommandHandler_HandleRestart_NoScript(t *testing.T) {
	tmpHome := t.TempDir()
	oldHome := os.Getenv("HOME")
	if err := os.Setenv("HOME", tmpHome); err != nil {
		t.Fatalf("set HOME: %v", err)
	}
	defer func() { _ = os.Setenv("HOME", oldHome) }()

	handler := NewCommandHandler(nil, "", 200000)
	msg := bus.InboundMessage{
		Channel:  "telegram",
		ChatID:   "123",
		SenderID: "u1",
		Content:  "/restart",
	}
	res := handler.HandleCommand(msg)
	if !res.Handled {
		t.Fatal("expected /restart handled")
	}
	if res.Restart {
		t.Fatal("restart should be false when script missing")
	}
	if !strings.Contains(res.Response, "Restart script not found") {
		t.Fatalf("unexpected response: %s", res.Response)
	}
	trigger := filepath.Join(tmpHome, ".aevitas", "restart_trigger.txt")
	if _, err := os.Stat(trigger); !os.IsNotExist(err) {
		t.Fatalf("trigger file should not exist when restart unavailable: %v", err)
	}
}

// Helper function to check if string contains substring
func contains(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	if len(s) < len(substr) {
		return false
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

