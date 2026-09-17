// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// withRegisteredProfiledRun pins the owning process until its committed snapshot
// is published. Registry replacement/removal takes the corresponding write lock;
// a delayed frame from an old handle cannot publish for its successor.
func (m *Module) withRegisteredProfiledRun(lr *liveRun, fn func() error) error {
	if lr == nil || lr.proc == nil || lr.profile == nil || lr.claim.SID == "" || lr.claim.Holder == "" || lr.claim.Fence < 1 || lr.launchID.IsZero() {
		return conflictErr("profiled activity requires its registered process and Claim")
	}
	lr.authorityMu.RLock()
	defer lr.authorityMu.RUnlock()
	if current, ok := m.rt.getLive(lr.tenant, lr.runRef); !ok || current != lr {
		return conflictErr("profiled process is no longer registered")
	}
	return fn()
}

// mutateProfiledRun shares the established Claim CAS and early/late expiry
// protocol with governed run writes. The run CAS and all derived writes share
// that transaction; a rejected generation never leaves a managed-live effect.
func (m *Module) mutateProfiledRun(ctx context.Context, lr *liveRun, fn func(store.Scope, model.Record) error) error {
	return m.authorizedMutate(ctx, lr.tenant, lr.claim, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		rec, err := findRunRec(ctx, repo, lr.runRef)
		if err != nil {
			return err
		}
		if err := guardRuntimeLaunch(lr.launchID)(rec); err != nil {
			return err
		}
		if rec.String(colState) != stateRunning || rec.String(colRunClaimSID) != lr.claim.SID || rec.Int(colClaimFence) != lr.claim.Fence || rec.String(colClaimHolder) != lr.claim.Holder || rec.String(colRunProfileID) != lr.profile.ProfileID || rec.String(colRunProfileDriver) != lr.profile.Driver || rec.String(colRunProfileEnvRef) != lr.profile.EnvironmentRef {
			return conflictErr("profiled run no longer owns this process and Claim")
		}
		return fn(sc, rec)
	})
}
