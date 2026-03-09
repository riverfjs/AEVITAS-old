package channel

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/riverfjs/aevitas/internal/bus"
	"github.com/riverfjs/aevitas/internal/config"
	"github.com/riverfjs/aevitas/internal/interaction"
	"github.com/riverfjs/aevitas/internal/protocol"
	sdklogger "github.com/riverfjs/agentsdk-go/pkg/logger"
)

const interactionChannelName = "interaction"

type InteractionChannel struct {
	BaseChannel
	cfg       config.InteractionConfig
	transport *interaction.HTTPTransport
	cancel    context.CancelFunc

	mu     sync.RWMutex
	routes map[string]interaction.OutboundAction
}

func NewInteractionChannel(cfg config.InteractionConfig, b *bus.MessageBus, logger sdklogger.Logger) (*InteractionChannel, error) {
	ch := &InteractionChannel{
		BaseChannel: NewBaseChannel(interactionChannelName, b, cfg.AllowFrom, logger),
		cfg:         cfg,
		routes:      make(map[string]interaction.OutboundAction),
	}
	transport, err := interaction.NewHTTPTransport(cfg, ch.onInboundEvent)
	if err != nil {
		return nil, err
	}
	ch.transport = transport
	return ch, nil
}

func (c *InteractionChannel) Start(ctx context.Context) error {
	ctx, c.cancel = context.WithCancel(ctx)
	return c.transport.Start(ctx)
}

func (c *InteractionChannel) Stop() error {
	if c.cancel != nil {
		c.cancel()
	}
	return c.transport.Stop()
}

func (c *InteractionChannel) Send(msg bus.OutboundMessage) error {
	action := c.buildOutboundAction(msg)
	if strings.TrimSpace(action.PluginID) == "" {
		c.logger.Warnf("[interaction] outbound blocked: missing plugin id session_chat=%s", strings.TrimSpace(msg.ChatID))
		return fmt.Errorf("missing plugin id for outbound chat=%s", strings.TrimSpace(msg.ChatID))
	}
	return c.transport.Send(context.Background(), action)
}

func (c *InteractionChannel) InvokeTool(ctx context.Context, sessionChatID, toolName string, params map[string]any) (map[string]any, error) {
	sessionChatID = strings.TrimSpace(sessionChatID)
	toolName = strings.TrimSpace(toolName)
	if sessionChatID == "" || toolName == "" {
		return nil, fmt.Errorf("session_chat_id and tool_name are required")
	}
	action := c.resolveRoute(sessionChatID)
	if strings.TrimSpace(action.PluginID) == "" {
		return nil, fmt.Errorf("missing plugin id for session_chat_id=%s", sessionChatID)
	}
	action.RequestID = strconv.FormatInt(time.Now().UnixNano(), 10)
	action.Action = protocol.ActionInvokeTool
	meta := mergeInteractionMetadata(action.Metadata, map[string]any{
		protocol.ActionKey:        protocol.ActionInvokeTool,
		protocol.SessionChatIDKey: sessionChatID,
		"tool_name":               toolName,
		"tool_params":             params,
	})
	action.Metadata = meta
	return c.transport.SendWithAck(ctx, action)
}

func (c *InteractionChannel) onInboundEvent(evt interaction.InboundEvent) {
	if strings.TrimSpace(evt.SenderID) != "" && !c.IsAllowed(strings.TrimSpace(evt.SenderID)) {
		return
	}
	if strings.TrimSpace(evt.PluginID) == "" {
		c.logger.Warnf("[interaction] inbound event missing plugin_id")
		return
	}
	eventType := protocol.EventType(evt.Metadata)
	switch eventType {
	case protocol.EventHostReady, protocol.EventOutboundResult, protocol.EventToolCatalog:
		c.handleControlEvent(evt, eventType)
	default:
		c.handleMessageEvent(evt)
	}
}

func (c *InteractionChannel) buildOutboundAction(msg bus.OutboundMessage) interaction.OutboundAction {
	action := c.resolveRoute(msg.ChatID)
	action.RequestID = strconv.FormatInt(time.Now().UnixNano(), 10)
	if len(msg.Metadata) > 0 {
		if s, ok := msg.Metadata[protocol.RequestIDKey].(string); ok && strings.TrimSpace(s) != "" {
			action.RequestID = strings.TrimSpace(s)
		}
	}
	action.Action = resolveInteractionAction(msg)
	action.Content = msg.Content
	action.Media = msg.Media
	action.Attachments = msg.Attachments
	action.Metadata = mergeInteractionMetadata(action.Metadata, msg.Metadata)
	if replyTo := strings.TrimSpace(msg.ReplyTo); replyTo != "" {
		action.ReplyTo = replyTo
	}
	if strings.EqualFold(strings.TrimSpace(msg.Channel), interactionChannelName) && strings.TrimSpace(msg.ChatID) != "" {
		if action.Metadata == nil {
			action.Metadata = map[string]any{}
		}
		if _, ok := action.Metadata[protocol.SessionChatIDKey]; !ok {
			action.Metadata[protocol.SessionChatIDKey] = strings.TrimSpace(msg.ChatID)
		}
	}
	return action
}

