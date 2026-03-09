package gateway

import (
	"encoding/json"
	"strings"
)

func detectPluginAuthWait(output string) (map[string]any, bool) {
	output = strings.TrimSpace(output)
	if output == "" {
		return nil, false
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		return nil, false
	}
	if meta, ok := authWaitMetaFromMap(payload); ok {
		return meta, true
	}
	if details, ok := payload["details"].(map[string]any); ok {
		if meta, ok := authWaitMetaFromMap(details); ok {
			return meta, true
		}
	}
	return nil, false
}

func authWaitMetaFromMap(m map[string]any) (map[string]any, bool) {
	if m == nil {
		return nil, false
	}
	waitingUser, _ := m["awaiting_authorization"].(bool)
	waitingApp, _ := m["awaiting_app_authorization"].(bool)
	if !waitingUser && !waitingApp {
		return nil, false
	}
	msg, _ := m["message"].(string)
	return map[string]any{
		"stop_agent":        true,
		"stop_reason":       "approval_required",
		"awaiting_auth":     true,
		"awaiting_auth_msg": strings.TrimSpace(msg),
	}, true
}
