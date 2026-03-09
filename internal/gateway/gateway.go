package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/riverfjs/aevitas/internal/bus"
	"github.com/riverfjs/aevitas/internal/channel"
	"github.com/riverfjs/aevitas/internal/config"
	"github.com/riverfjs/aevitas/internal/cron"
	"github.com/riverfjs/aevitas/internal/heartbeat"
	"github.com/riverfjs/aevitas/internal/logger"
	"github.com/riverfjs/aevitas/internal/pluginmgr"
	"github.com/riverfjs/aevitas/internal/protocol"
	"github.com/riverfjs/aevitas/internal/rpc"
	"github.com/riverfjs/aevitas/internal/runtimeopts"
	"github.com/riverfjs/aevitas/internal/usagehud"
	"github.com/riverfjs/agentsdk-go/pkg/api"
	"github.com/riverfjs/agentsdk-go/pkg/core/events"
	sdklogger "github.com/riverfjs/agentsdk-go/pkg/logger"
	"github.com/riverfjs/agentsdk-go/pkg/tool"
)

// Runtime interface for agent runtime (allows mocking in tests)
type Runtime interface {
	Run(ctx context.Context, req api.Request) (*api.Response, error)
	RunStream(ctx context.Context, req api.Request) (<-chan api.StreamEvent, error)
	ClearSession(sessionID string) error
	GetSessionStats(sessionID string) *api.SessionTokenStats
	GetTotalStats() *api.SessionTokenStats
	Close()
}

// runtimeAdapter wraps api.Runtime to implement Runtime interface
type runtimeAdapter struct {
	rt *api.Runtime
}

func (r *runtimeAdapter) Run(ctx context.Context, req api.Request) (*api.Response, error) {
	return r.rt.Run(ctx, req)
}

func (r *runtimeAdapter) RunStream(ctx context.Context, req api.Request) (<-chan api.StreamEvent, error) {
	return r.rt.RunStream(ctx, req)
}

func (r *runtimeAdapter) ClearSession(sessionID string) error {
	return r.rt.ClearSession(sessionID)
}

func (r *runtimeAdapter) GetSessionStats(sessionID string) *api.SessionTokenStats {
	return r.rt.GetSessionStats(sessionID)
}

func (r *runtimeAdapter) GetTotalStats() *api.SessionTokenStats {
	return r.rt.GetTotalStats()
}

func (r *runtimeAdapter) Close() {
	r.rt.Close()
}

// RuntimeFactory creates a Runtime instance. customTools are appended to built-in tools (e.g. from plugin tool_catalog).
type RuntimeFactory func(cfg *config.Config, sysPrompt string, realtimeCallback func(api.RealtimeEvent), customTools []tool.Tool) (Runtime, error)

type PluginRuntimeManager interface {
	StartEnabled() error
	StopAll() error
	IsRunning(pluginID string) (bool, error)
}

// Options for creating a Gateway
type Options struct {
	RuntimeFactory RuntimeFactory
	SignalChan     chan os.Signal // for testing signal handling
	RuntimeManager PluginRuntimeManager
}

// DefaultRuntimeFactory creates the default agentsdk-go runtime
func DefaultRuntimeFactory(cfg *config.Config, sysPrompt string, realtimeCallback func(api.RealtimeEvent), customTools []tool.Tool) (Runtime, error) {
	return defaultRuntimeFactoryWithPermissions(cfg, sysPrompt, realtimeCallback, nil, customTools)
}

func defaultRuntimeFactoryWithPermissions(cfg *config.Config, sysPrompt string, realtimeCallback func(api.RealtimeEvent), permissionHandler api.PermissionRequestHandler, customTools []tool.Tool) (Runtime, error) {
	// 初始化 logger - 默认启用 debug 日志
	debug := true // 始终启用详细日志
	zapLogger, err := logger.InitLogger(cfg.Agent.Workspace, debug)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize logger: %w", err)
	}
	sdkLog := sdklogger.NewZapLogger(zapLogger)

	provider := runtimeopts.NewProvider(cfg)
	rt, err := api.New(context.Background(), runtimeopts.BuildAPIOptions(cfg, provider, sysPrompt, sdkLog, realtimeCallback, permissionHandler, customTools))
	if err != nil {
		return nil, fmt.Errorf("create runtime: %w", err)
	}
	return &runtimeAdapter{rt: rt}, nil
}

type Gateway struct {
	cfg               *config.Config
	bus               *bus.MessageBus
	runtime           Runtime
	runtimeFactory    RuntimeFactory // Factory to recreate runtime on restart
	sysPrompt         string
	realtimeCallback  func(api.RealtimeEvent)
	channels          *channel.ChannelManager
	cron           *cron.Service
	hb             *heartbeat.Service
	cmdHandler     *channel.CommandHandler
	signalChan     chan os.Signal // for testing
	logger         sdklogger.Logger
	runtimeMgr     PluginRuntimeManager

	// Current execution context (for realtime callbacks)
	currentChannelID string
	currentChatID    string
	currentReplyTo   string
	usageMu          sync.Mutex
	usageNotified    map[string]uint8
	toolLogMu        sync.Mutex
	stateMu          sync.Mutex
	toolLogByChatKey map[string][]toolLogEntry
	outboundState    map[string]outboundMessageState
	hostReadyByKey   map[string]bool
	restartNotifyByKey map[string]restartNotifyState
	pluginToolsByKey map[string][]pluginToolDescriptor
	requestSeq       uint64

	// Optional test hooks for channel state/event observation.
	waitReadyFn     func(context.Context, string) bool
	channelStatesFn func() map[string]channel.ChannelState
	sendNowFn       func(bus.OutboundMessage) error
	restartFn       func() error

	approvalMu      sync.Mutex
	pendingApproval map[string]pendingApproval
}

type pendingApproval struct {
	requestID string
	channel   string
	chatID    string
	toolName  string
	display   string
	target    string
	decisionC chan events.PermissionDecisionType
}

type toolLogEntry struct {
	Name      string
	ParamsRaw string
	Raw       string
}

type pluginToolDescriptor struct {
	Name        string
	Description string
	InputSchema map[string]any
}

type outboundMessageState struct {
	PreviewMessageID string
	ToolMessageID    string
	ReactionSent     bool
	PendingRequest   map[string]outboundMessageKind
	PendingFinalContent string
}

type outboundMessageKind string

const (
	outboundMessagePreview outboundMessageKind = "preview"
	outboundMessageTool    outboundMessageKind = "tool"
)

type restartNotifyState struct {
	inFlight bool
	done     bool
}

type restartTrigger struct {
	Channel string `json:"channel"`
	ChatID  string `json:"chat_id"`
}

// New creates a Gateway with default options
func New(cfg *config.Config) (*Gateway, error) {
	return NewWithOptions(cfg, Options{})
}

