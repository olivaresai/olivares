// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package main

import (
	"log/slog"

	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/siemforward"
)

type ledgerForwardPump struct{}

func newLedgerForwardPump(func(string) string, store.Store, *siemforward.Module, *slog.Logger) *ledgerForwardPump {
	return nil
}
func (*ledgerForwardPump) register(*runtime.Runtime) error { return nil }
