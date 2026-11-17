package nativeusage

import (
	"context"
	"crypto/rand"
	"sync"

	sdk "github.com/MichaelKinsy/PiG/extensions/sdk"
)

type ownedWork struct {
	stopMu     sync.Mutex
	mu         sync.Mutex
	ext        *sdk.Extension
	channel    string
	stopSignal chan struct{}
	pending    map[string]func(context.Context)
	running    sync.WaitGroup
	registered bool
	stopping   bool
}

func newOwnedWork(ext *sdk.Extension) *ownedWork {
	return &ownedWork{
		ext:        ext,
		channel:    "native-usage-work-" + rand.Text(),
		stopSignal: make(chan struct{}),
		pending:    map[string]func(context.Context){},
	}
}

func (w *ownedWork) spawn(run func(context.Context)) {
	if err := w.listen(); err != nil {
		return
	}
	id := rand.Text()
	w.mu.Lock()
	if w.stopping {
		w.mu.Unlock()
		return
	}
	w.pending[id] = run
	w.running.Add(1)
	w.mu.Unlock()
	go func() {
		defer w.running.Done()
		if err := w.ext.Events().Emit(w.channel, map[string]any{"id": id}); err != nil {
			w.drop(id)
		}
	}()
}

func (w *ownedWork) stop() {
	w.stopMu.Lock()
	defer w.stopMu.Unlock()
	w.mu.Lock()
	w.stopping = true
	current := w.stopSignal
	w.pending = map[string]func(context.Context){}
	w.mu.Unlock()
	close(current)
	w.running.Wait()
	w.mu.Lock()
	w.stopping = false
	w.stopSignal = make(chan struct{})
	w.mu.Unlock()
}

func (w *ownedWork) listen() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.registered {
		return nil
	}
	if _, err := w.ext.Events().On(w.channel, w.dispatch); err != nil {
		return err
	}
	w.registered = true
	return nil
}

func (w *ownedWork) dispatch(call sdk.Context, payload any) error {
	id, ok := workID(payload)
	if !ok {
		return nil
	}
	run, stop, found := w.take(id)
	if !found {
		return nil
	}
	ctx, done := callContext(call)
	defer done()
	cancelled, release := watchedContext(ctx, stop)
	defer release()
	run(cancelled)
	return nil
}

func (w *ownedWork) take(id string) (func(context.Context), <-chan struct{}, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	run, found := w.pending[id]
	if !found {
		return nil, nil, false
	}
	delete(w.pending, id)
	return run, w.stopSignal, true
}

func (w *ownedWork) drop(id string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.pending, id)
}

func workID(payload any) (string, bool) {
	body, ok := payload.(map[string]any)
	if !ok {
		return "", false
	}
	id, ok := body["id"].(string)
	return id, ok
}
