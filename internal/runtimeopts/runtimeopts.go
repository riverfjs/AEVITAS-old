package runtimeopts

import (
	"github.com/riverfjs/aevitas/internal/config"
	"github.com/riverfjs/agentsdk-go/pkg/api"
	sdklogger "github.com/riverfjs/agentsdk-go/pkg/logger"
	"github.com/riverfjs/agentsdk-go/pkg/model"
	"github.com/riverfjs/agentsdk-go/pkg/tool"
)

func NewProvider(cfg *config.Config) api.ModelFactory {
	switch cfg.Provider.Type {
	case "openai":
		return &model.OpenAIProvider{
			APIKey:    cfg.Provider.APIKey,
			BaseURL:   cfg.Provider.BaseURL,
			ModelName: cfg.Agent.Model.Primary,
			MaxTokens: cfg.Agent.MaxTokens,
		}
	default: // "anthropic" or empty
		return &model.AnthropicProvider{
			APIKey:    cfg.Provider.APIKey,
			BaseURL:   cfg.Provider.BaseURL,
			ModelName: cfg.Agent.Model.Primary,
			MaxTokens: cfg.Agent.MaxTokens,
		}
	}
}

func BuildAPIOptions(cfg *config.Config, provider api.ModelFactory, sysPrompt string, log sdklogger.Logger, realtimeCallback func(api.RealtimeEvent), permissionHandler api.PermissionRequestHandler, customTools []tool.Tool) api.Options {
	inputGuardEnabled := cfg.Agent.Guard.InputEnabled
	outputGuardEnabled := cfg.Agent.Guard.OutputEnabled
	var promptGuardFactory api.ModelFactory
	if inputGuardEnabled {
		promptGuardFactory = provider
	}

	return api.Options{
		ProjectRoot:             cfg.Agent.Workspace,
		ModelFactory:            provider,
		PromptGuardModelFactory: promptGuardFactory,
		PromptGuardEnabled:      boolPtr(inputGuardEnabled),
		OutputGuardEnabled:      boolPtr(outputGuardEnabled),
		SystemPrompt:            sysPrompt,
		MaxIterations:           cfg.Agent.MaxToolIterations,
		Logger:                  log,
		HistoryLimit:            cfg.Agent.HistoryLimit,
		TokenTracking:           cfg.Agent.TokenTracking.Enabled,
		RealtimeEventCallback:   realtimeCallback,
		PermissionRequestHandler: permissionHandler,
		ProgressInterval:        cfg.Agent.ToolLog.Interval,
		AutoRecall:              cfg.Agent.AutoRecall,
		AutoRecallMaxResults:    cfg.Agent.AutoRecallMaxResults,
		AutoCompact: api.CompactConfig{
			Enabled:   cfg.Agent.Compaction.Enabled,
			Threshold: cfg.Agent.Compaction.Threshold,
		},
		Voice: api.VoiceConfig{
			Enabled: cfg.Voice.Enabled,
			ASR: api.VoiceASRConfig{
				Enabled:           cfg.Voice.ASR.Enabled,
				Provider:          cfg.Voice.ASR.Provider,
				APIKey:            cfg.Voice.ASR.APIKey,
				BaseURL:           cfg.Voice.ASR.BaseURL,
				SpeechModels:      cfg.Voice.ASR.SpeechModels,
				LanguageDetection: cfg.Voice.ASR.LanguageDetection,
				PollIntervalSec:   cfg.Voice.ASR.PollIntervalSec,
				TimeoutSec:        cfg.Voice.ASR.TimeoutSec,
			},
			TTS: api.VoiceTTSConfig{
				Enabled:    cfg.Voice.TTS.Enabled,
				Provider:   cfg.Voice.TTS.Provider,
				Voice:      cfg.Voice.TTS.Voice,
				Rate:       cfg.Voice.TTS.Rate,
				Volume:     cfg.Voice.TTS.Volume,
				Pitch:      cfg.Voice.TTS.Pitch,
				OutputDir:  cfg.Voice.TTS.OutputDir,
				TimeoutSec: cfg.Voice.TTS.TimeoutSec,
			},
		},
		PrimaryModelName:          cfg.Agent.Model.Primary,
		PrimaryFallbackModels:      cfg.Agent.Model.Fallbacks,
		ContextWindowTokens:        cfg.Agent.ContextWindow.Tokens,
		ContextWindowWarnRatio:     cfg.Agent.ContextWindow.WarnRatio,
		ContextWindowHardMinTokens: cfg.Agent.ContextWindow.HardMinTokens,
		CustomTools:                customTools,
		MemoryFlush: api.MemoryFlushConfig{
			Enabled:             cfg.Agent.MemoryFlush.Enabled,
			ReserveTokensFloor:  cfg.Agent.MemoryFlush.ReserveTokensFloor,
			SoftThresholdTokens: cfg.Agent.MemoryFlush.SoftThresholdTokens,
			Prompt:              cfg.Agent.MemoryFlush.Prompt,
		},
	}
}

func boolPtr(v bool) *bool {
	return &v
}
