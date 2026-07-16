package management

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// oauthSessionTTL must cover device-code flows (xAI ~30m, Kimi ~15m).
	oauthSessionTTL          = 30 * time.Minute
	oauthCompletedSessionTTL = time.Minute
	maxOAuthStateLength      = 128
)

const (
	oauthSessionSourceBuiltin = "builtin"
	oauthSessionSourcePlugin  = "plugin"
)

var (
	errInvalidOAuthState              = errors.New("invalid oauth state")
	errUnsupportedOAuthFlow           = errors.New("unsupported oauth provider")
	errOAuthSessionNotPending         = errors.New("oauth session is not pending")
	errOAuthSessionExists             = errors.New("oauth session already exists")
	errOAuthCallbackAlreadySubmitted  = errors.New("oauth callback already submitted")
	errOAuthCallbackAlreadyAwaited    = errors.New("oauth callback already awaited")
	errOAuthCallbackNotBuiltinSession = errors.New("oauth callback requires a built-in session")
	errOAuthCallbackHandlerNil        = errors.New("handler not initialized")
)

// OAuthCallback is the callback payload delivered to a pending built-in OAuth session.
type OAuthCallback struct {
	State string
	Code  string
	Error string
}

type oauthSession struct {
	Provider          string
	Status            string
	Source            string
	Metadata          map[string]any
	Completed         bool
	CreatedAt         time.Time
	ExpiresAt         time.Time
	callbackCh        chan OAuthCallback
	callbackDone      chan struct{}
	callbackSubmitted bool
	callbackAwaiting  bool
	callbackConsumed  bool
	callbackDoneClose bool
}

type oauthSessionStore struct {
	mu           sync.RWMutex
	ttl          time.Duration
	completedTTL time.Duration
	sessions     map[string]oauthSession
}

func newOAuthSessionStore(ttl time.Duration) *oauthSessionStore {
	if ttl <= 0 {
		ttl = oauthSessionTTL
	}
	completedTTL := oauthCompletedSessionTTL
	if ttl < completedTTL {
		completedTTL = ttl
	}
	return &oauthSessionStore{
		ttl:          ttl,
		completedTTL: completedTTL,
		sessions:     make(map[string]oauthSession),
	}
}

func (s *oauthSessionStore) purgeExpiredLocked(now time.Time) {
	for state, session := range s.sessions {
		if !session.ExpiresAt.IsZero() && now.After(session.ExpiresAt) {
			s.signalCallbackDoneLocked(&session)
			delete(s.sessions, state)
		}
	}
}

func (s *oauthSessionStore) signalCallbackDoneLocked(session *oauthSession) {
	if session.Source != oauthSessionSourceBuiltin || session.callbackDone == nil || session.callbackDoneClose {
		return
	}
	close(session.callbackDone)
	session.callbackDoneClose = true
}

func (s *oauthSessionStore) Register(state, provider string) {
	state = strings.TrimSpace(state)
	provider = strings.ToLower(strings.TrimSpace(provider))
	if state == "" || provider == "" {
		return
	}
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.purgeExpiredLocked(now)
	if existing, ok := s.sessions[state]; ok {
		if existing.Source == oauthSessionSourcePlugin {
			return
		}
		s.signalCallbackDoneLocked(&existing)
	}
	s.sessions[state] = oauthSession{
		Provider:     provider,
		Status:       "",
		Source:       oauthSessionSourceBuiltin,
		CreatedAt:    now,
		ExpiresAt:    now.Add(s.ttl),
		callbackCh:   make(chan OAuthCallback, 1),
		callbackDone: make(chan struct{}),
	}
}

func (s *oauthSessionStore) SubmitCallback(callback OAuthCallback) error {
	callback.State = strings.TrimSpace(callback.State)
	callback.Code = strings.TrimSpace(callback.Code)
	callback.Error = strings.TrimSpace(callback.Error)
	if callback.State == "" {
		return fmt.Errorf("%w: empty", errInvalidOAuthState)
	}

	now := time.Now()
	s.mu.Lock()
	s.purgeExpiredLocked(now)
	session, ok := s.sessions[callback.State]
	if !ok || session.Completed || session.Status != "" {
		s.mu.Unlock()
		return errOAuthSessionNotPending
	}
	if session.Source != oauthSessionSourceBuiltin {
		s.mu.Unlock()
		return errOAuthCallbackNotBuiltinSession
	}
	if session.callbackSubmitted {
		s.mu.Unlock()
		return errOAuthCallbackAlreadySubmitted
	}

	session.callbackSubmitted = true
	callbackCh := session.callbackCh
	s.sessions[callback.State] = session
	s.mu.Unlock()

	callbackCh <- callback
	return nil
}

