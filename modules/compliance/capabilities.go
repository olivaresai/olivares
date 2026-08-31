// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"context"
	"errors"
	"fmt"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	accessmap "github.com/olivaresai/olivares/modules/access-map"
)

// This file holds the FIXED capability vocabulary and the PROBE engine that evaluates
// each capability against live tenant evidence. The honesty line (docs/SECURITY-HARDENING.md) lives
// here: an OPERATIONAL capability is present ONLY when real tenant rows exist; an
// ARCHITECTURAL capability is present by construction with a doc citation (clearly
// labeled as design evidence, not telemetry); an opt-in guarantee defaults to absent.

// capabilityCatalog is the fixed, ordered capability vocabulary. A control maps to a
// subset (frameworks.go). The catalog itself is the contract for what "evidence"
// means — nothing outside it can back a control.
var capabilityCatalog = []Capability{
	// --- OPERATIONAL: present ONLY when real tenant data exists -------------------
	{Key: "audit_trail", Class: ClassOperational, Name: "Audit trail", Desc: "An append-only audit ledger records events for the tenant."},
	{Key: "audit_integrity", Class: ClassOperational, Name: "Audit integrity", Desc: "The audit hash-chain verifies (tamper-evident, checked live)."},
	{Key: "access_observability", Class: ClassOperational, Name: "Access observability", Desc: "Agent→resource access (R/RW) is recorded as edges (module III)."},
	{Key: "least_privilege_drift", Class: ClassOperational, Name: "Least-privilege drift", Desc: "Permitted-vs-observed access drift is computable (module III)."},
	{Key: "identity_governance", Class: ClassOperational, Name: "Identity governance", Desc: "Non-human identities and policies are governed (module VI)."},
	{Key: "threat_detection", Class: ClassOperational, Name: "Threat detection", Desc: "Security guardrail/anomaly findings are produced (module IX)."},
	{Key: "adversarial_testing", Class: ClassOperational, Name: "Adversarial testing", Desc: "Red-team robustness findings exist (module XVIII)."},
	{Key: "quality_evaluation", Class: ClassOperational, Name: "Quality evaluation", Desc: "Agent eval results exist (module XII)."},
	{Key: "change_management", Class: ClassOperational, Name: "Change management", Desc: "Governed deployments / change records exist (module VII)."},
	{Key: "data_lineage", Class: ClassOperational, Name: "Data lineage", Desc: "Data-lineage records prove client data flow in/out of the perimeter (module VIII)."},
	{Key: "risk_classification", Class: ClassOperational, Name: "Risk classification", Desc: "Agents carry EU-AI-Act/NIST risk classifications (this module)."},
	{Key: "data_residency", Class: ClassOperational, Name: "Data residency", Desc: "A residency attestation exists with no observed perimeter-egress violation."},
	{Key: "encryption_at_rest", Class: ClassOperational, Name: "Encryption at rest", Desc: "At-rest encryption is attested ON for the tenant (opt-in; default OFF → a gap)."},
	{Key: "resource_accounting", Class: ClassOperational, Name: "Resource accounting", Desc: "FinOps records token/compute/cost per inference (finops.cost_sample). Evidences accounting of computational resources (EU AI Act Annex IV(2)(c)), NOT dataset quality or model performance."},
	{Key: "external_activity", Class: ClassOperational, Name: "External activity evidence", Desc: "Anthropic Compliance API Activity Feed records (actor/type/timestamps) appended to the tamper-evident ledger as audit/eDiscovery evidence (connector: claude-compliance, CLA-06)."},
	{Key: "supplier_gpai_posture", Class: ClassOperational, Name: "Supplier GPAI compliance posture", Desc: "Brokered model providers have an operator-VERIFIED EU AI Act GPAI compliance posture on record (technical documentation / training-data summary / copyright policy / downstream info / Code of Practice signatory). A self-reported claim that has not been operator-verified is NOT evidence (FIN-13, models.gpai_posture)."},
	{Key: "signed_model_admission", Class: ClassOperational, Name: "Signed-model admission", Desc: "Self-hosted / third-party model artifacts carry an operator-VERIFIED signature/provenance verdict (OpenSSF Model Signing v1.0 / Sigstore) before admission, deny-closed per policy. A recorded-but-unverified admission is a claim, NOT evidence. Covers self-hosted/third-party artifacts, not closed-weight brokered models like Claude (no weights to sign) (G15, models.model_admission)."},
	{Key: "model_aibom", Class: ClassOperational, Name: "Model AIBOM (lineage)", Desc: "A CycloneDX AIBOM (machine-learning-model + dataset components, modelCard) is SEALED and ledger-anchored for admitted models, recording model/dataset lineage, provenance and the admission verdict. Evidences lineage/provenance, NOT dataset statistical quality/representativeness or model performance (G15, models.aibom)."},
	{Key: "pii_discovery", Class: ClassOperational, Name: "PII discovery", Desc: "Deterministic sensitivity/PII discovery scans run over governed knowledge bases and registered document sources, labeling content with explainable classes (named rule + occurrence count — never a matched value) and appending knowledge.pii_scan evidence rows (module VIII)."},
	{Key: "dlp_enforcement", Class: ClassOperational, Name: "DLP enforcement", Desc: "A deny-closed DLP egress gate keyed on sensitivity classes withholds labeled/unscanned chunks from retrieval and refuses embed-egress ingest, with append-only knowledge.dlp_event enforcement evidence. Present only when a DLP policy exists AND discovery scans have run — a policy over unscanned content is a recorded claim, not enforceable evidence (knowledge.dlp_rule)."},
	{Key: "session_recording", Class: ClassOperational, Name: "Privileged session recording", Desc: "Privileged sessions (break-glass, policy authoring, access-graph viewing) are recorded as ledger-anchored, redacted, reconstructable frames (recording.session). Evidences that a session-level record exists for forensic replay/defense — NOT a guarantee that every privileged surface was recorded."},
	{Key: "rtbf_erasure", Class: ClassOperational, Name: "Right-to-erasure fulfillment", Desc: "Data-subject erasure requests run a governed RTBF workflow (hold-gate, CRITICAL dual-control, crypto-shredding) over the control plane's own stores and seal an append-only, ledger-anchored erasure receipt with a post-erasure chain verification and a documented erasure↔retention reconciliation (compliance.erasure_receipt). Present only when a real erasure was fulfilled — a workflow that has never run is a claim, not evidence."},

	// --- ARCHITECTURAL: a platform-design guarantee, cited (design evidence) -------
	{Key: "audit_immutability", Class: ClassArchitectural, Name: "Audit immutability", Desc: "The ledger is append-only + hash-chained by construction.", Cite: "core/audit/archive.go; core/audit/archiveverify.go; /v1/audit/verify"},
	{Key: "audit_export", Class: ClassArchitectural, Name: "Audit export", Desc: "Continuous WORM/SIEM export (CEF/syslog/OTLP) is available.", Cite: "core/audit/export.go; /v1/audit/export"},
	{Key: "access_control_rbac", Class: ClassArchitectural, Name: "Access control (RBAC)", Desc: "RBAC + fail-closed multi-tenant isolation enforced by the engine.", Cite: "core/auth/authorizer.go; core/internal/store/sqlstore/generic.go"},
	{Key: "human_oversight", Class: ClassArchitectural, Name: "Human oversight", Desc: "HITL/approval gates, deny-by-default, are available.", Cite: "modules/governance/approvals.go; /v1/m/governance/approvals; /v1/m/governance/approvals/{id}/decisions"},
	{Key: "encryption_transit", Class: ClassArchitectural, Name: "Encryption in transit", Desc: "TLS ≥1.2 by default on every network listener (fail-closed, no plaintext fallback); automatic mutual TLS on the in-host connector channel; verified-client-cert mutual TLS available for remote collectors.", Cite: "core/secure/tls.go; cmd/olivares/cmd_serve.go"},
	{Key: "data_minimization", Class: ClassArchitectural, Name: "Data minimization", Desc: "Only relations/metadata are persisted, never payloads or PII.", Cite: "modules/inventory/schema.go"},
	{Key: "voice_call_governance", Class: ClassArchitectural, Name: "Voice call governance", Desc: "Governed realtime voice-call plane with default-deny inbound SIP call policy, webhook→PEP accept/reject with kill-switch precedence, append-only open/close decision ledger, live-call hangup under emergency stop, and ungoverned-call detection (realtime_session_ungoverned).", Cite: "modules/voice/calls.go; /v1/m/voice/sessions; /v1/m/voice/sessions/{ref}/decisions"},
	{Key: "voice_transcript_dlp", Class: ClassArchitectural, Name: "Voice transcript DLP", Desc: "In-memory sensitivity classification of live voice transcripts using the deterministic catalog; transcript text is never persisted, and findings surface voice_transcript_unclassified plus voice_recording_sad_risk, including the financial-data-while-recording escalation.", Cite: "modules/voice/calls.go; modules/voice/findings.go; /v1/m/voice/sessions/{ref}/decisions"},
	{Key: "secure_defaults", Class: ClassArchitectural, Name: "Secure defaults", Desc: "No default credentials, single-use setup token, TLS on, localhost bind.", Cite: "cmd/olivares/boot.go; cmd/olivares/bootstrapclient.go"},
	{Key: "supply_chain", Class: ClassArchitectural, Name: "Supply-chain integrity", Desc: "Signed releases + SBOM + pinned, minimal dependencies.", Cite: "cmd/olivares/cmd_release_manifest.go; cmd/olivares/cmd_upgrade.go"},
	{Key: "forensic_capability", Class: ClassArchitectural, Name: "Forensic capability", Desc: "A reconstructable, integrity-verified incident timeline is available.", Cite: "core/audit/archiveverify.go; /v1/audit/verify"},
	{Key: "transparency_record", Class: ClassArchitectural, Name: "Transparency / record-keeping", Desc: "A system/agent inventory and record-keeping surface is available.", Cite: "modules/inventory/api.go; /v1/m/inventory/entities; /v1/m/inventory/summary"},
}

