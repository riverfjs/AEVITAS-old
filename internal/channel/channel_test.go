package channel

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/riverfjs/aevitas/internal/bus"
	"github.com/riverfjs/aevitas/internal/config"
	sdklogger "github.com/riverfjs/agentsdk-go/pkg/logger"
)

// ===== BaseChannel 测试 =====

func TestBaseChannel_Name(t *testing.T) {
	b := bus.NewMessageBus(10)
	logger := sdklogger.NewDefault()
	ch := NewBaseChannel("test", b, nil, logger)
	if ch.Name() != "test" {
		t.Errorf("Name = %q, want test", ch.Name())
	}
}

func TestBaseChannel_IsAllowed_NoFilter(t *testing.T) {
	b := bus.NewMessageBus(10)
	logger := sdklogger.NewDefault()
	ch := NewBaseChannel("test", b, nil, logger)
	if !ch.IsAllowed("anyone") {
		t.Error("should allow anyone when allowFrom is empty")
	}
}

func TestBaseChannel_IsAllowed_WithFilter(t *testing.T) {
	b := bus.NewMessageBus(10)
	logger := sdklogger.NewDefault()
	ch := NewBaseChannel("test", b, []string{"user1", "user2"}, logger)

	if !ch.IsAllowed("user1") {
		t.Error("should allow user1")
	}
	if !ch.IsAllowed("user2") {
		t.Error("should allow user2")
	}
	if ch.IsAllowed("user3") {
		t.Error("should reject user3")
	}
}

// ===== ChannelManager 测试 =====

func TestChannelManager_Empty(t *testing.T) {
	b := bus.NewMessageBus(10)
	logger := sdklogger.NewDefault()
	m, err := NewChannelManager(&config.Config{}, b, logger)
	if err != nil {
		t.Fatalf("NewChannelManager error: %v", err)
	}
	if len(m.EnabledChannels()) != 0 {
		t.Errorf("expected 0 enabled channels, got %d", len(m.EnabledChannels()))
	}
}

func TestChannelManager_StartAll_Empty(t *testing.T) {
	b := bus.NewMessageBus(10)
	m, _ := NewChannelManager(&config.Config{}, b, sdklogger.NewDefault())

	ctx := context.Background()
	m.StartAll(ctx)
}

func TestChannelManager_StopAll_Empty(t *testing.T) {
	b := bus.NewMessageBus(10)
	m, _ := NewChannelManager(&config.Config{}, b, sdklogger.NewDefault())

	if err := m.StopAll(); err != nil {
		t.Errorf("StopAll error: %v", err)
	}
}

// ===== Mock Channel for Testing =====

type mockChannel struct {
	name     string
	started  bool
	stopped  bool
	startErr error
	stopErr  error
	sentMsgs []bus.OutboundMessage
}

func (m *mockChannel) Name() string { return m.name }

func (m *mockChannel) Start(ctx context.Context) error {
	m.started = true
	return m.startErr
}

func (m *mockChannel) Stop() error {
	m.stopped = true
	return m.stopErr
}

func (m *mockChannel) Send(msg bus.OutboundMessage) error {
	m.sentMsgs = append(m.sentMsgs, msg)
	return nil
}

func TestChannelManager_WithMockChannel(t *testing.T) {
	mock := &mockChannel{name: "mock"}

	m := &ChannelManager{
		channels: map[string]Channel{"mock": mock},
		logger:   sdklogger.NewDefault(),
	}
	m.readyCond = sync.NewCond(&m.mu)

	// Test StartAll
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.StartAll(ctx)
	deadline := time.Now().Add(500 * time.Millisecond)
	for !mock.started && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !mock.started {
		t.Error("mock channel should be started")
	}

	// Test EnabledChannels
	channels := m.EnabledChannels()
	if len(channels) != 1 || channels[0] != "mock" {
		t.Errorf("EnabledChannels = %v, want [mock]", channels)
	}

	// Test StopAll
	if err := m.StopAll(); err != nil {
		t.Errorf("StopAll error: %v", err)
	}
	if !mock.stopped {
		t.Error("mock channel should be stopped")
	}
}

func TestChannelManager_StartAll_Error(t *testing.T) {
	mock := &mockChannel{name: "mock", startErr: fmt.Errorf("start failed")}

	m := &ChannelManager{
		channels: map[string]Channel{"mock": mock},
		logger:   sdklogger.NewDefault(),
	}
	m.readyCond = sync.NewCond(&m.mu)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.StartAll(ctx)
	time.Sleep(50 * time.Millisecond)
	st := m.ChannelStates()["mock"]
	if st.RetryCount == 0 {
		t.Fatal("expected retry count > 0 when start fails")
	}
	if st.LastError == "" {
		t.Fatal("expected last error to be recorded")
	}
}

func TestChannelManager_StartAll_StopsAfterMaxRetries(t *testing.T) {
	mock := &mockChannel{name: "mock", startErr: fmt.Errorf("always fail")}
	m := &ChannelManager{
		channels: map[string]Channel{"mock": mock},
		logger:   sdklogger.NewDefault(),
	}
	m.readyCond = sync.NewCond(&m.mu)
	oldInitial := channelStartBackoffInitial
	oldMax := channelStartBackoffMax
	oldRetries := channelStartMaxRetries
	channelStartBackoffInitial = 5 * time.Millisecond
	channelStartBackoffMax = 20 * time.Millisecond
	channelStartMaxRetries = 5
	defer func() {
		channelStartBackoffInitial = oldInitial
		channelStartBackoffMax = oldMax
		channelStartMaxRetries = oldRetries
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.StartAll(ctx)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st := m.ChannelStates()["mock"]
		if st.RetryCount >= 5 {
			if st.RetryCount != 5 {
				t.Fatalf("retry count should stop at 5, got %d", st.RetryCount)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("expected retry count to reach 5, got %d", m.ChannelStates()["mock"].RetryCount)
}

func TestChannelManager_StopAll_Error(t *testing.T) {
	mock := &mockChannel{name: "mock", stopErr: fmt.Errorf("stop failed")}

	m := &ChannelManager{
		channels: map[string]Channel{"mock": mock},
		logger:   sdklogger.NewDefault(),
	}
	m.readyCond = sync.NewCond(&m.mu)

	// Should not return error (errors are logged)
	if err := m.StopAll(); err != nil {
		t.Errorf("StopAll should not return error: %v", err)
	}
}
