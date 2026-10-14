// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"path"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// A removed edition reader must not resolve against another module's basename.
func localNoneCitationResolves(lineCounts map[string][]int, kind model.Kind, cited string, first, last int) bool {
	// Immutable descriptors retain their historical citation literals. Validate
	// moved readers at their current locations. Custom authorization authoring
	// moved to Business; its stored values still have these shared readers.
	type citation struct {
		file        string
		first, last int
	}
	relocated := map[citation]citation{
		{"cmd/olivares/mcpgateway.go", 1301, 1301}: {"cmd/olivares/internal/mcpgateway/auditor.go", 36, 36},
		{"cmd/olivares/mcpgateway.go", 1308, 1308}: {"cmd/olivares/internal/mcpgateway/auditor.go", 43, 43},
		{"scopedadmin_handlers.go", 511, 514}:      {"modules/governance/scopedadmin.go", 282, 288},
		{"scopedadmin_handlers.go", 985, 1003}:     {"modules/governance/scopedadmin.go", 348, 362},
		{"scopedadmin_handlers.go", 1018, 1023}:    {"modules/governance/scopedadmin.go", 266, 298},
		{"scopedadmin_handlers.go", 969, 969}:      {"modules/governance/scopedadmin_handlers.go", 117, 117},
		{"costcenter.go", 84, 84}:                  {"modules/finops/costcenter.go", 90, 90},
		{"helpers.go", 149, 152}:                   {"modules/sandbox/helpers.go", 126, 129},
		{"costcenter.go", 495, 498}:                {"modules/finops/costcenter.go", 90, 93},
		{"costcenter.go", 495, 495}:                {"modules/finops/costcenter.go", 90, 90},
		{"costcenter.go", 475, 475}:                {"modules/finops/costcenter.go", 67, 70},
		{"costcenter.go", 488, 488}:                {"modules/finops/costcenter.go", 83, 83},
		{"costcenter.go", 116, 116}:                {"modules/finops/costcenter.go", 45, 45},
		{"costcenter.go", 450, 450}:                {"modules/finops/costcenter.go", 45, 45},
		{"cmd/olivares/sandboxrt.go", 75, 89}:      {"cmd/olivares/sandboxrt.go", 56, 69},
		// Business: the red-team severity mapping and sandbox adapter sit where
		// their readers are now (helpers.go resolves as helpers_enterprise.go).
		{"helpers.go", 173, 183}:                {"modules/redteam/helpers.go", 151, 162},
		{"cmd/olivares/sandboxrt.go", 118, 122}: {"cmd/olivares/redteamsandbox_enterprise.go", 85, 124},
		{"cmd/olivares/sandboxrt.go", 115, 133}: {"cmd/olivares/redteamsandbox_enterprise.go", 35, 75},
		// The step-kind refusal stands 398 lines earlier than the literal says.
		{"workflow_graph.go", 878, 878}: {"modules/orchestration/workflow_graph.go", 480, 480},
		// Orchestration's dto.go ends at line 273: the actor kind is read at 269
		// and the decision ledger projection spans decisionDTO.
		{"dto.go", 284, 284}: {"modules/orchestration/dto.go", 269, 269},
		{"dto.go", 271, 288}: {"modules/orchestration/dto.go", 239, 273},
		// The inventory readers of the directory mirror moved from roster.go to
		// identity_inventory.go when the Business roster sync left Community.
		{"roster.go", 512, 512}: {"modules/governance/identity_inventory.go", 87, 87},
		{"roster.go", 522, 522}: {"modules/governance/identity_inventory.go", 97, 97},
		{"roster.go", 590, 590}: {"modules/governance/identity_inventory.go", 165, 165},
		{"roster.go", 623, 623}: {"modules/governance/identity_inventory.go", 198, 198},
	}
	namespace, _, _ := strings.Cut(string(kind), ".")
	// Bare citations can relocate only within their owning module. Explicit
	// paths name shared readers and retain their cross-module relocations.
	if current, ok := relocated[citation{cited, first, last}]; ok &&
		(strings.Contains(cited, "/") || strings.HasPrefix(current.file, "modules/"+namespace+"/")) {
		cited, last = current.file, current.last
	}
	switch namespace {
	case "finops", "redteam", "sandbox", "governance", "orchestration":
		if !strings.Contains(cited, "/") {
			cited = "modules/" + namespace + "/" + cited
		}
		files := []string{cited}
		// Business keeps its own module's reader under the Community file name
		// plus _enterprise, in the same directory.
		if strings.HasPrefix(cited, "modules/"+namespace+"/") {
			files = append(files, strings.TrimSuffix(cited, ".go")+"_enterprise.go")
		}
		for _, file := range files {
			for _, n := range lineCounts[file] {
				if n >= last {
					return true
				}
			}
		}
		return false
	}
	for rel, counts := range lineCounts {
		if rel != cited && !strings.HasSuffix(rel, "/"+cited) {
			continue
		}
		for _, n := range counts {
			if n >= last {
				return true
			}
		}
	}
	return false
}