func (c *InteractionChannel) handleControlEvent(evt interaction.InboundEvent, eventType string) {
	meta := evt.Metadata
	if meta == nil {
		meta = map[string]any{}
	}
	chatID := ""
	if eventType == protocol.EventOutboundResult {
		chatID = strings.TrimSpace(protocol.MetaString(meta, protocol.SessionChatIDKey))
		if chatID == "" {
			c.logger.Warnf("[interaction] control event missing session_chat_id event=%s plugin=%s platform=%s", eventType, strings.TrimSpace(evt.PluginID), strings.TrimSpace(evt.Platform))
			return
		}
	}
	c.forwardInbound(evt, chatID, "", meta)
}


func (c *InteractionChannel) handleMessageEvent(evt interaction.InboundEvent) {
	chatID := interactionSessionChatID(evt.PluginID, evt.Platform, evt.ChatID)
	if chatID == "" {
		return
	}
	replyMessageID := canonicalInboundReplyMessageID(evt.MessageID)
	c.storeRoute(chatID, interaction.OutboundAction{
		PluginID: evt.PluginID,
		Platform: evt.Platform,
		Channel:  evt.Channel,
		ChatID:   evt.ChatID,
		ThreadID: evt.ThreadID,
		ReplyTo:  replyMessageID,
		Metadata: filterRouteMetadata(evt.Metadata),
	})
	meta := evt.Metadata
	if meta == nil {
		meta = map[string]any{}
	}
	if mid := strings.TrimSpace(replyMessageID); mid != "" {
		meta["message_id"] = mid
	}
	c.forwardInbound(evt, chatID, strings.TrimSpace(evt.Content), meta)
}

func (c *InteractionChannel) forwardInbound(evt interaction.InboundEvent, chatID, content string, meta map[string]any) {
	c.bus.Inbound <- bus.InboundMessage{
		Channel:     interactionChannelName,
		SenderID:    strings.TrimSpace(evt.SenderID),
		ChatID:      strings.TrimSpace(chatID),
		Content:     content,
		Timestamp:   parseInteractionTime(evt.Timestamp),
		Attachments: evt.Attachments,
		Metadata:    meta,
	}
}

func (c *InteractionChannel) storeRoute(sessionChatID string, route interaction.OutboundAction) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.routes[sessionChatID] = route
}

func (c *InteractionChannel) resolveRoute(sessionChatID string) interaction.OutboundAction {
	c.mu.RLock()
	route, ok := c.routes[sessionChatID]
	c.mu.RUnlock()
	if ok {
		return route
	}
	parts := strings.SplitN(strings.TrimSpace(sessionChatID), ":", 3)
	if len(parts) >= 2 {
		return interaction.OutboundAction{
			PluginID: parts[0],
			Platform: parts[1],
			ChatID:   parts[len(parts)-1],
		}
	}
	return interaction.OutboundAction{ChatID: strings.TrimSpace(sessionChatID)}
}

func interactionSessionChatID(pluginID, platform, chatID string) string {
	pluginID = strings.TrimSpace(pluginID)
	platform = strings.TrimSpace(platform)
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return ""
	}
	if pluginID == "" {
		pluginID = "default"
	}
	if platform == "" {
		platform = "generic"
	}
	return fmt.Sprintf("%s:%s:%s", pluginID, platform, chatID)
}

func canonicalInboundReplyMessageID(raw string) string {
	mid := strings.TrimSpace(raw)
	if mid == "" {
		return ""
	}
	// OAuth synthetic follow-up message IDs may carry a suffix like
	// "om_xxx:auth-complete". Feishu open_message_id must use the real om_ ID.
	if strings.HasPrefix(mid, "om_") {
		if i := strings.Index(mid, ":"); i > 0 {
			return strings.TrimSpace(mid[:i])
		}
	}
	return mid
}

func parseInteractionTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Now()
	}
	if ts, err := time.Parse(time.RFC3339, raw); err == nil {
		return ts
	}
	return time.Now()
}

func filterRouteMetadata(meta map[string]any) map[string]any {
	if len(meta) == 0 {
		return nil
	}
	out := map[string]any{}
	for _, key := range []string{"outbound_url", "outboundUrl"} {
		if v, ok := meta[key].(string); ok && strings.TrimSpace(v) != "" {
			out[key] = strings.TrimSpace(v)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func mergeInteractionMetadata(base, override map[string]any) map[string]any {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range override {
		out[k] = v
	}
	return out
}

func resolveInteractionAction(msg bus.OutboundMessage) string {
	if msg.Metadata != nil {
		if v, ok := msg.Metadata[protocol.ActionKey].(string); ok {
			switch strings.ToLower(strings.TrimSpace(v)) {
			case protocol.ActionSendMessage, protocol.ActionEditMessage, protocol.ActionAddReaction, protocol.ActionSendAudio, protocol.ActionSendMedia, protocol.ActionInvokeTool:
				return strings.ToLower(strings.TrimSpace(v))
			}
		}
	}
	eventType := protocol.EventType(msg.Metadata)
	switch eventType {
	case protocol.EventReactionOnIt:
		return protocol.ActionAddReaction
	}
	if msg.Metadata != nil {
		if v, ok := msg.Metadata["media_action"].(string); ok {
			switch strings.ToLower(strings.TrimSpace(v)) {
			case protocol.ActionSendAudio, protocol.ActionSendMedia:
				return strings.ToLower(strings.TrimSpace(v))
			}
		}
	}
	if len(msg.Attachments) > 0 || len(msg.Media) > 0 {
		return protocol.ActionSendMedia
	}
	if v, ok := msg.Metadata["message_id"].(string); ok && strings.TrimSpace(v) != "" {
		return protocol.ActionEditMessage
	}
	return protocol.ActionSendMessage
}
