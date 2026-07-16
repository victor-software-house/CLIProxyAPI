package cliproxy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"gopkg.in/yaml.v3"
)

func TestSerializedApply(t *testing.T) {
	service := newConfigApplyService(t, &config.Config{})
	var applying atomic.Int32
	var maxApplying atomic.Int32
	service.watcher = &WatcherWrapper{setConfig: func(*config.Config) {
		current := applying.Add(1)
		for {
			previous := maxApplying.Load()
			if current <= previous || maxApplying.CompareAndSwap(previous, current) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		applying.Add(-1)
	}}

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(port int) {
			defer wg.Done()
			<-start
			if _, errApply := service.ApplyConfig(context.Background(), &config.Config{Port: port}); errApply != nil {
				t.Errorf("ApplyConfig() error = %v", errApply)
			}
		}(8100 + i)
	}
	close(start)
	wg.Wait()

	if got := maxApplying.Load(); got != 1 {
		t.Fatalf("concurrent watcher config publications = %d, want 1", got)
	}
}

func TestApplyConfigClonesCallerInput(t *testing.T) {
	service := newConfigApplyService(t, &config.Config{})
	input := &config.Config{AuthDir: "before", GeminiKey: []config.GeminiKey{{APIKey: "synthetic-before"}}}

	if _, errApply := service.ApplyConfig(context.Background(), input); errApply != nil {
		t.Fatalf("ApplyConfig() error = %v", errApply)
	}
	input.AuthDir = "after"
	input.GeminiKey[0].APIKey = "synthetic-after"

	if service.cfg.AuthDir != "before" {
		t.Fatalf("applied AuthDir = %q, want before", service.cfg.AuthDir)
	}
	if service.cfg.GeminiKey[0].APIKey != "synthetic-before" {
		t.Fatalf("applied Gemini API key = %q, want synthetic-before", service.cfg.GeminiKey[0].APIKey)
	}
}

func TestApplyConfigHashCoversYAMLAndRuntimeOnlyFields(t *testing.T) {
	baseConfig := configApplyConfig(t, "example", "before")
	baseConfig.Host = "127.0.0.1"
	baseConfig.Port = 8100
	baseConfig.AuthDir = "auth-before"
	baseConfig.RemoteManagement.AllowRemote = false
	base := configApplyHash(t, baseConfig)

	mutations := []struct {
		name  string
		apply func(*config.Config)
	}{
		{"Host", func(cfg *config.Config) { cfg.Host = "localhost" }},
		{"Port", func(cfg *config.Config) { cfg.Port++ }},
		{"AuthDir", func(cfg *config.Config) { cfg.AuthDir = "auth-after" }},
		{"RemoteManagement", func(cfg *config.Config) { cfg.RemoteManagement.AllowRemote = true }},
		{"PluginRaw", func(cfg *config.Config) {
			plugin := cfg.Plugins.Configs["example"]
			plugin.Raw = configApplyPluginRaw(t, "after")
			cfg.Plugins.Configs["example"] = plugin
		}},
		{"HomeEnabled", func(cfg *config.Config) { cfg.Home.Enabled = true }},
		{"HomeNodeID", func(cfg *config.Config) { cfg.Home.NodeID = "node-after" }},
		{"HomeHost", func(cfg *config.Config) { cfg.Home.Host = "home-after" }},
		{"HomePort", func(cfg *config.Config) { cfg.Home.Port = 8101 }},
		{"HomeDisableClusterDiscovery", func(cfg *config.Config) { cfg.Home.DisableClusterDiscovery = true }},
		{"HomeTLSEnable", func(cfg *config.Config) { cfg.Home.TLS.Enable = true }},
		{"HomeTLSServerName", func(cfg *config.Config) { cfg.Home.TLS.ServerName = "home-tls-after" }},
		{"HomeTLSInsecureSkipVerify", func(cfg *config.Config) { cfg.Home.TLS.InsecureSkipVerify = true }},
		{"HomeTLSCACert", func(cfg *config.Config) { cfg.Home.TLS.CACert = "ca-after" }},
		{"HomeTLSClientCert", func(cfg *config.Config) { cfg.Home.TLS.ClientCert = "cert-after" }},
		{"HomeTLSClientKey", func(cfg *config.Config) { cfg.Home.TLS.ClientKey = "key-after" }},
		{"HomeTLSUseTargetServerName", func(cfg *config.Config) { cfg.Home.TLS.UseTargetServerName = true }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			cfg := configApplyConfig(t, "example", "before")
			cfg.Host = "127.0.0.1"
			cfg.Port = 8100
			cfg.AuthDir = "auth-before"
			cfg.RemoteManagement.AllowRemote = false
			mutation.apply(cfg)
			if got := configApplyHash(t, cfg); got == base {
				t.Fatalf("hash did not change after %s mutation", mutation.name)
			}
		})
	}
}

