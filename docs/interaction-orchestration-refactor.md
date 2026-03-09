# Interaction 最终迁移文档（破坏式，一次性，不兼容）

## 最终目标

- `gateway` 统一处理消息编排：preview/tool/reaction/message_id 生命周期、request_id 闭环。
- `channel`/`adapter` 只做执行层：send/edit/react/upload 与协议编解码。
- 不新增 bus 语义，不保留旧兼容分支。

## 职责边界（最终版）

### gateway
- 流式状态机（首发、增量、最终）
- tool log 聚合与单消息编辑策略
- reaction 触发时机（会话级）
- request_id -> message_id 闭环匹配（source_event_type 仅回退）
- 统一事件与动作生成（event_type + metadata）

### channel
- 执行 gateway 下发动作
- 平台入站/出站传输
- 统一控制回传（`BaseChannel.PublishOutboundResult`）

### host / adapter
- 动态加载 + 通讯桥接
- 配置透传
- 平台原语执行（send/edit/react/upload）
- 不维护业务状态机，不做业务时机策略

## Phase 状态

### Phase 1（完成）
- host/adapter 去业务化
- interaction 动作映射基础打通
- outbound_result 回传链路建立

### Phase 2（完成）
- telegram 旧 preview/tool 状态机路径物理删除
- gateway 会话状态统一（preview/tool/reaction）
- 公共 push 机制统一到 `BaseChannel`

### Phase 3（进行中）
- 已完成：request_id -> message_id 优先匹配
- 已完成：source_event_type 回退匹配
- 已完成：媒体动作由 gateway 明确下发（`message_action`），adapter 只执行动作
- 已完成：执行层 action 字段全部常量化（Go 协议常量 + shim 常量），去除魔法字符串

### Phase 4（待启动）
- 全量回归与端到端验收

## 本轮“最后迁移”范围

1. 将 `feishuOfficial` adapter 的媒体策略分支上收到 gateway/channel 决策层
2. adapter 仅执行显式动作，不再自行按附件类型决定策略
3. 维持现有 request_id/outbound_result 闭环
4. 完成后进入 Phase 4 测试

## 禁止项（硬规则）

1. 禁止增加兼容分支（旧 key / 旧行为）
2. 禁止在单一 channel 私有拼装控制回传
3. 禁止把编排策略放回 adapter
4. 同类改造必须一次性切换并同步删旧路径
5. action 字段必须使用协议常量（禁止硬编码魔法字符串）

## Gateway 并发状态规则（新增）

1. `gateway` 并发状态采用折中双锁：`toolLogMu`（仅 tool log）+ `stateMu`（outbound state / host ready）。
2. `host_ready` 采用一次判定，不做条件等待：仅 `markHostReady/isHostReady` 布尔状态。
3. 禁止在锁内执行 IO / 网络 / bus send；锁仅用于 map/struct 内存读写。
4. 禁止继续扩散锁种类；新增并发状态必须归入上述两类锁。

## 验收清单（Phase 4）

- preview：首发 -> 增量编辑 -> 最终收敛
- tool log：单消息持续编辑
- reaction：OnIt 时机正确且不重复
- voice：时长与发送类型正确
- 回归：
  - `go test ./internal/gateway ./internal/channel`
  - `node --check shim/plugin-host.mjs`
  - `node --check shim/adapters/feishuOfficial.mjs`

## 本轮变更记录（锁折中与启动通知简化）

1. `gateway` 并发模型从“单锁全状态”调整为“折中双锁”：
   - `toolLogMu`：仅管理 `toolLogByChatKey`
   - `stateMu`：仅管理 `outboundState` 与 `hostReadyByKey`
2. `host_ready` 机制从“条件等待”改为“一次判定”：
   - 删除 `hostReadyCond`、`ensureHostReadyState`、`waitHostReady`
   - 保留 `markHostReady/isHostReady`
3. 启动通知流程简化为无等待、无重试：
   - 仅在 `channel_running && runtime_running && host_ready` 时发送
   - 否则直接跳过并清理 trigger，不阻塞主流程
