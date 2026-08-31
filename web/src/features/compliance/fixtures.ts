// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Deterministic Compliance fixtures shaped exactly like the contract — used by
// the component tests and the visual e2e route mocks. They are realistic and
// non-trivial: every control status appears (satisfied/by_design/partial/gap/
// unmapped) so the StatusBar and badges read like the real product, an evidence
// package shows a clean integrity check AND a broken one, a risk classification spans
// the EU AI Act tiers, and a residency region carries an observed violation. NOTHING
// here is a secret or a payload — only control status, capability state, and
// tamper-evidence fingerprints (hashes are placeholder hex, never a real digest).
import type {
  ComplianceSummaryResponse,
  EvidenceExportResult,
  EvidencePackage,
  FrameworkListResponse,
  FrameworkStatusResponse,
  GapAnalysisResponse,
  OscalExport,
  ResidencyAttestation,
  RiskClassification,
} from './types'

const DISCLAIMER =
  'Control status and evidence only — this is NOT a certification or a statement of legal compliance. Verify with your auditor.'

export const frameworksFixture: FrameworkListResponse = {
  items: [
    {
      id: 'eu_ai_act',
      name: 'EU AI Act',
      version: 'Regulation (EU) 2024/1689',
      authority: 'European Union',
      controls: 11,
    },
    {
      id: 'nist_ai_rmf',
      name: 'NIST AI RMF',
      version: 'AI RMF 1.0',
      authority: 'NIST',
      controls: 14,
    },
    {
      id: 'iso_42001',
      name: 'ISO/IEC 42001',
      version: '2023',
      authority: 'ISO/IEC',
      controls: 16,
    },
    {
      id: 'soc2_tsc',
      name: 'SOC 2 TSC',
      version: '2017 (rev. 2022)',
      authority: 'AICPA',
      controls: 13,
    },
    {
      id: 'iso_27001_2022',
      name: 'ISO/IEC 27001',
      version: '2022',
      authority: 'ISO/IEC',
      controls: 12,
    },
    {
      id: 'gdpr',
      name: 'GDPR',
      version: 'Regulation (EU) 2016/679',
      authority: 'European Union',
      controls: 10,
    },
    // --- design-toward crosswalks (threat models / guidance / overlays) -----
    {
      id: 'nist_ai_600_1',
      name: 'NIST AI RMF Generative AI Profile (AI 600-1)',
      version: 'NIST AI 600-1 (July 2024)',
      authority: 'NIST',
      controls: 12,
    },
    {
      id: 'csa_maestro',
      name: 'CSA MAESTRO — Agentic AI threat-modeling framework (7-layer)',
      version: 'Cloud Security Alliance, 2025-02-06',
      authority: 'Cloud Security Alliance (CSA)',
      controls: 7,
    },
    {
      id: 'owasp_agentic_tm',
      name: 'OWASP Agentic AI — Threats and Mitigations (T1–T15)',
      version: 'OWASP GenAI Security Project, v1.0 (2025-02-17)',
      authority: 'OWASP GenAI Security Project',
      controls: 15,
    },
    {
      id: 'cisa_agentic_adoption',
      name: 'CISA/Five-Eyes — Careful Adoption of Agentic AI Services',
      version: 'Joint guidance, 2026-05',
      authority: 'CISA and international partners (Five-Eyes)',
      controls: 5,
    },
    {
      id: 'nist_cosais',
      name: 'NIST SP 800-53 Control Overlays for Securing AI Systems (COSAiS) — design-toward',
      version: 'NIST COSAiS — IN DEVELOPMENT (concept paper 2025-08)',
      authority: 'NIST (csrc.nist.gov/Projects/cosais)',
      controls: 4,
    },
  ],
  disclaimer: DISCLAIMER,
}

const EU_SUMMARY = {
  total: 11,
  satisfied: 3,
  by_design: 2,
  partial: 4,
  gap: 1,
  unmapped: 1,
}

