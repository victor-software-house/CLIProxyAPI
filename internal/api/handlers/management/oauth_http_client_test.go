package management

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/antigravity"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	xaiauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/xai"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

type testClaudeOAuthService struct {
	exchangeStarted chan<- struct{}
}

func (s testClaudeOAuthService) GenerateAuthURL(state string, _ *claude.PKCECodes) (string, string, error) {
	return "https://auth.example/claude", state, nil
}

func (s testClaudeOAuthService) ExchangeCodeForTokens(context.Context, string, string, *claude.PKCECodes) (*claude.ClaudeAuthBundle, error) {
	close(s.exchangeStarted)
	return nil, errors.New("stop before credential persistence")
}

func (testClaudeOAuthService) CreateTokenStorage(*claude.ClaudeAuthBundle) *claude.ClaudeTokenStorage {
	return nil
}

type testCodexOAuthService struct {
	exchangeStarted chan<- struct{}
}

func (s testCodexOAuthService) GenerateAuthURL(string, *codex.PKCECodes) (string, error) {
	return "https://auth.example/codex", nil
}

func (s testCodexOAuthService) ExchangeCodeForTokens(context.Context, string, *codex.PKCECodes) (*codex.CodexAuthBundle, error) {
	close(s.exchangeStarted)
	return nil, errors.New("stop before credential persistence")
}

func (testCodexOAuthService) CreateTokenStorage(*codex.CodexAuthBundle) *codex.CodexTokenStorage {
	return nil
}

type testAntigravityOAuthService struct {
	exchangeStarted chan<- struct{}
}

func (s testAntigravityOAuthService) BuildAuthURL(string, string) string {
	return "https://auth.example/antigravity"
}

func (s testAntigravityOAuthService) ExchangeCodeForTokens(context.Context, string, string) (*antigravity.TokenResponse, error) {
	close(s.exchangeStarted)
	return nil, errors.New("stop before credential persistence")
}

func (testAntigravityOAuthService) FetchUserInfo(context.Context, string) (string, error) {
	return "", errors.New("unexpected user info fetch")
}

func (testAntigravityOAuthService) FetchProjectID(context.Context, string) (string, error) {
	return "", errors.New("unexpected project fetch")
}

type testXAIOAuthService struct{}

func (testXAIOAuthService) StartDeviceFlow(context.Context) (*xaiauth.DeviceCodeResponse, error) {
	return nil, errors.New("stop before device authorization")
}

func (testXAIOAuthService) WaitForAuthorization(context.Context, *xaiauth.DeviceCodeResponse) (*xaiauth.AuthBundle, error) {
	return nil, errors.New("unexpected authorization wait")
}

func (testXAIOAuthService) CreateTokenStorage(*xaiauth.AuthBundle) *xaiauth.TokenStorage {
	return nil
}

func requestOAuthStart(t *testing.T, hit func(*gin.Context)) string {
	t.Helper()

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	hit(ctx)
	if recorder.Code != http.StatusOK {
		t.Fatalf("OAuth start status = %d, want %d", recorder.Code, http.StatusOK)
	}

	var response struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode OAuth start response: %v", err)
	}
	if response.State == "" {
		t.Fatal("OAuth start response did not include state")
	}
	return response.State
}

func waitForOAuthSessionCompletion(t *testing.T, state, provider string) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !IsOAuthSessionPending(state, provider) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s OAuth session %s", provider, state)
}

func waitForOAuthCodeExchange(t *testing.T, provider string, exchangeStarted <-chan struct{}) {
	t.Helper()

	select {
	case <-exchangeStarted:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s OAuth code exchange", provider)
	}
}

