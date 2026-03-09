# Feishu 授权阻塞与 Tool 日志聚合计划

## 背景与问题

当前链路 `gateway -> interaction -> plugin-host -> tool-bridge -> feishu plugin` 已打通，但出现三个关键流程问题：

1. 工具返回 `awaiting_authorization=true` 后，Agent 仍继续执行后续工具。
2. OAuth 成功后的 synthetic message id（`...:auth-complete`）被误用于 reply/open_message_id，触发飞书 400（`99992354`）并在网关侧表现为 interaction 500。
3. Tool progress/log 在 UI 中被拆成多个 block，未持续在同一锚点上更新。

目标是做“流程级”修复，避免补丁式兜底。

## 目标

- 授权未完成时，当前执行轮必须暂停，不允许继续调用后续业务工具。
- 授权完成后，由 synthetic message 触发新一轮执行恢复流程。
- 修正 reply id 语义，避免 synthetic id 进入 open_message_id。
- 同一轮 tool progress 统一聚合在一个 block 中更新（同 request/锚点）。

## 范围

- `aevitas/internal/gateway`（工具返回解析、执行控制、progress 锚点）
- `aevitas/internal/channel`（仅校验 request/metadata 透传，不承载业务决策）
- `aevitas/shim` 与 `tmp_plugin/official/package/src/tools`（synthetic message/reply id 语义对齐）

## 实施步骤

### Phase 1: 授权等待信号标准化

1. 在网关插件工具执行结果解析中，识别以下信号并归一化：
   - `details.awaiting_authorization == true`
   - `details.awaiting_app_authorization == true`
2. 将其映射为“可控中断”语义（暂停当前轮，而非普通成功）。
3. 当前轮输出明确提示“已发送授权卡片，等待用户授权”。

验收：
- 日志出现单一明确标记（建议：`tool_wait_user_auth`）。
- 授权前不再出现后续业务工具调用（例如建字段、写记录）。

### Phase 2: synthetic message reply id 语义修复

1. 梳理 synthetic message 里：
   - `message_id`（用于去重/会话推进）
   - `replyToMessageId`（用于回复定位）
2. 严格禁止 `:auth-complete` 这种 synthetic id 进入飞书 open_message_id 请求参数。
3. 仅使用真实 `om_` message id 作为 reply/open_message_id。

验收：
- 飞书日志不再出现 `99992354 Invalid open_message_id ... :auth-complete`。
- 网关侧不再出现对应 interaction 500。

### Phase 3: Tool progress 单锚点聚合

1. 复盘当前 progress block 锚点绑定逻辑（request_id/session_chat_id/message_id）。
2. 修复以下分裂触发条件：
   - 等待授权分支触发新的 request 锚点；
   - synthetic message 导致 progress 状态机被提前 reset；
   - outbound_result 未关联到当前请求（request not found）。
3. 确保同一轮工具链的 progress_update 复用同一锚点；新轮次再切新锚点。

验收：
- 一轮内多次工具调用在 UI 中持续编辑同一 block。
- 日志中不再出现同轮大量 `outbound_result dropped: request not found`。

## 日志与验证清单

核心日志关键字：

- 授权等待：`awaiting_authorization`, `awaiting_app_authorization`, `tool_wait_user_auth`
- 执行控制：后续工具是否继续出现（应为否）
- synthetic/reply：`auth-complete`, `99992354`, `open_message_id`
- progress 关联：`progress_update`, `request_id`, `outbound_result dropped`

建议验证流程：

1. 发起“创建多维表格”请求（首次无授权）。
2. 确认发卡成功后，观察是否暂停执行（不再继续字段/记录工具）。
3. 完成授权后，确认 synthetic 触发新轮并继续执行。
4. 检查整个过程中 progress 是否保持单 block 更新。

## 风险与注意

- 若将“等待授权”处理为普通错误，模型可能重复重试，导致卡片风暴。
- 若误把 synthetic id 当真实 reply id，会持续触发 400/500 并污染状态机。
- progress 聚合修复需避免影响 Telegram/WeCom 现有锚点机制。

## 交付定义

- 修复后满足：
  - 授权前不继续业务工具；
  - 授权后自动恢复；
  - 无 synthetic open_message_id 400；
  - 同轮 tool progress 单 block 更新。
