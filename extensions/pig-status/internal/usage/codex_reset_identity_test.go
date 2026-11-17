package usage

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

const (
	acceptResetStallTimeout = 50 * time.Millisecond
	acceptResetStallCap     = 2 * time.Second
)

func acceptResetStall(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func acceptResetStallScope(ctx context.Context, _ FastScope) (FastScope, error) {
	return FastScope{}, acceptResetStall(ctx)
}

func acceptResetLiveScope(_ context.Context, scope FastScope) (FastScope, error) {
	return scope, nil
}

func acceptResetStallIdentity(ctx context.Context, _ CodexAccount) (CodexAccount, error) {
	return CodexAccount{}, acceptResetStall(ctx)
}

func acceptResetLiveIdentity(_ context.Context, account CodexAccount) (CodexAccount, error) {
	return account, nil
}

func acceptResetStallRevalidateIdentity(_ context.Context, _ CodexAccount) (CodexAccount, error) {
	return acceptResetAccountWithRevalidate(acceptResetStall), nil
}

func acceptResetStallRevalidateWhenArmed(armed *atomic.Bool) func(context.Context, CodexAccount) (CodexAccount, error) {
	return func(ctx context.Context, account CodexAccount) (CodexAccount, error) {
		if armed.Load() {
			return acceptResetStallRevalidateIdentity(ctx, account)
		}
		return account, nil
	}
}

func acceptResetStalledNew(
	t *testing.T,
	current func(context.Context, FastScope) (FastScope, error),
	identify func(context.Context, CodexAccount) (CodexAccount, error),
) *acceptResetFixture {
	t.Helper()
	fixture := acceptResetNew(t, nil)
	fixture.resets = NewCodexResets(CodexResetsOptions{
		Client:   Client{Transport: fixture.server},
		Current:  func(ctx context.Context) (FastScope, error) { return current(ctx, fixture.identity.scope) },
		Identify: func(ctx context.Context) (CodexAccount, error) { return identify(ctx, fixture.identity.account) },
		Timeout:  acceptResetStallTimeout,
	})
	return fixture
}

func acceptResetRunBounded(t *testing.T, cap time.Duration, run func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- run() }()
	select {
	case err := <-done:
		return err
	case <-time.After(cap):
		t.Fatalf("Run did not return within %v, the hang guard cap, after a stalled identity call", cap)
		return nil
	}
}

func TestCodexResetsRunReportsAChangedProviderBeforeConsuming(t *testing.T) {
	fixture := acceptResetNew(t, nil)
	ui := acceptResetScript(fixture, func(index int) {
		if index == 1 {
			fixture.identity.scope.Model.Provider = "deepseek"
		}
	},
		acceptSelection{choice: acceptResetLabel(t, "Full reset", acceptResetExpires), ok: true},
		acceptSelection{choice: ResetActionConfirm, ok: true},
	)

	err := fixture.resets.Run(t.Context(), ui)

	acceptEqual(t, []string{"The active Codex model or account changed; the reset was not used."}, fixture.ui.notices, "changed provider notice")
	acceptEqual(t, 2, len(fixture.ui.titles), "prompts")
	acceptEqual(t, []string{ResetActionConfirm, ResetActionCancel}, fixture.ui.optionSets[1], "confirmation options")
	acceptEqual(t, 1, len(fixture.server.callsByMethod(http.MethodGet)), "list requests")
	acceptEqual(t, 0, len(fixture.server.callsByMethod(http.MethodPost)), "consume requests")
	acceptNoError(t, err, "changed provider before consuming")
	acceptResetSafe(t, fixture.ui, acceptResetToken, acceptResetAccountID, acceptResetCreditID)
}