const EU_AI_ACT_DISCLAIMER =
  'Technical control mapping for an AI control plane only; not legal advice and not a certification or conformity assessment of compliance with Regulation (EU) 2024/1689.'

export const statusFixture: FrameworkStatusResponse = {
  assessment: {
    framework: 'eu_ai_act',
    name: 'EU AI Act',
    version: 'Regulation (EU) 2024/1689',
    disclaimer: EU_AI_ACT_DISCLAIMER,
    summary: EU_SUMMARY,
    controls: [
      {
        control_id: 'art_12',
        title: 'Record-keeping',
        requirement:
          'High-risk AI systems shall technically allow for the automatic recording of events (logs) over their lifetime.',
        criterion:
          'A tamper-evident, queryable event log exists and is retained.',
        status: 'satisfied',
        note: '',
        capabilities: [
          {
            key: 'audit_trail',
            class: 'operational',
            state: 'present',
            detail: 'Audit ledger has 7 sealed events',
            count: 7,
            refs: [{ kind: 'audit_chain', detail: 'head seq 7' }],
          },
          {
            key: 'resource_accounting',
            class: 'operational',
            state: 'present',
            detail:
              'FinOps records token/compute/cost per inference (Annex IV(2)(c)) — 1,284 cost samples',
            count: 1284,
            refs: [{ kind: 'entity', detail: 'finops.cost_sample' }],
          },
          {
            key: 'external_activity',
            class: 'operational',
            state: 'present',
            detail:
              'Anthropic Compliance-API Activity Feed records appended to the ledger as audit/eDiscovery evidence',
            count: 42,
            refs: [{ kind: 'entity', detail: 'connector:claude-compliance' }],
          },
        ],
      },
      {
        control_id: 'art_15',
        title: 'Accuracy, robustness and cybersecurity',
        requirement:
          'High-risk AI systems shall be resilient to errors, faults and attempts to alter their use or behaviour.',
        criterion:
          'Adversarial robustness and access boundaries are designed into the platform.',
        status: 'by_design',
        note: 'Provided by the platform architecture — no per-tenant operational telemetry attests this yet.',
        capabilities: [
          {
            key: 'isolation_boundary',
            class: 'architectural',
            state: 'present',
            detail: 'Per-agent sandbox isolation is enforced by design',
            refs: [
              { kind: 'design_ref', detail: 'docs/05 §6 isolation model' },
            ],
          },
        ],
      },
      {
        control_id: 'art_14',
        title: 'Human oversight',
        requirement:
          'High-risk AI systems shall be designed to be effectively overseen by natural persons.',
        criterion: 'A human can review and override autonomous decisions.',
        status: 'partial',
        note: 'Review hooks exist for risk classification; not every autonomous path is gated yet.',
        capabilities: [
          {
            key: 'risk_review',
            class: 'operational',
            state: 'present',
            detail: '2 of 4 autonomous agents have a reviewed risk tier',
            count: 2,
          },
          {
            key: 'kill_switch',
            class: 'architectural',
            state: 'unknown',
            detail: 'Per-agent halt control not observed for this tenant',
          },
        ],
      },
      {
        control_id: 'art_9',
        title: 'Risk management system',
        requirement:
          'A risk management system shall be established for high-risk AI systems.',
        criterion:
          'Agents are classified by risk and the rationale is recorded.',
        status: 'partial',
        note: '',
        capabilities: [
          {
            key: 'risk_classification',
            class: 'operational',
            state: 'present',
            detail: '4 of 6 agents classified',
            count: 4,
          },
        ],
      },
      {
        control_id: 'art_10',
        title: 'Data and data governance',
        requirement:
          'Training, validation and testing data sets shall be subject to governance practices.',
        criterion: 'Data lineage and residency are attested.',
        status: 'gap',
        note: '',
        capabilities: [
          {
            key: 'data_residency',
            class: 'operational',
            state: 'absent',
            detail: 'No residency attestation present after the last scan',
          },
        ],
      },
      {
        control_id: 'art_11',
        title: 'Technical documentation',
        requirement:
          'Technical documentation (per Annex IV) shall be drawn up before a high-risk AI system is placed on the market and kept up to date.',
        criterion:
          'Change records and a maintained inventory keep evidence current, plus per-inference accounting of computational resources (Annex IV(2)(c)).',
        status: 'partial',
        note: 'Annex IV(2)(c) requires documenting the computational resources USED to develop, train, test and validate the system; the control plane evidences operational compute/cost accounting (resource_accounting), NOT training-time figures or dataset quality.',
        capabilities: [
          {
            key: 'resource_accounting',
            class: 'operational',
            state: 'present',
            detail:
              'Operational compute/cost accounting per inference — NOT dataset quality, energy or carbon',
            count: 1284,
          },
          {
            key: 'supplier_gpai_posture',
            class: 'operational',
            state: 'present',
            detail:
              'Operator-VERIFIED GPAI posture on record for 2 of 3 brokered providers; 1 provider self-reported only (a claim, not evidence)',
            count: 2,
            refs: [{ kind: 'entity', detail: 'models.gpai_posture' }],
          },
          {
            key: 'transparency_record',
            class: 'architectural',
            state: 'present',
            detail: 'System/agent inventory and record-keeping surface',
            refs: [{ kind: 'design', detail: 'modules I/II + audit' }],
          },
        ],
      },
      {
        control_id: 'art_61',
        title: 'Post-market monitoring',
        requirement:
          'Providers shall establish a post-market monitoring system.',
        criterion: 'Continuous monitoring telemetry is mapped to this control.',
        status: 'unmapped',
        note: 'No capability is mapped to this control yet — honest unknown, not a failure.',
        capabilities: [],
      },
    ],
  },
  disclaimer: DISCLAIMER,
}

