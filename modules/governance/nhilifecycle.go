// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// nhilifecycle.go — Subsystem F: governed NHI lifecycle on top of the
// read-only roster. It turns discovery into management: orchestrated rotation of
// keys/secrets (via the identitysource.LifecycleActuator connectors, gated by the
// HITL + dual-control floor), expiry/staleness enforcement (alert→block
// with the staged policy), governed offboarding (cascade + audited
// soft-delete recovery window + irreversible finalize behind a CRITICAL,
// no-break-glass approval), Entra-shaped owner/sponsor + orphan detection, and a
// posture surface. It closes SEC-G2.
//
// Design invariants:
//   - Convergence: a lifecycle row is keyed on identity_ref == the roster
//     external_id, so it tracks the SAME NHI the roster reconciles and the
//     access-map bridge attributes — never a parallel identity model.
//   - Read-first (docs/SECURITY-HARDENING.md): the module itself never holds a write credential.
//     Actuation is delegated to an opt-in LifecycleActuator the composition root
//     wires per (tenant, source); where a provider exposes no rotation/offboarding
//     API the orchestrator degrades HONESTLY (a coverage finding + a clear error),
//     never a fabricated actuation.
//   - Governed + audited: every actuation passes the LifecycleGate (the bridge
// engine, which floors CRITICAL actions at two distinct humans) and is
//     recorded in the append-only event trail + the tenant audit chain.
//   - Minimal data (docs/SECURITY-HARDENING.md): no row, finding, event or audit Meta carries a
//     secret. A rotation's minted credential is returned ONCE to the caller and
//     persisted nowhere.

// The OWASP/CSA criticality vocabulary an NHI credential carries. It reuses the
// ActionRiskTier string values deliberately (one tier vocabulary, not a third
// taxonomy) but names the CREDENTIAL's blast radius, distinct from an ACTION's
// risk: a CRITICAL credential blocks immediately on staleness (no grace window).
const (
	defaultNHICriticality = RiskTierHigh
)

// Governed NHI lifecycle action strings. They feed the risk-tier classifier
// (risktier.go): nhi.rotate and nhi.offboard.finalize are in the default CRITICAL
// set (two-person floor); nhi.offboard (the reversible soft-delete) defaults HIGH.
const (
	actionNHIRotate        = "nhi.rotate"
	actionNHIOffboard      = "nhi.offboard"
	actionNHIOffboardFinal = "nhi.offboard.finalize"
	nhiSubjectKind         = "nhi"
)

// Agent identity kind. A lifecycle row with kind=agent is subject to
// deny-closed sponsor-mandatory rules both at registration (the joiner) and in
// the sweep (the orphan enforcer).
const (
	NHIKindAgent = "agent"
)

// Staleness / enforcement / offboarding state vocabularies (materialized on the
// lifecycle row by the sweep and the actuation handlers).
const (
	staleOK      = "ok"
	staleStale   = "stale"
	staleUnknown = "unknown" // rotated_at not known — honest coverage gap, never a silent "fresh"

	enforceMonitor = "monitor"
	enforceAlert   = "alert"
	enforceBlocked = "blocked"

	offboardNone  = "none"
	offboardSoft  = "soft_deleted"
	offboardFinal = "finalized"
)

// LifecycleGate authorizes a governed NHI lifecycle actuation through the HITL
// bridge. It mirrors the module-owned ApprovalGate seam deploy/security use, so the
// AGPL composition root supplies the adapter over the engine (which owns the
// dual-control floor) and the module never imports the bridge. Deny-closed: the
// zero decision denies, and the default denyLifecycleGate denies everything until
// the bridge is wired.
type LifecycleGate interface {
	// Authorize opens (or idempotently reuses) a governed approval bound to the exact
	// plan hash and reports its effective status. AllowBreakGlass selects whether the
	// Emergency path may satisfy it: true for rotation (an emergency key rotation
	// is legitimate), false for an irreversible finalize (no emergency justifies
	// skipping the second human — the erase-gate precedent).
	Authorize(ctx context.Context, tenant model.TenantID, req LifecycleGateRequest) (LifecycleGateDecision, error)
}

