package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	DefaultModel             = "claude-sonnet-4-5-20250929"
	DefaultMaxTokens         = 8192
	DefaultTemperature       = 0.7
	DefaultMaxToolIterations = 20
	DefaultExecTimeout       = 60
	DefaultHost              = "0.0.0.0"
	DefaultPort              = 18790
	DefaultBufSize           = 100
)

type Config struct {
	Agent    AgentConfig    `json:"agent"`
	Channels ChannelsConfig `json:"channels"`
	Plugins  PluginsConfig  `json:"plugins,omitempty"`
	Provider ProviderConfig `json:"provider"`
	Voice    VoiceConfig    `json:"voice,omitempty"`
	Tools    ToolsConfig    `json:"tools"`
	Gateway  GatewayConfig  `json:"gateway"`
}

type AgentConfig struct {
	Workspace         string      `json:"workspace"`
	Model             ModelConfig `json:"model"`
	MaxTokens         int         `json:"maxTokens"`
	Temperature       float64     `json:"temperature"`
	MaxToolIterations int         `json:"maxToolIterations"`
	// HistoryLimit caps the number of user turns loaded from disk into each
	// session context. 0 = no limit (all history). Default: 30.
	HistoryLimit int `json:"historyLimit,omitempty"`
	// ToolLog controls real-time progress log messages sent during agent execution.
	ToolLog ToolLogConfig `json:"toolLog,omitempty"`
	// AutoRecall enables automatic memory injection before each agent turn.
	// When true, MEMORY.md is searched with the user's prompt and top results
	// are prepended to the message as context. Default: true.
	AutoRecall           bool `json:"autoRecall,omitempty"`
	AutoRecallMaxResults int  `json:"autoRecallMaxResults,omitempty"`
	// Compaction controls automatic context-window compaction.
	Compaction CompactionConfig `json:"compaction,omitempty"`
	// ContextWindow configures the Context Window Guard.
	// When Tokens > 0, the SDK warns when the estimated history token count
	// approaches the limit and rejects requests when too full.
	ContextWindow ContextWindowConfig `json:"contextWindow,omitempty"`
	// MemoryFlush controls automatic pre-compaction memory flush.
	// When enabled, a hidden agent turn runs to write memories to disk when
	// input tokens approach the context window limit.
	MemoryFlush MemoryFlushConfig `json:"memoryFlush,omitempty"`
	// TokenTracking controls SDK token usage aggregation.
	TokenTracking TokenTrackingConfig `json:"tokenTracking,omitempty"`
	// Guard controls prompt/output disclosure protections in agentsdk-go.
	Guard GuardConfig `json:"guard,omitempty"`
}

// ModelConfig supports legacy string model and structured model config:
// - "anthropic/claude-opus-4.6"
// - {"primary":"anthropic/claude-opus-4.6","fallbacks":["anthropic/claude-sonnet-4.5"]}
// It also accepts single `fallback` for compatibility.
type ModelConfig struct {
	Primary   string   `json:"primary,omitempty"`
	Fallbacks []string `json:"fallbacks,omitempty"`
}

func (m ModelConfig) MarshalJSON() ([]byte, error) {
	primary := m.Primary
	if len(m.Fallbacks) == 0 {
		return json.Marshal(primary)
	}
	type modelAlias struct {
		Primary   string   `json:"primary"`
		Fallbacks []string `json:"fallbacks,omitempty"`
	}
	return json.Marshal(modelAlias{
		Primary:   primary,
		Fallbacks: m.Fallbacks,
	})
}