// NewWithOptions creates a Gateway with custom options for testing
func NewWithOptions(cfg *config.Config, opts Options) (*Gateway, error) {
	// 初始化 logger
	debug := true // 始终启用详细日志
	zapLogger, err := logger.InitLogger(cfg.Agent.Workspace, debug)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize logger: %w", err)
	}

	g := &Gateway{
		cfg:             cfg,
		logger:          sdklogger.NewZapLogger(zapLogger),
		usageNotified:   make(map[string]uint8),
		toolLogByChatKey: make(map[string][]toolLogEntry),
		outboundState:    make(map[string]outboundMessageState),
		hostReadyByKey:   make(map[string]bool),
		restartNotifyByKey: make(map[string]restartNotifyState),
		pluginToolsByKey: make(map[string][]pluginToolDescriptor),
		pendingApproval: make(map[string]pendingApproval),
	}
	if opts.RuntimeManager != nil {
		g.runtimeMgr = opts.RuntimeManager
	} else if hasEnabledPlugins(cfg) {
		g.runtimeMgr = pluginmgr.NewRuntimeManager(cfg)
	}

	// Message bus
	g.bus = bus.NewMessageBus(config.DefaultBufSize)

	// Build system prompt
	sysPrompt := g.buildSystemPrompt()

	// Build real-time event callback.
	// Progress updates require toolLog.enabled; context window warnings always fire.
	realtimeCallback := func(event api.RealtimeEvent) {
		switch event.Type {
		case api.RealtimeEventModelSwitch:
			fromModel, _ := event.Metadata["from_model"].(string)
			toModel, _ := event.Metadata["to_model"].(string)
			lastError, _ := event.Metadata["last_error"].(string)
			if strings.TrimSpace(lastError) != "" {
				g.logger.Infof("[gateway] Realtime event: type=%s, from_model=%s, to_model=%s, error=%s",
					event.Type, fromModel, toModel, lastError)
			} else {
				g.logger.Infof("[gateway] Realtime event: type=%s, from_model=%s, to_model=%s",
					event.Type, fromModel, toModel)
			}
		default:
			g.logger.Infof("[gateway] Realtime event: type=%s, count=%d, tool=%s", event.Type, event.Count, event.LastTool)
		}
		if g.currentChannelID == "" || g.currentChatID == "" {
			return
		}

		var msg string
		switch event.Type {
		case api.RealtimeEventContextWindowWarn:
			// Always forward context window warnings — the user must know.
			msg = event.Message

		case api.RealtimeEventProgressUpdate:
			// Only forward progress updates when toolLog is enabled.
			if !cfg.Agent.ToolLog.Enabled {
				return
			}
			params := ""
			if len(event.RecentCalls) > 0 {
				params = event.RecentCalls[0].Params
			}
			msg = g.appendToolProgressBlock(g.currentChannelID, g.currentChatID, event.LastTool, params)

		case api.RealtimeEventModelSwitch:
			msg = strings.TrimSpace(event.Message)
			if msg == "" {
				msg = "Model switched to fallback."
			}
			msg = "⚠️ " + msg

		default:
			return
		}

		if msg == "" {
			return
		}
		var meta protocol.OutboundMeta
		replyTo := g.currentReplyTo
		if event.Type == api.RealtimeEventProgressUpdate {
			meta.EventType = protocol.EventToolProgress
			meta.ToolName = event.LastTool
			if len(event.RecentCalls) > 0 {
				meta.ToolParams = event.RecentCalls[0].Params
			}
			meta.ToolTime = time.Now().Format(time.RFC3339)
			// Tool progress should stay as independent tool blocks for most channels.
			// Keep reply threading for interaction so plugin side can react to user message.
			if !strings.EqualFold(strings.TrimSpace(g.currentChannelID), "interaction") {
				replyTo = ""
			}
			m := meta.ToMap()
			if mid := g.outboundMessageID(g.currentChannelID, g.currentChatID, outboundMessageTool); mid != "" {
				if m == nil {
					m = map[string]any{}
				}
				m["message_id"] = mid
			}
			m = g.attachOutboundRequest(g.currentChannelID, g.currentChatID, outboundMessageTool, m)
			g.bus.Outbound <- bus.OutboundMessage{
				Channel:  g.currentChannelID,
				ChatID:   g.currentChatID,
				ReplyTo:  replyTo,
				Content:  msg,
				Metadata: m,
			}
			g.logger.Debugf("[gateway] Sent %s event to %s/%s", event.Type, g.currentChannelID, g.currentChatID)
			return
		}
		if event.Type == api.RealtimeEventModelSwitch {
			// Model switch notice should be a standalone alert.
			replyTo = ""
		}
		g.bus.Outbound <- bus.OutboundMessage{
			Channel:  g.currentChannelID,
			ChatID:   g.currentChatID,
			ReplyTo:  replyTo,
			Content:  msg,
			Metadata: meta.ToMap(),
		}
		g.logger.Debugf("[gateway] Sent %s event to %s/%s", event.Type, g.currentChannelID, g.currentChatID)
	}

	g.sysPrompt = sysPrompt
	g.realtimeCallback = realtimeCallback

	// Create runtime using factory (allows injection for testing). Plugin CustomTools injected when tool_catalog is received.
	factory := opts.RuntimeFactory
	if factory == nil {
		rt, rtErr := defaultRuntimeFactoryWithPermissions(cfg, sysPrompt, realtimeCallback, g.handlePermissionRequest, nil)
		if rtErr != nil {
			return nil, rtErr
		}
		g.runtimeFactory = DefaultRuntimeFactory
		g.runtime = rt
	} else {
		g.runtimeFactory = factory // Save factory for restart
		rt, err := factory(cfg, sysPrompt, realtimeCallback, nil)
		if err != nil {
			return nil, err
		}
		g.runtime = rt
	}

	// Signal channel for testing
	g.signalChan = opts.SignalChan

	// Cron
	cronStorePath := filepath.Join(config.ConfigDir(), "data", "cron", "jobs.json")
	g.cron = cron.NewService(cronStorePath, g.logger)
	g.cron.OnJob = func(job cron.CronJob) (string, error) {
		var result string
		var err error

		sessionID := "system"
		if job.SessionTarget == cron.SessionIsolated {
			sessionID = fmt.Sprintf("cron-isolated-%s", job.ID)
		}

		switch job.Payload.Kind {
		case "command":
			// Direct exec: bypass agent entirely, stdout is the result
			out, execErr := exec.Command("bash", "-c", job.Payload.Command).Output()
			if execErr != nil {
				result = fmt.Sprintf("command error: %v\n%s", execErr, string(out))
			} else {
				result = string(out)
			}

		case "systemEvent":
			// Inject text as system event — no agent turn, result is the text itself
			result = job.Payload.Text

		default:
			// "agentTurn" or legacy (empty kind with message field)
			msg := job.Payload.Message
			if msg == "" {
				msg = job.Payload.Text
			}
			result, err = g.runAgent(context.Background(), msg, sessionID)
			if err != nil {
				return "", err
			}
		}

		// Deliver result via Delivery config (new style)
		if d := job.Delivery; d != nil && d.Mode == "announce" && d.Channel != "" {
			g.bus.Outbound <- bus.OutboundMessage{
				Channel: d.Channel,
				ChatID:  d.To,
				Content: result,
			}
		}
		return result, nil
	}

	// Heartbeat
	g.hb = heartbeat.New(cfg.Agent.Workspace, func(prompt string) (string, error) {
		return g.runAgent(context.Background(), prompt, "system")
	}, g.heartbeatNotify, 0, g.logger)

	// Command handler
	g.cmdHandler = channel.NewCommandHandler(g.runtime, cfg.Agent.Workspace, cfg.Agent.ContextWindow.Tokens)

	// Channels
	chMgr, err := channel.NewChannelManager(cfg, g.bus, g.logger)
	if err != nil {
		return nil, fmt.Errorf("create channel manager: %w", err)
	}
	g.channels = chMgr

	return g, nil
}

// resetSession clears the session history for the given sessionID.
func (g *Gateway) resetSession(sessionID string) error {
	if err := g.runtime.ClearSession(sessionID); err != nil {
		return fmt.Errorf("failed to clear session: %w", err)
	}
	g.usageMu.Lock()
	delete(g.usageNotified, sessionID)
	g.usageMu.Unlock()
	return nil
}

// runAgent runs the agent with the given prompt and sessionID, returning the text output.
func (g *Gateway) runAgent(ctx context.Context, prompt, sessionID string) (string, error) {
	resp, err := g.runtime.Run(ctx, api.Request{
		Prompt:    prompt,
		SessionID: sessionID,
	})
	if err != nil {
		return "", err
	}
	if resp == nil || resp.Result == nil {
		return "", nil
	}
	return resp.Result.Output, nil
}

func (g *Gateway) buildSystemPrompt() string {
	var sb strings.Builder

	if data, err := os.ReadFile(filepath.Join(g.cfg.Agent.Workspace, "AGENTS.md")); err == nil {
		sb.Write(data)
		sb.WriteString("\n\n")
	}

	if data, err := os.ReadFile(filepath.Join(g.cfg.Agent.Workspace, "RULE.md")); err == nil {
		sb.Write(data)
		sb.WriteString("\n\n")
	}

	if data, err := os.ReadFile(filepath.Join(g.cfg.Agent.Workspace, "SOUL.md")); err == nil {
		sb.Write(data)
		sb.WriteString("\n\n")
	}

	return sb.String()
}

// start initializes and starts all gateway services
func (g *Gateway) start(ctx context.Context) error {
	go g.bus.DispatchOutbound(ctx)

	// Start WebSocket RPC server (same protocol as openclaw)
	rpcAddr := fmt.Sprintf("%s:%d", g.cfg.Gateway.Host, g.cfg.Gateway.Port)
	rpcSrv := rpc.NewServer(g.logger)
	rpc.RegisterCronHandlers(rpcSrv, g.cron)
	rpc.RegisterNotifyHandlers(rpcSrv, g.bus)
	if err := rpcSrv.Start(ctx, rpcAddr); err != nil {
		return fmt.Errorf("rpc server: %w", err)
	}

	// Start core runtime loops first; channel failures should not block gateway core.
	if err := g.cron.Start(ctx); err != nil {
		g.logger.Warnf("[gateway] cron start warning: %v", err)
	}
	go func() {
		if err := g.hb.Start(ctx); err != nil {
			g.logger.Errorf("[gateway] heartbeat error: %v", err)
		}
	}()
	go g.processLoop(ctx)

	if g.runtimeMgr != nil {
		if err := g.runtimeMgr.StartEnabled(); err != nil {
			g.logger.Warnf("[gateway] plugin start-enabled failed: %v", err)
		}
	}
	g.channels.StartAll(ctx)
	g.logger.Infof("[gateway] channels configured: %v", g.channels.EnabledChannels())

	g.logger.Infof("[gateway] running on ws://%s", rpcAddr)

	// Consume restart trigger once at startup.
	g.consumeRestartTriggerOnce()
	// Telegram has no host_ready callback; do one readiness-triggered retry.
	go g.retryConsumeRestartTriggerForTelegram(ctx)

	return nil
}

