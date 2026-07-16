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
