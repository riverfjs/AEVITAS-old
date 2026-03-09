package channel

import (
	"context"
	"strings"
	"time"

	"github.com/riverfjs/aevitas/internal/protocol"
	sdklogger "github.com/riverfjs/agentsdk-go/pkg/logger"
	"github.com/riverfjs/aevitas/internal/bus"
)

type Channel interface {
	Name() string
	Start(ctx context.Context) error
	Stop() error
	Send(msg bus.OutboundMessage) error
}

type BaseChannel struct {
	name      string
	bus       *bus.MessageBus
	allowFrom map[string]bool
	logger    sdklogger.Logger
}

func NewBaseChannel(name string, b *bus.MessageBus, allowFrom []string, logger sdklogger.Logger) BaseChannel {
	af := make(map[string]bool, len(allowFrom))
	for _, id := range allowFrom {
		af[id] = true
	}
	return BaseChannel{name: name, bus: b, allowFrom: af, logger: logger}
}

func (c *BaseChannel) Name() string {
	return c.name
}

func (c *BaseChannel) IsAllowed(senderID string) bool {
	if len(c.allowFrom) == 0 {
		return true
	}
	return c.allowFrom[senderID]
}

func (c *BaseChannel) PublishOutboundResult(chatID, sourceEventType, requestID, messageID string) bool {
	if c.bus == nil {
		return false
	}
	chatID = strings.TrimSpace(chatID)
	requestID = strings.TrimSpace(requestID)
	messageID = strings.TrimSpace(messageID)
	if chatID == "" || requestID == "" || messageID == "" {
		return false
	}
	meta := map[string]any{
		protocol.EventTypeKey: protocol.EventOutboundResult,
		"source_event_type":   strings.TrimSpace(strings.ToLower(sourceEventType)),
		protocol.RequestIDKey: strings.TrimSpace(requestID),
		"message_id":          messageID,
	}
	timer := time.NewTimer(300 * time.Millisecond)
	defer timer.Stop()
	select {
	case c.bus.Inbound <- bus.InboundMessage{
		Channel:  c.name,
		ChatID:   chatID,
		Metadata: meta,
	}:
		return true
	case <-timer.C:
		if c.logger != nil {
			c.logger.Warnf("[%s] outbound_result publish timeout chat=%s source=%s message_id=%s", c.name, chatID, strings.TrimSpace(strings.ToLower(sourceEventType)), messageID)
		}
		return false
	}
}