// --- a design-toward crosswalk status fixture --------------------------
// nist_cosais is IN DEVELOPMENT (concept paper Aug 2025); the framework's OWN
// disclaimer (rendered prominently) carries the no-conformance-claim wording.
const COSAIS_DISCLAIMER =
  'Crosswalk to NIST SP 800-53 Control Overlays for Securing AI Systems (COSAiS), which is IN DEVELOPMENT and NOT a final standard (concept paper Aug 2025, annotated outline Jan 2026). Also references the OpenID AIIM Community Group, which by OIDF policy produces no specifications. This entry is explicitly design-toward / in development — NO conformance claim.'

export const crosswalkStatusFixture: FrameworkStatusResponse = {
  assessment: {
    framework: 'nist_cosais',
    name: 'NIST SP 800-53 Control Overlays for Securing AI Systems (COSAiS) — design-toward',
    version: 'NIST COSAiS — IN DEVELOPMENT (concept paper 2025-08)',
    disclaimer: COSAIS_DISCLAIMER,
    summary: {
      total: 4,
      satisfied: 1,
      by_design: 2,
      partial: 0,
      gap: 0,
      unmapped: 1,
    },
    controls: [
      {
        control_id: 'chain_of_custody',
        title: 'Chain of custody / traceability (overlay)',
        requirement:
          'Maintain an attributable, tamper-evident record of agent actions.',
        criterion:
          'Append-only, hash-chained, integrity-verified audit ledger (immutable by construction).',
        status: 'satisfied',
        note: '',
        capabilities: [
          {
            key: 'audit_trail',
            class: 'operational',
            state: 'present',
            detail: 'Audit ledger has 7 sealed events',
            count: 7,
          },
        ],
      },
      {
        control_id: 'agent_identity_management',
        title: 'Agent identity management (overlay / AIIM)',
        requirement:
          'Manage agent identities and authorization (the OpenID AIIM problem space).',
        criterion: 'Governed non-human identities and RBAC.',
        status: 'by_design',
        note: "Tracks the OpenID AIIM Community Group's problem space; AIIM is a community group (no specifications) — design-toward, no conformance claim.",
        capabilities: [
          {
            key: 'identity_governance',
            class: 'operational',
            state: 'present',
            detail: 'Non-human identities and policies are governed',
          },
          {
            key: 'access_control_rbac',
            class: 'architectural',
            state: 'present',
            detail: 'RBAC + fail-closed multi-tenant isolation by design',
          },
        ],
      },
      {
        control_id: 'agent_containment',
        title: 'Agent containment (overlay)',
        requirement:
          'Contain agent behavior with detective guardrails and human gates.',
        criterion:
          'Guardrail threat detection and deny-by-default human-in-the-loop gates.',
        status: 'by_design',
        note: '',
        capabilities: [
          {
            key: 'human_oversight',
            class: 'architectural',
            state: 'present',
            detail: 'HITL/approval gates, deny-by-default, available',
          },
        ],
      },
      {
        control_id: 'least_privilege_tool_access',
        title: 'Least-privilege tool access (overlay)',
        requirement:
          "Constrain and monitor an agent's tool/resource access to least privilege.",
        criterion:
          'Observed access (R/RW) with permitted-vs-observed least-privilege drift.',
        status: 'unmapped',
        note: 'No capability is mapped to this control yet for this tenant — honest unknown.',
        capabilities: [],
      },
    ],
  },
  disclaimer: DISCLAIMER,
}

