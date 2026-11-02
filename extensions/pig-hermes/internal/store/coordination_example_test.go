package store_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/danruto/pig-plugins/extensions/pig-hermes/internal/store"
)

func ExampleCoordination() {
	root, err := os.MkdirTemp("", "hermes-coordination-example-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(root)
	ctx := context.Background()
	document := filepath.Join(root, "knowledge", "MEMORY.md")
	mutation, err := store.MutationCoordination(document)
	if err != nil {
		panic(err)
	}
	lease, err := mutation.Acquire(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Println(lease.Token() != "")
	owned, err := lease.CurrentOwner(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Println(owned)
	renewed, err := lease.Renew(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Println(renewed)
	if err := lease.Release(); err != nil {
		panic(err)
	}
	// Output:
	// true
	// true
	// true
}

func ExampleConsolidationCoordination() {
	root, err := os.MkdirTemp("", "hermes-consolidation-example-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(root)
	coordination, err := store.ConsolidationCoordination(root, filepath.Join(root, "projects", "demo", "MEMORY.md"), "memory", "project")
	if err != nil {
		panic(err)
	}
	fmt.Println(coordination.StaleMS, coordination.RenewInterval)
	// Output: 45000 10s
}

func ExampleRecoveryCoordination() {
	root, err := os.MkdirTemp("", "hermes-recovery-example-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(root)
	waitMS := 0.0
	coordination, err := store.RecoveryCoordination(filepath.Join(root, "knowledge", "sessions.db"), store.RecoveryLockOptions{WaitMS: &waitMS})
	if err != nil {
		panic(err)
	}
	fmt.Println(coordination.WaitMS, coordination.PollMS)
	// Output: 0 50
}