func (s *oauthSessionStore) AwaitCallback(ctx context.Context, state string) (OAuthCallback, error) {
	state = strings.TrimSpace(state)
	if state == "" {
		return OAuthCallback{}, fmt.Errorf("%w: empty", errInvalidOAuthState)
	}

	now := time.Now()
	s.mu.Lock()
	s.purgeExpiredLocked(now)
	session, ok := s.sessions[state]
	if !ok || session.Completed || session.Status != "" {
		s.mu.Unlock()
		return OAuthCallback{}, errOAuthSessionNotPending
	}
	if session.Source != oauthSessionSourceBuiltin {
		s.mu.Unlock()
		return OAuthCallback{}, errOAuthCallbackNotBuiltinSession
	}
	if session.callbackConsumed {
		s.mu.Unlock()
		return OAuthCallback{}, errOAuthSessionNotPending
	}
	if session.callbackAwaiting {
		s.mu.Unlock()
		return OAuthCallback{}, errOAuthCallbackAlreadyAwaited
	}
	session.callbackAwaiting = true
	callbackCh := session.callbackCh
	callbackDone := session.callbackDone
	s.sessions[state] = session
	s.mu.Unlock()

	consumeCallback := func(callback OAuthCallback) (OAuthCallback, error) {
		if s.finishAwait(state, callbackCh, true) {
			return OAuthCallback{}, errOAuthSessionNotPending
		}
		return callback, nil
	}
	select {
	case callback := <-callbackCh:
		return consumeCallback(callback)
	default:
	}

	select {
	case callback := <-callbackCh:
		return consumeCallback(callback)
	case <-callbackDone:
		s.finishAwait(state, callbackCh, false)
		return OAuthCallback{}, errOAuthSessionNotPending
	case <-ctx.Done():
		select {
		case callback := <-callbackCh:
			return consumeCallback(callback)
		default:
		}
		if s.finishAwait(state, callbackCh, false) {
			return OAuthCallback{}, errOAuthSessionNotPending
		}
		return OAuthCallback{}, ctx.Err()
	}
}

func (s *oauthSessionStore) finishAwait(state string, callbackCh chan OAuthCallback, consumed bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[state]
	if !ok || session.callbackCh != callbackCh || session.Completed || session.Status != "" {
		return true
	}
	session.callbackAwaiting = false
	if consumed {
		session.callbackConsumed = true
	}
	s.sessions[state] = session
	return false
}

func (s *oauthSessionStore) RegisterPlugin(state, provider string, metadata map[string]any) error {
	state = strings.TrimSpace(state)
	provider = strings.ToLower(strings.TrimSpace(provider))
	if state == "" || provider == "" {
		return fmt.Errorf("%w: empty state or provider", errInvalidOAuthState)
	}
	if errState := ValidateOAuthState(state); errState != nil {
		return errState
	}
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.purgeExpiredLocked(now)
	if _, ok := s.sessions[state]; ok {
		return errOAuthSessionExists
	}
	s.sessions[state] = oauthSession{
		Provider:  provider,
		Status:    "",
		Source:    oauthSessionSourcePlugin,
		Metadata:  cloneOAuthSessionMetadata(metadata),
		CreatedAt: now,
		ExpiresAt: now.Add(s.ttl),
	}
	return nil
}

func (s *oauthSessionStore) SetError(state, message string) {
	state = strings.TrimSpace(state)
	message = strings.TrimSpace(message)
	if state == "" {
		return
	}
	if message == "" {
		message = "Authentication failed"
	}
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.purgeExpiredLocked(now)
	session, ok := s.sessions[state]
	if !ok || session.Completed {
		return
	}
	session.Status = message
	session.ExpiresAt = now.Add(s.ttl)
	s.signalCallbackDoneLocked(&session)
	s.sessions[state] = session
}

func (s *oauthSessionStore) Complete(state string) {
	state = strings.TrimSpace(state)
	if state == "" {
		return
	}
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.purgeExpiredLocked(now)
	session, ok := s.sessions[state]
	if !ok || session.Completed {
		return
	}
	session.Status = ""
	session.Metadata = nil
	session.Completed = true
	session.ExpiresAt = now.Add(s.completedTTL)
	s.signalCallbackDoneLocked(&session)
	s.sessions[state] = session
}

func (s *oauthSessionStore) CompleteProvider(provider string, source string) int {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return 0
	}
	source = strings.TrimSpace(source)
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.purgeExpiredLocked(now)
	removed := 0
	for state, session := range s.sessions {
		if !session.Completed && strings.EqualFold(session.Provider, provider) && (source == "" || session.Source == source) {
			session.Status = ""
			session.Metadata = nil
			session.Completed = true
			session.ExpiresAt = now.Add(s.completedTTL)
			s.signalCallbackDoneLocked(&session)
			s.sessions[state] = session
			removed++
		}
	}
	return removed
}