func (m *ModelConfig) UnmarshalJSON(data []byte) error {
	var rawString string
	if err := json.Unmarshal(data, &rawString); err == nil {
		m.Primary = rawString
		m.Fallbacks = nil
		return nil
	}
	var rawObj struct {
		Primary   string   `json:"primary"`
		Fallback  string   `json:"fallback"`
		Fallbacks []string `json:"fallbacks"`
	}
	if err := json.Unmarshal(data, &rawObj); err != nil {
		return err
	}
	m.Primary = rawObj.Primary
	seen := make(map[string]struct{})
	candidates := make([]string, 0, len(rawObj.Fallbacks)+1)
	if rawObj.Fallback != "" {
		candidates = append(candidates, rawObj.Fallback)
	}
	candidates = append(candidates, rawObj.Fallbacks...)
	m.Fallbacks = m.Fallbacks[:0]
	for _, candidate := range candidates {
		trimmed := candidate
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		m.Fallbacks = append(m.Fallbacks, trimmed)
	}
	return nil
}

type GuardConfig struct {
	// InputEnabled toggles prompt exfiltration guard before model generation.
	// Default: true.
	InputEnabled bool `json:"inputEnabled"`
	// OutputEnabled toggles output leak redaction after model generation.
	// Default: true.
	OutputEnabled bool `json:"outputEnabled"`
}

type TokenTrackingConfig struct {
	// Enabled toggles token usage aggregation for session/total stats.
	// Default: true.
	Enabled bool `json:"enabled"`
}

// ToolLogConfig controls periodic tool-call progress messages sent to the chat.
type ToolLogConfig struct {
	Enabled  bool `json:"enabled"`            // Whether to send progress messages (default: false)
	Interval int  `json:"interval,omitempty"` // Send a message every N tool calls (default: 5)
}

// ContextWindowConfig configures the lightweight Context Window Guard.
type ContextWindowConfig struct {
	// Tokens is the total context window size in tokens. 0 = disabled.
	// Set this to your model's context window (e.g. 200000 for Claude 3.5 Sonnet).
	Tokens int `json:"tokens,omitempty"`
	// WarnRatio is the usage fraction (0–1) that triggers a warning message.
	// Default: 0.8 (warn when estimated usage exceeds 80% of Tokens).
	WarnRatio float64 `json:"warnRatio,omitempty"`
	// HardMinTokens is the minimum estimated remaining tokens below which the
	// request is rejected with a message advising /reset. Default: 2000.
	HardMinTokens int `json:"hardMinTokens,omitempty"`
}

// MemoryFlushConfig controls automatic memory flush before context window exhaustion.
// Flush fires when inputTokens >= ContextWindow.Tokens - ReserveTokensFloor - SoftThresholdTokens.
type MemoryFlushConfig struct {
	// Enabled turns memory flush on or off. Default: true.
	Enabled bool `json:"enabled"`
	// ReserveTokensFloor is tokens reserved for model output during the flush turn.
	// Default: 20000 (same as openclaw). Separate from ContextWindow.HardMinTokens.
	ReserveTokensFloor int `json:"reserveTokensFloor,omitempty"`
	// SoftThresholdTokens is the additional early-trigger buffer. Default: 4000 (same as openclaw).
	// With 200k context and defaults, flush triggers at 176k tokens (88% usage).
	SoftThresholdTokens int `json:"softThresholdTokens,omitempty"`
	// Prompt is the synthetic user message for the flush turn. Uses default if empty.
	Prompt string `json:"prompt,omitempty"`
}

// CompactionConfig controls automatic session history compaction.
// When enabled, the SDK monitors token usage and summarises older messages
// via LLM when usage exceeds Threshold * context-window, keeping the
// conversation going indefinitely without hitting the model's context limit.
type CompactionConfig struct {
	// Enabled turns on automatic compaction. Default: false.
	Enabled bool `json:"enabled"`
	// Threshold is the fraction of the context window (0–1) at which compaction
	// triggers. Default: 0.8 (80%).
	Threshold float64 `json:"threshold,omitempty"`
}

type ProviderConfig struct {
	Type    string `json:"type,omitempty"` // "anthropic" (default) or "openai"
	APIKey  string `json:"apiKey"`
	BaseURL string `json:"baseUrl,omitempty"`
}

