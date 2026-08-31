// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// attribution is one normalized cost event with its resolved attribution, carried
// through ingestion and budget evaluation. Names are the connector's natural refs;
// ids are the resolved core-entity ids for the canonical ledger.
type attribution struct {
	ProviderRef string
	ModelRef    string
	SessionRef  string
	AgentRef    string
	Team        string
	Project     string

	ProviderID model.ID
	ModelID    model.ID
	SessionID  model.ID
	AgentID    model.ID

	InputTokens  int64
	OutputTokens int64
	CostMicroUSD int64
	OccurredAt   time.Time

	// Dimensions carried from the extended CostSample.
	Provenance            string
	WorkspaceRef          string
	APIKeyRef             string
	Actor                 string
	ServiceTier           string
	ContextWindow         string
	InferenceGeo          string
	Gateway               string
	CostType              string
	CacheReadTokens       int64
	CacheCreation1hTokens int64
	CacheCreation5mTokens int64

	// Firm identity: the roster Identity the spend resolves to (NHI/SPIFFE/
	// service-account), the FIRM key a per-identity dollar budget scopes on — distinct
	// from the free-text Actor/APIKeyRef/WorkspaceRef above. Populated by resolveIdentity
	// (agent.IdentityID, else api_key/actor matched to a roster Identity.ExternalID);
	// empty when nothing firm resolves (never fabricated). IdentityRef is the roster
	// Identity.ExternalID; Kind/Source carry its classification + provenance.
	IdentityRef    string
	IdentityKind   string
	IdentitySource string

	// RoutineRef: the Claude Code Routine (trigger) id that originated the
	// spend, if known. Populated by the pre-flight seam or resolved from the session's
	// trigger attribution. Enables per-routine budgets.
	RoutineRef string

	// CostCenterRef: the resolved accounting cost center code, resolved at
	// ingestion from the cost_center_mapping rules (resolveCostCenter). Empty when
	// no mapping matches — unmapped traffic.
	CostCenterRef string

	// Group refs are preventive-only attribution supplied or resolved by CheckBudget.
	// The cost read-model intentionally has no group columns; group budgets aggregate
	// by member fan-out over actor/agent_ref instead.
	UserGroupRefs  []string
	AgentGroupRefs []string

	// Labels are the sample's operator-supplied attribution tags (e.g.
	// OTEL_RESOURCE_ATTRIBUTES from a Claude Code fleet). team/project are
	// PROMOTED into the Team/Project dimensions above (agent labels win); the
	// rest are persisted on the canonical ledger metadata only. They never join
	// the dedup natural key.
	Labels map[string]string
}

// auditHook, when non-nil, is appended to the SAME store transaction as the cost
// ingest so a privileged write is audited atomically with its effect. The bus
// ingest path passes nil (connector-sourced samples are system ingestion, not a
// principal action and already deny-closed to the bus).
type auditHook func(context.Context, store.Scope) error

