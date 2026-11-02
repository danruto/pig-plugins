package store_test

import (
	"context"

	"github.com/danruto/pig-plugins/extensions/pig-hermes/internal/store"
)

type coordinationLease interface {
	Renew(context.Context) (bool, error)
	Release(context.Context) error
}

type nativeCoordinationLease struct{ *store.Lease }

func (lease nativeCoordinationLease) Release(context.Context) error {
	return lease.Lease.Release()
}

func nativeCoordination(request coordinationRequest) (store.Coordination, error) {
	switch request.Operation {
	case "mutation":
		return store.MutationCoordination(request.Identity)
	case "consolidation":
		return store.ConsolidationCoordination(request.AgentRoot, request.Identity, request.Target, request.ToolTarget)
	case "recovery":
		return store.RecoveryCoordination(request.Identity, store.RecoveryLockOptions{})
	case "recovery-overrides":
		wait, poll, stale := float64(80), float64(5), float64(request.StaleMS)
		return store.RecoveryCoordination(request.Identity, store.RecoveryLockOptions{WaitMS: &wait, PollMS: &poll, StaleMS: &stale})
	default:
		panic("unrecognized acceptance operation: " + request.Operation)
	}
}

func acquireNativeCoordination(ctx context.Context, request coordinationRequest) (coordinationLease, error) {
	coordination, err := nativeCoordination(request)
	if err != nil {
		return nil, err
	}
	lease, err := coordination.Acquire(ctx)
	if lease == nil {
		return nil, err
	}
	return nativeCoordinationLease{lease}, err
}
