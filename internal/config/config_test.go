package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg == nil {
		t.Fatal("DefaultConfig returned nil")
	}
	if cfg.Agent.Model.Primary != DefaultModel {
		t.Errorf("model.primary = %q, want %q", cfg.Agent.Model.Primary, DefaultModel)
	}
	if cfg.Agent.MaxTokens != DefaultMaxTokens {
		t.Errorf("maxTokens = %d, want %d", cfg.Agent.MaxTokens, DefaultMaxTokens)
	}
	if cfg.Agent.MaxToolIterations != DefaultMaxToolIterations {
		t.Errorf("maxToolIterations = %d, want %d", cfg.Agent.MaxToolIterations, DefaultMaxToolIterations)
	}
	if cfg.Gateway.Host != DefaultHost {
		t.Errorf("host = %q, want %q", cfg.Gateway.Host, DefaultHost)
	}
	if cfg.Gateway.Port != DefaultPort {
		t.Errorf("port = %d, want %d", cfg.Gateway.Port, DefaultPort)
	}
	if cfg.Tools.ExecTimeout != DefaultExecTimeout {
		t.Errorf("execTimeout = %d, want %d", cfg.Tools.ExecTimeout, DefaultExecTimeout)
	}
	if !cfg.Tools.RestrictToWorkspace {
		t.Error("restrictToWorkspace should be true by default")
	}
	if cfg.Agent.Workspace == "" {
		t.Error("workspace should not be empty")
	}
	if !cfg.Agent.Guard.InputEnabled {
		t.Error("guard.inputEnabled should be true by default")
	}
	if !cfg.Agent.Guard.OutputEnabled {
		t.Error("guard.outputEnabled should be true by default")
	}
	if !cfg.Agent.TokenTracking.Enabled {
		t.Error("tokenTracking.enabled should be true by default")
	}
}

func TestLoadConfig_NoFile(t *testing.T) {
	// Override config dir to a temp location
	tmpDir := t.TempDir()
	origHome := os.Getenv("HOME")
	t.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)

	// Clear any env overrides
	t.Setenv("AEVITAS_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	if cfg.Agent.Model.Primary != DefaultModel {
		t.Errorf("expected default model %q, got %q", DefaultModel, cfg.Agent.Model.Primary)
	}
}

func TestLoadConfig_FromFile(t *testing.T) {
	tmpDir := t.TempDir()
	origHome := os.Getenv("HOME")
	t.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)

	// Clear env overrides
	t.Setenv("AEVITAS_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")

	// Create config file
	cfgDir := filepath.Join(tmpDir, ".aevitas")
	os.MkdirAll(cfgDir, 0755)

	testCfg := map[string]any{
		"agent": map[string]any{
			"model":     "claude-opus-4-20250514",
			"maxTokens": 4096,
			"guard": map[string]any{
				"inputEnabled":  false,
				"outputEnabled": true,
			},
			"tokenTracking": map[string]any{
				"enabled": false,
			},
		},
		"provider": map[string]any{
			"apiKey": "sk-test-key",
		},
	}
	data, _ := json.MarshalIndent(testCfg, "", "  ")
	os.WriteFile(filepath.Join(cfgDir, "config.json"), data, 0644)

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	if cfg.Agent.Model.Primary != "claude-opus-4-20250514" {
		t.Errorf("model.primary = %q, want claude-opus-4-20250514", cfg.Agent.Model.Primary)
	}
	if cfg.Agent.MaxTokens != 4096 {
		t.Errorf("maxTokens = %d, want 4096", cfg.Agent.MaxTokens)
	}
	if cfg.Provider.APIKey != "sk-test-key" {
		t.Errorf("apiKey = %q, want sk-test-key", cfg.Provider.APIKey)
	}
	if cfg.Agent.Guard.InputEnabled {
		t.Error("guard.inputEnabled = true, want false")
	}
	if !cfg.Agent.Guard.OutputEnabled {
		t.Error("guard.outputEnabled = false, want true")
	}
	if cfg.Agent.TokenTracking.Enabled {
		t.Error("tokenTracking.enabled = true, want false")
	}
}

