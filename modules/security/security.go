// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

import (
	"context"
	"crypto/ed25519"
	"log/slog"
	"sync"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
)

// Name is the module's globally unique identifier (the runtime registry key).
const Name = "olivares.security"

// Namespace is the module's store and API namespace: its entities are
// "security.<entity>" and its routes mount under /v1/m/security/.
const Namespace = "security"

// The module's permissions, granted to the built-in roles by verb tier (viewer→
// read, editor→write, admin/owner→admin). Reading findings/anomalies/cases and the
// integrity report are read-tier (a viewer sees the security posture); running a
// guardrail inspection and opening/triaging cases are write-tier; changing the
// inline-ENFORCEMENT posture (the only thing that can touch production) is
// admin-tier and additionally governed by where wired (docs/SECURITY-HARDENING.md).
const (
	permFindingRead auth.Permission = "security:finding:read"
	// permObservedRead gates receiving guardrail.observed events over the bus. No
	// route uses it: the eventing module's per-event RBAC filter is its enforcement point.
	// It is declared so a role can be granted — or denied — it. See Permissions().
	permObservedRead     auth.Permission = "security:observed:read"
	permFindingWrite     auth.Permission = "security:finding:write"
	permGuardrailWrite   auth.Permission = "security:guardrail:write"
	permAnomalyRead      auth.Permission = "security:anomaly:read"
	permCaseRead         auth.Permission = "security:case:read"
	permCaseWrite        auth.Permission = "security:case:write"
	permIntegrityRead    auth.Permission = "security:integrity:read"
	permEnforcementAdmin auth.Permission = "security:enforcement:admin"
)

// Option configures a Module at construction.
type Option func(*Module)

// WithClock overrides the module clock (tests inject a deterministic clock).
func WithClock(c model.Clock) Option { return func(m *Module) { m.clock = c } }

// WithEngineVersion stamps exports with the running binary version. The module
// cannot discover package-main ldflags metadata through the SDK Host seam.
func WithEngineVersion(version string) Option {
	return func(m *Module) {
		if version != "" {
			m.engineVersion = version
		}
	}
}

// WithClassifier wires an OPTIONAL external guardrail classifier (a guardrail-LLM
// or a hosted model-backed detector) behind the deterministic in-process detectors.
// Without it the module runs only its own explainable, reproducible rules — the
// right default for findings that must hold up in forensics/compliance (docs/SECURITY-HARDENING.md).
func WithClassifier(c Classifier) Option { return func(m *Module) { m.classifier = c } }

// WithApprovalGate wires the HITL governance seam that gates ENABLING inline
// enforcement. Without it, enabling enforcement is permitted (an authenticated
// admin action) but recorded as UNGOVERNED, and Start warns once — the safe
// default is still detective (enforcement off).
func WithApprovalGate(g ApprovalGate) Option { return func(m *Module) { m.gate = g } }

// WithCheckpointKey wires the Ed25519 public key the forensic verifier uses to
// check the ledger's signed checkpoints (audit.VerifyCheckpoints). Without it the
// chain is still verified for internal consistency (AuditLog.Verify), but the
// signed-checkpoint attestation is reported as unavailable rather than faked.
func WithCheckpointKey(pub ed25519.PublicKey) Option {
	return func(m *Module) { m.checkpointKey = pub }
}

// WithCheckpointVerifierSource wires a LAZY multi-candidate checkpoint verifier
// (audit.Signer.CheckpointVerifier fits directly). It is REQUIRED when the
// ledger runs an off-box KMS/HSM checkpoint signer (HYOK): the on-box
// Ed25519 key of WithCheckpointKey cannot satisfy off-box-signed checkpoints,
// so without this every integrity endpoint would report checkpoint-sig-invalid
// for a perfectly healthy custody posture. Lazy because building it may fetch
// the off-box public key over the network: the module single-flights the source
// and caches the verifier after the first success; while the source is failing
// (e.g. a KMS outage) the checkpoint attestation is reported as UNAVAILABLE —
// deliberately NOT falling back to the on-box key, which would misreport the
// healthy off-box chain as checkpoint-sig-invalid. Never faked, never a 500.
func WithCheckpointVerifierSource(src func(context.Context) (*audit.CheckpointVerifier, error)) Option {
	return func(m *Module) { m.cpVerifierSource = src }
}

// WithDetector appends an extra guardrail detector to the default chain (a
// composition-root or test extension point; the engine never needs it).
func WithDetector(d Detector) Option { return func(m *Module) { m.detectors = append(m.detectors, d) } }

