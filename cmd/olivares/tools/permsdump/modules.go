// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"github.com/olivaresai/olivares/core/api"
	accessmap "github.com/olivaresai/olivares/modules/access-map"
	"github.com/olivaresai/olivares/modules/capabilities"
	"github.com/olivaresai/olivares/modules/catalog"
	"github.com/olivaresai/olivares/modules/claudeadoption"
	"github.com/olivaresai/olivares/modules/compliance"
	"github.com/olivaresai/olivares/modules/consoleviews"
	"github.com/olivaresai/olivares/modules/deploy"
	"github.com/olivaresai/olivares/modules/evals"
	"github.com/olivaresai/olivares/modules/eventing"
	"github.com/olivaresai/olivares/modules/finops"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/modules/health"
	"github.com/olivaresai/olivares/modules/inferenceproxy"
	"github.com/olivaresai/olivares/modules/inventory"
	"github.com/olivaresai/olivares/modules/knowledge"
	"github.com/olivaresai/olivares/modules/liveingest"
	"github.com/olivaresai/olivares/modules/models"
	"github.com/olivaresai/olivares/modules/notify"
	"github.com/olivaresai/olivares/modules/observability"
	"github.com/olivaresai/olivares/modules/orchestration"
	postureexport "github.com/olivaresai/olivares/modules/posture-export"
	"github.com/olivaresai/olivares/modules/recording"
	"github.com/olivaresai/olivares/modules/redteam"
	"github.com/olivaresai/olivares/modules/reporting"
	"github.com/olivaresai/olivares/modules/sandbox"
	"github.com/olivaresai/olivares/modules/security"
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/modules/siemforward"
	"github.com/olivaresai/olivares/modules/sourcescope"
	"github.com/olivaresai/olivares/modules/voice"
)

// allModules mirrors the composition root's module slice (cmd/olivares/wire.go,
// `all`) with every module built from its DEFAULT construction. The inventory only
// asks each module two questions — what permissions do you declare, and what does
// each route require — and neither answer depends on the options wire.go passes
// (credentials, clocks, enterprise adapters, bus handles): those govern behavior,
// not the permission vocabulary.
//
// The list is written out rather than reflected over the package tree because Go
// has no way to enumerate implementations of an interface at runtime. That makes
// OMISSION the failure mode to defend against: a module missing here is a module
// whose permissions the inventory never learns about, so the console's legitimate
// checks against it get reported as invented — a false positive, which is how a
// guard earns being switched off. permsdump_test.go therefore cross-checks this
// list against wire.go's own module imports and fails when they diverge, so adding
// a module to the product cannot silently skip the inventory.
func allModules() []api.Module {
	// siemforward is the one module whose constructor takes a required dependency;
	// its permission surface does not depend on the eventing module's state.
	evt := eventing.New()
	return []api.Module{
		accessmap.New(),
		capabilities.New(),
		catalog.New(),
		claudeadoption.New(),
		compliance.New(),
		consoleviews.New(),
		deploy.New(),
		evals.New(),
		evt,
		finops.New(),
		governance.New(),
		// The authoring consoles: route-only modules that reuse governance's
		// tables and mount their own hyphenated REST namespaces. They are separate
		// api.Module values in wire.go and carry their own permissions, so an
		// inventory that only built governance.New() would miss three namespaces.
		governance.NewPolicyConsole(),
		governance.NewAgentsConsole(),
		governance.NewIdentityConsole(),
		health.New(),
		inferenceproxy.New(),
		inventory.New(),
		knowledge.New(),
		liveingest.New(),
		models.New(),
		notify.New(),
		observability.New(),
		orchestration.New(),
		postureexport.New(),
		recording.New(),
		redteam.New(),
		reporting.New(),
		sandbox.New(),
		security.New(),
		sessions.New(),
		siemforward.New(evt),
		sourcescope.New(),
		voice.New(),
	}
}
