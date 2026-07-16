package management

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestBuiltInOAuthCallbacksSubmitInMemoryWithoutCreatingAuthDir(t *testing.T) {
	for _, provider := range []string{"anthropic", "codex", "antigravity"} {
		t.Run(provider, func(t *testing.T) {
			replaceOAuthSessionStoreForTest(t, newOAuthSessionStore(time.Minute))
			authDir := filepath.Join(t.TempDir(), "missing-auth")
			state := "test-" + provider + "-state"
			RegisterOAuthSession(state, provider)
			defer CompleteOAuthSession(state)

			h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)
			router := gin.New()
			router.GET("/"+provider+"/callback", h.GetOAuthCallback)

			req := httptest.NewRequest(http.MethodGet, "/"+provider+"/callback?provider="+provider+"&state="+state+"&code=test-code", nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("callback status = %d, want %d; body=%s", w.Code, http.StatusOK, w.Body.String())
			}

			callback, errAwait := currentOAuthSessionStore().AwaitCallback(context.Background(), state)
			if errAwait != nil {
				t.Fatalf("await callback: %v", errAwait)
			}
			if callback.State != state || callback.Code != "test-code" || callback.Error != "" {
				t.Fatalf("unexpected callback: %+v", callback)
			}
			if _, errStat := os.Stat(authDir); !os.IsNotExist(errStat) {
				t.Fatalf("built-in callback created auth dir: %v", errStat)
			}
		})
	}
}

func TestGetOAuthCallbackRejectsDuplicateBuiltInCallback(t *testing.T) {
	replaceOAuthSessionStoreForTest(t, newOAuthSessionStore(time.Minute))
	state := "test-codex-state"
	RegisterOAuthSession(state, "codex")
	defer CompleteOAuthSession(state)

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: filepath.Join(t.TempDir(), "missing-auth")}, nil)
	router := gin.New()
	router.GET("/v0/management/oauth-callback", h.GetOAuthCallback)

	request := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/v0/management/oauth-callback?provider=codex&state="+state+"&code=test-code", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	if w := request(); w.Code != http.StatusOK {
		t.Fatalf("first GET callback status = %d, want %d; body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	if w := request(); w.Code != http.StatusConflict {
		t.Fatalf("duplicate GET callback status = %d, want %d; body=%s", w.Code, http.StatusConflict, w.Body.String())
	}
}

func TestGetOAuthCallbackPublishesPluginCallbackFile(t *testing.T) {
	replaceOAuthSessionStoreForTest(t, newOAuthSessionStore(time.Minute))
	authDir := filepath.Join(t.TempDir(), "missing-auth")
	state := "test-geminicli-state"
	if errRegister := RegisterPluginOAuthSession(state, "gemini-cli", nil); errRegister != nil {
		t.Fatalf("register plugin oauth session: %v", errRegister)
	}
	defer CompleteOAuthSession(state)

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)
	router := gin.New()
	router.GET("/v0/management/oauth-callback", h.GetOAuthCallback)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/oauth-callback?state="+state+"&code=test-code", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("callback status = %d, want %d; body=%s", w.Code, http.StatusOK, w.Body.String())
	}

	callbackPath := filepath.Join(authDir, ".oauth-gemini-cli-"+state+".oauth")
	assertPluginOAuthCallbackFile(t, callbackPath, oauthCallbackFilePayload{Code: "test-code", State: state})
}

