package protocol

import (
	"fmt"
	"strings"
)

const (
	EventTypeKey = "event_type"
	ActionKey    = "message_action"
	RequestIDKey = "request_id"
	// SessionChatIDKey is the interaction-only session routing key.
	// It is required for interaction outbound_result events and should not
	// be used by non-interaction channels.
	SessionChatIDKey = "session_chat_id"

	EventPreviewUpdate = "preview_update"
	EventPreviewFinal  = "preview_final"
	EventToolProgress  = "tool_progress"
	EventReactionOnIt  = "reaction_onit"
	EventUsageHUD      = "usage_hud"
	EventOutboundResult = "outbound_result"
	EventHostReady      = "host_ready"
	EventToolCatalog    = "tool_catalog"

	ActionSendMessage = "send_message"
	ActionEditMessage = "edit_message"
	ActionAddReaction = "add_reaction"
	ActionSendAudio   = "send_audio"
	ActionSendMedia   = "send_media"
	ActionInvokeTool  = "invoke_tool"
)

type OutboundMeta struct {
	EventType       string
	ToolName        string
	ToolParams      string
	ToolTime        string
	VoiceDurationMS int
}

func (m OutboundMeta) ToMap() map[string]any {
	out := map[string]any{}
	if v := strings.TrimSpace(m.EventType); v != "" {
		out[EventTypeKey] = v
	}
	if v := strings.TrimSpace(m.ToolName); v != "" {
		out["tool_name"] = v
	}
	if v := strings.TrimSpace(m.ToolParams); v != "" {
		out["tool_params"] = v
	}
	if v := strings.TrimSpace(m.ToolTime); v != "" {
		out["tool_time"] = v
	}
	if m.VoiceDurationMS > 0 {
		out["voice_duration_ms"] = m.VoiceDurationMS
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func EventType(meta map[string]any) string {
	if len(meta) == 0 {
		return ""
	}
	mode, _ := meta[EventTypeKey].(string)
	return strings.ToLower(strings.TrimSpace(mode))
}

func MetaString(meta map[string]any, key string) string {
	if len(meta) == 0 {
		return ""
	}
	if v, ok := meta[key]; ok {
		switch x := v.(type) {
		case string:
			return strings.TrimSpace(x)
		default:
			return strings.TrimSpace(fmt.Sprintf("%v", x))
		}
	}
	return ""
}
