package cliproxy

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestInMemoryLifecycle(t *testing.T) {
	svc, done := startInMemoryService(t, t.TempDir(), "", nil)
	defer done()

	if svc.configPath != "" {
		t.Fatalf("config path = %q, want empty", svc.configPath)
	}
}

func TestInMemoryModeRequiresExplicitOptIn(t *testing.T) {
	cfg := &config.Config{AuthDir: t.TempDir()}

	_, errBuild := NewBuilder().WithConfig(cfg).Build()
	if errBuild == nil || errBuild.Error() != "cliproxy: configuration path is required" {
		t.Fatalf("Build() error = %v, want configuration path error", errBuild)
	}

	service, errBuild := NewBuilder().WithConfig(cfg).WithInMemoryMode().Build()
	if errBuild != nil {
		t.Fatalf("Build() error = %v", errBuild)
	}
	if service.cfg == cfg {
		t.Fatal("in-memory service retained caller config")
	}

	pathBacked, errBuild := NewBuilder().WithConfig(cfg).WithConfigPath("config.yaml").Build()
	if errBuild != nil {
		t.Fatalf("path-backed Build() error = %v", errBuild)
	}
	if pathBacked.cfg != cfg {
		t.Fatal("path-backed service unexpectedly cloned caller config")
	}
}

func TestInMemoryBuildClonesCallerInput(t *testing.T) {
	cfg := &config.Config{
		AuthDir: t.TempDir(),
		GeminiKey: []config.GeminiKey{
			{APIKey: "synthetic-key"},
		},
	}

	service, errBuild := NewBuilder().WithConfig(cfg).WithInMemoryMode().Build()
	if errBuild != nil {
		t.Fatalf("Build() error = %v", errBuild)
	}

	cfg.AuthDir = "changed"
	cfg.GeminiKey[0].APIKey = "changed"
	if service.cfg.AuthDir == cfg.AuthDir {
		t.Fatal("service config shares caller AuthDir")
	}
	if service.cfg.GeminiKey[0].APIKey == cfg.GeminiKey[0].APIKey {
		t.Fatal("service config shares caller API key entry")
	}
}

func TestInMemoryModePreservesAuthFileUpdates(t *testing.T) {
	authDir := t.TempDir()
	svc, done := startInMemoryService(t, authDir, "", nil)
	defer done()

	authPath := filepath.Join(authDir, "synthetic.json")
	writeSyntheticAuth(t, authPath, "first@example.test")
	waitForAuth(t, svc.coreManager, "synthetic.json", "first@example.test", true)

	writeSyntheticAuth(t, authPath, "second@example.test")
	waitForAuth(t, svc.coreManager, "synthetic.json", "second@example.test", true)

	replacement := filepath.Join(authDir, "replacement.json")
	writeSyntheticAuth(t, replacement, "third@example.test")
	if errRename := os.Rename(replacement, authPath); errRename != nil {
		t.Fatalf("replace auth file: %v", errRename)
	}
	waitForAuth(t, svc.coreManager, "synthetic.json", "third@example.test", true)

	time.Sleep(time.Second + 50*time.Millisecond)
	if errRemove := os.Remove(authPath); errRemove != nil {
		t.Fatalf("remove auth file: %v", errRemove)
	}
	waitForAuth(t, svc.coreManager, "synthetic.json", "", false)
}

func TestInMemoryModeNeverReadsOrWatchesConfigPath(t *testing.T) {
	authDir := t.TempDir()
	trapPath := filepath.Join(t.TempDir(), "config.yaml")
	watcherStarted := make(chan struct{})
	factory := func(configPath, gotAuthDir string, reload func(*config.Config)) (*WatcherWrapper, error) {
		if configPath != "" {
			t.Fatalf("watcher config path = %q, want empty", configPath)
		}
		if reload != nil {
			t.Fatal("in-memory watcher received a config reload callback")
		}
		if gotAuthDir != authDir {
			t.Fatalf("watcher auth dir = %q, want %q", gotAuthDir, authDir)
		}
		wrapper, errFactory := defaultWatcherFactory(configPath, gotAuthDir, reload)
		if errFactory != nil {
			return nil, errFactory
		}
		start := wrapper.start
		wrapper.start = func(ctx context.Context) error {
			errStart := start(ctx)
			if errStart == nil {
				close(watcherStarted)
			}
			return errStart
		}
		return wrapper, nil
	}

	service, done := startInMemoryService(t, authDir, trapPath, factory)
	defer done()
	select {
	case <-watcherStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not start")
	}

	initialPort := service.cfg.Port
	if errWrite := os.WriteFile(trapPath, []byte("port: 1\n"), 0o600); errWrite != nil {
		t.Fatalf("write trap config: %v", errWrite)
	}
	time.Sleep(400 * time.Millisecond)
	if service.cfg.Port != initialPort {
		t.Fatalf("trap config reload changed port to %d, want %d", service.cfg.Port, initialPort)
	}
}

func startInMemoryService(t *testing.T, authDir, configPath string, factory WatcherFactory) (*Service, func()) {
	t.Helper()
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("reserve listener: %v", errListen)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if errClose := listener.Close(); errClose != nil {
		t.Fatalf("release listener: %v", errClose)
	}

	started := make(chan struct{})
	builder := NewBuilder().
		WithConfig(&config.Config{Host: "127.0.0.1", Port: port, AuthDir: authDir}).
		WithInMemoryMode().
		WithHooks(Hooks{OnAfterStart: func(*Service) { close(started) }})
	if configPath != "" {
		builder.WithConfigPath(configPath)
	}
	if factory != nil {
		builder.WithWatcherFactory(factory)
	}
	service, errBuild := builder.Build()
	if errBuild != nil {
		t.Fatalf("Build() error = %v", errBuild)
	}

	ctx, cancel := context.WithCancel(context.Background())
	runResult := make(chan error, 1)
	go func() { runResult <- service.Run(ctx) }()
	select {
	case <-started:
	case errRun := <-runResult:
		cancel()
		t.Fatalf("Run() ended before startup: %v", errRun)
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("service did not start")
	}
	waitForListening(t, fmt.Sprintf("127.0.0.1:%d", port))

	return service, func() {
		cancel()
		select {
		case errRun := <-runResult:
			if errRun != context.Canceled {
				t.Errorf("Run() error = %v, want context canceled", errRun)
			}
		case <-time.After(5 * time.Second):
			t.Error("service did not stop")
		}
	}
}

func waitForListening(t *testing.T, address string) {
	t.Helper()
	client := &http.Client{Timeout: 100 * time.Millisecond}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response, errGet := client.Get("http://" + address + "/")
		if errGet == nil {
			if errClose := response.Body.Close(); errClose != nil {
				t.Fatalf("close response body: %v", errClose)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("service did not listen on %s", address)
}

func writeSyntheticAuth(t *testing.T, path, email string) {
	t.Helper()
	if errWrite := os.WriteFile(path, []byte(`{"type":"claude","email":"`+email+`"}`), 0o600); errWrite != nil {
		t.Fatalf("write synthetic auth: %v", errWrite)
	}
}

func waitForAuth(t *testing.T, manager *coreauth.Manager, id, label string, wantPresent bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		auth, ok := manager.GetByID(id)
		if ok == wantPresent && (!wantPresent || auth.Label == label) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	auth, ok := manager.GetByID(id)
	t.Fatalf("auth %q present=%t label=%q, want present=%t label=%q", id, ok, authLabel(auth), wantPresent, label)
}

func authLabel(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	return auth.Label
}