export const gapsFixture: GapAnalysisResponse = {
  framework: 'eu_ai_act',
  name: 'EU AI Act',
  summary: EU_SUMMARY,
  gaps: [
    {
      control_id: 'art_10',
      title: 'Data and data governance',
      requirement:
        'Training, validation and testing data sets shall be subject to governance practices.',
      criterion: 'Data lineage and residency are attested.',
      status: 'gap',
      note: '',
      capabilities: [],
      missing_capabilities: ['data_residency'],
    },
    {
      control_id: 'art_14',
      title: 'Human oversight',
      requirement:
        'High-risk AI systems shall be designed to be effectively overseen by natural persons.',
      criterion: 'A human can review and override autonomous decisions.',
      status: 'partial',
      note: 'Review hooks exist for risk classification; not every autonomous path is gated yet.',
      capabilities: [],
      missing_capabilities: ['kill_switch'],
    },
    {
      control_id: 'art_9',
      title: 'Risk management system',
      requirement:
        'A risk management system shall be established for high-risk AI systems.',
      criterion: 'Agents are classified by risk and the rationale is recorded.',
      status: 'partial',
      note: '',
      capabilities: [],
      missing_capabilities: ['risk_classification'],
    },
    {
      control_id: 'art_61',
      title: 'Post-market monitoring',
      requirement: 'Providers shall establish a post-market monitoring system.',
      criterion: 'Continuous monitoring telemetry is mapped to this control.',
      status: 'unmapped',
      note: 'No capability is mapped to this control yet — honest unknown, not a failure.',
      capabilities: [],
      missing_capabilities: ['post_market_monitoring'],
    },
  ],
  disclaimer: DISCLAIMER,
}