func (g *Gateway) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Initial start
	if err := g.start(ctx); err != nil {
		return err
	}

	// Use injected signal channel for testing, or create default
	sigCh := g.signalChan
	if sigCh == nil {
		sigCh = make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	}
	sig := <-sigCh
	g.logger.Infof("[gateway] shutdown signal received: %v", sig)
	cancel() // Cancel context to stop all goroutines
	g.logger.Infof("[gateway] shutting down...")
	return g.Shutdown()
}

func (g *Gateway) processLoop(ctx context.Context) {
	for {
		select {
		case msg := <-g.bus.Inbound:
			if g.handleOutboundControl(msg) {
				continue
			}
			g.logger.Infof("[gateway] inbound from %s/%s: %s", msg.Channel, msg.SenderID, truncate(msg.Content, 80))
			if g.tryHandleApprovalResponse(msg) {
				continue
			}

			// Check if this is a special command
			var cmdResult channel.CommandResult
			if g.cmdHandler != nil {
				cmdResult = g.cmdHandler.HandleCommand(msg)
			}
			if cmdResult.Handled {
				g.logger.Infof("[gateway] command handled: %s", truncate(msg.Content, 40))

				// Stop typing indicator for commands
				if stopTyping, ok := msg.Metadata["stop_typing"].(chan struct{}); ok {
					close(stopTyping)
				}

				meta := map[string]any{}
				if strings.TrimSpace(cmdResult.Event) != "" {
					meta[protocol.EventTypeKey] = strings.TrimSpace(cmdResult.Event)
				}
				cmdAttachments := buildBusAttachmentsFromPaths(cmdResult.Files)
				outMsg := bus.OutboundMessage{
					Channel:     msg.Channel,
					ChatID:      msg.ChatID,
					Content:     cmdResult.Response,
					Media:       cmdResult.Files,
					Attachments: cmdAttachments,
					Metadata:    meta,
				}

				if cmdResult.Restart {
					// Keep Telegram restart pre-notice as standalone text.
					// Feishu keeps default rendering (interactive card).
					if strings.EqualFold(strings.TrimSpace(msg.Channel), "telegram") {
						if outMsg.Metadata == nil {
							outMsg.Metadata = map[string]any{}
						}
						outMsg.Metadata[protocol.EventTypeKey] = protocol.EventUsageHUD
					}

					sendNow := g.sendNowFn
					if sendNow == nil && g.channels != nil {
						sendNow = g.channels.SendNow
					}
					if sendNow == nil {
						g.logger.Errorf("[gateway] restart aborted: no direct sender for %s/%s", msg.Channel, msg.ChatID)
						continue
					}
					if err := sendNow(outMsg); err != nil {
						g.logger.Errorf("[gateway] restart pre-notification failed for %s/%s: %v", msg.Channel, msg.ChatID, err)
						if strings.EqualFold(strings.TrimSpace(msg.Channel), "interaction") {
							g.logger.Warnf("[gateway] continue restart despite interaction pre-notification failure for %s/%s", msg.Channel, msg.ChatID)
						} else {
							continue
						}
					}

					restartNow := g.restartFn
					if restartNow == nil {
						restartNow = g.executeRestartScript
					}
					if err := restartNow(); err != nil {
						g.logger.Errorf("[gateway] restart execution failed: %v", err)
						_ = sendNow(bus.OutboundMessage{
							Channel: msg.Channel,
							ChatID:  msg.ChatID,
							Content: fmt.Sprintf("❌ Failed to restart: %v", err),
						})
					}
					continue
				}

				if outMsg.Content != "" || len(outMsg.Attachments) > 0 || len(outMsg.Media) > 0 {
					g.bus.Outbound <- outMsg
				}
				continue
			}

			// 异步处理 agent
			go g.processAgent(ctx, msg)
		case <-ctx.Done():
			return
		}
	}
}

func (g *Gateway) handlePermissionRequest(ctx context.Context, req api.PermissionRequest) (events.PermissionDecisionType, error) {
	sessionID := strings.TrimSpace(req.SessionID)
	channelID, chatID := parseSessionID(sessionID)
	if channelID == "" || chatID == "" {
		return events.PermissionAsk, nil
	}
	requestID := fmt.Sprintf("%d", time.Now().UnixNano())
	pending := pendingApproval{
		requestID: requestID,
		channel:   channelID,
		chatID:    chatID,
		toolName:  strings.TrimSpace(req.ToolName),
		display:   strings.TrimSpace(req.Display),
		target:    strings.TrimSpace(req.Target),
		decisionC: make(chan events.PermissionDecisionType, 1),
	}
	g.approvalMu.Lock()
	g.pendingApproval[sessionID] = pending
	g.approvalMu.Unlock()

	g.bus.Outbound <- bus.OutboundMessage{
		Channel: channelID,
		ChatID:  chatID,
		Content: buildApprovalPrompt(pending.toolName, pending.display, pending.target),
		Metadata: map[string]any{
			"approval_prompt": true,
			"approval_id":     requestID,
		},
	}

	select {
	case decision := <-pending.decisionC:
		g.approvalMu.Lock()
		delete(g.pendingApproval, sessionID)
		g.approvalMu.Unlock()
		return decision, nil
	case <-ctx.Done():
		g.approvalMu.Lock()
		delete(g.pendingApproval, sessionID)
		g.approvalMu.Unlock()
		return events.PermissionAsk, ctx.Err()
	}
}

func (g *Gateway) tryHandleApprovalResponse(msg bus.InboundMessage) bool {
	decision, approvalID, handled := parseApprovalDecision(msg)
	if !handled {
		return false
	}

	sessionID := msg.SessionKey()
	g.approvalMu.Lock()
	pending, ok := g.pendingApproval[sessionID]
	g.approvalMu.Unlock()
	if !ok {
		return true
	}
	if approvalID != "" && approvalID != pending.requestID {
		return true
	}
	select {
	case pending.decisionC <- decision:
	default:
	}
	if decision == events.PermissionAllow {
		return true
	}
	g.bus.Outbound <- bus.OutboundMessage{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		ReplyTo: inboundReplyTo(msg),
		Content: "已拒绝本次命令执行。",
	}
	return true
}

