package nativeusage

import (
	"sync"
	"testing"

	sdk "github.com/MichaelKinsy/PiG/extensions/sdk"
	"github.com/danruto/pig-plugins/extensions/pig-status/internal/usage"
)

type publishedReasons struct {
	mu      sync.Mutex
	reasons []string
}

func (p *publishedReasons) publish(view usage.CachedUsage) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reasons = append(p.reasons, view.Reason)
}

func (p *publishedReasons) since(mark int) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.reasons[mark:]...)
}

func (p *publishedReasons) mark() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.reasons)
}

func tickUntilFirstHostCall(runtime *Runtime, current *owner) {
	defer func() { _ = recover() }()
	_ = runtime.tick(sdk.Context{}, current)
}

func TestSuccessorTickNeverPublishesPredecessorFailureReason(t *testing.T) {
	host, transport, published := newSeedHost(), &seedTransport{}, &publishedReasons{}
	runtime, err := Register(sdk.New("lifecycle"), Options{Client: usage.Client{Transport: transport}, Publish: published.publish})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	if err := runtime.start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	predecessorReason := seedFailureBackoff(t, runtime, host, transport).Reason
	if predecessorReason == "" {
		t.Fatal("seeded failure recorded no reason")
	}
	_ = runtime.start()
	successor := ownedOwner(t, runtime)
	mark := published.mark()

	tickUntilFirstHostCall(runtime, successor)

	for _, reason := range published.since(mark) {
		if reason == predecessorReason {
			t.Fatalf("successor published the predecessor's reason %q before its own refresh; published %q", predecessorReason, published.since(mark))
		}
	}
}