// Module is module IX — security, guardrails & forensics (see doc.go for the
// bounded context and the read-first / minimal-data / integrity / anti-evasion
// RED LINE). It is event-driven for anomaly detection (it subscribes to the estate
// stream) and request-driven for guardrails, cases and integrity verification.
type Module struct {
	log           *slog.Logger
	data          api.ModuleData
	host          sdk.Host
	clock         model.Clock
	engineVersion string
	detectors     []Detector
	classifier    Classifier
	gate          ApprovalGate
	checkpointKey ed25519.PublicKey
	// cpVerifierSource lazily yields the multi-candidate checkpoint verifier
	// (on-box + off-box keys); nil falls back to checkpointKey alone.
	cpVerifierSource func(context.Context) (*audit.CheckpointVerifier, error)

	mu     sync.Mutex
	cancel func() // bus unsubscribe

	// cpMu single-flights cpVerifierSource and guards cpVerifier (the cache of
	// the first SUCCESSFUL build). A DEDICATED mutex, not mu: the source may
	// fetch the off-box public key over HTTP, and a hung KMS must never block
	// the module lifecycle (Stop) behind a request-path lock.
	cpMu       sync.Mutex
	cpVerifier *audit.CheckpointVerifier
}

// Compile-time proof the module satisfies the SDK lifecycle, the API route/
// permission seam and the data-consumer seam. RegisterSchema (the engine-side
// SchemaProvider seam) is structural and verified by the runtime at boot/test.
var (
	_ sdk.Module       = (*Module)(nil)
	_ api.Module       = (*Module)(nil)
	_ api.DataConsumer = (*Module)(nil)
)

// New returns a security module with safe defaults: the full chain of
// deterministic in-process guardrail detectors, no external classifier, an
// ungoverned-but-audited approval gate, and no checkpoint key. The composition
// root wires the gate and the audit checkpoint key via options.
func New(opts ...Option) *Module {
	m := &Module{
		clock:         model.SystemClock{},
		engineVersion: "dev",
		detectors: []Detector{
			newPIIDetector(),
			newInjectionDetector(),
			newUntrustedContentDetector(),
			newJailbreakDetector(),
			newContentDetector(),
			newOutputValidator(),
			newOWASPAgenticDetector(),
		},
		gate: ungovernedGate{},
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

// Descriptor returns the module's self-description.
func (m *Module) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeModule,
		Title:       "Security, guardrails & forensics",
		Description: "Detective-by-default guardrails (PII, prompt-injection, jailbreak, content, output-validation, OWASP Agentic Top 10), threat/anomaly detection over the estate (incl. the anti-evasion mark and drift), and incident-response forensics: reconstructible, hash-chain-verified timelines from the tamper-evident ledger, exportable to WORM/SIEM. Inline enforcement is opt-in and governed.",
	}
}

// UseData receives the least-privilege, tenant-parameterized data handle from the
// engine boot (the api.DataConsumer seam), before Start.
func (m *Module) UseData(d api.ModuleData) { m.data = d }

// SetLogger attaches a logger (optional; Init also sets one from the host).
func (m *Module) SetLogger(l *slog.Logger) { m.log = l }

// Init wires the module to the bus and keeps the host for publishing findings. It
// subscribes to two estate streams: the FINDING stream — the anti_evasion mark
// it correlates and other modules' high-severity findings it persists into the
// security view — and the OBSERVED-TEXT stream (TypeGuardrailObserved), the
// hook that routes redacted observed agent text through the guardrail detector
// chain so findings emit automatically without POST /guardrails/inspect. The
// estate's edge/drift ground-truth is read on demand from the persisted access
// graph, not re-ingested per edge. It must not block.
func (m *Module) Init(_ context.Context, host sdk.Host) error {
	m.log = host.Logger()
	m.host = host
	cancel, err := host.Subscribe([]event.Type{event.TypeFindingReported, event.TypeGuardrailObserved}, m.onBusEvent)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.cancel = cancel
	m.mu.Unlock()
	return nil
}

// Start has no background work (the module is event- and request-driven; it cannot
// enumerate tenants, so anomaly correlation is per-event and forensics is a
// tenant-scoped endpoint). It warns once per seam that is un-wired or degraded so a
// silently weaker security plane is VISIBLE rather than a surprise (docs/SECURITY-HARDENING.md).
func (m *Module) Start(context.Context) error {
	if m.log == nil {
		return nil
	}
	if m.data == nil {
		m.log.Warn("security: started without a data handle; findings, cases and anomalies will not persist")
	}
	if _, ok := m.gate.(ungovernedGate); ok {
		m.log.Warn("security: no HITL approval gate wired; enabling inline enforcement will be permitted but UNGOVERNED (still detective/off by default)")
	}
	if len(m.checkpointKey) == 0 {
		m.log.Warn("security: no audit checkpoint public key wired; forensic timelines verify chain consistency but NOT the signed checkpoints (integrity.checkpoints = unavailable)")
	}
	return nil
}