// capState holds the once-read tenant evidence the probes derive from, so evaluating
// all capabilities for a framework is a single bounded pass over the store.
type capState struct {
	auditHead   store.HeadRef
	auditHeadOK bool
	auditVerify store.VerifyReport

	edges     int64
	edgesMore bool
	drift     int64

	identities int64
	policies   int64

	securityFindings int64
	redteamFindings  int64
	evalFindings     int64
	findingsMore     bool

	evalResults int64
	deployments int64
	lineageRows int64

	riskClasses int64

	residencyRows       int64
	residencyAttested   bool
	residencyScanned    bool
	encAtRest           bool
	residencyViolations int64

	costSamples      int64
	externalActivity int64

	gpaiPostureRows int64
	gpaiVerified    int64

	modelAdmissionRows     int64
	modelAdmissionVerified int64
	aibomSeals             int64

	piiScans  int64
	dlpRules  int64
	dlpEvents int64

	recordingSessions int64

	erasureReceipts int64
}

// gatherEvidence reads the tenant's evidence once (bounded). It tolerates a missing
// sibling ext entity (the knowledge lineage table is only present once module VIII is
// registered) — an absent source becomes "absent", never an error and never a faked
// present.
func gatherEvidence(ctx context.Context, sc store.Scope) (*capState, error) {
	s := &capState{}

	head, ok, err := sc.Audit().Head(ctx)
	if err != nil {
		return nil, err
	}
	s.auditHead, s.auditHeadOK = head, ok

	rep, err := sc.Audit().Verify(ctx, 0)
	if err != nil {
		return nil, err
	}
	s.auditVerify = rep

	edges, more, err := pageCount(ctx, sc.AccessEdges().List)
	if err != nil {
		return nil, err
	}
	s.edges, s.edgesMore = int64(len(edges)), more
	if s.edges > 0 {
		// Consume module III's RECONCILED drift, not the raw store Drift: the raw
		// path double-counts cross-origin access (an agent's observed access against
		// the identity it assumes shows up as both a false unexpected access and a
		// false unused grant), which would inflate this compliance evidence with
		// false positives (C2 /). ReconciledDrift is the single owner of
		// the reconciliation logic; compliance does not reimplement it.
		diff, derr := accessmap.ReconciledDrift(ctx, sc, model.Query{Limit: listCap})
		if derr != nil {
			return nil, derr
		}
		s.drift = int64(len(diff.UnexpectedAccesses) + len(diff.UnusedGrants))
	}

	if s.identities, err = count(ctx, sc.Identities().List); err != nil {
		return nil, err
	}
	if s.policies, err = count(ctx, sc.Policies().List); err != nil {
		return nil, err
	}

	findings, fmore, err := pageCount(ctx, sc.Findings().List)
	if err != nil {
		return nil, err
	}
	s.findingsMore = fmore
	for _, f := range findings {
		switch classifyFinding(f.Kind) {
		case findingRedteam:
			s.redteamFindings++
		case findingEval:
			s.evalFindings++
		case findingExternalActivity:
			s.externalActivity++
		default:
			s.securityFindings++
		}
	}

	if s.evalResults, err = count(ctx, sc.Evals().List); err != nil {
		return nil, err
	}
	if s.deployments, err = count(ctx, sc.Deployments().List); err != nil {
		return nil, err
	}

	// Own entities.
	if s.riskClasses, err = countExt(ctx, sc, riskKind); err != nil {
		return nil, err
	}
	if err = s.readResidency(ctx, sc); err != nil {
		return nil, err
	}

	// Sibling ext (graceful): the knowledge data-lineage table, if module VIII is
	// registered. An unknown entity is an honest absent, not a failure.
	if s.lineageRows, err = countExt(ctx, sc, lineageExtKind); err != nil {
		return nil, err
	}

	// Sibling ext (graceful): FinOps' cost-sample read model, if FinOps is registered.
	// Backs resource_accounting (EU AI Act Annex IV computational-resources, FIN-12).
	if s.costSamples, err = countExt(ctx, sc, costSampleExtKind); err != nil {
		return nil, err
	}

	// Sibling ext (graceful): the models module's per-provider GPAI posture, if the
	// models module is registered. Backs supplier_gpai_posture (FIN-13) — and only
	// when a posture is operator-VERIFIED, not merely claimed.
	if err = s.readGPAIPosture(ctx, sc); err != nil {
		return nil, err
	}

	// Sibling ext (graceful): the models module's signed-model admission verdicts and
	// sealed AIBOMs. Backs signed_model_admission (only when VERIFIED) and
	// model_aibom (a sealed, ledger-anchored AIBOM exists).
	if err = s.readModelAdmission(ctx, sc); err != nil {
		return nil, err
	}
	if s.aibomSeals, err = countExt(ctx, sc, aibomExtKind); err != nil {
		return nil, err
	}

	// Sibling ext (graceful): module VIII's PII-discovery and DLP entities. Back
	// pii_discovery (scans actually ran) and dlp_enforcement (policy + scans; events
	// enrich the note — zero events with the gate armed is still present: an estate
	// with no violations is not a gap). Absent when the knowledge module is not
	// registered → an honest gap, never a fake.
	if s.piiScans, err = countExt(ctx, sc, piiScanExtKind); err != nil {
		return nil, err
	}
	if s.dlpRules, err = countExt(ctx, sc, dlpRuleExtKind); err != nil {
		return nil, err
	}
	if s.dlpEvents, err = countExt(ctx, sc, dlpEventExtKind); err != nil {
		return nil, err
	}

	// Sibling ext (graceful): the recording module's privileged-session records.
	// Backs session_recording (a session-level forensic record exists). Absent when
	// the recording module is not registered → an honest gap, never a fake.
	if s.recordingSessions, err = countExt(ctx, sc, recordingSessionExtKind); err != nil {
		return nil, err
	}

	// Own entity: the sealed erasure receipts. Backs rtbf_erasure — present
	// only when a real data-subject erasure was fulfilled and certified.
	if s.erasureReceipts, err = countExt(ctx, sc, erasureReceiptKind); err != nil {
		return nil, err
	}

	return s, nil
}

