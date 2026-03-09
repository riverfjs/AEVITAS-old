package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riverfjs/aevitas/internal/bus"
	"github.com/riverfjs/aevitas/internal/channel"
	"github.com/riverfjs/aevitas/internal/config"
	"github.com/riverfjs/aevitas/internal/cron"
	"github.com/riverfjs/aevitas/internal/heartbeat"
	"github.com/riverfjs/aevitas/internal/protocol"
	"github.com/riverfjs/agentsdk-go/pkg/api"
	"github.com/riverfjs/agentsdk-go/pkg/core/events"
	sdklogger "github.com/riverfjs/agentsdk-go/pkg/logger"
	"github.com/riverfjs/agentsdk-go/pkg/tool"
	"go.uber.org/zap"
)

// newTestLogger returns a no-op logger for unit tests.
func newTestLogger() sdklogger.Logger {
	return sdklogger.NewZapLogger(zap.NewNop())
}

// mockRuntime implements Runtime interface for testing
type mockRuntime struct {
	response           *api.Response
	err                error
	streamCh           <-chan api.StreamEvent
	closed             bool
	clearSessionCalled bool
	clearSessionError  error
	sessionStats       *api.SessionTokenStats
	totalStats         *api.SessionTokenStats
}

type mockPluginRuntimeManager struct {
	running bool
	err     error
}

func (m *mockPluginRuntimeManager) StartEnabled() error { return nil }
func (m *mockPluginRuntimeManager) StopAll() error      { return nil }
func (m *mockPluginRuntimeManager) IsRunning(pluginID string) (bool, error) {
	if m.err != nil {
		return false, m.err
	}
	return m.running, nil
}

func (m *mockRuntime) Run(ctx context.Context, req api.Request) (*api.Response, error) {
	return m.response, m.err
}

func (m *mockRuntime) RunStream(ctx context.Context, req api.Request) (<-chan api.StreamEvent, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.streamCh != nil {
		return m.streamCh, nil
	}
	ch := make(chan api.StreamEvent, 2)
	ch <- api.StreamEvent{Type: api.EventFinalResponse, Output: m.response}
	close(ch)
	return ch, nil
}

func (m *mockRuntime) ClearSession(sessionID string) error {
	m.clearSessionCalled = true
	return m.clearSessionError
}

func (m *mockRuntime) GetSessionStats(sessionID string) *api.SessionTokenStats {
	return m.sessionStats
}

func (m *mockRuntime) GetTotalStats() *api.SessionTokenStats {
	return m.totalStats
}

func (m *mockRuntime) Close() {
	m.closed = true
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input string
		n     int
		want  string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"this is a long message", 10, "this is a ..."},
		{"", 5, ""},
	}

	for _, tt := range tests {
		got := truncate(tt.input, tt.n)
		if got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.n, got, tt.want)
		}
	}
}

func TestGateway_BuildSystemPrompt(t *testing.T) {
	tmpDir := t.TempDir()

	// Create workspace files
	os.WriteFile(filepath.Join(tmpDir, "AGENTS.md"), []byte("# Agent\nYou are helpful."), 0644)
	os.WriteFile(filepath.Join(tmpDir, "SOUL.md"), []byte("# Soul\nBe kind."), 0644)

	cfg := &config.Config{
		Agent: config.AgentConfig{
			Workspace: tmpDir,
		},
	}

	g := &Gateway{
		cfg: cfg,
	}

	prompt := g.buildSystemPrompt()

	if prompt == "" {
		t.Error("expected non-empty prompt")
	}
	if !contains(prompt, "# Agent") {
		t.Error("missing AGENTS.md content")
	}
	if !contains(prompt, "# Soul") {
		t.Error("missing SOUL.md content")
	}
}

func TestGateway_BuildSystemPrompt_NoFiles(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &config.Config{
		Agent: config.AgentConfig{
			Workspace: tmpDir,
		},
	}

	g := &Gateway{
		cfg: cfg,
	}

	prompt := g.buildSystemPrompt()

	// Should return empty when no files exist
	if prompt != "" {
		t.Errorf("expected empty prompt, got %q", prompt)
	}
}

func TestGateway_Shutdown(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &config.Config{
		Agent: config.AgentConfig{
			Workspace: tmpDir,
		},
	}

	tl := newTestLogger()
	msgBus := bus.NewMessageBus(10)
	chMgr, _ := channel.NewChannelManager(&config.Config{}, msgBus, tl)
	cronSvc := cron.NewService(filepath.Join(tmpDir, "cron.json"), tl)
	mockRt := &mockRuntime{}

	g := &Gateway{
		cfg:      cfg,
		bus:      msgBus,
		channels: chMgr,
		cron:     cronSvc,
		hb:       heartbeat.New(tmpDir, nil, nil, 0, tl),
		runtime:  mockRt,
		logger:   tl,
	}

	err := g.Shutdown()
	if err != nil {
		t.Errorf("Shutdown error: %v", err)
	}
	if !mockRt.closed {
		t.Error("runtime should be closed")
	}
}

func TestGateway_RunAgent(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &config.Config{
		Agent: config.AgentConfig{
			Workspace: tmpDir,
		},
	}

	mockRt := &mockRuntime{
		response: &api.Response{
			Result: &api.Result{
				Output: "Hello from mock",
			},
		},
	}

	g := &Gateway{
		cfg:     cfg,
		runtime: mockRt,
	}

	result, err := g.runAgent(context.Background(), "test", "session1")
	if err != nil {
		t.Errorf("runAgent error: %v", err)
	}
	if result != "Hello from mock" {
		t.Errorf("result = %q, want 'Hello from mock'", result)
	}
}

func TestGateway_RunAgent_NilResponse(t *testing.T) {
	mockRt := &mockRuntime{response: nil}

	g := &Gateway{runtime: mockRt}

	result, err := g.runAgent(context.Background(), "test", "session1")
	if err != nil {
		t.Errorf("runAgent error: %v", err)
	}
	if result != "" {
		t.Errorf("result = %q, want empty", result)
	}
}

func TestGateway_RunAgent_NilResult(t *testing.T) {
	mockRt := &mockRuntime{response: &api.Response{Result: nil}}

	g := &Gateway{runtime: mockRt}

	result, err := g.runAgent(context.Background(), "test", "session1")
	if err != nil {
		t.Errorf("runAgent error: %v", err)
	}
	if result != "" {
		t.Errorf("result = %q, want empty", result)
	}
}