// Historical custody is limited to Community and exact kind/reason/citation triples.
// Shared citations in the same reason must still resolve in the current tree.
func historicalBusinessNoneCitation(community bool, kind model.Kind, reason, cited string) bool {
	if !community {
		return false
	}
	for _, historical := range historicalBusinessNoneCitations[historicalNoneDeclaration{kind, reason}] {
		if cited == historical {
			return true
		}
	}
	return false
}

type historicalNoneDeclaration struct {
	kind   model.Kind
	reason string
}

// Immutable reasons retained when readers moved to Business in 6210f28b69d5
// (FinOps/red-team) and beab6e740a19 (break-glass). Declaration literals and
// historical readers were checked in each move's parent. The already-stale
// red-team helpers.go:148-151 and :173-183 literals trace to hashHex/hashBytes
// at modules/redteam/helpers.go:125-132 and sevToCore at :150-161 respectively.
// This is historical provenance; it makes no claim about the private readers'
// current implementation.
var historicalBusinessNoneCitations = map[historicalNoneDeclaration][]string{
	{"finops.cost_center", "operator display text or tags of a cost center, rendered only: costcenter.go:42-56"}:                                                                      {"costcenter.go:42-56"},
	{"redteam.result", "a closed severity set: helpers.go:173-183"}:                                                                                                                   {"helpers.go:173-183"},
	{"finops.cost_sample", "a SHA-256 of the row's natural key, used only to deduplicate: ingest.go:687-696, value.go:209-216"}:                                                       {"value.go:209-216"},
	{"finops.cost_sample", "a cost-center accounting code, copied onto samples from an active cost center and filtered on: costcenter.go:495-498, statements.go:184"}:                 {"statements.go:184"},
	{"finops.cost_sample", "a session's external id, resolved only against sessions: value.go:229-231"}:                                                                               {"value.go:229-231"},
	{"finops.cost_sample", "an agent's external id or name, never an account: value.go:239-245, value.go:259-262"}:                                                                    {"value.go:239-245", "value.go:259-262"},
	{"finops.seat_count", "the provider and UTC day of a seat-count snapshot: seats.go:110, seats.go:216-227"}:                                                                        {"seats.go:110", "seats.go:216-227"},
	{"finops.outcome", "a SHA-256 of the row's natural key, used only to deduplicate: ingest.go:687-696, value.go:209-216"}:                                                           {"value.go:209-216"},
	{"finops.outcome", "a session's external id, resolved only against sessions: value.go:229-231"}:                                                                                   {"value.go:229-231"},
	{"finops.outcome", "an agent's external id or name, never an account: value.go:239-245, value.go:259-262"}:                                                                        {"value.go:239-245", "value.go:259-262"},
	{"finops.outcome", "an outcome's subject kind, outcome id, verdict or source, rendered only: value.go:50-52, value.go:111, dto.go:312-324"}:                                       {"value.go:50-52", "value.go:111"},
	{"finops.cost_center", "a cost-center accounting code, copied onto samples from an active cost center and filtered on: costcenter.go:495-498, statements.go:184"}:                 {"statements.go:184"},
	{"finops.cost_center_mapping", "the id of the cost center a mapping rule or statement points at: costcenter.go:475, costcenter.go:488, statements.go:70"}:                         {"statements.go:70"},
	{"finops.model_rate", "a provider, model or note of the admin price sheet: ratecatalog.go:45-56, ratecatalog.go:309-310"}:                                                         {"ratecatalog.go:45-56", "ratecatalog.go:309-310"},
	{"finops.chargeback_statement", "a statement key, cost-center code or name, period or status, written by the generator and rendered: statements.go:67-85, statements.go:213-243"}: {"statements.go:67-85", "statements.go:213-243"},
	{"finops.chargeback_statement", "the id of the cost center a mapping rule or statement points at: costcenter.go:475, costcenter.go:488, statements.go:70"}:                        {"statements.go:70"},
	{"finops.statement_line", "the statement id or the model, provider or agent reference a line aggregates: statements.go:87-97, statements.go:266-270"}:                             {"statements.go:87-97", "statements.go:266-270"},
	{"redteam.target", "a closed status set registered|authorized|revoked: consent.go:99, consent.go:161, consent.go:165"}:                                                            {"consent.go:99", "consent.go:161", "consent.go:165"},
	{"redteam.target", "a display label that defaults to the agent reference and is only rendered: consent.go:75-78, consent.go:41"}:                                                  {"consent.go:75-78", "consent.go:41"},
	{"redteam.target", "an agent's canonical id or source external id, resolved only against the tenant's agent inventory: ownership.go:26-47"}:                                       {"ownership.go:26-47"},
	{"redteam.target", "free-text testing scope, rendered only and never handed to the sandbox: consent.go:42, cmd/olivares/sandboxrt.go:115-133"}:                                    {"consent.go:42", "cmd/olivares/sandboxrt.go:115-133"},
	{"redteam.target", "the network endpoint of the agent under test, parsed only into an egress host rule: cmd/olivares/sandboxrt.go:118-122"}:                                       {"cmd/olivares/sandboxrt.go:118-122"},
	{"redteam.run", "a closed status set completed|degraded|error: scorecard.go:67-78"}:                                                                                               {"scorecard.go:67-78"},
	{"redteam.run", "a closed suite set: battery.go:26-27, scorecard.go:107"}:                                                                                                         {"battery.go:26-27", "scorecard.go:107"},
	{"redteam.run", "a one-way SHA-256 hex digest: helpers.go:148-151, scorecard.go:169, scorecard.go:191"}:                                                                           {"helpers.go:148-151", "scorecard.go:169", "scorecard.go:191"},
	{"redteam.run", "the id of a red-team target row: scorecard.go:98, scorecard.go:171"}:                                                                                             {"scorecard.go:98", "scorecard.go:171"},
	{"redteam.result", "a closed outcome set: ports.go:63-70, battery.go:67-68"}:                                                                                                      {"battery.go:67-68"},
	{"redteam.result", "a closed probe family set: battery.go:18-23"}:                                                                                                                 {"battery.go:18-23"},
	{"redteam.result", "a one-way SHA-256 hex digest: helpers.go:148-151, scorecard.go:169, scorecard.go:191"}:                                                                        {"helpers.go:148-151", "scorecard.go:169", "scorecard.go:191"},
	{"redteam.result", "a probe id from the fixed probe catalog: attacks_injection.go:18, scorecard.go:189"}:                                                                          {"attacks_injection.go:18", "scorecard.go:189"},
	{"redteam.result", "an OWASP or ATLAS reference id from the fixed probe catalog: ports.go:29-31, scorecard.go:190"}:                                                               {"scorecard.go:190"},
	{"redteam.result", "the id of a red-team run row: scorecard.go:180, scorecard.go:189"}:                                                                                            {"scorecard.go:180", "scorecard.go:189"},
	{"governance.breakglass", "a constant sentinel backing the one-unreviewed-grant index: breakglass.go:81, breakglass.go:269"}:                                                      {"breakglass.go:81", "breakglass.go:269"},
	{"governance.breakglass", "a lifecycle status from a closed set: breakglass.go:144"}:                                                                                              {"breakglass.go:144"},
	{"governance.breakglass", "an action pattern matched against the consumed action only: breakglass.go:652"}:                                                                        {"breakglass.go:652"},
	{"governance.breakglass", "bounded operator prose, rendered only: breakglass.go:130"}:                                                                                             {"breakglass.go:130"},
	{"governance.breakglass", "bounded operator prose, rendered only: breakglass.go:135"}:                                                                                             {"breakglass.go:135"},
	{"governance.breakglass_use", "the caller-declared subject of the use, recorded and rendered only: breakglass.go:413"}:                                                            {"breakglass.go:413"},
	{"governance.breakglass_use", "the consumed action name, rendered only: breakglass.go:412"}:                                                                                       {"breakglass.go:412"},
	{"governance.breakglass_use", "the id of the break-glass grant that was used: breakglass.go:406"}:                                                                                 {"breakglass.go:406"},

	// Identity & Scale readers moved to Business in 46cfbf0c. Each citation was checked in that commit's parent.
	// Some were already approximate there: handlers:143 is the brace after the event read at :139; handlers:372 and :375 are the
	// block that declares and reads source, target and kind; agentidentity:269 returns on the empty reference (the agent-kind
	// comparison is at :279 and :289).
	{"governance.nhi_lifecycle", "the provider an identity comes from, rendered and used to pick a rotation actuator: nhilifecycle.go:267, nhilifecycle_handlers.go:372"}: {"nhilifecycle_handlers.go:372"},
	{"governance.nhi_lifecycle", "an operator-declared actuation target handed to the rotation actuator: nhilifecycle_handlers.go:375"}:                                   {"nhilifecycle_handlers.go:375"},
	{"governance.nhi_lifecycle", "ok, stale or unknown, counted by status only: reportsource.go:65, nhilifecycle_sweep.go:431"}:                                           {"nhilifecycle_sweep.go:431"},
	{"governance.nhi_lifecycle", "monitor, alert or blocked, compared at nhilifecycle_sweep.go:492"}:                                                                      {"nhilifecycle_sweep.go:492"},
	{"governance.nhi_lifecycle", "a lifecycle reason returned with a block and rendered: nhilifecycle_sweep.go:494, nhilifecycle.go:277"}:                                 {"nhilifecycle_sweep.go:494"},
	{"governance.nhi_lifecycle", "empty or agent, compared at agentidentity.go:269"}:                                                                                      {"agentidentity.go:269"},
	{"governance.nhi_lifecycle_event", "a lifecycle event name, rendered only: nhilifecycle_handlers.go:143"}:                                                             {"nhilifecycle_handlers.go:143"},
	// The directory roster sync stayed in Business (roster.go:339, :369, :399, :277 of 46cfbf0c's parent); the inventory
	// readers the same declarations cite moved to identity_inventory.go and relocate above.
	{"governance.collection", "directory group metadata whose email, UPN and credential keys are dropped before storage (roster.go:51, roster.go:399); no reader decodes it: roster.go:512"}: {"roster.go:399"},
	{"governance.collection", "a directory source name, a lookup key and a rendered label: roster.go:339, roster.go:512"}:                                                                    {"roster.go:339"},
	{"governance.collection", "a directory group ref, a lookup key and a rendered label: roster.go:339, roster.go:512"}:                                                                      {"roster.go:339"},
	{"governance.collection_member", "a directory source name, part of the edge's lookup key: roster.go:369"}:                                                                                {"roster.go:369"},
	{"core.identity", "closed governance keys and allow-listed, non-identifying directory attributes: modules/governance/roster.go:45, modules/governance/roster.go:277"}:                    {"modules/governance/roster.go:277"},
	{"core.set_seen_jti", "a SET publisher id, compared to refuse a replay and resolved to a publisher: core/auth/set_publisher.go:237, core/auth/caep_events.go:182"}:                       {"core/auth/caep_events.go:182"},
}