export const summaryFixture: ComplianceSummaryResponse = {
  frameworks: [
    {
      framework: 'eu_ai_act',
      name: 'EU AI Act',
      version: 'Regulation (EU) 2024/1689',
      summary: EU_SUMMARY,
    },
    {
      framework: 'nist_ai_rmf',
      name: 'NIST AI RMF',
      version: 'AI RMF 1.0',
      summary: {
        total: 14,
        satisfied: 5,
        by_design: 3,
        partial: 3,
        gap: 2,
        unmapped: 1,
      },
    },
    {
      framework: 'iso_42001',
      name: 'ISO/IEC 42001',
      version: '2023',
      summary: {
        total: 16,
        satisfied: 4,
        by_design: 4,
        partial: 5,
        gap: 2,
        unmapped: 1,
      },
    },
    {
      framework: 'soc2_tsc',
      name: 'SOC 2 TSC',
      version: '2017 (rev. 2022)',
      summary: {
        total: 13,
        satisfied: 6,
        by_design: 2,
        partial: 3,
        gap: 1,
        unmapped: 1,
      },
    },
    {
      framework: 'iso_27001_2022',
      name: 'ISO/IEC 27001',
      version: '2022',
      summary: {
        total: 12,
        satisfied: 5,
        by_design: 2,
        partial: 3,
        gap: 1,
        unmapped: 1,
      },
    },
    {
      framework: 'gdpr',
      name: 'GDPR',
      version: 'Regulation (EU) 2016/679',
      summary: {
        total: 10,
        satisfied: 4,
        by_design: 1,
        partial: 3,
        gap: 1,
        unmapped: 1,
      },
    },
    // --- design-toward crosswalks ------------------------------------------
    {
      framework: 'nist_ai_600_1',
      name: 'NIST AI RMF Generative AI Profile (AI 600-1)',
      version: 'NIST AI 600-1 (July 2024)',
      summary: {
        total: 12,
        satisfied: 4,
        by_design: 1,
        partial: 2,
        gap: 1,
        unmapped: 4,
      },
    },
    {
      framework: 'csa_maestro',
      name: 'CSA MAESTRO — Agentic AI threat-modeling framework (7-layer)',
      version: 'Cloud Security Alliance, 2025-02-06',
      summary: {
        total: 7,
        satisfied: 3,
        by_design: 2,
        partial: 1,
        gap: 0,
        unmapped: 1,
      },
    },
    {
      framework: 'owasp_agentic_tm',
      name: 'OWASP Agentic AI — Threats and Mitigations (T1–T15)',
      version: 'OWASP GenAI Security Project, v1.0 (2025-02-17)',
      summary: {
        total: 15,
        satisfied: 6,
        by_design: 3,
        partial: 3,
        gap: 1,
        unmapped: 2,
      },
    },
    {
      framework: 'cisa_agentic_adoption',
      name: 'CISA/Five-Eyes — Careful Adoption of Agentic AI Services',
      version: 'Joint guidance, 2026-05',
      summary: {
        total: 5,
        satisfied: 2,
        by_design: 2,
        partial: 0,
        gap: 0,
        unmapped: 1,
      },
    },
    {
      framework: 'nist_cosais',
      name: 'NIST SP 800-53 Control Overlays for Securing AI Systems (COSAiS) — design-toward',
      version: 'NIST COSAiS — IN DEVELOPMENT (concept paper 2025-08)',
      summary: {
        total: 4,
        satisfied: 1,
        by_design: 2,
        partial: 0,
        gap: 0,
        unmapped: 1,
      },
    },
  ],
  disclaimer: DISCLAIMER,
}

export const evidenceFixture: EvidencePackage[] = [
  {
    id: 'evp-2026q2-euaiact',
    framework: 'eu_ai_act',
    framework_version: 'Regulation (EU) 2024/1689',
    generated_at: '2026-06-03T09:12:00Z',
    generated_by: 'user:fran',
    ledger_seq: 12,
    ledger_hash:
      '9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08',
    integrity_ok: true,
    integrity_checked: 12,
    integrity_reason: '',
    summary: EU_SUMMARY,
    manifest_hash:
      '60303ae22b998861bce3b28f33eec1be758a213c86c93c076dbe9f558c11c752',
    scope_note: 'Q2 2026 internal audit',
    disclaimer: DISCLAIMER,
  },
  {
    id: 'evp-2026q1-soc2',
    framework: 'soc2_tsc',
    framework_version: '2017 (rev. 2022)',
    generated_at: '2026-03-30T16:40:00Z',
    generated_by: 'user:fran',
    ledger_seq: 8,
    ledger_hash:
      '2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae',
    integrity_ok: false,
    integrity_checked: 8,
    integrity_reason:
      'Hash chain broken at seq 5 — re-seal required before export.',
    summary: {
      total: 13,
      satisfied: 6,
      by_design: 2,
      partial: 3,
      gap: 1,
      unmapped: 1,
    },
    manifest_hash:
      'fcde2b2edba56bf408601fb721fe9b5c338d10ee429ea04fae5511b68fbf8fb9',
    scope_note: 'Q1 2026 readiness snapshot',
    disclaimer: DISCLAIMER,
  },
]