func parseSessionID(sessionID string) (channelID string, chatID string) {
	parts := strings.SplitN(strings.TrimSpace(sessionID), ":", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
}

func buildApprovalPrompt(toolName, display, target string) string {
	toolName = strings.TrimSpace(toolName)
	display = strings.TrimSpace(display)
	target = strings.TrimSpace(target)
	if toolName == "" {
		toolName = "工具"
	}
	if display == "" {
		display = target
	}
	if display == "" {
		return fmt.Sprintf("命令需要审批：`%s`", toolName)
	}
	return fmt.Sprintf("命令需要审批：`%s %s`", toolName, display)
}

func parseApprovalDecision(msg bus.InboundMessage) (events.PermissionDecisionType, string, bool) {
	if msg.Metadata != nil {
		approvalID, _ := msg.Metadata["approval_id"].(string)
		if action, ok := msg.Metadata["approval_action"].(string); ok {
			switch strings.ToLower(strings.TrimSpace(action)) {
			case "allow", "approve", "yes":
				return events.PermissionAllow, strings.TrimSpace(approvalID), true
			case "deny", "reject", "no":
				return events.PermissionDeny, strings.TrimSpace(approvalID), true
			}
		}
	}
	return events.PermissionAsk, "", false
}

func (g *Gateway) processAgent(ctx context.Context, msg bus.InboundMessage) {
	// Set current execution context for realtime callbacks
	g.currentChannelID = msg.Channel
	g.currentChatID = msg.ChatID
	g.currentReplyTo = inboundReplyTo(msg)
	defer func() {
		g.currentChannelID = ""
		g.currentChatID = ""
		g.currentReplyTo = ""
	}()

	// Stop typing indicator when processing completes (deferred)
	if stopTyping, ok := msg.Metadata["stop_typing"].(chan struct{}); ok {
		defer close(stopTyping)
	}

	attachments := buildAttachments(msg)
	g.clearToolProgress(msg.Channel, msg.ChatID)
	g.clearOutboundTurnState(msg.Channel, msg.ChatID)
	if strings.EqualFold(strings.TrimSpace(msg.Channel), "interaction") {
		if g.markReactionSent(msg.Channel, msg.ChatID) {
			g.bus.Outbound <- bus.OutboundMessage{
				Channel: msg.Channel,
				ChatID:  msg.ChatID,
				ReplyTo: inboundReplyTo(msg),
				Metadata: map[string]any{
					protocol.EventTypeKey: protocol.EventReactionOnIt,
				},
			}
		}
	}
	if len(attachments) > 0 {
		imageCount := 0
		audioCount := 0
		for _, att := range attachments {
			switch strings.ToLower(strings.TrimSpace(att.Type)) {
			case "audio":
				audioCount++
			default:
				imageCount++
			}
		}
		g.logger.Infof("[gateway] processing attachments: total=%d image=%d audio=%d", len(attachments), imageCount, audioCount)
	}

	req := api.Request{
		Prompt:      msg.Content,
		SessionID:   msg.SessionKey(),
		Attachments: attachments,
	}

	// Channels with preview support: prefer stream path with message preview editing.
	if supportsPreviewStream(msg.Channel) {
		if handled := g.processAgentStream(ctx, msg, req); handled {
			return
		}
	}

	resp, err := g.runtime.Run(ctx, req)
	if err != nil {
		g.emitAgentError(msg, err)
		return
	}
	g.deliverAgentResponse(msg, resp, false)
}

const (
	usageMark30 = 1 << 0
	usageMark50                = 1 << 1
	usageMark80                = 1 << 2
)

type previewPhase uint8

const (
	previewPhaseIdle previewPhase = iota
	previewPhaseAnchorPendingAck
	previewPhaseAnchored
)

type previewFlowState struct {
	phase           previewPhase
	lastPreviewLen  int
	bufferedPreview string
	previewSent     bool
}

func (s *previewFlowState) onPreviewDispatched(targetMessageID string) {
	if strings.TrimSpace(targetMessageID) == "" {
		s.phase = previewPhaseAnchorPendingAck
		return
	}
	s.phase = previewPhaseAnchored
}

func (s *previewFlowState) advancePreviewState(cur, anchorID string) (string, bool) {
	if s.phase == previewPhaseAnchorPendingAck && strings.TrimSpace(anchorID) != "" {
		s.phase = previewPhaseAnchored
		if strings.TrimSpace(s.bufferedPreview) != "" {
			next := s.bufferedPreview
			s.bufferedPreview = ""
			s.lastPreviewLen = len(cur)
			s.previewSent = true
			return next, true
		}
	}
	if cur == "" || len(cur) == s.lastPreviewLen {
		return "", false
	}
	if s.phase == previewPhaseAnchorPendingAck && strings.TrimSpace(anchorID) == "" {
		s.bufferedPreview = cur
		s.lastPreviewLen = len(cur)
		s.previewSent = true
		return "", false
	}
	s.lastPreviewLen = len(cur)
	s.previewSent = true
	return cur, true
}

func (g *Gateway) finalizePreviewState(channel, chatID string, previewSent bool) map[string]any {
	if !previewSent || !supportsPreviewStream(channel) {
		return map[string]any{}
	}
	if mid := g.outboundMessageID(channel, chatID, outboundMessagePreview); mid == "" {
		return map[string]any{}
	}
	meta := map[string]any{
		protocol.EventTypeKey: protocol.EventPreviewFinal,
	}
	meta["message_id"] = g.outboundMessageID(channel, chatID, outboundMessagePreview)
	return g.attachOutboundRequest(channel, chatID, outboundMessagePreview, meta)
}

const minPreviewChunk = 64 // event-driven throttle: send preview when accumulated text grew by at least this many runes

func (g *Gateway) processAgentStream(ctx context.Context, msg bus.InboundMessage, req api.Request) bool {
	stream, err := g.runtime.RunStream(ctx, req)
	if err != nil {
		g.logger.Warnf("[gateway] stream unavailable, fallback to non-stream: %v", err)
		return false
	}

	var (
		sb        strings.Builder
		streamErr error
		finalResp *api.Response
		state     previewFlowState
	)

	sendPreview := func(mode string, content string) {
		if content == "" {
			return
		}
		meta := protocol.OutboundMeta{EventType: mode}.ToMap()
		if mid := g.outboundMessageID(msg.Channel, msg.ChatID, outboundMessagePreview); mid != "" {
			if meta == nil {
				meta = map[string]any{}
			}
			meta["message_id"] = mid
		}
		meta = g.attachOutboundRequest(msg.Channel, msg.ChatID, outboundMessagePreview, meta)
		g.bus.Outbound <- bus.OutboundMessage{
			Channel:  msg.Channel,
			ChatID:   msg.ChatID,
			ReplyTo:  inboundReplyTo(msg),
			Content:  content,
			Metadata: meta,
		}
		if mode == protocol.EventPreviewUpdate {
			state.onPreviewDispatched(strings.TrimSpace(protocol.MetaString(meta, "message_id")))
		}
	}

	trySendPreview := func() {
		cur := sb.String()
		anchorID := g.outboundMessageID(msg.Channel, msg.ChatID, outboundMessagePreview)
		// Event-driven: only advance when enough new content (or first chunk / buffer flush)
		growth := len(cur) - state.lastPreviewLen
		if growth < minPreviewChunk && state.phase != previewPhaseIdle && state.phase != previewPhaseAnchorPendingAck {
			return
		}
		if state.phase == previewPhaseIdle && len(cur) == 0 {
			return
		}
		if next, ok := state.advancePreviewState(cur, anchorID); ok {
			sendPreview(protocol.EventPreviewUpdate, next)
		}
	}

	flushRemainingPreview := func() {
		cur := sb.String()
		anchorID := g.outboundMessageID(msg.Channel, msg.ChatID, outboundMessagePreview)
		if next, ok := state.advancePreviewState(cur, anchorID); ok {
			sendPreview(protocol.EventPreviewUpdate, next)
		}
	}

	for {
		select {
		case <-ctx.Done():
			return true
		case evt, ok := <-stream:
			if !ok {
				// Flush remaining preview (no min-chunk throttle) before final
				flushRemainingPreview()
				if streamErr != nil {
					g.emitAgentError(msg, streamErr)
					return true
				}
				if finalResp != nil {
					g.deliverAgentResponse(msg, finalResp, state.previewSent)
					return true
				}
				g.deliverStreamAccumulatedFallback(msg, strings.TrimSpace(sb.String()), state.previewSent)
				return true
			}

			switch evt.Type {
			case api.EventError:
				if s, ok := evt.Output.(string); ok && s != "" {
					streamErr = fmt.Errorf("%s", s)
				} else {
					streamErr = fmt.Errorf("stream error")
				}
			case api.EventContentBlockDelta:
				if evt.Delta != nil && evt.Delta.Type == "text_delta" && evt.Delta.Text != "" {
					sb.WriteString(evt.Delta.Text)
					trySendPreview()
				}
			case api.EventFinalResponse:
				switch out := evt.Output.(type) {
				case *api.Response:
					finalResp = out
				case api.Response:
					tmp := out
					finalResp = &tmp
				}
			}
		}
	}
}

func (g *Gateway) deliverStreamAccumulatedFallback(msg bus.InboundMessage, raw string, previewSent bool) {
	if raw == "" {
		return
	}
	g.deliverAgentResponse(msg, &api.Response{
		Result: &api.Result{Output: raw},
	}, previewSent)
}

func (g *Gateway) takePendingFinalTextIfAnchored(channel, chatID string) string {
	key := toolLogChatKey(channel, chatID)
	g.stateMu.Lock()
	defer g.stateMu.Unlock()
	state := g.outboundState[key]
	if strings.TrimSpace(state.PreviewMessageID) == "" || strings.TrimSpace(state.PendingFinalContent) == "" {
		return ""
	}
	content := state.PendingFinalContent
	state.PendingFinalContent = ""
	g.outboundState[key] = state
	return content
}

func (g *Gateway) emitFinalText(msg bus.InboundMessage, content string, previewSent bool) {
	content = strings.TrimSpace(content)
	if content == "" {
		return
	}
	if previewSent && supportsPreviewStream(msg.Channel) {
		meta := g.finalizePreviewState(msg.Channel, msg.ChatID, true)
		if mode := strings.TrimSpace(protocol.MetaString(meta, protocol.EventTypeKey)); mode == protocol.EventPreviewFinal {
			g.bus.Outbound <- bus.OutboundMessage{
				Channel:  msg.Channel,
				ChatID:   msg.ChatID,
				ReplyTo:  inboundReplyTo(msg),
				Content:  content,
				Metadata: meta,
			}
			return
		}
		key := toolLogChatKey(msg.Channel, msg.ChatID)
		g.stateMu.Lock()
		if g.outboundState == nil {
			g.outboundState = map[string]outboundMessageState{}
		}
		state := g.outboundState[key]
		state.PendingFinalContent = content
		g.outboundState[key] = state
		g.stateMu.Unlock()
		g.logger.Infof("[gateway] final pending wait-anchor channel=%s chat=%s content_len=%d", msg.Channel, msg.ChatID, len(content))
		return
	}
	g.bus.Outbound <- bus.OutboundMessage{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		ReplyTo: inboundReplyTo(msg),
		Content: content,
	}
}

func (g *Gateway) emitAgentError(msg bus.InboundMessage, err error) {
	g.logger.Errorf("[gateway] agent error: %v", err)
	var errorMsg string
	if strings.Contains(err.Error(), "max iterations reached") {
		errorMsg = "抱歉，这个任务太复杂了，我尝试了太多次工具调用。请简化您的请求或分步骤提问。"
	} else if strings.Contains(err.Error(), "context deadline exceeded") {
		errorMsg = "抱歉，处理超时了。请稍后再试或简化您的请求。"
	} else {
		errorMsg = "抱歉，处理您的消息时遇到了错误。"
	}
	g.bus.Outbound <- bus.OutboundMessage{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		ReplyTo: inboundReplyTo(msg),
		Content: errorMsg,
	}
}

func (g *Gateway) deliverAgentResponse(msg bus.InboundMessage, resp *api.Response, previewSent bool) {
	if resp == nil {
		return
	}
	defer g.emitTelegramUsageHUD(msg, resp)

	// 构建响应内容（优先级：Commands > Skills > AskUserQuestion > Subagent > Result.Output）
	var content strings.Builder
	for _, cmdRes := range resp.CommandResults {
		if output, ok := cmdRes.Result.Output.(string); ok && output != "" {
			content.WriteString(output)
			content.WriteString("\n\n")
		}
	}
	for _, skillRes := range resp.SkillResults {
		if output, ok := skillRes.Result.Output.(string); ok && output != "" {
			content.WriteString(output)
			content.WriteString("\n\n")
		}
	}

	hookResult := g.processHookEvents(resp)
	for _, media := range hookResult.media {
		g.logger.Infof("[gateway] SendFile detected: %s", media.path)
		att := normalizeBusAttachment(api.Attachment{
			FilePath: media.path,
			Type:     media.kind,
			MimeType: media.mime,
		})
		if att.FilePath == "" || att.Type == "" {
			continue
		}
		outMeta := map[string]any{}
		if strings.EqualFold(strings.TrimSpace(media.tool), "voice_tts") {
			if converted, convErr := channel.TranscodeToFeishuOpusForGateway(att.FilePath); convErr != nil {
				g.logger.Warnf("[gateway] voice_tts transcode skipped path=%s err=%v", att.FilePath, convErr)
			} else {
				att.FilePath = converted
				att.Type = "audio"
				att.MimeType = "audio/ogg"
			}
			outMeta["voice_duration_ms"] = channel.DetectAudioDurationMillisForGateway(att.FilePath)
			outMeta[protocol.ActionKey] = protocol.ActionSendAudio
		} else {
			if strings.EqualFold(strings.TrimSpace(att.Type), "audio") {
				outMeta[protocol.ActionKey] = protocol.ActionSendAudio
			} else {
				outMeta[protocol.ActionKey] = protocol.ActionSendMedia
			}
		}
		g.bus.Outbound <- bus.OutboundMessage{
			Channel:     msg.Channel,
			ChatID:      msg.ChatID,
			Media:       []string{att.FilePath},
			Attachments: []api.Attachment{att},
			Metadata:    outMeta,
		}
		g.logger.Infof("[gateway] media outbound queued channel=%s chat=%s action=%s tool=%s path=%s",
			msg.Channel,
			msg.ChatID,
			strings.TrimSpace(protocol.MetaString(outMeta, protocol.ActionKey)),
			strings.TrimSpace(media.tool),
			att.FilePath,
		)
	}

	if hookResult.askQuestion != "" {
		g.logger.Infof("[gateway] AskUserQuestion: %s", truncate(hookResult.askQuestion, 60))
		g.emitFinalText(msg, hookResult.askQuestion, previewSent)
		return
	}

	if resp.Subagent != nil {
		if output, ok := resp.Subagent.Output.(string); ok && output != "" {
			content.WriteString(output)
			content.WriteString("\n\n")
		}
	}
	if resp.Result != nil && resp.Result.Output != "" {
		content.WriteString(resp.Result.Output)
	}
	result := strings.TrimSpace(content.String())

	if result != "" {
		g.logger.Infof("[gateway] outbound to %s/%s: %s", msg.Channel, msg.ChatID, truncate(result, 80))
		g.emitFinalText(msg, result, previewSent)
		} else if len(hookResult.media) == 0 {
		if resp.Result != nil {
			g.logger.Infof("[gateway] stop-reason fallback channel=%s chat=%s stop_reason=%s preview_sent=%v",
				msg.Channel, msg.ChatID, strings.TrimSpace(resp.Result.StopReason), previewSent)
			switch strings.TrimSpace(resp.Result.StopReason) {
			case api.StopReasonPermissionDenied:
				g.emitFinalText(msg, "已拒绝执行该命令。", previewSent)
				return
			case api.StopReasonApprovalRequired:
				g.emitFinalText(msg, "该命令需要审批，请先完成审批操作。", previewSent)
				return
			}
		}
		g.logger.Warnf("[gateway] no response generated for %s/%s", msg.Channel, msg.SenderID)
	}

	if hookResult.memoryNotice != "" {
		g.bus.Outbound <- bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			ReplyTo: inboundReplyTo(msg),
			Content: hookResult.memoryNotice,
		}
	}
}

