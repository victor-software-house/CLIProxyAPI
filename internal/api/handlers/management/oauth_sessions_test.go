package management

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestOAuthSessionStoreCompleteKeepsShortLivedSession(t *testing.T) {
	store := newOAuthSessionStore(time.Minute)
	store.Register("completed-state", "codex")

	store.Complete("completed-state")

	if _, ok := store.Get("completed-state"); !ok {
		t.Fatal("completed OAuth session was deleted instead of retained as a tombstone")
	}
	if store.IsPending("completed-state", "codex") {
		t.Fatal("completed OAuth session remained pending")
	}
}

func TestOAuthSessionStoreCompleteDoesNotExtendCompletedSession(t *testing.T) {
	store := newOAuthSessionStore(time.Minute)
	store.Register("completed-state", "codex")
	store.Complete("completed-state")
	before, ok := store.Get("completed-state")
	if !ok {
		t.Fatal("completed OAuth session tombstone is missing")
	}

	store.completedTTL = 2 * time.Minute
	store.Complete("completed-state")
	after, ok := store.Get("completed-state")
	if !ok {
		t.Fatal("completed OAuth session tombstone is missing after repeated completion")
	}
	if !after.ExpiresAt.Equal(before.ExpiresAt) {
		t.Fatalf("repeated completion extended expiry from %s to %s", before.ExpiresAt, after.ExpiresAt)
	}
}

func TestOAuthSessionStoreCompleteProviderSkipsCompletedSessions(t *testing.T) {
	store := newOAuthSessionStore(time.Minute)
	store.Register("completed-state", "codex")
	store.Register("pending-state", "codex")
	store.Complete("completed-state")
	completedBefore, ok := store.Get("completed-state")
	if !ok {
		t.Fatal("completed OAuth session tombstone is missing")
	}

	store.completedTTL = 2 * time.Minute
	if got := store.CompleteProvider("codex", oauthSessionSourceBuiltin); got != 1 {
		t.Fatalf("CompleteProvider() = %d, want 1 newly completed session", got)
	}
	completedAfter, ok := store.Get("completed-state")
	if !ok {
		t.Fatal("completed OAuth session tombstone is missing after provider completion")
	}
	if !completedAfter.ExpiresAt.Equal(completedBefore.ExpiresAt) {
		t.Fatalf("provider completion extended existing tombstone from %s to %s", completedBefore.ExpiresAt, completedAfter.ExpiresAt)
	}
	pendingAfter, ok := store.Get("pending-state")
	if !ok || !pendingAfter.Completed {
		t.Fatalf("pending session completed/ok = %t/%t, want true/true", pendingAfter.Completed, ok)
	}
}

func TestGetOAuthSessionHidesCompletedSession(t *testing.T) {
	store := newOAuthSessionStore(time.Minute)
	replaceOAuthSessionStoreForTest(t, store)
	store.Register("completed-state", "codex")
	store.Complete("completed-state")

	provider, status, ok := GetOAuthSession("completed-state")
	if ok {
		t.Fatalf("GetOAuthSession() = (%q, %q, true), want completed session hidden", provider, status)
	}

	_, _, _, _, completed, detailsOK := GetOAuthSessionDetails("completed-state")
	if !detailsOK || !completed {
		t.Fatalf("GetOAuthSessionDetails() completed/ok = %t/%t, want true/true", completed, detailsOK)
	}
}

func TestGetAuthStatusRejectsUnknownStateAndAcceptsCompletedState(t *testing.T) {
	store := newOAuthSessionStore(time.Minute)
	replaceOAuthSessionStoreForTest(t, store)

	handler := &Handler{}
	router := gin.New()
	router.GET("/status", handler.GetAuthStatus)

	unknown := performOAuthStatusRequest(t, router, "unknown-state")
	if unknown.Status != "error" || unknown.Error != "unknown or expired state" {
		t.Fatalf("unknown state response = %#v, want unknown/expired error", unknown)
	}

	store.Register("completed-state", "codex")
	store.Complete("completed-state")
	completed := performOAuthStatusRequest(t, router, "completed-state")
	if completed.Status != "ok" || completed.Error != "" {
		t.Fatalf("completed state response = %#v, want success", completed)
	}
}