// Stop unsubscribes from the bus. It is idempotent.
func (m *Module) Stop(context.Context) error {
	m.mu.Lock()
	cancel := m.cancel
	m.cancel = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

// APINamespace returns the module's namespace; it roots routes at /v1/m/security/.
func (m *Module) APINamespace() string { return Namespace }

// Permissions declares the permissions the module's routes require so the built-in
// roles grant them by verb tier.
func (m *Module) Permissions() []auth.Permission {
	return []auth.Permission{
		permFindingRead, permFindingWrite, permGuardrailWrite, permAnomalyRead,
		permCaseRead, permCaseWrite, permIntegrityRead, permEnforcementAdmin,
		// "security:observed:read" gates DELIVERY of guardrail.observed events
		// (modules/eventing/catalog.go) — the redacted observed-agent-text stream, the most
		// content-like fact on the bus, which is why core/auth privilegedReadPerms keeps it
		// at editor and above. It is enforced by the eventing per-event RBAC filter, not by
		// a route here, and until NO module declared it: it therefore never reached the
		// scope-grantable catalog, and an operator could not author a role that confers or
		// withholds it. It belongs to THIS module because this module owns the "security"
		// namespace — eventing declaring it would let one module widen another's.
		permObservedRead,
	}
}

// APIRoutes mounts the module's routes. The engine wraps each with authentication,
// tenant resolution and the declared permission check before the handler runs, and
// pins the data handle to the resolved tenant. Privileged reads (timeline, export,
// integrity, anomalies) additionally self-audit (docs/SECURITY-HARDENING.md).
func (m *Module) APIRoutes(reg api.RouteRegistrar) {
	// Findings (the guardrail/anomaly/forensic output) — read + triage.
	reg.Handle("GET", "/findings", permFindingRead, m.handleListFindings)
	reg.Handle("GET", "/findings/export", permFindingRead, m.handleExportFindings)
	reg.Handle("GET", "/findings/{id}", permFindingRead, m.handleGetFinding)
	reg.Handle("PATCH", "/findings/{id}", permFindingWrite, m.handleTriageFinding)

	// Safety posture — the read-first per-provider AI-safety view aggregating
	// the OpenAI Moderation / AWS Bedrock Guardrails / Azure RAI posture findings.
	reg.Handle("GET", "/safety-posture", permFindingRead, m.handleSafetyPosture)

	// Guardrails — the inspection service. Producing a finding is a write; the
	// verdict is detective (allow|flag) unless inline enforcement is enabled+governed.
	reg.Handle("POST", "/guardrails/inspect", permGuardrailWrite, m.handleInspect)

	// Inline-enforcement posture (the only thing that can touch production) —
	// read the policy, set it (admin-tier + governed by where wired).
	reg.Handle("GET", "/enforcement", permFindingRead, m.handleGetEnforcement)
	reg.Handle("PUT", "/enforcement", permEnforcementAdmin, m.handleSetEnforcement)

	// Threat/anomaly detection over the estate (drift + correlated findings).
	reg.Handle("GET", "/anomalies", permAnomalyRead, m.handleAnomalies)

	// Incident response / forensics: cases + the reconstructible, verified timeline.
	reg.Handle("GET", "/cases", permCaseRead, m.handleListCases)
	reg.Handle("POST", "/cases", permCaseWrite, m.handleCreateCase)
	reg.Handle("GET", "/cases/{id}", permCaseRead, m.handleGetCase)
	reg.Handle("PATCH", "/cases/{id}", permCaseWrite, m.handleUpdateCase)
	reg.Handle("POST", "/cases/{id}/links", permCaseWrite, m.handleLinkCase)
	reg.Handle("GET", "/cases/{id}/links", permCaseRead, m.handleListCaseLinks)
	reg.Handle("GET", "/cases/{id}/timeline", permCaseRead, m.handleTimeline)
	reg.Handle("GET", "/cases/{id}/export", permCaseRead, m.handleExportCase)

	// Ledger integrity verification (chain + signed checkpoints) — the forensic
	// guarantee that the evidence is unaltered (docs/SECURITY-HARDENING.md, §5).
	reg.Handle("GET", "/integrity/verify", permIntegrityRead, m.handleVerifyIntegrity)
}

// debugf / errorf log if a logger is set. errorf surfaces a lost best-effort
// secondary write (a finding, an audit) rather than swallowing it (docs/SECURITY-HARDENING.md).
func (m *Module) debugf(msg string, args ...any) {
	if m.log != nil {
		m.log.Debug(msg, args...)
	}
}
