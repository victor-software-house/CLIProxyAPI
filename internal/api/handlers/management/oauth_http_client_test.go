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

func TestOAuthStartPathsUseHandlerHTTPClient(t *testing.T) {
	gin.SetMode(gin.TestMode)
	authDir := t.TempDir()
	client := &http.Client{}
	handler := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil, client)

	previousClaudeService := newClaudeOAuthService
	previousCodexService := newCodexOAuthService
	previousXAIService := newXAIOAuthService
	t.Cleanup(func() {
		newClaudeOAuthService = previousClaudeService
		newCodexOAuthService = previousCodexService
		newXAIOAuthService = previousXAIService
	})

	claudeExchangeStarted := make(chan struct{})
	codexExchangeStarted := make(chan struct{})
	var claudeClient, codexClient, xaiClient *http.Client
	newClaudeOAuthService = func(_ *config.Config, got *http.Client) claudeOAuthService {
		claudeClient = got
		return testClaudeOAuthService{exchangeStarted: claudeExchangeStarted}
	}
	newCodexOAuthService = func(_ *config.Config, got *http.Client) codexOAuthService {
		codexClient = got
		return testCodexOAuthService{exchangeStarted: codexExchangeStarted}
	}
	newXAIOAuthService = func(_ *config.Config, got *http.Client) xaiOAuthService {
		xaiClient = got
		return testXAIOAuthService{}
	}

	claudeState := requestOAuthStart(t, handler.RequestAnthropicToken)
	if _, err := WriteOAuthCallbackFileForPendingSession(authDir, "anthropic", claudeState, "test-code", ""); err != nil {
		t.Fatalf("write Claude callback: %v", err)
	}
	<-claudeExchangeStarted
	waitForOAuthSessionCompletion(t, claudeState, "anthropic")

	codexState := requestOAuthStart(t, handler.RequestCodexToken)
	if _, err := WriteOAuthCallbackFileForPendingSession(authDir, "codex", codexState, "test-code", ""); err != nil {
		t.Fatalf("write Codex callback: %v", err)
	}
	<-codexExchangeStarted
	waitForOAuthSessionCompletion(t, codexState, "codex")

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
