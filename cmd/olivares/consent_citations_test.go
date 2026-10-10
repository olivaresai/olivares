// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
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
	}
	namespace, _, _ := strings.Cut(string(kind), ".")
	// Bare citations can relocate only within their owning module. Explicit
	// paths name shared readers and retain their cross-module relocations.
	if current, ok := relocated[citation{cited, first, last}]; ok &&
		(strings.Contains(cited, "/") || strings.HasPrefix(current.file, "modules/"+namespace+"/")) {
		cited, last = current.file, current.last
	}
	switch namespace {
	case "finops", "redteam", "sandbox", "governance":
		if !strings.Contains(cited, "/") {
			cited = "modules/" + namespace + "/" + cited
		}
		for _, n := range lineCounts[cited] {
			if n >= last {
				return true
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
	for _, kind := range []model.Kind{"finops.cost_sample", "redteam.result", "governance.breakglass", "sandbox.scenario"} {
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