func (g *Gateway) emitTelegramUsageHUD(msg bus.InboundMessage, resp *api.Response) {
	if !supportsPreviewStream(msg.Channel) || g.runtime == nil {
		return
	}
	contextWindowTokens := 0
	if g.cfg != nil {
		contextWindowTokens = g.cfg.Agent.ContextWindow.Tokens
	}
	if contextWindowTokens <= 0 || resp == nil || resp.Result == nil {
		return
	}
	input := resp.Result.Usage.InputTokens
	if input <= 0 {
		return
	}
	ratio := float64(input) / float64(contextWindowTokens)
	reached := usageThresholdMask(ratio * 100)
	if reached == 0 {
		return
	}
	sessionID := msg.SessionKey()
	g.usageMu.Lock()
	prev := g.usageNotified[sessionID]
	if reached&^prev == 0 {
		g.usageMu.Unlock()
		return
	}
	g.usageNotified[sessionID] = prev | reached
	g.usageMu.Unlock()

	stats := g.runtime.GetSessionStats(msg.SessionKey())
	if stats == nil {
		return
	}
	content := formatUsageHUD(stats, resp, contextWindowTokens)
	g.bus.Outbound <- bus.OutboundMessage{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: content,
		Metadata: map[string]any{
			protocol.EventTypeKey: protocol.EventUsageHUD,
		},
	}
}

func supportsPreviewStream(channel string) bool {
	switch strings.ToLower(strings.TrimSpace(channel)) {
	case "telegram", "feishu", "interaction":
		return true
	default:
		return false
	}
}

func hasEnabledPlugins(cfg *config.Config) bool {
	if cfg == nil || len(cfg.Plugins.Entries) == 0 {
		return false
	}
	for _, entry := range cfg.Plugins.Entries {
		if entry.Enabled {
			return true
		}
	}
	return false
}

func formatUsageHUD(stats *api.SessionTokenStats, resp *api.Response, contextWindowTokens int) string {
	if stats == nil {
		return ""
	}
	inputTokens := 0
	if resp != nil && resp.Result != nil {
		inputTokens = resp.Result.Usage.InputTokens
	}
	return usagehud.Format("📊 Usage", stats, inputTokens, contextWindowTokens)
}

func usageThresholdMask(percent float64) uint8 {
	var mask uint8
	if percent >= 30 {
		mask |= usageMark30
	}
	if percent >= 50 {
		mask |= usageMark50
	}
	if percent >= 80 {
		mask |= usageMark80
	}
	return mask
}

type hookMedia struct {
	path string
	kind string
	mime string
	tool string
}

// hookEventResult holds all data extracted from a single pass over resp.HookEvents.
type hookEventResult struct {
	media        []hookMedia
	askQuestion  string
	memoryNotice string
}