// onCost ingests one cost sample. Dedup is by NATURAL key (provider/model/session/
// instant + every attribution dimension + provenance) — NOT a content hash that
// includes the value — so a re-pulled time bucket whose value grew (an open/current
// bucket) or re-settled (cost_report late settlement) is an UPSERT that REPLACES the
// row's tokens/cost, never a second row that double-counts. The read-model row and
// its linked CostRecord ledger entry are updated together. BILLED samples are
// reconciliation data and are NOT written to the canonical CostRecord ledger, so the
// ledger stays single-provenance (estimated) and its consumers never mix streams. The
// whole thing is one Mutate so it commits atomically; FindingReports are published
// only AFTER commit so a rolled-back evaluation never emits a phantom alert.
func (m *Module) onCost(ctx context.Context, tenant model.TenantID, cost sdkmodel.CostSample, audit auditHook) error {
	if cost.ProviderRef == "" && cost.ModelRef == "" {
		return nil
	}
	at := cost.OccurredAt
	if at.IsZero() {
		at = m.clock.Now().Time()
	}
	var pending []pendingAlert
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		// A privileged HTTP ingest carries an audit hook appended in THIS
		// transaction, so the principal's action is recorded atomically with its
		// ledger effect (a rolled-back ingest leaves no phantom audit, and a
		// committed ingest can never be unaudited). It runs regardless of the dedup
		// outcome below — the audited fact is "principal X pushed this cost sample".
		// The bus path passes nil: system ingestion from a connector is not a
		// principal action.
		if audit != nil {
			if err := audit(ctx, sc); err != nil {
				return err
			}
		}
		attr := attribution{
			ProviderRef: cost.ProviderRef, ModelRef: cost.ModelRef, SessionRef: cost.SessionRef,
			InputTokens: cost.InputTokens, OutputTokens: cost.OutputTokens,
			CostMicroUSD: cost.CostMicroUSD, OccurredAt: at,
			Provenance:            provenanceOf(cost.Provenance),
			WorkspaceRef:          cost.WorkspaceRef,
			APIKeyRef:             cost.APIKeyRef,
			Actor:                 cost.Actor,
			ServiceTier:           cost.ServiceTier,
			ContextWindow:         cost.ContextWindow,
			InferenceGeo:          cost.InferenceGeo,
			Gateway:               gatewayOf(cost.Gateway),
			CostType:              cost.CostType,
			CacheReadTokens:       cost.CacheReadTokens,
			CacheCreation1hTokens: cost.CacheCreation1hTokens,
			CacheCreation5mTokens: cost.CacheCreation5mTokens,
			// Operator-supplied labels: team/project seed the queryable
			// dimensions; resolveSession lets the agent's CURATED labels outrank
			// them. The remaining labels ride the ledger metadata (writeCostRecord).
			Team:    cost.Labels["team"],
			Project: cost.Labels["project"],
			Labels:  cost.Labels,
		}
		if err := resolveSession(ctx, sc, &attr); err != nil {
			return err
		}
		// Resolve the FIRM roster identity the spend is attributed to, so a
		// per-identity dollar budget can scope on it. Derived from the same dimensions
		// already in the dedup natural key (session→agent, api_key, actor), so it is
		// deterministic per row — stamped, not part of the key.
		if err := resolveIdentity(ctx, sc, &attr); err != nil {
			return err
		}
		// Resolve the cost center from the mapping rules. Derived from the
		// same attribution dimensions already in the natural key, so it is
		// deterministic and stamped, not part of the key.
		if err := resolveCostCenter(ctx, sc, &attr); err != nil {
			return err
		}
		repo, err := sc.Ext(costSampleKind)
		if err != nil {
			return err
		}

		nk := naturalKey(attr)
		existing, found, err := findSample(ctx, repo, nk)
		if err != nil {
			return err
		}
		if found {
			if !sampleChanged(existing, attr) {
				return nil // exact re-delivery: unchanged bucket, do not re-count
			}
			// Re-pull of a grown/re-settled bucket: replace the value in place.
			applySampleValues(existing, attr)
			if _, err := repo.Update(ctx, existing); err != nil {
				return err
			}
			if attr.Provenance != provenanceBilled {
				if crID := existing.String(colCostRecordID); crID != "" {
					if err := updateCostRecord(ctx, sc, model.ID(crID), attr); err != nil {
						return err
					}
				}
			}
		} else {
			// A new bucket. Resolve ids, write the canonical ledger (estimated only),
			// and link the read-model row to its ledger entry.
			if attr.ProviderRef != "" {
				if attr.ProviderID, err = foProvider(ctx, sc, attr.ProviderRef); err != nil {
					return err
				}
			}
			if attr.ModelRef != "" {
				if attr.ModelID, err = foModel(ctx, sc, attr.ModelRef, attr.ProviderID); err != nil {
					return err
				}
			}
			var crID model.ID
			if attr.Provenance != provenanceBilled {
				if crID, err = writeCostRecord(ctx, sc, attr); err != nil {
					return err
				}
			}
			if _, err := repo.Create(ctx, sampleRecord(nk, crID, attr)); err != nil {
				if errors.Is(err, store.ErrConflict) {
					return nil // raced with a concurrent insert of the same bucket
				}
				return err
			}
		}

		alerts, err := m.evaluateBudgets(ctx, sc, attr)
		if err != nil {
			return err
		}
		pending = alerts
		return nil
	})
	if err != nil {
		return err
	}
	for _, a := range pending {
		m.emitAlert(ctx, tenant, a)
	}
	return nil
}

