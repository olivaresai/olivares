// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package compliance

import "github.com/olivaresai/olivares/core/model"

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