// lineageExtKind is module VIII's append-only data-lineage entity. Probed by KIND
// string only (read-only, decoupled — no import): absent when module VIII is not
// registered, which the probe reports as an honest gap.
const lineageExtKind model.Kind = "knowledge.lineage"

// costSampleExtKind is FinOps' cost-sample read model (token/compute/cost per call).
// Probed by KIND string only (read-only, decoupled — no import), exactly like the
// lineage probe: absent when FinOps is not registered → an honest gap, never a fake.
const costSampleExtKind model.Kind = "finops.cost_sample"

// gpaiPostureExtKind is the models module's per-provider GPAI compliance-posture
// entity (FIN-13). Probed by KIND string only (read-only, decoupled — no import):
// absent when the models module is not registered → an honest gap, never a fake.
const gpaiPostureExtKind model.Kind = "models.gpai_posture"

// modelAdmissionExtKind and aibomExtKind are the models module's signed-model
// admission verdict and sealed AIBOM entities. Probed by KIND string only (decoupled,
// no import), exactly like the GPAI posture: absent when models is not registered →
// an honest gap, never a fake.
const modelAdmissionExtKind model.Kind = "models.model_admission"
const aibomExtKind model.Kind = "models.aibom"

// workspaceResidencyExtKind is the models module's per-workspace mirror of the
// Anthropic Workspace Admin API data-residency config (allowed/default inference
// geos). Probed by KIND string only (read-only, decoupled — no import), exactly
// like the cost-sample probe: absent when the models module is not registered → no
// signal (honest), never a fabricated pass.
const workspaceResidencyExtKind model.Kind = "models.workspace_residency"

