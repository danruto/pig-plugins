package usage

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

const (
	acceptFastFirstAccountID  = "synthetic-account-first"
	acceptFastSecondAccountID = "synthetic-account-second"
	acceptFastTargetAccountID = "synthetic-account-target"
	acceptFastOtherAccountID  = "synthetic-account-other"
	acceptFastUnfetchedReason = "Codex Fast capability information has not been fetched for this account."
	acceptFastShortTimeout    = 50 * time.Millisecond
)

func acceptFastAccountWithID(id string) CodexAccount {
	return CodexAccount{AccountID: id, Headers: http.Header{"Authorization": {"Bearer " + id}, "ChatGPT-Account-Id": {id}}}
}

type acceptFastAccountSwitch struct {
	mu      sync.Mutex
	current CodexAccount
}

func (s *acceptFastAccountSwitch) identify(context.Context) (CodexAccount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current, nil
}

func (s *acceptFastAccountSwitch) activate(account CodexAccount) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current = account
}

func acceptFastAccountDirectory(bodies map[string]string, blocked string, release <-chan struct{}, entered chan<- struct{}) http.RoundTripper {
	var once sync.Once
	return queryRoundTripper(func(request *http.Request) (*http.Response, error) {
		account := acceptFastHeaderValue(request.Header, "ChatGPT-Account-Id")
		if account == blocked {
			once.Do(func() { close(entered) })
			<-release
		}
		return acceptFastResponse(http.StatusOK, bodies[account]), nil
	})
}

func acceptFastBlockedDirectory() http.RoundTripper {
	return queryRoundTripper(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
}

func TestFastCapabilityReadIsFencedToItsOwnAccount(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		first := acceptFastAccountWithID(acceptFastFirstAccountID)
		second := acceptFastAccountWithID(acceptFastSecondAccountID)
		accounts := &acceptFastAccountSwitch{}
		accounts.activate(first)
		release := make(chan struct{})
		closeRelease := sync.OnceFunc(func() { close(release) })
		t.Cleanup(closeRelease)
		entered := make(chan struct{})
		fixture := acceptFastNew(t, "", acceptFastAccountDirectory(map[string]string{
			first.AccountID:  acceptFastSupportedDirectory,
			second.AccountID: acceptFastUnsupportedDirectory,
		}, first.AccountID, release, entered), func(options *CodexFastOptions) {
			options.Identify = accounts.identify
		})
		results := make(chan FastCapability, 1)

		go func() {
			capability, _ := fixture.fast.RefreshCapabilities(t.Context(), true)
			results <- capability
		}()
		<-entered
		accounts.activate(second)
		secondCapability, secondOK := fixture.fast.RefreshCapabilities(t.Context(), true)
		closeRelease()
		firstCapability := <-results

		acceptEqual(t, true, secondOK, "capability refresh for the second account")
		acceptEqual(t, FastUnsupported, secondCapability.Kind, "capability for the second account")
		acceptNotEqual(t, FastUnsupported, firstCapability.Kind, "capability for the first account")
	})
}

func TestFastCommandDoesNotEnableFastFromAnotherAccountsCapability(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		target := acceptFastAccountWithID(acceptFastTargetAccountID)
		other := acceptFastAccountWithID(acceptFastOtherAccountID)
		accounts := &acceptFastAccountSwitch{}
		accounts.activate(target)
		release := make(chan struct{})
		closeRelease := sync.OnceFunc(func() { close(release) })
		t.Cleanup(closeRelease)
		entered := make(chan struct{})
		fixture := acceptFastNew(t, "", acceptFastAccountDirectory(map[string]string{
			target.AccountID: acceptFastUnsupportedDirectory,
			other.AccountID:  acceptFastSupportedDirectory,
		}, target.AccountID, release, entered), func(options *CodexFastOptions) {
			options.Identify = accounts.identify
		})
		enabled := make(chan error, 1)

		go func() { enabled <- fixture.fast.Run(t.Context(), fixture.ui, "") }()
		<-entered
		accounts.activate(other)
		_, otherOK := fixture.fast.RefreshCapabilities(t.Context(), true)
		closeRelease()
		err := <-enabled

		acceptNoError(t, err, "enable attempt")
		acceptEqual(t, true, otherOK, "capability refresh for the activating account")
		acceptEqual(t, false, fixture.store.Get().Settings.CodexFastMode, "stored fast mode")
		acceptNoFile(t, fixture.path, "settings file after a refused enable")
		acceptEqual(t, []string{"warning"}, fixture.ui.levels, "notice levels")
		rewritten, rewrittenOK := fixture.fast.BeforeProviderRequest(t.Context(), map[string]any{"model": acceptFastModelID, "input": []any{}})
		acceptEqual(t, true, rewrittenOK, "payload rewrite")
		acceptEqual(t, "default", rewritten["service_tier"], "request tier")
	})
}

func TestFastAvailabilityReportsDirectoryTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fixture := acceptFastNew(t, "", acceptFastBlockedDirectory(), func(options *CodexFastOptions) {
			options.Timeout = acceptFastShortTimeout
		})

		fixture.fast.RefreshCapabilities(t.Context(), true)
		available := fixture.fast.Availability(acceptFastModel(acceptFastModelID))

		acceptEqual(t, FastUnknown, available.Kind, "availability kind")
		acceptPrefix(t, available.Reason, "Codex Fast capability is unknown: ", "availability reason")
		acceptNotEqual(t, acceptFastUnfetchedReason, available.Reason, "availability reason after a directory timeout")
		requireSafeText(t, available.Reason)
	})
}

func TestFastDirectoryTimeoutStartsTheFailureBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fixture := acceptFastNew(t, "", acceptFastBlockedDirectory(), func(options *CodexFastOptions) {
			options.Timeout = acceptFastShortTimeout
		})

		fixture.fast.RefreshCapabilities(t.Context(), true)
		fixture.clock.Advance(10 * time.Second)
		backedOff, _ := fixture.fast.RefreshCapabilities(t.Context(), false)

		acceptEqual(t, FastUnknown, backedOff.Kind, "capability inside the backoff")
		acceptEqual(t, 1, int(fixture.requests.Load()), "directory requests inside the backoff")
		fixture.clock.Advance(40 * time.Second)
		fixture.fast.RefreshCapabilities(t.Context(), false)

		acceptEqual(t, 2, int(fixture.requests.Load()), "directory requests after the backoff")
	})
}

func TestFastDirectoryTimeoutKeepsTheLastKnownModels(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		fixture := acceptFastNew(t, "", queryRoundTripper(func(request *http.Request) (*http.Response, error) {
			if calls.Add(1) == 1 {
				return acceptFastResponse(http.StatusOK, acceptFastSupportedDirectory), nil
			}
			<-request.Context().Done()
			return nil, request.Context().Err()
		}), func(options *CodexFastOptions) {
			options.Timeout = acceptFastShortTimeout
		})
		acceptEqual(t, FastSupported, fixture.warmCapability(t).Kind, "warm capability")
		fixture.clock.Advance(6 * time.Minute)

		capability, _ := fixture.fast.RefreshCapabilities(t.Context(), false)

		acceptEqual(t, FastSupported, capability.Kind, "capability after a directory timeout")
		acceptEqual(t, 2, int(fixture.requests.Load()), "directory requests")
	})
}

func TestFastCallerCancellationLeavesNoDirectoryBackoff(t *testing.T) {
	var calls atomic.Int32
	entered := make(chan struct{})
	fixture := acceptFastNew(t, "", queryRoundTripper(func(request *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-request.Context().Done()
			return nil, request.Context().Err()
		}
		return acceptFastResponse(http.StatusOK, acceptFastSupportedDirectory), nil
	}), nil)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	results := make(chan bool, 1)

	go func() {
		_, ok := fixture.fast.RefreshCapabilities(ctx, true)
		results <- ok
	}()
	<-entered
	cancel()
	cancelledOK := <-results
	capability, ok := fixture.fast.RefreshCapabilities(t.Context(), false)

	acceptEqual(t, false, cancelledOK, "capability under a caller cancel")
	acceptEqual(t, true, ok, "capability refresh after a cancelled read")
	acceptEqual(t, FastSupported, capability.Kind, "capability after a cancelled read")
	acceptEqual(t, 2, int(fixture.requests.Load()), "directory requests after a cancelled read")
}
