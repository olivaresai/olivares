// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"strconv"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// SessionLimits keep one runaway session or one person from taking a shared host.
// The zero value sets none: a module built without the option behaves as before.
type SessionLimits struct {
	// MaxPerUser is how many sessions one person may have running at once; 0 sets no cap.
	MaxPerUser int
	// MemoryMax and TasksMax are the ceilings of each session's systemd scope, written
	// as systemd's MemoryMax= and TasksMax= take them ("8G", "50%", "4096", "infinity").
	// Empty leaves that ceiling unset. They are cgroup ceilings on resident memory and
	// threads, not address space, so the large reservations of race and sanitizer
	// runtimes do not count against them.
	MemoryMax, TasksMax string
}

// WithSessionLimits sets the per-person cap and the per-session ceilings.
func WithSessionLimits(l SessionLimits) Option {
	return func(m *Module) { m.rt.limits = l }
}

// refuseOverSessionCap refuses a launch of a person who already has the most
// sessions running that the server allows one person. The count is the person's
// run rows that are starting or running, so it holds across a restart and on every
// node of a shared store. A launch with no person behind it (an agent) is not
// counted: the cap is per person. Two launches of one person that race may both
// pass; the cap is a guard for a shared host, not a quota.
func (m *Module) refuseOverSessionCap(ctx context.Context, tenant model.TenantID, person model.ID) error {
	limit := m.rt.limits.MaxPerUser
	if limit <= 0 || person.IsZero() {
		return nil
	}
	var running int
	err := m.runtimeData(ctx).View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		recs, _, err := repo.List(ctx, model.Query{Limit: limit, Filters: []model.Filter{
			eq(colRunLauncherUserID, person.String()),
			{Column: colState, Op: model.OpIn, Value: []string{statePending, stateRunning}},
		}})
		running = len(recs)
		return err
	})
	if err != nil {
		return denyClosedErr("the person's running sessions could not be counted", err)
	}
	if running >= limit {
		return &runErr{http.StatusTooManyRequests, "you have " + strconv.Itoa(limit) + " sessions running, the most this server allows one person; stop one to start another"}
	}
	return nil
}
