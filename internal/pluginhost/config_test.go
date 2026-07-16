package pluginhost

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"gopkg.in/yaml.v3"
)

func TestRuntimeConfigFromConfigUsesConstructedHostValues(t *testing.T) {
	instance, errNew := sdkconfig.NewPluginInstanceConfig(true, 5, map[string]any{"setting": "value"})
	if errNew != nil {
		t.Fatalf("NewPluginInstanceConfig() error = %v", errNew)
	}
	enabled := false
	instance.Enabled = &enabled
	instance.Priority = 9
	cfg := &config.Config{Plugins: config.PluginsConfig{Configs: map[string]config.PluginInstanceConfig{"sample": instance}}}

	var got map[string]any
	if errDecode := yaml.Unmarshal(runtimeConfigFromConfig(cfg).Items["sample"].ConfigYAML, &got); errDecode != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", errDecode)
	}
	if got["setting"] != "value" || got["enabled"] != false || got["priority"] != 9 {
		t.Fatalf("decoded runtime config has unexpected fields")
	}
}

func TestRuntimeConfigYAMLAddsHostDefaultsToRawPluginConfig(t *testing.T) {
	var node yaml.Node
	if errDecode := yaml.Unmarshal([]byte("config1: true\nconfig2: value\n"), &node); errDecode != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", errDecode)
	}
	if len(node.Content) != 1 {
		t.Fatalf("yaml node content length = %d, want 1", len(node.Content))
	}
	item := config.PluginInstanceConfig{
		Priority: 3,
		Raw:      *node.Content[0],
	}

	got := string(runtimeConfigYAML(item, true))
	for _, want := range []string{
		"config1: true",
		"config2: value",
		"enabled: true",
		"priority: 3",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("runtimeConfigYAML() missing %q in:\n%s", want, got)
		}
	}
}

func TestRuntimeConfigYAMLDefaultsEnabledFalse(t *testing.T) {
	item := config.PluginInstanceConfig{
		Priority: 3,
	}

	got := string(runtimeConfigYAML(item, false))
	for _, want := range []string{
		"enabled: false",
		"priority: 3",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("runtimeConfigYAML() missing %q in:\n%s", want, got)
		}
	}
}

func TestRuntimeConfigFromConfigExtractsStoreVersion(t *testing.T) {
	var node yaml.Node
	if errDecode := yaml.Unmarshal([]byte("store:\n  version: 1.0.3\n  release-tag: v1.0.3\n"), &node); errDecode != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", errDecode)
	}
	enabled := true
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Enabled: true,
			Configs: map[string]config.PluginInstanceConfig{
				"alpha": {
					Enabled: &enabled,
					Raw:     *node.Content[0],
				},
			},
		},
	}

	got := runtimeConfigFromConfig(cfg)
	if got.Items["alpha"].Version != "1.0.3" {
		t.Fatalf("runtimeConfigFromConfig() version = %q, want 1.0.3", got.Items["alpha"].Version)
	}
}

func TestRuntimeConfigFromConfigDerivesStoreVersionFromReleaseTag(t *testing.T) {
	var node yaml.Node
	if errDecode := yaml.Unmarshal([]byte("store:\n  release-tag: v1.0.3\n"), &node); errDecode != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", errDecode)
	}
	enabled := true
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Enabled: true,
			Configs: map[string]config.PluginInstanceConfig{
				"alpha": {
					Enabled: &enabled,
					Raw:     *node.Content[0],
				},
			},
		},
	}

	got := runtimeConfigFromConfig(cfg)
	if got.Items["alpha"].Version != "1.0.3" {
		t.Fatalf("runtimeConfigFromConfig() version = %q, want 1.0.3", got.Items["alpha"].Version)
	}
}
