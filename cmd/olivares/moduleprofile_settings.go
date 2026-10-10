// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The module selection is product state: it lives in the deployment settings
// record (productSettingsDoc.Modules). Each node keeps a file copy because modules
// are built before the store opens, exactly like the activation; boot reconciles
// the two before any listener starts and restarts once when they differ.

// moduleProfileFile is this node's copy of the selection, in the data directory.
const moduleProfileFile = "module-profile.json"

const moduleProfileVersion = "olivares.module.profile.v2"

// moduleSelectionDoc is the selection, as recorded and as copied on each node.
type moduleSelectionDoc struct {
	Version   string   `json:"version,omitempty"`
	Selected  []string `json:"selected"`
	UpdatedAt string   `json:"updated_at,omitempty"`
	// ImportPending identifies an automatic fresh-store default written before
	// SYSTEM is created. Reconcile adds seeded data modules, then clears it.
	ImportPending bool `json:"import_pending,omitempty"`
}

// Before v2, sessions forced liveingest ON even for an empty selection.
// Preserve that state until an administrator records a v2 selection.
func (doc moduleSelectionDoc) selectedModules() []string {
	if doc.Version != moduleProfileVersion {
		return sortedUnion(doc.Selected, []string{"liveingest"})
	}
	return doc.Selected
}

func moduleProfilePath(dataDir string) string { return filepath.Join(dataDir, moduleProfileFile) }

// loadNodeModuleSelection reads this node's copy; found is false when there is none.
func loadNodeModuleSelection(dataDir string) (sel []string, found bool, err error) {
	doc, found, err := loadNodeModuleDocument(dataDir)
	if err == nil && found {
		sel = doc.selectedModules()
	}
	return sel, found, err
}

func loadNodeModuleDocument(dataDir string) (doc moduleSelectionDoc, found bool, err error) {
	raw, err := os.ReadFile(moduleProfilePath(dataDir))
	if errors.Is(err, os.ErrNotExist) {
		return doc, false, nil
	}
	if err != nil {
		return doc, false, err
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return doc, true, fmt.Errorf("%s: %w", moduleProfilePath(dataDir), err)
	}
	return doc, true, nil
}

// saveNodeModuleSelection writes this node's copy atomically, owner-only.
func saveNodeModuleSelection(dataDir string, selected []string, at time.Time) error {
	return saveNodeModuleDocument(dataDir, moduleSelectionDoc{Selected: selected}, at)
}