// LifecycleGateRequest is one governed actuation to authorize.
type LifecycleGateRequest struct {
	Action          string
	SubjectKind     string
	SubjectRef      string
	PlanHash        string
	Reason          string
	RequestedBy     string
	AllowBreakGlass bool
}

// LifecycleGateDecision is the gate's effective verdict. Status uses the exported
// GateStatus* vocabulary so the composition-root adapter and the module agree.
type LifecycleGateDecision struct {
	Status      string
	ApprovalRef string
	PlanHash    string
}

// The gate status vocabulary (the bridge's neutral states, exported so the
// adapter maps onto them).
const (
	GateStatusApproved   = "approved"
	GateStatusBreakGlass = "break_glass"
	GateStatusPending    = "pending"
	GateStatusRejected   = "rejected"
	GateStatusExpired    = "expired"
	GateStatusNoGate     = "no_gate"
)

// Allowed reports whether the decision authorizes the actuation. Break-glass
// counts as allowed (the engine recorded the emergency use when it granted).
func (d LifecycleGateDecision) Allowed() bool {
	return d.Status == GateStatusApproved || d.Status == GateStatusBreakGlass
}

// denyLifecycleGate is the deny-closed default: with no bridge wired, no NHI
// actuation can proceed (a no_gate decision), so an unconfigured deployment is
// safe rather than silently ungoverned.
type denyLifecycleGate struct{}

func (denyLifecycleGate) Authorize(_ context.Context, _ model.TenantID, req LifecycleGateRequest) (LifecycleGateDecision, error) {
	return LifecycleGateDecision{Status: GateStatusNoGate, PlanHash: req.PlanHash}, nil
}

// WithLifecycleGate wires the governed HITL gate the composition root builds over
// the approval bridge. nil leaves the deny-closed default.
func WithLifecycleGate(g LifecycleGate) Option {
	return func(m *Module) {
		if g != nil {
			m.lifecycleGate = g
		}
	}
}

// UseLifecycleGate is the additive post-construction injection of the governed HITL
// gate (parallel to UseRosterProviders): the bridge is built downstream of the
// module in the composition root, so the gate is wired here rather than at New. nil
// leaves the deny-closed default. Safe to call before Start.
func (m *Module) UseLifecycleGate(g LifecycleGate) {
	if g == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lifecycleGate = g
}

// LifecycleActuatorBinding pairs a write-capable LifecycleActuator (an opt-in
// connector side-type) with the source it actuates and the tenant it serves. The
// composition root builds these from the deployment's secrets/identity connectors
// and hands them via UseLifecycleActuators — the same additive-injection pattern
// as UseRosterProviders. The module never builds a connector or holds a credential.
type LifecycleActuatorBinding struct {
	// Source is the identitysource SourceKind string the actuator handles; it is
	// matched against a lifecycle row's source (== the roster identity Provider).
	Source string
	// TenantRef is the business tenant this actuator serves.
	TenantRef string
	// Actuator is the write-capable lifecycle arm.
	Actuator identitysource.LifecycleActuator
}

// UseLifecycleActuators wires the per-(tenant,source) lifecycle actuators. Safe to
// call before Start; tests pass fakes here.
func (m *Module) UseLifecycleActuators(bindings []LifecycleActuatorBinding) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.actuators = map[model.TenantID]map[string]identitysource.LifecycleActuator{}
	for _, b := range bindings {
		tid, ok := tenantOf(b.TenantRef)
		if !ok || b.Actuator == nil || strings.TrimSpace(b.Source) == "" {
			continue
		}
		if m.actuators[tid] == nil {
			m.actuators[tid] = map[string]identitysource.LifecycleActuator{}
		}
		m.actuators[tid][b.Source] = b.Actuator
	}
}