// processHookEvents iterates resp.HookEvents once and extracts all relevant data.
func (g *Gateway) processHookEvents(resp *api.Response) hookEventResult {
	if resp == nil {
		return hookEventResult{}
	}

	type memWrite struct {
		path  string
		bytes int
	}
	var (
		res       hookEventResult
		toolNames []string
		memWrites []memWrite
	)

	for _, event := range resp.HookEvents {
		switch event.Type {
		case events.PostToolUse:
			payload, ok := event.Payload.(events.ToolResultPayload)
			if !ok {
				continue
			}
			toolNames = append(toolNames, payload.Name)

			switch payload.Name {
			case "AskUserQuestion", "ask_user_question":
				if output, ok := payload.Result.(string); ok && output != "" {
					res.askQuestion = output
				}

			case "memory_write":
				if payload.Err != nil {
					continue
				}
				path, _ := payload.Params["path"].(string)
				if path == "" {
					path = "memory"
				}
				n := 0
				if output, ok := payload.Result.(string); ok {
					fmt.Sscanf(output, "Appended %d", &n)
					if n == 0 {
						fmt.Sscanf(output, "Written %d", &n)
					}
				}
				memWrites = append(memWrites, memWrite{path: path, bytes: n})
			}

		case events.FileAttachment:
			payload, ok := event.Payload.(events.FileAttachmentPayload)
			if !ok {
				continue
			}
			g.logger.Debugf("[gateway] FileAttachment: path=%s tool=%s", payload.FilePath, payload.ToolName)
			if payload.FilePath == "" {
				continue
			}
			toolName := strings.ToLower(strings.TrimSpace(payload.ToolName))
			// Send explicit tool files and voice pipeline audio attachments.
			if toolName == "sendfile" || toolName == "voice_tts" {
				res.media = append(res.media, hookMedia{
					path: payload.FilePath,
					kind: strings.ToLower(strings.TrimSpace(payload.Type)),
					mime: strings.TrimSpace(payload.MimeType),
					tool: toolName,
				})
			}
		}
	}

	if len(toolNames) > 0 {
		g.logger.Debugf("[gateway] PostToolUse: used %d tool(s): %v", len(toolNames), toolNames)
	}
	if len(res.media) > 0 {
		g.logger.Infof("[gateway] Extracted %d file attachment(s) from hooks", len(res.media))
	}

	// Build memory notice
	if len(memWrites) > 0 {
		parts := make([]string, 0, len(memWrites))
		for _, w := range memWrites {
			if w.bytes > 0 {
				parts = append(parts, fmt.Sprintf("📝 %s (+%d bytes)", w.path, w.bytes))
			} else {
				parts = append(parts, fmt.Sprintf("📝 %s", w.path))
			}
		}
		res.memoryNotice = strings.Join(parts, "\n")
	}

	return res
}

func (g *Gateway) Shutdown() error {
	g.cron.Stop()
	if g.runtimeMgr != nil {
		if err := g.runtimeMgr.StopAll(); err != nil {
			g.logger.Warnf("[gateway] plugin stop-all failed: %v", err)
		}
	}
	_ = g.channels.StopAll()
	if g.runtime != nil {
		g.runtime.Close()
	}
	g.logger.Infof("[gateway] shutdown complete")
	return nil
}

func (g *Gateway) executeRestartScript() error {
	if g.cmdHandler == nil {
		return fmt.Errorf("command handler not initialized")
	}
	scriptPath := g.cmdHandler.RestartScriptPath()
	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		return fmt.Errorf("restart script not found: %s", scriptPath)
	}
	cmd := exec.Command("/bin/bash", scriptPath)
	return cmd.Start()
}

// consumeRestartTriggerOnce is the single entrypoint for restart-trigger consumption.
func (g *Gateway) consumeRestartTriggerOnce() {
	// Check if there's a restart trigger file
	restartTriggerFile := channel.RestartTriggerFilePath()
	data, err := os.ReadFile(restartTriggerFile)
	if err != nil {
		// No trigger file, skip notification
		g.logger.Debug("[gateway] no restart trigger file found, skipping startup notification")
		return
	}

	var trigger restartTrigger
	if err := json.Unmarshal(data, &trigger); err != nil {
		g.logger.Warnf("[gateway] invalid restart trigger json: %v raw=%s", err, strings.TrimSpace(string(data)))
		_ = os.Remove(restartTriggerFile)
		return
	}
	channelName := strings.TrimSpace(trigger.Channel)
	chatID := strings.TrimSpace(trigger.ChatID)
	if channelName == "" || chatID == "" {
		g.logger.Warnf("[gateway] invalid restart trigger payload: channel=%q chat_id=%q", trigger.Channel, trigger.ChatID)
		_ = os.Remove(restartTriggerFile)
		return
	}
	pluginID, platform := interactionPluginPlatformFromSessionChatID(chatID)
	notifyKey := strings.ToLower(channelName) + "|" + strings.TrimSpace(chatID)
	if !g.beginRestartNotification(notifyKey) {
		g.logger.Infof("[gateway] startup notification skipped: already in-flight/done key=%s", notifyKey)
		return
	}
	defer func() {
		// keep done state after success; release in-flight after failure.
		g.endRestartNotification(notifyKey, false)
	}()

	// Get PID for status
	pid := os.Getpid()

	startupMsg := fmt.Sprintf("✅ **Gateway Restarted Successfully**\n\nPID: %d\nTime: %s",
		pid, time.Now().Format("2006-01-02 15:04:05"))

	isInteractionRuntimeReady := func() bool {
		if !strings.EqualFold(strings.TrimSpace(channelName), "interaction") {
			return true
		}
		if g.runtimeMgr == nil {
			return true
		}
		if pluginID == "" {
			g.logger.Warnf("[gateway] startup notification interaction chat id has no plugin id: chat=%s", chatID)
			return true
		}
		ok, err := g.runtimeMgr.IsRunning(pluginID)
		if err != nil {
			g.logger.Warnf("[gateway] startup notification runtime check failed plugin=%s err=%v", pluginID, err)
			return false
		}
		return ok
	}
	isInteractionHostReady := func() bool {
		if !strings.EqualFold(strings.TrimSpace(channelName), "interaction") {
			return true
		}
		if pluginID == "" {
			return true
		}
		return g.isHostReady(pluginID, platform)
	}

	send := func() bool {
		if err := g.sendStartupOnlineMessage(channelName, chatID, startupMsg); err != nil {
			g.logger.Warnf("[gateway] startup notification send failed channel=%s chat=%s err=%v", channelName, chatID, err)
			return false
		}
		g.endRestartNotification(notifyKey, true)
		if err := os.Remove(restartTriggerFile); err != nil && !os.IsNotExist(err) {
			g.logger.Warnf("[gateway] startup notification trigger remove failed file=%s err=%v", restartTriggerFile, err)
		}
		return true
	}

	runtimeReady := isInteractionRuntimeReady()
	hostReady := isInteractionHostReady()
	if runtimeReady && hostReady {
		if send() {
			return
		}
		return
	}
	g.logger.Warnf("[gateway] startup notification skipped: not-ready channel=%s chat=%s runtime_ready=%v host_ready=%v",
		channelName, chatID, runtimeReady, hostReady)
}

func (g *Gateway) retryConsumeRestartTriggerForTelegram(ctx context.Context) {
	waitReady := g.waitReadyFn
	if waitReady == nil && g.channels != nil {
		waitReady = g.channels.WaitReady
	}
	if waitReady == nil {
		return
	}
	waitCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if waitReady(waitCtx, "telegram") {
		g.consumeRestartTriggerOnce()
	}
}

func (g *Gateway) sendStartupOnlineMessage(channelName, chatID, content string) error {
	g.logger.Infof("[gateway] sending restart notification to %s/%s", channelName, chatID)
	sendNow := g.sendNowFn
	if sendNow == nil && g.channels != nil {
		sendNow = g.channels.SendNow
	}
	if sendNow == nil {
		return fmt.Errorf("no direct sender available")
	}
	return sendNow(bus.OutboundMessage{
		Channel: channelName,
		ChatID:  chatID,
		Content: content,
	})
}

