// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !linux

package services

import (
	"context"
	"errors"
)

var errNoSystemBus = errors.New("the units helper reaches the service manager on Linux only")

// SystemBus answers nothing here: every call fails, so no act is performed.
type SystemBus struct{}

// DialSystemBus fails on a system without the Linux service manager.
func DialSystemBus() (*SystemBus, error) { return nil, errNoSystemBus }

// Close implements the connection's close.
func (b *SystemBus) Close() error { return nil }

// ListUnitsByPatterns implements Bus.
func (b *SystemBus) ListUnitsByPatterns(context.Context, []string, []string) ([]Unit, error) {
	return nil, errNoSystemBus
}

// GetUnitFileState implements Bus.
func (b *SystemBus) GetUnitFileState(context.Context, string) (string, error) {
	return "", errNoSystemBus
}

// Dependents implements Bus.
func (b *SystemBus) Dependents(context.Context, string) ([]string, error) { return nil, errNoSystemBus }

// Subscribe implements Bus.
func (b *SystemBus) Subscribe(context.Context) (<-chan JobRemoved, func(), error) {
	return nil, nil, errNoSystemBus
}

// QueueJob implements Bus.
func (b *SystemBus) QueueJob(context.Context, string, string, string) (string, error) {
	return "", errNoSystemBus
}

// EnableUnitFiles implements Bus.
func (b *SystemBus) EnableUnitFiles(context.Context, []string, bool, bool) (int, error) {
	return 0, errNoSystemBus
}

// DisableUnitFiles implements Bus.
func (b *SystemBus) DisableUnitFiles(context.Context, []string, bool) (int, error) {
	return 0, errNoSystemBus
}