func TestOAuthStartPathsUseHandlerHTTPClient(t *testing.T) {
	replaceOAuthSessionStoreForTest(t, newOAuthSessionStore(time.Minute))
	gin.SetMode(gin.TestMode)
	authDir := t.TempDir()
	client := &http.Client{}
	handler := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil, client)

	previousClaudeService := newClaudeOAuthService
	previousCodexService := newCodexOAuthService
	previousAntigravityService := newAntigravityOAuthService
	previousXAIService := newXAIOAuthService
	t.Cleanup(func() {
		newClaudeOAuthService = previousClaudeService
		newCodexOAuthService = previousCodexService
		newAntigravityOAuthService = previousAntigravityService
		newXAIOAuthService = previousXAIService
	})

	claudeExchangeStarted := make(chan struct{})
	codexExchangeStarted := make(chan struct{})
	antigravityExchangeStarted := make(chan struct{})
	var claudeClient, codexClient, antigravityClient, xaiClient *http.Client
	newClaudeOAuthService = func(_ *config.Config, got *http.Client) claudeOAuthService {
		claudeClient = got
		return testClaudeOAuthService{exchangeStarted: claudeExchangeStarted}
	}
	newCodexOAuthService = func(_ *config.Config, got *http.Client) codexOAuthService {
		codexClient = got
		return testCodexOAuthService{exchangeStarted: codexExchangeStarted}
	}
	newAntigravityOAuthService = func(_ *config.Config, got *http.Client) antigravityOAuthService {
		antigravityClient = got
		return testAntigravityOAuthService{exchangeStarted: antigravityExchangeStarted}
	}
	newXAIOAuthService = func(_ *config.Config, got *http.Client) xaiOAuthService {
		xaiClient = got
		return testXAIOAuthService{}
	}

	claudeState := requestOAuthStart(t, handler.RequestAnthropicToken)
	if errSubmit := handler.SubmitOAuthCallback(OAuthCallback{State: claudeState, Code: "test-code"}); errSubmit != nil {
		t.Fatalf("submit Claude callback: %v", errSubmit)
	}
	waitForOAuthCodeExchange(t, "Claude", claudeExchangeStarted)
	waitForOAuthSessionCompletion(t, claudeState, "anthropic")

	codexState := requestOAuthStart(t, handler.RequestCodexToken)
	if errSubmit := handler.SubmitOAuthCallback(OAuthCallback{State: codexState, Code: "test-code"}); errSubmit != nil {
		t.Fatalf("submit Codex callback: %v", errSubmit)
	}
	waitForOAuthCodeExchange(t, "Codex", codexExchangeStarted)
	waitForOAuthSessionCompletion(t, codexState, "codex")

	antigravityState := requestOAuthStart(t, handler.RequestAntigravityToken)
	if errSubmit := handler.SubmitOAuthCallback(OAuthCallback{State: antigravityState, Code: "test-code"}); errSubmit != nil {
		t.Fatalf("submit Antigravity callback: %v", errSubmit)
	}
	waitForOAuthCodeExchange(t, "Antigravity", antigravityExchangeStarted)
	waitForOAuthSessionCompletion(t, antigravityState, "antigravity")

	xaiRecorder := httptest.NewRecorder()
	xaiCtx, _ := gin.CreateTestContext(xaiRecorder)
	xaiCtx.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	handler.RequestXAIToken(xaiCtx)
	if xaiRecorder.Code != http.StatusInternalServerError {
		t.Fatalf("xAI start status = %d, want %d", xaiRecorder.Code, http.StatusInternalServerError)
	}

	for _, got := range []*http.Client{claudeClient, codexClient, xaiClient} {
		if got != client {
			t.Fatalf("OAuth service received client %p, want handler client %p", got, client)
		}
	}
	if antigravityClient != nil {
		t.Fatalf("Antigravity OAuth service received client %p, want nil", antigravityClient)
	}
}

func TestCancelledBuiltInCallbackDoesNotExchangeOrSave(t *testing.T) {
	replaceOAuthSessionStoreForTest(t, newOAuthSessionStore(time.Minute))
	previousCodexService := newCodexOAuthService
	t.Cleanup(func() { newCodexOAuthService = previousCodexService })

	exchangeStarted := make(chan struct{})
	newCodexOAuthService = func(_ *config.Config, _ *http.Client) codexOAuthService {
		return testCodexOAuthService{exchangeStarted: exchangeStarted}
	}

	handler := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	state := requestOAuthStart(t, handler.RequestCodexToken)
	if !CancelOAuthSession(state) {
		t.Fatal("CancelOAuthSession() = false, want true")
	}
	if errSubmit := handler.SubmitOAuthCallback(OAuthCallback{State: state, Code: "test-code"}); !errors.Is(errSubmit, errOAuthSessionNotPending) {
		t.Fatalf("SubmitOAuthCallback() error = %v, want not pending", errSubmit)
	}
	select {
	case <-exchangeStarted:
		t.Fatal("code exchange started for cancelled OAuth session")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestOAuthStartPathsRetainDistinctHandlerHTTPClients(t *testing.T) {
	gin.SetMode(gin.TestMode)
	clientA := &http.Client{}
	clientB := &http.Client{}
	handlerA := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil, clientA)
	handlerB := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil, clientB)

	previousXAIService := newXAIOAuthService
	t.Cleanup(func() { newXAIOAuthService = previousXAIService })
	var clients []*http.Client
	newXAIOAuthService = func(_ *config.Config, client *http.Client) xaiOAuthService {
		clients = append(clients, client)
		return testXAIOAuthService{}
	}

	for _, handler := range []*Handler{handlerA, handlerB} {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodPost, "/", nil)
		handler.RequestXAIToken(ctx)
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("xAI start status = %d, want %d", recorder.Code, http.StatusInternalServerError)
		}
	}

	if len(clients) != 2 {
		t.Fatalf("OAuth services created = %d, want 2", len(clients))
	}
	if clients[0] != clientA || clients[1] != clientB {
		t.Fatalf("OAuth clients = %p, %p; want %p, %p", clients[0], clients[1], clientA, clientB)
	}
}