func TestHistoricalBusinessNoneCitationsAreExact(t *testing.T) {
	for _, tc := range []struct {
		kind          model.Kind
		reason, cited string
	}{
		{"finops.cost_sample", "a SHA-256 of the row's natural key, used only to deduplicate: ingest.go:687-696, value.go:209-216", "value.go:209-216"},
		{"redteam.target", "the network endpoint of the agent under test, parsed only into an egress host rule: cmd/olivares/sandboxrt.go:118-122", "cmd/olivares/sandboxrt.go:118-122"},
		{"redteam.run", "a one-way SHA-256 hex digest: helpers.go:148-151, scorecard.go:169, scorecard.go:191", "helpers.go:148-151"},
		{"redteam.result", "a closed severity set: helpers.go:173-183", "helpers.go:173-183"},
		{"finops.cost_center", "operator display text or tags of a cost center, rendered only: costcenter.go:42-56", "costcenter.go:42-56"},
		{"governance.breakglass", "an action pattern matched against the consumed action only: breakglass.go:652", "breakglass.go:652"},
		{"governance.collection", "a directory source name, a lookup key and a rendered label: roster.go:339, roster.go:512", "roster.go:339"},
		{"governance.collection", "a directory group ref, a lookup key and a rendered label: roster.go:339, roster.go:512", "roster.go:339"},
		{"governance.collection", "directory group metadata whose email, UPN and credential keys are dropped before storage (roster.go:51, roster.go:399); no reader decodes it: roster.go:512", "roster.go:399"},
		{"governance.collection_member", "a directory source name, part of the edge's lookup key: roster.go:369", "roster.go:369"},
		{"core.identity", "closed governance keys and allow-listed, non-identifying directory attributes: modules/governance/roster.go:45, modules/governance/roster.go:277", "modules/governance/roster.go:277"},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			if !historicalBusinessNoneCitation(true, tc.kind, tc.reason, tc.cited) {
				t.Error("lost exact immutable historical citation")
			}
			if historicalBusinessNoneCitation(false, tc.kind, tc.reason, tc.cited) {
				t.Error("Business must resolve its actual reader")
			}
			for _, kind := range []model.Kind{"notify.route", tc.kind + "_new"} {
				if historicalBusinessNoneCitation(true, kind, tc.reason, tc.cited) {
					t.Errorf("historical custody covered foreign kind %s", kind)
				}
			}
			for _, reason := range []string{tc.reason + " changed", strings.ReplaceAll(tc.reason, tc.cited, tc.cited+"0"), "new missing reader: " + tc.cited} {
				if historicalBusinessNoneCitation(true, tc.kind, reason, tc.cited) {
					t.Errorf("admitted unrecorded reason %q", reason)
				}
			}
			for _, cited := range []string{tc.cited + "0", "missing.go:1", "ingest.go:687-696"} {
				if historicalBusinessNoneCitation(true, tc.kind, tc.reason, cited) {
					t.Errorf("admitted unrecorded or shared citation %q", cited)
				}
			}
		})
	}
}