// actuatorFor returns the wired actuator for (tenant, source), or nil for honest
// degrade (no rotation/offboarding capability for that provider).
func (m *Module) actuatorFor(tenant model.TenantID, source string) identitysource.LifecycleActuator {
	m.mu.Lock()
	defer m.mu.Unlock()
	if bySource := m.actuators[tenant]; bySource != nil {
		return bySource[source]
	}
	return nil
}

// gate returns the wired LifecycleGate or the deny-closed default.
func (m *Module) gate() LifecycleGate {
	m.mu.Lock()
	g := m.lifecycleGate
	m.mu.Unlock()
	if g != nil {
		return g
	}
	return denyLifecycleGate{}
}

// defaultMaxAge is the rotation window per criticality when a row sets none. The
// values reflect the CSA/Five-Eyes posture (critical NHI rotated frequently,
// short-lived); an operator overrides per row via the policy endpoint.
func defaultMaxAge(crit ActionRiskTier) time.Duration {
	switch crit {
	case RiskTierCritical:
		return 30 * 24 * time.Hour
	case RiskTierHigh:
		return 90 * 24 * time.Hour
	case RiskTierMedium:
		return 180 * 24 * time.Hour
	default:
		return 365 * 24 * time.Hour
	}
}

// blockGraceWindow is the alert→block escalation window for a NON-critical stale
// credential ("escalado automático a bloqueo a los 30 días"). A
// CRITICAL credential skips this grace and blocks immediately on staleness.
const blockGraceWindow = 30 * 24 * time.Hour

// nhiDTO is the lifecycle view of one NHI. It never carries a secret.
type nhiDTO struct {
	IdentityRef    string `json:"identity_ref"`
	Source         string `json:"source,omitempty"`
	Kind           string `json:"kind,omitempty"` // agent | "" (legacy)
	Criticality    string `json:"criticality"`
	OwnerRef       string `json:"owner_ref,omitempty"`
	SponsorRef     string `json:"sponsor_ref,omitempty"`
	RotatedAt      string `json:"rotated_at,omitempty"`
	MaxAgeSeconds  int64  `json:"max_age_seconds,omitempty"`
	RotationTarget string `json:"rotation_target,omitempty"`
	Staleness      string `json:"staleness_status"`
	Enforcement    string `json:"enforcement"`
	EnforceReason  string `json:"enforcement_reason,omitempty"`
	Orphaned       bool   `json:"orphaned"`
	RegistryOrphan bool   `json:"registry_orphaned,omitempty"` // the registry's own assertion
	OffboardState  string `json:"offboard_state"`
	RecoveryUntil  string `json:"recovery_until,omitempty"`
}

func toNHIDTO(rec model.Record) nhiDTO {
	d := nhiDTO{
		IdentityRef:    rec.String(colNHIIdentityRef),
		Source:         rec.String(colNHISource),
		Kind:           rec.String(colNHIKind),
		Criticality:    rec.String(colNHICriticality),
		OwnerRef:       rec.String(colNHIOwnerRef),
		SponsorRef:     rec.String(colNHISponsorRef),
		RotatedAt:      rec.String(colNHIRotatedAt),
		MaxAgeSeconds:  rec.Int(colNHIMaxAgeSec),
		RotationTarget: rec.String(colNHITargetRef),
		Staleness:      rec.String(colNHIStaleStatus),
		Enforcement:    rec.String(colNHIEnforce),
		EnforceReason:  rec.String(colNHIEnforceWhy),
		Orphaned:       rec.Bool(colNHIOrphaned),
		RegistryOrphan: rec.Bool(colNHIRegistryOrphan),
		OffboardState:  rec.String(colNHIOffboard),
		RecoveryUntil:  rec.String(colNHIRecoverUntil),
	}
	if d.Criticality == "" {
		d.Criticality = string(defaultNHICriticality)
	}
	if d.Staleness == "" {
		d.Staleness = staleUnknown
	}
	if d.Enforcement == "" {
		d.Enforcement = enforceMonitor
	}
	if d.OffboardState == "" {
		d.OffboardState = offboardNone
	}
	return d
}

