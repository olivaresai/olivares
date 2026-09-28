// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package services

import "context"

// Unit is one row of the service manager's ListUnitsByPatterns answer, the fields this module
// reads.
type Unit struct {
	Name        string `json:"name"`
	LoadState   string `json:"load_state"`
	ActiveState string `json:"active_state"`
	SubState    string `json:"sub_state"`
}

// JobRemoved is the service manager's JobRemoved signal: the job's id and object path, the unit
// it ran for and its result (done, canceled, timeout, failed, dependency or skipped).
type JobRemoved struct {
	ID     uint32
	Job    string
	Unit   string
	Result string
}

// Bus is the part of org.freedesktop.systemd1.Manager the units helper calls. It has no method
// that masks, links, edits or writes a unit, a drop-in or a path.
type Bus interface {
	// ListUnitsByPatterns returns the loaded units in states whose names match patterns.
	ListUnitsByPatterns(ctx context.Context, states, patterns []string) ([]Unit, error)
	// GetUnitFileState returns the enablement of unit's file.
	GetUnitFileState(ctx context.Context, unit string) (string, error)
	// Dependents returns the units that require unit or are bound to it.
	Dependents(ctx context.Context, unit string) ([]string, error)
	// Subscribe asks the service manager for its job signals and delivers each JobRemoved from
	// then on, until stop is called.
	Subscribe(ctx context.Context) (signals <-chan JobRemoved, stop func(), err error)
	// QueueJob calls method (StartUnit, StopUnit, RestartUnit or ReloadUnit) for unit with mode
	// and returns the object path of the job the service manager queued.
	QueueJob(ctx context.Context, method, unit, mode string) (string, error)
	// EnableUnitFiles enables units and returns how many changes the service manager made.
	EnableUnitFiles(ctx context.Context, units []string, runtime, force bool) (int, error)
	// DisableUnitFiles disables units and returns how many changes the service manager made.
	DisableUnitFiles(ctx context.Context, units []string, runtime bool) (int, error)
}
