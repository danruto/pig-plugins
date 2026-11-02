// Package pighermes stages native Hermes configuration and shared-schema initialization.
// The factory remains outside installed extension membership until the remaining safety gates qualify it.
package pighermes

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	sdk "github.com/MichaelKinsy/PiG/extensions/sdk"

	"github.com/danruto/pig-plugins/extensions/pig-hermes/internal/config"
	"github.com/danruto/pig-plugins/extensions/pig-hermes/internal/paths"
	"github.com/danruto/pig-plugins/extensions/pig-hermes/internal/store"
)

type storageOwner struct {
	mu       sync.Mutex
	db       *sql.DB
	memory   *store.Memory
	settings config.Settings
	paths    paths.Resolved
}

func Extension() *sdk.Extension {
	ext := sdk.New("pig-hermes")
	owner := &storageOwner{}
	ext.OnSessionStart(func(ctx sdk.Context, _ map[string]any) (any, error) {
		return nil, owner.start(requestContext{Context: ctx}, ctx.Cwd())
	})
	ext.OnSessionShutdown(func(_ sdk.Context, _ map[string]any) (any, error) {
		owner.mu.Lock()
		defer owner.mu.Unlock()
		return nil, owner.close()
	})
	return ext
}

type requestContext struct{ sdk.Context }

func (requestContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (requestContext) Value(any) any               { return nil }

func environment(cwd string) (paths.Environment, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return paths.Environment{}, "", err
	}
	pigHome := os.Getenv("PIG_HOME")
	if pigHome == "" {
		pigHome = filepath.Join(home, ".pig")
	}
	pigAgent := os.Getenv("PIG_CODING_AGENT_DIR")
	if pigAgent == "" {
		pigAgent = filepath.Join(pigHome, "agent")
	}
	env := paths.Environment{
		Home: home, Cwd: cwd, AgentRoot: os.Getenv("PI_CODING_AGENT_DIR"), PiGAgentRoot: pigAgent,
		PiGSessionRoot: os.Getenv("PIG_CODING_AGENT_SESSION_DIR"),
		PiSessionRoot:  os.Getenv("PI_CODING_AGENT_SESSION_DIR"),
	}
	path := os.Getenv("PIG_HERMES_CONFIG")
	if path == "" {
		path = filepath.Join(paths.NativeAgentRoot(env), "hermes-memory-config.json")
	}
	if !filepath.IsAbs(path) {
		return paths.Environment{}, "", errors.New("native configuration path must be absolute")
	}
	return env, path, nil
}

func (o *storageOwner) start(ctx context.Context, cwd string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.close(); err != nil {
		return errors.New("hermes: previous database close failed")
	}
	env, path, err := environment(cwd)
	if err != nil {
		return errors.New("hermes: environment resolution failed")
	}
	settings, err := config.Load(path)
	if err != nil {
		return errors.New("hermes: invalid native configuration")
	}
	resolved, err := paths.Resolve(settings, env)
	if err != nil {
		return errors.New("hermes: storage path resolution failed")
	}
	db, err := store.Open(ctx, resolved.Database)
	if err != nil {
		return errors.New("hermes: schema initialization failed")
	}
	memory, err := store.NewMemory(db, settings, resolved)
	if err != nil {
		if closeErr := db.Close(); closeErr != nil {
			return errors.New("hermes: memory initialization and database close failed")
		}
		return errors.New("hermes: memory initialization failed")
	}
	o.settings, o.paths, o.db, o.memory = settings, resolved, db, memory
	return nil
}

func (o *storageOwner) close() error {
	if o.db == nil {
		return nil
	}
	err := o.db.Close()
	o.db, o.memory = nil, nil
	return err
}
