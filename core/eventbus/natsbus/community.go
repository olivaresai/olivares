// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise || !addon_ids

package natsbus

import (
	"errors"
	"github.com/olivaresai/olivares/core/eventbus"
)

// Bus exists only as an unavailable constructor's return type.
type Bus struct{ eventbus.Bus }

func New(Config, Options) (*Bus, error) {
	return nil, errors.New("event bus: the Core NATS bridge is a Business capability")
}
func (*Bus) SetInjectGate(func() bool) {}
func (*Bus) Bridge() BridgeStats       { return BridgeStats{} }