export const riskFixture: RiskClassification[] = [
  {
    id: 'rsk-support-triage',
    subject_kind: 'agent',
    subject_ref: 'agent_support_triage',
    agent_id: 'agent_support_triage',
    tier: 'high',
    suggested_tier: 'high',
    state: 'approved',
    rationale:
      'high: 1 high/critical finding(s), 6 write access(es) across 4 resources',
    nist_functions: ['GOVERN', 'MAP', 'MEASURE', 'MANAGE'],
    signals: {
      rw_edges: 6,
      total_edges: 8,
      distinct_resources: 4,
      high_severity_findings: 1,
      scheduled: false,
      autonomous: false,
    },
    reviewed_by: 'user:fran',
    classified_at: '2026-06-02T11:30:00Z',
    disclaimer: DISCLAIMER,
  },
  {
    id: 'rsk-nightly-batch',
    subject_kind: 'agent',
    subject_ref: 'agent_nightly_batch',
    agent_id: 'agent_nightly_batch',
    tier: 'unacceptable',
    suggested_tier: 'high',
    state: 'overridden',
    rationale:
      'overridden to unacceptable after human review: unsupervised autonomous writes to production',
    nist_functions: ['GOVERN', 'MANAGE'],
    signals: {
      rw_edges: 9,
      total_edges: 11,
      distinct_resources: 6,
      high_severity_findings: 2,
      scheduled: true,
      autonomous: true,
    },
    reviewed_by: 'user:fran',
    classified_at: '2026-06-01T07:05:00Z',
    disclaimer: DISCLAIMER,
  },
  {
    id: 'rsk-code-reviewer',
    subject_kind: 'agent',
    subject_ref: 'agent_code_reviewer',
    agent_id: 'agent_code_reviewer',
    tier: 'limited',
    suggested_tier: 'limited',
    state: 'suggested',
    rationale: 'limited: 2 write access(es), no high/critical findings',
    nist_functions: ['MAP', 'MEASURE'],
    signals: {
      rw_edges: 2,
      total_edges: 5,
      distinct_resources: 2,
      high_severity_findings: 0,
      scheduled: false,
      autonomous: false,
    },
    reviewed_by: '',
    classified_at: '2026-06-03T14:22:00Z',
    disclaimer: DISCLAIMER,
  },
  {
    id: 'rsk-docs-indexer',
    subject_kind: 'agent',
    subject_ref: 'agent_docs_indexer',
    agent_id: 'agent_docs_indexer',
    tier: 'minimal',
    suggested_tier: 'minimal',
    state: 'suggested',
    rationale: 'minimal: read-only access, no findings',
    nist_functions: ['MAP'],
    signals: {
      rw_edges: 0,
      total_edges: 3,
      distinct_resources: 3,
      high_severity_findings: 0,
      scheduled: false,
      autonomous: false,
    },
    reviewed_by: '',
    classified_at: '2026-06-03T14:22:00Z',
    disclaimer: DISCLAIMER,
  },
]

export const residencyFixture: ResidencyAttestation[] = [
  {
    id: 'res-eu-west',
    region: 'eu-west',
    perimeter: 'self-hosted',
    self_hosted: true,
    encryption_at_rest: true,
    data_classes: ['pii', 'logs'],
    attested_by: 'user:fran',
    attested_at: '2026-05-20T10:00:00Z',
    violations_observed: 0,
    last_checked: '2026-06-03T03:00:00Z',
    note: '',
  },
  {
    id: 'res-us-east',
    region: 'us-east',
    perimeter: 'model-backed',
    self_hosted: false,
    encryption_at_rest: false,
    data_classes: ['pii'],
    attested_by: 'user:fran',
    attested_at: '2026-04-11T09:00:00Z',
    violations_observed: 2,
    last_checked: '2026-06-03T03:00:00Z',
    note: 'Data-lineage egress detected to a model-backed region — investigate before relying on this attestation.',
  },
]