func TestApplyConfigHashIgnoresMapInsertionOrder(t *testing.T) {
	first := configApplyConfig(t, "alpha", "one")
	first.Plugins.Configs["beta"] = configApplyConfig(t, "beta", "two").Plugins.Configs["beta"]
	first.OAuthExcludedModels = map[string][]string{"alpha": {"one"}, "beta": {"two"}}

	second := configApplyConfig(t, "beta", "two")
	second.Plugins.Configs["alpha"] = configApplyConfig(t, "alpha", "one").Plugins.Configs["alpha"]
	second.OAuthExcludedModels = map[string][]string{}
	second.OAuthExcludedModels["beta"] = []string{"two"}
	second.OAuthExcludedModels["alpha"] = []string{"one"}

	if got, want := configApplyHash(t, second), configApplyHash(t, first); got != want {
		t.Fatalf("map insertion changed hash: got %s, want %s", got, want)
	}
}

func TestApplyConfigPublishesWatcherConfigBeforeSuccess(t *testing.T) {
	service := newConfigApplyService(t, &config.Config{})
	var watched *config.Config
	service.watcher = &WatcherWrapper{setConfig: func(cfg *config.Config) { watched = cfg }}
	input := &config.Config{Port: 8100}

	result, errApply := service.ApplyConfig(context.Background(), input)
	if errApply != nil {
		t.Fatalf("ApplyConfig() error = %v", errApply)
	}
	if watched == nil {
		t.Fatal("watcher did not receive applied config")
	}
	if watched == input || watched == service.cfg {
		t.Fatal("watcher retained an applied config pointer")
	}
	if watched.Port != service.cfg.Port {
		t.Fatalf("watcher config port = %d, applied port = %d", watched.Port, service.cfg.Port)
	}
	if result.ConfigHash == "" || result.AppliedAt.IsZero() {
		t.Fatalf("ApplyConfig() result = %#v, want hash and timestamp", result)
	}
	if result.AppliedAt.Location() != time.UTC {
		t.Fatalf("ApplyConfig() timestamp location = %v, want UTC", result.AppliedAt.Location())
	}
}

func TestAppliedConfigDrivesSubsequentAuthSynthesis(t *testing.T) {
	authDir := t.TempDir()
	service, done := startInMemoryService(t, authDir, "", nil)
	defer done()

	updated := service.cfg.CloneForRuntime()
	updated.OAuthExcludedModels = map[string][]string{
		"claude": {"post-apply-model"},
	}
	if _, errApply := service.ApplyConfig(context.Background(), updated); errApply != nil {
		t.Fatalf("ApplyConfig() error = %v", errApply)
	}

	authPath := filepath.Join(authDir, "post-apply.json")
	writeSyntheticAuth(t, authPath, "post-apply@example.test")
	waitForAuth(t, service.coreManager, "post-apply.json", "post-apply@example.test", true)

	auth, ok := service.coreManager.GetByID("post-apply.json")
	if !ok || auth == nil {
		t.Fatal("watcher-synthesized auth is unavailable")
	}
	if got := auth.Attributes["excluded_models"]; got != "post-apply-model" {
		t.Fatalf("watcher-synthesized excluded_models = %q, want post-apply-model", got)
	}
}