func TestCodexResetsRunReportsAnUnreadableScopeBeforeConsuming(t *testing.T) {
	var unreadable bool
	fixture := acceptResetNew(t, nil)
	fixture.resets = NewCodexResets(CodexResetsOptions{
		Client: Client{Transport: fixture.server},
		Current: func(context.Context) (FastScope, error) {
			if unreadable {
				return FastScope{}, errors.New("raw-provider-canary " + acceptResetToken)
			}
			return fixture.identity.scope, nil
		},
		Identify: func(context.Context) (CodexAccount, error) { return fixture.identity.account, nil },
		Timeout:  acceptQueryTimeout,
	})
	ui := acceptResetScript(fixture, func(index int) {
		if index == 1 {
			unreadable = true
		}
	},
		acceptSelection{choice: acceptResetLabel(t, "Full reset", acceptResetExpires), ok: true},
		acceptSelection{choice: ResetActionConfirm, ok: true},
	)

	err := fixture.resets.Run(t.Context(), ui)

	acceptEqual(t, []string{"The active Codex model or account changed; the reset was not used."}, fixture.ui.notices, "unreadable scope notice")
	acceptEqual(t, 2, len(fixture.ui.titles), "prompts")
	acceptEqual(t, []string{ResetActionConfirm, ResetActionCancel}, fixture.ui.optionSets[1], "confirmation options")
	acceptEqual(t, 1, len(fixture.server.callsByMethod(http.MethodGet)), "list requests")
	acceptEqual(t, 0, len(fixture.server.callsByMethod(http.MethodPost)), "consume requests")
	acceptNoError(t, err, "unreadable scope before consuming")
	acceptResetSafe(t, fixture.ui, acceptResetToken, acceptResetAccountID, acceptResetCreditID, "raw-provider-canary")
}

func TestCodexResetsRunReportsAStalledIdentityBeforeListing(t *testing.T) {
	for _, tt := range []struct {
		name     string
		current  func(context.Context, FastScope) (FastScope, error)
		identify func(context.Context, CodexAccount) (CodexAccount, error)
	}{
		{name: "current stalls on the first read", current: acceptResetStallScope, identify: acceptResetLiveIdentity},
		{name: "identify stalls on the first read", current: acceptResetLiveScope, identify: acceptResetStallIdentity},
		{name: "revalidate stalls before listing", current: acceptResetLiveScope, identify: acceptResetStallRevalidateIdentity},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := acceptResetStalledNew(t, tt.current, tt.identify)
			ui := acceptResetScript(fixture, nil, acceptSelection{ok: false})

			err := acceptResetRunBounded(t, acceptResetStallCap, func() error {
				return fixture.resets.Run(t.Context(), ui)
			})

			acceptEqual(t, 1, len(fixture.ui.notices), "notices")
			acceptEqual(t, []string{"error"}, fixture.ui.levels, "notice level")
			acceptNotEqual(t, "", fixture.ui.notices[0], "notice text")
			acceptEqual(t, 0, len(fixture.ui.titles), "prompts")
			acceptEqual(t, 0, len(fixture.server.callsByMethod(http.MethodGet)), "list requests")
			acceptEqual(t, 0, len(fixture.server.callsByMethod(http.MethodPost)), "consume requests")
			acceptNoError(t, err, "stalled identity before listing")
			acceptResetSafe(t, fixture.ui, acceptResetToken, acceptResetAccountID)
		})
	}
}

func TestCodexResetsRunRetriesWhenTheAccountRevalidationStallsBeforeConsuming(t *testing.T) {
	var armed atomic.Bool
	fixture := acceptResetStalledNew(t, acceptResetLiveScope, acceptResetStallRevalidateWhenArmed(&armed))
	ui := acceptResetScript(fixture, func(index int) {
		if index == 1 {
			armed.Store(true)
		}
	},
		acceptSelection{choice: acceptResetLabel(t, "Full reset", acceptResetExpires), ok: true},
		acceptSelection{choice: ResetActionConfirm, ok: true},
		acceptSelection{choice: ResetActionBack, ok: true},
	)

	err := acceptResetRunBounded(t, acceptResetStallCap, func() error {
		return fixture.resets.Run(t.Context(), ui)
	})

	acceptNoError(t, err, "abandoned retry")
	acceptEqual(t, 3, len(fixture.ui.titles), "prompts")
	acceptPrefix(t, fixture.ui.titles[2], "Couldn't reset usage: ", "retry prompt")
	acceptContains(t, fixture.ui.titles[2], "the account could not be verified", "retry prompt")
	acceptContains(t, fixture.ui.titles[2], "Try again with the same request.", "retry prompt")
	acceptEqual(t, []string{ResetActionRetry, ResetActionBack}, fixture.ui.optionSets[2], "retry options")
	acceptEqual(t, 1, len(fixture.server.callsByMethod(http.MethodGet)), "list requests")
	acceptEqual(t, 0, len(fixture.server.callsByMethod(http.MethodPost)), "consume requests")
	acceptResetSafe(t, fixture.ui, acceptResetToken, acceptResetAccountID, acceptResetCreditID)
}
