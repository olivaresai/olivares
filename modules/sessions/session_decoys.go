// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
)

// SessionDecoySource is the Business edition's hook into a launch: it puts
// credential-shaped decoy files into the session's user home and takes them away
// when the run ends. Community wires none, and a launch without one is unchanged.
//
// The source is given the run, the HOME the child will see and the child's working
// directory, and nothing else: no environment value, token or argv reaches it. The home
// is empty when the launch has no home of its own to plant in (it is the engine user's
// own home, or the child is not confined and names none): that directory is never
// planted in, and the source decides what an empty home means. A confined child that
// names no HOME runs in the private temporary directory the runner makes for it; the
// source is then asked once the runner has made it (LaunchSpec.ScratchHome). A returned
// error refuses the launch (a control the operator turned on must not silently stop
// working); cleanup may be nil.
type SessionDecoySource interface {
	PlantSessionDecoys(ctx context.Context, tenant model.TenantID, runRef, home, workDir string) (cleanup func(), err error)
}

// prepareSessionDecoys asks the wired source to plant decoys in the launch's HOME and
// ties their removal to the run. A launch with no source is left as it was. A confined
// launch that names no HOME is asked by the runner, with the private directory it makes
// and names HOME; every other launch without a home of its own is asked here with an
// empty home, so that a source that cannot work refuses it like any other.
func (m *Module) prepareSessionDecoys(ctx, runCtx context.Context, tenant model.TenantID, runRef string, spec *LaunchSpec) error {
	src := m.rt.SessionDecoys
	if src == nil {
		return nil
	}
	plant := func(home string) error {
		cleanup, err := src.PlantSessionDecoys(ctx, tenant, runRef, home, spec.Dir)
		if cleanup != nil {
			context.AfterFunc(runCtx, cleanup)
		}
		return err
	}
	named, home := false, ""
	for _, e := range spec.Env {
		if e.Name == envUserHome {
			named, home = true, e.Value // the last one wins, as it does for the child
		}
	}
	switch {
	case !named && spec.Confinement != nil:
		spec.ScratchHome = plant
		return nil
	case isEngineUserHome(home):
		home = ""
	}
	return plant(home)
}