func interactionPluginPlatformFromSessionChatID(sessionChatID string) (string, string) {
	parts := strings.SplitN(strings.TrimSpace(sessionChatID), ":", 3)
	if len(parts) < 3 {
		return "", ""
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
}

// heartbeatNotify delivers a heartbeat agent response to the user.
// It sends to the last active session, falling back to the first configured
// Telegram allowFrom user if no session is currently active.
func (g *Gateway) heartbeatNotify(result string) {
	channelID := g.currentChannelID
	chatID := g.currentChatID

	// Fallback: use first Telegram allowFrom
	if channelID == "" || chatID == "" {
		if len(g.cfg.Channels.Telegram.AllowFrom) > 0 {
			channelID = "telegram"
			chatID = g.cfg.Channels.Telegram.AllowFrom[0]
		}
	}

	if channelID == "" || chatID == "" {
		g.logger.Warnf("[heartbeat] cannot notify: no active session and no allowFrom configured")
		return
	}

	g.logger.Infof("[heartbeat] notifying user channel=%s chatID=%s", channelID, chatID)
	g.bus.Outbound <- bus.OutboundMessage{
		Channel: channelID,
		ChatID:  chatID,
		Content: result,
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func inboundReplyTo(msg bus.InboundMessage) string {
	if msg.Metadata == nil {
		return ""
	}
	v, ok := msg.Metadata["message_id"]
	if !ok || v == nil {
		return ""
	}
	switch id := v.(type) {
	case int:
		if id > 0 {
			return strconv.Itoa(id)
		}
	case int64:
		if id > 0 {
			return strconv.FormatInt(id, 10)
		}
	case float64:
		if id > 0 {
			return strconv.Itoa(int(id))
		}
	case string:
		id = strings.TrimSpace(id)
		if id != "" {
			return id
		}
	}
	return ""
}

func formatProgressParams(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return ""
	}
	return "```text\n" + raw + "\n```"
}

func toolLogChatKey(channel, chatID string) string {
	return strings.TrimSpace(strings.ToLower(channel)) + ":" + strings.TrimSpace(chatID)
}

func (g *Gateway) clearToolProgress(channel, chatID string) {
	key := toolLogChatKey(channel, chatID)
	g.toolLogMu.Lock()
	delete(g.toolLogByChatKey, key)
	g.toolLogMu.Unlock()
}

func (g *Gateway) appendToolProgressBlock(channel, chatID, name, params string) string {
	key := toolLogChatKey(channel, chatID)
	entry := toolLogEntry{
		Name:      strings.TrimSpace(name),
		ParamsRaw: strings.TrimSpace(params),
	}
	g.toolLogMu.Lock()
	entries := append(g.toolLogByChatKey[key], entry)
	for len(entries) > 1 && len(formatToolProgressBlock(entries)) > 3600 {
		entries = entries[1:]
	}
	g.toolLogByChatKey[key] = entries
	g.toolLogMu.Unlock()
	return formatToolProgressBlock(entries)
}

func formatToolProgressBlock(entries []toolLogEntry) string {
	if len(entries) == 0 {
		return "🧰 Tool Calls\n（等待工具调用）"
	}
	var b strings.Builder
	b.WriteString("🧰 Tool Calls")
	for _, e := range entries {
		name := strings.TrimSpace(e.Name)
		if name == "" {
			name = "Tool"
		}
		raw := strings.TrimSpace(e.ParamsRaw)
		if raw == "" || raw == "{}" {
			raw = strings.TrimSpace(e.Raw)
		}
		if raw == "" {
			raw = "{}"
		}
		b.WriteString("\n\n⏳ ")
		b.WriteString(name)
		b.WriteString("\n```text\n")
		b.WriteString(raw)
		b.WriteString("\n```")
	}
	return b.String()
}

func buildAttachments(msg bus.InboundMessage) []api.Attachment {
	busAttachments := msg.Attachments
	if len(busAttachments) == 0 && len(msg.Media) > 0 {
		busAttachments = buildBusAttachmentsFromPaths(msg.Media)
	}
	if len(busAttachments) == 0 {
		return nil
	}

	attachments := make([]api.Attachment, 0, len(busAttachments))
	for _, raw := range busAttachments {
		att := normalizeBusAttachment(raw)
		if att.FilePath == "" || att.Type == "" {
			continue
		}
		attachments = append(attachments, api.Attachment{
			Type:     att.Type,
			FilePath: att.FilePath,
			MimeType: att.MimeType,
		})
	}
	return attachments
}

func buildBusAttachmentsFromPaths(paths []string) []api.Attachment {
	out := make([]api.Attachment, 0, len(paths))
	for _, path := range paths {
		att := normalizeBusAttachment(api.Attachment{FilePath: path})
		if att.FilePath == "" || att.Type == "" {
			continue
		}
		out = append(out, att)
	}
	return out
}

func normalizeBusAttachment(att api.Attachment) api.Attachment {
	path := strings.TrimSpace(att.FilePath)
	if path == "" {
		return api.Attachment{}
	}
	mime := strings.TrimSpace(att.MimeType)
	if mime == "" {
		mime = strings.TrimSpace(api.DetectAttachmentMIME("", path))
	}
	kind := strings.ToLower(strings.TrimSpace(att.Type))
	if kind == "" {
		kind = api.DetectAttachmentTypeFromMIME(mime)
	}
	switch kind {
	case "image", "audio", "file":
	default:
		return api.Attachment{}
	}
	return api.Attachment{
		FilePath: path,
		Type:     kind,
		MimeType: mime,
	}
}

func (g *Gateway) handleOutboundControl(msg bus.InboundMessage) bool {
	switch protocol.EventType(msg.Metadata) {
	case protocol.EventHostReady:
		pluginID := strings.TrimSpace(protocol.MetaString(msg.Metadata, "plugin_id"))
		platform := strings.TrimSpace(protocol.MetaString(msg.Metadata, "platform"))
		if pluginID == "" {
			if p, pf := interactionPluginPlatformFromSessionChatID(msg.ChatID); p != "" {
				pluginID, platform = p, pf
			}
		}
		if pluginID != "" {
			g.markHostReady(pluginID, platform)
			g.logger.Infof("[gateway] interaction host ready plugin=%s platform=%s", pluginID, platform)
			go g.consumeRestartTriggerOnce()
		}
		return true
	case protocol.EventToolCatalog:
		pluginID := strings.TrimSpace(protocol.MetaString(msg.Metadata, "plugin_id"))
		platform := strings.TrimSpace(protocol.MetaString(msg.Metadata, "platform"))
		if pluginID == "" {
			pluginID, platform = interactionPluginPlatformFromSessionChatID(msg.ChatID)
		}
		if pluginID == "" {
			return true
		}
		tools := parsePluginToolCatalog(msg.Metadata)
		g.setPluginToolCatalog(pluginID, platform, tools)
		names := make([]string, 0, len(tools))
		for _, t := range tools {
			if n := strings.TrimSpace(t.Name); n != "" {
				names = append(names, n)
			}
		}
		g.logger.Infof("[gateway] plugin_tool_catalog plugin=%s platform=%s count=%d names=%v", pluginID, platform, len(tools), names)
		g.recreateRuntimeWithPluginTools()
		return true
	case protocol.EventOutboundResult:
		// continue below
	default:
		return false
	}
	g.applyOutboundAck(msg)
	return true
}

func (g *Gateway) applyOutboundAck(msg bus.InboundMessage) {
	source := strings.ToLower(strings.TrimSpace(protocol.MetaString(msg.Metadata, "source_event_type")))
	messageID := strings.TrimSpace(protocol.MetaString(msg.Metadata, "message_id"))
	requestID := strings.TrimSpace(protocol.MetaString(msg.Metadata, protocol.RequestIDKey))
	controlChatID := strings.TrimSpace(msg.ChatID)
	if strings.EqualFold(strings.TrimSpace(msg.Channel), "interaction") {
		controlChatID = strings.TrimSpace(protocol.MetaString(msg.Metadata, protocol.SessionChatIDKey))
		if controlChatID == "" {
			if g.logger != nil {
				g.logger.Warnf("[gateway] outbound_result dropped: missing session_chat_id channel=%s source=%s request_id=%s", msg.Channel, source, requestID)
			}
			return
		}
	}
	if messageID == "" || requestID == "" {
		if g.logger != nil {
			g.logger.Warnf("[gateway] outbound_result dropped: missing required ids channel=%s chat=%s source=%s request_id=%s message_id=%s",
				msg.Channel, controlChatID, source, requestID, messageID)
		}
		return
	}
	if kind, ok := g.applyOutboundResultByRequest(msg.Channel, controlChatID, requestID, messageID); ok {
		if kind == outboundMessagePreview {
			if pending := g.takePendingFinalTextIfAnchored(msg.Channel, controlChatID); pending != "" {
				meta := g.finalizePreviewState(msg.Channel, controlChatID, true)
				if mode := strings.TrimSpace(protocol.MetaString(meta, protocol.EventTypeKey)); mode == protocol.EventPreviewFinal {
					g.bus.Outbound <- bus.OutboundMessage{
						Channel:  msg.Channel,
						ChatID:   controlChatID,
						Content:  pending,
						Metadata: meta,
					}
				}
			}
		}
		return
	}
	if g.logger != nil {
		g.logger.Warnf("[gateway] outbound_result dropped: request not found channel=%s chat=%s source=%s request_id=%s", msg.Channel, controlChatID, source, requestID)
	}
}

func pluginPlatformKey(pluginID, platform string) string {
	return strings.ToLower(strings.TrimSpace(pluginID)) + "|" + strings.ToLower(strings.TrimSpace(platform))
}

func (g *Gateway) markHostReady(pluginID, platform string) {
	g.stateMu.Lock()
	if g.hostReadyByKey == nil {
		g.hostReadyByKey = map[string]bool{}
	}
	g.hostReadyByKey[pluginPlatformKey(pluginID, platform)] = true
	g.stateMu.Unlock()
}

func (g *Gateway) isHostReady(pluginID, platform string) bool {
	g.stateMu.Lock()
	defer g.stateMu.Unlock()
	if g.hostReadyByKey == nil {
		return false
	}
	return g.hostReadyByKey[pluginPlatformKey(pluginID, platform)]
}

func (g *Gateway) setPluginToolCatalog(pluginID, platform string, tools []pluginToolDescriptor) {
	key := pluginPlatformKey(pluginID, platform)
	g.stateMu.Lock()
	if g.pluginToolsByKey == nil {
		g.pluginToolsByKey = map[string][]pluginToolDescriptor{}
	}
	cloned := make([]pluginToolDescriptor, 0, len(tools))
	cloned = append(cloned, tools...)
	g.pluginToolsByKey[key] = cloned
	g.stateMu.Unlock()
}

// collectAllPluginTools merges all cached plugin tool catalogs and returns them as CustomTools (deduped by name).
func (g *Gateway) collectAllPluginTools() []tool.Tool {
	g.stateMu.Lock()
	var out []tool.Tool
	for key, descs := range g.pluginToolsByKey {
		parts := strings.SplitN(key, "|", 2)
		pluginID := ""
		platform := ""
		if len(parts) > 0 {
			pluginID = strings.TrimSpace(parts[0])
		}
		if len(parts) > 1 {
			platform = strings.TrimSpace(parts[1])
		}
		out = append(out, pluginToolsFromDescriptors(descs, pluginID, platform, g.executePluginToolSync)...)
	}
	g.stateMu.Unlock()
	return out
}

// recreateRuntimeWithPluginTools builds a new runtime with built-in + all plugin CustomTools and replaces g.runtime.
func (g *Gateway) recreateRuntimeWithPluginTools() {
	customTools := g.collectAllPluginTools()
	if len(customTools) == 0 {
		return
	}
	newRt, err := defaultRuntimeFactoryWithPermissions(g.cfg, g.sysPrompt, g.realtimeCallback, g.handlePermissionRequest, customTools)
	if err != nil {
		if g.logger != nil {
			g.logger.Warnf("[gateway] recreate runtime with plugin tools failed: %v", err)
		}
		return
	}
	old := g.runtime
	g.runtime = newRt
	if old != nil {
		old.Close()
	}
	if g.logger != nil {
		g.logger.Infof("[gateway] runtime recreated with %d plugin CustomTools", len(customTools))
	}
}

func (g *Gateway) executePluginToolSync(ctx context.Context, pluginID, platform, toolName string, params map[string]interface{}) (*tool.ToolResult, error) {
	if g.channels == nil {
		return &tool.ToolResult{Success: false, Error: fmt.Errorf("interaction channel unavailable")}, nil
	}
	sessionChatID := strings.TrimSpace(g.currentChatID)
	if sessionChatID == "" || !strings.HasPrefix(sessionChatID, strings.TrimSpace(pluginID)+":"+strings.TrimSpace(platform)+":") {
		return &tool.ToolResult{Success: false, Error: fmt.Errorf("plugin tool requires interaction session context")}, nil
	}
	ack, err := g.channels.InvokeInteractionTool(ctx, sessionChatID, toolName, params)
	if err != nil {
		return &tool.ToolResult{Success: false, Error: err}, nil
	}
	if ok, _ := ack["success"].(bool); !ok {
		errText, _ := ack["error"].(string)
		if strings.TrimSpace(errText) == "" {
			errText = "plugin tool execution failed"
		}
		return &tool.ToolResult{Success: false, Error: fmt.Errorf("%s", errText)}, nil
	}
	output, _ := ack["output"].(string)
	if waitMeta, waiting := detectPluginAuthWait(output); waiting {
		return &tool.ToolResult{
			Success: true,
			Output:  output,
			Data:    waitMeta,
		}, nil
	}
	return &tool.ToolResult{Success: true, Output: output}, nil
}

func parsePluginToolCatalog(meta map[string]any) []pluginToolDescriptor {
	raw, ok := meta["tools"]
	if !ok || raw == nil {
		return nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]pluginToolDescriptor, 0, len(items))
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name := strings.TrimSpace(protocol.MetaString(entry, "name"))
		if name == "" {
			continue
		}
		desc := strings.TrimSpace(protocol.MetaString(entry, "description"))
		schema := map[string]any{}
		if v, ok := entry["input_schema"].(map[string]any); ok {
			for k, val := range v {
				schema[k] = val
			}
		}
		out = append(out, pluginToolDescriptor{
			Name:        name,
			Description: desc,
			InputSchema: schema,
		})
	}
	return out
}