// piiScanExtKind / dlpRuleExtKind / dlpEventExtKind are module VIII's entities:
// the append-only PII-discovery scan runs, the per-class DLP policy rules and the
// append-only DLP enforcement events. Probed by KIND string only (read-only,
// decoupled — no import), exactly like the lineage probe: absent when the knowledge
// module is not registered → an honest gap, never a fake.
const (
	piiScanExtKind  model.Kind = "knowledge.pii_scan"
	dlpRuleExtKind  model.Kind = "knowledge.dlp_rule"
	dlpEventExtKind model.Kind = "knowledge.dlp_event"
)

// recordingSessionExtKind is the recording module's privileged-session entity.
// Probed by KIND string only (read-only, decoupled — no import), exactly like the
// lineage probe: absent when the recording module is not registered → an honest gap,
// never a fake.
const recordingSessionExtKind model.Kind = "recording.session"

// readModelAdmission counts the signed-model admission verdicts and how many are
// VERIFIED (vs merely recorded) — the claim-vs-verified honesty line applied to
// model artifacts. The signature_verified column name mirrors the models schema.
func (s *capState) readModelAdmission(ctx context.Context, sc store.Scope) error {
	repo, err := sc.Ext(modelAdmissionExtKind)
	if err != nil {
		if errors.Is(err, store.ErrUnknownEntity) {
			return nil
		}
		return err
	}
	rows, err := listAll(ctx, repo)
	if err != nil {
		return err
	}
	s.modelAdmissionRows = int64(len(rows))
	for _, rec := range rows {
		if rec.Bool("signature_verified") {
			s.modelAdmissionVerified++
		}
	}
	return nil
}