func (s *oauthSessionStore) Get(state string) (oauthSession, bool) {
	state = strings.TrimSpace(state)
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.purgeExpiredLocked(now)
	session, ok := s.sessions[state]
	session.Metadata = cloneOAuthSessionMetadata(session.Metadata)
	return session, ok
}

func (s *oauthSessionStore) IsPending(state, provider string) bool {
	state = strings.TrimSpace(state)
	provider = strings.ToLower(strings.TrimSpace(provider))
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.purgeExpiredLocked(now)
	session, ok := s.sessions[state]
	if !ok {
		return false
	}
	if session.Completed || session.Status != "" {
		return false
	}
	if provider == "" {
		return true
	}
	return strings.EqualFold(session.Provider, provider)
}

// Cancel removes a pending OAuth session so background waiters exit without saving credentials.
// Returns true when a pending session was cancelled.
func (s *oauthSessionStore) Cancel(state string) bool {
	state = strings.TrimSpace(state)
	if state == "" {
		return false
	}
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.purgeExpiredLocked(now)
	session, ok := s.sessions[state]
	if !ok || session.Completed || session.Status != "" {
		return false
	}
	s.signalCallbackDoneLocked(&session)
	delete(s.sessions, state)
	return true
}

func cloneOAuthSessionMetadata(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

var (
	oauthSessionsMu sync.RWMutex
	oauthSessions   = newOAuthSessionStore(oauthSessionTTL)
)

func currentOAuthSessionStore() *oauthSessionStore {
	oauthSessionsMu.RLock()
	store := oauthSessions
	oauthSessionsMu.RUnlock()
	return store
}

func swapOAuthSessionStore(store *oauthSessionStore) *oauthSessionStore {
	oauthSessionsMu.Lock()
	previous := oauthSessions
	oauthSessions = store
	oauthSessionsMu.Unlock()
	return previous
}

func registerOAuthSessionForFlow(state, provider string) *oauthSessionStore {
	store := currentOAuthSessionStore()
	store.Register(state, provider)
	return store
}

func RegisterOAuthSession(state, provider string) { registerOAuthSessionForFlow(state, provider) }

// SubmitOAuthCallback delivers a callback to a pending built-in OAuth session.
func (h *Handler) SubmitOAuthCallback(callback OAuthCallback) error {
	if h == nil {
		return errOAuthCallbackHandlerNil
	}
	return currentOAuthSessionStore().SubmitCallback(callback)
}

func RegisterPluginOAuthSession(state, provider string, metadata map[string]any) error {
	return currentOAuthSessionStore().RegisterPlugin(state, provider, metadata)
}

func SetOAuthSessionError(state, message string) { currentOAuthSessionStore().SetError(state, message) }

func CompleteOAuthSession(state string) { currentOAuthSessionStore().Complete(state) }

func CompleteOAuthSessionsByProvider(provider string) int {
	return currentOAuthSessionStore().CompleteProvider(provider, oauthSessionSourceBuiltin)
}

func CompletePluginOAuthSessionsByProvider(provider string) int {
	return currentOAuthSessionStore().CompleteProvider(provider, oauthSessionSourcePlugin)
}

func GetOAuthSession(state string) (provider string, status string, ok bool) {
	session, ok := currentOAuthSessionStore().Get(state)
	if !ok || session.Completed {
		return "", "", false
	}
	return session.Provider, session.Status, true
}

func GetOAuthSessionDetails(state string) (provider string, status string, isPlugin bool, metadata map[string]any, completed bool, ok bool) {
	session, ok := currentOAuthSessionStore().Get(state)
	if !ok {
		return "", "", false, nil, false, false
	}
	return session.Provider, session.Status, session.Source == oauthSessionSourcePlugin, cloneOAuthSessionMetadata(session.Metadata), session.Completed, true
}

func IsOAuthSessionPending(state, provider string) bool {
	return currentOAuthSessionStore().IsPending(state, provider)
}

// guardOAuthSessionPendingForSave returns errOAuthSessionNotPending when the session
// is no longer pending (cancelled, completed, errored, or expired).
// Call immediately before persisting credentials so a cancel that races with token
// exchange or metadata fetch cannot save credentials for a cancelled flow.
func guardOAuthSessionPendingForSave(store *oauthSessionStore, state, provider string) error {
	if store.IsPending(state, provider) {
		return nil
	}
	return errOAuthSessionNotPending
}

// CancelOAuthSession cancels a pending OAuth session by state.
// Background callback and device-code waiters observe IsOAuthSessionPending as false and exit without saving credentials.
func CancelOAuthSession(state string) bool {
	return currentOAuthSessionStore().Cancel(state)
}

func oauthSessionErrorWithCause(message string, cause error) string {
	message = strings.TrimSpace(message)
	if message == "" {
		message = "Authentication failed"
	}
	if cause == nil {
		return message
	}
	detail := strings.TrimSpace(cause.Error())
	if detail == "" {
		return message
	}
	return message + ": " + detail
}

func ValidateOAuthState(state string) error {
	trimmed := strings.TrimSpace(state)
	if trimmed == "" {
		return fmt.Errorf("%w: empty", errInvalidOAuthState)
	}
	if len(trimmed) > maxOAuthStateLength {
		return fmt.Errorf("%w: too long", errInvalidOAuthState)
	}
	if strings.Contains(trimmed, "/") || strings.Contains(trimmed, "\\") {
		return fmt.Errorf("%w: contains path separator", errInvalidOAuthState)
	}
	if strings.Contains(trimmed, "..") {
		return fmt.Errorf("%w: contains '..'", errInvalidOAuthState)
	}
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return fmt.Errorf("%w: invalid character", errInvalidOAuthState)
		}
	}
	return nil
}

