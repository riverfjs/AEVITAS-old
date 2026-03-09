package interaction

import "github.com/riverfjs/agentsdk-go/pkg/api"

type InboundEvent struct {
	EventID     string           `json:"event_id,omitempty"`
	PluginID    string           `json:"plugin_id,omitempty"`
	Platform    string           `json:"platform,omitempty"`
	Channel     string           `json:"channel,omitempty"`
	SenderID    string           `json:"sender_id,omitempty"`
	ChatID      string           `json:"chat_id,omitempty"`
	ThreadID    string           `json:"thread_id,omitempty"`
	MessageID   string           `json:"message_id,omitempty"`
	Content     string           `json:"content,omitempty"`
	Timestamp   string           `json:"timestamp,omitempty"`
	Attachments []api.Attachment `json:"attachments,omitempty"`
	Metadata    map[string]any   `json:"metadata,omitempty"`
}

type OutboundAction struct {
	RequestID   string           `json:"request_id"`
	PluginID    string           `json:"plugin_id,omitempty"`
	Action      string           `json:"action"`
	Platform    string           `json:"platform,omitempty"`
	Channel     string           `json:"channel,omitempty"`
	ChatID      string           `json:"chat_id,omitempty"`
	ThreadID    string           `json:"thread_id,omitempty"`
	Content     string           `json:"content,omitempty"`
	ReplyTo     string           `json:"reply_to,omitempty"`
	Media       []string         `json:"media,omitempty"`
	Attachments []api.Attachment `json:"attachments,omitempty"`
	Metadata    map[string]any   `json:"metadata,omitempty"`
}

type Ack struct {
	Accepted bool   `json:"accepted"`
	EventID  string `json:"event_id,omitempty"`
	Error    string `json:"error,omitempty"`
}
