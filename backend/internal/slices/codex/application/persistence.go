package application

import (
	"context"
	"sync"
)

// Gates and generations survive removal: a late result from an earlier sign-in
// cannot become current again when the same account (or even token) returns.
// Generation fields, like accounts, are guarded by Service.mu.
type accountLifecycle struct {
	gate       refreshGate
	generation uint64
}

type lifecycleVersion struct {
	fleet   uint64
	account uint64
}

func (service *Service) lifecycleLocked(accountID string) *accountLifecycle {
	lifecycle := service.diskGates[accountID]
	if lifecycle == nil {
		lifecycle = &accountLifecycle{}
		service.diskGates[accountID] = lifecycle
	}
	return lifecycle
}

func (service *Service) versionLocked(accountID string) lifecycleVersion {
	return lifecycleVersion{fleet: service.fleetGeneration, account: service.lifecycleLocked(accountID).generation}
}

// Imports learn the account ID only after parsing/refresh. Capture existing
// generations before that work, not after a logout has invalidated it.
type importVersions struct {
	fleet    uint64
	accounts map[string]uint64
}

func (service *Service) captureImportVersions() importVersions {
	service.mu.Lock()
	defer service.mu.Unlock()
	versions := importVersions{fleet: service.fleetGeneration, accounts: make(map[string]uint64, len(service.diskGates))}
	for id, lifecycle := range service.diskGates {
		versions.accounts[id] = lifecycle.generation
	}
	return versions
}

func (versions importVersions) forAccount(id string) lifecycleVersion {
	return lifecycleVersion{fleet: versions.fleet, account: versions.accounts[id]}
}

func (service *Service) versionMatchesLocked(accountID string, version lifecycleVersion) bool {
	return service.clearingAll == 0 && service.versionLocked(accountID) == version
}

// lockAccountDisk orders the durable mutation and its memory commit. Shared
// fleet fencing permits unrelated accounts to save concurrently; only
// Load/ClearAll require the exclusive side. No status lock spans I/O.
func (service *Service) lockAccountDisk(ctx context.Context, accountID string) (func(), error) {
	service.persistenceMu.RLock()
	service.mu.Lock()
	gate := &service.lifecycleLocked(accountID).gate
	service.mu.Unlock()
	if err := gate.acquire(ctx); err != nil {
		service.persistenceMu.RUnlock()
		return nil, err
	}
	// Callers release before notifying listeners, with defer covering failures.
	return sync.OnceFunc(func() { gate.release(); service.persistenceMu.RUnlock() }), nil
}