// newLifecycleRecord seeds a lifecycle row's defaults for a freshly-registered NHI.
// kind optionally sets the colNHIKind field; pass "" to leave it unset
// (legacy behavior — no agent constraints applied).
func newLifecycleRecord(identityRef, source, kind string) model.Record {
	rec := model.Record{
		colNHIIdentityRef: identityRef,
		colNHISource:      source,
		colNHICriticality: string(defaultNHICriticality),
		colNHIMaxAgeSec:   int64(0),
		colNHIStaleStatus: staleUnknown,
		colNHIEnforce:     enforceMonitor,
		colNHIOrphaned:    false,
		colNHIOffboard:    offboardNone,
	}
	if kind != "" {
		rec[colNHIKind] = kind
	}
	return rec
}

// foLifecycle find-or-creates the lifecycle row for an NHI identity_ref and returns
// it. On create it seeds the source from the roster identity (if resolvable) so the
// actuator lookup has a provider. It converges on identity_ref alone (single-writer
// discipline, like the roster).
func foLifecycle(ctx context.Context, sc store.Scope, identityRef string) (store.GenericRepo, model.Record, error) {
	repo, err := sc.Ext(nhiLifecycleKind)
	if err != nil {
		return nil, nil, err
	}
	cur, found, err := findOne(ctx, repo, eq(colNHIIdentityRef, identityRef))
	if err != nil {
		return nil, nil, err
	}
	if found {
		return repo, cur, nil
	}
	source := ""
	if id, ok, e := identityByExternalID(ctx, sc, identityRef); e == nil && ok {
		source = id.Provider
	}
	created, err := repo.Create(ctx, newLifecycleRecord(identityRef, source, ""))
	if err != nil {
		if isConflict(err) { // a concurrent creator won; re-read
			cur, found, err = findOne(ctx, repo, eq(colNHIIdentityRef, identityRef))
			if err != nil || !found {
				return nil, nil, err
			}
			return repo, cur, nil
		}
		return nil, nil, err
	}
	return repo, created, nil
}

// recordLifecycleEvent appends an immutable lifecycle event (the append-only trail).
// detail is bounded and must never carry a secret. actor/actorUser identify who
// caused the transition (a route principal, or the sweep system actor).
func (m *Module) recordLifecycleEvent(ctx context.Context, sc store.Scope, identityRef, evt, actor, actorUser, detail string) error {
	repo, err := sc.Ext(nhiEventKind)
	if err != nil {
		return err
	}
	if len(detail) > maxMatchLen*4 {
		detail = detail[:maxMatchLen*4]
	}
	_, err = repo.Create(ctx, model.Record{
		colNHIEvtIdentity: identityRef,
		colNHIEvtKind:     evt,
		colNHIEvtActor:    actor,
		colNHIEvtUser:     actorUser,
		colNHIEvtDetail:   detail,
		colNHIEvtAt:       m.clock.Now().String(),
	})
	return err
}

// planHashFor binds a governed actuation to its exact parameters (anti-TOCTOU): a
// later actuation under the same approval must present the same identity, op and
// target, or the gate's plan binding rejects it.
func planHashFor(identityRef, op, target string) string {
	sum := sha256.Sum256([]byte(identityRef + "\x1f" + op + "\x1f" + target))
	return hex.EncodeToString(sum[:])
}

// nhiResultDTO is the response to a governed actuation: the gate status (so the
// operator knows whether to await approval) plus, on a completed rotation, the
// minted credential ONCE.
type nhiResultDTO struct {
	Status      string `json:"status"` // approved|pending|rejected|expired|no_gate|break_glass|done
	ApprovalRef string `json:"approval_ref,omitempty"`
	Detail      string `json:"detail,omitempty"`
	// NewSecret is the freshly-minted credential, returned EXACTLY ONCE and stored
	// nowhere (the WIF MintedToken rule). Empty unless a rotation completed.
	NewSecret        string `json:"new_secret,omitempty"`
	NewCredentialRef string `json:"new_credential_ref,omitempty"`
}