func TestLoadConfig_ModelObjectWithFallbacks(t *testing.T) {
	tmpDir := t.TempDir()
	origHome := os.Getenv("HOME")
	t.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)

	cfgDir := filepath.Join(tmpDir, ".aevitas")
	os.MkdirAll(cfgDir, 0755)

	testCfg := map[string]any{
		"agent": map[string]any{
			"model": map[string]any{
				"primary":   "anthropic/claude-opus-4.6",
				"fallbacks": []string{"anthropic/claude-sonnet-4.5", "openai/gpt-4o"},
			},
		},
	}
	data, _ := json.MarshalIndent(testCfg, "", "  ")
	os.WriteFile(filepath.Join(cfgDir, "config.json"), data, 0644)

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	if cfg.Agent.Model.Primary != "anthropic/claude-opus-4.6" {
		t.Fatalf("model.primary = %q", cfg.Agent.Model.Primary)
	}
	if len(cfg.Agent.Model.Fallbacks) != 2 {
		t.Fatalf("fallback count = %d, want 2", len(cfg.Agent.Model.Fallbacks))
	}
}

func TestLoadConfig_EnvOverrides(t *testing.T) {
	tmpDir := t.TempDir()
	origHome := os.Getenv("HOME")
	t.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)

	tests := []struct {
		name    string
		envKey  string
		envVal  string
		wantKey string
	}{
		{"AEVITAS_API_KEY", "AEVITAS_API_KEY", "aevitas-key", "aevitas-key"},
		{"ANTHROPIC_API_KEY", "ANTHROPIC_API_KEY", "anthropic-key", "anthropic-key"},
		{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_AUTH_TOKEN", "auth-token", "auth-token"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("AEVITAS_API_KEY", "")
			t.Setenv("ANTHROPIC_API_KEY", "")
			t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
			t.Setenv(tt.envKey, tt.envVal)

			cfg, err := LoadConfig()
			if err != nil {
				t.Fatalf("LoadConfig error: %v", err)
			}
			if cfg.Provider.APIKey != tt.wantKey {
				t.Errorf("apiKey = %q, want %q", cfg.Provider.APIKey, tt.wantKey)
			}
		})
	}
}

func TestLoadConfig_EnvPriority(t *testing.T) {
	tmpDir := t.TempDir()
	origHome := os.Getenv("HOME")
	t.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)

	// AEVITAS_API_KEY takes priority over ANTHROPIC_API_KEY
	t.Setenv("AEVITAS_API_KEY", "aevitas-wins")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-loses")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "token-loses")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	if cfg.Provider.APIKey != "aevitas-wins" {
		t.Errorf("apiKey = %q, want aevitas-wins", cfg.Provider.APIKey)
	}
}

func TestLoadConfig_BaseURLEnv(t *testing.T) {
	tmpDir := t.TempDir()
	origHome := os.Getenv("HOME")
	t.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)

	t.Setenv("AEVITAS_API_KEY", "key")
	t.Setenv("ANTHROPIC_BASE_URL", "http://localhost:8080")
	t.Setenv("AEVITAS_BASE_URL", "")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	if cfg.Provider.BaseURL != "http://localhost:8080" {
		t.Errorf("baseURL = %q, want http://localhost:8080", cfg.Provider.BaseURL)
	}
}

func TestSaveConfig(t *testing.T) {
	tmpDir := t.TempDir()
	origHome := os.Getenv("HOME")
	t.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)

	cfg := DefaultConfig()
	cfg.Provider.APIKey = "test-key"

	if err := SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(tmpDir, ".aevitas", "config.json"))
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}

	var loaded Config
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("unmarshal saved config: %v", err)
	}
	if loaded.Provider.APIKey != "test-key" {
		t.Errorf("saved apiKey = %q, want test-key", loaded.Provider.APIKey)
	}
}