func TestEditionNoneCitationsDoNotResolveForeignModuleBasenames(t *testing.T) {
	for _, kind := range []model.Kind{"finops.cost_sample", "redteam.result", "governance.breakglass", "sandbox.scenario", "orchestration.workflow"} {
		t.Run(string(kind), func(t *testing.T) {
			namespace, _, _ := strings.Cut(string(kind), ".")
			own := "modules/" + namespace + "/helpers.go"
			counts := map[string][]int{"modules/notify/helpers.go": {200}}
			if localNoneCitationResolves(counts, kind, "helpers.go", 183, 183) {
				t.Error("an unrelated module supplied the missing reader")
			}
			counts[own] = []int{182}
			if localNoneCitationResolves(counts, kind, "helpers.go", 183, 183) {
				t.Error("an unrelated module supplied the out-of-range reader")
			}
			counts[own] = []int{183}
			if !localNoneCitationResolves(counts, kind, "helpers.go", 183, 183) {
				t.Error("lost the owning module's reader")
			}
			if !localNoneCitationResolves(counts, kind, "modules/notify/helpers.go", 183, 183) {
				t.Error("lost an explicit shared-reader path")
			}
		})
	}
}

// Exercise the same relocation and resolution path as the composed census guard.
// A bare alias is evidence only for the module that owns its shared reader.
func TestEditionNoneCitationRelocationsRespectOwnership(t *testing.T) {
	for _, tc := range []struct {
		kind                     model.Kind
		file                     string
		first, last, currentLast int
	}{
		{"sandbox.scenario", "helpers.go", 149, 152, 129},
		{"finops.cost_center", "costcenter.go", 84, 84, 90},
		{"finops.cost_sample", "costcenter.go", 495, 498, 93},
		{"finops.cost_center", "costcenter.go", 495, 495, 90},
		{"finops.cost_center_mapping", "costcenter.go", 475, 475, 70},
		{"finops.cost_center_mapping", "costcenter.go", 488, 488, 83},
		{"finops.cost_center_mapping", "costcenter.go", 116, 116, 45},
		{"finops.cost_center_mapping", "costcenter.go", 450, 450, 45},
		{"redteam.result", "helpers.go", 173, 183, 162},
		{"orchestration.workflow", "workflow_graph.go", 878, 878, 480},
		{"orchestration.schedule", "dto.go", 284, 284, 269},
		{"orchestration.decision", "dto.go", 271, 288, 273},
	} {
		t.Run(fmt.Sprintf("%s:%d-%d", tc.file, tc.first, tc.last), func(t *testing.T) {
			namespace, _, _ := strings.Cut(string(tc.kind), ".")
			shared := "modules/" + namespace + "/" + tc.file
			counts := map[string][]int{shared: {tc.currentLast}}
			if !localNoneCitationResolves(counts, tc.kind, tc.file, tc.first, tc.last) {
				t.Error("lost the owning module's relocated reader")
			}
			for _, kind := range []model.Kind{"finops.cost_sample", "redteam.result", "sandbox.scenario", "governance.breakglass"} {
				foreignNamespace, _, _ := strings.Cut(string(kind), ".")
				if foreignNamespace == namespace {
					continue
				}
				t.Run(string(kind), func(t *testing.T) {
					own := "modules/" + foreignNamespace + "/" + tc.file
					for _, count := range []int{0, tc.last - 1} {
						counts[own] = []int{count}
						if localNoneCitationResolves(counts, kind, tc.file, tc.first, tc.last) {
							t.Errorf("a foreign relocation hid a missing or short reader (own lines=%d)", count)
						}
					}
					if !localNoneCitationResolves(counts, kind, shared, tc.currentLast, tc.currentLast) {
						t.Error("lost an explicit shared-reader path")
					}
				})
			}
			counts[shared] = []int{tc.currentLast - 1}
			if localNoneCitationResolves(counts, tc.kind, tc.file, tc.first, tc.last) {
				t.Error("admitted an out-of-range relocated reader")
			}
		})
	}
}