func TestGateway_ProcessLoop(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &config.Config{
		Agent: config.AgentConfig{
			Workspace: tmpDir,
		},
	}

	msgBus := bus.NewMessageBus(10)
	mockRt := &mockRuntime{
		response: &api.Response{
			Result: &api.Result{Output: "response"},
		},
	}

	g := &Gateway{
		cfg:     cfg,
		bus:     msgBus,
		runtime: mockRt,
		logger:  newTestLogger(),
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Start process loop
	go g.processLoop(ctx)

	// Send inbound message
	msgBus.Inbound <- bus.InboundMessage{
		Channel:  "test",
		SenderID: "user1",
		ChatID:   "chat1",
		Content:  "hello",
	}

	// Wait for outbound message
	select {
	case outMsg := <-msgBus.Outbound:
		if outMsg.Content != "response" {
			t.Errorf("outbound content = %q, want 'response'", outMsg.Content)
		}
		if outMsg.Channel != "test" {
			t.Errorf("outbound channel = %q, want 'test'", outMsg.Channel)
		}
	case <-time.After(time.Second):
		t.Error("timeout waiting for outbound message")
	}

	cancel()
}

func TestGateway_DeliverAgentResponse_WithTelegramPreviewFinal(t *testing.T) {
	msgBus := bus.NewMessageBus(10)
	g := &Gateway{
		bus:          msgBus,
		logger:       newTestLogger(),
		outboundState: map[string]outboundMessageState{},
	}
	in := bus.InboundMessage{
		Channel: "telegram",
		ChatID:  "chat1",
	}
	g.setOutboundMessageID("telegram", "chat1", outboundMessagePreview, "mid-1")
	resp := &api.Response{
		Result: &api.Result{Output: "final answer"},
	}

	g.deliverAgentResponse(in, resp, true)

	select {
	case out := <-msgBus.Outbound:
		if out.Content != "final answer" {
			t.Fatalf("unexpected content: %q", out.Content)
		}
		if mode, _ := out.Metadata[protocol.EventTypeKey].(string); mode != protocol.EventPreviewFinal {
			t.Fatalf("expected preview final metadata, got %#v", out.Metadata)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting outbound message")
	}
}

func TestBuildApprovalPrompt_PrefersDisplay(t *testing.T) {
	got := buildApprovalPrompt("Bash", "cd /a && make prod", "cd:/a | make:prod")
	if !strings.Contains(got, "cd /a && make prod") {
		t.Fatalf("expected prompt to include display command, got %q", got)
	}
	if strings.Contains(got, "cd:/a | make:prod") {
		t.Fatalf("prompt should not include internal target when display exists: %q", got)
	}
}

func TestGateway_DeliverAgentResponse_StopReasonFallback(t *testing.T) {
	msgBus := bus.NewMessageBus(10)
	g := &Gateway{
		bus:    msgBus,
		logger: newTestLogger(),
	}
	in := bus.InboundMessage{
		Channel: "feishu",
		ChatID:  "oc_1",
	}
	resp := &api.Response{
		Result: &api.Result{
			Output:     "",
			StopReason: api.StopReasonPermissionDenied,
		},
	}

	g.deliverAgentResponse(in, resp, false)

	select {
	case out := <-msgBus.Outbound:
		if !strings.Contains(out.Content, "已拒绝执行该命令") {
			t.Fatalf("unexpected fallback content: %q", out.Content)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting stop reason fallback message")
	}
}

func TestGateway_DeliverAgentResponse_StopReasonFallback_NoPreviewFinalWhenPreviewNotSent(t *testing.T) {
	msgBus := bus.NewMessageBus(10)
	g := &Gateway{
		bus:    msgBus,
		logger: newTestLogger(),
	}
	in := bus.InboundMessage{
		Channel: "telegram",
		ChatID:  "chat_1",
	}
	resp := &api.Response{
		Result: &api.Result{
			Output:     "",
			StopReason: api.StopReasonPermissionDenied,
		},
	}

	g.deliverAgentResponse(in, resp, false)

	select {
	case out := <-msgBus.Outbound:
		if !strings.Contains(out.Content, "已拒绝执行该命令") {
			t.Fatalf("unexpected fallback content: %q", out.Content)
		}
		if out.Metadata != nil {
			if mode, _ := out.Metadata[protocol.EventTypeKey].(string); mode == protocol.EventPreviewFinal {
				t.Fatalf("did not expect preview final metadata when preview not sent, got %#v", out.Metadata)
			}
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting stop reason preview final message")
	}
}

func TestGateway_DeliverAgentResponse_EmitsTelegramUsageHUD(t *testing.T) {
	msgBus := bus.NewMessageBus(10)
	g := &Gateway{
		cfg: &config.Config{
			Agent: config.AgentConfig{
				ContextWindow: config.ContextWindowConfig{Tokens: 200000},
			},
		},
		bus: msgBus,
		runtime: &mockRuntime{
			sessionStats: &api.SessionTokenStats{
				TotalInput:   1200,
				TotalOutput:  300,
				TotalTokens:  1500,
				CacheRead:    40,
				CacheCreated: 10,
			},
		},
		logger:        newTestLogger(),
		usageNotified: make(map[string]uint8),
	}
	in := bus.InboundMessage{Channel: "telegram", ChatID: "chat1", SenderID: "u1", Content: "hello"}
	resp := &api.Response{
		Result: &api.Result{Output: "final answer"},
	}
	resp.Result.Usage.InputTokens = 70000
	resp.Result.Usage.OutputTokens = 100

	g.deliverAgentResponse(in, resp, false)

	var (
		gotResult bool
		gotUsage  bool
	)
	timeout := time.After(time.Second)
	for !(gotResult && gotUsage) {
		select {
		case out := <-msgBus.Outbound:
			if out.Content == "final answer" {
				gotResult = true
			}
			if out.Metadata != nil {
				if v, ok := out.Metadata[protocol.EventTypeKey].(string); ok && v == protocol.EventUsageHUD {
					gotUsage = true
					if !strings.Contains(out.Content, "Context window:") {
						t.Fatalf("expected context window in usage hud, got %q", out.Content)
					}
					if !strings.Contains(out.Content, "🟨") || !strings.Contains(out.Content, "⬜") {
						t.Fatalf("expected emoji usage bar in usage hud, got %q", out.Content)
					}
				}
			}
		case <-timeout:
			t.Fatalf("timeout waiting for result+usage (result=%v usage=%v)", gotResult, gotUsage)
		}
	}
}

func TestGateway_DeliverAgentResponse_UsageThresholdAndDedup(t *testing.T) {
	msgBus := bus.NewMessageBus(20)
	g := &Gateway{
		cfg: &config.Config{
			Agent: config.AgentConfig{
				ContextWindow: config.ContextWindowConfig{Tokens: 200000},
			},
		},
		bus: msgBus,
		runtime: &mockRuntime{
			sessionStats: &api.SessionTokenStats{
				TotalInput:   2000,
				TotalOutput:  300,
				TotalTokens:  2300,
				CacheRead:    20,
				CacheCreated: 10,
			},
		},
		logger:        newTestLogger(),
		usageNotified: make(map[string]uint8),
	}
	in := bus.InboundMessage{Channel: "telegram", ChatID: "chat1", SenderID: "u1", Content: "hello"}
	resp := &api.Response{Result: &api.Result{Output: "ok"}}

	// <30% should not emit usage.
	resp.Result.Usage.InputTokens = 50000 // 25%
	g.deliverAgentResponse(in, resp, false)
	first := <-msgBus.Outbound
	if first.Content != "ok" {
		t.Fatalf("expected first result message, got %q", first.Content)
	}
	select {
	case out := <-msgBus.Outbound:
		t.Fatalf("did not expect usage below threshold, got %q", out.Content)
	case <-time.After(50 * time.Millisecond):
	}

	// >30% should emit usage once.
	resp.Result.Usage.InputTokens = 70000 // 35%
	g.deliverAgentResponse(in, resp, false)
	_ = <-msgBus.Outbound // result
	var gotUsage30 bool
	select {
	case out := <-msgBus.Outbound:
		gotUsage30 = out.Metadata[protocol.EventTypeKey] == protocol.EventUsageHUD
	case <-time.After(time.Second):
		t.Fatal("expected usage message at 30% threshold")
	}
	if !gotUsage30 {
		t.Fatal("expected telegram usage_hud event")
	}

	// Still between 30-50 should not emit again.
	resp.Result.Usage.InputTokens = 80000 // 40%
	g.deliverAgentResponse(in, resp, false)
	_ = <-msgBus.Outbound // result
	select {
	case out := <-msgBus.Outbound:
		t.Fatalf("expected no duplicate usage in same threshold band, got %q", out.Content)
	case <-time.After(50 * time.Millisecond):
	}

	// Cross 50 should emit again.
	resp.Result.Usage.InputTokens = 110000 // 55%
	g.deliverAgentResponse(in, resp, false)
	_ = <-msgBus.Outbound // result
	select {
	case out := <-msgBus.Outbound:
		if out.Metadata[protocol.EventTypeKey] != protocol.EventUsageHUD {
			t.Fatalf("expected usage event at 50%%, got %#v", out.Metadata)
		}
	case <-time.After(time.Second):
		t.Fatal("expected usage message at 50% threshold")
	}
}

func TestGateway_RunAgent_Error(t *testing.T) {
	mockRt := &mockRuntime{err: context.DeadlineExceeded}

	g := &Gateway{runtime: mockRt}

	_, err := g.runAgent(context.Background(), "test", "session1")
	if err != context.DeadlineExceeded {
		t.Errorf("expected DeadlineExceeded, got %v", err)
	}
}

func TestGateway_TryHandleApprovalResponse_NoPending_Consumed(t *testing.T) {
	msgBus := bus.NewMessageBus(10)
	g := &Gateway{
		bus:             msgBus,
		logger:          newTestLogger(),
		pendingApproval: make(map[string]pendingApproval),
	}
	msg := bus.InboundMessage{
		Channel:  "feishu",
		ChatID:   "oc_123",
		SenderID: "u1",
		Metadata: map[string]any{
			"approval_action": "deny",
			"approval_id":     "missing",
		},
	}

	if handled := g.tryHandleApprovalResponse(msg); !handled {
		t.Fatal("expected approval callback to be consumed when pending is missing")
	}

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for i := 0; i < 10; i++ {
		select {
		case out := <-msgBus.Outbound:
			t.Fatalf("expected no outbound for missing pending approval, got %q", out.Content)
		case <-ticker.C:
		}
	}
}

func TestGateway_TryHandleApprovalResponse_MismatchedID_Consumed(t *testing.T) {
	msgBus := bus.NewMessageBus(10)
	decisionC := make(chan events.PermissionDecisionType, 1)
	g := &Gateway{
		bus:    msgBus,
		logger: newTestLogger(),
		pendingApproval: map[string]pendingApproval{
			"feishu:oc_123": {
				requestID: "req-1",
				decisionC: decisionC,
			},
		},
	}
	msg := bus.InboundMessage{
		Channel:  "feishu",
		ChatID:   "oc_123",
		SenderID: "u1",
		Metadata: map[string]any{
			"approval_action": "deny",
			"approval_id":     "req-2",
		},
	}

	if handled := g.tryHandleApprovalResponse(msg); !handled {
		t.Fatal("expected mismatched approval callback to be consumed")
	}

	select {
	case d := <-decisionC:
		t.Fatalf("decision channel should not receive value on mismatch, got %v", d)
	default:
	}

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for i := 0; i < 10; i++ {
		select {
		case out := <-msgBus.Outbound:
			t.Fatalf("expected no outbound for mismatched approval id, got %q", out.Content)
		case <-ticker.C:
		}
	}
}

func TestGateway_ProcessLoop_AgentError(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &config.Config{
		Agent: config.AgentConfig{
			Workspace: tmpDir,
		},
	}

	msgBus := bus.NewMessageBus(10)
	mockRt := &mockRuntime{err: context.DeadlineExceeded}

	g := &Gateway{
		cfg:     cfg,
		bus:     msgBus,
		runtime: mockRt,
		logger:  newTestLogger(),
	}

	ctx, cancel := context.WithCancel(context.Background())

	go g.processLoop(ctx)

	msgBus.Inbound <- bus.InboundMessage{
		Channel:  "test",
		SenderID: "user1",
		ChatID:   "chat1",
		Content:  "hello",
	}

	select {
	case outMsg := <-msgBus.Outbound:
		if outMsg.Content == "" {
			t.Errorf("expected non-empty error message, got %q", outMsg.Content)
		}
	case <-time.After(time.Second):
		t.Error("timeout waiting for error response")
	}

	cancel()
}

func TestGateway_ProcessLoop_EmptyResult(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &config.Config{
		Agent: config.AgentConfig{
			Workspace: tmpDir,
		},
	}

	msgBus := bus.NewMessageBus(10)
	mockRt := &mockRuntime{
		response: &api.Response{
			Result: &api.Result{Output: ""},
		},
	}

	g := &Gateway{
		cfg:     cfg,
		bus:     msgBus,
		runtime: mockRt,
		logger:  newTestLogger(),
	}

	ctx, cancel := context.WithCancel(context.Background())

	go g.processLoop(ctx)

	msgBus.Inbound <- bus.InboundMessage{
		Channel:  "test",
		SenderID: "user1",
		ChatID:   "chat1",
		Content:  "hello",
	}

	// Should NOT receive outbound message when result is empty
	select {
	case outMsg := <-msgBus.Outbound:
		t.Errorf("should not send empty result, got %q", outMsg.Content)
	case <-time.After(100 * time.Millisecond):
		// Expected - no message sent
	}

	cancel()
}

func TestGateway_ProcessLoop_ContextCancelled(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &config.Config{
		Agent: config.AgentConfig{
			Workspace: tmpDir,
		},
	}

	msgBus := bus.NewMessageBus(10)
	mockRt := &mockRuntime{}

	g := &Gateway{
		cfg:     cfg,
		bus:     msgBus,
		runtime: mockRt,
		logger:  newTestLogger(),
	}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		g.processLoop(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
		// Expected - loop exited
	case <-time.After(time.Second):
		t.Error("processLoop did not exit after context cancel")
	}
}

func TestGateway_Shutdown_NilRuntime(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &config.Config{
		Agent: config.AgentConfig{
			Workspace: tmpDir,
		},
	}

	tl2 := newTestLogger()
	msgBus := bus.NewMessageBus(10)
	chMgr, _ := channel.NewChannelManager(&config.Config{}, msgBus, tl2)
	cronSvc := cron.NewService(filepath.Join(tmpDir, "cron.json"), tl2)

	g := &Gateway{
		cfg:      cfg,
		bus:      msgBus,
		channels: chMgr,
		cron:     cronSvc,
		hb:       heartbeat.New(tmpDir, nil, nil, 0, tl2),
		runtime:  nil,
		logger:   tl2,
	}

	err := g.Shutdown()
	if err != nil {
		t.Errorf("Shutdown error: %v", err)
	}
}

// mockRuntimeFactory returns a factory that creates mock runtimes
func mockRuntimeFactory(rt Runtime) RuntimeFactory {
	return func(cfg *config.Config, sysPrompt string, realtimeCallback func(api.RealtimeEvent), customTools []tool.Tool) (Runtime, error) {
		return rt, nil
	}
}

// errorRuntimeFactory returns a factory that always fails
func errorRuntimeFactory(err error) RuntimeFactory {
	return func(cfg *config.Config, sysPrompt string, realtimeCallback func(api.RealtimeEvent), customTools []tool.Tool) (Runtime, error) {
		return nil, err
	}
}

func TestNewWithOptions_MockRuntime(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &config.Config{
		Agent: config.AgentConfig{
			Workspace: tmpDir,
		},
		Channels: config.ChannelsConfig{},
	}

	mockRt := &mockRuntime{
		response: &api.Response{
			Result: &api.Result{Output: "test"},
		},
	}

	g, err := NewWithOptions(cfg, Options{
		RuntimeFactory: mockRuntimeFactory(mockRt),
	})
	if err != nil {
		t.Fatalf("NewWithOptions error: %v", err)
	}

	if g == nil {
		t.Fatal("gateway should not be nil")
	}
	if g.runtime != mockRt {
		t.Error("runtime should be the mock")
	}
	if g.bus == nil {
		t.Error("bus should not be nil")
	}
	if g.cron == nil {
		t.Error("cron should not be nil")
	}
	if g.hb == nil {
		t.Error("heartbeat should not be nil")
	}
	if g.channels == nil {
		t.Error("channels should not be nil")
	}

	// Clean up
	g.Shutdown()
}

func TestNewWithOptions_RuntimeFactoryError(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &config.Config{
		Agent: config.AgentConfig{
			Workspace: tmpDir,
		},
	}

	_, err := NewWithOptions(cfg, Options{
		RuntimeFactory: errorRuntimeFactory(context.DeadlineExceeded),
	})
	if err != context.DeadlineExceeded {
		t.Errorf("expected DeadlineExceeded, got %v", err)
	}
}

func TestNewWithOptions_ChannelManagerError(t *testing.T) {
	tmpDir := t.TempDir()

	// Invalid telegram config to trigger channel manager error
	cfg := &config.Config{
		Agent: config.AgentConfig{
			Workspace: tmpDir,
		},
		Channels: config.ChannelsConfig{
			Telegram: config.TelegramConfig{
				Enabled: true,
				Token:   "", // Empty token with enabled=true may cause error
			},
		},
	}

	mockRt := &mockRuntime{}
	_, err := NewWithOptions(cfg, Options{
		RuntimeFactory: mockRuntimeFactory(mockRt),
	})
	// Channel manager may or may not error with empty token - just ensure we don't panic
	_ = err
}

func TestGateway_Run_WithSignalChan(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &config.Config{
		Agent: config.AgentConfig{
			Workspace: tmpDir,
		},
		Gateway: config.GatewayConfig{
			Host: "localhost",
			Port: 8080,
		},
		Channels: config.ChannelsConfig{},
	}

	mockRt := &mockRuntime{}
	sigCh := make(chan os.Signal, 1)

	g, err := NewWithOptions(cfg, Options{
		RuntimeFactory: mockRuntimeFactory(mockRt),
		SignalChan:     sigCh,
	})
	if err != nil {
		t.Fatalf("NewWithOptions error: %v", err)
	}

	// Run in goroutine
	done := make(chan error, 1)
	go func() {
		done <- g.Run(context.Background())
	}()

	// Give it time to start
	time.Sleep(50 * time.Millisecond)

	// Send shutdown signal
	sigCh <- os.Interrupt

	// Wait for Run to complete
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("Run did not exit after signal")
	}

	if !mockRt.closed {
		t.Error("runtime should be closed after shutdown")
	}
}

func TestGateway_Run_ChannelStartError_DoesNotBlockCoreStartup(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &config.Config{
		Agent: config.AgentConfig{
			Workspace: tmpDir,
		},
		Gateway: config.GatewayConfig{
			Host: "localhost",
			Port: 8080,
		},
		Channels: config.ChannelsConfig{
			Telegram: config.TelegramConfig{
				Enabled: true,
				Token:   "invalid-token", // Will fail on StartAll
			},
		},
	}

	mockRt := &mockRuntime{}
	sigCh := make(chan os.Signal, 1)

	g, err := NewWithOptions(cfg, Options{
		RuntimeFactory: mockRuntimeFactory(mockRt),
		SignalChan:     sigCh,
	})
	if err != nil {
		t.Fatalf("NewWithOptions error: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- g.Run(context.Background())
	}()

	// Give startup time; channel may fail, but core should keep running.
	time.Sleep(120 * time.Millisecond)
	sigCh <- os.Interrupt

	select {
	case runErr := <-done:
		if runErr != nil {
			t.Errorf("Run should not fail when channel start fails: %v", runErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not exit after signal")
	}
}

func TestGateway_RealtimeModelSwitch_EmitsStandaloneMessage(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Agent: config.AgentConfig{
			Workspace: tmpDir,
		},
		Channels: config.ChannelsConfig{},
	}

	mockRt := &mockRuntime{}
	var captured func(api.RealtimeEvent)
	factory := func(cfg *config.Config, sysPrompt string, realtimeCallback func(api.RealtimeEvent), customTools []tool.Tool) (Runtime, error) {
		captured = realtimeCallback
		return mockRt, nil
	}

	g, err := NewWithOptions(cfg, Options{RuntimeFactory: factory})
	if err != nil {
		t.Fatalf("NewWithOptions error: %v", err)
	}
	defer g.Shutdown()
	if captured == nil {
		t.Fatal("realtime callback should be captured")
	}

	g.currentChannelID = "telegram"
	g.currentChatID = "5821086579"
	g.currentReplyTo = "123"

	captured(api.RealtimeEvent{
		Type:    api.RealtimeEventModelSwitch,
		Message: "Model fallback switch: anthropic/claude-opus-4.6 -> deepseek/deepseek-v3.2",
	})

	select {
	case out := <-g.bus.Outbound:
		if out.Channel != "telegram" || out.ChatID != "5821086579" {
			t.Fatalf("unexpected outbound target: %+v", out)
		}
		if out.ReplyTo != "" {
			t.Fatalf("model switch notice should be standalone, got reply_to=%q", out.ReplyTo)
		}
		if !strings.Contains(out.Content, "fallback switch") {
			t.Fatalf("unexpected outbound content: %q", out.Content)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting model switch outbound message")
	}
}

func TestGateway_HandleOutboundControl_RequestIDPrecedence(t *testing.T) {
	g := &Gateway{
		outboundState: make(map[string]outboundMessageState),
	}
	meta := g.attachOutboundRequest("interaction", "chat-1", outboundMessagePreview, map[string]any{
		protocol.EventTypeKey: protocol.EventPreviewUpdate,
	})
	reqID, _ := meta["request_id"].(string)
	if strings.TrimSpace(reqID) == "" {
		t.Fatal("expected generated request_id")
	}
	handled := g.handleOutboundControl(bus.InboundMessage{
		Channel: "interaction",
		ChatID:  "chat-1",
		Metadata: map[string]any{
			protocol.EventTypeKey: protocol.EventOutboundResult,
			"request_id":          reqID,
			"source_event_type":   protocol.EventToolProgress,
			"message_id":          "mid-123",
			protocol.SessionChatIDKey: "chat-1",
		},
	})
	if !handled {
		t.Fatal("expected outbound control message handled")
	}
	if got := g.outboundMessageID("interaction", "chat-1", outboundMessagePreview); got != "mid-123" {
		t.Fatalf("expected preview message id from request mapping, got %q", got)
	}
	if got := g.outboundMessageID("interaction", "chat-1", outboundMessageTool); got != "" {
		t.Fatalf("expected tool message id unchanged, got %q", got)
	}
}

func TestGateway_FinalizePreviewState_AttachesRequestAndMessageID(t *testing.T) {
	g := &Gateway{
		logger:        newTestLogger(),
		outboundState: make(map[string]outboundMessageState),
	}

	meta := g.finalizePreviewState("telegram", "chat-1", true)
	if len(meta) != 0 {
		t.Fatalf("expected empty metadata before anchor ack, got %#v", meta)
	}
	g.setOutboundMessageID("telegram", "chat-1", outboundMessagePreview, "mid-1")

	meta2 := g.finalizePreviewState("telegram", "chat-1", true)
	if mode, _ := meta2[protocol.EventTypeKey].(string); mode != protocol.EventPreviewFinal {
		t.Fatalf("expected preview final metadata, got %#v", meta2)
	}
	reqID, _ := meta2[protocol.RequestIDKey].(string)
	if strings.TrimSpace(reqID) == "" {
		t.Fatalf("expected request_id in metadata, got %#v", meta2)
	}
	if got, _ := meta2["message_id"].(string); got != "mid-1" {
		t.Fatalf("expected carry message_id in final metadata, got %#v", meta2)
	}
}

func TestGateway_FinalizePreviewState_NoPreviewSent_ReturnsEmpty(t *testing.T) {
	g := &Gateway{
		outboundState: make(map[string]outboundMessageState),
	}
	meta := g.finalizePreviewState("telegram", "chat-1", false)
	if len(meta) != 0 {
		t.Fatalf("expected empty metadata when preview not sent, got %#v", meta)
	}
}

func TestGateway_ApplyOutboundAck_InteractionMissingSessionChatID_Dropped(t *testing.T) {
	g := &Gateway{
		logger:        newTestLogger(),
		outboundState: make(map[string]outboundMessageState),
	}
	meta := g.attachOutboundRequest("interaction", "session-1", outboundMessagePreview, map[string]any{
		protocol.EventTypeKey: protocol.EventPreviewUpdate,
	})
	reqID, _ := meta[protocol.RequestIDKey].(string)
	if strings.TrimSpace(reqID) == "" {
		t.Fatal("expected generated request_id")
	}
	g.applyOutboundAck(bus.InboundMessage{
		Channel: "interaction",
		ChatID:  "ignored-chat",
		Metadata: map[string]any{
			protocol.EventTypeKey: protocol.EventOutboundResult,
			protocol.RequestIDKey: reqID,
			"source_event_type":   protocol.EventPreviewUpdate,
			"message_id":          "mid-should-not-apply",
		},
	})
	if got := g.outboundMessageID("interaction", "session-1", outboundMessagePreview); got != "" {
		t.Fatalf("expected ack dropped without session_chat_id, got %q", got)
	}
}

func TestPreviewFlowState_AdvancePreviewState_Matrix(t *testing.T) {
	t.Run("idle emits update when content grows", func(t *testing.T) {
		state := previewFlowState{phase: previewPhaseIdle}
		next, ok := state.advancePreviewState("hello", "")
		if !ok || next != "hello" {
			t.Fatalf("expected emit hello, got ok=%v next=%q", ok, next)
		}
		if state.lastPreviewLen != len("hello") || !state.previewSent {
			t.Fatalf("unexpected state after emit: %+v", state)
		}
	})

	t.Run("anchor pending buffers before ack", func(t *testing.T) {
		state := previewFlowState{phase: previewPhaseAnchorPendingAck}
		next, ok := state.advancePreviewState("hello", "")
		if ok || next != "" {
			t.Fatalf("expected no emit while pending ack, got ok=%v next=%q", ok, next)
		}
		if state.bufferedPreview != "hello" || state.lastPreviewLen != len("hello") || !state.previewSent {
			t.Fatalf("unexpected buffered state: %+v", state)
		}
	})

	t.Run("anchor pending flushes buffered content after ack", func(t *testing.T) {
		state := previewFlowState{
			phase:           previewPhaseAnchorPendingAck,
			bufferedPreview: "hello world",
		}
		next, ok := state.advancePreviewState("hello world", "mid-1")
		if !ok || next != "hello world" {
			t.Fatalf("expected buffered emit after ack, got ok=%v next=%q", ok, next)
		}
		if state.phase != previewPhaseAnchored || state.bufferedPreview != "" {
			t.Fatalf("expected anchored phase with empty buffer, got %+v", state)
		}
	})

	t.Run("anchored no-change does not emit", func(t *testing.T) {
		state := previewFlowState{
			phase:          previewPhaseAnchored,
			lastPreviewLen: len("hello"),
		}
		next, ok := state.advancePreviewState("hello", "mid-1")
		if ok || next != "" {
			t.Fatalf("expected no emit on unchanged content, got ok=%v next=%q", ok, next)
		}
	})
}

func TestGateway_DeliverStreamAccumulatedFallback_UsesUnifiedFinalPath(t *testing.T) {
	msgBus := bus.NewMessageBus(10)
	g := &Gateway{
		bus:          msgBus,
		logger:       newTestLogger(),
		outboundState: map[string]outboundMessageState{},
	}
	in := bus.InboundMessage{
		Channel: "telegram",
		ChatID:  "chat-acc",
	}
	g.setOutboundMessageID("telegram", "chat-acc", outboundMessagePreview, "mid-acc-1")

	g.deliverStreamAccumulatedFallback(in, "fallback final text", true)

	select {
	case out := <-msgBus.Outbound:
		if out.Content != "fallback final text" {
			t.Fatalf("unexpected content: %q", out.Content)
		}
		if mode, _ := out.Metadata[protocol.EventTypeKey].(string); mode != protocol.EventPreviewFinal {
			t.Fatalf("expected preview final metadata, got %#v", out.Metadata)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting fallback outbound")
	}
}

func TestGateway_ProcessAgentStream_AnchorsAfterAckAndEditsSameMessage(t *testing.T) {
	streamCh := make(chan api.StreamEvent, 8)
	msgBus := bus.NewMessageBus(20)
	g := &Gateway{
		bus:          msgBus,
		logger:       newTestLogger(),
		outboundState: map[string]outboundMessageState{},
		runtime: &mockRuntime{
			streamCh: streamCh,
		},
	}
	in := bus.InboundMessage{
		Channel:  "telegram",
		ChatID:   "chat-stream-1",
		SenderID: "u1",
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan bool, 1)
	go func() {
		done <- g.processAgentStream(ctx, in, api.Request{})
	}()

	streamCh <- api.StreamEvent{
		Type: api.EventContentBlockDelta,
		Delta: &api.Delta{
			Type: "text_delta",
			Text: "hello",
		},
	}

	var first bus.OutboundMessage
	select {
	case first = <-msgBus.Outbound:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting first preview update")
	}
	if mode, _ := first.Metadata[protocol.EventTypeKey].(string); mode != protocol.EventPreviewUpdate {
		t.Fatalf("expected first outbound preview update, got %#v", first.Metadata)
	}
	firstReqID, _ := first.Metadata[protocol.RequestIDKey].(string)
	if strings.TrimSpace(firstReqID) == "" {
		t.Fatalf("expected first preview request_id, got %#v", first.Metadata)
	}
	if got, _ := first.Metadata["message_id"].(string); strings.TrimSpace(got) != "" {
		t.Fatalf("expected first preview without target message_id before ack, got %#v", first.Metadata)
	}

	handled := g.handleOutboundControl(bus.InboundMessage{
		Channel: "telegram",
		ChatID:  "chat-stream-1",
		Metadata: map[string]any{
			protocol.EventTypeKey: protocol.EventOutboundResult,
			protocol.RequestIDKey: firstReqID,
			"source_event_type":   protocol.EventPreviewUpdate,
			"message_id":          "mid-anchor-1",
		},
	})
	if !handled {
		t.Fatal("expected outbound_result handled")
	}

	streamCh <- api.StreamEvent{
		Type: api.EventContentBlockDelta,
		Delta: &api.Delta{
			Type: "text_delta",
			Text: " world",
		},
	}

	var second bus.OutboundMessage
	select {
	case second = <-msgBus.Outbound:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting second preview update")
	}
	if mode, _ := second.Metadata[protocol.EventTypeKey].(string); mode != protocol.EventPreviewUpdate {
		t.Fatalf("expected second outbound preview update, got %#v", second.Metadata)
	}
	if got, _ := second.Metadata["message_id"].(string); got != "mid-anchor-1" {
		t.Fatalf("expected second preview target anchored message_id, got %#v", second.Metadata)
	}
	if second.Content != "hello world" {
		t.Fatalf("expected merged preview content, got %q", second.Content)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting stream loop exit")
	}
}

func TestGateway_FinalizePreviewState_UnsupportedChannel_ReturnsEmpty(t *testing.T) {
	g := &Gateway{
		outboundState: make(map[string]outboundMessageState),
	}
	meta := g.finalizePreviewState("slack", "chat-1", true)
	if len(meta) != 0 {
		t.Fatalf("expected empty metadata for unsupported channel, got %#v", meta)
	}
}

func TestGateway_FinalizePreviewState_InteractionAddsSessionChatID(t *testing.T) {
	g := &Gateway{
		outboundState: make(map[string]outboundMessageState),
	}
	chatID := "feishuOfficial:feishu:ou_abc"
	g.setOutboundMessageID("interaction", chatID, outboundMessagePreview, "om_1")
	meta := g.finalizePreviewState("interaction", chatID, true)
	if mode, _ := meta[protocol.EventTypeKey].(string); mode != protocol.EventPreviewFinal {
		t.Fatalf("expected preview final metadata, got %#v", meta)
	}
	if got, _ := meta[protocol.SessionChatIDKey].(string); got != chatID {
		t.Fatalf("expected session_chat_id=%q, got %#v", chatID, meta)
	}
}

func TestGateway_ApplyOutboundAck_RequestNotFound_NoMutation(t *testing.T) {
	g := &Gateway{
		logger:        newTestLogger(),
		outboundState: make(map[string]outboundMessageState),
	}
	g.applyOutboundAck(bus.InboundMessage{
		Channel: "telegram",
		ChatID:  "chat-404",
		Metadata: map[string]any{
			protocol.EventTypeKey: protocol.EventOutboundResult,
			protocol.RequestIDKey: "req-missing",
			"source_event_type":   protocol.EventPreviewUpdate,
			"message_id":          "mid-404",
		},
	})
	if got := g.outboundMessageID("telegram", "chat-404", outboundMessagePreview); got != "" {
		t.Fatalf("expected no mutation when request not found, got %q", got)
	}
}

func TestGateway_ApplyOutboundAck_MissingRequiredIDs_Dropped(t *testing.T) {
	g := &Gateway{
		logger:        newTestLogger(),
		outboundState: make(map[string]outboundMessageState),
	}
	meta := g.attachOutboundRequest("telegram", "chat-miss", outboundMessagePreview, map[string]any{
		protocol.EventTypeKey: protocol.EventPreviewUpdate,
	})
	reqID, _ := meta[protocol.RequestIDKey].(string)
	if strings.TrimSpace(reqID) == "" {
		t.Fatal("expected generated request_id")
	}

	// Missing message_id
	g.applyOutboundAck(bus.InboundMessage{
		Channel: "telegram",
		ChatID:  "chat-miss",
		Metadata: map[string]any{
			protocol.EventTypeKey: protocol.EventOutboundResult,
			protocol.RequestIDKey: reqID,
			"source_event_type":   protocol.EventPreviewUpdate,
		},
	})
	if got := g.outboundMessageID("telegram", "chat-miss", outboundMessagePreview); got != "" {
		t.Fatalf("expected drop when message_id missing, got %q", got)
	}

	// Missing request_id
	g.applyOutboundAck(bus.InboundMessage{
		Channel: "telegram",
		ChatID:  "chat-miss",
		Metadata: map[string]any{
			protocol.EventTypeKey: protocol.EventOutboundResult,
			"source_event_type":   protocol.EventPreviewUpdate,
			"message_id":          "mid-x",
		},
	})
	if got := g.outboundMessageID("telegram", "chat-miss", outboundMessagePreview); got != "" {
		t.Fatalf("expected drop when request_id missing, got %q", got)
	}
}

func TestGateway_ApplyOutboundAck_FlushesPendingFinalAfterPreviewAck(t *testing.T) {
	msgBus := bus.NewMessageBus(10)
	g := &Gateway{
		logger:        newTestLogger(),
		bus:           msgBus,
		outboundState: make(map[string]outboundMessageState),
	}
	in := bus.InboundMessage{Channel: "telegram", ChatID: "chat-flush"}
	g.deliverAgentResponse(in, &api.Response{Result: &api.Result{Output: "final text"}}, true)
	select {
	case <-msgBus.Outbound:
		t.Fatal("should not emit final before anchor ack")
	default:
	}
	key := toolLogChatKey("telegram", "chat-flush")
	if got := strings.TrimSpace(g.outboundState[key].PendingFinalContent); got != "final text" {
		t.Fatalf("expected pending final content, got %q", got)
	}

	meta := g.attachOutboundRequest("telegram", "chat-flush", outboundMessagePreview, map[string]any{
		protocol.EventTypeKey: protocol.EventPreviewUpdate,
	})
	reqID, _ := meta[protocol.RequestIDKey].(string)
	g.applyOutboundAck(bus.InboundMessage{
		Channel: "telegram",
		ChatID:  "chat-flush",
		Metadata: map[string]any{
			protocol.EventTypeKey: protocol.EventOutboundResult,
			protocol.RequestIDKey: reqID,
			"source_event_type":   protocol.EventPreviewUpdate,
			"message_id":          "mid-flush-1",
		},
	})
	select {
	case out := <-msgBus.Outbound:
		if out.Content != "final text" {
			t.Fatalf("unexpected flushed final content: %q", out.Content)
		}
		if mode, _ := out.Metadata[protocol.EventTypeKey].(string); mode != protocol.EventPreviewFinal {
			t.Fatalf("expected preview final flush, got %#v", out.Metadata)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting flushed pending final")
	}
	if got := strings.TrimSpace(g.outboundState[key].PendingFinalContent); got != "" {
		t.Fatalf("expected pending final cleared, got %q", got)
	}
}

func TestGateway_AttachOutboundRequest_InteractionAddsSessionChatID(t *testing.T) {
	g := &Gateway{
		outboundState: make(map[string]outboundMessageState),
	}
	meta := g.attachOutboundRequest("interaction", "feishuOfficial:feishu:ou_123", outboundMessagePreview, map[string]any{})
	if got, _ := meta[protocol.SessionChatIDKey].(string); got != "feishuOfficial:feishu:ou_123" {
		t.Fatalf("expected session_chat_id in metadata, got %q", got)
	}
}

func TestGateway_HandleOutboundControl_ToolCatalog(t *testing.T) {
	g := &Gateway{
		logger:           newTestLogger(),
		pluginToolsByKey: map[string][]pluginToolDescriptor{},
	}
	handled := g.handleOutboundControl(bus.InboundMessage{
		Channel: "interaction",
		ChatID:  "",
		Metadata: map[string]any{
			protocol.EventTypeKey: protocol.EventToolCatalog,
			"plugin_id":          "feishuOfficial",
			"platform":           "feishu",
			"tools": []any{
				map[string]any{
					"name":        "feishu_mcp_fetch_doc",
					"description": "fetch doc content",
					"input_schema": map[string]any{
						"type": "object",
					},
				},
			},
		},
	})
	if !handled {
		t.Fatal("expected tool_catalog control event handled")
	}
	key := pluginPlatformKey("feishuOfficial", "feishu")
	got := g.pluginToolsByKey[key]
	if len(got) != 1 {
		t.Fatalf("expected one tool in catalog, got %d", len(got))
	}
	if got[0].Name != "feishu_mcp_fetch_doc" {
		t.Fatalf("unexpected tool name: %q", got[0].Name)
	}
}

func TestGateway_SendStartupNotification_SkipsWhenChannelNotReady(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	trigger := filepath.Join(tmpHome, ".aevitas", "restart_trigger.txt")
	if err := os.MkdirAll(filepath.Dir(trigger), 0755); err != nil {
		t.Fatalf("mkdir trigger dir: %v", err)
	}
	raw1, _ := json.Marshal(map[string]string{"channel": "telegram", "chat_id": "5821086579"})
	if err := os.WriteFile(trigger, raw1, 0644); err != nil {
		t.Fatalf("write trigger file: %v", err)
	}

	msgBus := bus.NewMessageBus(10)
	g := &Gateway{
		bus:    msgBus,
		logger: newTestLogger(),
		channelStatesFn: func() map[string]channel.ChannelState {
			return map[string]channel.ChannelState{
				"telegram": {Running: false},
			}
		},
	}

	g.consumeRestartTriggerOnce()

	select {
	case <-msgBus.Outbound:
		t.Fatal("startup notification should be skipped when channel is not ready")
	default:
	}

	if _, err := os.Stat(trigger); err != nil {
		t.Fatalf("trigger file should be kept for later retry via event, err=%v", err)
	}
}

func TestGateway_SendStartupNotification_SendsImmediatelyWhenChannelRunning(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	trigger := filepath.Join(tmpHome, ".aevitas", "restart_trigger.txt")
	if err := os.MkdirAll(filepath.Dir(trigger), 0755); err != nil {
		t.Fatalf("mkdir trigger dir: %v", err)
	}
	raw2, _ := json.Marshal(map[string]string{"channel": "feishu", "chat_id": "oc_123"})
	if err := os.WriteFile(trigger, raw2, 0644); err != nil {
		t.Fatalf("write trigger file: %v", err)
	}

	msgBus := bus.NewMessageBus(10)
	sent := make(chan bus.OutboundMessage, 1)
	g := &Gateway{
		bus:    msgBus,
		logger: newTestLogger(),
		sendNowFn: func(msg bus.OutboundMessage) error {
			sent <- msg
			return nil
		},
	}

	g.consumeRestartTriggerOnce()

	select {
	case out := <-sent:
		if out.Channel != "feishu" || out.ChatID != "oc_123" {
			t.Fatalf("unexpected target: %+v", out)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting immediate startup notification")
	}
}

func TestGateway_SendStartupNotification_InteractionSentAfterHostReadyEvent(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	trigger := filepath.Join(tmpHome, ".aevitas", "restart_trigger.txt")
	if err := os.MkdirAll(filepath.Dir(trigger), 0755); err != nil {
		t.Fatalf("mkdir trigger dir: %v", err)
	}
	raw, _ := json.Marshal(map[string]string{
		"channel": "interaction",
		"chat_id": "feishuOfficial:feishu:oc_123",
	})
	if err := os.WriteFile(trigger, raw, 0644); err != nil {
		t.Fatalf("write trigger file: %v", err)
	}

	msgBus := bus.NewMessageBus(10)
	sent := make(chan bus.OutboundMessage, 1)
	g := &Gateway{
		bus:        msgBus,
		logger:     newTestLogger(),
		runtimeMgr: &mockPluginRuntimeManager{running: true},
		sendNowFn: func(msg bus.OutboundMessage) error {
			sent <- msg
			return nil
		},
		hostReadyByKey: map[string]bool{},
	}

	g.consumeRestartTriggerOnce()
	select {
	case <-msgBus.Outbound:
		t.Fatal("startup notification should not send before host_ready")
	default:
	}
	if _, err := os.Stat(trigger); err != nil {
		t.Fatalf("trigger should remain before host_ready, err=%v", err)
	}

	handled := g.handleOutboundControl(bus.InboundMessage{
		Channel: "interaction",
		ChatID:  "feishuOfficial:feishu:oc_123",
		Metadata: map[string]any{
			protocol.EventTypeKey: protocol.EventHostReady,
			"plugin_id":          "feishuOfficial",
			"platform":           "feishu",
		},
	})
	if !handled {
		t.Fatal("expected host_ready control event handled")
	}

	select {
	case out := <-sent:
		if out.Channel != "interaction" || out.ChatID != "feishuOfficial:feishu:oc_123" {
			t.Fatalf("unexpected target: %+v", out)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting startup notification after host_ready")
	}

	deadline := time.Now().Add(time.Second)
	for {
		_, err := os.Stat(trigger)
		if os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("trigger file should be removed after successful send, err=%v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestGateway_SendStartupNotification_SingleflightPreventsDuplicateSend(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	trigger := filepath.Join(tmpHome, ".aevitas", "restart_trigger.txt")
	if err := os.MkdirAll(filepath.Dir(trigger), 0755); err != nil {
		t.Fatalf("mkdir trigger dir: %v", err)
	}
	raw, _ := json.Marshal(map[string]string{"channel": "interaction", "chat_id": "feishuOfficial:feishu:oc_123"})
	if err := os.WriteFile(trigger, raw, 0644); err != nil {
		t.Fatalf("write trigger file: %v", err)
	}

	msgBus := bus.NewMessageBus(10)
	var mu sync.Mutex
	sendCount := 0
	release := make(chan struct{})
	g := &Gateway{
		bus:        msgBus,
		logger:     newTestLogger(),
		runtimeMgr: &mockPluginRuntimeManager{running: true},
		hostReadyByKey: map[string]bool{
			pluginPlatformKey("feishuOfficial", "feishu"): true,
		},
		sendNowFn: func(msg bus.OutboundMessage) error {
			mu.Lock()
			sendCount++
			mu.Unlock()
			<-release
			return nil
		},
	}

	done := make(chan struct{}, 2)
	go func() { g.consumeRestartTriggerOnce(); done <- struct{}{} }()
	go func() { g.consumeRestartTriggerOnce(); done <- struct{}{} }()
	time.Sleep(50 * time.Millisecond)
	close(release)
	<-done
	<-done

	mu.Lock()
	got := sendCount
	mu.Unlock()
	if got != 1 {
		t.Fatalf("expected single startup send, got %d", got)
	}
}

func TestGateway_RetryConsumeRestartTriggerForTelegram_WaitsReadyThenConsumes(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	trigger := filepath.Join(tmpHome, ".aevitas", "restart_trigger.txt")
	if err := os.MkdirAll(filepath.Dir(trigger), 0755); err != nil {
		t.Fatalf("mkdir trigger dir: %v", err)
	}
	raw, _ := json.Marshal(map[string]string{"channel": "telegram", "chat_id": "5821086579"})
	if err := os.WriteFile(trigger, raw, 0644); err != nil {
		t.Fatalf("write trigger file: %v", err)
	}

	sent := make(chan bus.OutboundMessage, 1)
	g := &Gateway{
		logger: newTestLogger(),
		sendNowFn: func(msg bus.OutboundMessage) error {
			sent <- msg
			return nil
		},
		waitReadyFn: func(ctx context.Context, channel string) bool {
			return channel == "telegram"
		},
	}

	g.retryConsumeRestartTriggerForTelegram(context.Background())

	select {
	case out := <-sent:
		if out.Channel != "telegram" || out.ChatID != "5821086579" {
			t.Fatalf("unexpected target: %+v", out)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting telegram retry consume notification")
	}
	if _, err := os.Stat(trigger); !os.IsNotExist(err) {
		t.Fatalf("trigger should be removed after retry consume success, err=%v", err)
	}
}

func TestGateway_RetryConsumeRestartTriggerForTelegram_NotReadySkipsConsume(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	trigger := filepath.Join(tmpHome, ".aevitas", "restart_trigger.txt")
	if err := os.MkdirAll(filepath.Dir(trigger), 0755); err != nil {
		t.Fatalf("mkdir trigger dir: %v", err)
	}
	raw, _ := json.Marshal(map[string]string{"channel": "telegram", "chat_id": "5821086579"})
	if err := os.WriteFile(trigger, raw, 0644); err != nil {
		t.Fatalf("write trigger file: %v", err)
	}

	sent := make(chan bus.OutboundMessage, 1)
	g := &Gateway{
		logger: newTestLogger(),
		sendNowFn: func(msg bus.OutboundMessage) error {
			sent <- msg
			return nil
		},
		waitReadyFn: func(ctx context.Context, channel string) bool {
			return false
		},
	}

	g.retryConsumeRestartTriggerForTelegram(context.Background())

	select {
	case out := <-sent:
		t.Fatalf("should not send when telegram is not ready, got %+v", out)
	default:
	}
	if _, err := os.Stat(trigger); err != nil {
		t.Fatalf("trigger should remain when telegram not ready, err=%v", err)
	}
}

func TestGateway_RestartCommand_SendsPreNoticeBeforeRestart(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	cmd := channel.NewCommandHandler(nil, "", 200000)
	scriptPath := cmd.RestartScriptPath()
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0755); err != nil {
		t.Fatalf("mkdir script dir: %v", err)
	}
	if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\n"), 0755); err != nil {
		t.Fatalf("write restart script: %v", err)
	}

	msgBus := bus.NewMessageBus(10)
	restartCalled := make(chan struct{}, 1)
	var sent bus.OutboundMessage
	g := &Gateway{
		bus:        msgBus,
		logger:     newTestLogger(),
		cmdHandler: cmd,
		sendNowFn: func(msg bus.OutboundMessage) error {
			sent = msg
			return nil
		},
		restartFn: func() error {
			restartCalled <- struct{}{}
			return nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go g.processLoop(ctx)

	msgBus.Inbound <- bus.InboundMessage{
		Channel:  "feishu",
		ChatID:   "oc_123",
		SenderID: "u1",
		Content:  "/restart",
	}

	select {
	case <-restartCalled:
	case <-time.After(time.Second):
		t.Fatal("restart should be executed after pre-notice")
	}

	want := "🔄 Restarting Gateway\n\nThe gateway will restart in a few seconds. You'll receive a notification when it's back online."
	if sent.Content != want {
		t.Fatalf("unexpected pre-restart message: %q", sent.Content)
	}
	if sent.Channel != "feishu" || sent.ChatID != "oc_123" {
		t.Fatalf("unexpected pre-restart target: %+v", sent)
	}
	if _, ok := sent.Metadata[protocol.EventTypeKey]; ok {
		t.Fatalf("feishu pre-restart message should use default card flow, got metadata=%v", sent.Metadata)
	}

	trigger := filepath.Join(tmpHome, ".aevitas", "restart_trigger.txt")
	data, err := os.ReadFile(trigger)
	if err != nil {
		t.Fatalf("read restart trigger: %v", err)
	}
	var triggerData map[string]string
	if err := json.Unmarshal(data, &triggerData); err != nil {
		t.Fatalf("parse restart trigger json: %v", err)
	}
	if triggerData["channel"] != "feishu" || triggerData["chat_id"] != "oc_123" {
		t.Fatalf("unexpected restart trigger content: %q", string(data))
	}
}

func TestGateway_RestartCommand_DoesNotRestartWhenPreNoticeFails(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	cmd := channel.NewCommandHandler(nil, "", 200000)
	scriptPath := cmd.RestartScriptPath()
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0755); err != nil {
		t.Fatalf("mkdir script dir: %v", err)
	}
	if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\n"), 0755); err != nil {
		t.Fatalf("write restart script: %v", err)
	}

	msgBus := bus.NewMessageBus(10)
	restartCalled := false
	g := &Gateway{
		bus:        msgBus,
		logger:     newTestLogger(),
		cmdHandler: cmd,
		sendNowFn: func(msg bus.OutboundMessage) error {
			return errors.New("send failed")
		},
		restartFn: func() error {
			restartCalled = true
			return nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go g.processLoop(ctx)

	msgBus.Inbound <- bus.InboundMessage{
		Channel:  "telegram",
		ChatID:   "5821086579",
		SenderID: "u1",
		Content:  "/restart",
	}

	time.Sleep(100 * time.Millisecond)
	if restartCalled {
		t.Fatal("restart should not execute when pre-notice fails")
	}
}

func TestGateway_RestartCommand_InteractionRestartsWhenPreNoticeFails(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	cmd := channel.NewCommandHandler(nil, "", 200000)
	scriptPath := cmd.RestartScriptPath()
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0755); err != nil {
		t.Fatalf("mkdir script dir: %v", err)
	}
	if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\n"), 0755); err != nil {
		t.Fatalf("write restart script: %v", err)
	}

	msgBus := bus.NewMessageBus(10)
	restartCalled := false
	g := &Gateway{
		bus:        msgBus,
		logger:     newTestLogger(),
		cmdHandler: cmd,
		sendNowFn: func(msg bus.OutboundMessage) error {
			return errors.New("send failed")
		},
		restartFn: func() error {
			restartCalled = true
			return nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go g.processLoop(ctx)

	msgBus.Inbound <- bus.InboundMessage{
		Channel:  "interaction",
		ChatID:   "feishuOfficial:feishu:oc_123",
		SenderID: "u1",
		Content:  "/restart",
	}

	time.Sleep(100 * time.Millisecond)
	if !restartCalled {
		t.Fatal("restart should execute for interaction even when pre-notice fails")
	}
}

func TestDefaultRuntimeFactory_NoAPIKey(t *testing.T) {
	cfg := &config.Config{
		Provider: config.ProviderConfig{
			APIKey: "",
		},
	}

	// DefaultRuntimeFactory will try to create real runtime
	// which may fail in different ways depending on SDK behavior
	_, err := DefaultRuntimeFactory(cfg, "test prompt", nil, nil)
	// Just ensure it doesn't panic - error is expected
	_ = err
}

func TestGateway_CronOnJob(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &config.Config{
		Agent: config.AgentConfig{
			Workspace: tmpDir,
		},
		Channels: config.ChannelsConfig{},
	}

	mockRt := &mockRuntime{
		response: &api.Response{
			Result: &api.Result{Output: "cron result"},
		},
	}

	g, err := NewWithOptions(cfg, Options{
		RuntimeFactory: mockRuntimeFactory(mockRt),
	})
	if err != nil {
		t.Fatalf("NewWithOptions error: %v", err)
	}
	defer g.Shutdown()

	// Test cron OnJob callback
	job := cron.CronJob{
		ID: "test-job",
		Payload: cron.Payload{
			Message: "test message",
		},
	}

	result, err := g.cron.OnJob(job)
	if err != nil {
		t.Errorf("OnJob error: %v", err)
	}
	if result != "cron result" {
		t.Errorf("result = %q, want 'cron result'", result)
	}
}

func TestGateway_CronOnJob_WithDelivery(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &config.Config{
		Agent: config.AgentConfig{
			Workspace: tmpDir,
		},
		Channels: config.ChannelsConfig{},
	}

	mockRt := &mockRuntime{
		response: &api.Response{
			Result: &api.Result{Output: "delivered result"},
		},
	}

	g, err := NewWithOptions(cfg, Options{
		RuntimeFactory: mockRuntimeFactory(mockRt),
	})
	if err != nil {
		t.Fatalf("NewWithOptions error: %v", err)
	}
	defer g.Shutdown()

	// Test cron OnJob with delivery
	job := cron.CronJob{
		ID: "test-job",
		Payload: cron.Payload{
			Message: "test message",
		},
		Delivery: &cron.Delivery{
			Mode:    "announce",
			Channel: "telegram",
			To:      "12345",
		},
	}

	// Start a goroutine to consume outbound message
	done := make(chan struct{})
	go func() {
		select {
		case msg := <-g.bus.Outbound:
			if msg.Content != "delivered result" {
				t.Errorf("outbound content = %q, want 'delivered result'", msg.Content)
			}
			if msg.Channel != "telegram" {
				t.Errorf("outbound channel = %q, want 'telegram'", msg.Channel)
			}
			if msg.ChatID != "12345" {
				t.Errorf("outbound chatID = %q, want '12345'", msg.ChatID)
			}
		case <-time.After(time.Second):
			t.Error("timeout waiting for outbound message")
		}
		close(done)
	}()

	result, err := g.cron.OnJob(job)
	if err != nil {
		t.Errorf("OnJob error: %v", err)
	}
	if result != "delivered result" {
		t.Errorf("result = %q, want 'delivered result'", result)
	}

	<-done
}

func TestGateway_CronOnJob_Error(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &config.Config{
		Agent: config.AgentConfig{
			Workspace: tmpDir,
		},
		Channels: config.ChannelsConfig{},
	}

	mockRt := &mockRuntime{
		err: context.DeadlineExceeded,
	}

	g, err := NewWithOptions(cfg, Options{
		RuntimeFactory: mockRuntimeFactory(mockRt),
	})
	if err != nil {
		t.Fatalf("NewWithOptions error: %v", err)
	}
	defer g.Shutdown()

	// Test cron OnJob with error
	job := cron.CronJob{
		ID: "test-job",
		Payload: cron.Payload{
			Message: "test message",
		},
	}

	_, err = g.cron.OnJob(job)
	if err != context.DeadlineExceeded {
		t.Errorf("expected DeadlineExceeded, got %v", err)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestGateway_ResetSession_Success(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &config.Config{
		Agent: config.AgentConfig{
			Workspace: tmpDir,
		},
		Channels: config.ChannelsConfig{},
	}

	mockRt := &mockRuntime{
		response: &api.Response{
			Result: &api.Result{Output: "test"},
		},
	}

	g, err := NewWithOptions(cfg, Options{
		RuntimeFactory: mockRuntimeFactory(mockRt),
	})
	if err != nil {
		t.Fatalf("NewWithOptions error: %v", err)
	}
	defer g.Shutdown()

	sessionKey := "telegram-123"
	g.usageMu.Lock()
	g.usageNotified[sessionKey] = usageMark30 | usageMark50
	g.usageMu.Unlock()

	// Reset session
	if err := g.resetSession(sessionKey); err != nil {
		t.Fatalf("resetSession error: %v", err)
	}

	// Verify SDK ClearSession was called
	if !mockRt.clearSessionCalled {
		t.Error("Expected ClearSession to be called on runtime")
	}
	g.usageMu.Lock()
	_, exists := g.usageNotified[sessionKey]
	g.usageMu.Unlock()
	if exists {
		t.Error("expected usage threshold state to be cleared after reset")
	}
}

func TestGateway_ResetSession_SDKError(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &config.Config{
		Agent: config.AgentConfig{
			Workspace: tmpDir,
		},
		Channels: config.ChannelsConfig{},
	}

	expectedErr := errors.New("SDK error")
	mockRt := &mockRuntime{
		response: &api.Response{
			Result: &api.Result{Output: "test"},
		},
		clearSessionError: expectedErr,
	}

	g, err := NewWithOptions(cfg, Options{
		RuntimeFactory: mockRuntimeFactory(mockRt),
	})
	if err != nil {
		t.Fatalf("NewWithOptions error: %v", err)
	}
	defer g.Shutdown()

	// Reset session should propagate SDK error
	if err := g.resetSession("test-session"); err == nil {
		t.Error("Expected error from resetSession")
	} else if !strings.Contains(err.Error(), "failed to clear session") {
		t.Errorf("Expected 'failed to clear session' error, got: %v", err)
	}
}

func TestGateway_CommandHandler_Integration(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &config.Config{
		Agent: config.AgentConfig{
			Workspace: tmpDir,
		},
		Channels: config.ChannelsConfig{},
	}

	mockRt := &mockRuntime{
		response: &api.Response{
			Result: &api.Result{Output: "agent response"},
		},
	}

	g, err := NewWithOptions(cfg, Options{
		RuntimeFactory: mockRuntimeFactory(mockRt),
	})
	if err != nil {
		t.Fatalf("NewWithOptions error: %v", err)
	}
	defer g.Shutdown()

	// Test that cmdHandler is initialized
	if g.cmdHandler == nil {
		t.Fatal("Expected cmdHandler to be initialized")
	}

	// Test /start command
	startMsg := bus.InboundMessage{
		Channel:  "test",
		ChatID:   "123",
		SenderID: "user1",
		Content:  "/start",
	}

	result := g.cmdHandler.HandleCommand(startMsg)
	if !result.Handled {
		t.Error("Expected /start to be handled")
	}
	if !contains(result.Response, "Aevitas") {
		t.Errorf("Expected Aevitas in response, got: %s", result.Response)
	}
}
