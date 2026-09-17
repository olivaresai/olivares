// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/olivaresai/olivares/core/dr/opgate"
	"github.com/olivaresai/olivares/core/store"
)

// consoleRestoreGuard is the EXCLUSIVE local control the console restore holds
// across its critical section.
//
// Exclusive, because this operation changes the destination: it overwrites the
// installation's signing keys and then replaces its store file. Held ACROSS the
// whole of that section rather than checked at its start, because the section is
// not atomic — the keys are written one at a time and the store is copied
// afterwards, so an interleaved boot or a second restore inside it would observe an
// installation whose custody and whose data belong to different generations.
type consoleRestoreGuard struct {
	lease   *opgate.Lease
	anchors []opgate.Anchor
}

// acquireConsoleRestoreGuard fences the console's data directory and, on SQLite,
// the store file the promotion replaces.
//
// It refuses rather than waits. A restore that queued behind another one would be
// a second destructive operation started by a user who was told the first had not
// finished, and the console has no way to show that queue.
func acquireConsoleRestoreGuard(cfg DRConfig) (*consoleRestoreGuard, error) {
	var anchors []opgate.Anchor
	dirAnchor, present, err := opgate.AnchorForDataDir(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	if present {
		anchors = append(anchors, dirAnchor)
	}
	if store.Engine(cfg.EngineKind) == store.EngineSQLite && present {
		// The same path this handler promotes into. It is derived here rather than
		// passed in so the fenced file and the replaced file cannot drift apart.
		//
		// AnchorForStoreFile is now a PATH-ONLY constructor over the same frozen
		// identity the DSN resolver produces — it resolves the final component as well
		// as the parents — so this promotion and an ordinary boot of the same
		// installation land on the SAME anchor even when olivares.db is a symlink.
		// Before that they did not, and the console could hold a file nobody opened.
		//
		// It is reached only when the data directory itself resolved: a store file
		// whose parent does not exist is an unprovable destination and now says so,
		// rather than yielding the empty anchor that used to mean "unfenced".
		fileAnchor, filePresent, ferr := opgate.AnchorForStoreFile(filepath.Join(cfg.DataDir, "olivares.db"))
		if ferr != nil {
			return nil, ferr
		}
		if filePresent {
			anchors = append(anchors, fileAnchor)
		}
	}
	if len(anchors) == 0 {
		return nil, fmt.Errorf("this installation has no data directory at %q to fence, so a restore into it cannot be coordinated", cfg.DataDir)
	}
	lease, ok, err := opgate.TryAcquire(opgate.ModeExclusive, anchors...)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf(
			"another disaster-recovery operation holds this installation (%s); this restore is refused rather than queued behind it", cfg.DataDir)
	}
	g := &consoleRestoreGuard{lease: lease, anchors: anchors}
	// A control already in a blocking state means an earlier operation did not
	// finish. Overwriting custody on top of it would destroy the evidence that
	// operation is waiting on.
	for _, a := range anchors {
		rec, recPresent, rerr := lease.Read(a)
		if rerr != nil {
			g.release()
			return nil, fmt.Errorf("the restore control at %s exists and could not be read, and a read that failed is not an absence: %w", a.RecordPath(), rerr)
		}
		if recPresent && rec.Blocks() {
			g.release()
			return nil, fmt.Errorf(
				"a disaster-recovery operation has left %s in state %q (operation %s); resolve it before restoring over this installation",
				a.Canonical(), rec.State, rec.OpID)
		}
	}
	return g, nil
}

// release gives the guard back. Idempotent, so it can be deferred and also
// released explicitly.
func (g *consoleRestoreGuard) release() {
	if g == nil || g.lease == nil {
		return
	}
	if err := g.lease.Release(); err != nil {
		slog.Warn("the console restore's local control lease could not be confirmed released", "err", err)
	}
	g.lease = nil
}
