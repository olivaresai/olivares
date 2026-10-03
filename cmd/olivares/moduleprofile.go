// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"slices"
	"sort"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/modules/recording"
)

// The module profile is the set of modules this node runs: the administrator's
// selection, closed over what each selected module requires. A module outside
// the profile is dormant: its tables stay, but it is not started, its routes
// answer "module_not_enabled" and its periodic jobs are not scheduled.
//
// Requirements are the composition root's own wiring (a module that receives
// another module at construction or at boot requires it), so they live here
// beside that wiring. A namespace that is not in the catalog (edition modules,
// host modules) is always active: the profile only ever turns off what it knows.

// moduleSpec is one catalog entry.
type moduleSpec struct {
	// requires are the modules this one is wired to and cannot run without.
	requires []string
	// kernel modules always run: the rest of the engine calls them directly.
	kernel bool
	// standard modules are in the profile of a new installation.
	standard bool
}

// moduleCatalog is every module the profile can turn off, by API namespace.
var moduleCatalog = map[string]moduleSpec{
	"governance":     {kernel: true},
	"sessions":       {kernel: true, requires: []string{"governance", "liveingest"}},
	"claude-policy":  {standard: true, requires: []string{"governance"}},
	"claude-agents":  {requires: []string{"governance"}},
	"identity":       {standard: true, requires: []string{"governance"}},
	"accessmap":      {},
	"adoption":       {},
	"capabilities":   {standard: true},
	"catalog":        {},
	"compliance":     {},
	"consoleviews":   {standard: true},
	"deploy":         {requires: []string{"governance"}},
	"evals":          {requires: []string{"sessions"}},
	"eventing":       {requires: []string{"governance"}},
	"finops":         {},
	"gitpublish":     {},
	"health":         {},
	"inferenceproxy": {requires: []string{"models", "finops", "knowledge", "governance"}},
	"inventory":      {},
	"knowledge":      {requires: []string{"sourcescope", "compliance"}},
	"liveingest":     {},
	"models":         {requires: []string{"finops", "governance", "sourcescope"}},
	"notify":         {},
	"observability":  {},
	"orchestration":  {requires: []string{"finops", "governance", "notify", "sessions"}},
	"posture":        {requires: []string{"inventory"}},
	"recording":      {requires: []string{"sessions"}},
	"redteam":        {},
	"reporting":      {requires: []string{"compliance"}},
	"sandbox":        {requires: []string{"evals", "sessions"}},
	"security":       {},
	"siemforward":    {requires: []string{"eventing"}},
	"sourcescope":    {requires: []string{"governance"}},
	"voice":          {requires: []string{"finops", "governance"}},
}