func TestApplyConfigCancelledContextDoesNotMutate(t *testing.T) {
	service := newConfigApplyService(t, &config.Config{Port: 8100})
	before, errSnapshot := service.ConfigSnapshot(context.Background())
	if errSnapshot != nil {
		t.Fatalf("ConfigSnapshot() error = %v", errSnapshot)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, errApply := service.ApplyConfig(ctx, &config.Config{Port: 8101})
	if !errors.Is(errApply, context.Canceled) {
		t.Fatalf("ApplyConfig() error = %v, want context canceled", errApply)
	}
	after, errSnapshot := service.ConfigSnapshot(context.Background())
	if errSnapshot != nil {
		t.Fatalf("ConfigSnapshot() error = %v", errSnapshot)
	}
	if service.cfg.Port != 8100 || after != before {
		t.Fatalf("cancelled apply mutated service: port=%d snapshot=%#v, want %#v", service.cfg.Port, after, before)
	}
}

func TestConfigSnapshotAvailableBeforeFirstApply(t *testing.T) {
	initial := &config.Config{Port: 8100}
	service := newConfigApplyService(t, initial)

	snapshot, errSnapshot := service.ConfigSnapshot(context.Background())
	if errSnapshot != nil {
		t.Fatalf("ConfigSnapshot() error = %v", errSnapshot)
	}
	if snapshot.ConfigHash == "" || snapshot.AppliedAt.IsZero() {
		t.Fatalf("ConfigSnapshot() = %#v, want initial hash and timestamp", snapshot)
	}
	if snapshot.AppliedAt.Location() != time.UTC {
		t.Fatalf("snapshot time location = %v, want UTC", snapshot.AppliedAt.Location())
	}
}

func TestConcurrentApplyRaceFree(t *testing.T) {
	service := newConfigApplyService(t, &config.Config{})
	service.watcher = &WatcherWrapper{setConfig: func(*config.Config) {}}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(port int) {
			defer wg.Done()
			if _, errApply := service.ApplyConfig(context.Background(), &config.Config{Port: port}); errApply != nil {
				t.Errorf("ApplyConfig() error = %v", errApply)
			}
		}(8200 + i)
	}
	wg.Wait()
}

func TestApplyConfigCreatesNoConfigFile(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	service := newConfigApplyService(t, &config.Config{})
	service.configPath = configPath

	if _, errApply := service.ApplyConfig(context.Background(), &config.Config{Port: 8100}); errApply != nil {
		t.Fatalf("ApplyConfig() error = %v", errApply)
	}
	if _, errStat := os.Stat(configPath); !errors.Is(errStat, os.ErrNotExist) {
		t.Fatalf("config path stat error = %v, want not exist", errStat)
	}
}

func newConfigApplyService(t *testing.T, cfg *config.Config) *Service {
	t.Helper()
	service, errBuild := NewBuilder().WithConfig(cfg).WithInMemoryMode().Build()
	if errBuild != nil {
		t.Fatalf("Build() error = %v", errBuild)
	}
	t.Cleanup(func() {
		for _, auth := range service.coreManager.List() {
			GlobalModelRegistry().UnregisterClient(auth.ID)
		}
	})
	return service
}

func configApplyConfig(t *testing.T, name, value string) *config.Config {
	t.Helper()
	cfg, errParse := config.ParseConfigBytes([]byte("plugins:\n  configs:\n    " + name + ":\n      enabled: false\n      custom: " + value + "\n"))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	return cfg
}

func configApplyHash(t *testing.T, cfg *config.Config) string {
	t.Helper()
	service := newConfigApplyService(t, cfg)
	snapshot, errSnapshot := service.ConfigSnapshot(context.Background())
	if errSnapshot != nil {
		t.Fatalf("ConfigSnapshot() error = %v", errSnapshot)
	}
	return snapshot.ConfigHash
}

func configApplyPluginRaw(t *testing.T, value string) yaml.Node {
	t.Helper()
	var node yaml.Node
	if errUnmarshal := yaml.Unmarshal([]byte("enabled: false\ncustom: "+value+"\n"), &node); errUnmarshal != nil {
		t.Fatalf("parse plugin raw YAML: %v", errUnmarshal)
	}
	if len(node.Content) != 1 {
		t.Fatalf("plugin raw document content = %d, want 1", len(node.Content))
	}
	return *node.Content[0]
}