// The directory inventory readers live in identity_inventory.go; roster.go keeps
// only the Business seam, so a bare roster.go citation relocates for governance
// kinds alone and only while identity_inventory.go still has the cited line.
func TestRosterInventoryCitationsRelocateToIdentityInventory(t *testing.T) {
	const inventory = "modules/governance/identity_inventory.go"
	for _, tc := range []struct {
		kind           model.Kind
		cited, current int
	}{
		{"governance.collection", 512, 87},
		{"governance.collection", 522, 97},
		{"governance.collection_member", 590, 165},
		{"governance.collection_member", 623, 198},
	} {
		t.Run(fmt.Sprintf("%s:%d", tc.kind, tc.cited), func(t *testing.T) {
			counts := map[string][]int{"modules/governance/roster.go": {95}}
			if localNoneCitationResolves(counts, tc.kind, "roster.go", tc.cited, tc.cited) {
				t.Error("the short roster.go resolved a reader it does not have")
			}
			counts[inventory] = []int{tc.current - 1}
			if localNoneCitationResolves(counts, tc.kind, "roster.go", tc.cited, tc.cited) {
				t.Error("admitted an out-of-range relocated reader")
			}
			counts[inventory] = []int{tc.current}
			if !localNoneCitationResolves(counts, tc.kind, "roster.go", tc.cited, tc.cited) {
				t.Error("lost the relocated inventory reader")
			}
			if localNoneCitationResolves(counts, "finops.cost_sample", "roster.go", tc.cited, tc.cited) {
				t.Error("a foreign module borrowed the governance relocation")
			}
		})
	}
}

