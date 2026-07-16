package config_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestPluginInstanceConfigConstruction(t *testing.T) {
	instance, errNew := config.NewPluginInstanceConfig(true, 3, map[string]any{"endpoint": "https://plugin.example/v1"})
	if errNew != nil {
		t.Fatalf("NewPluginInstanceConfig() error = %v", errNew)
	}
	cfg := config.Config{Plugins: config.PluginsConfig{Configs: map[string]config.PluginInstanceConfig{"example": instance}}}
	got := cfg.Plugins.Configs["example"]
	if got.Enabled == nil || !*got.Enabled || got.Priority != 3 {
		t.Fatalf("host fields = (%v, %d), want (true, 3)", got.Enabled, got.Priority)
	}
}

func TestRuntimeConfigConstruction(t *testing.T) {
	cfg := config.Config{
		SDKConfig: config.SDKConfig{
			DisableImageGeneration: config.DisableImageGenerationPassthrough,
		},
		Routing: config.RoutingConfig{
			Strategy:           "round-robin",
			SessionAffinity:    true,
			SessionAffinityTTL: "1h",
		},
		QuotaExceeded: config.QuotaExceeded{
			SwitchProject:      true,
			SwitchPreviewModel: true,
			AntigravityCredits: true,
		},
		ClaudeHeaderDefaults: config.ClaudeHeaderDefaults{
			UserAgent: "Claude Code",
		},
		CodexHeaderDefaults: config.CodexHeaderDefaults{
			UserAgent: "Codex",
		},
		Codex: config.CodexConfig{
			IdentityConfuse: true,
		},
		GeminiKey: []config.GeminiKey{{
			APIKey: "gemini-key",
			Models: []config.GeminiModel{{
				Name:  "gemini-2.5-pro",
				Alias: "gemini-pro",
			}},
		}},
		ClaudeKey: []config.ClaudeKey{{
			APIKey: "claude-key",
			Cloak: &config.CloakConfig{
				Mode: "never",
			},
			Models: []config.ClaudeModel{{
				Name:  "claude-sonnet-4",
				Alias: "claude-sonnet",
			}},
		}},
		CodexKey: []config.CodexKey{{
			APIKey:  "codex-key",
			BaseURL: "https://chatgpt.com/backend-api/codex",
			Models: []config.CodexModel{{
				Name:  "gpt-5-codex",
				Alias: "codex",
			}},
		}},
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:    "compatible",
			BaseURL: "https://example.com/v1",
			Models: []config.OpenAICompatibilityModel{{
				Name:  "reasoning-model",
				Alias: "reasoning",
				Thinking: &config.ThinkingSupport{
					Levels: []string{"low", "high"},
				},
			}},
		}},
	}

	if cfg.DisableImageGeneration != config.DisableImageGenerationPassthrough {
		t.Fatalf("DisableImageGeneration = %v, want passthrough", cfg.DisableImageGeneration)
	}
	if got := cfg.Routing.Strategy; got != "round-robin" {
		t.Errorf("routing strategy = %q, want %q", got, "round-robin")
	}
	if !cfg.QuotaExceeded.SwitchProject {
		t.Error("quota switch project = false, want true")
	}
	if !cfg.Codex.IdentityConfuse {
		t.Error("Codex identity confuse = false, want true")
	}
	if got := cfg.GeminiKey[0].Models[0].Alias; got != "gemini-pro" {
		t.Fatalf("Gemini model alias = %q, want %q", got, "gemini-pro")
	}
	if got := cfg.ClaudeKey[0].Models[0].Alias; got != "claude-sonnet" {
		t.Fatalf("Claude model alias = %q, want %q", got, "claude-sonnet")
	}
	if got := cfg.ClaudeKey[0].Cloak.Mode; got != "never" {
		t.Fatalf("Claude cloak mode = %q, want %q", got, "never")
	}
	if got := cfg.CodexKey[0].Models[0].Alias; got != "codex" {
		t.Fatalf("Codex model alias = %q, want %q", got, "codex")
	}
	if got := cfg.OpenAICompatibility[0].Models[0].Thinking.Levels[1]; got != "high" {
		t.Fatalf("thinking level = %q, want %q", got, "high")
	}
}
