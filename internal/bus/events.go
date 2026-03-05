package bus

import (
	"time"

	"github.com/riverfjs/agentsdk-go/pkg/api"
)

type InboundMessage struct {
	Channel     string
	SenderID    string
	ChatID      string
	Content     string
	Timestamp   time.Time
	Media       []string
	Attachments []api.Attachment
	Metadata    map[string]any
}

func (m *InboundMessage) SessionKey() string {
	return m.Channel + ":" + m.ChatID
}

type OutboundMessage struct {
	Channel     string
	ChatID      string
	Content     string
	ReplyTo     string
	Media       []string
	Attachments []api.Attachment
	Metadata    map[string]any
}
