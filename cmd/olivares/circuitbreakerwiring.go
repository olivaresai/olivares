// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

// circuitbreakerwiring.go is the AGPL composition-root glue for the OPTIONAL commercial
// runtime circuit-breaker (enterprise/circuitbreaker). Same shape as
// incidentloopwiring.go: the build-independent seams live here and the closed engine is
// reached only through newCircuitBreakerEngine (real in the enterprise overlay, nil in
// wire_noenterprise.go), so the default artifact never references the closed module.
//
// WHY THIS FILE EXISTS AT ALL. It did not, and that was the defect. Everything else
// was already built -- the interface, the gate, the engine, the open stub -- and NOTHING
// CONNECTED THEM. Measured before writing it: newCircuitBreakerEngine was declared in
// wire_noenterprise.go and called by nobody, the enterprise overlay never declared it at
// all, and inferenceProxyDecider.circuitBreaker was never assigned at any construction
// site. The field was permanently nil, circuitBreakerGateCheck always took its nil path,
// and the breaker had never tripped in any build that shipped -- while the catalog sold
// it.
//
// A breaker needs BOTH halves. State() answers the inference gate; OnFinding drives the
// state machine that lets State() ever say "open". Wiring only the consult side would have
// satisfied the linkage gate while still shipping a breaker that never trips, which is
// worse than leaving it visibly dead.

import (
	"context"
	"log/slog"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
)

// Ext entity kinds and tables for the breaker's persisted state. Named here, in the open
// tree, for the same reason the tool-pin table is (toolpinpersist.go): SCHEMA IS ENGINE
// SCHEMA. It is registered in EVERY edition so the two artifacts describe the same
// database -- which is not a preference but a gate, `task lint:schema-parity` compares
// community against enterprise and an enterprise-only table would fail it. Registering the
// schema in both editions is also what makes an in-place edition swap safe.
const (
	cbRuleKind   = "governance.cb_rule"
	cbRuleTable  = "governance_cb_rule"
	cbStateKind  = "governance.cb_state"
	cbStateTable = "governance_cb_state"
)

// registerCircuitBreakerSchema registers the breaker's two ext entities. Only the
// enterprise engine ever writes to them; the community artifact carries the tables and
// leaves them empty, exactly like the tool-pin table.
func registerCircuitBreakerSchema(reg store.ExtensionRegistry) error {
	if err := reg.Register(model.EntityDescriptor{
		Kind:  cbRuleKind,
		Table: cbRuleTable,
		Fields: []model.FieldSpec{
			{Name: "name", Kind: model.KindText, Indexed: true},
			{Name: "enabled", Kind: model.KindBool, Indexed: true},
			{Name: "agent_tier", Kind: model.KindText, Nullable: true},
			{Name: "trigger_kind", Kind: model.KindText},
			{Name: "match_kinds", Kind: model.KindText, Nullable: true},
			{Name: "min_severity", Kind: model.KindText},
			{Name: "threshold", Kind: model.KindInt},
			{Name: "window_seconds", Kind: model.KindInt},
			{Name: "response", Kind: model.KindText},
			{Name: "cooldown_seconds", Kind: model.KindInt, Nullable: true},
			{Name: "escalation_trips", Kind: model.KindInt, Nullable: true},
			{Name: "created_by", Kind: model.KindText},
			{Name: "note", Kind: model.KindText, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			Name:    "governance_cb_rule_uniq",
			Columns: []string{model.ColTenantID, "name"},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}
	return reg.Register(model.EntityDescriptor{
		Kind:  cbStateKind,
		Table: cbStateTable,
		Fields: []model.FieldSpec{
			{Name: "rule_id", Kind: model.KindUUID, Indexed: true},
			{Name: "agent_ref", Kind: model.KindText, Indexed: true},
			{Name: "state", Kind: model.KindText, Indexed: true},
			{Name: "trip_count", Kind: model.KindInt},
			{Name: "current_count", Kind: model.KindInt},
			{Name: "window_start", Kind: model.KindTimestamp, Nullable: true},
			{Name: "tripped_at", Kind: model.KindTimestamp, Nullable: true},
			{Name: "resets_at", Kind: model.KindTimestamp, Nullable: true},
			{Name: "killswitch_id", Kind: model.KindUUID, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			Name:    "governance_cb_state_uniq",
			Columns: []string{model.ColTenantID, "rule_id", "agent_ref"},
			Unique:  true,
		}},
	})
}

// agentTierReader is the narrow slice of the governance module the breaker consults to
// decide whether a rule scoped to an agent tier applies. *governance.Module satisfies it
// (modules/governance/agentrisk.go:506). Declared here so the closed engine depends on a
// seam rather than on the governance module itself.
type agentTierReader interface {
	AgentEffectiveTier(ctx context.Context, tenant model.TenantID, agentRef string) (string, error)
}

// subscribeCircuitBreaker binds the engine to the finding rail. Findings are what trip a
// breaker, so without this the engine would answer "closed" forever and the add-on would be
// decoration.
//
// Nil-safe: with no enterprise build, or an enterprise build with no circuit-breaker
// config, engine is nil and this is a no-op — the default artifact stays byte-identical in
// behavior. A subscribe failure leaves the breaker INERT rather than failing boot: a
// breaker that cannot see findings must not also take the engine down, and the
// kill-switch remains the hard stop either way.
func subscribeCircuitBreaker(engine circuitBreakerEngine, bus eventbus.Bus, log *slog.Logger) {
	if engine == nil {
		return
	}
	// ClassEnforcement, not ClassNotify: this subscriber CHANGES enforcement state (it
	// trips breakers that later deny requests), which is a different class from the
	// passive incident and notify sinks.
	if _, err := subscribeClassed(bus, eventbus.ClassEnforcement, "circuit-breaker",
		[]event.Type{event.TypeFindingReported}, engine.OnFinding); err != nil {
		log.Warn("circuit-breaker: bus subscription failed; breaker inactive (it can never trip)", "err", err)
	}
}

// circuitBreakerDeps is what the closed engine needs from the composition root, gathered so
// the constructor keeps one shape across both wire files.
type circuitBreakerDeps struct {
	Data api.ModuleData
	Gov  agentTierReader
}