// readGPAIPosture reads the per-provider GPAI postures and counts how many are
// operator-VERIFIED (vs merely claimed). The verified column name mirrors the
// models module's schema.
func (s *capState) readGPAIPosture(ctx context.Context, sc store.Scope) error {
	repo, err := sc.Ext(gpaiPostureExtKind)
	if err != nil {
		if errors.Is(err, store.ErrUnknownEntity) {
			return nil
		}
		return err
	}
	rows, err := listAll(ctx, repo)
	if err != nil {
		return err
	}
	s.gpaiPostureRows = int64(len(rows))
	for _, rec := range rows {
		if rec.Bool("verified") {
			s.gpaiVerified++
		}
	}
	return nil
}

func (s *capState) readResidency(ctx context.Context, sc store.Scope) error {
	repo, err := sc.Ext(residencyKind)
	if err != nil {
		if errors.Is(err, store.ErrUnknownEntity) {
			return nil
		}
		return err
	}
	rows, err := listAll(ctx, repo)
	if err != nil {
		return err
	}
	s.residencyRows = int64(len(rows))
	for _, rec := range rows {
		if rec.Bool(colSelfHosted) {
			s.residencyAttested = true
		}
		if rec.Bool(colEncAtRest) {
			s.encAtRest = true
		}
		// A scan stamps last_checked. Attestation alone is a CLAIM, not evidence of
		// "no observed egress" — only a completed scan can back that (docs/SECURITY-HARDENING.md).
		if rec.String(colLastChecked) != "" {
			s.residencyScanned = true
		}
		s.residencyViolations += rec.Int(colViolations)
	}
	return nil
}

// findingClass coarsely classifies a core Finding by its Kind so the probes decouple
// from the exact strings sibling modules emit.
type findingClass int

const (
	findingSecurity findingClass = iota
	findingRedteam
	findingEval
	findingExternalActivity
)

