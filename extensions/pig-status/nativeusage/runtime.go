// Package nativeusage stages owned native collection against the reviewed host SDK.
// Register embeds collection in a conventional factory; Publish is an in-process,
// prompt, cache-only sink and must not retain SDK contexts. Optional Repaint receives the
// live host context after each publish made in a session handler or a current owner's
// tick, and must use it only before returning. No usage bus API is exposed.
// Each private tick receives a fresh awaited host context; the scheduler and every
// listener are cancelled and joined before replacement. Contexts never leave a listener.
// One exception: the readiness emit is a host-owned event-bus dispatch the host
// cancels only at teardown, so it is never joined. Collection waits for it only
// until the request timeout or its context ends, and a superseded owner publishes
// nothing from it. A bridge emission failure publishes one cleared view carrying a
// fixed sanitized reason for the length of the failure streak; the scheduler stays
// alive and retries on the next wake or after the failure backoff. An owner publishes
// nothing before its own first completed refresh.
package nativeusage

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"maps"
	"sync"
	"sync/atomic"
	"time"

	sdk "github.com/MichaelKinsy/PiG/extensions/sdk"
	"github.com/danruto/pig-plugins/extensions/pig-status/internal/usage"
)

type Options struct {
	Client   usage.Client
	Settings usage.CollectionSettings
	Publish  func(usage.CachedUsage)
	Repaint  func(sdk.Context)
}

type Runtime struct {
	lifecycle sync.Mutex
	mu        sync.Mutex
	ext       *sdk.Extension
	collector *usage.Collector
	settings  usage.CollectionSettings
	applied   usage.CollectionSettings
	publish   func(usage.CachedUsage)
	repaint   func(sdk.Context)
	owner     *owner
	active    bool
	closed    bool
}

type owner struct {
	stop      chan struct{}
	done      chan struct{}
	wake      chan struct{}
	listeners sync.WaitGroup
	off       func()
	err       error
	reason    string
	refresh   atomic.Bool
	refreshed atomic.Bool
}

var errBridge = errors.New("native usage host bridge failed")

const bridgeFailureReason = "host-bridge-failed"
const bridgeFailureBackoff = 30 * time.Second
const scheduledRefreshTimeout = 5 * time.Second

func Register(ext *sdk.Extension, options Options) (*Runtime, error) {
	if options.Publish == nil {
		return nil, errors.New("native usage requires an in-process cache sink")
	}
	r := &Runtime{ext: ext, collector: usage.NewCollector(options.Client), settings: copySettings(options.Settings), publish: options.Publish, repaint: options.Repaint}
	r.applySettings()
	ext.OnSessionStart(r.onSession(r.start))
	for _, event := range []string{"model_select", "session_tree"} {
		ext.OnEvent(event, r.onSession(r.start))
	}
	ext.OnEvent("turn_end", func(_ sdk.Context, _ map[string]any) (any, error) { r.wake(); return nil, nil })
	ext.OnSessionShutdown(r.onSession(r.stop))
	return r, nil
}

func (r *Runtime) onSession(change func() error) sdk.EventFunc {
	return func(call sdk.Context, _ map[string]any) (any, error) {
		err := change()
		r.repaintWith(call)
		return nil, err
	}
}

func (r *Runtime) repaintWith(call sdk.Context) {
	if r.repaint != nil && call.Err() == nil {
		r.repaint(call)
	}
}

func copySettings(settings usage.CollectionSettings) usage.CollectionSettings {
	settings.Targets = maps.Clone(settings.Targets)
	return settings
}

func (r *Runtime) Configure(settings usage.CollectionSettings) error {
	r.lifecycle.Lock()
	defer r.lifecycle.Unlock()
	r.settings = copySettings(settings)
	predecessor := r.drain()
	r.applySettings()
	if r.active && !r.closed {
		return errors.Join(r.launch(), predecessorError(predecessor))
	}
	return predecessorError(predecessor)
}

func (r *Runtime) applySettings() {
	if maps.Equal(r.settings.Targets, r.applied.Targets) && r.settings.UseCodexUsage == r.applied.UseCodexUsage {
		return
	}
	r.collector.Configure(r.settings)
	r.applied = copySettings(r.settings)
}

func (r *Runtime) start() error {
	r.lifecycle.Lock()
	defer r.lifecycle.Unlock()
	if r.closed {
		return nil
	}
	predecessor := r.drain()
	r.active = true
	return errors.Join(r.launch(), predecessorError(predecessor))
}

