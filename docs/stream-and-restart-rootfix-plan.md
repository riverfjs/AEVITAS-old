# Stream 与 Restart 流程化重构计划（最终版）

## 1. 问题归类（流程失真）

### Stream
- Feishu：preview 阶段出现多条新消息块，未稳定编辑同一锚点。
- Telegram：preview 编辑正常，但 final 阶段与 TTS 并行后出现文本重复发送。

### Restart
- 同一 trigger 在启动路径与 host_ready 路径发生并发消费，导致重启成功通知双发。

## 2. 统一流程模型

### 2.1 Stream 主流程（gateway 编排）
1. `StartTurn`：初始化 turn 状态（无锚点）。
2. `OnDelta`：接收模型增量文本。
3. `EmitPreview`：按状态决定 send/edit。
4. `OnOutboundAck`：处理 outbound_result 回执，推进状态。
5. `OnFinal`：收敛 final 到锚点。
6. `OnPostMedia`：发送媒体（如 TTS），不改变文本状态。
7. `CloseTurn`：清理状态。

### 2.2 Restart 主流程（trigger 消费）
1. `/restart` 写 `restart_trigger.txt`（channel/chat_id）。
2. 启动后读取 trigger。
3. 进入原子消费（claim）。
4. 尝试发送重启成功通知。
5. 发送成功后删除 trigger；失败保留 trigger。
6. host_ready 仅触发同一消费流程，不允许第二次成功消费。

## 3. 流程状态机

### 3.1 Stream State（每个 channel+chat 一份）
- `Idle`：未发送 preview。
- `AnchorPendingAck`：已发首条 preview，等待 message_id 回执。
- `Anchored`：已有锚点，后续仅 edit。
- `Finalizing`：收敛 final 中。
- `Finalized`：文本闭环完成。

### 3.2 转移规则
- `Idle -> AnchorPendingAck`：首次 preview `send_message`。
- `AnchorPendingAck -> Anchored`：收到首条 preview outbound_result(message_id)。
- `Anchored -> Anchored`：preview update 走 `edit_message`。
- `Anchored -> Finalizing -> Finalized`：preview_final 成功编辑。
- 任意状态 `+ PostMedia`：只发媒体，不触发文本 send/edit。

### 3.3 Restart Claim State
- `Unclaimed`：可消费。
- `Claimed`：消费中。
- `Done`：已成功通知并完成删除。

## 4. 协议契约（流程前置条件）

1. 所有 outbound 动作必须携带 `request_id`。
2. interaction outbound_result 必须携带 `session_chat_id`。
3. outbound_result 必须携带 `request_id + message_id`。
4. 缺字段直接拒绝状态推进，仅记录错误日志，不做回退推断。

## 5. 模块职责重划

### gateway（唯一编排者）
- 维护 stream/restart 状态机。
- 决定何时 send/edit/final/media。
- 决定失败策略与重试策略。

### channel（执行层）
- 严格执行 `send_message` / `edit_message` / `send_audio`。
- 回传执行结果，不参与时序策略。

### shim/adapter（平台层）
- 做 API 调用与字段透传。
- 不保留业务状态，不做策略 fallback。

## 6. 实施步骤

### Phase A：文档与契约落地
1. 固化状态机转移表与失败策略。
2. 补齐结构化日志字段：`state/from/to/request_id/message_id/action`。

### Phase B：Stream 重构
1. gateway 中抽取流程函数（必须落到独立函数，不保留内联大分支）：
   - `advancePreviewState`
   - `applyOutboundAck`
   - `finalizePreviewState`
2. `processAgentStream` 仅负责：
   - 采集 delta
   - 调用流程函数推进状态
   - 接收 final response
3. 删除旧的“长度差分驱动 + patch 条件分叉 + 临时 pending 变量”。
4. channel 侧移除 final 特判 fallback，final 策略由 gateway 决策。

### Phase C：Restart 重构
1. 统一 trigger consume 入口 `consumeRestartTriggerOnce`（单入口、幂等）。
2. 将当前 restart 相关双入口函数路径收敛为：
   - 启动路径调用 `consumeRestartTriggerOnce`
   - host_ready 路径调用 `consumeRestartTriggerOnce`
3. 入口内部流程固定：
   - 读取 trigger
   - claim（防并发重复发送）
   - 发送通知
   - 成功后删除 trigger
4. 删除重复逻辑：
   - 多处 readiness 判定副本
   - 多处发送实现副本
   - 多处删除 trigger 副本

### Phase D：测试与验收
1. 流程测试（行为级）：
   - Feishu 全程单锚点编辑；
   - Telegram final+tts 不重复文本；
   - restart 并发触发只发一次通知。
2. 单元测试新增覆盖：
   - `advancePreviewState` 状态转移矩阵
   - `finalizePreviewState` 成功/失败分支
   - `consumeRestartTriggerOnce` 幂等与并发