// The four inventory readers must resolve through the relocation, which checks them in
// identity_inventory.go. Recording any of them as Business custody would hide a broken
// relocation, so no historical entry may admit them.
func TestHistoricalCustodyDoesNotCoverRelocatedRosterReaders(t *testing.T) {
	for declaration := range historicalBusinessNoneCitations {
		for _, cited := range []string{"roster.go:512", "roster.go:522", "roster.go:590", "roster.go:623"} {
			if historicalBusinessNoneCitation(true, declaration.kind, declaration.reason, cited) {
				t.Errorf("%s admits %s as historical custody", declaration.kind, cited)
			}
		}
	}
}

// Business keeps an immutable declaration's citation while the file that holds
// its reader differs: the module's own <name>_enterprise.go, or the file the
// Business wiring moved to. Like the census, the check is a presence check: the
// citation resolves at that file once the file has the cited last line, and
// never at another module's file of the same name.
func TestBusinessNoneCitationsResolveWhereTheirReadersMoved(t *testing.T) {
	for _, tc := range []struct {
		kind        model.Kind
		cited       string
		first, last int
		file        string // the file of the assembled Business tree the citation resolves to
		lines       int    // the line count that file must reach
	}{
		{"finops.cost_sample", "value.go", 209, 216, "modules/finops/value_enterprise.go", 216},
		{"finops.cost_sample", "statements.go", 184, 184, "modules/finops/statements_enterprise.go", 184},
		{"redteam.target", "consent.go", 75, 78, "modules/redteam/consent_enterprise.go", 78},
		{"redteam.result", "helpers.go", 173, 183, "modules/redteam/helpers_enterprise.go", 162},
		{"redteam.target", "cmd/olivares/sandboxrt.go", 118, 122, "cmd/olivares/redteamsandbox_enterprise.go", 124},
		{"redteam.target", "cmd/olivares/sandboxrt.go", 115, 133, "cmd/olivares/redteamsandbox_enterprise.go", 75},
		{"governance.breakglass", "breakglass.go", 130, 130, "modules/governance/breakglass_enterprise.go", 130},
		{"orchestration.workflow", "workflow_graph.go", 878, 878, "modules/orchestration/workflow_graph.go", 480},
	} {
		t.Run(fmt.Sprintf("%s/%s:%d-%d", tc.kind, tc.cited, tc.first, tc.last), func(t *testing.T) {
			if !localNoneCitationResolves(map[string][]int{tc.file: {tc.lines}}, tc.kind, tc.cited, tc.first, tc.last) {
				t.Errorf("%s with %d lines does not resolve the citation", tc.file, tc.lines)
			}
			if localNoneCitationResolves(map[string][]int{tc.file: {tc.lines - 1}}, tc.kind, tc.cited, tc.first, tc.last) {
				t.Errorf("admitted %s with %d lines, short of the cited line", tc.file, tc.lines-1)
			}
			if !strings.HasPrefix(tc.file, "modules/") {
				return
			}
			namespace, _, _ := strings.Cut(string(tc.kind), ".")
			for _, other := range []string{"finops", "redteam", "governance", "sandbox", "notify", "security"} {
				foreign := "modules/" + other + "/" + path.Base(tc.file)
				if other != namespace && localNoneCitationResolves(map[string][]int{foreign: {1 << 20}}, tc.kind, tc.cited, tc.first, tc.last) {
					t.Errorf("another module's %s supplied the citation", foreign)
				}
			}
		})
	}
}