type VoiceConfig struct {
	Enabled bool           `json:"enabled"`
	ASR     VoiceASRConfig `json:"asr,omitempty"`
	TTS     VoiceTTSConfig `json:"tts,omitempty"`
}

type VoiceASRConfig struct {
	Enabled           bool     `json:"enabled"`
	Provider          string   `json:"provider,omitempty"` // "assemblyai"
	APIKey            string   `json:"apiKey,omitempty"`
	BaseURL           string   `json:"baseUrl,omitempty"`
	SpeechModels      []string `json:"speechModels,omitempty"`
	LanguageDetection bool     `json:"languageDetection,omitempty"`
	PollIntervalSec   int      `json:"pollIntervalSec,omitempty"`
	TimeoutSec        int      `json:"timeoutSec,omitempty"`
}

type VoiceTTSConfig struct {
	Enabled    bool   `json:"enabled"`
	Provider   string `json:"provider,omitempty"` // "edge"
	Voice      string `json:"voice,omitempty"`
	Rate       string `json:"rate,omitempty"`
	Volume     string `json:"volume,omitempty"`
	Pitch      string `json:"pitch,omitempty"`
	OutputDir  string `json:"outputDir,omitempty"`
	TimeoutSec int    `json:"timeoutSec,omitempty"`
}

type ChannelsConfig struct {
	Telegram    TelegramConfig    `json:"telegram"`
	WeCom       WeComConfig       `json:"wecom"`
	Interaction InteractionConfig `json:"interaction"`
	Plugin      map[string]any    `json:"-"`
}

type PluginsConfig struct {
	Home     string                  `json:"home,omitempty"`
	Registry string                  `json:"registry,omitempty"`
	Entries  map[string]PluginConfig `json:"-"`
}

type PluginConfig struct {
	Enabled bool               `json:"enabled"`
	Source  PluginSourceConfig `json:"source,omitempty"`
}