func TestWritePluginOAuthCallbackFileAtomicallyReplacesCallback(t *testing.T) {
	replaceOAuthSessionStoreForTest(t, newOAuthSessionStore(time.Minute))
	authDir := t.TempDir()
	state := "plugin-atomic-state"
	if errRegister := RegisterPluginOAuthSession(state, "gemini-cli", nil); errRegister != nil {
		t.Fatalf("register plugin oauth session: %v", errRegister)
	}
	defer CompleteOAuthSession(state)

	callbackPath := filepath.Join(authDir, ".oauth-gemini-cli-"+state+".oauth")
	if errWrite := os.WriteFile(callbackPath, []byte(`{"code":"stale"}`), 0o600); errWrite != nil {
		t.Fatalf("seed callback file: %v", errWrite)
	}
	path, errWrite := writePluginOAuthCallbackFile(authDir, "gemini-cli", state, "fresh-code", "")
	if errWrite != nil {
		t.Fatalf("write plugin callback file: %v", errWrite)
	}
	if path != callbackPath {
		t.Fatalf("callback path = %q, want %q", path, callbackPath)
	}
	assertPluginOAuthCallbackFile(t, callbackPath, oauthCallbackFilePayload{Code: "fresh-code", State: state})

	entries, errReadDir := os.ReadDir(authDir)
	if errReadDir != nil {
		t.Fatalf("read callback directory: %v", errReadDir)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".oauth-callback-") {
			t.Fatalf("temporary callback file remains: %s", entry.Name())
		}
	}
}

func TestWritePluginOAuthCallbackFileRejectsInvalidOrNonPendingSessions(t *testing.T) {
	tests := []struct {
		name     string
		state    string
		provider string
		register func() error
		want     error
	}{
		{
			name:     "invalid state",
			state:    "bad/state",
			provider: "gemini-cli",
			want:     errInvalidOAuthState,
		},
		{
			name:     "nonpending session",
			state:    "missing-state",
			provider: "gemini-cli",
			want:     errOAuthSessionNotPending,
		},
		{
			name:     "plugin provider mismatch",
			state:    "plugin-mismatch-state",
			provider: "other-plugin",
			register: func() error {
				return RegisterPluginOAuthSession("plugin-mismatch-state", "gemini-cli", nil)
			},
			want: errOAuthSessionNotPending,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			replaceOAuthSessionStoreForTest(t, newOAuthSessionStore(time.Minute))
			if test.register != nil {
				if errRegister := test.register(); errRegister != nil {
					t.Fatalf("register plugin oauth session: %v", errRegister)
				}
			}
			_, errWrite := writePluginOAuthCallbackFile(t.TempDir(), test.provider, test.state, "test-code", "")
			if !errors.Is(errWrite, test.want) {
				t.Fatalf("write plugin callback error = %v, want %v", errWrite, test.want)
			}
		})
	}
}

func TestGetOAuthCallbackDoesNotAliasPluginProvider(t *testing.T) {
	replaceOAuthSessionStoreForTest(t, newOAuthSessionStore(time.Minute))
	authDir := filepath.Join(t.TempDir(), "missing-auth")
	state := "test-openai-plugin-state"
	if errRegister := RegisterPluginOAuthSession(state, "openai", nil); errRegister != nil {
		t.Fatalf("register plugin oauth session: %v", errRegister)
	}
	defer CompleteOAuthSession(state)

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)
	router := gin.New()
	router.GET("/v0/management/oauth-callback", h.GetOAuthCallback)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/oauth-callback?state="+state+"&code=test-code", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("callback status = %d, want %d; body=%s", w.Code, http.StatusOK, w.Body.String())
	}

	assertPluginOAuthCallbackFile(t, filepath.Join(authDir, ".oauth-openai-"+state+".oauth"), oauthCallbackFilePayload{Code: "test-code", State: state})
	if _, errRead := os.ReadFile(filepath.Join(authDir, ".oauth-codex-"+state+".oauth")); errRead == nil {
		t.Fatal("unexpected codex callback file for openai plugin provider")
	}
}

func assertPluginOAuthCallbackFile(t *testing.T, path string, want oauthCallbackFilePayload) {
	t.Helper()
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("read plugin callback file: %v", errRead)
	}
	var got oauthCallbackFilePayload
	if errUnmarshal := json.Unmarshal(data, &got); errUnmarshal != nil {
		t.Fatalf("decode plugin callback payload: %v", errUnmarshal)
	}
	if got != want {
		t.Fatalf("callback payload = %+v, want %+v", got, want)
	}
	info, errStat := os.Stat(path)
	if errStat != nil {
		t.Fatalf("stat plugin callback file: %v", errStat)
	}
	if gotMode := info.Mode().Perm(); gotMode != 0o600 {
		t.Fatalf("plugin callback file mode = %04o, want %04o", gotMode, 0o600)
	}
}