// findSample returns the read-model row for a natural key, or found=false.
func findSample(ctx context.Context, repo store.GenericRepo, nk string) (model.Record, bool, error) {
	rows, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{eq(colSampleKey, nk)}, Limit: 1})
	if err != nil || len(rows) == 0 {
		return nil, false, err
	}
	return rows[0], true, nil
}

// sampleChanged reports whether the ingested values differ from the stored row — the
// mutable token/cost fields, OR a firm identity that NEWLY resolves. The latter
// is essential: a closed/settled bucket first ingested before the roster linked its
// api-key/agent is re-pulled byte-identical AFTER the link, and without the identity
// check sampleChanged would say "unchanged" and the now-resolvable identity would be
// dropped on the read-model row and the ledger. The attr.IdentityRef != "" guard keeps
// the never-blank semantics (an unresolved re-pull never clears a prior attribution).
// An otherwise-unchanged re-delivery stays a no-op (no re-count).
func sampleChanged(rec model.Record, attr attribution) bool {
	return rec.Int(colCostMicroUSD) != attr.CostMicroUSD ||
		rec.Int(colInputTokens) != attr.InputTokens ||
		rec.Int(colOutputTokens) != attr.OutputTokens ||
		rec.Int(colCacheReadTokens) != attr.CacheReadTokens ||
		rec.Int(colCacheCreation1hTokens) != attr.CacheCreation1hTokens ||
		rec.Int(colCacheCreation5mTokens) != attr.CacheCreation5mTokens ||
		(attr.IdentityRef != "" && rec.String(colIdentityRef) != attr.IdentityRef)
}

// applySampleValues overwrites the mutable value columns on an existing row, leaving
// the natural-key columns (provider/model/dims) and base fields intact.
func applySampleValues(rec model.Record, attr attribution) {
	rec[colInputTokens] = attr.InputTokens
	rec[colOutputTokens] = attr.OutputTokens
	rec[colCostMicroUSD] = attr.CostMicroUSD
	rec[colCacheReadTokens] = attr.CacheReadTokens
	rec[colCacheCreation1hTokens] = attr.CacheCreation1hTokens
	rec[colCacheCreation5mTokens] = attr.CacheCreation5mTokens
	// Re-stamp the resolved firm identity: identity is derived, not part of the
	// natural key, so a bucket re-pull after the roster linked the api-key/agent to an
	// Identity attributes the existing spend to it — never blanks a prior attribution.
	if attr.IdentityRef != "" {
		rec[colIdentityRef] = attr.IdentityRef
		rec[colIdentityKind] = attr.IdentityKind
		rec[colIdentitySource] = attr.IdentitySource
	}
}