func (c *ChannelsConfig) UnmarshalJSON(data []byte) error {
	type alias struct {
		Telegram    TelegramConfig    `json:"telegram"`
		WeCom       WeComConfig       `json:"wecom"`
		Interaction InteractionConfig `json:"interaction"`
	}
	var known alias
	if err := json.Unmarshal(data, &known); err != nil {
		return err
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	delete(raw, "telegram")
	delete(raw, "wecom")
	delete(raw, "interaction")
	c.Telegram = known.Telegram
	c.WeCom = known.WeCom
	c.Interaction = known.Interaction
	c.Plugin = raw
	return nil
}

func (c ChannelsConfig) MarshalJSON() ([]byte, error) {
	out := map[string]any{
		"telegram":    c.Telegram,
		"wecom":       c.WeCom,
		"interaction": c.Interaction,
	}
	for k, v := range c.Plugin {
		trimmed := strings.TrimSpace(k)
		if trimmed == "" {
			continue
		}
		out[trimmed] = v
	}
	return json.Marshal(out)
}

func (p *PluginsConfig) UnmarshalJSON(data []byte) error {
	type alias struct {
		Home     string `json:"home,omitempty"`
		Registry string `json:"registry,omitempty"`
	}
	var known alias
	if err := json.Unmarshal(data, &known); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	delete(raw, "home")
	delete(raw, "registry")
	entries := map[string]PluginConfig{}
	for k, v := range raw {
		trimmed := strings.TrimSpace(k)
		if trimmed == "" {
			continue
		}
		var cfg PluginConfig
		if err := json.Unmarshal(v, &cfg); err != nil {
			return fmt.Errorf("parse plugins.%s: %w", trimmed, err)
		}
		entries[trimmed] = cfg
	}
	p.Home = known.Home
	p.Registry = known.Registry
	p.Entries = entries
	return nil
}

func (p PluginsConfig) MarshalJSON() ([]byte, error) {
	out := map[string]any{}
	if strings.TrimSpace(p.Home) != "" {
		out["home"] = p.Home
	}
	if strings.TrimSpace(p.Registry) != "" {
		out["registry"] = p.Registry
	}
	for k, v := range p.Entries {
		trimmed := strings.TrimSpace(k)
		if trimmed == "" {
			continue
		}
		out[trimmed] = v
	}
	return json.Marshal(out)
}

func (p PluginsConfig) Entry(pluginID string) (PluginConfig, bool) {
	if p.Entries == nil {
		return PluginConfig{}, false
	}
	cfg, ok := p.Entries[strings.TrimSpace(pluginID)]
	return cfg, ok
}

type PluginSourceConfig struct {
	NpmSpec string `json:"npmSpec,omitempty"`
	Tarball string `json:"tarball,omitempty"`
}

type TelegramConfig struct {
	Enabled   bool     `json:"enabled"`
	Token     string   `json:"token"`
	AllowFrom []string `json:"allowFrom"`
	Proxy     string   `json:"proxy,omitempty"`
}

type WeComConfig struct {
	Enabled        bool     `json:"enabled"`
	Token          string   `json:"token"`
	EncodingAESKey string   `json:"encodingAESKey"`
	ReceiveID      string   `json:"receiveId,omitempty"`
	Port           int      `json:"port,omitempty"`
	AllowFrom      []string `json:"allowFrom"`
}

type InteractionConfig struct {
	Enabled           bool     `json:"enabled"`
	ListenAddr        string   `json:"listenAddr,omitempty"`
	InboundPath       string   `json:"inboundPath,omitempty"`
	OutboundURL       string   `json:"outboundUrl,omitempty"`
	AuthToken         string   `json:"authToken,omitempty"`
	RequestTimeoutSec int      `json:"requestTimeoutSec,omitempty"`
	AllowFrom         []string `json:"allowFrom"`
}

type ToolsConfig struct {
	BraveAPIKey         string `json:"braveApiKey,omitempty"`
	ExecTimeout         int    `json:"execTimeout"`
	RestrictToWorkspace bool   `json:"restrictToWorkspace"`
}

type GatewayConfig struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

func DefaultConfig() *Config {
	home, _ := os.UserHomeDir()
	return &Config{
		Agent: AgentConfig{
			Workspace:         filepath.Join(home, ".aevitas", "workspace"),
			Model:             ModelConfig{Primary: DefaultModel},
			MaxTokens:         DefaultMaxTokens,
			Temperature:       DefaultTemperature,
			MaxToolIterations: DefaultMaxToolIterations,
			HistoryLimit:      30,
			ToolLog:           ToolLogConfig{Enabled: false, Interval: 5},
			AutoRecall:        true,
			MemoryFlush:       MemoryFlushConfig{Enabled: true, ReserveTokensFloor: 20000, SoftThresholdTokens: 4000},
			TokenTracking:     TokenTrackingConfig{Enabled: true},
			Guard:             GuardConfig{InputEnabled: true, OutputEnabled: true},
		},
		Provider: ProviderConfig{},
		Voice: VoiceConfig{
			Enabled: false,
			ASR: VoiceASRConfig{
				Enabled:           false,
				Provider:          "assemblyai",
				BaseURL:           "https://api.assemblyai.com",
				SpeechModels:      []string{"universal-3-pro", "universal-2"},
				LanguageDetection: true,
				PollIntervalSec:   3,
				TimeoutSec:        120,
			},
			TTS: VoiceTTSConfig{
				Enabled:    false,
				Provider:   "edge",
				Voice:      "zh-CN-XiaoxiaoMultilingualNeural",
				Rate:       "+0%",
				Volume:     "+0%",
				Pitch:      "+0Hz",
				TimeoutSec: 60,
			},
		},
		Channels: ChannelsConfig{},
		Plugins: PluginsConfig{},
		Tools: ToolsConfig{
			ExecTimeout:         DefaultExecTimeout,
			RestrictToWorkspace: true,
		},
		Gateway: GatewayConfig{
			Host: DefaultHost,
			Port: DefaultPort,
		},
	}
}

func ConfigDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".aevitas")
}

