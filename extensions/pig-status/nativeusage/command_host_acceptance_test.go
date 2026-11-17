package nativeusage_test

import (
	"testing"
)

func TestFastCommandReadsTheHostOnlyThroughItsOwnRequest(t *testing.T) {
	host := startWireHost(t)
	serveCodexModel(host)
	host.exchange(t, "codex-session", sessionStartRequest(), holdNoCall)

	host.whileHeld = func() {
		hook := host.exchange(t, "streamed-turn", streamingTurnRequest(), holdNoCall)
		if !hook.ReturnedWhileACallStayedHeld {
			t.Fatalf("the streaming hook returned only after the /fast command resumed")
		}
		host.release(t)
	}
	fast := host.exchange(t, "fast-command", fastCommandRequest(), holdAnyCall)

	host.requireOnlyCallsOf(t, fast, fast.HeldIndex, "fast-command")
}

func TestCodexSessionEventsReturnWithoutWaitingOnTheRefresh(t *testing.T) {
	tests := []struct {
		name    string
		request map[string]any
	}{
		{name: "session start reads the account only after returning", request: sessionStartRequest()},
		{name: "model select reads the account only after returning", request: modelSelectRequest()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host := startWireHost(t)
			serveCodexModel(host)

			host.exchange(t, "codex-session", tt.request, holdCodexAccountRead)
			calls := host.awaitCall(t, "codex-session", "getProviderAuth")

			if !hasMethod(calls, "getProviderAuth") && !hasMethod(calls, "getModelAuth") {
				t.Fatalf("%s never read the Codex account after returning to the host", tt.name)
			}
		})
	}
}