func (r *Runtime) stop() error {
	r.lifecycle.Lock()
	defer r.lifecycle.Unlock()
	r.active = false
	return r.drain()
}

func (r *Runtime) Close() error {
	r.lifecycle.Lock()
	defer r.lifecycle.Unlock()
	r.active, r.closed = false, true
	return r.drain()
}

func (r *Runtime) drain() error {
	r.mu.Lock()
	previous := r.owner
	r.owner = nil
	r.mu.Unlock()
	if previous != nil {
		close(previous.stop)
		<-previous.done
		previous.listeners.Wait()
		previous.off()
	}
	r.publish(usage.CachedUsage{})
	if previous != nil {
		return previous.err
	}
	return nil
}

func predecessorError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("previous native usage owner: %w", err)
}

func (r *Runtime) launch() error {
	current := &owner{stop: make(chan struct{}), done: make(chan struct{}), wake: make(chan struct{}, 1)}
	channel := "native-usage-private-" + rand.Text()
	off, err := r.ext.Events().On(channel, func(call sdk.Context, _ any) error { return r.tick(call, current) })
	if err != nil {
		return errBridge
	}
	current.off = off
	r.mu.Lock()
	r.owner = current
	r.mu.Unlock()
	go r.schedule(current, channel)
	return nil
}

func (r *Runtime) wake() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.owner != nil {
		r.owner.refresh.Store(true)
		select {
		case r.owner.wake <- struct{}{}:
		default:
		}
	}
}

func (r *Runtime) schedule(current *owner, channel string) {
	defer close(current.done)
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-current.stop:
			return
		case <-current.wake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
		}
		select {
		case <-current.stop:
			return
		default:
		}
		if err := r.ext.Events().Emit(channel, nil); err != nil {
			current.err = errBridge
			r.publishBridgeFailure(current)
			timer.Reset(bridgeFailureBackoff)
			continue
		}
		timer.Reset(nextWake(r.collector.Snapshot(time.Now()), time.Now()))
	}
}

func nextWake(view usage.CachedUsage, now time.Time) time.Duration {
	next := now.Add(time.Minute)
	for _, deadline := range []time.Time{view.RefreshAt, view.ExpiresAt} {
		if !deadline.IsZero() && deadline.Before(next) {
			next = deadline
		}
	}
	for _, window := range view.Windows {
		if !window.ResetsAt.IsZero() && window.ResetsAt.Before(next) {
			next = window.ResetsAt
		}
	}
	return max(time.Millisecond, next.Sub(now))
}

func (r *Runtime) tick(call sdk.Context, current *owner) error {
	r.mu.Lock()
	if r.owner != current {
		r.mu.Unlock()
		return nil
	}
	current.listeners.Add(1)
	r.mu.Unlock()
	defer current.listeners.Done()
	ctx, cancel := context.WithCancel(context.Background())
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		select {
		case <-current.stop:
			cancel()
		case <-call.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	defer func() { cancel(); <-joined }()
	view := r.collector.Snapshot(time.Now())
	refreshed := current.refreshed.Load()
	if refreshed && view.ExpiresAt.IsZero() {
		r.publishCurrent(call, current, view)
	}
	if current.refresh.Swap(false) || !refreshed || view.RefreshAt.IsZero() || !time.Now().Before(view.RefreshAt) {
		refreshCtx, stopRefresh := context.WithTimeout(ctx, scheduledRefreshTimeout)
		err := r.collector.Refresh(refreshCtx, usage.Resolver{Host: Port(call)})
		stopRefresh()
		if err != nil && ctx.Err() != nil {
			return nil
		}
		current.refreshed.Store(true)
		view = r.collector.Snapshot(time.Now())
	}
	r.publishCurrent(call, current, view)
	return nil
}

func (r *Runtime) publishBridgeFailure(current *owner) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.owner != current || current.reason == bridgeFailureReason {
		return
	}
	current.reason = bridgeFailureReason
	r.publish(usage.CachedUsage{Reason: bridgeFailureReason})
}

func (r *Runtime) publishCurrent(call sdk.Context, current *owner, view usage.CachedUsage) {
	r.mu.Lock()
	if r.owner != current || call.Err() != nil {
		r.mu.Unlock()
		return
	}
	r.publish(view)
	notify := view.Reason != "" && view.Reason != current.reason
	current.reason = view.Reason
	r.mu.Unlock()
	r.repaintWith(call)
	if notify {
		call.Notify("Native usage unavailable: "+view.Reason, "warning")
	}
}
