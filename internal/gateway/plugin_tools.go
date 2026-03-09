package gateway

import (
	"context"
	"fmt"
	"strings"

	"github.com/riverfjs/agentsdk-go/pkg/tool"
)

// pluginProxyTool implements tool.Tool for plugin-registered tools so they appear in api.Options.CustomTools.
// Execute returns a placeholder; actual execution is intended to be forwarded to the plugin (future).
type pluginProxyTool struct {
	name        string
	description string
	schema      *tool.JSONSchema
	pluginID    string
	platform    string
	executeFn   func(ctx context.Context, pluginID, platform, toolName string, params map[string]interface{}) (*tool.ToolResult, error)
}

func (p *pluginProxyTool) Name() string        { return p.name }
func (p *pluginProxyTool) Description() string { return p.description }
func (p *pluginProxyTool) Schema() *tool.JSONSchema {
	if p.schema == nil {
		return &tool.JSONSchema{Type: "object", Properties: map[string]interface{}{}}
	}
	return p.schema
}
func (p *pluginProxyTool) Execute(ctx context.Context, params map[string]interface{}) (*tool.ToolResult, error) {
	if p.executeFn == nil {
		return &tool.ToolResult{Success: false, Output: "", Error: fmt.Errorf("plugin tool executor not configured")}, nil
	}
	return p.executeFn(ctx, p.pluginID, p.platform, p.name, params)
}

// pluginSchemaToJSONSchema converts plugin input_schema (map from tool_catalog) to agentsdk JSONSchema.
func pluginSchemaToJSONSchema(raw map[string]any) *tool.JSONSchema {
	if raw == nil {
		return &tool.JSONSchema{Type: "object", Properties: map[string]interface{}{}}
	}
	out := &tool.JSONSchema{
		Type:       "object",
		Properties: map[string]interface{}{},
		Required:   []string{},
	}
	if t, ok := raw["type"].(string); ok && strings.TrimSpace(t) != "" {
		out.Type = strings.TrimSpace(t)
	}
	if props, ok := raw["properties"].(map[string]any); ok {
		for k, v := range props {
			out.Properties[k] = v
		}
	}
	if req, ok := raw["required"].([]any); ok {
		for _, r := range req {
			if s, ok := r.(string); ok && strings.TrimSpace(s) != "" {
				out.Required = append(out.Required, strings.TrimSpace(s))
			}
		}
	}
	return out
}

// pluginToolsFromDescriptors converts plugin catalog descriptors to []tool.Tool for api.Options.CustomTools.
func pluginToolsFromDescriptors(descriptors []pluginToolDescriptor, pluginID, platform string, executeFn func(ctx context.Context, pluginID, platform, toolName string, params map[string]interface{}) (*tool.ToolResult, error)) []tool.Tool {
	if len(descriptors) == 0 {
		return nil
	}
	out := make([]tool.Tool, 0, len(descriptors))
	seen := make(map[string]struct{})
	for _, d := range descriptors {
		name := strings.TrimSpace(d.Name)
		if name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, &pluginProxyTool{
			name:        name,
			description: strings.TrimSpace(d.Description),
			schema:      pluginSchemaToJSONSchema(d.InputSchema),
			pluginID:    strings.TrimSpace(pluginID),
			platform:    strings.TrimSpace(platform),
			executeFn:   executeFn,
		})
	}
	return out
}