2. 回归：
   - `go test ./internal/gateway ./internal/channel`
   - `node --check shim/plugin-host.mjs`
   - `node --check shim/adapters/feishuOfficial.mjs`

### Phase E：重复函数治理（结构收敛）
1. 对“同构但语义等价”的 key/helper 函数做统一收敛（例如 plugin+platform 组合 key）。
2. 无复用优势的新增重复函数必须移除，避免并行演化。
3. 新增代码若与现有函数仅参数名不同，优先复用或合并，不接受复制实现。

### Phase F：Telegram TTS 后重复文本根修（简化流程版）
1. **仅新增一个状态字段**：在每个 `channel+chat` 的出站状态里增加 `PendingFinalText`（不新增调度层，不引入时间窗口）。
2. **final 改为状态事件，不直接兜底发送**：
   - 当 `previewSent=true` 且锚点未确认（无 `PreviewMessageID`）时，final 文本仅写入 `PendingFinalText`；
   - 不允许在该分支直接 `send_message`。
3. **ACK 驱动 final 冲刷**：
   - `outbound_result` 首次建立 preview 锚点后，若存在 `PendingFinalText`，立即发送一次 `preview_final`（必须带 `message_id`，走 edit）。
4. **媒体支线隔离**：
   - `send_audio`/TTS 不参与文本 finalize 判定，不触发文本新发。
5. **约束**：
   - 一轮 turn 已进入 preview 流后，文本 final 只能 edit 同锚点；
   - 禁止 `preview_final` 在无锚点时降级为新消息发送。
6. **测试**：
   - 覆盖 `final 先到、preview ack 后到`，断言最终仅一条文本锚点；
   - 覆盖 `final + tts` 并发，断言不会新增第二条文本。

## 7. 验收标准

1. stream 文本只保留一个锚点消息，update/final 都编辑该锚点。
2. TTS 发送后不新增文本消息。
3. 同一 restart trigger 只发送一次成功通知。
4. 日志可完整追踪一轮流程：`request_id -> action -> outbound_result -> state transition`。

## 8. 当前进度（2026-03-09）

- **Phase A**：已完成（契约与日志字段已落地）。
- **Phase C**：可验收（`consumeRestartTriggerOnce` 已作为唯一消费入口；startup/host_ready/telegram-ready 补触发均调用同一入口；补齐 telegram ready retry 分支测试并收敛 `waitReadyFn` 优先路径）。
- **Phase C 待清理**：已完成，`sendStartupNotification` 兼容包装已移除，调用方统一为 `consumeRestartTriggerOnce`。
- **Phase B**：可验收（`previewFlowState/advancePreviewState/applyOutboundAck/finalizePreviewState` 已函数化落地；`processAgentStream` 关闭流时的 accumulated fallback 已收敛到统一响应出口；已补充 `advancePreviewState` 状态转移矩阵、`finalizePreviewState` 通道/session 分支、`applyOutboundAck` 缺字段/未命中分支测试；新增 `processAgentStream` 单锚点行为测试覆盖 pending->ack->anchored->edit）。
- **Phase E**：执行中（已完成 `hostReadyKey/pluginToolKey` 收敛、restart 入口包装函数去重；本轮完成 metadata 读取 helper 收敛：统一 `protocol.MetaString`，移除 `gateway/interaction/telegram` 内同构 wrapper）。
- **Phase F**：已落地（简化流程版：PendingFinalContent + ACK 驱动冲刷；preview 发送改为**事件驱动**：移除 700ms ticker，按 `text_delta` 触发，用 `minPreviewChunk`（64 字符）节流，流结束时 `flushRemainingPreview` 兜底；Telegram `preview_final` 若 API 返回 "message is not modified" 视为内容已一致，打 INFO 不 fallback）。

## 9. 固定剩余清单（本轮起不再增项）

1. **Phase B 行为级验收**：已完成 `processAgentStream` 单锚点链路测试（pending -> ack -> anchored -> edit）。
2. **Phase B 完成判定**：已执行，Phase B 状态已更新为“可验收”。
3. **Phase C**：已可验收（本轮完成 retry 分支测试与入口收敛）。
4. **Phase E**：当前暂停（不新增改动）。

## 10. 插件 Tool 与 API 注入（已实现）

- **实现**：收到 `EventToolCatalog` 后缓存在 `pluginToolsByKey`，并调用 `recreateRuntimeWithPluginTools()`：合并所有已缓存插件的 tool 描述，通过 `pluginToolsFromDescriptors` 转为 `[]tool.Tool`（proxy 实现），用 `defaultRuntimeFactoryWithPermissions(..., customTools)` 重建 runtime 并替换 `g.runtime`，旧 runtime 关闭。`BuildAPIOptions` 增加 `customTools` 参数并写入 `api.Options.CustomTools`。
- **结果**：插件上报 tool_catalog 后，agent 可见并可使用这些工具（名称/描述/schema 暴露给模型；Execute 当前为占位，实际执行待后续转发到插件）。