func saveNodeModuleDocument(dataDir string, doc moduleSelectionDoc, at time.Time) error {
	doc.Version = moduleProfileVersion
	doc.Selected = slices.Clone(doc.Selected)
	doc.UpdatedAt = at.UTC().Format(time.RFC3339)
	if doc.Selected == nil {
		doc.Selected = []string{}
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	path := moduleProfilePath(dataDir)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// knownModules keeps the catalog modules of sel, sorted; a name this binary does
// not know (a module removed since the selection was written) is dropped.
func knownModules(sel []string) (known, unknown []string) {
	for _, name := range sel {
		if _, ok := moduleCatalog[name]; ok {
			if !slices.Contains(known, name) {
				known = append(known, name)
			}
		} else {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(known)
	return known, unknown
}

// bootModuleProfile is the profile this start builds its modules with, decided
// before the store opens:
//
//   - this node's copy, when there is one;
//   - a new installation (no store yet) runs the standard selection;
//   - an existing store without a copy (an installation from before module
//     profiles, or a node that lost its copy) runs every module, so nothing in
//     use is off; the reconcile preserves the published default for an existing
//     installation and follows any recorded or explicit node selection.
//
// An unreadable copy also runs every module, and says so. The modules of the
// add-ons active in this node's activation file run as well (activationModules).
func bootModuleProfile(dataDir string, storeExists bool, log *slog.Logger) moduleProfile {
	sel, found, err := loadNodeModuleSelection(dataDir)
	switch {
	case err != nil:
		log.Error("modules: this node's module profile is unreadable; every module runs until the deployment settings rewrite it", "err", err)
		sel = allModuleSelection()
	case !found && storeExists:
		sel = allModuleSelection()
	case !found:
		sel = standardModuleSelection()
	}
	known, unknown := knownModules(sel)
	if len(unknown) > 0 {
		log.Warn("modules: this build does not have some selected modules; they are ignored", "modules", unknown)
	}
	activation, err := LoadActivationManifest(dataDir)
	if err != nil {
		log.Error("modules: this node's activation file is unreadable; no add-on runs its modules until the deployment settings rewrite it", "err", err)
	}
	p, _ := resolveModuleProfileWith(known, activationModules(activation)) // known names always resolve
	return p
}

// usedModules names the catalog modules whose tables hold at least one row in
// any tenant this node serves: an installation that already has data in a
// module keeps it on.
// census is the store's closed registry (engine.census): the store the engine
// serves through is a wrapper and does not expose it.
func usedModules(ctx context.Context, st store.Store, census store.CompositionCensus) ([]string, error) {
	if census == nil {
		return nil, errors.New("the store's table registry is not available")
	}
	byModule := map[string][]model.Kind{}
	for _, d := range census.CensusDescriptors() {
		ns := d.Kind.Namespace()
		// The kernel always runs: it is never part of a selection.
		if spec, ok := moduleCatalog[ns]; ok && spec.Kind != "kernel" {
			byModule[ns] = append(byModule[ns], d.Kind)
		}
	}
	// The tenants every install can read (servedWorkTenants): the default
	// PostgreSQL install has no BYPASSRLS admin pool for ListOrgs.
	tenants, err := servedWorkTenants(ctx, st)
	if err != nil {
		return nil, fmt.Errorf("list the tenants: %w", err)
	}
	var used []string
	for ns, kinds := range byModule {
		hasRows := false
		for _, tenant := range tenants {
			err := st.View(ctx, tenant, func(sc store.Scope) error {
				for _, kind := range kinds {
					repo, err := sc.Ext(kind)
					if err != nil {
						return err
					}
					rows, _, err := repo.List(ctx, model.Query{Limit: 1})
					if err != nil {
						return fmt.Errorf("%s: %w", kind, err)
					}
					if len(rows) > 0 {
						hasRows = true
						return nil
					}
				}
				return nil
			})
			if err != nil {
				return nil, fmt.Errorf("read the %s tables: %w", ns, err)
			}
			if hasRows {
				break
			}
		}
		if hasRows {
			used = append(used, ns)
		}
	}
	sort.Strings(used)
	return used, nil
}

// moduleReconcile is what the settings reconcile needs to bring the module
// profile in step with the record.
type moduleReconcile struct {
	// booted is the profile this start built its modules with.
	booted moduleProfile
	// used names the modules that already hold data (usedModules).
	used func(context.Context) ([]string, error)
	// demo: this start seeded the demo estate, which a new installation's
	// navigation must still list (its pages are what the demo shows).
	demo bool
}

// moduleInstallationExists reads the durable marker 26.10.0 ensured on every
// active boot. Call before this boot ensures it, including on a fresh store.
// Binding SYSTEM directly works under PostgreSQL RLS without an admin pool.
func moduleInstallationExists(ctx context.Context, st store.Store) (bool, error) {
	var exists bool
	err := st.System(ctx, func(sys store.SystemScope) error {
		_, err := sys.GetOrg(ctx, model.SystemTenantID)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		exists = err == nil
		return err
	})
	return exists, err
}

// reconcileModules brings this node's module profile in step with the record and
// reports whether the modules this start built differ from it (a restart is due).
//
//   - no selection recorded yet: import an explicit node selection, otherwise
//     preserve the 26.10.0 default for an existing installation, otherwise
//     select the standard modules and this new installation's seeded data; a
//     new installation also records previews_hidden (productSettingsDoc);
//   - otherwise this node's copy is rewritten from the record when it differs.
func (p *productSettings) reconcileModules(ctx context.Context, mr moduleReconcile, log *slog.Logger) (bool, error) {
	doc, found, err := p.load(ctx)
	if err != nil {
		return false, err
	}
	var selection, used []string
	action := ""
	newInstallation := false
	node, nodeFound, nodeErr := loadNodeModuleDocument(p.dataDir)
	if found && doc.Modules != nil {
		selection = doc.Modules.selectedModules()
		if doc.Modules.Version != moduleProfileVersion {
			action = "deployment.settings.modules.upgrade"
		}
	} else {
		switch {
		case nodeErr == nil && nodeFound && !node.ImportPending:
			selection = node.selectedModules() // a v2 explicit empty selection is authoritative too
		case nodeErr == nil && !nodeFound && mr.booted.existingInstallation:
			selection = published26100ModuleSelection()
		default:
			if nodeErr != nil {
				log.Warn("modules: this node's unreadable profile is being repaired from the standard selection and modules holding data", "err", nodeErr)
			}
			used, err = mr.used(ctx)
			if err != nil {
				return false, fmt.Errorf("find the modules this installation uses: %w", err)
			}
			selection = standardModuleSelection()
			if nodeErr == nil && node.ImportPending {
				selection = slices.Clone(node.selectedModules())
			}
			selection = append(selection, used...)
		}
		selection, _ = knownModules(selection)
		action = "deployment.settings.modules.import"
		// This start created the installation, or a first start stopped before
		// this import: a new installation, whose console lists the first job
		// only, unless it is the seeded demo.
		newInstallation = !mr.demo && (!mr.booted.existingInstallation || (nodeErr == nil && nodeFound && node.ImportPending))
	}
	if action != "" {
		actor, aerr := auth.NewSystemOperator("boot/module-profile", "record this installation's module selection in the deployment settings")
		if aerr != nil {
			return false, aerr
		}
		at := p.now().UTC().Format(time.RFC3339)
		meta := map[string]any{}
		err = p.update(ctx, actor, action, meta, func(current *productSettingsDoc) {
			delete(meta, "previews_hidden") // a retry after a conflict may not record it
			if current.Modules == nil {
				current.Modules = &moduleSelectionDoc{Version: moduleProfileVersion, Selected: slices.Clone(selection), UpdatedAt: at}
				if current.Modules.Selected == nil {
					current.Modules.Selected = []string{}
				}
				if newInstallation {
					current.PreviewsHidden = true
					meta["previews_hidden"] = true
				}
			} else if current.Modules.Version != moduleProfileVersion {
				current.Modules.Selected = current.Modules.selectedModules()
				current.Modules.Version = moduleProfileVersion
				current.Modules.UpdatedAt = at
			}
			// Import and upgrade both follow the current record; a newer admin selection wins.
			selection = current.Modules.Selected
			meta["selected"] = selection
		})
		if err != nil {
			return false, err
		}
		log.Info("modules: recorded this installation's module selection", "selected", selection, "in_use", used)
	}
	known, unknown := knownModules(selection)
	if len(unknown) > 0 {
		log.Warn("modules: the deployment settings select modules this build does not have; they are ignored", "modules", unknown)
	}
	activation := doc.Activation
	if activation == nil {
		if activation, err = LoadActivationManifest(p.dataDir); err != nil {
			return false, fmt.Errorf("read this node's activation file: %w", err)
		}
	}
	want, _ := resolveModuleProfileWith(known, activationModules(activation))
	if nodeErr != nil || !nodeFound || node.ImportPending || node.Version != moduleProfileVersion || !slices.Equal(node.Selected, want.Selected()) {
		if err := saveNodeModuleSelection(p.dataDir, want.Selected(), p.now()); err != nil {
			return false, fmt.Errorf("write this node's module profile from the deployment settings: %w", err)
		}
	}
	if want.sameActive(mr.booted) {
		log.Info("modules: in step with the deployment settings", "running", want.ActiveNames())
		return false, nil
	}
	log.Info("modules: the deployment settings change the modules this node runs",
		"was", mr.booted.ActiveNames(), "now", want.ActiveNames())
	return true, nil
}

// moduleSelectionService is the console's module selection (/v1/console/modules).
// A change is recorded in the deployment settings, copied to this node, and
// applied by restarting the engine; only a durable record plus an accepted
// restart is reported as success.
type moduleSelectionService struct {
	settings *productSettings
	running  moduleProfile
	restart  func(reason string) error
	log      *slog.Logger
	// used names the modules whose tables hold rows (usedModules); nil reads none.
	used func(context.Context) ([]string, error)
	// sessions counts the sessions running now, which a restart stops; nil counts none.
	sessions func() int
}

// runningSessions is how many sessions the restart that applies a change would stop.
func (s moduleSelectionService) runningSessions() int {
	if s.sessions == nil {
		return 0
	}
	return s.sessions()
}

// holdingData is the set of modules whose tables hold rows.
func (s moduleSelectionService) holdingData(ctx context.Context) ([]string, error) {
	if s.used == nil {
		return nil, nil
	}
	used, err := s.used(ctx)
	if err != nil {
		return nil, fmt.Errorf("find the modules that hold data: %w", err)
	}
	return used, nil
}

func (s moduleSelectionService) ModuleSelection(ctx context.Context) (api.ModuleSelectionDTO, error) {
	selected := s.running.Selected()
	doc, found, err := s.settings.load(ctx)
	if err != nil {
		return api.ModuleSelectionDTO{}, err
	}
	if found && doc.Modules != nil {
		selected, _ = knownModules(doc.Modules.Selected)
	}
	used, err := s.holdingData(ctx)
	if err != nil {
		return api.ModuleSelectionDTO{}, err
	}
	dto := moduleSelectionDTO(selected, s.running, used)
	dto.RunningSessions = s.runningSessions()
	return dto, nil
}

func (s moduleSelectionService) SelectModules(ctx context.Context, actor auth.Principal, selected []string) (api.ModuleSelectionDTO, error) {
	activation, err := s.settings.Activation(ctx)
	if err != nil {
		return api.ModuleSelectionDTO{}, fmt.Errorf("%w: %v", api.ErrModulesNotRecorded, err)
	}
	want, err := resolveModuleProfileWith(selected, activationModules(activation))
	if err != nil {
		return api.ModuleSelectionDTO{}, fmt.Errorf("%w: %v", api.ErrUnknownModule, err)
	}
	if err := s.settings.writeModules(ctx, actor, "deployment.settings.modules", want.Selected()); err != nil {
		return api.ModuleSelectionDTO{}, fmt.Errorf("%w: %v", api.ErrModulesNotRecorded, err)
	}
	if err := saveNodeModuleSelection(s.settings.dataDir, want.Selected(), s.settings.now()); err != nil {
		s.log.Warn("modules: recorded, but this node's copy was not written; the restart rewrites it from the deployment settings", "err", err)
	}
	used, err := s.holdingData(ctx)
	if err != nil {
		return api.ModuleSelectionDTO{}, err
	}
	dto := moduleSelectionDTO(want.Selected(), s.running, used)
	// Read before any restart is requested: the sessions it is about to stop.
	dto.RunningSessions = s.runningSessions()
	if want.sameActive(s.running) {
		return dto, nil
	}
	if s.restart == nil {
		return api.ModuleSelectionDTO{}, api.ErrModulesRestartUnavailable
	}
	if err := s.restart("module selection"); err != nil {
		return api.ModuleSelectionDTO{}, fmt.Errorf("%w: %v", api.ErrModulesRestartUnavailable, err)
	}
	dto.Restarting = true
	return dto, nil
}

// moduleSelectionDTO describes every catalog module: whether it is selected,
// whether it runs on this node now, and which active add-ons run it.
func moduleSelectionDTO(selected []string, running moduleProfile, used []string) api.ModuleSelectionDTO {
	names := allModuleSelection()
	out := api.ModuleSelectionDTO{Modules: make([]api.ModuleStateDTO, 0, len(names))}
	for _, name := range names {
		spec := moduleCatalog[name]
		st := api.ModuleStateDTO{
			Name: name, Selected: slices.Contains(selected, name), Running: running.Active(name),
			AlwaysOn: spec.Kind == "kernel", Requires: slices.Clone(spec.Requires),
			HoldsData: slices.Contains(used, name), ActivatedBy: running.ActivatedBy(name),
		}
		if st.Running && !st.Selected && !st.AlwaysOn {
			st.RequiredBy = running.requiredBy(name)
		}
		out.Modules = append(out.Modules, st)
	}
	return out
}
