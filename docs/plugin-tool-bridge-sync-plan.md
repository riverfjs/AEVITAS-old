# Plugin Tool Bridge Sync Plan

## Goal

打通同步链路：`gateway -> interaction_channel -> plugin-host -> tool-bridge -> plugin tool`，让插件工具真正执行，而不是占位返回。

## Design (Simple / Sync)

1. `pluginProxyTool.Execute` 直接同步调用 interaction 通道，不引入额外异步状态 map。
2. interaction 侧新增同步发送方法，返回 plugin-host 的 HTTP JSON 响应。
3. plugin-host 处理 `action=invoke_tool`：
   - 调用 `toolBridge.executeTool(name, params)`
   - 直接在 HTTP 响应中返回 `{ success, output|error }`
4. gateway 收到响应后直接返回 `ToolResult` 给 agentsdk。

## Implemented

- 协议动作增加：`message_action=invoke_tool`
- interaction:
  - `HTTPTransport.SendWithAck(...)` 返回响应 JSON
  - `InteractionChannel.InvokeTool(...)` 同步执行工具
  - `ChannelManager.InvokeInteractionTool(...)` 网关调用入口
- plugin-host:
  - `invoke_tool` 走 tool-bridge 执行并同步返回结果
- tool-bridge:
  - 新增 `executeTool(name, params)`，调用插件注册的 `execute`
- gateway:
  - 插件工具执行函数改为同步调用 interaction 通道
  - 去掉 pending map 方案，不走 `tool_result` 异步事件

## Notes

- 当前要求在 interaction 会话上下文中执行（依赖 `session_chat_id`）。
- 并发场景先不扩展，保持同步简单实现。