// The orchestration reader file of the assembled Business tree has 273 lines. A
// longer dto.go of another module is no evidence for a citation of line 284 or
// of lines 271-288. The own reader resolves them at the lines it really has
// and not one line short.
func TestOrchestrationDtoCitationsDoNotResolveAgainstOtherModules(t *testing.T) {
	foreign := map[string][]int{"modules/security/dto.go": {293}, "modules/finops/dto.go": {518}}
	for _, tc := range []struct {
		kind                     model.Kind
		first, last, currentLast int
	}{
		{"orchestration.schedule", 284, 284, 269},
		{"orchestration.decision", 271, 288, 273},
	} {
		t.Run(fmt.Sprintf("%s:%d-%d", tc.kind, tc.first, tc.last), func(t *testing.T) {
			if localNoneCitationResolves(foreign, tc.kind, "dto.go", tc.first, tc.last) {
				t.Error("another module's dto.go supplied the orchestration reader")
			}
			own := map[string][]int{"modules/orchestration/dto.go": {273}, "modules/security/dto.go": {293}}
			if !localNoneCitationResolves(own, tc.kind, "dto.go", tc.first, tc.last) {
				t.Error("lost the orchestration reader at its relocated lines")
			}
			short := map[string][]int{"modules/orchestration/dto.go": {tc.currentLast - 1}, "modules/security/dto.go": {293}}
			if localNoneCitationResolves(short, tc.kind, "dto.go", tc.first, tc.last) {
				t.Error("admitted an orchestration reader short of its relocated lines")
			}
		})
	}
}
