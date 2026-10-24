// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/olivaresai/olivares/core/runtime"
)

// liveModuleProfile is the module profile this node runs now. It starts as the
// profile the boot resolved and changes when an activation turns modules on or off
// (liveActivation.Apply). The API, the module page and the periodic jobs ask it, so
// they follow a change without a restart.
type liveModuleProfile struct{ p atomic.Pointer[moduleProfile] }

func newLiveModuleProfile(p moduleProfile) *liveModuleProfile {
	l := &liveModuleProfile{}
	l.p.Store(&p)
	return l
}

// Load is the profile the node runs now.
func (l *liveModuleProfile) Load() moduleProfile { return *l.p.Load() }

// Active reports whether the module with this API namespace runs now.
func (l *liveModuleProfile) Active(namespace string) bool { return l.p.Load().Active(namespace) }

// liveActivation applies the deployment settings' modules to the running engine: the
// modules an activation adds start, the ones it takes away pause, and the profile
// the rest of the engine reads follows. The settings are the one source: Apply
// reads the module selection and the activation they record now.
type liveActivation struct {
	// mu serializes Apply: two administrators' changes meet one after the other, each
	// reading the settings the previous one left.
	mu       sync.Mutex
	settings *productSettings
	profile  *liveModuleProfile
	rt       *runtime.Runtime
	log      *slog.Logger
}

// Apply makes the engine run the modules the deployment settings call for. A module
// that fails to change state undoes the ones already changed, so the engine never
// runs half of an activation; the profile changes only when every module followed.
func (l *liveActivation) Apply(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	m, err := l.settings.Activation(ctx)
	if err != nil {
		return fmt.Errorf("read the activation: %w", err)
	}
	doc, found, err := l.settings.load(ctx)
	if err != nil {
		return err
	}
	have := l.profile.Load()
	selected := have.Selected()
	if found && doc.Modules != nil {
		selected, _ = knownModules(doc.Modules.Selected)
	}
	want, err := resolveModuleProfileWith(selected, activationModules(m))
	if err != nil {
		return fmt.Errorf("resolve the modules: %w", err)
	}
	var changed []string
	for _, name := range allModuleSelection() {
		if want.Active(name) == have.Active(name) {
			continue
		}
		if err := l.rt.SetModuleActive(ctx, moduleCatalog[name].Name, want.Active(name)); err != nil {
			failure := fmt.Errorf("%s: %w", name, err)
			for _, back := range changed {
				if rerr := l.rt.SetModuleActive(ctx, moduleCatalog[back].Name, have.Active(back)); rerr != nil {
					// The engine now differs from its profile; the caller restarts to rebuild it.
					l.log.Error("activation: could not undo a module change", "module", back, "err", rerr)
					failure = errors.Join(failure, fmt.Errorf("undo %s: %w", back, rerr))
				}
			}
			return failure
		}
		changed = append(changed, name)
	}
	publishActivationOverlay(m)
	l.profile.p.Store(&want)
	if len(changed) > 0 {
		l.log.Info("activation: the running engine now runs the modules the activation calls for", "changed", changed, "now", want.ActiveNames())
	}
	return nil
}