func (g *Gateway) clearOutboundTurnState(channel, chatID string) {
	key := toolLogChatKey(channel, chatID)
	g.stateMu.Lock()
	delete(g.outboundState, key)
	g.stateMu.Unlock()
}

func (g *Gateway) outboundMessageID(channel, chatID string, kind outboundMessageKind) string {
	key := toolLogChatKey(channel, chatID)
	g.stateMu.Lock()
	defer g.stateMu.Unlock()
	state := g.outboundState[key]
	switch kind {
	case outboundMessageTool:
		return strings.TrimSpace(state.ToolMessageID)
	default:
		return strings.TrimSpace(state.PreviewMessageID)
	}
}

func (g *Gateway) setOutboundMessageID(channel, chatID string, kind outboundMessageKind, messageID string) {
	key := toolLogChatKey(channel, chatID)
	g.stateMu.Lock()
	state := g.outboundState[key]
	switch kind {
	case outboundMessageTool:
		state.ToolMessageID = strings.TrimSpace(messageID)
	default:
		state.PreviewMessageID = strings.TrimSpace(messageID)
	}
	g.outboundState[key] = state
	g.stateMu.Unlock()
}

func (g *Gateway) applyOutboundResultByRequest(channel, chatID, requestID, messageID string) (outboundMessageKind, bool) {
	key := toolLogChatKey(channel, chatID)
	g.stateMu.Lock()
	defer g.stateMu.Unlock()
	state := g.outboundState[key]
	kind, ok := state.PendingRequest[requestID]
	if !ok {
		return "", false
	}
	delete(state.PendingRequest, requestID)
	switch kind {
	case outboundMessageTool:
		state.ToolMessageID = strings.TrimSpace(messageID)
	default:
		state.PreviewMessageID = strings.TrimSpace(messageID)
	}
	g.outboundState[key] = state
	return kind, true
}

func (g *Gateway) attachOutboundRequest(channel, chatID string, kind outboundMessageKind, meta map[string]any) map[string]any {
	key := toolLogChatKey(channel, chatID)
	requestID := g.nextOutboundRequestID()
	g.stateMu.Lock()
	if g.outboundState == nil {
		g.outboundState = map[string]outboundMessageState{}
	}
	state := g.outboundState[key]
	if state.PendingRequest == nil {
		state.PendingRequest = map[string]outboundMessageKind{}
	}
	state.PendingRequest[requestID] = kind
	g.outboundState[key] = state
	g.stateMu.Unlock()
	if meta == nil {
		meta = map[string]any{}
	}
	meta[protocol.RequestIDKey] = requestID
	if strings.EqualFold(strings.TrimSpace(channel), "interaction") {
		meta[protocol.SessionChatIDKey] = strings.TrimSpace(chatID)
	}
	return meta
}

func (g *Gateway) nextOutboundRequestID() string {
	seq := atomic.AddUint64(&g.requestSeq, 1)
	return fmt.Sprintf("gw-%d", seq)
}

func (g *Gateway) markReactionSent(channel, chatID string) bool {
	key := toolLogChatKey(channel, chatID)
	g.stateMu.Lock()
	defer g.stateMu.Unlock()
	state := g.outboundState[key]
	if state.ReactionSent {
		return false
	}
	state.ReactionSent = true
	g.outboundState[key] = state
	return true
}

func (g *Gateway) beginRestartNotification(key string) bool {
	key = strings.TrimSpace(strings.ToLower(key))
	if key == "" {
		return false
	}
	g.stateMu.Lock()
	defer g.stateMu.Unlock()
	if g.restartNotifyByKey == nil {
		g.restartNotifyByKey = map[string]restartNotifyState{}
	}
	st := g.restartNotifyByKey[key]
	if st.done || st.inFlight {
		return false
	}
	st.inFlight = true
	g.restartNotifyByKey[key] = st
	return true
}

func (g *Gateway) endRestartNotification(key string, success bool) {
	key = strings.TrimSpace(strings.ToLower(key))
	if key == "" {
		return
	}
	g.stateMu.Lock()
	defer g.stateMu.Unlock()
	if g.restartNotifyByKey == nil {
		g.restartNotifyByKey = map[string]restartNotifyState{}
	}
	st := g.restartNotifyByKey[key]
	st.inFlight = false
	if success {
		st.done = true
	}
	g.restartNotifyByKey[key] = st
}