// --- evidence export fixtures (FIN-10) ---------------------------------------
// reportDisclaimer mirrors the engine constant (report.go:26) so fixtures read like
// real responses.
const reportDisclaimer =
  'Technical control-status mapping derived from observed platform evidence. NOT a certification and NOT legal advice (docs/08 §9).'

// The OSCAL bundle's finding status is the 2-value enum {satisfied, not-satisfied};
// the precise product status rides in target.status.reason so a by_design control is
// NEVER laundered to "satisfied" (modules/compliance/oscal.go). The fixture proves it:
// a by_design finding is OSCAL `not-satisfied` with reason `by_design`.
export const oscalExportFixture: OscalExport = {
  oscal_version: '1.2.2',
  'assessment-results': {
    results: [
      {
        uuid: '00000000-0000-5000-8000-000000000001',
        title: 'EU AI Act control assessment',
        findings: [
          {
            uuid: '00000000-0000-5000-8000-000000000010',
            title: 'art_12 — satisfied',
            description: 'satisfied: 3/3 capabilities present',
            target: {
              type: 'objective-id',
              'target-id': 'art_12_obj',
              status: {
                state: 'satisfied',
                reason: 'satisfied',
                remarks: 'Operational evidence backs this control.',
              },
            },
            props: [
              {
                name: 'control_status',
                ns: 'https://olivares.ai/ns/oscal',
                value: 'satisfied',
              },
            ],
          },
          {
            uuid: '00000000-0000-5000-8000-000000000011',
            title: 'art_15 — by_design',
            description: 'by_design: 1/1 capabilities present',
            target: {
              type: 'objective-id',
              'target-id': 'art_15_obj',
              status: {
                // OSCAL collapses by_design to not-satisfied; reason preserves the truth.
                state: 'not-satisfied',
                reason: 'by_design',
                remarks:
                  'Architectural guarantee only — no operational telemetry.',
              },
            },
            props: [
              {
                name: 'control_status',
                ns: 'https://olivares.ai/ns/oscal',
                value: 'by_design',
              },
            ],
          },
          {
            uuid: '00000000-0000-5000-8000-000000000012',
            title: 'art_10 — gap',
            description: 'gap: 0/1 capabilities present',
            target: {
              type: 'objective-id',
              'target-id': 'art_10_obj',
              status: {
                state: 'not-satisfied',
                reason: 'gap',
              },
            },
            props: [
              {
                name: 'control_status',
                ns: 'https://olivares.ai/ns/oscal',
                value: 'gap',
              },
            ],
          },
        ],
      },
    ],
  },
  disclaimer:
    reportDisclaimer +
    ' OSCAL v1.2.2 export; satisfied is asserted ONLY for controls with live operational evidence (by_design/partial/gap map to OSCAL not-satisfied; the control-mapping uses intersects-with and never asserts conformance).',
}

export const oscalExportResultFixture: EvidenceExportResult = {
  format: 'oscal',
  filename: 'evidence-evp-2026q2-euaiact.oscal.json',
  content_type: 'application/json',
  text: JSON.stringify(oscalExportFixture),
  oscal: oscalExportFixture,
}

export const csvExportResultFixture: EvidenceExportResult = {
  format: 'csv',
  filename: 'evidence-evp-2026q2-euaiact.csv',
  content_type: 'text/csv; charset=utf-8',
  text:
    '# evidence package evp-2026q2-euaiact framework=eu_ai_act integrity_ok=true ledger_seq=12\n' +
    'control_id,status,title,evidence_summary\n' +
    'art_12,satisfied,Record-keeping,satisfied: 3/3 capabilities present\n' +
    'art_15,by_design,Accuracy robustness and cybersecurity,by_design: 1/1 capabilities present\n' +
    'art_10,gap,Data and data governance,gap: 0/1 capabilities present; missing: data_residency\n',
}