func ConfigPath() string {
	return filepath.Join(ConfigDir(), "config.json")
}

func LoadConfig() (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(ConfigPath())
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read config: %w", err)
		}
	} else {
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse config: %w", err)
		}
	}

	// Environment variable overrides
	if key := os.Getenv("AEVITAS_API_KEY"); key != "" {
		cfg.Provider.APIKey = key
	}
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" && cfg.Provider.APIKey == "" {
		cfg.Provider.APIKey = key
	}
	if key := os.Getenv("ANTHROPIC_AUTH_TOKEN"); key != "" && cfg.Provider.APIKey == "" {
		cfg.Provider.APIKey = key
	}
	if key := os.Getenv("OPENAI_API_KEY"); key != "" && cfg.Provider.APIKey == "" {
		cfg.Provider.APIKey = key
		if cfg.Provider.Type == "" {
			cfg.Provider.Type = "openai"
		}
	}
	if url := os.Getenv("AEVITAS_BASE_URL"); url != "" {
		cfg.Provider.BaseURL = url
	}
	if url := os.Getenv("ANTHROPIC_BASE_URL"); url != "" && cfg.Provider.BaseURL == "" {
		cfg.Provider.BaseURL = url
	}
	if token := os.Getenv("AEVITAS_TELEGRAM_TOKEN"); token != "" {
		cfg.Channels.Telegram.Token = token
	}
	if token := os.Getenv("AEVITAS_WECOM_TOKEN"); token != "" {
		cfg.Channels.WeCom.Token = token
	}
	if aesKey := os.Getenv("AEVITAS_WECOM_ENCODING_AES_KEY"); aesKey != "" {
		cfg.Channels.WeCom.EncodingAESKey = aesKey
	}
	if receiveID := os.Getenv("AEVITAS_WECOM_RECEIVE_ID"); receiveID != "" {
		cfg.Channels.WeCom.ReceiveID = receiveID
	}
	if key := os.Getenv("AEVITAS_VOICE_ASR_API_KEY"); key != "" {
		cfg.Voice.ASR.APIKey = key
	}
	if url := os.Getenv("AEVITAS_VOICE_ASR_BASE_URL"); url != "" {
		cfg.Voice.ASR.BaseURL = url
	}
	if dir := os.Getenv("AEVITAS_VOICE_TTS_OUTPUT_DIR"); dir != "" {
		cfg.Voice.TTS.OutputDir = dir
	}

	if cfg.Agent.Workspace == "" {
		cfg.Agent.Workspace = DefaultConfig().Agent.Workspace
	}
	if cfg.Agent.Model.Primary == "" {
		cfg.Agent.Model.Primary = DefaultConfig().Agent.Model.Primary
	}

	return cfg, nil
}

func SaveConfig(cfg *Config) error {
	dir := ConfigDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	return os.WriteFile(ConfigPath(), data, 0644)
}

func (c *Config) PluginHomeDir() string {
	if c != nil {
		if v := strings.TrimSpace(c.Plugins.Home); v != "" {
			return v
		}
	}
	return filepath.Join(ConfigDir(), "plugins")
}

func (c *Config) PluginRegistryPath() string {
	if c != nil {
		if v := strings.TrimSpace(c.Plugins.Registry); v != "" {
			return v
		}
	}
	return filepath.Join(c.PluginHomeDir(), "registry.json")
}

func (c *Config) EnabledChannels() map[string]bool {
	out := map[string]bool{
		"telegram":    c != nil && c.Channels.Telegram.Enabled,
		"wecom":       c != nil && c.Channels.WeCom.Enabled,
		"interaction": c != nil && c.Channels.Interaction.Enabled,
	}
	return out
}

func SortedStatusKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