func TestLoadConfig_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	origHome := os.Getenv("HOME")
	t.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)

	cfgDir := filepath.Join(tmpDir, ".aevitas")
	os.MkdirAll(cfgDir, 0755)
	os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte("invalid json"), 0644)

	_, err := LoadConfig()
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestLoadConfig_EmptyWorkspace(t *testing.T) {
	tmpDir := t.TempDir()
	origHome := os.Getenv("HOME")
	t.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)

	cfgDir := filepath.Join(tmpDir, ".aevitas")
	os.MkdirAll(cfgDir, 0755)

	// Config with empty workspace - should use default
	testCfg := map[string]any{
		"agent": map[string]any{
			"workspace": "",
		},
	}
	data, _ := json.MarshalIndent(testCfg, "", "  ")
	os.WriteFile(filepath.Join(cfgDir, "config.json"), data, 0644)

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	if cfg.Agent.Workspace == "" {
		t.Error("workspace should not be empty")
	}
}

func TestLoadConfig_TelegramToken(t *testing.T) {
	tmpDir := t.TempDir()
	origHome := os.Getenv("HOME")
	t.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)

	t.Setenv("AEVITAS_TELEGRAM_TOKEN", "test-telegram-token")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	if cfg.Channels.Telegram.Token != "test-telegram-token" {
		t.Errorf("telegram token = %q, want test-telegram-token", cfg.Channels.Telegram.Token)
	}
}

func TestLoadConfig_AEVITASBaseURL(t *testing.T) {
	tmpDir := t.TempDir()
	origHome := os.Getenv("HOME")
	t.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)

	t.Setenv("AEVITAS_BASE_URL", "http://aevitas.local")
	t.Setenv("ANTHROPIC_BASE_URL", "http://anthropic.local")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	// AEVITAS_BASE_URL takes priority
	if cfg.Provider.BaseURL != "http://aevitas.local" {
		t.Errorf("baseURL = %q, want http://aevitas.local", cfg.Provider.BaseURL)
	}
}

func TestLoadConfig_WeComEnvOverrides(t *testing.T) {
	tmpDir := t.TempDir()
	origHome := os.Getenv("HOME")
	t.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)

	t.Setenv("AEVITAS_WECOM_TOKEN", "wecom-token")
	t.Setenv("AEVITAS_WECOM_ENCODING_AES_KEY", "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG")
	t.Setenv("AEVITAS_WECOM_RECEIVE_ID", "wecom-receive-id")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}

	if cfg.Channels.WeCom.Token != "wecom-token" {
		t.Errorf("wecom token = %q, want wecom-token", cfg.Channels.WeCom.Token)
	}
	if cfg.Channels.WeCom.EncodingAESKey != "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG" {
		t.Errorf("wecom aes key = %q, want configured value", cfg.Channels.WeCom.EncodingAESKey)
	}
	if cfg.Channels.WeCom.ReceiveID != "wecom-receive-id" {
		t.Errorf("wecom receiveId = %q, want wecom-receive-id", cfg.Channels.WeCom.ReceiveID)
	}
}

func TestEnabledChannels_Explicit(t *testing.T) {
	cfg := &Config{
		Channels: ChannelsConfig{
			Telegram:    TelegramConfig{Enabled: true},
			WeCom:       WeComConfig{Enabled: false},
			Interaction: InteractionConfig{Enabled: true},
		},
	}

	channels := cfg.EnabledChannels()
	if channels["telegram"] != true || channels["interaction"] != true || channels["wecom"] != false {
		t.Fatalf("unexpected channels status: %#v", channels)
	}
}

func TestPluginPaths_DefaultAndOverride(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.PluginHomeDir() == "" || cfg.PluginRegistryPath() == "" {
		t.Fatal("plugin paths should not be empty")
	}
	cfg.Plugins.Home = "/tmp/aevitas-plugins"
	cfg.Plugins.Registry = "/tmp/aevitas-plugins/custom-registry.json"
	if cfg.PluginHomeDir() != "/tmp/aevitas-plugins" {
		t.Fatalf("PluginHomeDir = %q", cfg.PluginHomeDir())
	}
	if cfg.PluginRegistryPath() != "/tmp/aevitas-plugins/custom-registry.json" {
		t.Fatalf("PluginRegistryPath = %q", cfg.PluginRegistryPath())
	}
}
