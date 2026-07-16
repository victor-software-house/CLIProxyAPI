// Package api exposes helpers for embedding CLIProxyAPI.
//
// It wraps internal management handler types and helpers so external projects
// can integrate management endpoints without importing internal packages.
package api

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	internalmanagement "github.com/router-for-me/CLIProxyAPI/v7/internal/api/handlers/management"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

// Handler re-exports the management handler used by the internal HTTP API.
type Handler = internalmanagement.Handler

// OAuthCallback re-exports a callback payload for a pending built-in OAuth session.
type OAuthCallback = internalmanagement.OAuthCallback

// ManagementTokenRequester exposes a limited subset of management endpoints for requesting tokens.
type ManagementTokenRequester interface {
	RequestAnthropicToken(*gin.Context)
	RequestCodexToken(*gin.Context)
	RequestAntigravityToken(*gin.Context)
	RequestKimiToken(*gin.Context)
	GetAuthStatus(c *gin.Context)
	PostOAuthCallback(c *gin.Context)
	SubmitOAuthCallback(OAuthCallback) error
}

type managementTokenRequester struct {
	handler *Handler
}

// NewHandler creates a management handler for SDK consumers.
func NewHandler(cfg *config.Config, configFilePath string, manager *coreauth.Manager) *Handler {
	return internalmanagement.NewHandler(cfg, configFilePath, manager)
}

// NewHandlerWithoutConfigFilePath creates a management handler that skips config file persistence.
func NewHandlerWithoutConfigFilePath(cfg *config.Config, manager *coreauth.Manager, client *http.Client) *Handler {
	return internalmanagement.NewHandlerWithoutConfigFilePath(cfg, manager, client)
}

// NewManagementTokenRequester creates a limited management handler exposing only token request endpoints.
func NewManagementTokenRequester(cfg *config.Config, manager *coreauth.Manager) ManagementTokenRequester {
	return &managementTokenRequester{
		handler: NewHandlerWithoutConfigFilePath(cfg, manager, nil),
	}
}

func (m *managementTokenRequester) RequestAnthropicToken(c *gin.Context) {
	m.handler.RequestAnthropicToken(c)
}

func (m *managementTokenRequester) RequestCodexToken(c *gin.Context) {
	m.handler.RequestCodexToken(c)
}

func (m *managementTokenRequester) RequestAntigravityToken(c *gin.Context) {
	m.handler.RequestAntigravityToken(c)
}

func (m *managementTokenRequester) RequestKimiToken(c *gin.Context) {
	m.handler.RequestKimiToken(c)
}

func (m *managementTokenRequester) GetAuthStatus(c *gin.Context) {
	m.handler.GetAuthStatus(c)
}

func (m *managementTokenRequester) PostOAuthCallback(c *gin.Context) {
	m.handler.PostOAuthCallback(c)
}

func (m *managementTokenRequester) SubmitOAuthCallback(callback OAuthCallback) error {
	return m.handler.SubmitOAuthCallback(callback)
}

// WriteConfig persists management configuration to disk.
func WriteConfig(path string, data []byte) error {
	return internalmanagement.WriteConfig(path, data)
}

// RegisterOAuthSession records a pending OAuth callback state.
func RegisterOAuthSession(state, provider string) {
	internalmanagement.RegisterOAuthSession(state, provider)
}

// SetOAuthSessionError stores an OAuth session error message.
func SetOAuthSessionError(state, message string) {
	internalmanagement.SetOAuthSessionError(state, message)
}

// CompleteOAuthSession marks a single OAuth session as completed.
func CompleteOAuthSession(state string) {
	internalmanagement.CompleteOAuthSession(state)
}

// CompleteOAuthSessionsByProvider removes all pending OAuth sessions for a provider.
func CompleteOAuthSessionsByProvider(provider string) int {
	return internalmanagement.CompleteOAuthSessionsByProvider(provider)
}

// GetOAuthSession returns the current OAuth session state.
func GetOAuthSession(state string) (provider string, status string, ok bool) {
	return internalmanagement.GetOAuthSession(state)
}

// IsOAuthSessionPending reports whether a provider/state pair is still pending.
func IsOAuthSessionPending(state, provider string) bool {
	return internalmanagement.IsOAuthSessionPending(state, provider)
}

// ValidateOAuthState validates an OAuth state token.
func ValidateOAuthState(state string) error {
	return internalmanagement.ValidateOAuthState(state)
}

// NormalizeOAuthProvider normalizes a provider name to its canonical form.
func NormalizeOAuthProvider(provider string) (string, error) {
	return internalmanagement.NormalizeOAuthProvider(provider)
}

// PopulateAuthContext copies auth metadata from a Gin context into a request context.
func PopulateAuthContext(ctx context.Context, c *gin.Context) context.Context {
	return internalmanagement.PopulateAuthContext(ctx, c)
}