func classifyFinding(kind string) findingClass {
	switch kind {
	case findingKindRedteam:
		return findingRedteam
	case findingKindEvalRegression:
		return findingEval
	case findingKindExternalActivity:
		// External Compliance-API activity records are evidence for external_activity,
		// NOT security findings — they must never inflate threat_detection.
		return findingExternalActivity
	default:
		// (deliberate, verified 2026-06-10): the knowledge module's bus kinds
		// (knowledge_pii_discovered MEDIUM, knowledge_dlp_blocked MEDIUM/HIGH) never
		// land in core Finding rows under their own kind strings — modules only
		// PUBLISH findings on the bus, and security's anomaly reactor (the sole
		// persister, modules/security/anomaly.go onEvent) re-kinds cross-module HIGH+
		// findings to its own "anomaly" kind (the origin kind is demoted to Source)
		// and drops sub-HIGH ones entirely. A HIGH dlp-block persisted as an anomaly
		// IS a genuine security signal, so counting it under threat_detection is
		// correct, not inflation — no branch is needed here.
		return findingSecurity
	}
}

const (
	findingKindRedteam          = "redteam"
	findingKindEvalRegression   = "eval_regression"
	findingKindExternalActivity = "external_activity"
)

// evaluateCapabilities evaluates EVERY capability in the catalog against the gathered
// evidence and returns the result keyed by capability. Operational capabilities are
// present only when real data backs them; architectural ones are present by design
// with a citation.
func evaluateCapabilities(s *capState) map[CapabilityKey]CapabilityEvidence {
	out := make(map[CapabilityKey]CapabilityEvidence, len(capabilityCatalog))
	for _, c := range capabilityCatalog {
		out[c.Key] = evaluateCapability(c, s)
	}
	return out
}

