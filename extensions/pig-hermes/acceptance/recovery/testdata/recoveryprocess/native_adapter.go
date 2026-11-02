package main

import (
	"context"
	"database/sql"
	"time"

	"github.com/danruto/pig-plugins/extensions/pig-hermes/internal/config"
	"github.com/danruto/pig-plugins/extensions/pig-hermes/internal/paths"
	"github.com/danruto/pig-plugins/extensions/pig-hermes/internal/store"
)

type nativeRecovery struct {
	manager *store.Database
	path    string
	options store.RecoveryOptions
}

func newNativeRecovery(path string, req request) (*nativeRecovery, error) {
	options := store.RecoveryOptions{BackupRetention: req.Backups}
	if req.Limit != nil {
		options.CircuitLimit = *req.Limit
	}
	if req.WindowMS != nil {
		options.CircuitWindow = time.Duration(*req.WindowMS) * time.Millisecond
	}
	return &nativeRecovery{path: path, options: options}, nil
}

func (n *nativeRecovery) open(ctx context.Context) error {
	if n.manager != nil {
		return nil
	}
	manager, err := store.OpenDatabase(ctx, n.path, n.options)
	n.manager = manager
	return err
}
func (n *nativeRecovery) recover(ctx context.Context) error {
	if err := n.open(ctx); err != nil {
		return err
	}
	_, err := n.manager.Recover(ctx)
	return err
}
func (n *nativeRecovery) close() error {
	if n.manager == nil {
		return nil
	}
	return n.manager.Close()
}
func (d *driver) withDB(ctx context.Context, operation func(*sql.DB) error) error {
	if d.recovery != nil {
		if err := d.recovery.open(ctx); err != nil {
			return err
		}
		return d.recovery.manager.Do(ctx, operation)
	}
	return operation(d.db)
}
func (d *driver) managedMemory(ctx context.Context) error {
	settings, err := config.Parse([]byte(`{}`))
	if err != nil {
		return err
	}
	if d.recovery != nil {
		if err := d.recovery.open(ctx); err != nil {
			return err
		}
		d.memory, err = store.NewManagedMemory(ctx, d.recovery.manager, settings, d.resolved())
	} else {
		d.memory, err = store.NewMemory(d.db, settings, d.resolved())
	}
	return err
}
func syncNative(ctx context.Context, memory *store.Memory) error {
	_, err := memory.Reconcile(ctx, store.Scope{Target: "memory"})
	return err
}
func (d *driver) maintainNative(ctx context.Context, resolved paths.Resolved) error {
	if d.memory == nil {
		if d.recovery == nil {
			n, err := newNativeRecovery(resolved.Database, request{})
			if err != nil {
				return err
			}
			d.recovery = n
		}
		if err := d.managedMemory(ctx); err != nil {
			return err
		}
	}
	_, err := d.memory.Maintain(ctx)
	return err
}