4. `sendStartupNotification` 签名改为无参：
   - `sendStartupNotification(ctx)` -> `sendStartupNotification()`
   - 删除匿名占位参数写法（如 `_ = ctx`）
5. 本轮回归已通过：
   - `go test ./internal/gateway`
   - `go test ./internal/channel`

## 本轮任务计划（重启通知语义修正）

1. 保留 `restart_trigger` 作为跨进程最小载体（保障 Telegram/Interaction 重启通知）。
2. 修正删除时机：只有“重启成功通知实际发送后”才删除 trigger。
3. `interaction` 未 ready（`host_ready=false`）时不删除 trigger、不阻塞、不重试。
4. 收到 `host_ready` 控制事件后，gateway 触发一次非阻塞 `sendStartupNotification()` 再尝试发送。
5. Telegram 语义保持不变：满足 running 条件时可直接发送。
6. 回归标准：
   - interaction：初次未 ready 不丢通知，ready 后发送成功并删除 trigger。
   - telegram：重启成功通知行为保持。
   - `go test ./internal/gateway ./internal/channel` 通过。

## 重启通知最终语义（本轮）

1. 发送尝试由事件驱动（启动时一次 + `host_ready` 到达时一次），不使用时间重试。
2. 主线程不阻塞；发送条件不满足时立即返回。
3. trigger 删除是成功提交通知后的“提交点”，不是尝试点。

## Tool Bridge（Phase 1）

1. `plugin-host` 不再吞掉 `registerTool`，改为收集插件工具目录（catalog）。
2. 新增独立模块 `shim/tool-bridge.mjs`，避免把工具桥接复杂化写入 `plugin-host` 主文件。
3. `plugin-host` 在 `host_ready` 后上报 `tool_catalog` 控制事件（含 tool name/description/input_schema）。
4. gateway 接收并缓存 `tool_catalog`（按 `plugin_id + platform`），用于后续“启动时注入 runtime tools”阶段。
5. 当前阶段不实现热插拔与运行中注入，仅完成目录链路打通（discover-only）。

## 会话主键统一（破坏式）

1. 新增控制事件会话主键：`session_chat_id`（协议字段，唯一会话路由键）。
2. gateway 下发 interaction outbound action 时必须携带 `metadata.session_chat_id`。
3. host/adapter 回传 `outbound_result` 时必须透传同一个 `session_chat_id`。
4. interaction channel/gateway 处理控制事件时仅按 `session_chat_id` 路由会话状态，不再依赖 `plugin+platform+chat` 派生拼接。
5. 禁止回退到 `generic/feishu` 平台推断路由；缺失 `session_chat_id` 的控制事件只记日志并丢弃。

## 出站回执字段约束（严格，无兜底）

1. `request_id` 为出站动作主键：
   - gateway 生成并写入 outbound metadata；
   - interaction channel 负责映射到 `OutboundAction.request_id`（传输层字段）；
   - host/adapter 回传 `outbound_result` 必须携带同一个 `request_id`。
2. `session_chat_id` 仅用于 interaction 会话路由：
   - 仅 interaction `outbound_result` 必填；
   - telegram/feishu 直连通道禁止依赖该字段。
3. gateway 处理 `outbound_result` 时仅按 `request_id` 关联 pending request；
   - 缺 `request_id` / `message_id` 直接丢弃；
   - interaction 缺 `session_chat_id` 直接丢弃；
   - 不再使用 `source_event_type` 回退匹配 message state。

## Interaction Channel 重构计划（事件分发）

1. `onInboundEvent` 改为事件分发入口：按 `event_type` 使用 `switch/case` 路由，不再层层 `if/else`。
2. 控制事件处理统一收敛到 `handleControlEvent`：
   - `host_ready`
   - `outbound_result`
   - `tool_catalog`
3. 普通消息处理收敛到 `handleMessageEvent`，仅负责 route 存储与 inbound 投递。
4. `Send` 路径收敛到 `buildOutboundAction`，统一注入 `session_chat_id`，去除重复 map/string 处理。
5. 验收：
   - 行为不变（协议不变）
   - `go test ./internal/channel ./internal/gateway` 通过