// updateCostRecord replaces the figures on the linked canonical ledger entry when a
// bucket re-pull changes its value, so the ledger tracks the read-model rather than
// accumulating duplicates. A missing ledger row (e.g. id drift) is tolerated.
func updateCostRecord(ctx context.Context, sc store.Scope, id model.ID, attr attribution) error {
	cr, err := sc.Costs().Get(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	cr.InputTokens = attr.InputTokens
	cr.OutputTokens = attr.OutputTokens
	cr.CostMicroUSD = attr.CostMicroUSD
	// Re-stamp the firm identity if it now resolves (roster linked since first ingest);
	// never blank a prior attribution, mirroring applySampleValues.
	if attr.IdentityRef != "" {
		if cr.Metadata == nil {
			cr.Metadata = map[string]any{}
		}
		cr.Metadata["identity_ref"] = attr.IdentityRef
		cr.Metadata["identity_kind"] = attr.IdentityKind
		cr.Metadata["identity_source"] = attr.IdentitySource
	}
	_, err = sc.Costs().Update(ctx, cr)
	return err
}

// resolveSession resolves the cost's session reference to a core Session (without
// creating one — sessions are owned by the inventory/sessions modules) and, when
// the session names an agent, the agent and its team/project labels. The
// model/provider cost stream carries no session ref, so this attribution is
// usually empty here; it is populated when a connector that attributes cost to a
// session is wired.
func resolveSession(ctx context.Context, sc store.Scope, attr *attribution) error {
	if attr.SessionRef == "" {
		return nil
	}
	s, ok, err := findOne(ctx, sc.Sessions(), eq("external_id", attr.SessionRef))
	if err != nil || !ok {
		return err
	}
	attr.SessionID = s.ID
	if s.AgentID.IsZero() {
		return nil
	}
	a, err := sc.Agents().Get(ctx, s.AgentID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	attr.AgentID = a.ID
	attr.AgentRef = a.ExternalID
	if attr.AgentRef == "" {
		attr.AgentRef = a.Name
	}
	// The agent's CURATED labels outrank the sample's operator-supplied labels
	//: only overwrite the telemetry-seeded Team/Project when the agent
	// actually carries the label, never blank an already-attributed dimension.
	if t := labelString(a.Labels, "team"); t != "" {
		attr.Team = t
	}
	if p := labelString(a.Labels, "project"); p != "" {
		attr.Project = p
	}
	return nil
}

// resolveIdentity resolves the cost's attribution to a FIRM roster Identity (core
// model.Identity, module VI) — the per-identity dollar-budget scoping key.
// Resolution order, first firm hit wins: (1) the agent's IdentityID (the structural
// agent→NHI binding, entities.go:48); (2) the api-key ref, then the actor, matched to
// a roster Identity by ExternalID (the directory/claude-wif roster keys NHIs — svac_/
// apikey_/a SPIFFE id — on the SAME ExternalID the cost stream carries, so the
// convergence is exact). WorkspaceRef is deliberately NOT matched: an Anthropic
// workspace is a roster Collection (group), not an Identity — it stays the separate
// `workspace` budget dimension. It leaves the identity empty (honest, never
// fabricated) when nothing firm resolves, tolerates a missing row, and is READ-ONLY —
// attribution must never mint a roster Identity. Reused by CheckBudget (which carries
// only the AgentRef string), so it resolves the agent itself when AgentID is unset.
func resolveIdentity(ctx context.Context, sc store.Scope, attr *attribution) error {
	agentID := attr.AgentID
	if agentID.IsZero() && attr.AgentRef != "" {
		id, ok, err := resolveAgentID(ctx, sc, attr.AgentRef)
		if err != nil {
			return err
		}
		if ok {
			agentID = id
		}
	}
	if !agentID.IsZero() {
		a, err := sc.Agents().Get(ctx, agentID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if err == nil && !a.IdentityID.IsZero() {
			i, err := sc.Identities().Get(ctx, a.IdentityID)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
			if err == nil {
				stampIdentity(attr, i)
				return nil
			}
		}
	}
	// An api-key / actor ref that IS itself a roster identity (an NHI the directory
	// knows). The api-key ref is preferred over the actor: it is the firmer machine ref.
	for _, ref := range []string{attr.APIKeyRef, attr.Actor} {
		if ref == "" {
			continue
		}
		i, ok, err := findOne(ctx, sc.Identities(), eq("external_id", ref))
		if err != nil {
			return err
		}
		if ok {
			stampIdentity(attr, i)
			return nil
		}
	}
	return nil
}

// stampIdentity records a resolved roster identity on the attribution.
func stampIdentity(attr *attribution, i model.Identity) {
	attr.IdentityRef = i.ExternalID
	attr.IdentityKind = i.Kind
	attr.IdentitySource = i.Provider
}

// writeCostRecord appends the canonical CostRecord ledger entry and returns
// its id (linked from the read-model row so a bucket re-pull updates it in place).
// The attribution dimensions ride in Metadata (the established escape hatch — the
// core ledger schema is not widened per-provider), keeping the audit record complete
// while the queryable copy lives in the read-model. Called only for ESTIMATED samples;
// billed reconciliation data is not written to the canonical ledger.
func writeCostRecord(ctx context.Context, sc store.Scope, attr attribution) (model.ID, error) {
	meta := map[string]any{}
	putMeta := func(k, v string) {
		if v != "" {
			meta[k] = v
		}
	}
	putMeta("team", attr.Team)
	putMeta("project", attr.Project)
	putMeta("provenance", attr.Provenance)
	putMeta("workspace_ref", attr.WorkspaceRef)
	putMeta("api_key_ref", attr.APIKeyRef)
	putMeta("actor", attr.Actor)
	putMeta("service_tier", attr.ServiceTier)
	putMeta("context_window", attr.ContextWindow)
	putMeta("inference_geo", attr.InferenceGeo)
	putMeta("gateway", attr.Gateway)
	putMeta("cost_type", attr.CostType)
	putMeta("identity_ref", attr.IdentityRef)
	putMeta("identity_kind", attr.IdentityKind)
	putMeta("identity_source", attr.IdentitySource)
	putMeta("cost_center_ref", attr.CostCenterRef)
	if attr.CacheReadTokens > 0 {
		meta["cache_read_tokens"] = attr.CacheReadTokens
	}
	if attr.CacheCreation1hTokens > 0 {
		meta["cache_creation_1h_tokens"] = attr.CacheCreation1hTokens
	}
	if attr.CacheCreation5mTokens > 0 {
		meta["cache_creation_5m_tokens"] = attr.CacheCreation5mTokens
	}
	// Operator-supplied labels beyond the promoted team/project dimensions
	// ride the ledger metadata under a label_ prefix, so the audit record keeps the
	// org's own attribution without widening the core schema per label key.
	for _, k := range sortedLabelKeys(attr.Labels) {
		if k == "team" || k == "project" {
			continue // already promoted to first-class dimensions above
		}
		putMeta("label_"+k, attr.Labels[k])
	}
	cr, err := sc.Costs().Create(ctx, model.CostRecord{
		SessionID: attr.SessionID, AgentID: attr.AgentID,
		ModelID: attr.ModelID, ProviderID: attr.ProviderID,
		OccurredAt:   model.NewTimestamp(attr.OccurredAt),
		InputTokens:  attr.InputTokens,
		OutputTokens: attr.OutputTokens,
		CostMicroUSD: attr.CostMicroUSD,
		Currency:     "USD",
		Metadata:     meta,
	})
	return cr.ID, err
}

// provenanceOf normalizes a CostSample provenance to a stored string, defaulting an
// empty/unknown value to "estimated" — the conservative default so a sample is never
// silently treated as billed truth (ARCHITECTURE.md).
func provenanceOf(p sdkmodel.CostProvenance) string {
	if p == sdkmodel.ProvenanceBilled {
		return string(sdkmodel.ProvenanceBilled)
	}
	return string(sdkmodel.ProvenanceEstimated)
}

// gatewayOf normalizes a CostSample gateway to a stored string, defaulting empty to
// "direct" — honoring the contract that "empty is treated as direct by consumers"
// (sdk/model.CostSample.Gateway). Without this, an emitter that leaves Gateway unset
// (e.g. the cooperative OTEL path) would be silently excluded from a gateway=direct
// budget or spend slice.
func gatewayOf(g sdkmodel.Gateway) string {
	if g == "" {
		return string(sdkmodel.GatewayDirect)
	}
	return string(g)
}

// sampleRecord builds the FinOps read-model / dedup row for a sample. key is the
// natural key (the unique dedup column); costRecordID links the canonical ledger
// entry (empty for billed rows, which have no ledger entry).
func sampleRecord(key string, costRecordID model.ID, attr attribution) model.Record {
	return model.Record{
		colSampleKey:             key,
		colCostRecordID:          costRecordID.String(),
		colProviderRef:           attr.ProviderRef,
		colModelRef:              attr.ModelRef,
		colAgentRef:              attr.AgentRef,
		colSessionRef:            attr.SessionRef,
		colTeam:                  attr.Team,
		colProject:               attr.Project,
		colInputTokens:           attr.InputTokens,
		colOutputTokens:          attr.OutputTokens,
		colCostMicroUSD:          attr.CostMicroUSD,
		colOccurredAt:            model.NewTimestamp(attr.OccurredAt).String(),
		colProvenance:            attr.Provenance,
		colWorkspaceRef:          attr.WorkspaceRef,
		colAPIKeyRef:             attr.APIKeyRef,
		colActor:                 attr.Actor,
		colServiceTier:           attr.ServiceTier,
		colContextWindow:         attr.ContextWindow,
		colInferenceGeo:          attr.InferenceGeo,
		colGateway:               attr.Gateway,
		colCostType:              attr.CostType,
		colIdentityRef:           attr.IdentityRef,
		colIdentityKind:          attr.IdentityKind,
		colIdentitySource:        attr.IdentitySource,
		colCacheReadTokens:       attr.CacheReadTokens,
		colCacheCreation1hTokens: attr.CacheCreation1hTokens,
		colCacheCreation5mTokens: attr.CacheCreation5mTokens,
		colCostCenterRef:         attr.CostCenterRef,
	}
}

// naturalKey is the dedup key for a cost sample: provider/model/session, the bucket
// instant, the provenance and EVERY attribution dimension — but NOT the token/cost
// VALUES. Excluding the values is what makes a re-pulled bucket (open/cumulative
// usage, or a re-settled cost_report day) collapse onto the SAME row and upsert its
// value, instead of inserting a second row that double-counts. Two genuinely distinct
// usages still differ in a dimension or the bucket instant (the API aggregates one
// row per dimension-combination per bucket); per-request cooperative events differ in
// their nanosecond instant. Billed vs estimated differ in provenance, so they never
// collapse into one another.
func naturalKey(attr attribution) string {
	parts := []string{
		attr.ProviderRef, attr.ModelRef, attr.SessionRef,
		attr.OccurredAt.UTC().Format(time.RFC3339Nano),
		attr.Provenance, attr.WorkspaceRef, attr.APIKeyRef, attr.Actor, attr.ServiceTier,
		attr.ContextWindow, attr.InferenceGeo, attr.Gateway, attr.CostType,
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])
}

// sortedLabelKeys returns a label map's keys in stable order (deterministic
// ledger metadata).
func sortedLabelKeys(labels map[string]string) []string {
	if len(labels) == 0 {
		return nil
	}
	out := make([]string, 0, len(labels))
	for k := range labels {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// labelString reads a string label value, tolerating a non-string value.
func labelString(labels map[string]any, key string) string {
	if labels == nil {
		return ""
	}
	if s, ok := labels[key].(string); ok {
		return s
	}
	return ""
}

// findOne returns the first entity matching the AND of filters, or ok=false.
func findOne[T any](ctx context.Context, repo store.ReadRepository[T], filters ...model.Filter) (T, bool, error) {
	var zero T
	list, _, err := repo.List(ctx, model.Query{Filters: filters, Limit: 1})
	if err != nil {
		return zero, false, err
	}
	if len(list) == 0 {
		return zero, false, nil
	}
	return list[0], true, nil
}

// foProvider find-or-creates a provider by its natural reference (name==ref).
func foProvider(ctx context.Context, sc store.Scope, ref string) (model.ID, error) {
	if p, ok, err := findOne(ctx, sc.Providers(), eq("name", ref)); err != nil {
		return "", err
	} else if ok {
		return p.ID, nil
	}
	p, err := sc.Providers().Create(ctx, model.Provider{Name: ref, Kind: ref, Status: model.StatusActive})
	return p.ID, err
}

// foModel find-or-creates a model by (name, provider). Pricing/enrichment is
// module X's job; FinOps only needs the id for ledger attribution.
func foModel(ctx context.Context, sc store.Scope, name string, providerID model.ID) (model.ID, error) {
	filters := []model.Filter{eq("name", name)}
	if !providerID.IsZero() {
		filters = append(filters, eq("provider_id", providerID.String()))
	}
	if mm, ok, err := findOne(ctx, sc.Models(), filters...); err != nil {
		return "", err
	} else if ok {
		return mm.ID, nil
	}
	mm, err := sc.Models().Create(ctx, model.Model{Name: name, ProviderID: providerID, Status: model.StatusActive})
	return mm.ID, err
}