func TestOAuthCallbackRejectsCompletedSession(t *testing.T) {
	store := newOAuthSessionStore(time.Minute)
	replaceOAuthSessionStoreForTest(t, store)
	store.Register("completed-state", "codex")
	store.Complete("completed-state")

	handler := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	router := gin.New()
	router.POST("/oauth-callback", handler.PostOAuthCallback)

	req := httptest.NewRequest(
		http.MethodPost,
		"/oauth-callback",
		strings.NewReader(`{"provider":"codex","state":"completed-state","code":"test-code"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("completed callback status = %d, want %d; body=%s", w.Code, http.StatusConflict, w.Body.String())
	}
}

type oauthStatusResponse struct {
	Status string `json:"status"`
	Error  string `json:"error"`
}

func performOAuthStatusRequest(t *testing.T, router http.Handler, state string) oauthStatusResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/status?state="+state, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status request returned %d, want %d; body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	var response oauthStatusResponse
	if errDecode := json.Unmarshal(w.Body.Bytes(), &response); errDecode != nil {
		t.Fatalf("decode status response: %v", errDecode)
	}
	return response
}

func TestOAuthSessionStoreCancelRemovesPendingSession(t *testing.T) {
	store := newOAuthSessionStore(time.Minute)
	store.Register("pending-state", "xai")

	if !store.Cancel("pending-state") {
		t.Fatal("Cancel() = false, want true for pending session")
	}
	if store.IsPending("pending-state", "xai") {
		t.Fatal("cancelled session remained pending")
	}
	if _, ok := store.Get("pending-state"); ok {
		t.Fatal("cancelled session still present in store")
	}
	if store.Cancel("pending-state") {
		t.Fatal("second Cancel() = true, want false")
	}
}

func TestOAuthSessionStoreCancelIgnoresCompletedAndUnknown(t *testing.T) {
	store := newOAuthSessionStore(time.Minute)
	store.Register("completed-state", "codex")
	store.Complete("completed-state")

	if store.Cancel("completed-state") {
		t.Fatal("Cancel() completed session = true, want false")
	}
	if _, ok := store.Get("completed-state"); !ok {
		t.Fatal("completed tombstone was removed by Cancel")
	}
	if store.Cancel("missing-state") {
		t.Fatal("Cancel() unknown session = true, want false")
	}
}

func TestOAuthSessionStoreCancelIgnoresErrorSession(t *testing.T) {
	store := newOAuthSessionStore(time.Minute)
	store.Register("error-state", "kimi")
	store.SetError("error-state", "Authentication failed")

	if store.IsPending("error-state", "kimi") {
		t.Fatal("error session should not be pending")
	}
	if store.Cancel("error-state") {
		t.Fatal("Cancel() error session = true, want false")
	}
}

func TestCancelOAuthSessionAndCallbackRejectAfterCancel(t *testing.T) {
	store := newOAuthSessionStore(time.Minute)
	replaceOAuthSessionStoreForTest(t, store)
	store.Register("callback-state", "anthropic")

	if !CancelOAuthSession("callback-state") {
		t.Fatal("CancelOAuthSession() = false, want true")
	}
	if IsOAuthSessionPending("callback-state", "anthropic") {
		t.Fatal("session still pending after cancel")
	}

	if errSubmit := store.SubmitCallback(OAuthCallback{State: "callback-state", Code: "code"}); !errors.Is(errSubmit, errOAuthSessionNotPending) {
		t.Fatalf("callback submission error = %v, want %v", errSubmit, errOAuthSessionNotPending)
	}
}

func TestGuardOAuthSessionPendingForSave(t *testing.T) {
	store := newOAuthSessionStore(time.Minute)
	replaceOAuthSessionStoreForTest(t, store)

	providers := []string{"anthropic", "codex", "antigravity", "xai", "kimi"}
	for _, provider := range providers {
		state := provider + "-save-guard"
		store.Register(state, provider)

		if errGuard := guardOAuthSessionPendingForSave(store, state, provider); errGuard != nil {
			t.Fatalf("%s pending guard error = %v, want nil", provider, errGuard)
		}

		if !CancelOAuthSession(state) {
			t.Fatalf("%s CancelOAuthSession() = false, want true", provider)
		}
		if errGuard := guardOAuthSessionPendingForSave(store, state, provider); !errors.Is(errGuard, errOAuthSessionNotPending) {
			t.Fatalf("%s after cancel guard error = %v, want %v", provider, errGuard, errOAuthSessionNotPending)
		}
	}

	// Completed and errored sessions must also refuse save.
	store.Register("completed-save", "codex")
	store.Complete("completed-save")
	if errGuard := guardOAuthSessionPendingForSave(store, "completed-save", "codex"); !errors.Is(errGuard, errOAuthSessionNotPending) {
		t.Fatalf("completed guard error = %v, want %v", errGuard, errOAuthSessionNotPending)
	}

	store.Register("error-save", "anthropic")
	store.SetError("error-save", "Authentication failed")
	if errGuard := guardOAuthSessionPendingForSave(store, "error-save", "anthropic"); !errors.Is(errGuard, errOAuthSessionNotPending) {
		t.Fatalf("error guard error = %v, want %v", errGuard, errOAuthSessionNotPending)
	}
}

func TestCancelAuthSessionHandler(t *testing.T) {
	store := newOAuthSessionStore(time.Minute)
	replaceOAuthSessionStoreForTest(t, store)
	store.Register("device-state", "xai")

	handler := &Handler{}
	router := gin.New()
	router.DELETE("/oauth-session", handler.CancelAuthSession)

	missing := performOAuthCancelRequest(t, router, "")
	if missing.status != http.StatusBadRequest {
		t.Fatalf("missing state status = %d, want %d", missing.status, http.StatusBadRequest)
	}

	invalid := performOAuthCancelRequest(t, router, "bad/state")
	if invalid.status != http.StatusBadRequest {
		t.Fatalf("invalid state status = %d, want %d", invalid.status, http.StatusBadRequest)
	}

	cancelled := performOAuthCancelRequest(t, router, "device-state")
	if cancelled.status != http.StatusOK || !cancelled.cancelled || cancelled.bodyStatus != "ok" {
		t.Fatalf("cancel pending response = %#v, want ok/cancelled", cancelled)
	}
	if IsOAuthSessionPending("device-state", "xai") {
		t.Fatal("device session still pending after cancel API")
	}

	repeat := performOAuthCancelRequest(t, router, "device-state")
	if repeat.status != http.StatusOK || repeat.cancelled {
		t.Fatalf("repeat cancel response = %#v, want ok with cancelled=false", repeat)
	}

	// Status after cancel should not report success.
	statusRouter := gin.New()
	statusRouter.GET("/status", handler.GetAuthStatus)
	unknown := performOAuthStatusRequest(t, statusRouter, "device-state")
	if unknown.Status != "error" || unknown.Error != "unknown or expired state" {
		t.Fatalf("status after cancel = %#v, want unknown/expired error", unknown)
	}
}

type oauthCancelResponse struct {
	status     int
	bodyStatus string
	cancelled  bool
}

func performOAuthCancelRequest(t *testing.T, router http.Handler, state string) oauthCancelResponse {
	t.Helper()
	path := "/oauth-session"
	if state != "" {
		path += "?state=" + state
	}
	req := httptest.NewRequest(http.MethodDelete, path, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	var body struct {
		Status    string `json:"status"`
		Cancelled bool   `json:"cancelled"`
		Error     string `json:"error"`
	}
	if w.Body.Len() > 0 {
		if errDecode := json.Unmarshal(w.Body.Bytes(), &body); errDecode != nil {
			t.Fatalf("decode cancel response: %v body=%s", errDecode, w.Body.String())
		}
	}
	return oauthCancelResponse{
		status:     w.Code,
		bodyStatus: body.Status,
		cancelled:  body.Cancelled,
	}
}

func TestSubmitOAuthCallbackDeliversToBuiltInSession(t *testing.T) {
	store := newOAuthSessionStore(time.Minute)
	replaceOAuthSessionStoreForTest(t, store)
	store.Register("callback-state", "codex")

	handler := &Handler{}
	callback := OAuthCallback{State: "callback-state", Code: "test-code"}
	if errSubmit := handler.SubmitOAuthCallback(callback); errSubmit != nil {
		t.Fatalf("SubmitOAuthCallback() error = %v", errSubmit)
	}

	delivered, errAwait := store.AwaitCallback(context.Background(), "callback-state")
	if errAwait != nil {
		t.Fatalf("AwaitCallback() error = %v", errAwait)
	}
	if delivered != callback {
		t.Fatalf("AwaitCallback() = %#v, want %#v", delivered, callback)
	}
}

func TestSubmitOAuthCallbackRejectsEmptyAndUnknownState(t *testing.T) {
	store := newOAuthSessionStore(time.Minute)

	if errSubmit := store.SubmitCallback(OAuthCallback{}); !errors.Is(errSubmit, errInvalidOAuthState) {
		t.Fatalf("empty SubmitCallback() error = %v, want invalid state", errSubmit)
	}
	if errSubmit := store.SubmitCallback(OAuthCallback{State: "unknown-state"}); !errors.Is(errSubmit, errOAuthSessionNotPending) {
		t.Fatalf("unknown SubmitCallback() error = %v, want not pending", errSubmit)
	}
}

func TestSubmitOAuthCallbackRejectsNonPendingSessions(t *testing.T) {
	store := newOAuthSessionStore(time.Minute)

	store.Register("cancelled-state", "codex")
	if !store.Cancel("cancelled-state") {
		t.Fatal("Cancel() = false, want true")
	}
	if errSubmit := store.SubmitCallback(OAuthCallback{State: "cancelled-state"}); !errors.Is(errSubmit, errOAuthSessionNotPending) {
		t.Fatalf("cancelled SubmitCallback() error = %v, want not pending", errSubmit)
	}

	store.Register("completed-state", "codex")
	store.Complete("completed-state")
	if errSubmit := store.SubmitCallback(OAuthCallback{State: "completed-state"}); !errors.Is(errSubmit, errOAuthSessionNotPending) {
		t.Fatalf("completed SubmitCallback() error = %v, want not pending", errSubmit)
	}

	store.Register("expired-state", "codex")
	store.mu.Lock()
	expired := store.sessions["expired-state"]
	expired.ExpiresAt = time.Now().Add(-time.Second)
	store.sessions["expired-state"] = expired
	store.mu.Unlock()
	if errSubmit := store.SubmitCallback(OAuthCallback{State: "expired-state"}); !errors.Is(errSubmit, errOAuthSessionNotPending) {
		t.Fatalf("expired SubmitCallback() error = %v, want not pending", errSubmit)
	}
}

func TestSubmitOAuthCallbackAllowsExactlyOneConcurrentDelivery(t *testing.T) {
	store := newOAuthSessionStore(time.Minute)
	store.Register("callback-state", "codex")

	const submitters = 16
	var wg sync.WaitGroup
	results := make(chan error, submitters)
	for i := 0; i < submitters; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- store.SubmitCallback(OAuthCallback{State: "callback-state", Code: "test-code"})
		}()
	}
	wg.Wait()
	close(results)

	successes := 0
	for errSubmit := range results {
		if errSubmit == nil {
			successes++
			continue
		}
		if !errors.Is(errSubmit, errOAuthCallbackAlreadySubmitted) {
			t.Fatalf("concurrent SubmitCallback() error = %v, want duplicate callback", errSubmit)
		}
	}
	if successes != 1 {
		t.Fatalf("successful concurrent submissions = %d, want 1", successes)
	}

	if _, errAwait := store.AwaitCallback(context.Background(), "callback-state"); errAwait != nil {
		t.Fatalf("AwaitCallback() error = %v", errAwait)
	}
}

func TestSubmitOAuthCallbackRejectsPluginSession(t *testing.T) {
	store := newOAuthSessionStore(time.Minute)
	if errRegister := store.RegisterPlugin("plugin-state", "gemini-cli", nil); errRegister != nil {
		t.Fatalf("RegisterPlugin() error = %v", errRegister)
	}

	if errSubmit := store.SubmitCallback(OAuthCallback{State: "plugin-state", Code: "test-code"}); !errors.Is(errSubmit, errOAuthCallbackNotBuiltinSession) {
		t.Fatalf("plugin SubmitCallback() error = %v, want built-in session error", errSubmit)
	}
}

func TestAwaitOAuthCallbackReturnsQueuedCallbackWhenContextIsReady(t *testing.T) {
	store := newOAuthSessionStore(time.Minute)
	callback := OAuthCallback{State: "callback-state", Code: "test-code"}
	store.Register(callback.State, "codex")
	if errSubmit := store.SubmitCallback(callback); errSubmit != nil {
		t.Fatalf("SubmitCallback() error = %v", errSubmit)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	delivered, errAwait := store.AwaitCallback(ctx, callback.State)
	if errAwait != nil || delivered != callback {
		t.Fatalf("AwaitCallback() = %#v, %v; want %#v, nil", delivered, errAwait, callback)
	}
}

func TestAwaitOAuthCallbackDoesNotReturnQueuedCallbackAfterCancel(t *testing.T) {
	store := newOAuthSessionStore(time.Minute)
	callback := OAuthCallback{State: "callback-state", Code: "test-code"}
	store.Register(callback.State, "codex")
	if errSubmit := store.SubmitCallback(callback); errSubmit != nil {
		t.Fatalf("SubmitCallback() error = %v", errSubmit)
	}
	if !store.Cancel(callback.State) {
		t.Fatal("Cancel() = false, want true")
	}

	delivered, errAwait := store.AwaitCallback(context.Background(), callback.State)
	if !errors.Is(errAwait, errOAuthSessionNotPending) {
		t.Fatalf("AwaitCallback() error = %v, want not pending", errAwait)
	}
	if delivered != (OAuthCallback{}) {
		t.Fatalf("AwaitCallback() = %#v, want no callback after cancellation", delivered)
	}
}

func TestAwaitOAuthCallbackRespectsContextCancellation(t *testing.T) {
	store := newOAuthSessionStore(time.Minute)
	store.Register("callback-state", "codex")

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, errAwait := store.AwaitCallback(ctx, "callback-state")
		result <- errAwait
	}()
	cancel()

	if errAwait := <-result; !errors.Is(errAwait, context.Canceled) {
		t.Fatalf("AwaitCallback() error = %v, want context canceled", errAwait)
	}
}

func TestSubmitOAuthCallbackNilHandler(t *testing.T) {
	var handler *Handler
	if errSubmit := handler.SubmitOAuthCallback(OAuthCallback{State: "callback-state"}); !errors.Is(errSubmit, errOAuthCallbackHandlerNil) {
		t.Fatalf("nil SubmitOAuthCallback() error = %v, want handler not initialized", errSubmit)
	}
}

func TestAwaitOAuthCallbackReturnsWhenSessionTerminates(t *testing.T) {
	tests := []struct {
		name      string
		terminate func(*oauthSessionStore)
	}{
		{"cancel", func(store *oauthSessionStore) { store.Cancel("callback-state") }},
		{"set error", func(store *oauthSessionStore) { store.SetError("callback-state", "failed") }},
		{"complete", func(store *oauthSessionStore) { store.Complete("callback-state") }},
		{"complete provider", func(store *oauthSessionStore) { store.CompleteProvider("codex", oauthSessionSourceBuiltin) }},
		{"expiry purge", func(store *oauthSessionStore) {
			store.mu.Lock()
			session := store.sessions["callback-state"]
			session.ExpiresAt = time.Now().Add(-time.Second)
			store.sessions["callback-state"] = session
			store.mu.Unlock()
			store.Get("trigger-purge")
		}},
		{"replacement", func(store *oauthSessionStore) { store.Register("callback-state", "codex") }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newOAuthSessionStore(time.Minute)
			store.Register("callback-state", "codex")
			result := awaitOAuthCallback(t, store, "callback-state")
			waitForOAuthCallbackWaiter(t, store, "callback-state")

			test.terminate(store)
			if errAwait := <-result; !errors.Is(errAwait, errOAuthSessionNotPending) {
				t.Fatalf("AwaitCallback() error = %v, want not pending", errAwait)
			}
		})
	}
}

func TestRegisterOAuthSessionPreservesPluginSession(t *testing.T) {
	store := newOAuthSessionStore(time.Minute)
	if errRegister := store.RegisterPlugin("shared-state", "plugin-provider", map[string]any{"source": "plugin"}); errRegister != nil {
		t.Fatalf("RegisterPlugin() error = %v", errRegister)
	}

	store.Register("shared-state", "codex")
	session, ok := store.Get("shared-state")
	if !ok || session.Source != oauthSessionSourcePlugin || session.Provider != "plugin-provider" {
		t.Fatalf("Register() replaced plugin session = %#v, present=%t", session, ok)
	}
}

func TestAwaitOAuthCallbackRejectsSecondWaiter(t *testing.T) {
	store := newOAuthSessionStore(time.Minute)
	store.Register("callback-state", "codex")
	result := awaitOAuthCallback(t, store, "callback-state")
	waitForOAuthCallbackWaiter(t, store, "callback-state")

	if _, errAwait := store.AwaitCallback(context.Background(), "callback-state"); !errors.Is(errAwait, errOAuthCallbackAlreadyAwaited) {
		t.Fatalf("second AwaitCallback() error = %v, want already awaited", errAwait)
	}
	store.Cancel("callback-state")
	if errAwait := <-result; !errors.Is(errAwait, errOAuthSessionNotPending) {
		t.Fatalf("first AwaitCallback() error = %v, want not pending", errAwait)
	}
}

func TestAwaitOAuthCallbackConsumesDeliveredCallbackOnce(t *testing.T) {
	store := newOAuthSessionStore(time.Minute)
	store.Register("callback-state", "codex")
	callback := OAuthCallback{State: "callback-state", Code: "test-code"}
	if errSubmit := store.SubmitCallback(callback); errSubmit != nil {
		t.Fatalf("SubmitCallback() error = %v", errSubmit)
	}

	delivered, errAwait := store.AwaitCallback(context.Background(), "callback-state")
	if errAwait != nil || delivered != callback {
		t.Fatalf("first AwaitCallback() = %#v, %v; want %#v, nil", delivered, errAwait, callback)
	}
	if _, errAwait := store.AwaitCallback(context.Background(), "callback-state"); !errors.Is(errAwait, errOAuthSessionNotPending) {
		t.Fatalf("second AwaitCallback() error = %v, want not pending", errAwait)
	}
}

func TestSubmitOAuthCallbackRacesWithTerminalTransitions(t *testing.T) {
	for i := 0; i < 64; i++ {
		store := newOAuthSessionStore(time.Minute)
		store.Register("callback-state", "codex")
		result := awaitOAuthCallback(t, store, "callback-state")
		waitForOAuthCallbackWaiter(t, store, "callback-state")

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = store.SubmitCallback(OAuthCallback{State: "callback-state", Code: "test-code"})
		}()
		go func() {
			defer wg.Done()
			store.Cancel("callback-state")
		}()
		wg.Wait()

		if errAwait := <-result; errAwait != nil && !errors.Is(errAwait, errOAuthSessionNotPending) {
			t.Fatalf("AwaitCallback() error = %v, want callback or not pending", errAwait)
		}
	}
}

func TestOAuthSessionStoreSwapDoesNotCrossDeliverCapturedFlow(t *testing.T) {
	first := newOAuthSessionStore(time.Minute)
	replaceOAuthSessionStoreForTest(t, first)
	flowStore := registerOAuthSessionForFlow("shared-state", "codex")
	result := awaitOAuthCallback(t, flowStore, "shared-state")
	waitForOAuthCallbackWaiter(t, flowStore, "shared-state")

	second := newOAuthSessionStore(time.Minute)
	swapOAuthSessionStore(second)
	second.Register("shared-state", "codex")
	handler := &Handler{}
	if errSubmit := handler.SubmitOAuthCallback(OAuthCallback{State: "shared-state", Code: "second-code"}); errSubmit != nil {
		t.Fatalf("SubmitOAuthCallback() into replacement store error = %v", errSubmit)
	}

	select {
	case errAwait := <-result:
		t.Fatalf("captured waiter completed from replacement store: %v", errAwait)
	case <-time.After(20 * time.Millisecond):
	}

	if errSubmit := flowStore.SubmitCallback(OAuthCallback{State: "shared-state", Code: "first-code"}); errSubmit != nil {
		t.Fatalf("SubmitCallback() into captured store error = %v", errSubmit)
	}
	if errAwait := <-result; errAwait != nil {
		t.Fatalf("captured AwaitCallback() error = %v", errAwait)
	}
}

func awaitOAuthCallback(t *testing.T, store *oauthSessionStore, state string) <-chan error {
	t.Helper()
	result := make(chan error, 1)
	go func() {
		_, errAwait := store.AwaitCallback(context.Background(), state)
		result <- errAwait
	}()
	return result
}

func waitForOAuthCallbackWaiter(t *testing.T, store *oauthSessionStore, state string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		store.mu.RLock()
		waiting := store.sessions[state].callbackAwaiting
		store.mu.RUnlock()
		if waiting {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("AwaitCallback(%q) did not start waiting", state)
}

func replaceOAuthSessionStoreForTest(t *testing.T, store *oauthSessionStore) {
	t.Helper()
	original := swapOAuthSessionStore(store)
	t.Cleanup(func() {
		swapOAuthSessionStore(original)
	})
}
