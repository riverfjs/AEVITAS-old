package channel

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/riverfjs/aevitas/internal/bus"
	"github.com/riverfjs/aevitas/internal/config"
	sdklogger "github.com/riverfjs/agentsdk-go/pkg/logger"
)

var (
	channelStartBackoffInitial = 3 * time.Second
	channelStartBackoffMax     = 30 * time.Second
	channelStartMaxRetries     = 5
)

type ChannelManager struct {
	channels map[string]Channel
	logger   sdklogger.Logger

	mu        sync.RWMutex
	states    map[string]ChannelState
	cancelSup context.CancelFunc
	supWG     sync.WaitGroup
	readyCond *sync.Cond
}

type ChannelState struct {
	Running     bool
	LastError   string
	RetryCount  int
	LastAttempt time.Time
	LastSuccess time.Time
}

func NewChannelManager(cfg *config.Config, b *bus.MessageBus, logger sdklogger.Logger) (*ChannelManager, error) {
	if cfg == nil {
		cfg = &config.Config{}
	}
	m := &ChannelManager{
		channels: make(map[string]Channel),
		logger:   logger,
		states:   make(map[string]ChannelState),
	}
	m.readyCond = sync.NewCond(&m.mu)

	if cfg.Channels.Telegram.Enabled {
		ch, err := NewTelegramChannel(cfg.Channels.Telegram, b, logger)
		if err != nil {
			return nil, fmt.Errorf("init telegram channel: %w", err)
		}
		m.channels[ch.Name()] = ch
		m.states[ch.Name()] = ChannelState{}
		b.SubscribeOutbound(ch.Name(), func(msg bus.OutboundMessage) {
			if err := ch.Send(msg); err != nil {
				logger.Errorf("[channel-mgr] send to %s failed: %v", ch.Name(), err)
			}
		})
	}

	if cfg.Channels.WeCom.Enabled {
		ch, err := NewWeComChannel(cfg.Channels.WeCom, b, logger)
		if err != nil {
			return nil, fmt.Errorf("init wecom channel: %w", err)
		}
		m.channels[ch.Name()] = ch
		m.states[ch.Name()] = ChannelState{}
		b.SubscribeOutbound(ch.Name(), func(msg bus.OutboundMessage) {
			if err := ch.Send(msg); err != nil {
				logger.Errorf("[channel-mgr] send to %s failed: %v", ch.Name(), err)
			}
		})
	}

	if cfg.Channels.Interaction.Enabled {
		ch, err := NewInteractionChannel(cfg.Channels.Interaction, b, logger)
		if err != nil {
			return nil, fmt.Errorf("init interaction channel: %w", err)
		}
		m.channels[ch.Name()] = ch
		m.states[ch.Name()] = ChannelState{}
		b.SubscribeOutbound(ch.Name(), func(msg bus.OutboundMessage) {
			if err := ch.Send(msg); err != nil {
				logger.Errorf("[channel-mgr] send to %s failed: %v", ch.Name(), err)
			}
		})
	}

	return m, nil
}

func (m *ChannelManager) StartAll(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancelSup != nil {
		return
	}
	if m.states == nil {
		m.states = make(map[string]ChannelState)
	}
	supervisorCtx, cancel := context.WithCancel(ctx)
	m.cancelSup = cancel
	for name, ch := range m.channels {
		if _, ok := m.states[name]; !ok {
			m.states[name] = ChannelState{}
		}
		m.supWG.Add(1)
		go m.superviseChannel(supervisorCtx, name, ch)
	}
}

func (m *ChannelManager) StopAll() error {
	m.mu.Lock()
	cancel := m.cancelSup
	m.cancelSup = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	for name, ch := range m.channels {
		m.logger.Infof("[channel-mgr] stopping %s", name)
		if err := ch.Stop(); err != nil {
			m.logger.Errorf("[channel-mgr] error stopping %s: %v", name, err)
		}
	}

	waitDone := make(chan struct{})
	go func() {
		m.supWG.Wait()
		close(waitDone)
	}()
	timeoutTicker := time.NewTicker(2 * time.Second)
	defer timeoutTicker.Stop()
	select {
	case <-waitDone:
	case <-timeoutTicker.C:
		m.logger.Warnf("[channel-mgr] supervisor shutdown timeout; forcing gateway shutdown continuation")
	}
	return nil
}

func (m *ChannelManager) EnabledChannels() []string {
	names := make([]string, 0, len(m.channels))
	for name := range m.channels {
		names = append(names, name)
	}
	return names
}

func (m *ChannelManager) SendNow(msg bus.OutboundMessage) error {
	channelName := strings.TrimSpace(msg.Channel)
	if channelName == "" {
		return fmt.Errorf("empty channel")
	}
	ch, ok := m.channels[channelName]
	if !ok || ch == nil {
		return fmt.Errorf("channel not found: %s", channelName)
	}
	return ch.Send(msg)
}

func (m *ChannelManager) InvokeInteractionTool(ctx context.Context, sessionChatID, toolName string, params map[string]any) (map[string]any, error) {
	ch, ok := m.channels[interactionChannelName]
	if !ok || ch == nil {
		return nil, fmt.Errorf("channel not found: %s", interactionChannelName)
	}
	ich, ok := ch.(*InteractionChannel)
	if !ok {
		return nil, fmt.Errorf("channel %s type mismatch", interactionChannelName)
	}
	return ich.InvokeTool(ctx, sessionChatID, toolName, params)
}

func (m *ChannelManager) ChannelStates() map[string]ChannelState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]ChannelState, len(m.states))
	for k, v := range m.states {
		out[k] = v
	}
	return out
}

func (m *ChannelManager) WaitReady(ctx context.Context, name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			m.mu.Lock()
			m.readyCond.Broadcast()
			m.mu.Unlock()
		case <-done:
		}
	}()
	defer close(done)

	m.mu.Lock()
	defer m.mu.Unlock()
	for {
		if st, ok := m.states[name]; ok && st.Running {
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		m.readyCond.Wait()
	}
}

func (m *ChannelManager) superviseChannel(ctx context.Context, name string, ch Channel) {
	defer m.supWG.Done()

	backoff := channelStartBackoffInitial

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		m.logger.Infof("[channel-mgr] starting %s", name)
		m.updateState(name, func(s *ChannelState) {
			s.LastAttempt = time.Now()
		})

		err := ch.Start(ctx)
		if err == nil {
			m.updateState(name, func(s *ChannelState) {
				s.Running = true
				s.LastError = ""
				s.LastSuccess = time.Now()
			})
			m.mu.Lock()
			m.readyCond.Broadcast()
			m.mu.Unlock()
			return
		}

		m.updateState(name, func(s *ChannelState) {
			s.Running = false
			s.RetryCount++
			s.LastError = err.Error()
		})
		m.mu.Lock()
		retryCount := m.states[name].RetryCount
		m.mu.Unlock()
		if retryCount >= channelStartMaxRetries {
			m.logger.Errorf("[channel-mgr] start %s failed after %d retries; giving up: %v", name, retryCount, err)
			return
		}
		m.logger.Warnf("[channel-mgr] start %s failed: %v (retry in %s)", name, err, backoff)

		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		backoff *= 2
		if backoff > channelStartBackoffMax {
			backoff = channelStartBackoffMax
		}
	}
}

func (m *ChannelManager) updateState(name string, mutate func(*ChannelState)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.states == nil {
		m.states = make(map[string]ChannelState)
	}
	s := m.states[name]
	mutate(&s)
	// Normalize state text to keep logs/status clean.
	s.LastError = strings.TrimSpace(s.LastError)
	m.states[name] = s
}
