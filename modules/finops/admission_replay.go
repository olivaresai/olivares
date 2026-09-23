// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// admissionReplayWindow is how long one admission answers for RETRIES of the call that
// wrote it, measured from the moment its row entered its state. It must never be
// shorter than the reservation TTL.
const admissionReplayWindow = 5 * time.Minute

// replayOutcome is what an admission row says about a request that carries its key.
type replayOutcome int

const (
	// replayUnknown comes with every error: the row's answer could not be read. It is
	// the zero value, so an outcome read past its error is never "evaluate".
	replayUnknown replayOutcome = iota
	// replayEvaluate: the row does not answer for the request, which is evaluated.
	replayEvaluate
	// replayAnswer: the request is a retry of the call that wrote the row, and is
	// answered with that call's hold.
	replayAnswer
	// replayConflict: the key was written for another payload. Never a replay.
	replayConflict
)

// String names the outcome.
func (o replayOutcome) String() string {
	switch o {
	case replayEvaluate:
		return "evaluate"
	case replayAnswer:
		return "replay"
	case replayConflict:
		return "conflict"
	}
	return "unknown"
}

// replayFor decides whether row answers for a request whose payload hashes to hash, and
// with which hold. Not implemented yet: no row answers, and nothing is decided.
func (m *Module) replayFor(ctx context.Context, sc store.Scope, row admissionRow, hash string) (replayOutcome, holdID, error) {
	return replayUnknown, "", nil
}

// handlesLive reports whether the holds a row names still withhold headroom. Not
// implemented yet: no hold is live.
func handlesLive(ctx context.Context, sc store.Scope, handle, spend holdID, now model.Timestamp) (bool, error) {
	return false, nil
}
