package codex

import (
	"net/http"
	"testing"
)

func TestNewCodexAuthWithHTTPClientUsesCallerClientWithoutGlobalMutation(t *testing.T) {
	defaultClient := http.DefaultClient
	defaultTransport := http.DefaultTransport
	client := &http.Client{}

	if auth := NewCodexAuthWithHTTPClient(nil, client); auth.httpClient != client {
		t.Fatalf("http client = %p, want caller client %p", auth.httpClient, client)
	}
	if auth := NewCodexAuthWithHTTPClient(nil, nil); auth.httpClient == nil || auth.httpClient == http.DefaultClient {
		t.Fatalf("nil client constructed %p, want a non-default client", auth.httpClient)
	}
	if http.DefaultClient != defaultClient || http.DefaultTransport != defaultTransport {
		t.Fatal("OAuth construction mutated an HTTP global")
	}
}