func evaluateCapability(c Capability, s *capState) CapabilityEvidence {
	ev := CapabilityEvidence{Key: c.Key, Class: c.Class}

	// Architectural capabilities are present by construction, with a citation. They
	// are labeled as design evidence (not telemetry) by their class.
	if c.Class == ClassArchitectural {
		ev.State = EvidencePresent
		ev.Detail = "platform guarantee by design (cited): " + c.Desc
		ev.Refs = []EvidenceRef{{Kind: "design", Detail: c.Cite}}
		return ev
	}

	// Operational capabilities: present only when real tenant evidence exists.
	switch c.Key {
	case "audit_trail":
		if s.auditHeadOK {
			ev.State, ev.Count = EvidencePresent, s.auditHead.Seq
			ev.Detail = fmt.Sprintf("audit ledger has %d sealed events", s.auditHead.Seq)
			ev.Refs = []EvidenceRef{{Kind: "audit_chain", Detail: fmt.Sprintf("head seq %d", s.auditHead.Seq)}}
		} else {
			ev.State, ev.Detail = EvidenceAbsent, "no audit events recorded for this tenant"
		}
	case "audit_integrity":
		if s.auditHeadOK && s.auditVerify.OK {
			ev.State, ev.Count = EvidencePresent, s.auditVerify.Checked
			ev.Detail = fmt.Sprintf("hash-chain verified intact across %d events", s.auditVerify.Checked)
			ev.Refs = []EvidenceRef{{Kind: "audit_chain", Detail: fmt.Sprintf("verify ok, checked %d", s.auditVerify.Checked)}}
		} else if !s.auditHeadOK {
			ev.State, ev.Detail = EvidenceAbsent, "no audit chain to verify"
		} else {
			ev.State = EvidenceAbsent
			ev.Detail = "audit hash-chain integrity check FAILED: " + s.auditVerify.Reason
			ev.Refs = []EvidenceRef{{Kind: "audit_chain", Detail: fmt.Sprintf("break at seq %d: %s", s.auditVerify.BreakAt, s.auditVerify.Reason)}}
		}
	case "access_observability":
		presentCount(&ev, s.edges, s.edgesMore, "access edges", "access_edge")
	case "least_privilege_drift":
		if s.edges > 0 {
			ev.State, ev.Count = EvidencePresent, s.drift
			ev.Detail = fmt.Sprintf("permitted-vs-observed drift computable; %d reconciled drift edges", s.drift)
			ev.Refs = []EvidenceRef{{Kind: "entity", Detail: "access_edge (drift)"}}
		} else {
			ev.State, ev.Detail = EvidenceAbsent, "no access edges to compute least-privilege drift from"
		}
	case "identity_governance":
		n := s.identities + s.policies
		if n > 0 {
			ev.State, ev.Count = EvidencePresent, n
			ev.Detail = fmt.Sprintf("%d identities and %d policies governed", s.identities, s.policies)
			ev.Refs = []EvidenceRef{{Kind: "entity", Detail: "identity, policy"}}
		} else {
			ev.State, ev.Detail = EvidenceAbsent, "no governed identities or policies recorded"
		}
	case "threat_detection":
		presentCount(&ev, s.securityFindings, s.findingsMore, "security findings", "finding")
	case "adversarial_testing":
		presentCount(&ev, s.redteamFindings, s.findingsMore, "red-team findings", "finding")
	case "quality_evaluation":
		n := s.evalResults + s.evalFindings
		if n > 0 {
			ev.State, ev.Count = EvidencePresent, n
			ev.Detail = fmt.Sprintf("%d eval results and %d regression findings", s.evalResults, s.evalFindings)
			ev.Refs = []EvidenceRef{{Kind: "entity", Detail: "eval_result, finding"}}
		} else {
			ev.State, ev.Detail = EvidenceAbsent, "no eval results recorded"
		}
	case "change_management":
		presentCount(&ev, s.deployments, false, "deployments", "deployment")
	case "data_lineage":
		presentCount(&ev, s.lineageRows, false, "data-lineage records", "knowledge.lineage")
	case "risk_classification":
		presentCount(&ev, s.riskClasses, false, "agent risk classifications", "compliance.risk_class")
	case "data_residency":
		switch {
		case s.residencyViolations > 0:
			ev.State, ev.Count = EvidenceAbsent, s.residencyViolations
			ev.Detail = fmt.Sprintf("residency attested but %d perimeter-egress violation(s) observed", s.residencyViolations)
			ev.Refs = []EvidenceRef{{Kind: "attestation", Detail: "compliance.residency (violations)"}}
		case s.residencyAttested && s.residencyScanned:
			ev.State, ev.Count = EvidencePresent, s.residencyRows
			ev.Detail = "residency attested self-hosted and scanned with no observed perimeter-egress violation"
			ev.Refs = []EvidenceRef{{Kind: "attestation", Detail: "compliance.residency"}}
		case s.residencyAttested:
			// Attested but never scanned: the no-egress claim is unverified — an honest
			// gap, not evidence (docs/SECURITY-HARDENING.md).
			ev.State, ev.Detail = EvidenceAbsent, "residency attested but not yet scanned for egress (run /residency/scan)"
		default:
			ev.State, ev.Detail = EvidenceAbsent, "no residency attestation recorded for this tenant"
		}
	case "encryption_at_rest":
		if s.encAtRest {
			ev.State = EvidencePresent
			ev.Detail = "at-rest encryption attested ON"
			ev.Refs = []EvidenceRef{{Kind: "attestation", Detail: "compliance.residency (encryption_at_rest)"}}
		} else {
			ev.State, ev.Detail = EvidenceAbsent, "at-rest encryption not attested (opt-in; default off)"
		}
	case "resource_accounting":
		presentCount(&ev, s.costSamples, false, "resource-accounting records (token/compute/cost per call)", "finops.cost_sample")
	case "external_activity":
		presentCount(&ev, s.externalActivity, s.findingsMore, "external compliance-activity records", "finding(external_activity)")
	case "supplier_gpai_posture":
		switch {
		case s.gpaiVerified > 0:
			ev.State, ev.Count = EvidencePresent, s.gpaiVerified
			ev.Detail = fmt.Sprintf("%d brokered provider(s) have an operator-verified GPAI compliance posture", s.gpaiVerified)
			ev.Refs = []EvidenceRef{{Kind: "attestation", Detail: "models.gpai_posture (verified)"}}
		case s.gpaiPostureRows > 0:
			// Recorded but not operator-verified: a self-reported claim is not evidence
			// (docs/SECURITY-HARDENING.md, the residency claim-vs-verified line applied to suppliers).
			ev.State, ev.Count = EvidenceAbsent, s.gpaiPostureRows
			ev.Detail = fmt.Sprintf("%d GPAI posture(s) recorded but none operator-verified — a claim, not evidence (FIN-13)", s.gpaiPostureRows)
		default:
			ev.State, ev.Detail = EvidenceAbsent, "no GPAI provider compliance posture recorded for this tenant"
		}
	case "signed_model_admission":
		switch {
		case s.modelAdmissionVerified > 0:
			ev.State, ev.Count = EvidencePresent, s.modelAdmissionVerified
			ev.Detail = fmt.Sprintf("%d model version(s) carry a verified signed-model admission", s.modelAdmissionVerified)
			ev.Refs = []EvidenceRef{{Kind: "attestation", Detail: "models.model_admission (verified)"}}
		case s.modelAdmissionRows > 0:
			// Recorded but not verified: a claim, not evidence (the GPAI/residency
			// claim-vs-verified line applied to model artifacts, docs/SECURITY-HARDENING.md).
			ev.State, ev.Count = EvidenceAbsent, s.modelAdmissionRows
			ev.Detail = fmt.Sprintf("%d model admission(s) recorded but none verified — a claim, not evidence", s.modelAdmissionRows)
		default:
			ev.State, ev.Detail = EvidenceAbsent, "no signed-model admission recorded for this tenant"
		}
	case "model_aibom":
		presentCount(&ev, s.aibomSeals, false, "sealed model AIBOM(s)", "models.aibom")
	case "pii_discovery":
		presentCount(&ev, s.piiScans, false, "PII discovery scan runs", "knowledge.pii_scan")
	case "session_recording":
		presentCount(&ev, s.recordingSessions, false, "privileged session recordings", "recording.session")
	case "rtbf_erasure":
		presentCount(&ev, s.erasureReceipts, false, "sealed erasure receipt(s)", "compliance.erasure_receipt")
	case "dlp_enforcement":
		switch {
		case s.dlpRules > 0 && s.piiScans > 0:
			// Zero enforcement events with the gate armed is still PRESENT: an estate
			// with no violations is not a gap. The event count enriches the note —
			// events prove the gate fired, rules+scans prove it is enforceable.
			ev.State, ev.Count = EvidencePresent, s.dlpRules
			ev.Detail = fmt.Sprintf("deny-closed DLP gate armed: %d rule(s) over scanned content; %d enforcement event(s) recorded", s.dlpRules, s.dlpEvents)
			ev.Refs = []EvidenceRef{{Kind: "entity", Detail: "knowledge.dlp_rule, knowledge.pii_scan, knowledge.dlp_event"}}
		case s.dlpRules > 0:
			// Rules without a single discovery scan: nothing is labeled, so the gate
			// has no classes to key on — a recorded claim, not enforceable evidence
			// (the residency attested-but-unscanned honesty line, docs/SECURITY-HARDENING.md).
			ev.State, ev.Count = EvidenceAbsent, s.dlpRules
			ev.Detail = fmt.Sprintf("%d DLP rule(s) recorded but no discovery scan has run — a policy without labels is a claim, not enforceable evidence (run a knowledge scan)", s.dlpRules)
		case s.piiScans > 0:
			ev.State, ev.Detail = EvidenceAbsent, "discovery scans exist but no DLP rule is configured (the gate is inert)"
		default:
			ev.State, ev.Detail = EvidenceAbsent, "no DLP policy or discovery scan recorded for this tenant"
		}
	default:
		// An operational capability with no probe is an honest unknown, never a pass.
		ev.State, ev.Detail = EvidenceUnknown, "no probe wired for this capability"
	}
	return ev
}

