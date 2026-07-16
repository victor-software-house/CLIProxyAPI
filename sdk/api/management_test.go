package api

import (
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestNewHandlerWithoutConfigFilePathAcceptsCallerHTTPClient(t *testing.T) {
	client := &http.Client{}
	if handler := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil, client); handler == nil {
		t.Fatal("NewHandlerWithoutConfigFilePath() returned nil")
	}
}

func TestManagementTokenRequesterSubmitsOAuthCallback(t *testing.T) {
	state := "sdk-oauth-callback-state"
	RegisterOAuthSession(state, "codex")
	defer CompleteOAuthSession(state)

	requester := NewManagementTokenRequester(&config.Config{AuthDir: t.TempDir()}, nil)
	if errSubmit := requester.SubmitOAuthCallback(OAuthCallback{State: state, Code: "test-code"}); errSubmit != nil {
		t.Fatalf("SubmitOAuthCallback() error = %v", errSubmit)
	}
}