// standardModuleSelection is the selection of a new installation.
func standardModuleSelection() []string {
	var out []string
	for name, spec := range moduleCatalog {
		if spec.standard {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// allModuleSelection selects every module in the catalog.
func allModuleSelection() []string {
	out := make([]string, 0, len(moduleCatalog))
	for name := range moduleCatalog {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// published26100ModuleSelection is the profile-free default shipped in 26.10.0.
// Keep it fixed: adding a catalog module must not silently enable it on upgrade.
func published26100ModuleSelection() []string {
	return []string{
		"accessmap", "adoption", "capabilities", "catalog", "claude-agents",
		"claude-policy", "compliance", "consoleviews", "deploy", "evals", "eventing",
		"finops", "gitpublish", "governance", "health", "identity", "inferenceproxy",
		"inventory", "knowledge", "liveingest", "models", "notify", "observability",
		"orchestration", "posture", "recording", "redteam", "reporting", "sandbox",
		"security", "sessions", "siemforward", "sourcescope", "voice",
	}
}

// moduleProfile is a resolved selection.
type moduleProfile struct {
	selected []string
	active   map[string]bool
	// existingInstallation is observed before this boot ensures SYSTEM. It lets
	// the first settings import preserve profile-free 26.10.0 installations.
	existingInstallation bool
	// activatedBy names, per module, the active activation add-ons that run it.
	activatedBy map[string][]string
}

// activationModulesOf is the edition seam editionActivationModules; tests
// replace it to give an add-on modules.
var activationModulesOf = editionActivationModules

// activationModules is the module to add-on map of m's active entries: while an
// add-on is active, the modules its edition names for it run, whatever the
// administrator selected. Enabling a family is then one action, its activation.
func activationModules(m *ActivationManifest) map[string][]string {
	if m == nil {
		return nil
	}
	out := map[string][]string{}
	for _, e := range m.Entries {
		if !e.overlaid() {
			continue
		}
		for _, name := range activationModulesOf(e.Addon) {
			if _, ok := moduleCatalog[name]; ok && !slices.Contains(out[name], e.Addon) {
				out[name] = append(out[name], e.Addon)
			}
		}
	}
	for name := range out {
		sort.Strings(out[name])
	}
	return out
}

// resolveModuleProfile closes selected over the catalog's requirements and adds
// the kernel. An unknown name is an error: a selection names catalog modules.
func resolveModuleProfile(selected []string) (moduleProfile, error) {
	return resolveModuleProfileWith(selected, nil)
}

// resolveModuleProfileWith is resolveModuleProfile plus the modules active
// activation add-ons run (activationModules): they run, with their
// requirements, without being selected.
func resolveModuleProfileWith(selected []string, activated map[string][]string) (moduleProfile, error) {
	p := moduleProfile{active: map[string]bool{}, activatedBy: activated}
	var queue []string
	for name := range activated {
		queue = append(queue, name)
	}
	for _, name := range selected {
		if _, ok := moduleCatalog[name]; !ok {
			return moduleProfile{}, fmt.Errorf("unknown module %q", name)
		}
		if !slices.Contains(p.selected, name) {
			p.selected = append(p.selected, name)
		}
		queue = append(queue, name)
	}
	sort.Strings(p.selected)
	for name, spec := range moduleCatalog {
		if spec.kernel {
			queue = append(queue, name)
		}
	}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if p.active[name] {
			continue
		}
		p.active[name] = true
		queue = append(queue, moduleCatalog[name].requires...)
	}
	return p, nil
}

// Active reports whether the module with this API namespace runs on this node.
// The zero profile (an engine composed without one) runs every module.
func (p moduleProfile) Active(namespace string) bool {
	if _, known := moduleCatalog[namespace]; !known || p.active == nil {
		return true
	}
	return p.active[namespace]
}

// Selected is the selection the profile was resolved from, sorted.
func (p moduleProfile) Selected() []string { return slices.Clone(p.selected) }

// ActiveNames are the catalog modules that run, sorted.
func (p moduleProfile) ActiveNames() []string {
	out := make([]string, 0, len(p.active))
	for name := range p.active {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// ActivatedBy names the active activation add-ons that run name, sorted.
func (p moduleProfile) ActivatedBy(name string) []string { return slices.Clone(p.activatedBy[name]) }

// sameActive reports whether two profiles run the same modules.
func (p moduleProfile) sameActive(q moduleProfile) bool {
	return slices.Equal(p.ActiveNames(), q.ActiveNames())
}

// requiredBy names the active modules that require name, sorted: the reason a
// module the administrator did not select still runs.
func (p moduleProfile) requiredBy(name string) []string {
	var out []string
	for other := range p.active {
		if slices.Contains(moduleCatalog[other].requires, name) {
			out = append(out, other)
		}
	}
	sort.Strings(out)
	return out
}

// running is the view of set that optional wiring and periodic jobs receive: a
// module this node does not run is nil there, so nothing hands it work. The
// modules themselves are still built and registered (dormant), so direct wiring
// that requires them keeps its value.
func (set moduleSet) running(p moduleProfile) moduleSet {
	r := set
	if !p.Active("compliance") {
		r.compliance = nil
	}
	if !p.Active("recording") {
		r.recorder = nil
	}
	if !p.Active("eventing") {
		r.eventing = nil
	}
	if !p.Active("orchestration") {
		r.orchestration = nil
	}
	if !p.Active("notify") {
		r.notify = nil
	}
	if !p.Active("siemforward") {
		r.siemforward = nil
	}
	if !p.Active("finops") {
		r.finops = nil
		r.finopsBackstop = nil
	}
	if !p.Active("models") {
		r.models = nil
	}
	if !p.Active("inferenceproxy") {
		r.inferenceProxy = nil
	}
	if !p.Active("knowledge") {
		r.knowledge = nil
	}
	if !p.Active("reporting") {
		r.reporting = nil
	}
	if !p.Active("inventory") {
		r.inventory = nil
	}
	if !p.Active("gitpublish") {
		r.gitpublish = nil
	}
	return r
}

// sessionRecorderOf keeps a module that does not run out of the API's route
// recorder: a nil *recording.Module would be a non-nil interface.
func sessionRecorderOf(rec *recording.Module) api.SessionRecorder {
	if rec == nil {
		return nil
	}
	return rec
}

// notEnabledNamespaces are the namespaces of mods this node does not run, for
// the API (they answer module_not_enabled) and the console (server-info).
func notEnabledNamespaces(mods []api.Module, p moduleProfile) []string {
	var out []string
	for _, m := range mods {
		if !p.Active(m.APINamespace()) {
			out = append(out, m.APINamespace())
		}
	}
	sort.Strings(out)
	return out
}