// presentCount sets an evidence result present when count>0, else absent — the common
// "real rows exist" probe shape.
func presentCount(ev *CapabilityEvidence, n int64, more bool, label, entity string) {
	if n > 0 {
		ev.State, ev.Count, ev.More = EvidencePresent, n, more
		suffix := ""
		if more {
			suffix = "+"
		}
		ev.Detail = fmt.Sprintf("%d%s %s recorded", n, suffix, label)
		ev.Refs = []EvidenceRef{{Kind: "entity", Detail: entity}}
	} else {
		ev.State = EvidenceAbsent
		ev.Detail = "no " + label + " recorded for this tenant"
	}
}

// count fully pages a typed repository to a bounded total (good enough for an
// evidence count; the store caps a page at listCap so a very large set reports the
// cap, never an unbounded scan in the hot path).
func count[T any](ctx context.Context, list func(context.Context, model.Query) ([]T, model.Page, error)) (int64, error) {
	rows, _, err := list(ctx, model.Query{Limit: listCap})
	if err != nil {
		return 0, err
	}
	return int64(len(rows)), nil
}

// countExt counts rows of a module ext entity, tolerating an unregistered entity (an
// honest absent, e.g. a sibling module not loaded).
func countExt(ctx context.Context, sc store.Scope, kind model.Kind) (int64, error) {
	repo, err := sc.Ext(kind)
	if err != nil {
		if errors.Is(err, store.ErrUnknownEntity) {
			return 0, nil
		}
		return 0, err
	}
	rows, _, err := repo.List(ctx, model.Query{Limit: listCap})
	if err != nil {
		return 0, err
	}
	return int64(len(rows)), nil
}
