<!-- GENERATED FROM CODE — DO NOT EDIT. Run bash scripts/hermes-recovery-reference.sh > docs/reference/hermes-recovery.md to regenerate. -->

# Staged Hermes recovery API

Signatures are extracted from the store package. See the staged recovery guide for ownership, accepted bounds, outcomes and qualification limits.

```go
package store // import "github.com/danruto/pig-plugins/extensions/pig-hermes/internal/store"

func OpenDatabase(ctx context.Context, path string, options RecoveryOptions) (*Database, error)
package store // import "github.com/danruto/pig-plugins/extensions/pig-hermes/internal/store"

type Database struct {
	// Has unexported fields.
}

func OpenDatabase(ctx context.Context, path string, options RecoveryOptions) (*Database, error)
func (d *Database) CheckIntegrity(ctx context.Context) error
func (d *Database) Close() error
func (d *Database) Do(ctx context.Context, operation func(*sql.DB) error) error
func (d *Database) Recover(ctx context.Context) (RecoveryResult, error)
func (d *Database) Status() DatabaseStatus
package store // import "github.com/danruto/pig-plugins/extensions/pig-hermes/internal/store"

type RecoveryOptions struct {
	Lock            RecoveryLockOptions
	CircuitLimit    int
	CircuitWindow   time.Duration
	BackupRetention *int
	Timeout         time.Duration
}

package store // import "github.com/danruto/pig-plugins/extensions/pig-hermes/internal/store"

type RecoveryResult struct {
	Strategy         string
	BackupPaths      []string
	RecoveredRows    map[string]int
	UnreadableTables []string
	Degraded         bool
}

package store // import "github.com/danruto/pig-plugins/extensions/pig-hermes/internal/store"

type RecoveryError struct {
	Result RecoveryResult
	Cause  error
}

func (e *RecoveryError) Error() string
func (e *RecoveryError) Unwrap() error
package store // import "github.com/danruto/pig-plugins/extensions/pig-hermes/internal/store"

type DatabaseStatus struct {
	Recovery          RecoveryResult
	IntegrityComplete bool
	Degraded          bool
	Reason            string
}

package store // import "github.com/danruto/pig-plugins/extensions/pig-hermes/internal/store"

var (
	ErrRecoveryCircuit  = errors.New("database recovery circuit is open")
	ErrDatabaseCorrupt  = errors.New("database integrity check failed")
	ErrRestoreFailed    = errors.New("database restore failed; backups retained")
	ErrDatabaseDegraded = errors.New("database is degraded; explicit recovery required")
)
package store // import "github.com/danruto/pig-plugins/extensions/pig-hermes/internal/store"

func NewManagedMemory(ctx context.Context, database *Database, settings config.Settings, resolved paths.Resolved) (*Memory, error)
package store // import "github.com/danruto/pig-plugins/extensions/pig-hermes/internal/store"

type Memory struct {
	// Has unexported fields.
}

func NewManagedMemory(ctx context.Context, database *Database, settings config.Settings, ...) (*Memory, error)
func NewMemory(db *sql.DB, settings config.Settings, resolved paths.Resolved) (*Memory, error)
func (m *Memory) Add(ctx context.Context, scope Scope, mutation Mutation) (MutationResult, error)
func (m *Memory) Apply(ctx context.Context, scope Scope, plan MutationPlan) (MutationResult, error)
func (m *Memory) Maintain(ctx context.Context) (MaintenanceResult, error)
func (m *Memory) Reconcile(ctx context.Context, scope Scope) (result MutationResult, err error)
func (m *Memory) Reload(ctx context.Context, scope Scope) ([]string, error)
func (m *Memory) Remove(ctx context.Context, scope Scope, mutation Mutation) (MutationResult, error)
func (m *Memory) Replace(ctx context.Context, scope Scope, mutation Mutation) (MutationResult, error)
package store // import "github.com/danruto/pig-plugins/extensions/pig-hermes/internal/store"

type MaintenanceResult struct {
	StoresMaintained int
	FailedStores     []string
}

```

## Recovery defaults and validation

```go
package store // import "github.com/danruto/pig-plugins/extensions/pig-hermes/internal/store"

func resolveRecoveryOptions(options RecoveryOptions) (RecoveryOptions, error) {
	if options.CircuitLimit == 0 {
		options.CircuitLimit = 3
	}
	if options.CircuitWindow == 0 {
		options.CircuitWindow = 300000 * time.Millisecond
	}
	if options.Timeout == 0 {
		options.Timeout = 30 * time.Second
	}
	if options.BackupRetention == nil {
		options.BackupRetention = new(3)
	} else {
		options.BackupRetention = new(*options.BackupRetention)
	}
	options.Lock = RecoveryLockOptions{cloneTiming(options.Lock.WaitMS), cloneTiming(options.Lock.PollMS), cloneTiming(options.Lock.StaleMS)}
	if options.CircuitLimit < 1 || options.CircuitWindow < 0 || options.Timeout < 0 || *options.BackupRetention < 0 {
		return options, errors.New("invalid recovery bounds")
	}
	return options, nil
}
```