func NormalizeOAuthProvider(provider string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "anthropic", "claude":
		return "anthropic", nil
	case "codex", "openai":
		return "codex", nil
	case "antigravity", "anti-gravity":
		return "antigravity", nil
	case "xai", "x-ai", "x.ai", "grok":
		return "xai", nil
	default:
		return "", errUnsupportedOAuthFlow
	}
}

func NormalizeOAuthCallbackProvider(provider string) (string, error) {
	if normalized, errNormalize := NormalizeOAuthProvider(provider); errNormalize == nil {
		return normalized, nil
	}
	return NormalizePluginOAuthCallbackProvider(provider)
}

func NormalizePluginOAuthCallbackProvider(provider string) (string, error) {
	trimmed := strings.ToLower(strings.TrimSpace(provider))
	if trimmed == "" {
		return "", errUnsupportedOAuthFlow
	}
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-':
		default:
			return "", errUnsupportedOAuthFlow
		}
	}
	return trimmed, nil
}

type oauthCallbackFilePayload struct {
	Code  string `json:"code"`
	State string `json:"state"`
	Error string `json:"error"`
}

// writePluginOAuthCallbackFile publishes the callback handoff consumed only by plugin adapters.
func writePluginOAuthCallbackFile(authDir, provider, state, code, errorMessage string) (string, error) {
	canonicalProvider, errNormalize := NormalizePluginOAuthCallbackProvider(provider)
	if errNormalize != nil {
		return "", errNormalize
	}
	if errState := ValidateOAuthState(state); errState != nil {
		return "", errState
	}
	session, ok := currentOAuthSessionStore().Get(state)
	if !ok || session.Source != oauthSessionSourcePlugin || session.Completed || session.Status != "" || !strings.EqualFold(session.Provider, canonicalProvider) {
		return "", errOAuthSessionNotPending
	}
	if strings.TrimSpace(authDir) == "" {
		return "", fmt.Errorf("auth dir is empty")
	}

	fileName := fmt.Sprintf(".oauth-%s-%s.oauth", canonicalProvider, state)
	filePath := filepath.Join(authDir, fileName)
	if errMkdir := os.MkdirAll(authDir, 0o700); errMkdir != nil {
		return "", fmt.Errorf("create oauth callback dir: %w", errMkdir)
	}
	payload := oauthCallbackFilePayload{
		Code:  strings.TrimSpace(code),
		State: strings.TrimSpace(state),
		Error: strings.TrimSpace(errorMessage),
	}
	data, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return "", fmt.Errorf("marshal oauth callback payload: %w", errMarshal)
	}

	temp, errCreate := os.CreateTemp(authDir, ".oauth-callback-*")
	if errCreate != nil {
		return "", fmt.Errorf("create oauth callback temp file: %w", errCreate)
	}
	tempPath := temp.Name()
	published := false
	defer func() {
		if !published {
			_ = temp.Close()
			_ = os.Remove(tempPath)
		}
	}()
	if errChmod := temp.Chmod(0o600); errChmod != nil {
		return "", fmt.Errorf("set oauth callback temp file mode: %w", errChmod)
	}
	if _, errWrite := temp.Write(data); errWrite != nil {
		return "", fmt.Errorf("write oauth callback temp file: %w", errWrite)
	}
	if errClose := temp.Close(); errClose != nil {
		return "", fmt.Errorf("close oauth callback temp file: %w", errClose)
	}
	if errRename := os.Rename(tempPath, filePath); errRename != nil {
		return "", fmt.Errorf("publish oauth callback file: %w", errRename)
	}
	published = true
	return filePath, nil
}
