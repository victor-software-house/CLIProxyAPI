package management

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestHandlerSetTokenStore_UsesInjectedStoreWithoutAffectingOtherHandlers(t *testing.T) {
	ctx := context.Background()
	defaultStore := &memoryAuthStore{}
	otherStore := &memoryAuthStore{}
	injectedStore := &memoryAuthStore{}
	h := &Handler{cfg: &config.Config{}, tokenStore: defaultStore}
	other := &Handler{cfg: &config.Config{}, tokenStore: otherStore}

	h.SetTokenStore(injectedStore)
	if _, err := h.saveTokenRecord(ctx, &coreauth.Auth{ID: "injected"}); err != nil {
		t.Fatalf("save injected token record: %v", err)
	}
	injectedRecords, err := injectedStore.List(ctx)
	if err != nil {
		t.Fatalf("list injected store after save: %v", err)
	}
	if len(injectedRecords) != 1 || injectedRecords[0].ID != "injected" {
		t.Fatalf("injected store records after save = %#v, want injected record", injectedRecords)
	}

	for name, store := range map[string]*memoryAuthStore{
		"default": defaultStore,
		"other":   otherStore,
	} {
		records, err := store.List(ctx)
		if err != nil {
			t.Fatalf("list %s store: %v", name, err)
		}
		if len(records) != 0 {
			t.Fatalf("%s store records = %d, want 0", name, len(records))
		}
	}

	if err := h.deleteTokenRecord(ctx, "injected"); err != nil {
		t.Fatalf("delete injected token record: %v", err)
	}
	injectedRecords, err = injectedStore.List(ctx)
	if err != nil {
		t.Fatalf("list injected store after delete: %v", err)
	}
	if len(injectedRecords) != 0 {
		t.Fatalf("injected store records after delete = %d, want 0", len(injectedRecords))
	}

	if got := other.tokenStoreWithBaseDir(); got != otherStore {
		t.Fatalf("other handler store = %T, want its original store", got)
	}
}

func TestHandlerSetTokenStore_RetainsDistinctStores(t *testing.T) {
	ctx := context.Background()
	firstStore := &memoryAuthStore{}
	secondStore := &memoryAuthStore{}
	first := &Handler{cfg: &config.Config{}}
	second := &Handler{cfg: &config.Config{}}

	first.SetTokenStore(firstStore)
	second.SetTokenStore(secondStore)
	if _, err := first.saveTokenRecord(ctx, &coreauth.Auth{ID: "first"}); err != nil {
		t.Fatalf("save first token record: %v", err)
	}
	if _, err := second.saveTokenRecord(ctx, &coreauth.Auth{ID: "second"}); err != nil {
		t.Fatalf("save second token record: %v", err)
	}

	firstRecords, err := firstStore.List(ctx)
	if err != nil {
		t.Fatalf("list first store: %v", err)
	}
	if len(firstRecords) != 1 || firstRecords[0].ID != "first" {
		t.Fatalf("first store records = %#v, want first record", firstRecords)
	}
	secondRecords, err := secondStore.List(ctx)
	if err != nil {
		t.Fatalf("list second store: %v", err)
	}
	if len(secondRecords) != 1 || secondRecords[0].ID != "second" {
		t.Fatalf("second store records = %#v, want second record", secondRecords)
	}
}

func TestHandlerSetTokenStore_NilHandlerIsSafe(t *testing.T) {
	var h *Handler
	h.SetTokenStore(&memoryAuthStore{})
}

func TestHandlerSetTokenStore_NilRestoresLazyDefault(t *testing.T) {
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, nil)
	initialStore := h.resolveTokenStore()
	injectedStore := &memoryAuthStore{}

	h.SetTokenStore(injectedStore)
	if got := h.resolveTokenStore(); got != injectedStore {
		t.Fatalf("injected store = %T, want injected store", got)
	}
	h.SetTokenStore(nil)
	if got := h.resolveTokenStore(); got != initialStore {
		t.Fatalf("restored store = %T, want initial default store", got)
	}
}

func TestHandlerSetTokenStore_HooksCanReenter(t *testing.T) {
	initialStore := &memoryAuthStore{}
	preHookStore := &memoryAuthStore{}
	postHookStore := &memoryAuthStore{}
	h := &Handler{cfg: &config.Config{}, tokenStore: initialStore}
	h.SetPostAuthHook(func(context.Context, *coreauth.Auth) error {
		h.SetTokenStore(preHookStore)
		return nil
	})
	h.SetPostAuthPersistHook(func(context.Context, *coreauth.Auth) error {
		h.SetTokenStore(postHookStore)
		return nil
	})

	done := make(chan error, 1)
	go func() {
		_, err := h.saveTokenRecord(context.Background(), &coreauth.Auth{ID: "hook-reentry"})
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("save token record: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("save token record deadlocked when hooks reset token store")
	}
	if got := h.resolveTokenStore(); got != postHookStore {
		t.Fatalf("handler store = %T, want post-hook store", got)
	}
}

func TestHandlerSetTokenStore_ConcurrentWithTokenPersistence(t *testing.T) {
	ctx := context.Background()
	firstStore := &memoryAuthStore{}
	secondStore := &memoryAuthStore{}
	h := &Handler{cfg: &config.Config{}, tokenStore: firstStore}

	var wg sync.WaitGroup
	for _, store := range []coreauth.Store{firstStore, secondStore} {
		wg.Add(1)
		go func(store coreauth.Store) {
			defer wg.Done()
			for range 100 {
				h.SetTokenStore(store)
			}
		}(store)
	}
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			for range 100 {
				if _, err := h.saveTokenRecord(ctx, &coreauth.Auth{ID: id}); err != nil {
					t.Errorf("save token record: %v", err)
					return
				}
				if err := h.deleteTokenRecord(ctx, id); err != nil {
					t.Errorf("delete token record: %v", err)
					return
				}
				h.tokenStoreWithBaseDir()
			}
		}(string(rune('a' + i)))
	}
	wg.Wait()
}
