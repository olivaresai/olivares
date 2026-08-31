// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

// This file is the VERSIONED, IN-REPO control catalog — the deterministic source
// of truth (decision #1: the frameworks change slowly and the trace must be
// reproducible). It is a TECHNICAL control mapping for an AI control plane, NOT legal
// advice and NOT a certification (docs/SECURITY-HARDENING.md). Each control's Capabilities list the
// platform capabilities that HONESTLY evidence it (capabilities.go); an empty list is
// an honest gap. Control identifiers, titles and requirements were authored and then
// adversarially fact-checked against the primary texts; a synthetic sub-clause and
// several over-claimed capability mappings were removed in review.
//
// To extend: add a Framework here (or a Control to one). The catalog is data, not
// schema — no migration, no engine release. An optional external import path can be
// layered on top later; this in-repo catalog stays the verified truth.
//
// (2026-H2 re-baseline): every framework carries a structured version PIN
// (document + primary source + verified_on); application DATES live exclusively in
// the regulatory calendar (calendar.go) and are referenced via Control.MilestoneIDs
// — both invariants are tested. Every date and list entry below was re-verified
// against its primary source on 2026-06-10 (12-agent research + adversarial
// verification pass; see sessions-rebaseline-2026.md).

// highRiskMilestones links every EU AI Act Chapter III Section 2 (high-risk)
// obligation to its re-baselined application dates in the regulatory calendar
//: the IN-FORCE Art 113 dates plus the adopted Digital Omnibus
// deferrals, which stay adopted_pending_oj until OJ publication. Dates live only
// in calendar.go.
var highRiskMilestones = []string{
	"eu_ai_act.high_risk_annex3_original", "eu_ai_act.high_risk_annex3_omnibus",
	"eu_ai_act.high_risk_annex1_original", "eu_ai_act.high_risk_annex1_omnibus",
}

// catalog is the ordered set of frameworks the module maps. The order is the display
// order; lookups go through frameworkByID.
var catalog = []Framework{
	{
		ID:         "eu_ai_act",
		Name:       "EU AI Act",
		Version:    "Regulation (EU) 2024/1689",
		Authority:  "European Parliament and Council of the European Union (enforced via national market surveillance authorities and the European AI Office)",
		Disclaimer: "Technical control mapping for an AI control plane only; not legal advice and not a certification or conformity assessment of compliance with Regulation (EU) 2024/1689.",
		Pin: FrameworkPin{
			Document:    "Regulation (EU) 2024/1689 (Artificial Intelligence Act)",
			PublishedOn: "2024-07-12",
			SourceURL:   "https://eur-lex.europa.eu/eli/reg/2024/1689/oj",
			VerifiedOn:  "2026-06-10",
			Status:      PinInForce,
		},
		Controls: []Control{
			{
				ID:           "art_5",
				Title:        "Prohibited AI practices",
				Requirement:  "Prohibits placing on the market, putting into service, or using AI systems that engage in unacceptable-risk practices (e.g. manipulative/exploitative techniques, social scoring, untargeted facial-recognition scraping, certain biometric categorisation and real-time remote biometric identification).",
				Criterion:    "Evidence that each agent/system is risk-classified and screened so that prohibited-practice (unacceptable-risk) categories are flagged and blocked, with an inventory mapping every system to its tier.",
				Capabilities: []CapabilityKey{"risk_classification", "transparency_record"},
				Note:         "The adopted Digital Omnibus amending act, pending OJ publication, adds a NEW prohibited practice (NCII/CSAM generation) with its own compliance date — see the linked calendar milestones.",
				MilestoneIDs: []string{"eu_ai_act.prohibitions_literacy_apply", "eu_ai_act.new_art5_ncii_csam_compliance"},
			},
			{
				ID:           "art_6",
				Title:        "Classification rules for high-risk AI systems",
				Requirement:  "Defines when an AI system is high-risk, including systems listed in Annex III, triggering the Chapter III Section 2 provider obligations.",
				Criterion:    "Each agent/system carries a recorded EU-AI-Act tier classification (incl. Annex III high-risk determination) maintained in an inventory.",
				Capabilities: []CapabilityKey{"risk_classification", "transparency_record"},
				Note:         "High-risk application timing is re-baselined by the adopted Digital Omnibus amending act, pending OJ publication: the in-force dates stand until publication — both sets live in the regulatory calendar, never as prose here.",
				MilestoneIDs: highRiskMilestones,
			},
			{
				ID:           "art_9",
				Title:        "Risk management system",
				Requirement:  "Requires a continuous, iterative risk management system across the high-risk AI system's lifecycle: identifying, estimating, evaluating and mitigating risks, including through testing.",
				Criterion:    "Documented risk classifications plus adversarial/robustness testing, quality evaluation and threat-detection findings that evidence an iterative identify-test-mitigate loop over the system lifecycle.",
				Capabilities: []CapabilityKey{"risk_classification", "adversarial_testing", "quality_evaluation", "threat_detection"},
				MilestoneIDs: highRiskMilestones,
			},
			{
				ID:           "art_10",
				Title:        "Data and data governance",
				Requirement:  "Requires training, validation and testing data to be subject to data governance and management practices appropriate to the purpose, including provenance, relevance, representativeness, bias examination and handling of personal data.",
				Criterion:    "Data-lineage evidence that client/training data stays within the defined perimeter, plus residency attestation, data-minimization by construction, and deterministic PII-discovery scans that surface personal data held in governed knowledge/document sources.",
				Capabilities: []CapabilityKey{"data_lineage", "data_residency", "data_minimization", "pii_discovery"},
				Note:         "The platform evidences perimeter/provenance, data minimization and personal-data discovery; dataset representativeness, bias examination/mitigation and statistical data quality are NOT evidenced by the control plane.",
				MilestoneIDs: highRiskMilestones,
			},
			{
				ID:           "art_12",
				Title:        "Record-keeping",
				Requirement:  "Requires high-risk AI systems to technically allow automatic recording of events (logs) over their lifetime, with traceability appropriate to the intended purpose and supporting post-market monitoring.",
				Criterion:    "An append-only, hash-chained audit ledger with verified integrity, immutable by construction, exportable to WORM/SIEM and reconstructable for forensic timelines, plus per-inference resource accounting recorded over the system's operation.",
				Capabilities: []CapabilityKey{"audit_trail", "audit_integrity", "audit_immutability", "audit_export", "forensic_capability", "access_observability", "resource_accounting"},
				MilestoneIDs: highRiskMilestones,
			},
			{
				ID:           "art_13",
				Title:        "Transparency and provision of information to deployers",
				Requirement:  "Requires high-risk AI systems to be sufficiently transparent for deployers to interpret and use outputs, accompanied by instructions for use detailing capabilities, limitations, performance and log-collection mechanisms.",
				Criterion:    "A maintained system/agent inventory and record-keeping surface that documents each system, its capabilities and its logging, supporting the information provided to deployers.",
				Capabilities: []CapabilityKey{"transparency_record", "audit_trail"},
				MilestoneIDs: highRiskMilestones,
			},
			{
				ID:           "art_14",
				Title:        "Human oversight",
				Requirement:  "Requires high-risk AI systems to be designed so they can be effectively overseen by natural persons during use, including the ability to intervene, override or stop the system.",
				Criterion:    "Human-in-the-loop / approval gates that are deny-by-default, with the oversight actions and access governed and recorded.",
				Capabilities: []CapabilityKey{"human_oversight", "identity_governance", "access_control_rbac"},
				MilestoneIDs: highRiskMilestones,
			},
			{
				ID:           "art_15",
				Title:        "Accuracy, robustness and cybersecurity",
				Requirement:  "Requires high-risk AI systems to achieve appropriate levels of accuracy, robustness and cybersecurity and to perform consistently in those respects throughout their lifecycle, including resilience to errors and adversarial manipulation.",
				Criterion:    "Adversarial/robustness testing, quality evaluation, threat-detection findings, encryption in transit and secure defaults that evidence resilience and security posture.",
				Capabilities: []CapabilityKey{"adversarial_testing", "quality_evaluation", "threat_detection", "encryption_transit", "secure_defaults", "supply_chain"},
				MilestoneIDs: highRiskMilestones,
			},
			{
				ID:           "art_50",
				Title:        "Transparency obligations for providers and deployers of certain AI systems",
				Requirement:  "Requires limited-risk transparency: informing natural persons they are interacting with an AI system and marking/disclosing AI-generated or manipulated content (e.g. deepfakes, synthetic media).",
				Criterion:    "Evidence that systems subject to Art. 50 disclosure are identified via risk classification and recorded in inventory.",
				Capabilities: []CapabilityKey{"risk_classification", "transparency_record"},
				Note:         "The platform evidences inventory/classification of in-scope systems; the substantive Art. 50 duties (end-user AI-interaction notices and synthetic-content watermarking) are not implemented and remain a gap. Application timing under the adopted Digital Omnibus amending act, pending OJ publication, lives in the linked calendar milestones.",
				MilestoneIDs: []string{"eu_ai_act.art50_transparency_applies", "eu_ai_act.art50_marking_grace_ends"},
			},
			{
				ID:           "art_72",
				Title:        "Post-market monitoring by providers and post-market monitoring plan for high-risk AI systems",
				Requirement:  "Requires providers to establish and document a post-market monitoring system that actively and systematically collects, documents and analyses performance data over the system's lifetime to evaluate continued compliance.",
				Criterion:    "Continuous post-deployment telemetry: audit trail, change/deployment records, threat-detection and quality-evaluation findings, exportable for ongoing analysis.",
				Capabilities: []CapabilityKey{"audit_trail", "change_management", "threat_detection", "quality_evaluation", "audit_export", "access_observability"},
				MilestoneIDs: highRiskMilestones,
			},
			{
				ID:           "art_11",
				Title:        "Technical documentation",
				Requirement:  "Requires technical documentation (per Annex IV) to be drawn up before a high-risk AI system is placed on the market and kept up to date, demonstrating compliance with Chapter III Section 2.",
				Criterion:    "Change-management/deployment records and a maintained system inventory that keep evidence of the system's state current and reconstructable, plus per-inference accounting of the computational resources used to operate the system (Annex IV(2)(c)).",
				Capabilities: []CapabilityKey{"change_management", "transparency_record", "resource_accounting"},
				Note:         "Annex IV(2)(c) requires documenting the computational resources USED to develop, train, test and validate the system; the control plane evidences the operational compute/cost accounting (resource_accounting), NOT training-time figures or dataset quality.",
				MilestoneIDs: highRiskMilestones,
			},
		},
	},
	{
		ID:      "nist_ai_rmf",
		Name:    "NIST AI Risk Management Framework (AI RMF 1.0)",
		Version: "NIST AI 100-1 (January 2023)",
		Pin: FrameworkPin{
			Document:    "NIST AI 100-1 (AI RMF 1.0)",
			PublishedOn: "2023-01-26",
			SourceURL:   "https://nvlpubs.nist.gov/nistpubs/ai/NIST.AI.100-1.pdf",
			VerifiedOn:  "2026-06-10",
			Status:      PinFinal,
		},
		Authority:  "National Institute of Standards and Technology (NIST), U.S. Department of Commerce",
		Disclaimer: "Technical mapping of control-plane capabilities to NIST AI RMF 1.0 subcategories — not legal advice, and the module makes no claim of certification or NIST conformity assessment (the AI RMF is a voluntary framework with no certification scheme).",
		Controls: []Control{
			{
				ID:           "GOVERN-1.1",
				Title:        "Legal and regulatory requirements are understood, managed, and documented",
				Requirement:  "Legal and regulatory requirements involving AI are understood, managed, and documented.",
				Criterion:    "Per-agent AI risk classifications (EU AI Act / NIST risk tiers) recorded in the compliance module, with an append-only, exportable audit trail of who classified what and when.",
				Capabilities: []CapabilityKey{"risk_classification", "audit_trail", "audit_export", "transparency_record"},
			},
			{
				ID:           "GOVERN-1.4",
				Title:        "Risk management process and outcomes established through transparent controls",
				Requirement:  "The risk management process and its outcomes are established through transparent policies, procedures, and other controls based on organizational risk priorities.",
				Criterion:    "RBAC/tenant-isolation enforcement plus an append-only, hash-chained audit ledger provide transparent, attributable evidence of the risk-management controls and their outcomes.",
				Capabilities: []CapabilityKey{"access_control_rbac", "audit_immutability", "audit_trail", "risk_classification"},
			},
			{
				ID:           "GOVERN-1.5",
				Title:        "Ongoing monitoring and periodic review of the risk process are planned with roles defined",
				Requirement:  "Ongoing monitoring and periodic review of the risk management process and its outcomes are planned and organizational roles and responsibilities clearly defined, including determining the frequency of periodic review.",
				Criterion:    "Continuous access-observability edges and least-privilege drift computation provide a live monitoring signal whose outcomes are recorded and exportable for periodic review.",
				Capabilities: []CapabilityKey{"access_observability", "least_privilege_drift", "audit_trail", "audit_export"},
			},
			{
				ID:           "GOVERN-1.6",
				Title:        "Mechanisms are in place to inventory AI systems",
				Requirement:  "Mechanisms are in place to inventory AI systems and are resourced according to organizational risk priorities.",
				Criterion:    "A system/agent inventory (modules I/II) with per-agent risk classifications maintained as durable, queryable records.",
				Capabilities: []CapabilityKey{"transparency_record", "risk_classification"},
			},
			{
				ID:           "GOVERN-4.1",
				Title:        "Policies foster a critical-thinking and safety-first mindset across the AI lifecycle",
				Requirement:  "Organizational policies and practices are in place to foster a critical thinking and safety-first mindset in the design, development, deployment, and uses of AI systems to minimize potential negative impacts.",
				Criterion:    "Deny-by-default access control, secure defaults, and HITL approval gates operationalize a safety-first posture by construction.",
				Capabilities: []CapabilityKey{"human_oversight", "secure_defaults", "access_control_rbac"},
				Note:         "Backed only by design-evidence (architectural) capabilities; a critical-thinking/safety-first organizational culture is an outcome the platform supports but cannot prove — never read as fully satisfied.",
			},
			{
				ID:           "GOVERN-5.1",
				Title:        "Collect and integrate feedback from those external to the team on individual and societal impacts",
				Requirement:  "Organizational policies and practices are in place to collect, consider, prioritize, and integrate feedback from those external to the team that developed or deployed the AI system regarding the potential individual and societal impacts related to AI risks.",
				Criterion:    "Evidence of structured external-stakeholder/affected-community feedback channels feeding the risk process.",
				Capabilities: nil, // honest gap: the control plane cannot yet evidence this control
			},
			{
				ID:           "GOVERN-6.1",
				Title:        "Policies address AI risks of third-party entities, including IP and other rights",
				Requirement:  "Policies and procedures are in place that address AI risks associated with third-party entities, including risks of infringement of a third-party's intellectual property or other rights.",
				Criterion:    "Signed releases, SBOM, and pinned dependencies provide attestable third-party/supply-chain risk evidence; change-ledger records track third-party component changes; and an operator-verified per-provider GPAI compliance posture evidences third-party AI-model risk (FIN-13).",
				Capabilities: []CapabilityKey{"supply_chain", "change_management", "supplier_gpai_posture"},
			},
			{
				ID:           "MAP-4.1",
				Title:        "Map technology and legal risks of components, including third-party data/software and IP rights",
				Requirement:  "Approaches for mapping AI technology and legal risks of its components – including the use of third-party data or software – are in place, followed, and documented, as are risks of infringement of a third party's intellectual property or other rights.",
				Criterion:    "Data-lineage evidence keeping client data within the perimeter plus SBOM/pinned-dependency records document component-level technology risk; an operator-verified per-provider GPAI compliance posture maps the legal/IP risk of brokered third-party models (FIN-13).",
				Capabilities: []CapabilityKey{"data_lineage", "supply_chain", "transparency_record", "supplier_gpai_posture"},
			},
			{
				ID:           "MEASURE-2.7",
				Title:        "AI system security and resilience are evaluated and documented",
				Requirement:  "AI system security and resilience – as identified in the MAP function – are evaluated and documented.",
				Criterion:    "Live security-guardrail/anomaly findings and adversarial red-team results, backed by an integrity-verified, reconstructable incident timeline.",
				Capabilities: []CapabilityKey{"forensic_capability", "audit_integrity", "threat_detection", "adversarial_testing"},
			},
			{
				ID:           "MEASURE-2.8",
				Title:        "Transparency and accountability risks are examined and documented",
				Requirement:  "Risks associated with transparency and accountability – as identified in the MAP function – are examined and documented.",
				Criterion:    "Append-only hash-chained ledger with live integrity verification, continuous WORM/SIEM export, and recorded agent->resource access edges provide tamper-evident accountability records.",
				Capabilities: []CapabilityKey{"audit_immutability", "audit_export", "transparency_record", "audit_trail", "audit_integrity", "access_observability"},
			},
			{
				ID:           "MEASURE-3.1",
				Title:        "Identify and track existing, unanticipated, and emergent AI risks from deployed performance",
				Requirement:  "Approaches, personnel, and documentation are in place to regularly identify and track existing, unanticipated, and emergent AI risks based on factors such as intended and actual performance in deployed contexts.",
				Criterion:    "Continuous threat/anomaly detection, least-privilege drift (permitted vs observed), and agent eval results provide the live tracking signal for emergent risks.",
				Capabilities: []CapabilityKey{"threat_detection", "least_privilege_drift", "quality_evaluation", "access_observability"},
			},
			{
				ID:           "MANAGE-2.4",
				Title:        "Mechanisms to supersede, disengage, or deactivate AI systems, with responsibilities assigned",
				Requirement:  "Mechanisms are in place and applied, and responsibilities are assigned and understood, to supersede, disengage, or deactivate AI systems that demonstrate performance or outcomes inconsistent with intended use.",
				Criterion:    "HITL/approval gates with deny-by-default and RBAC-enforced control over non-human identities provide the kill-switch/deactivate capability; a change ledger records disable/deactivate actions.",
				Capabilities: []CapabilityKey{"human_oversight", "access_control_rbac", "change_management"},
			},
			{
				ID:           "MANAGE-4.1",
				Title:        "Post-deployment monitoring implemented, including input capture, appeal/override, incident response, change management",
				Requirement:  "Post-deployment AI system monitoring plans are implemented, including mechanisms for capturing and evaluating input from users and other relevant AI actors, appeal and override, decommissioning, incident response, recovery, and change management.",
				Criterion:    "Deployment/change-ledger records, continuous threat/anomaly monitoring, agent eval results, and an append-only audit trail collectively evidence post-deployment monitoring and change management.",
				Capabilities: []CapabilityKey{"change_management", "audit_trail", "threat_detection", "quality_evaluation", "human_oversight"},
			},
			{
				ID:           "MANAGE-4.3",
				Title:        "Incidents communicated to relevant AI actors; tracking, response, and recovery documented",
				Requirement:  "Incidents and errors are communicated to relevant AI actors, including affected communities. Processes for tracking, responding to, and recovering from incidents and errors are followed and documented.",
				Criterion:    "An integrity-verified, reconstructable incident timeline backed by the tamper-evident audit ledger and WORM/SIEM export, with security findings recorded.",
				Capabilities: []CapabilityKey{"forensic_capability", "audit_export", "audit_trail", "audit_integrity", "threat_detection"},
			},
		},
	},
	{
		ID:      "iso_42001",
		Name:    "ISO/IEC 42001 — Information technology — Artificial intelligence — Management system",
		Version: "ISO/IEC 42001:2023 (Annex A)",
		Pin: FrameworkPin{
			Document:    "ISO/IEC 42001:2023",
			PublishedOn: "2023-12",
			SourceURL:   "https://www.iso.org/standard/81230.html",
			VerifiedOn:  "2026-06-10",
			Status:      PinFinal,
		},
		Authority:  "ISO/IEC JTC 1/SC 42 (International Organization for Standardization / International Electrotechnical Commission)",
		Disclaimer: "Technical control-to-capability mapping for an AI control plane; not legal advice and not a certification or statement of conformity to ISO/IEC 42001. Annex A is a reference control set selected via a Statement of Applicability against the organization's AI risk assessment — these mappings evidence platform support for specific controls, not full conformity.",
		Controls: []Control{
			{
				ID:           "A.6.2.8",
				Title:        "AI system recording of event logs",
				Requirement:  "The organization shall determine at which phases of the AI system life cycle event logging should be recorded, and shall keep records of those events.",
				Criterion:    "Per-tenant append-only audit events covering AI agent/system activity exist, the hash-chain verifies live, immutability is guaranteed by construction, and continuous WORM/SIEM export is available.",
				Capabilities: []CapabilityKey{"audit_trail", "audit_integrity", "audit_immutability", "audit_export", "access_observability"},
			},
			{
				ID:           "A.6.2.6",
				Title:        "AI system operation and monitoring",
				Requirement:  "The organization shall define and document the necessary elements for the ongoing operation and monitoring of the AI system.",
				Criterion:    "Live operational monitoring exists for the tenant: security guardrail/anomaly findings, least-privilege drift between permitted and observed access, quality eval results, and a residency attestation with no observed perimeter-egress violation.",
				Capabilities: []CapabilityKey{"threat_detection", "least_privilege_drift", "access_observability", "quality_evaluation", "data_residency"},
			},
			{
				ID:           "A.6.2.5",
				Title:        "AI system deployment",
				Requirement:  "The organization shall document and implement a deployment plan and ensure requirements are met before the AI system is deployed.",
				Criterion:    "Deployment / change-ledger records exist for the tenant's agents, and the platform enforces secure-by-default deployment posture (no default creds, TLS on, localhost bind, setup token).",
				Capabilities: []CapabilityKey{"change_management", "secure_defaults"},
			},
			{
				ID:           "A.6.2.4",
				Title:        "AI system verification and validation",
				Requirement:  "The organization shall define and document verification and validation measures for the AI system and specify criteria for their use.",
				Criterion:    "Agent evaluation results and adversarial/red-team robustness findings exist for the tenant as auditable verification-and-validation evidence.",
				Capabilities: []CapabilityKey{"quality_evaluation", "adversarial_testing"},
			},
			{
				ID:           "A.5.2",
				Title:        "AI system impact assessment process",
				Requirement:  "The organization shall establish a process to assess the potential consequences (impacts) of the AI system to individuals, groups of individuals, and societies throughout its life cycle.",
				Criterion:    "Per-agent EU-AI-Act / NIST risk classifications produced by the compliance module exist and feed a documented impact/risk view for the tenant's AI systems.",
				Capabilities: []CapabilityKey{"risk_classification"},
			},
			{
				ID:           "A.7.5",
				Title:        "Data provenance",
				Requirement:  "The organization shall define and document a process for recording the provenance of data used in its AI systems over the life cycle of the data and the AI system.",
				Criterion:    "A sealed, ledger-anchored CycloneDX AIBOM records the provenance and lineage of an admitted model's datasets (and the model artifact) over the life cycle.",
				Capabilities: []CapabilityKey{"model_aibom"},
				Note:         "Evidenced by a sealed AIBOM recording model/dataset lineage and provenance; it does NOT evidence dataset statistical quality, representativeness or bias. Absent until at least one AIBOM is sealed (honest gap).",
			},
			{
				ID:           "A.4.5",
				Title:        "System and computing resources",
				Requirement:  "The organization shall determine and provide the system and computing resources necessary for the AI system, including how they are documented and managed.",
				Criterion:    "Token/compute/cost is accounted per inference (FinOps resource accounting), RBAC and multi-tenant isolation are enforced by the engine over compute/resource access, and non-human identities and their policies are governed.",
				Capabilities: []CapabilityKey{"resource_accounting", "access_control_rbac", "identity_governance"},
				Note:         "Verified against ISO/IEC 42001:2023 Annex A.4 (Resources for AI systems) via public secondary sources — the standard's normative text is paywalled, so the exact control title is not certified bit-exact.",
			},
			{
				ID:           "A.9.2",
				Title:        "Processes for responsible use of AI systems",
				Requirement:  "The organization shall establish and document processes for the responsible use of AI systems.",
				Criterion:    "Human-in-the-loop / approval gates with deny-by-default are available, agent->resource access edges are observed, and permitted-vs-observed least-privilege drift is computable for the tenant's AI usage.",
				Capabilities: []CapabilityKey{"human_oversight", "access_observability", "least_privilege_drift"},
			},
			{
				ID:           "A.10.3",
				Title:        "Suppliers",
				Requirement:  "The organization shall establish and document a process to ensure that its use of services, products or materials provided by suppliers aligns with its responsible AI approach.",
				Criterion:    "The tenant records an operator-verified GPAI compliance posture for each brokered model provider (technical documentation / training-data summary / copyright policy / downstream info / Code of Practice), evidencing that upstream AI suppliers align with its responsible-AI approach (FIN-13).",
				Capabilities: []CapabilityKey{"supplier_gpai_posture"},
			},
			{
				ID:           "A.8.4",
				Title:        "Communication of incidents",
				Requirement:  "The organization shall determine and document a process to inform interested parties of, and respond to, incidents involving the AI system.",
				Criterion:    "Security guardrail/anomaly findings are detected and a reconstructable, integrity-verified incident timeline can be produced from the tenant's audit ledger.",
				Capabilities: []CapabilityKey{"threat_detection", "forensic_capability", "audit_trail"},
			},
			{
				ID:           "A.8.2",
				Title:        "System documentation and information for users",
				Requirement:  "The organization shall determine and provide the necessary information about the AI system to users so they can understand and operate it appropriately.",
				Criterion:    "A system/agent inventory and record-keeping (transparency record) is available, listing the AI systems in scope and their operational records.",
				Capabilities: []CapabilityKey{"transparency_record"},
			},
			{
				ID:           "A.3.2",
				Title:        "AI roles and responsibilities",
				Requirement:  "The organization shall define and allocate roles and responsibilities for AI throughout the AI system life cycle.",
				Criterion:    "Organizational AI roles, responsibilities and accountability are defined and allocated to human actors across the AI system life cycle.",
				Capabilities: nil, // honest gap: the control plane cannot yet evidence this control
			},
			{
				ID:           "A.5.4",
				Title:        "Assessing AI system impact on individuals or groups of individuals",
				Requirement:  "The organization shall assess and document the potential impacts of the AI system on individuals or groups of individuals throughout its life cycle.",
				Criterion:    "A documented assessment of harms to individuals/groups (e.g. fairness, privacy, rights impacts) exists for the AI system.",
				Capabilities: nil, // honest gap: the control plane cannot yet evidence this control
			},
		},
	},
	{
		ID:      "soc2_tsc",
		Name:    "SOC 2 - Trust Services Criteria (Security / Common Criteria)",
		Version: "2017 Trust Services Criteria with Revised Points of Focus - 2022",
		Pin: FrameworkPin{
			Document:    "TSP Section 100: 2017 Trust Services Criteria (With Revised Points of Focus — 2022)",
			PublishedOn: "2022",
			SourceURL:   "https://www.aicpa-cima.com/resources/download/2017-trust-services-criteria-with-revised-points-of-focus-2022",
			VerifiedOn:  "2026-06-10",
			Status:      PinFinal,
		},
		Authority:  "AICPA (American Institute of Certified Public Accountants) - Assurance Services Executive Committee",
		Disclaimer: "Technical control mapping only - not legal advice, not an audit opinion, and not a SOC 2 attestation or certification. Evidence here supports a CPA examination but does not replace one.",
		Controls: []Control{
			{
				ID:           "CC4.1",
				Title:        "Monitoring Activities - Ongoing and Separate Evaluations (COSO Principle 16)",
				Requirement:  "The entity selects, develops, and performs ongoing and/or separate evaluations to ascertain whether the components of internal control are present and functioning.",
				Criterion:    "Continuous telemetry exists that evaluates whether controls are operating: live observability of agent->resource access, recurring guardrail/anomaly findings, quality-evaluation runs, and red-team results, all anchored to a verifiable audit ledger.",
				Capabilities: []CapabilityKey{"access_observability", "threat_detection", "quality_evaluation", "adversarial_testing", "audit_trail", "audit_integrity"},
			},
			{
				ID:           "CC5.2",
				Title:        "Control Activities - General Control Activities Over Technology (COSO Principle 11)",
				Requirement:  "The entity also selects and develops general control activities over technology to support the achievement of objectives.",
				Criterion:    "Technology general controls are designed into the engine: RBAC over the technology infrastructure, a governed change/deployment process for that technology, and secure-by-default configuration.",
				Capabilities: []CapabilityKey{"access_control_rbac", "change_management", "secure_defaults"},
			},
			{
				ID:           "CC6.1",
				Title:        "Logical and Physical Access Controls - Logical Access Security",
				Requirement:  "The entity implements logical access security software, infrastructure, and architectures over protected information assets to protect them from security events to meet the entity's objectives.",
				Criterion:    "Logical access is enforced by the engine via RBAC and multi-tenant isolation, recorded as observable access edges, with transit encryption protecting assets and secure-by-default deployment posture.",
				Capabilities: []CapabilityKey{"access_control_rbac", "access_observability", "encryption_transit", "secure_defaults"},
			},
			{
				ID:           "CC6.2",
				Title:        "Logical and Physical Access Controls - User Registration, Authorization and Deprovisioning",
				Requirement:  "Prior to issuing system credentials and granting system access, the entity registers and authorizes new internal and external users; credentials are removed when access is no longer authorized.",
				Criterion:    "Identities (including non-human/agent identities) are registered and governed under policy, with RBAC controlling grant/removal of access and human approval gates for authorization.",
				Capabilities: []CapabilityKey{"identity_governance", "access_control_rbac", "human_oversight"},
			},
			{
				ID:           "CC6.3",
				Title:        "Logical and Physical Access Controls - Least Privilege and Segregation of Duties",
				Requirement:  "The entity authorizes, modifies, or removes access to data, software, functions, and other protected information assets based on roles, responsibilities, or system design and changes, giving consideration to least privilege and segregation of duties.",
				Criterion:    "Permitted-vs-observed access drift is computable so least-privilege violations surface, access edges are recorded, RBAC defines role-based authorization, and human-oversight gates approve privilege changes.",
				Capabilities: []CapabilityKey{"least_privilege_drift", "access_observability", "access_control_rbac", "human_oversight"},
			},
			{
				ID:           "CC6.6",
				Title:        "Logical and Physical Access Controls - Protection Against External Threats",
				Requirement:  "The entity implements logical access security measures to protect against threats from sources outside its system boundaries.",
				Criterion:    "External-threat protection is evidenced by guardrail/anomaly detection findings, mTLS/TLS on all boundary communication, and secure-by-default network posture (TLS on, localhost bind, no default creds).",
				Capabilities: []CapabilityKey{"threat_detection", "encryption_transit", "secure_defaults"},
			},
			{
				ID:           "CC6.7",
				Title:        "Logical and Physical Access Controls - Restriction and Protection of Information in Transmission, Movement and Removal",
				Requirement:  "The entity restricts the transmission, movement, and removal of information to authorized internal and external users and processes, and protects it during transmission, movement, or removal to meet the entity's objectives.",
				Criterion:    "Information movement is restricted and protected: only relations/metadata are persisted (never payloads/PII), transit is TLS/mTLS protected, lineage/residency evidence shows client data stays within the perimeter, and a deny-closed DLP gate restricts movement of sensitivity-classified content with append-only enforcement evidence.",
				Capabilities: []CapabilityKey{"data_minimization", "encryption_transit", "data_lineage", "data_residency", "dlp_enforcement"},
			},
			{
				ID:           "CC6.8",
				Title:        "Logical and Physical Access Controls - Prevention/Detection of Unauthorized or Malicious Software",
				Requirement:  "The entity implements controls to prevent or detect and act upon the introduction of unauthorized or malicious software to meet the entity's objectives.",
				Criterion:    "Software integrity is controlled via signed releases, SBOM and pinned dependencies (supply chain), and a governed change process for what software is introduced.",
				Capabilities: []CapabilityKey{"supply_chain", "change_management"},
			},
			{
				ID:           "CC7.1",
				Title:        "System Operations - Detection of Configuration Changes and New Vulnerabilities",
				Requirement:  "To meet its objectives, the entity uses detection and monitoring procedures to identify (1) changes to configurations that result in the introduction of new vulnerabilities, and (2) susceptibilities to newly discovered vulnerabilities.",
				Criterion:    "Configuration changes are captured in the change/deployment ledger and correlated with anomaly/guardrail detection that surfaces newly introduced or anomalous configuration states.",
				Capabilities: []CapabilityKey{"change_management", "threat_detection"},
			},
			{
				ID:           "CC7.2",
				Title:        "System Operations - Monitoring for Anomalies Indicative of Security Events",
				Requirement:  "The entity monitors system components and the operation of those components for anomalies that are indicative of malicious acts, natural disasters, and errors; anomalies are analyzed to determine whether they represent security events.",
				Criterion:    "Components and agent behavior are continuously monitored: guardrail/anomaly findings are generated, access activity is observed, and all of it lands in a tamper-evident audit ledger that verifies on a live check.",
				Capabilities: []CapabilityKey{"threat_detection", "access_observability", "audit_trail", "audit_integrity"},
			},
			{
				ID:           "CC7.3",
				Title:        "System Operations - Evaluation of Security Events",
				Requirement:  "The entity evaluates security events to determine whether they could or have resulted in a failure of the entity to meet its objectives (security incidents) and, if so, takes actions to prevent or address such failures.",
				Criterion:    "Detected events can be evaluated against a reconstructable, integrity-verified incident timeline and a verifying audit hash-chain, so analysts can determine whether an event is an incident.",
				Capabilities: []CapabilityKey{"forensic_capability", "threat_detection", "audit_integrity", "audit_trail"},
			},
			{
				ID:           "CC7.4",
				Title:        "System Operations - Incident Response Program Execution",
				Requirement:  "The entity responds to identified security incidents by executing a defined incident-response program to understand, contain, remediate, and communicate security incidents, as appropriate.",
				Criterion:    "Incident response is supported by a reconstructable integrity-verified timeline for understanding the incident, an immutable hash-chained ledger of actions, and continuous WORM/SIEM export (CEF/syslog/OTLP) for communication and external response tooling.",
				Capabilities: []CapabilityKey{"forensic_capability", "audit_immutability", "audit_export", "audit_trail"},
			},
			{
				ID:           "CC8.1",
				Title:        "Change Management - Authorized, Tested and Documented Changes",
				Requirement:  "The entity authorizes, designs, develops or acquires, configures, documents, tests, approves, and implements changes to infrastructure, data, software, and procedures to meet its objectives.",
				Criterion:    "Changes are recorded in a deployment/change ledger, gated by human approval (HITL, deny-by-default), and built on a controlled software supply chain (signed releases, SBOM, pinned deps).",
				Capabilities: []CapabilityKey{"change_management", "human_oversight", "supply_chain"},
			},
		},
	},
	{
		ID:      "iso_27001_2022",
		Name:    "ISO/IEC 27001:2022 — Annex A controls",
		Version: "ISO/IEC 27001:2022 (Annex A, aligned to ISO/IEC 27002:2022 control set)",
		Pin: FrameworkPin{
			Document:    "ISO/IEC 27001:2022",
			PublishedOn: "2022-10",
			SourceURL:   "https://www.iso.org/standard/27001",
			VerifiedOn:  "2026-06-10",
			Status:      PinFinal,
		},
		Authority:  "International Organization for Standardization (ISO) / International Electrotechnical Commission (IEC)",
		Disclaimer: "Technical control mapping only — not legal advice, not a certification, and not a substitute for an accredited ISO/IEC 27001 audit; capability coverage is evidence input, not conformance.",
		Controls: []Control{
			{
				ID:           "A.5.12",
				Title:        "Classification of information",
				Requirement:  "Information shall be classified according to the information security needs of the organization based on confidentiality, integrity, availability and relevant interested party requirements.",
				Criterion:    "Deterministic PII/sensitivity discovery scans classify governed knowledge and document sources into explainable sensitivity classes (named rule + occurrence count, never a matched value), with a recommended classification recorded per document.",
				Capabilities: []CapabilityKey{"pii_discovery"},
			},
			{
				ID:           "A.5.13",
				Title:        "Labelling of information",
				Requirement:  "An appropriate set of procedures for information labelling shall be developed and implemented in accordance with the information classification scheme adopted by the organization.",
				Criterion:    "Every scanned document carries a persistent sensitivity label (classes, max severity, detector version, content hash) — including an explicit clean label for scanned content with no hits — kept current per scan/ingest.",
				Capabilities: []CapabilityKey{"pii_discovery"},
			},
			{
				ID:           "A.5.15",
				Title:        "Access control",
				Requirement:  "Rules to control physical and logical access to information and other associated assets shall be established and implemented based on business and information security requirements.",
				Criterion:    "An RBAC/multi-tenant authorization model is enforced by the engine and agent->resource access edges (R/RW) are observably recorded.",
				Capabilities: []CapabilityKey{"access_control_rbac", "access_observability"},
			},
			{
				ID:           "A.5.16",
				Title:        "Identity management",
				Requirement:  "The full life cycle of identities shall be managed.",
				Criterion:    "Non-human (agent/service) identities and their policies are governed across their lifecycle.",
				Capabilities: []CapabilityKey{"identity_governance"},
			},
			{
				ID:           "A.5.18",
				Title:        "Access rights",
				Requirement:  "Access rights to information and other associated assets shall be provisioned, reviewed, modified and removed in accordance with the organization's topic-specific policy on and rules for access control.",
				Criterion:    "Permitted-versus-observed access can be compared (least-privilege drift) so over-provisioned rights are surfaced for review and removal.",
				Capabilities: []CapabilityKey{"least_privilege_drift", "access_observability", "identity_governance"},
			},
			{
				ID:           "A.5.23",
				Title:        "Information security for use of cloud services",
				Requirement:  "Processes for acquisition, use, management and exit from cloud services shall be established in accordance with the organization's information security requirements.",
				Criterion:    "Data lineage shows client data stays within the defined perimeter and a residency attestation exists with no observed egress violation.",
				Capabilities: []CapabilityKey{"data_lineage", "data_residency", "data_minimization"},
			},
			{
				ID:           "A.5.24",
				Title:        "Information security incident management planning and preparation",
				Requirement:  "The organization shall plan and prepare for managing information security incidents by defining, establishing and communicating information security incident management processes, roles and responsibilities.",
				Criterion:    "A forensic, integrity-verified incident-reconstruction capability and human escalation/approval gates are available before an incident occurs.",
				Capabilities: []CapabilityKey{"forensic_capability", "human_oversight"},
			},
			{
				ID:           "A.5.26",
				Title:        "Response to information security incidents",
				Requirement:  "Information security incidents shall be responded to in accordance with the documented procedures.",
				Criterion:    "A live threat-detection signal plus a forensic, integrity-verified timeline support investigating and responding to a detected incident.",
				Capabilities: []CapabilityKey{"threat_detection", "forensic_capability"},
			},
			{
				ID:           "A.5.28",
				Title:        "Collection of evidence",
				Requirement:  "The organization shall establish and implement procedures for the identification, collection, acquisition and preservation of evidence related to information security events.",
				Criterion:    "Tenant events are captured in an append-only hash-chained ledger whose integrity verifies, with continuous WORM/SIEM export preserving evidence externally.",
				Capabilities: []CapabilityKey{"audit_trail", "audit_integrity", "audit_immutability", "audit_export", "forensic_capability"},
			},
			{
				ID:           "A.5.31",
				Title:        "Legal, statutory, regulatory and contractual requirements",
				Requirement:  "Legal, statutory, regulatory and contractual requirements relevant to information security and the organization's approach to meet these requirements shall be identified, documented and kept up to date.",
				Criterion:    "Per-agent regulatory risk classifications (EU AI Act / NIST) identify applicable AI-specific regulatory obligations.",
				Capabilities: []CapabilityKey{"risk_classification"},
			},
			{
				ID:           "A.8.12",
				Title:        "Data leakage prevention",
				Requirement:  "Data leakage prevention measures shall be applied to systems, networks and any other devices that process, store or transmit sensitive information.",
				Criterion:    "A deny-closed DLP egress gate keyed on sensitivity classes withholds classified/unscanned content from retrieval and refuses embed-egress ingest before content leaves the perimeter, recording append-only enforcement evidence (knowledge.dlp_event).",
				Capabilities: []CapabilityKey{"dlp_enforcement"},
			},
			{
				ID:           "A.8.15",
				Title:        "Logging",
				Requirement:  "Logs that record activities, exceptions, faults and other relevant events shall be produced, stored, protected and analyzed.",
				Criterion:    "An append-only, hash-chained audit ledger produces tenant events whose integrity verifies live, and which can be exported to external WORM/SIEM.",
				Capabilities: []CapabilityKey{"audit_trail", "audit_integrity", "audit_immutability", "audit_export"},
			},
			{
				ID:           "A.8.16",
				Title:        "Monitoring activities",
				Requirement:  "Networks, systems and applications shall be monitored for anomalous behavior and appropriate actions taken to evaluate potential information security incidents.",
				Criterion:    "Agent behavior and access are continuously observed and anomalies/security findings are detected for evaluation.",
				Capabilities: []CapabilityKey{"access_observability", "threat_detection"},
			},
			{
				ID:           "A.8.24",
				Title:        "Use of cryptography",
				Requirement:  "Rules for the effective use of cryptography, including cryptographic key management, shall be defined and implemented.",
				Criterion:    "Data in transit is protected by mTLS/TLS by default, and at-rest encryption is attested ON for the tenant.",
				Capabilities: []CapabilityKey{"encryption_transit", "encryption_at_rest"},
				Note:         "Encryption in transit is provided by design; at-rest encryption is opt-in (a gap until attested) and cryptographic KEY MANAGEMENT is not evidenced by the control plane.",
			},
			{
				ID:           "A.8.25",
				Title:        "Secure development life cycle",
				Requirement:  "Rules for the secure development of software and systems shall be established and applied.",
				Criterion:    "Agent quality is evaluated, robustness is adversarially tested, and releases are produced through a signed/SBOM-backed pinned supply chain.",
				Capabilities: []CapabilityKey{"quality_evaluation", "adversarial_testing", "supply_chain"},
			},
			{
				ID:           "A.8.32",
				Title:        "Change management",
				Requirement:  "Changes to information processing facilities and information systems shall be subject to change management procedures.",
				Criterion:    "Deployments and changes are recorded in a change ledger, with human approval/oversight gates available before changes take effect.",
				Capabilities: []CapabilityKey{"change_management", "human_oversight"},
			},
		},
	},
	{
		ID:      "gdpr",
		Name:    "General Data Protection Regulation (GDPR)",
		Version: "Regulation (EU) 2016/679",
		Pin: FrameworkPin{
			Document:    "Regulation (EU) 2016/679 (GDPR)",
			PublishedOn: "2016-05-04",
			SourceURL:   "https://eur-lex.europa.eu/eli/reg/2016/679/oj",
			VerifiedOn:  "2026-06-10",
			Status:      PinInForce,
		},
		Authority:  "European Parliament and Council of the European Union (enforced by national Supervisory Authorities / EDPB)",
		Disclaimer: "Technical control mapping for an AI control plane, not legal advice; supports but does not constitute GDPR compliance or any certification.",
		Controls: []Control{
			{
				ID:           "art_5_1_c",
				Title:        "Data minimization (Art. 5(1)(c))",
				Requirement:  "Personal data must be adequate, relevant and limited to what is necessary in relation to the purposes for which they are processed.",
				Criterion:    "The plane persists only access relations and metadata (never payloads/PII), so the data it processes about agents and resources is structurally limited to what governance requires; deterministic PII-discovery scans make personal data held in governed knowledge visible so over-collection can be surfaced and reduced.",
				Capabilities: []CapabilityKey{"data_minimization", "pii_discovery"},
			},
			{
				ID:           "art_5_1_f",
				Title:        "Integrity and confidentiality (Art. 5(1)(f))",
				Requirement:  "Personal data must be processed in a manner that ensures appropriate security, including protection against unauthorized or unlawful processing and accidental loss, destruction or damage, using appropriate technical or organizational measures.",
				Criterion:    "RBAC + multi-tenant isolation, encryption in transit, recorded access edges and tamper-evident audit show confidentiality/integrity controls are enforced and observable; a deny-closed DLP egress gate keyed on sensitivity classes protects classified personal data against unauthorized egress.",
				Capabilities: []CapabilityKey{"access_control_rbac", "encryption_transit", "access_observability", "audit_integrity", "threat_detection", "dlp_enforcement"},
			},
			{
				ID:           "art_5_2",
				Title:        "Accountability (Art. 5(2))",
				Requirement:  "The controller must be responsible for, and be able to demonstrate, compliance with the Art. 5(1) processing principles.",
				Criterion:    "An append-only, hash-chained, integrity-verifiable audit ledger plus a system/agent inventory provides demonstrable evidence of how processing is governed over time.",
				Capabilities: []CapabilityKey{"audit_trail", "audit_integrity", "audit_immutability", "audit_export", "transparency_record"},
			},
			{
				ID:           "art_25",
				Title:        "Data protection by design and by default (Art. 25)",
				Requirement:  "Implement appropriate technical and organizational measures (e.g. minimization, pseudonymisation) at design time, and ensure by default only data necessary for each purpose is processed and not made accessible to an indefinite number of persons.",
				Criterion:    "By construction the engine minimizes stored data, ships secure defaults (TLS on, no default creds, localhost bind, deny-by-default approval gates), and enforces RBAC so data is not accessible by default; PII-discovery scans evidence the minimization posture is verified against the data actually held.",
				Capabilities: []CapabilityKey{"data_minimization", "secure_defaults", "access_control_rbac", "human_oversight", "pii_discovery"},
			},
			{
				ID:           "art_30",
				Title:        "Records of processing activities (Art. 30)",
				Requirement:  "Maintain records of processing activities, including categories of processing, recipients, transfers to third countries, and a general description of technical and organizational security measures.",
				Criterion:    "A system/agent inventory plus the audit ledger and recorded access edges constitute a maintainable, exportable record of which agents access which resources and how.",
				Capabilities: []CapabilityKey{"transparency_record", "audit_trail", "access_observability", "audit_export"},
			},
			{
				ID:           "art_32_1_a",
				Title:        "Security of processing — pseudonymisation and encryption (Art. 32(1)(a))",
				Requirement:  "Implement appropriate measures including the pseudonymisation and encryption of personal data as appropriate to the risk.",
				Criterion:    "mTLS/TLS protects data in transit by default; at-rest encryption is attested only when the opt-in is enabled; a deny-closed DLP gate keyed on sensitivity classes restricts what classified personal data can leave the perimeter.",
				Capabilities: []CapabilityKey{"encryption_transit", "dlp_enforcement"},
			},
			{
				ID:           "art_32_1_b",
				Title:        "Security of processing — confidentiality, integrity, availability, resilience (Art. 32(1)(b))",
				Requirement:  "Ensure the ongoing confidentiality, integrity, availability and resilience of processing systems and services.",
				Criterion:    "RBAC/isolation and transit encryption protect confidentiality; the hash-chained ledger evidences integrity; anomaly findings and governed identities support ongoing resilience.",
				Capabilities: []CapabilityKey{"access_control_rbac", "encryption_transit", "audit_integrity", "audit_immutability", "threat_detection", "identity_governance"},
			},
			{
				ID:           "art_32_1_d",
				Title:        "Security of processing — regular testing and evaluation (Art. 32(1)(d))",
				Requirement:  "Have a process for regularly testing, assessing and evaluating the effectiveness of the technical and organizational measures for ensuring security of processing.",
				Criterion:    "Adversarial/red-team findings, agent eval results, least-privilege drift computation, and a change ledger provide recurring, recorded evidence that controls are tested and assessed.",
				Capabilities: []CapabilityKey{"adversarial_testing", "quality_evaluation", "least_privilege_drift", "change_management"},
			},
			{
				ID:           "art_33",
				Title:        "Notification of a personal data breach to the supervisory authority (Art. 33)",
				Requirement:  "Notify the competent supervisory authority of a personal data breach without undue delay and, where feasible, within 72 hours; document the facts, effects and remedial action (Art. 33(5)).",
				Criterion:    "Security/anomaly detection surfaces incidents and an integrity-verified, reconstructable forensic timeline supports the 72-hour assessment and the Art. 33(5) documentation duty.",
				Capabilities: []CapabilityKey{"threat_detection", "forensic_capability", "audit_trail", "audit_integrity"},
			},
			{
				ID:           "art_44",
				Title:        "General principle for transfers (Chapter V, Art. 44)",
				Requirement:  "Any transfer of personal data to a third country or international organization may take place only subject to the conditions of Chapter V, so that the level of protection guaranteed by the GDPR is not undermined.",
				Criterion:    "A residency attestation with no observed perimeter-egress violation, plus data-lineage evidence that client data stays within the perimeter and a deny-closed DLP egress gate blocking classified content from leaving (append-only knowledge.dlp_event evidence), demonstrates transfer/residency boundaries are honored.",
				Capabilities: []CapabilityKey{"data_residency", "data_lineage", "dlp_enforcement"},
			},
			{
				ID:           "art_24",
				Title:        "Responsibility of the controller (Art. 24)",
				Requirement:  "Implement appropriate technical and organizational measures, taking into account risk, to ensure and demonstrate that processing is performed in accordance with the Regulation, and review/update them.",
				Criterion:    "Risk classification of agents, governed non-human identities, change-management records and an immutable audit trail demonstrate measures are implemented, risk-driven and reviewable.",
				Capabilities: []CapabilityKey{"risk_classification", "identity_governance", "change_management", "audit_trail", "audit_immutability"},
			},
			{
				ID:          "art_17",
				Title:       "Right to erasure ('right to be forgotten') (Art. 17)",
				Requirement: "On request and where the conditions apply, erase a data subject's personal data without undue delay across all processing locations.",
				Criterion:   "An auditable workflow that locates and erases a specific data subject's personal data on request and records the fulfillment.",
				// Closes the former honest gap with OPERATIONAL evidence only:
				// a sealed, ledger-anchored erasure receipt proves a real
				// fulfillment; a workflow that has never run stays a gap. The single
				// capability is deliberate — co-mapping audit_trail would let a
				// quiet tenant's ledger alone drag the control to "partial" with
				// zero erasures ever fulfilled.
				Capabilities: []CapabilityKey{"rtbf_erasure"},
			},
		},
	},
	{
		ID:         "nis2",
		Name:       "NIS 2 Directive",
		Version:    "Directive (EU) 2022/2555",
		Authority:  "European Parliament and Council of the EU (enforced via national competent authorities and CSIRTs designated under each Member State's transposing law)",
		Disclaimer: "Technical control mapping for an AI control plane only; not legal advice and not a certification of compliance with Directive (EU) 2022/2555. NIS 2 obligations apply to entities classified as essential or important by the Member State — the platform cannot determine whether a given operator falls within scope.",
		Pin: FrameworkPin{
			Document:    "Directive (EU) 2022/2555 (NIS 2 Directive)",
			PublishedOn: "2022-12-27",
			SourceURL:   "https://eur-lex.europa.eu/eli/dir/2022/2555/oj",
			VerifiedOn:  "2026-06-30",
			Status:      PinInForce,
		},
		Controls: []Control{
			{
				ID:           "art_20",
				Title:        "Governance (Art 20)",
				Requirement:  "Management bodies of essential and important entities must approve cybersecurity risk-management measures, oversee their implementation, and be accountable for non-compliance; members must undergo training and offer similar training to employees (Art 20(1)-(2)).",
				Criterion:    "Agent/system risk classifications and a maintained inventory evidence that the AI estate is assessed and documented for management oversight; training obligations are organizational and NOT evidenced by the platform.",
				Capabilities: []CapabilityKey{"risk_classification", "transparency_record"},
				Note:         "The platform evidences the risk-classified inventory; management body training/accountability is organizational and remains an honest gap.",
				MilestoneIDs: []string{"nis2.transposition_deadline"},
			},
			{
				ID:           "art_21_2a",
				Title:        "Risk analysis and information system security policies (Art 21(2)(a))",
				Requirement:  "Entities must adopt policies on risk analysis and information system security as part of their cybersecurity risk-management measures.",
				Criterion:    "Risk classification of agents/systems, secure-defaults enforcement and threat-detection findings evidence an implemented risk-analysis and IS-security program.",
				Capabilities: []CapabilityKey{"risk_classification", "secure_defaults", "threat_detection"},
				MilestoneIDs: []string{"nis2.transposition_deadline"},
			},
			{
				ID:           "art_21_2b",
				Title:        "Incident handling (Art 21(2)(b))",
				Requirement:  "Entities must implement incident handling capabilities as part of their cybersecurity risk-management measures.",
				Criterion:    "A reconstructable forensic timeline from the tamper-evident audit trail, guardrail/anomaly threat-detection findings and continuous WORM/SIEM export evidence an incident-detection-and-response capability.",
				Capabilities: []CapabilityKey{"forensic_capability", "audit_trail", "threat_detection", "audit_export"},
				MilestoneIDs: []string{"nis2.transposition_deadline"},
			},
			{
				ID:           "art_21_2c",
				Title:        "Business continuity and crisis management (Art 21(2)(c))",
				Requirement:  "Entities must implement business continuity measures including backup management, disaster recovery and crisis management.",
				Criterion:    "Continuous WORM/SIEM export of the append-only, immutable ledger and change-management records evidence that operational continuity data is preserved and recoverable.",
				Capabilities: []CapabilityKey{"audit_export", "audit_immutability", "change_management"},
				Note:         "The platform evidences data preservation and export; BC/DR planning, backup rotation and crisis-management processes are organizational.",
				MilestoneIDs: []string{"nis2.transposition_deadline"},
			},
			{
				ID:           "art_21_2d",
				Title:        "Supply chain security (Art 21(2)(d))",
				Requirement:  "Entities must address supply chain security including security-related aspects of relationships with direct suppliers and service providers, taking into account their vulnerabilities and the overall quality of their products and cybersecurity practices.",
				Criterion:    "Signed releases with SBOM, operator-verified GPAI compliance posture for model providers and signed-model admission with provenance verification evidence supply-chain integrity and third-party governance.",
				Capabilities: []CapabilityKey{"supply_chain", "supplier_gpai_posture", "signed_model_admission"},
				Note:         "The platform evidences supplier technical controls; contractual security requirements for suppliers are organizational.",
				MilestoneIDs: []string{"nis2.transposition_deadline"},
			},
			{
				ID:           "art_21_2e",
				Title:        "Security in acquisition, development and maintenance (Art 21(2)(e))",
				Requirement:  "Entities must implement security measures covering network and information system acquisition, development and maintenance, including vulnerability handling and disclosure.",
				Criterion:    "Governed change-management records, secure defaults and adversarial/robustness testing evidence that security is embedded in the development and maintenance lifecycle.",
				Capabilities: []CapabilityKey{"change_management", "secure_defaults", "adversarial_testing"},
				Note:         "The platform evidences development-lifecycle security controls; a formal vulnerability disclosure program is organizational.",
				MilestoneIDs: []string{"nis2.transposition_deadline"},
			},
			{
				ID:           "art_21_2f",
				Title:        "Cybersecurity risk-management measures effectiveness assessment (Art 21(2)(f))",
				Requirement:  "Entities must have policies and procedures to assess the effectiveness of cybersecurity risk-management measures.",
				Criterion:    "Agent evaluation/quality results, adversarial-testing findings and live hash-chain integrity verification evidence a measure-effectiveness assessment capability.",
				Capabilities: []CapabilityKey{"quality_evaluation", "adversarial_testing", "audit_integrity"},
				MilestoneIDs: []string{"nis2.transposition_deadline"},
			},
			{
				ID:           "art_21_2g",
				Title:        "Basic cyber hygiene practices and cybersecurity training (Art 21(2)(g))",
				Requirement:  "Entities must implement basic cyber hygiene practices and cybersecurity training for all employees.",
				Criterion:    "Governed non-human identities evidence that the AI estate's cyber hygiene (lifecycle, rotation, deprovisioning) is systematized.",
				Capabilities: []CapabilityKey{"identity_governance"},
				Note:         "The platform evidences NHI hygiene; human employee training and awareness programs are organizational and remain an honest gap.",
				MilestoneIDs: []string{"nis2.transposition_deadline"},
			},
			{
				ID:           "art_21_2h",
				Title:        "Policies and procedures on the use of cryptography and encryption (Art 21(2)(h))",
				Requirement:  "Entities must have policies and procedures regarding the use of cryptography and, where appropriate, encryption.",
				Criterion:    "TLS in transit (fail-closed, no plaintext fallback) and an opt-in at-rest encryption attestation evidence cryptographic controls.",
				Capabilities: []CapabilityKey{"encryption_transit", "encryption_at_rest"},
				Note:         "The platform evidences transport and storage encryption; key-management procedures and cryptographic policy governance are organizational.",
				MilestoneIDs: []string{"nis2.transposition_deadline"},
			},
			{
				ID:           "art_21_2i",
				Title:        "Human resources security, access control policies and asset management (Art 21(2)(i))",
				Requirement:  "Entities must implement human resources security, access control policies and asset management.",
				Criterion:    "RBAC with fail-closed isolation, observed access with permitted-vs-observed least-privilege drift, governed identities and a maintained system/agent inventory evidence access-control and asset-management controls.",
				Capabilities: []CapabilityKey{"access_control_rbac", "access_observability", "identity_governance", "transparency_record", "least_privilege_drift"},
				Note:         "The platform evidences AI-estate access control and asset inventory; HR-lifecycle security (background checks, onboarding, offboarding) is organizational.",
				MilestoneIDs: []string{"nis2.transposition_deadline"},
			},
			{
				ID:           "art_21_2j",
				Title:        "Multi-factor authentication or continuous authentication (Art 21(2)(j))",
				Requirement:  "Entities must use multi-factor authentication or continuous authentication solutions, as well as secured voice, video and text communications and secured emergency communication systems, where appropriate.",
				Criterion:    "Governed non-human identities and RBAC evidence that authentication controls are in place for the AI estate.",
				Capabilities: []CapabilityKey{"identity_governance", "access_control_rbac"},
				Note:         "MFA enforcement is IdP-side; the platform evidences governed identities and access control but does not implement the MFA mechanism itself.",
				MilestoneIDs: []string{"nis2.transposition_deadline"},
			},
			{
				ID:           "art_23",
				Title:        "Notification obligations (Art 23)",
				Requirement:  "Essential and important entities must notify the CSIRT or competent authority of any significant incident: early warning within 24 hours, incident notification within 72 hours, and a final report within one month, specifying whether the incident was caused by unlawful or malicious action and whether it could have cross-border impact (Art 23(4)).",
				Criterion:    "A reconstructable forensic timeline, tamper-evident audit trail, threat-detection findings and continuous SIEM/WORM export evidence the capacity to detect, characterize and document incidents within the reporting windows.",
				Capabilities: []CapabilityKey{"forensic_capability", "audit_trail", "audit_export", "threat_detection"},
				Note:         "The platform evidences incident-detection and record-keeping capacity; the duty to classify and notify rests with the entity. Structured incident classification and tiered report drafting are available as an enterprise depth add-on (NIS2 incident classifier).",
				MilestoneIDs: []string{"nis2.transposition_deadline"},
			},
			{
				ID:           "art_29",
				Title:        "Cybersecurity information-sharing arrangements (Art 29)",
				Requirement:  "Essential and important entities may voluntarily exchange relevant cybersecurity information among themselves, including threat intelligence, indicators of compromise, tactics and procedures, and cybersecurity alerts.",
				Criterion:    "Continuous WORM/SIEM export of security-relevant events and threat-detection findings evidence the capacity to participate in information-sharing arrangements.",
				Capabilities: []CapabilityKey{"audit_export", "threat_detection"},
				Note:         "The platform evidences exportable security telemetry; the establishment and governance of sharing agreements is organizational.",
				MilestoneIDs: []string{"nis2.transposition_deadline"},
			},
		},
	},
	{
		ID:      "eu_pld",
		Name:    "EU Product Liability Directive (revised) — defense-evidence crosswalk",
		Version: "Directive (EU) 2024/2853",
		Pin: FrameworkPin{
			Document:    "Directive (EU) 2024/2853 (revised Product Liability Directive)",
			PublishedOn: "2024-11-18",
			SourceURL:   "https://eur-lex.europa.eu/eli/dir/2024/2853/oj",
			VerifiedOn:  "2026-06-10",
			Status:      PinInForce,
		},
		Authority:  "European Parliament and Council of the European Union (national transposition deadline lives in the regulatory calendar: milestone eu_pld.transposition_deadline)",
		Disclaimer: "DEFENSE-EVIDENCE framing, not a compliance framework: the revised PLD is no-fault liability law under which software — including AI systems — is a 'product' (Art 4(1), recital 13). This crosswalk maps which control-plane records a producer/operator would rely on under the PLD's evidence rules; it is NOT legal advice, NOT a liability assessment and NOT a certification. The PLD is the liability instrument that remains after the EU's separate AI-specific liability proposal was withdrawn — the withdrawal is recorded as the calendar milestone eu_aild.withdrawn and must never be cited as pending law.",
		Controls: []Control{
			{
				ID:           "art_9_disclosure",
				Title:        "Disclosure of evidence (Art 9)",
				Requirement:  "On a claimant's substantiated request, the defendant must disclose relevant evidence at its disposal (Art 9(1)), limited to what is necessary and proportionate (Art 9(3)) and subject to the Art 9(4)-(7) safeguards; failing to disclose triggers the Art 10(2)(a) presumption of defectiveness.",
				Criterion:    "Disclosable, tamper-evident records exist BEFORE any dispute: an append-only, hash-chained, integrity-verified ledger with continuous WORM/SIEM export, reconstructable forensic timelines, and privileged-session recordings — produced from normal operation, not reconstructed after the fact.",
				Capabilities: []CapabilityKey{"audit_trail", "audit_integrity", "audit_export", "forensic_capability", "session_recording"},
				Note:         "The plane evidences that disclosure-grade records of AI-estate governance exist; what is legally disclosable in a given claim is counsel's determination, not the platform's.",
				MilestoneIDs: []string{"eu_pld.entry_into_force", "eu_pld.transposition_deadline"},
			},
			{
				ID:           "art_10_presumptions",
				Title:        "Burden of proof and rebuttable presumptions (Art 10)",
				Requirement:  "Defectiveness and/or the causal link are presumed in defined situations — including the defendant's failure to disclose evidence (Art 10(2)(a)) and cases of scientific/technical complexity (Art 10(4)); the presumptions are rebuttable (Art 10(5)).",
				Criterion:    "Rebuttal-grade operational records: deny-by-default human-oversight approvals on high-impact actions, governed change/deployment history, quality-evaluation and adversarial-testing results showing the system was tested, and an immutable record of what the AI estate actually did.",
				Capabilities: []CapabilityKey{"human_oversight", "change_management", "quality_evaluation", "adversarial_testing", "audit_immutability"},
				MilestoneIDs: []string{"eu_pld.transposition_deadline"},
			},
		},
	},
	{
		ID:      "nist_ai_600_1",
		Name:    "NIST AI RMF Generative AI Profile (AI 600-1)",
		Version: "NIST AI 600-1 (July 2024)",
		Pin: FrameworkPin{
			Document:    "NIST AI 600-1 (Generative AI Profile)",
			PublishedOn: "2024-07-26",
			SourceURL:   "https://nvlpubs.nist.gov/nistpubs/ai/NIST.AI.600-1.pdf",
			VerifiedOn:  "2026-06-10",
			Status:      PinFinal,
		},
		Authority:  "National Institute of Standards and Technology (NIST), U.S. Department of Commerce",
		Disclaimer: "Technical mapping of control-plane capabilities to the 12 risks and suggested actions of the NIST AI RMF Generative AI Profile (AI 600-1) — not legal advice, and the module makes no claim of certification or NIST conformity assessment (the AI RMF is a voluntary framework with no certification scheme). The suggested-action IDs (GV/MP/MS/MG) cited below are quoted verbatim from NIST AI 600-1 (July 2024); they predate Model Context Protocol, so any MCP framing is this mapping's interpretation, not NIST's wording.",
		// The twelve GenAI risks (NIST AI 600-1 §2, alphabetical order), each mapped to
		// the platform capability that honestly evidences the relevant suggested actions.
		// A risk the control plane cannot evidence (CBRN, bias, IP) is an HONEST gap with
		// nil Capabilities (docs/SECURITY-HARDENING.md) — never asserted as covered. Every GV/MP/MS/MG
		// action ID cited below was read VERBATIM from NIST AI 600-1 (July 2024;
		// no invented ids) — an auditor can re-verify each id against §3's Suggested
		// Actions tables in the published NIST source.
		Controls: []Control{
			{
				ID:           "cbrn_information_or_capabilities",
				Title:        "CBRN Information or Capabilities",
				Requirement:  "Eased access to or synthesis of materially nefarious chemical, biological, radiological or nuclear information/capabilities (NIST AI 600-1 §2; suggested actions MS-2.6-006, MS-2.6-007).",
				Criterion:    "Out of scope for a control plane: assessing CBRN uplift requires domain red-teaming of the model itself, which the platform cannot evidence.",
				Capabilities: nil,
				Note:         "Honest gap: the control plane does not and cannot evidence CBRN-uplift safety. The conservative content filter flags a narrow set of harmful-content requests but is NOT a CBRN safety control.",
			},
			{
				ID:           "confabulation",
				Title:        "Confabulation",
				Requirement:  "Confidently-stated false or misleading content ('hallucination') (NIST AI 600-1 §2; suggested actions MS-2.3-002, MS-2.3-001, MS-2.5-001, MS-2.6-005).",
				Criterion:    "Agent eval results / regression findings and adversarial robustness testing evidence empirical evaluation of output validity over the system lifecycle.",
				Capabilities: []CapabilityKey{"quality_evaluation", "adversarial_testing"},
				Note:         "The platform evidences that evaluation/robustness testing is performed; it does not itself measure factual accuracy of model outputs.",
			},
			{
				ID:           "dangerous_violent_or_hateful_content",
				Title:        "Dangerous, Violent, or Hateful Content",
				Requirement:  "Easier production of or access to violent, inciting, radicalizing, threatening or harassing content (NIST AI 600-1 §2).",
				Criterion:    "A detective content guardrail flags a conservative set of harmful-content requests (malware, weapon/drug synthesis, self-harm, violence) for human review.",
				Capabilities: []CapabilityKey{"threat_detection"},
				Note:         "The content filter is a deliberately conservative starter set (a flag for human review, not a moderation guarantee); it does not cover the full breadth of this risk.",
			},
			{
				ID:           "data_privacy",
				Title:        "Data Privacy",
				Requirement:  "Leakage/exposure or unauthorized use/de-anonymization of biometric, health, location or other personal data (NIST AI 600-1 §2).",
				Criterion:    "Secret/PII detection on inspected surfaces, deterministic PII-discovery scans with explainable class labels over governed knowledge and document sources, data-lineage proving client data stays within the perimeter, residency attestation, and data minimization by construction (only relations/metadata persisted, never payloads).",
				Capabilities: []CapabilityKey{"threat_detection", "data_lineage", "data_residency", "data_minimization", "pii_discovery"},
			},
			{
				ID:           "environmental_impacts",
				Title:        "Environmental Impacts",
				Requirement:  "High compute resource utilization in training or operating GAI models, and associated environmental impacts (NIST AI 600-1 §2).",
				Criterion:    "FinOps accounts token/compute/cost per inference, the operational compute-utilization signal.",
				Capabilities: []CapabilityKey{"resource_accounting"},
				Note:         "The platform evidences operational compute/cost ACCOUNTING only; it does not measure energy consumption, carbon footprint or training-time environmental impact.",
			},
			{
				ID:           "harmful_bias_or_homogenization",
				Title:        "Harmful Bias or Homogenization",
				Requirement:  "Amplification/exacerbation of bias, or homogenization of outputs/monoculture (NIST AI 600-1 §2). (The suggested-action tables tag this 'Harmful Bias and Homogenization'.)",
				Criterion:    "Requires statistical bias examination of datasets and outputs, which the control plane does not perform.",
				Capabilities: nil,
				Note:         "Honest gap: the control plane does not examine dataset/output bias or measure homogenization.",
			},
			{
				ID:           "human_ai_configuration",
				Title:        "Human-AI Configuration",
				Requirement:  "Arrangements of/interactions between a human and an AI system that can lead to misuse, over-reliance, emotional entanglement or unsafe automation (NIST AI 600-1 §2; suggested action MS-2.5-001).",
				Criterion:    "Human-in-the-loop / approval gates (deny-by-default), governed non-human identities, and observed agent→resource access keep a human in control of the configuration.",
				Capabilities: []CapabilityKey{"human_oversight", "identity_governance", "access_observability"},
			},
			{
				ID:           "information_integrity",
				Title:        "Information Integrity",
				Requirement:  "Lowered barrier to and scaled creation/spread of false, inaccurate or misleading content (NIST AI 600-1 §2; suggested action MS-2.7-004).",
				Criterion:    "A tamper-evident, hash-chained audit ledger with verified integrity and a reconstructable forensic timeline preserve the integrity and provenance of recorded events.",
				Capabilities: []CapabilityKey{"audit_trail", "audit_integrity", "forensic_capability"},
				Note:         "The platform evidences integrity/provenance of its OWN recorded events; it does not detect misinformation in model content (cf. the deliberate LLM09 refutation).",
			},
			{
				ID:           "information_security",
				Title:        "Information Security",
				Requirement:  "Lowered barriers for offensive capabilities, expanded attack surface and novel GAI attacks (prompt injection, data poisoning, model exfiltration) (NIST AI 600-1 §2; suggested actions MS-2.7-007, MS-2.7-009, MS-2.6-006).",
				Criterion:    "Guardrail threat detection (prompt injection, jailbreak, exfil), adversarial/red-team robustness testing, and a tamper-evident audit trail with a reconstructable forensic timeline.",
				Capabilities: []CapabilityKey{"threat_detection", "adversarial_testing", "audit_trail", "forensic_capability"},
			},
			{
				ID:           "intellectual_property",
				Title:        "Intellectual Property",
				Requirement:  "Eased production or replication of alleged IP-, copyright- or trade-secret-protected content without authorization (NIST AI 600-1 §2).",
				Criterion:    "Requires content-provenance / copyright detection, which the control plane does not perform.",
				Capabilities: nil,
				Note:         "Honest gap: the control plane does not detect IP/copyright infringement or content provenance.",
			},
			{
				ID:           "obscene_degrading_abusive_content",
				Title:        "Obscene, Degrading, and/or Abusive Content",
				Requirement:  "Eased production of and access to obscene, degrading and/or abusive imagery, including synthetic CSAM and NCII (NIST AI 600-1 §2).",
				Criterion:    "The detective content guardrail flags a conservative set of harmful-content requests for human review.",
				Capabilities: []CapabilityKey{"threat_detection"},
				Note:         "The content filter is a conservative starter set for human review; it does not provide comprehensive coverage of this risk and performs no image analysis.",
			},
			{
				ID:           "value_chain_and_component_integration",
				Title:        "Value Chain and Component Integration",
				Requirement:  "Non-transparent/untraceable integration of upstream third-party components: improperly obtained/vetted datasets, tools, models or services (NIST AI 600-1 §2; suggested actions GV-6.1-007, GV-6.1-009, GV-6.2-001, GV-6.2-004, MS-2.7-001).",
				Criterion:    "Signed releases + SBOM + pinned minimal dependencies (software supply-chain integrity), change-management/deployment records, signed-model admission verifying the provenance of integrated model artifacts, and a CycloneDX AIBOM recording model/dataset lineage.",
				Capabilities: []CapabilityKey{"supply_chain", "change_management", "signed_model_admission", "model_aibom"},
				Note:         "Evidences the platform's own software supply chain AND the integrated MODEL/DATASET supply chain: signed-model admission (verified artifact provenance) + the AIBOM (model/dataset lineage). Closed-weight brokered models (e.g. Claude) cannot be artifact-signed (no weights) and are instead evidenced via the supplier GPAI posture (supplier_gpai_posture). It does not vet the datasets used to train third-party models.",
			},
		},
	},
	{
		ID:      "csa_maestro",
		Name:    "CSA MAESTRO — Agentic AI threat-modeling framework (7-layer)",
		Version: "Cloud Security Alliance, 2025-02-06",
		Pin: FrameworkPin{
			Document:    "CSA MAESTRO (Multi-Agent Environment, Security, Threat, Risk, & Outcome)",
			PublishedOn: "2025-02-06",
			SourceURL:   "https://cloudsecurityalliance.org/blog/2025/02/06/agentic-ai-threat-modeling-framework-maestro",
			VerifiedOn:  "2026-06-10",
			Status:      PinGuidance,
		},
		Authority:  "Cloud Security Alliance (CSA)",
		Disclaimer: "Positioning crosswalk of control-plane capabilities to the seven layers of the CSA MAESTRO (Multi-Agent Environment, Security, Threat, Risk, & Outcome) agentic threat-modeling framework. MAESTRO is a threat-modeling methodology, NOT a conformance standard — this is a design-toward signaling map, not a certification or conformance claim.",
		Controls: []Control{
			{ID: "L1", Title: "Foundation Models", Requirement: "Threats at the model layer: jailbreak, prompt injection, model/system-prompt extraction, adversarial inputs.", Criterion: "Guardrail threat detection over model I/O and red-team adversarial robustness testing.", Capabilities: []CapabilityKey{"threat_detection", "adversarial_testing"}},
			{ID: "L2", Title: "Data Operations", Requirement: "Threats to data pipelines/RAG/memory: poisoning, leakage, provenance loss.", Criterion: "Data-lineage within the perimeter, data minimization by construction, and residency attestation.", Capabilities: []CapabilityKey{"data_lineage", "data_minimization", "data_residency"}},
			{ID: "L3", Title: "Agent Frameworks", Requirement: "Threats to agent reasoning/tooling/orchestration: goal hijack, tool misuse, unsafe autonomy.", Criterion: "OWASP-Agentic (ASI) guardrail detection over tool/agent surfaces and human-in-the-loop gates.", Capabilities: []CapabilityKey{"threat_detection", "human_oversight"}},
			{ID: "L4", Title: "Deployment and Infrastructure", Requirement: "Threats to the runtime/host/network: insecure defaults, plaintext transport, broad access.", Criterion: "Secure defaults, TLS in transit, RBAC + fail-closed isolation, and change-management records.", Capabilities: []CapabilityKey{"secure_defaults", "encryption_transit", "access_control_rbac", "change_management"}},
			{ID: "L5", Title: "Evaluation and Observability", Requirement: "Threats to monitoring/eval: blind spots, unverifiable telemetry.", Criterion: "Agent eval results, an audit trail, and continuous WORM/SIEM export.", Capabilities: []CapabilityKey{"quality_evaluation", "audit_trail", "audit_export"}},
			{ID: "L6", Title: "Security and Compliance (Vertical/Cross-Layer)", Requirement: "Cross-cutting integrity, forensics and risk governance across every layer.", Criterion: "Tamper-evident audit integrity, a reconstructable forensic timeline, and agent risk classification.", Capabilities: []CapabilityKey{"audit_integrity", "forensic_capability", "risk_classification"}},
			{ID: "L7", Title: "Agent Ecosystem", Requirement: "Threats across the multi-agent/marketplace ecosystem: rogue agents, supply chain, identity, privilege creep.", Criterion: "Governed non-human identities, observed access with least-privilege drift, and supply-chain integrity.", Capabilities: []CapabilityKey{"identity_governance", "access_observability", "least_privilege_drift", "supply_chain"}},
		},
	},
	{
		ID:      "owasp_agentic_tm",
		Name:    "OWASP Agentic AI — Threats and Mitigations (T1–T15)",
		Version: "OWASP GenAI Security Project, v1.0 (2025-02-17)",
		Pin: FrameworkPin{
			Document:    "Agentic AI — Threats and Mitigations, v1.0",
			PublishedOn: "2025-02-17",
			SourceURL:   "https://genai.owasp.org/resource/agentic-ai-threats-and-mitigations/",
			VerifiedOn:  "2026-06-10",
			Status:      PinGuidance,
		},
		Authority:  "OWASP GenAI Security Project — Agentic Security Initiative",
		Disclaimer: "Technical crosswalk of control-plane capabilities to the 15 threats (T1–T15) of OWASP 'Agentic AI – Threats and Mitigations' v1.0 — a threat catalog, not a certifiable standard. Not a certification or conformance claim.",
		Controls: []Control{
			{ID: "T1", Title: "Memory Poisoning", Requirement: "Planting persistent malicious state in agent memory/context.", Criterion: "Guardrail detection of memory/context-poisoning instructions (ASI06).", Capabilities: []CapabilityKey{"threat_detection"}},
			{ID: "T2", Title: "Tool Misuse", Requirement: "Coercing an agent to misuse its tools with destructive/over-privileged arguments.", Criterion: "Guardrail tool-misuse detection (ASI02), observed access, and permitted-vs-observed least-privilege drift.", Capabilities: []CapabilityKey{"threat_detection", "access_observability", "least_privilege_drift"}},
			{ID: "T3", Title: "Privilege Compromise", Requirement: "Privilege escalation / scope creep across the agent estate.", Criterion: "Least-privilege drift, RBAC isolation, and governed identities/policies.", Capabilities: []CapabilityKey{"least_privilege_drift", "access_control_rbac", "identity_governance"}},
			{ID: "T4", Title: "Resource Overload", Requirement: "Exhausting compute/budget through runaway agent activity.", Criterion: "FinOps token/compute/cost accounting bounds and evidences consumption.", Capabilities: []CapabilityKey{"resource_accounting"}},
			{ID: "T5", Title: "Cascading Hallucination Attacks", Requirement: "Compounding false outputs across an agent chain.", Criterion: "Eval/quality evidence plus guardrail detection of cascade-inducing instructions (ASI08).", Capabilities: []CapabilityKey{"quality_evaluation", "threat_detection"}},
			{ID: "T6", Title: "Intent Breaking & Goal Manipulation", Requirement: "Hijacking the agent's goal/objective.", Criterion: "Prompt-injection / goal-hijack guardrail detection (ASI01 / LLM01).", Capabilities: []CapabilityKey{"threat_detection"}},
			{ID: "T7", Title: "Misaligned & Deceptive Behaviors", Requirement: "Agent behaving deceptively or out of alignment.", Criterion: "Red-team robustness testing and guardrail detection.", Capabilities: []CapabilityKey{"adversarial_testing", "threat_detection"}},
			{ID: "T8", Title: "Repudiation & Untraceability", Requirement: "Actions that cannot be attributed or are tamper-able.", Criterion: "An append-only, Ed25519-signed, hash-chained audit ledger with verified integrity (immutable by construction) makes every action attributable and tamper-evident.", Capabilities: []CapabilityKey{"audit_trail", "audit_integrity", "audit_immutability"}},
			{ID: "T9", Title: "Identity Spoofing & Impersonation", Requirement: "Forging an agent/identity.", Criterion: "Governed non-human identities and RBAC.", Capabilities: []CapabilityKey{"identity_governance", "access_control_rbac"}},
			{ID: "T10", Title: "Overwhelming Human-in-the-Loop", Requirement: "Approval fatigue / flooding the human reviewer to extract consent.", Criterion: "Deny-by-default HITL/approval gates and guardrail detection of trust-exploitation/over-approval instructions (ASI09).", Capabilities: []CapabilityKey{"human_oversight", "threat_detection"}},
			{ID: "T11", Title: "Unexpected RCE and Code Attacks", Requirement: "Unintended remote code execution via the agent.", Criterion: "Guardrail detection of code-execution sinks (ASI05); red-team probes execute ONLY in the isolated sandbox, never against the control plane.", Capabilities: []CapabilityKey{"threat_detection", "adversarial_testing"}, Note: "The execution-isolation guarantee is the sandbox (architectural); the platform detects exec instructions and tests refusal, it does not itself sandbox the client's production agent."},
			{ID: "T12", Title: "Agent Communication Poisoning", Requirement: "Poisoning inter-agent messages/relays.", Criterion: "Guardrail detection of insecure inter-agent relay (ASI07).", Capabilities: []CapabilityKey{"threat_detection"}},
			{ID: "T13", Title: "Rogue Agents in Multi-Agent Systems", Requirement: "Covert/unsupervised agents acting against the operator.", Criterion: "Guardrail rogue-agent detection (ASI10) and least-privilege drift across the estate.", Capabilities: []CapabilityKey{"threat_detection", "least_privilege_drift"}},
			{ID: "T14", Title: "Human Attacks on Multi-Agent Systems", Requirement: "Humans exploiting trust/relationships between agents.", Criterion: "Red-team testing and guardrail detection across agent surfaces.", Capabilities: []CapabilityKey{"adversarial_testing", "threat_detection"}},
			{ID: "T15", Title: "Human Manipulation", Requirement: "The agent manipulating its human operator.", Criterion: "Guardrail detection of human-agent trust exploitation (ASI09).", Capabilities: []CapabilityKey{"threat_detection"}},
		},
	},
	{
		ID:      "owasp_agentic_top10",
		Name:    "OWASP Top 10 for Agentic Applications (2026)",
		Version: "OWASP GenAI Security Project, Version 2026 (published 2025-12-09)",
		Pin: FrameworkPin{
			Document:    "OWASP Top 10 For Agentic Applications 2026",
			PublishedOn: "2025-12-09",
			SourceURL:   "https://genai.owasp.org/resource/owasp-top-10-for-agentic-applications-for-2026/",
			VerifiedOn:  "2026-06-10",
			Status:      PinGuidance,
		},
		Authority:  "OWASP GenAI Security Project — Agentic Security Initiative",
		Disclaimer: "Technical crosswalk of control-plane capabilities to the OWASP Top 10 for Agentic Applications 2026 (ASI01–ASI10) — a risk-awareness list, not a certifiable standard. It COMPLEMENTS (does not supersede) the same initiative's Agentic AI — Threats and Mitigations taxonomy (owasp_agentic_tm, T1–T15); both entries are deliberately kept. Entry IDs are plain ASI01–ASI10 per the official PDF (no year suffix). Not a certification or conformance claim.",
		Controls: []Control{
			{
				ID:           "ASI01",
				Title:        "Agent Goal Hijack",
				Requirement:  "Attackers manipulate an agent's objectives, task selection or decision pathways via prompt-based manipulation, deceptive tool descriptions/outputs or poisoned context.",
				Criterion:    "Deterministic guardrail detection of goal-hijack instructions over every inspected surface (module IX ASI01 rules, cross-tagged LLM01:2025 / ATLAS prompt-injection).",
				Capabilities: []CapabilityKey{"threat_detection"},
			},
			{
				ID:           "ASI02",
				Title:        "Tool Misuse and Exploitation",
				Requirement:  "Agents misuse legitimate tools — destructive or over-privileged invocations — due to prompt injection, misalignment or excessive permissions.",
				Criterion:    "Guardrail tool-misuse detection over tool arguments (ASI02 rules), observed agent→resource access edges, and permitted-vs-observed least-privilege drift bounding what a misused tool can reach.",
				Capabilities: []CapabilityKey{"threat_detection", "access_observability", "least_privilege_drift"},
			},
			{
				ID:           "ASI03",
				Title:        "Identity and Privilege Abuse",
				Requirement:  "Exploits the dynamic trust and delegation patterns of agent systems to escalate access and bypass controls.",
				Criterion:    "Governed non-human identities (lifecycle, rotation, expiry —), engine-enforced RBAC, least-privilege drift surfacing over-provisioned grants, and guardrail privilege-escalation detection (ASI03 rules).",
				Capabilities: []CapabilityKey{"identity_governance", "access_control_rbac", "least_privilege_drift", "threat_detection"},
			},
			{
				ID:           "ASI04",
				Title:        "Agentic Supply Chain Vulnerabilities",
				Requirement:  "Third-party agents, tools, plug-ins, models/weights, datasets and MCP/A2A interfaces introduce compromised components into agent workflows.",
				Criterion:    "Signed releases + SBOM + pinned dependencies for the platform itself; signed-model admission (deny-closed per policy) and the sealed AIBOM for model/dataset artifacts; guardrail detection of install-unverified-tool instructions (ASI04 rules).",
				Capabilities: []CapabilityKey{"supply_chain", "signed_model_admission", "model_aibom", "threat_detection"},
			},
			{
				ID:           "ASI05",
				Title:        "Unexpected Code Execution (RCE)",
				Requirement:  "Code-generation features or embedded tool access escalate into unintended remote code execution.",
				Criterion:    "Guardrail detection of code-execution sinks in agent traffic (ASI05 rules) and red-team probes of exec refusal.",
				Capabilities: []CapabilityKey{"threat_detection", "adversarial_testing"},
				Note:         "Red-team probes execute ONLY in the isolated sandbox; the plane detects exec instructions and tests refusal — it does not itself sandbox the client's production agent (same honest line as owasp_agentic_tm T11).",
			},
			{
				ID:           "ASI06",
				Title:        "Memory & Context Poisoning",
				Requirement:  "Adversaries corrupt or seed an agent's stored/retrievable context — summaries, embeddings, RAG stores, long-term memory — with malicious or misleading data that steers future behavior.",
				Criterion:    "Guardrail detection of memory/context-poisoning instructions (ASI06 rules) plus retrieval lineage tracing every chunk served from governed knowledge stores back to its recorded source (origin→answer, module VIII) — a poisoned chunk is traceable after retrieval.",
				Capabilities: []CapabilityKey{"threat_detection", "data_lineage"},
				Note:         "Lineage is retrieval-time (origin→answer); ingest-time/out-of-band poisoning of the store itself is not evidenced by the control plane.",
			},
			{
				ID:           "ASI07",
				Title:        "Insecure Inter-Agent Communication",
				Requirement:  "Exchanges between autonomous agents lack authentication, integrity or semantic validation, allowing interception, spoofing or manipulation of inter-agent traffic.",
				Criterion:    "Guardrail detection of insecure inter-agent relay (ASI07 rules), TLS/mTLS by default on every channel the plane controls, and governed agent identities for attribution.",
				Capabilities: []CapabilityKey{"threat_detection", "encryption_transit", "identity_governance"},
			},
			{
				ID:           "ASI08",
				Title:        "Cascading Failures",
				Requirement:  "A single fault — hallucination, malicious input, corrupted tool or poisoned memory — propagates across interconnected agents and tools.",
				Criterion:    "Guardrail detection of unbounded retry/recursion/fan-out instructions (ASI08 rules), per-inference resource accounting bounding runaway consumption, and quality-evaluation results catching compounding regressions.",
				Capabilities: []CapabilityKey{"threat_detection", "resource_accounting", "quality_evaluation"},
			},
			{
				ID:           "ASI09",
				Title:        "Human-Agent Trust Exploitation",
				Requirement:  "Adversaries or misaligned designs exploit the trust agents establish with humans (anthropomorphism, authority bias, persuasive explanations) to influence decisions or extract approval.",
				Criterion:    "Deny-by-default HITL approval gates whose decisions rest with operators (never delegated to the agent), and guardrail detection of over-approval / reviewer-social-engineering instructions (ASI09 rules).",
				Capabilities: []CapabilityKey{"human_oversight", "threat_detection"},
			},
			{
				ID:           "ASI10",
				Title:        "Rogue Agents",
				Requirement:  "Malicious or compromised agents deviate from their intended function or authorized scope, acting harmfully, deceptively or parasitically within multi-agent or human-agent ecosystems.",
				Criterion:    "Guardrail rogue-agent detection (ASI10 rules); permitted-vs-observed drift IS scope deviation made measurable; governed identities and observed access edges localize the deviating agent across the estate.",
				Capabilities: []CapabilityKey{"threat_detection", "least_privilege_drift", "identity_governance", "access_observability"},
			},
		},
	},
	{
		ID:      "cisa_agentic_adoption",
		Name:    "Five Eyes — Careful Adoption of Agentic AI Services",
		Version: "Joint guidance, 2026-05-01 (ASD ACSC, CISA, NSA, CCCS, NCSC-NZ, NCSC-UK)",
		Pin: FrameworkPin{
			Document:    "Careful adoption of agentic AI services",
			PublishedOn: "2026-05-01",
			SourceURL:   "https://www.cisa.gov/resources-tools/resources/careful-adoption-agentic-ai-services",
			VerifiedOn:  "2026-06-10",
			Status:      PinGuidance,
		},
		Authority:  "Co-authored by ASD's ACSC (AU), CISA and NSA (US), the Canadian Centre for Cyber Security, NCSC-NZ and NCSC-UK",
		Disclaimer: "Crosswalk to the FIVE OFFICIAL risk categories of the joint guidance 'Careful adoption of agentic AI services' (2026-05-01): Privilege risks, Design and configuration risks, Behaviour risks, Structural risks, Accountability risks (replaces the earlier generic placeholder controls). GUIDANCE, not a certifiable standard; the mapping is design-toward and makes no conformance claim.",
		Controls: []Control{
			{
				ID:           "privilege_risks",
				Title:        "Privilege risks",
				Requirement:  "Privileges assigned to agents directly determine the risk they can introduce: privilege compromise and scope creep, identity spoofing and agent impersonation (incl. the confused-deputy pattern). Strict least privilege is critical.",
				Criterion:    "Each agent is a governed, distinct non-human identity with a lifecycle; permitted-vs-observed R/RW drift surfaces scope creep; engine-enforced RBAC keeps least privilege; observed access edges attribute every touch.",
				Capabilities: []CapabilityKey{"identity_governance", "least_privilege_drift", "access_control_rbac", "access_observability"},
				Note:         "The guidance mandates ephemeral/just-in-time credentials and cryptographically anchored per-agent identity — issuing those is the estate IdP/workload-identity duty; the plane governs and observes them (NHI lifecycle) rather than issuing credentials.",
			},
			{
				ID:           "design_configuration_risks",
				Title:        "Design and configuration risks",
				Requirement:  "Insecure design and provisioning decisions: unvetted third-party components with excessive privileges, static/stale permission checks evaluated only at startup, poor segmentation between agent environments, incomplete or outdated allow lists.",
				Criterion:    "Authorization is evaluated continuously at a centralized policy decision point per request (the deny-closed PEP — never a startup-time snapshot), secure-by-default posture ships fail-safe, changes/deployments are governed and recorded, and platform components are supply-chain vetted.",
				Capabilities: []CapabilityKey{"access_control_rbac", "secure_defaults", "change_management", "supply_chain"},
			},
			{
				ID:           "behaviour_risks",
				Title:        "Behaviour risks",
				Requirement:  "Ways agents may act unexpectedly, cause harm or become exploitable: goal misalignment and unintended behavior, deceptive behavior, emergent capabilities, and malicious exploitation (prompt injection, jailbreaks, data poisoning).",
				Criterion:    "Guardrail detection over agent surfaces (goal hijack, injection, deception signals), deny-by-default human approval for high-impact actions (decisions never delegated to the agent), agent eval results, and adversarial red-team findings.",
				Capabilities: []CapabilityKey{"threat_detection", "human_oversight", "quality_evaluation", "adversarial_testing"},
			},
			{
				ID:           "structural_risks",
				Title:        "Structural risks",
				Requirement:  "Risks from the interconnected structure between agents, tools and the outside world: orchestration/resource exhaustion (DoS/sponge attacks, cascading failures, hallucination propagation), tool use, third-party components (tool/agent squatting), data aggregation, rogue agents, and insecure communication.",
				Criterion:    "Per-inference resource accounting bounds consumption; guardrail cascade/rogue/insecure-relay detection (ASI08/ASI10/ASI07 lenses); TLS/mTLS on every channel the plane controls; observed access edges map the actual interconnection structure.",
				Capabilities: []CapabilityKey{"resource_accounting", "threat_detection", "encryption_transit", "access_observability"},
			},
			{
				ID:           "accountability_risks",
				Title:        "Accountability risks",
				Requirement:  "Agentic architectures obscure what caused an action: opaque decision-making, sub-agent spawning, non-reproducibility, log volume, hallucination, and processes outpacing human monitoring.",
				Criterion:    "Every action is attributable in an append-only, hash-chained, integrity-verified ledger (immutable by construction); a reconstructable forensic timeline and privileged-session recordings answer 'what caused this action' after the fact.",
				Capabilities: []CapabilityKey{"audit_trail", "audit_integrity", "audit_immutability", "forensic_capability", "session_recording"},
				Note:         "The guidance asks for monitoring of agent INTERNAL processes and intention-vs-behavior anomaly detection; the plane evidences action-level attribution and session replay, not model-internal introspection.",
			},
		},
	},
	{
		ID:      "cisa_ai_data_security",
		Name:    "CISA/NSA/FBI — AI Data Security (joint CSI)",
		Version: "Joint Cybersecurity Information Sheet, 2025-05-22 (U/OO/157249-25, Ver. 1.0)",
		Pin: FrameworkPin{
			Document:    "AI Data Security: Best Practices for Securing Data Used to Train & Operate AI Systems",
			PublishedOn: "2025-05-22",
			SourceURL:   "https://media.defense.gov/2025/May/22/2003720601/-1/-1/0/CSI_AI_DATA_SECURITY.PDF",
			VerifiedOn:  "2026-06-10",
			Status:      PinGuidance,
		},
		Authority:  "NSA's Artificial Intelligence Security Center (AISC), CISA, FBI, ASD's ACSC, NCSC-NZ and NCSC-UK",
		Disclaimer: "Crosswalk to the ten best practices of the joint CSI 'AI Data Security' (2025-05-22), structured over the NIST AI RMF lifecycle stages. GUIDANCE, not a certifiable standard; design-toward, no conformance claim. Practices the control plane cannot evidence (secure media sanitization, privacy-preserving computation) are honest gaps or carry explicit coverage notes.",
		Controls: []Control{
			{
				ID:           "source_provenance",
				Title:        "Source reliable data and track data provenance",
				Requirement:  "Use authoritative data sources and maintain provenance — the CSI prescribes a cryptographically signed, immutable, append-only ledger of data changes.",
				Criterion:    "Dataset records carry provenance attestation and are sealed into the ledger-anchored AIBOM; data-lineage rows track flow within the perimeter; the provenance anchor is the append-only, hash-chained, Ed25519-signed audit ledger — literally the construction the CSI prescribes.",
				Capabilities: []CapabilityKey{"data_lineage", "model_aibom", "audit_immutability"},
			},
			{
				ID:           "verify_integrity",
				Title:        "Verify and maintain data integrity during storage and transport",
				Requirement:  "Use checksums and cryptographic hashes to verify data has not been altered or tampered with during storage or transmission.",
				Criterion:    "Dataset/artifact content hashes ride in the AIBOM and admission verdicts, the ledger hash-chain verifies live, and TLS protects every transport the plane controls.",
				Capabilities: []CapabilityKey{"audit_integrity", "encryption_transit", "model_aibom"},
			},
			{
				ID:           "digital_signatures",
				Title:        "Employ digital signatures to authenticate trusted data revisions",
				Requirement:  "Cryptographically sign original data and every revision; adopt quantum-resistant digital signature standards.",
				Criterion:    "Signed-model admission verifies artifact signatures before admission (OpenSSF Model Signing / Sigstore), and the ledger's per-event Ed25519 signatures authenticate the evidence trail.",
				Capabilities: []CapabilityKey{"signed_model_admission", "audit_immutability"},
				Note:         "Quantum-resistant signatures (e.g. ML-DSA) are a tracked roadmap item (docs/SEC-G3, CNSA 2.0 milestone in the calendar), not shipped — current signing is Ed25519/ECDSA/RSA.",
			},
			{
				ID:           "trusted_infrastructure",
				Title:        "Leverage trusted infrastructure",
				Requirement:  "Use a trusted computing environment leveraging Zero Trust architecture; provide secure enclaves for sensitive data processing.",
				Criterion:    "Deny-closed RBAC + fail-closed multi-tenant isolation, secure-by-default deployment posture, and TLS-by-default transport evidence the Zero-Trust slice the plane controls.",
				Capabilities: []CapabilityKey{"access_control_rbac", "secure_defaults", "encryption_transit"},
				Note:         "Hardware secure enclaves / confidential computing are estate infrastructure duties the plane does not evidence.",
			},
			{
				ID:           "classification_access",
				Title:        "Classify data and use access controls",
				Requirement:  "Categorize data by sensitivity with a classification system; classify AI system output at the level of its input data; gate access accordingly.",
				Criterion:    "Deterministic sensitivity/PII classification labels governed content (named rule + count, never values), a deny-closed DLP gate keyed on those classes restricts egress, and RBAC gates access.",
				Capabilities: []CapabilityKey{"pii_discovery", "dlp_enforcement", "access_control_rbac"},
			},
			{
				ID:           "encrypt_data",
				Title:        "Encrypt data",
				Requirement:  "Secure data at rest, in transit and during processing with encryption proportional to the protection level (AES-256 cited as the de facto standard; TLS or post-quantum in transit).",
				Criterion:    "TLS ≥1.2 (with PQC hybrid key exchange where supported, docs/SEC-G3) protects transit by default; at-rest encryption is attested per tenant (opt-in — absent until attested).",
				Capabilities: []CapabilityKey{"encryption_transit", "encryption_at_rest"},
				Note:         "Encryption DURING PROCESSING (confidential computing) is not evidenced by the control plane.",
			},
			{
				ID:           "secure_storage",
				Title:        "Store data securely",
				Requirement:  "Store data in certified storage devices enforcing NIST FIPS 140-3 compliance.",
				Criterion:    "At-rest encryption attestation per tenant; the FIPS-mode build pins a FIPS 140-3 validated module (docs/SEC-G3) — the FIPS 140-2 sunset is a calendar milestone.",
				Capabilities: []CapabilityKey{"encryption_at_rest"},
				Note:         "FIPS 140-3-validated STORAGE DEVICES are an estate/hardware duty the control plane cannot evidence; the plane evidences the tenant's at-rest encryption attestation and its own FIPS-mode build only — never read this as certified-storage coverage.",
				MilestoneIDs: []string{"fips_140_2.historical"},
			},
			{
				ID:           "privacy_preserving",
				Title:        "Leverage privacy-preserving techniques",
				Requirement:  "Apply data masking/depersonalization, differential privacy, federated learning and secure multi-party computation where practical.",
				Criterion:    "Structural data minimization (only relations/metadata persisted, never payloads) and deterministic PII discovery making personal data visible for reduction.",
				Capabilities: []CapabilityKey{"data_minimization", "pii_discovery"},
				Note:         "Differential privacy, federated learning and SMPC are model/data-pipeline techniques the control plane does not implement or evidence.",
			},
			{
				ID:           "secure_deletion",
				Title:        "Delete data securely",
				Requirement:  "Erase drives with cryptographic erase, block erase or data overwrite (NIST SP 800-88) before repurposing or decommissioning.",
				Criterion:    "Evidence of NIST SP 800-88 media sanitization for storage used by AI data.",
				Capabilities: nil, // honest gap: media sanitization is an estate/hardware duty the plane cannot evidence
			},
			{
				ID:           "ongoing_risk_assessments",
				Title:        "Conduct ongoing data security risk assessments",
				Requirement:  "Conduct ongoing risk assessments using industry-standard frameworks (NIST RMF, NIST AI 100-1) to evaluate the AI data security landscape and prioritize actions.",
				Criterion:    "Recurring, governed AI risk classifications (EU AI Act × NIST AI RMF cross-walk), continuous threat detection and least-privilege drift provide the live assessment signal.",
				Capabilities: []CapabilityKey{"risk_classification", "threat_detection", "least_privilege_drift"},
			},
		},
	},
	{
		ID:      "csa_aicm",
		Name:    "CSA AI Controls Matrix (AICM)",
		Version: "Cloud Security Alliance — AI Controls Matrix v1.1 (bundle 2026-06)",
		Pin: FrameworkPin{
			Document:    "CSA AI Controls Matrix (AICM) v1.1 — 247 control objectives across 18 domains",
			PublishedOn: "2026-06-22",
			SourceURL:   "https://cloudsecurityalliance.org/artifacts/ai-controls-matrix-v1-1",
			VerifiedOn:  "2026-07-03",
			Status:      PinGuidance,
		},
		Authority: "Cloud Security Alliance (CSA)",
		Disclaimer: "Positioning crosswalk of control-plane capabilities to the CSA AI Controls Matrix (AICM): " +
			"247 control objectives across 18 security domains — the 17 Cloud Controls Matrix (CCM v4) domains plus the " +
			"new AI-specific Model Security (MDS) domain. Coverage is mapped at the DOMAIN level: each domain lists the " +
			"capabilities that honestly evidence SOME of its control objectives; it is NOT a per-objective conformance " +
			"claim. Domains the control plane cannot evidence (Datacenter Security, Human Resources Security, Business " +
			"Continuity, Interoperability & Portability, Universal Endpoint Management) are honest nil-capability gaps. " +
			"AICM v1.1 ships with AI-CAIQ v1.1, implementation and auditing guidance, and expanded mappings to ISO 42001, " +
			"EU AI Act, BSI AIC4, NIST AI RMF/AI 600-1 and AIUC-1. STAR for AI Level 1 self-assessment (AI-CAIQ v1.1 " +
			"submitted to the STAR Registry) is the program level available today; certification levels are upcoming — the " +
			"module signals design-toward readiness, never a certification claim. Not a certification or " +
			"conformance claim.",
		Controls: []Control{
			{
				ID:           "A&A",
				Title:        "Audit and Assurance",
				Requirement:  "Establish, plan and execute independent audits and assessments of the AI/cloud control environment, with attestable evidence.",
				Criterion:    "An append-only, hash-chained audit ledger with live integrity verification and continuous WORM/SIEM export provides tamper-evident assurance evidence; per-agent risk classification feeds the audit scope.",
				Capabilities: []CapabilityKey{"audit_trail", "audit_integrity", "audit_export", "risk_classification"},
				Note:         "Evidences the audit EVIDENCE substrate; the independent-assessment program and audit scheduling are an organizational process the control plane does not run.",
			},
			{
				ID:           "AIS",
				Title:        "Application and Interface Security",
				Requirement:  "Secure application and interface (API) design, including input validation, against attacks on the AI application surface.",
				Criterion:    "Guardrail threat detection over agent/application I/O, red-team adversarial robustness testing, TLS on every interface, and secure-by-default posture.",
				Capabilities: []CapabilityKey{"threat_detection", "adversarial_testing", "encryption_transit", "secure_defaults"},
			},
			{
				ID:           "BCR",
				Title:        "Business Continuity Management and Operational Resilience",
				Requirement:  "Business continuity, disaster recovery, backup and resilience planning for AI systems and their supporting infrastructure.",
				Criterion:    "Backup/restore, failover and BC/DR planning are estate/infrastructure duties.",
				Capabilities: nil,
				Note:         "Honest gap: the control plane does not evidence backup, disaster recovery or business-continuity controls — these are estate/infrastructure responsibilities.",
			},
			{
				ID:           "CCC",
				Title:        "Change Control and Configuration Management",
				Requirement:  "Govern changes and configuration of AI systems and their supporting components across the lifecycle.",
				Criterion:    "Governed deployment/change-ledger records, secure-by-default configuration, and a signed/SBOM-backed pinned supply chain for the platform's own components.",
				Capabilities: []CapabilityKey{"change_management", "secure_defaults", "supply_chain"},
			},
			{
				ID:           "CEK",
				Title:        "Cryptography, Encryption and Key Management",
				Requirement:  "Apply cryptography to protect AI data and manage cryptographic keys across their lifecycle.",
				Criterion:    "TLS protects data in transit by default; at-rest encryption is attested per tenant (opt-in).",
				Capabilities: []CapabilityKey{"encryption_transit", "encryption_at_rest"},
				Note:         "Encryption in transit is by design; at-rest is opt-in (a gap until attested). Cryptographic KEY MANAGEMENT (rotation, escrow, HSM) is not evidenced by the control plane.",
			},
			{
				ID:           "DCS",
				Title:        "Datacenter Security",
				Requirement:  "Physical and environmental security of the datacenters hosting AI systems.",
				Criterion:    "Physical/datacenter security is an estate/facilities duty.",
				Capabilities: nil,
				Note:         "Honest gap: physical and datacenter security is outside the control plane's scope.",
			},
			{
				ID:           "DSP",
				Title:        "Data Security and Privacy Lifecycle Management",
				Requirement:  "Govern the security and privacy of data across its lifecycle in AI systems, including classification, minimization, residency and leakage prevention.",
				Criterion:    "Data minimization by construction, deterministic PII/sensitivity discovery, a deny-closed DLP egress gate, data-lineage within the perimeter, and a residency attestation with no observed egress violation.",
				Capabilities: []CapabilityKey{"data_minimization", "pii_discovery", "dlp_enforcement", "data_lineage", "data_residency"},
			},
			{
				ID:           "GRC",
				Title:        "Governance, Risk and Compliance",
				Requirement:  "Establish AI governance, risk management and compliance processes, with an inventory of AI systems and their risk posture.",
				Criterion:    "Governed, audited per-agent AI risk classifications (EU AI Act × NIST), a maintained system/agent inventory, and an append-only audit trail of governance actions.",
				Capabilities: []CapabilityKey{"risk_classification", "transparency_record", "audit_trail"},
			},
			{
				ID:           "HRS",
				Title:        "Human Resources Security",
				Requirement:  "Personnel security across hiring, employment and termination for staff operating AI systems.",
				Criterion:    "Personnel/HR security is an organizational process.",
				Capabilities: nil,
				Note:         "Honest gap: human-resources security (background checks, onboarding/offboarding, training) is an organizational duty the control plane does not evidence.",
			},
			{
				ID:           "IAM",
				Title:        "Identity and Access Management",
				Requirement:  "Manage identities (including non-human/agent identities) and enforce least-privilege access across the AI estate.",
				Criterion:    "Governed non-human identities and policies, engine-enforced RBAC + fail-closed isolation, observed agent→resource access edges, and permitted-vs-observed least-privilege drift.",
				Capabilities: []CapabilityKey{"identity_governance", "access_control_rbac", "access_observability", "least_privilege_drift"},
			},
			{
				ID:           "IPY",
				Title:        "Interoperability and Portability",
				Requirement:  "Ensure data and service interoperability and portability for AI systems to avoid lock-in.",
				Criterion:    "Workload/data portability between providers is a deployment-architecture concern.",
				Capabilities: nil,
				Note:         "Honest gap: AI workload/data portability and provider interoperability are deployment-architecture concerns; the plane's interoperable compliance-evidence export (OSCAL/CEF/syslog/OTLP) supports audit portability but is not a portability control for the customer's AI workloads.",
			},
			{
				ID:           "IVS",
				Title:        "Infrastructure and Virtualization Security",
				Requirement:  "Secure the infrastructure and virtualization layer hosting AI systems, including segmentation and hardening.",
				Criterion:    "Secure-by-default deployment posture, TLS in transit, and RBAC + fail-closed multi-tenant isolation enforced by the engine.",
				Capabilities: []CapabilityKey{"secure_defaults", "encryption_transit", "access_control_rbac"},
				Note:         "Evidences the control plane's own secure posture and tenant isolation; host/network/hypervisor hardening of the estate is an infrastructure duty.",
			},
			{
				ID:           "LOG",
				Title:        "Logging and Monitoring",
				Requirement:  "Produce, protect, retain and analyze logs of AI system activity, and monitor for anomalies.",
				Criterion:    "An append-only, hash-chained audit ledger (immutable by construction) with live integrity verification, continuous WORM/SIEM export, recorded access edges, and guardrail/anomaly detection.",
				Capabilities: []CapabilityKey{"audit_trail", "audit_integrity", "audit_immutability", "audit_export", "access_observability", "threat_detection"},
			},
			{
				ID:           "MDS",
				Title:        "Model Security",
				Requirement:  "Secure AI models against tampering, poisoning, theft and supply-chain compromise across their lifecycle (the AICM AI-specific domain).",
				Criterion:    "Operator-verified signed-model admission and a sealed, ledger-anchored AIBOM (model/dataset lineage) for self-hosted/third-party artifacts, red-team adversarial robustness testing, guardrail threat detection over model I/O, and supply-chain integrity.",
				Capabilities: []CapabilityKey{"signed_model_admission", "model_aibom", "adversarial_testing", "threat_detection", "supply_chain"},
				Note:         "Closed-weight brokered models (e.g. Claude) have no weights to sign and are evidenced via the supplier GPAI posture (supplier_gpai_posture, STA), not artifact signing; the plane does not evidence model-training-time integrity.",
			},
			{
				ID:           "SEF",
				Title:        "Security Incident Management, E-Discovery and Cloud Forensics",
				Requirement:  "Detect, respond to and forensically investigate security incidents involving AI systems.",
				Criterion:    "Guardrail/anomaly threat detection, a reconstructable integrity-verified forensic timeline, and an append-only audit trail with continuous WORM/SIEM export for e-discovery.",
				Capabilities: []CapabilityKey{"threat_detection", "forensic_capability", "audit_trail", "audit_export"},
			},
			{
				ID:           "STA",
				Title:        "Supply Chain Management, Transparency and Accountability",
				Requirement:  "Govern the AI supply chain — third-party components, models, datasets and providers — with transparency and accountability.",
				Criterion:    "Signed releases + SBOM + pinned dependencies for the platform, signed-model admission and the sealed AIBOM for model/dataset artifacts, governed change records, and an operator-verified per-provider GPAI compliance posture for brokered models.",
				Capabilities: []CapabilityKey{"supply_chain", "change_management", "signed_model_admission", "model_aibom", "supplier_gpai_posture"},
			},
			{
				ID:           "TVM",
				Title:        "Threat and Vulnerability Management",
				Requirement:  "Identify, assess and remediate threats and vulnerabilities affecting AI systems.",
				Criterion:    "Continuous guardrail/anomaly threat detection over agent surfaces and red-team adversarial robustness testing.",
				Capabilities: []CapabilityKey{"threat_detection", "adversarial_testing"},
				Note:         "Evidences AI-surface threat detection and adversarial testing; vulnerability scanning and patch management of the underlying estate is an organizational duty.",
			},
			{
				ID:           "UEM",
				Title:        "Universal Endpoint Management",
				Requirement:  "Manage and secure the endpoints/devices that access AI systems.",
				Criterion:    "Endpoint/device management is an estate duty.",
				Capabilities: nil,
				Note:         "Honest gap: endpoint/device management (MDM, device posture) is an estate responsibility outside the control plane.",
			},
		},
	},
	{
		ID:      "llm_top10",
		Name:    "OWASP Top 10 for LLM Applications 2025",
		Version: "OWASP GenAI Security Project — 2025 list (doc v4.2.0a)",
		Pin: FrameworkPin{
			Document:    "OWASP Top 10 for Large Language Model Applications, 2025 (LLM01:2025–LLM10:2025)",
			PublishedOn: "2024-11-18",
			SourceURL:   "https://genai.owasp.org/llm-top-10/",
			VerifiedOn:  "2026-06-21",
			Status:      PinGuidance,
		},
		Authority: "OWASP GenAI Security Project (Top 10 for LLM Applications team)",
		Disclaimer: "Technical crosswalk of control-plane capabilities to the OWASP Top 10 for LLM Applications 2025 " +
			"(LLM01:2025–LLM10:2025) — a risk-awareness list, not a certifiable standard. Coverage is anchored to the " +
			"red-team battery's LLMxx:2025-tagged probes (modules/redteam) and the guardrail detection surfaces. " +
			"LLM09:2025 Misinformation is a deliberate HONEST GAP (nil capabilities): the plane evidences the integrity of " +
			"its OWN recorded events but does NOT detect misinformation in model OUTPUT — never read as covered (cf. " +
			"nist_ai_600_1 information_integrity). Not a certification or conformance claim.",
		Controls: []Control{
			{
				ID:           "LLM01:2025",
				Title:        "Prompt Injection",
				Requirement:  "User or third-party content manipulates the LLM's behavior via crafted prompts (direct) or poisoned external content (indirect), altering outputs or actions.",
				Criterion:    "Deterministic guardrail prompt-injection detection over every inspected surface and a red-team injection/jailbreak battery (probes tagged LLM01:2025 / ATLAS AML.T0051).",
				Capabilities: []CapabilityKey{"threat_detection", "adversarial_testing"},
			},
			{
				ID:           "LLM02:2025",
				Title:        "Sensitive Information Disclosure",
				Requirement:  "The LLM application leaks sensitive data (PII, secrets, proprietary content) through its outputs.",
				Criterion:    "Guardrail exfiltration detection and a red-team data-exfil battery (probes tagged LLM02:2025), deterministic PII/sensitivity discovery, a deny-closed DLP egress gate, and data minimization by construction.",
				Capabilities: []CapabilityKey{"threat_detection", "adversarial_testing", "pii_discovery", "dlp_enforcement", "data_minimization"},
			},
			{
				ID:           "LLM03:2025",
				Title:        "Supply Chain",
				Requirement:  "Vulnerabilities introduced through third-party models, datasets, plug-ins and components in the LLM application supply chain.",
				Criterion:    "Signed releases + SBOM + pinned dependencies for the platform, plus operator-verified signed-model admission and the sealed AIBOM (model/dataset lineage) for model artifacts.",
				Capabilities: []CapabilityKey{"supply_chain", "signed_model_admission", "model_aibom"},
			},
			{
				ID:           "LLM04:2025",
				Title:        "Data and Model Poisoning",
				Requirement:  "Training, fine-tuning or retrieval data (or model weights) are manipulated to introduce backdoors, biases or vulnerabilities.",
				Criterion:    "Retrieval lineage traces every served chunk back to its recorded source (a poisoned chunk is traceable after retrieval), signed-model admission and the AIBOM evidence artifact provenance, and guardrail detection flags poisoning instructions.",
				Capabilities: []CapabilityKey{"data_lineage", "signed_model_admission", "model_aibom", "threat_detection"},
				Note:         "Evidences retrieval-time provenance and model-artifact provenance; ingest-time / training-time data poisoning of the model itself is NOT evidenced by the control plane.",
			},
			{
				ID:           "LLM05:2025",
				Title:        "Improper Output Handling",
				Requirement:  "Insufficient validation/sanitization of LLM output before it is passed to downstream components, enabling XSS, SSRF, SQLi or remote code execution.",
				Criterion:    "Guardrail detection of code-execution and injection sinks in agent traffic (the ASI05/RCE lens).",
				Capabilities: []CapabilityKey{"threat_detection"},
				Note:         "The plane detects exec/injection sinks in agent traffic; output sanitization inside the consuming downstream application is that application's responsibility, not the control plane's — partial coverage by design.",
			},
			{
				ID:           "LLM06:2025",
				Title:        "Excessive Agency",
				Requirement:  "An LLM-based system is granted excessive functionality, permissions or autonomy, so a compromise or hallucination causes damaging actions.",
				Criterion:    "Permitted-vs-observed least-privilege drift surfaces over-provisioned agency, observed access edges and governed non-human identities bound it, engine-enforced RBAC constrains it, and deny-by-default human-in-the-loop gates hold high-impact decisions with operators.",
				Capabilities: []CapabilityKey{"least_privilege_drift", "access_observability", "identity_governance", "access_control_rbac", "human_oversight"},
			},
			{
				ID:           "LLM07:2025",
				Title:        "System Prompt Leakage",
				Requirement:  "The system prompt (instructions, secrets, guardrails) is extracted by an adversary, exposing sensitive configuration.",
				Criterion:    "Guardrail detection of system-prompt-extraction attempts and a red-team battery probing system-prompt leakage (probes tagged LLM07:2025 / ATLAS AML.T0056).",
				Capabilities: []CapabilityKey{"threat_detection", "adversarial_testing"},
			},
			{
				ID:           "LLM08:2025",
				Title:        "Vector and Embedding Weaknesses",
				Requirement:  "Weaknesses in how vectors and embeddings are generated, stored or retrieved (RAG) enable data leakage, cross-tenant access or poisoning.",
				Criterion:    "Retrieval lineage over governed knowledge stores, a deny-closed DLP gate that refuses embed-egress ingest of classified/unscanned content, deterministic PII discovery over knowledge sources, and RBAC over knowledge access.",
				Capabilities: []CapabilityKey{"data_lineage", "dlp_enforcement", "pii_discovery", "access_control_rbac"},
				Note:         "Evidences retrieval provenance, egress gating and access control over governed knowledge; embedding-inversion attacks at the vector-store math level are not directly evidenced.",
			},
			{
				ID:           "LLM09:2025",
				Title:        "Misinformation",
				Requirement:  "The LLM produces false or misleading information presented as authoritative, which users may rely on (overlaps confabulation).",
				Criterion:    "Detecting misinformation in model OUTPUT requires factual-accuracy evaluation of generated content, which the control plane does not perform.",
				Capabilities: nil,
				Note:         "Honest gap (the deliberate LLM09 refutation): the control plane evidences the integrity and provenance of its OWN recorded events, but does NOT detect or measure misinformation in model output. Never read as covered.",
			},
			{
				ID:           "LLM10:2025",
				Title:        "Unbounded Consumption",
				Requirement:  "Uncontrolled inference consumption (resource exhaustion, denial-of-wallet, model extraction via volume) degrades availability or inflates cost.",
				Criterion:    "FinOps accounts token/compute/cost per inference, the operational signal that bounds and evidences consumption.",
				Capabilities: []CapabilityKey{"resource_accounting"},
			},
		},
	},
	{
		ID:      "mitre_atlas",
		Name:    "MITRE ATLAS — adversarial technique coverage",
		Version: "MITRE ATLAS — atlas-data 2026.05 (data format v6.0.0)",
		Pin: FrameworkPin{
			Document:    "MITRE ATLAS (Adversarial Threat Landscape for AI Systems), atlas-data release 2026.05",
			PublishedOn: "2026-05-27",
			SourceURL:   "https://atlas.mitre.org/",
			VerifiedOn:  "2026-06-21",
			Status:      PinGuidance,
		},
		Authority: "The MITRE Corporation",
		Disclaimer: "Coverage map of control-plane detections and red-team probes to MITRE ATLAS adversarial techniques, " +
			"pinned to the published atlas-data 2026.05 release (data format v6.0.0), co-verified with the red-team " +
			"battery's ATLAS version stamp (modules/redteam/atlas.go). ATLAS is a continually-updated knowledge base on a " +
			"YYYY.MM versioning scheme — a versioning intent, NOT an update SLA — so this is a DATED SNAPSHOT of the " +
			"techniques the Olivares red-team battery and guardrails address, never a claim of continuous parity with the " +
			"live matrix or of full-matrix coverage. ATLAS is a knowledge base, not a certifiable standard. Not a " +
			"certification or conformance claim.",
		Controls: []Control{
			{
				ID:           "AML.T0024",
				Title:        "Exfiltration via AI Inference API",
				Requirement:  "An adversary exfiltrates private data or model information by querying the AI inference API.",
				Criterion:    "Guardrail exfiltration detection and a red-team exfil battery probing inference-API data leakage.",
				Capabilities: []CapabilityKey{"threat_detection", "adversarial_testing"},
			},
			{
				ID:           "AML.T0051.000",
				Title:        "LLM Prompt Injection: Direct",
				Requirement:  "An adversary directly injects crafted prompt content to manipulate the LLM's behavior.",
				Criterion:    "Deterministic guardrail prompt-injection detection and a red-team direct-injection battery.",
				Capabilities: []CapabilityKey{"threat_detection", "adversarial_testing"},
			},
			{
				ID:           "AML.T0051.001",
				Title:        "LLM Prompt Injection: Indirect",
				Requirement:  "An adversary plants injection content in external data the LLM later consumes (indirect injection).",
				Criterion:    "Guardrail detection over tool/retrieved surfaces and a red-team indirect-injection battery.",
				Capabilities: []CapabilityKey{"threat_detection", "adversarial_testing"},
			},
			{
				ID:           "AML.T0054",
				Title:        "LLM Jailbreak",
				Requirement:  "An adversary crafts inputs that bypass the model's safety alignment or output controls.",
				Criterion:    "Guardrail jailbreak detection and a red-team jailbreak battery.",
				Capabilities: []CapabilityKey{"threat_detection", "adversarial_testing"},
			},
			{
				ID:           "AML.T0056",
				Title:        "Extract LLM System Prompt",
				Requirement:  "An adversary extracts the system prompt to reveal instructions, secrets or guardrails.",
				Criterion:    "Guardrail detection of system-prompt-extraction attempts and a red-team system-prompt-leak battery.",
				Capabilities: []CapabilityKey{"threat_detection", "adversarial_testing"},
			},
			{
				ID:           "AML.T0057",
				Title:        "LLM Data Leakage",
				Requirement:  "The LLM discloses sensitive or private data from its context or training in its responses.",
				Criterion:    "Guardrail data-leakage detection and a red-team exfil battery.",
				Capabilities: []CapabilityKey{"threat_detection", "adversarial_testing"},
			},
			{
				ID:           "AML.T0104",
				Title:        "Publish Poisoned AI Agent Tool",
				Requirement:  "An adversary publishes a poisoned tool/plugin that an agent may adopt, introducing malicious behavior.",
				Criterion:    "Guardrail tool-poisoning detection, a red-team tool-poisoning battery, and supply-chain integrity over admitted components.",
				Capabilities: []CapabilityKey{"threat_detection", "adversarial_testing", "supply_chain"},
			},
			{
				ID:           "AML.T0105",
				Title:        "Escape to Host",
				Requirement:  "An adversary escapes the agent's execution environment to reach the underlying host.",
				Criterion:    "Guardrail detection of host-escape / code-execution sinks and a red-team escape battery (probes execute only in the isolated sandbox).",
				Capabilities: []CapabilityKey{"threat_detection", "adversarial_testing"},
				Note:         "The plane detects escape/exec instructions and tests refusal; sandbox execution-isolation of the client's production agent is the deployment's responsibility (same honest line as owasp_agentic_tm T11).",
			},
			{
				ID:           "AML.T0110",
				Title:        "AI Agent Tool Poisoning",
				Requirement:  "An adversary poisons the tools an agent uses (descriptions, outputs, parameters) to steer agent behavior.",
				Criterion:    "Guardrail tool-poisoning detection and a red-team tool-poisoning battery.",
				Capabilities: []CapabilityKey{"threat_detection", "adversarial_testing"},
			},
		},
	},
	{
		ID:      "nist_cosais",
		Name:    "NIST SP 800-53 Control Overlays for Securing AI Systems (COSAiS) — design-toward",
		Version: "NIST COSAiS — IN DEVELOPMENT (concept paper 2025-08, annotated outline 2026-01)",
		Pin: FrameworkPin{
			Document:   "NIST SP 800-53 Control Overlays for Securing AI Systems (COSAiS)",
			SourceURL:  "https://csrc.nist.gov/projects/cosais",
			VerifiedOn: "2026-06-10",
			Status:     PinInDevelopment,
		},
		Authority:  "NIST (csrc.nist.gov/Projects/cosais); references the OpenID AI Identity Management (AIIM) Community Group",
		Disclaimer: "Crosswalk to NIST SP 800-53 Control Overlays for Securing AI Systems (COSAiS), which is IN DEVELOPMENT and NOT a final standard (concept paper Aug 2025, annotated outline Jan 2026). Also references the OpenID AIIM Community Group, which by OIDF policy produces no specifications. This entry is explicitly design-toward / in development — NO conformance claim.",
		Controls: []Control{
			{ID: "least_privilege_tool_access", Title: "Least-privilege tool access (overlay)", Requirement: "Constrain and monitor an agent's tool/resource access to least privilege.", Criterion: "Observed access (R/RW) with permitted-vs-observed least-privilege drift.", Capabilities: []CapabilityKey{"least_privilege_drift", "access_observability"}},
			{ID: "chain_of_custody", Title: "Chain of custody / traceability (overlay)", Requirement: "Maintain an attributable, tamper-evident record of agent actions.", Criterion: "Append-only, hash-chained, integrity-verified audit ledger (immutable by construction).", Capabilities: []CapabilityKey{"audit_trail", "audit_integrity", "audit_immutability"}},
			{ID: "agent_containment", Title: "Agent containment (overlay)", Requirement: "Contain agent behavior with detective guardrails and human gates.", Criterion: "Guardrail threat detection and deny-by-default human-in-the-loop gates.", Capabilities: []CapabilityKey{"threat_detection", "human_oversight"}},
			{ID: "agent_identity_management", Title: "Agent identity management (overlay / AIIM)", Requirement: "Manage agent identities and authorization (the OpenID AIIM problem space).", Criterion: "Governed non-human identities and RBAC.", Capabilities: []CapabilityKey{"identity_governance", "access_control_rbac"}, Note: "Tracks the OpenID AIIM Community Group's problem space; AIIM is a community group (no specifications) — design-toward, no conformance claim."},
		},
	},

	// ──: US state AI law frameworks ──────────────────────────────────────
	//
	// Each US state AI law is its own framework with controls mapping that law's
	// specific obligations to platform capabilities. The NIST AI RMF crosswalk
	// (the affirmative-defense bridge where the statute provides one) is cited in
	// each control's Note, NOT as a separate framework (nist_ai_rmf is already in
	// the catalog).

	{
		ID:      "tx_traiga",
		Name:    "Texas Responsible AI Governance Act (TRAIGA)",
		Version: "89(R) HB 149 (2025, signed 2025-06-22)",
		Pin: FrameworkPin{
			Document:    "Texas HB 149 (TRAIGA), 89th Legislature, Regular Session",
			PublishedOn: "2025-06-22",
			SourceURL:   "https://capitol.texas.gov/BillLookup/History.aspx?LegSess=89R&Bill=HB149",
			VerifiedOn:  "2026-06-28",
			Status:      PinInForce,
		},
		Authority: "State of Texas (Attorney General " +
			"exclusive enforcement; no private right " +
			"of action)",
		Disclaimer: "Technical control mapping for an AI " +
			"control plane only; not legal advice and " +
			"not a certification of compliance with " +
			"Texas TRAIGA (HB 149). The affirmative " +
			"defense (§552.105(e)) enables the " +
			"internal-review discovery pathway for " +
			"those substantially complying with NIST " +
			"AI 600-1 or another recognized framework " +
			"— this mapping evidences platform " +
			"support, not the defense itself. " +
			"The enacted law is HB 149 " +
			"(not HB 1709, which did not advance).",
		Controls: []Control{
			{
				ID:    "sec_552_051_disclosure",
				Title: "Government/healthcare AI disclosure",
				Requirement: "§552.051: Government agencies must " +
					"disclose AI interaction to consumers " +
					"before or at time of interaction; " +
					"healthcare providers must disclose AI " +
					"use in treatment before service begins " +
					"(emergency exception).",
				Criterion: "A maintained system/agent inventory " +
					"(transparency record) with per-agent " +
					"risk classifications identifying " +
					"AI-interacting systems.",
				Capabilities: []CapabilityKey{
					"transparency_record",
					"risk_classification",
				},
				Note: "HB 149 disclosure obligations apply " +
					"to government agencies and healthcare " +
					"providers, NOT private-sector " +
					"deployers generally.",
				MilestoneIDs: []string{
					"tx_traiga.effective",
				},
			},
			{
				ID:    "sec_552_052_manipulation",
				Title: "Prohibition on behavioral manipulation",
				Requirement: "§552.052: prohibits AI use to " +
					"manipulate persons into self-harm, " +
					"harm to others, or criminal activity.",
				Criterion: "Guardrail threat detection over " +
					"agent surfaces flags manipulative " +
					"or harmful content for human review.",
				Capabilities: []CapabilityKey{
					"threat_detection",
				},
				MilestoneIDs: []string{
					"tx_traiga.effective",
				},
			},
			{
				ID:    "sec_552_105_affirmative_defense",
				Title: "NIST AI 600-1 affirmative defense",
				Requirement: "§552.105(e)(2)(D): a defendant " +
					"who substantially complies with " +
					"NIST AI 600-1 (Generative AI " +
					"Profile) or another nationally/ " +
					"internationally recognized risk " +
					"management framework may use an " +
					"internal review process as a " +
					"discovery pathway for the " +
					"affirmative defense.",
				Criterion: "Adversarial/red-team robustness " +
					"testing (§552.105(e)(2)(B)), " +
					"quality evaluation, and " +
					"per-agent risk classifications " +
					"aligned to NIST AI 600-1.",
				Capabilities: []CapabilityKey{
					"adversarial_testing",
					"quality_evaluation",
					"risk_classification",
				},
				Note: "The statute specifically names " +
					"NIST AI 600-1 (GenAI Profile, " +
					"nist_ai_600_1 in this catalog), " +
					"not the base NIST AI RMF 100-1. " +
					"The defense is a prerequisite for " +
					"the internal-review discovery " +
					"pathway, not a blanket safe harbor.",
				MilestoneIDs: []string{
					"tx_traiga.effective",
				},
			},
			{
				ID:    "sec_552_105_cure",
				Title: "60-day cure period",
				Requirement: "§552.104: AG must provide " +
					"60-day notice and opportunity to " +
					"cure before civil action for " +
					"curable violations.",
				Criterion: "Governed change/deployment records " +
					"and an audit trail documenting the " +
					"cure actions.",
				Capabilities: []CapabilityKey{
					"change_management",
					"audit_trail",
				},
				MilestoneIDs: []string{
					"tx_traiga.effective",
				},
			},
		},
	},
	{
		ID:      "ca_sb53",
		Name:    "California Frontier AI Safety Act (SB 53, TFAIA)",
		Version: "SB 53, Chapter 138 (2025, signed 2025-09-29)",
		Pin: FrameworkPin{
			Document:    "California SB 53, Chapter 138 (Statutes of 2025)",
			PublishedOn: "2025-09-29",
			SourceURL:   "https://leginfo.legislature.ca.gov/faces/billTextClient.xhtml?bill_id=202520260SB53",
			VerifiedOn:  "2026-06-28",
			Status:      PinInForce,
		},
		Authority: "State of California (AG exclusive " +
			"enforcement; up to $1M per violation; " +
			"no private right of action for main " +
			"provisions; whistleblower PRA for " +
			"retaliation under Labor Code §1107)",
		Disclaimer: "Technical control mapping for an AI " +
			"control plane only; not legal advice and " +
			"not a certification of compliance with " +
			"California SB 53. SB 53 " +
			"applies to 'large frontier developers' " +
			"(>$500M revenue + frontier models >10^26 " +
			"FLOP) — applicability depends on the " +
			"operator's status. No NIST AI RMF " +
			"affirmative defense exists in this law.",
		Controls: []Control{
			{
				ID:    "sec_22757_12a_framework",
				Title: "Frontier AI safety framework",
				Requirement: "§22757.12(a): large frontier " +
					"developers must publish a framework " +
					"addressing 10 enumerated approaches " +
					"(safety testing, third-party " +
					"evaluators, cybersecurity, " +
					"governance, etc.).",
				Criterion: "Adversarial/red-team robustness " +
					"testing results, quality evaluation " +
					"and threat-detection findings " +
					"evidence pre-deployment safety " +
					"testing.",
				Capabilities: []CapabilityKey{
					"adversarial_testing",
					"quality_evaluation",
					"threat_detection",
				},
				MilestoneIDs: []string{
					"ca_sb53.effective",
				},
			},
			{
				ID:    "sec_22757_12c_transparency",
				Title: "Pre-deployment transparency report",
				Requirement: "§22757.12(c): transparency reports " +
					"must be published before deploying " +
					"new or materially modified frontier " +
					"models.",
				Criterion: "A maintained system/agent inventory " +
					"and governed change/deployment " +
					"records.",
				Capabilities: []CapabilityKey{
					"transparency_record",
					"change_management",
				},
				MilestoneIDs: []string{
					"ca_sb53.effective",
				},
			},
			{
				ID:    "sec_22757_13_incidents",
				Title: "Incident reporting",
				Requirement: "§22757.13(c): safety incidents " +
					"must be reported within 15 days " +
					"(24 hours if imminent risk of " +
					"death or serious injury).",
				Criterion: "Threat-detection findings, a " +
					"reconstructable forensic timeline " +
					"and an integrity-verified audit " +
					"trail support incident " +
					"identification and reporting.",
				Capabilities: []CapabilityKey{
					"threat_detection",
					"forensic_capability",
					"audit_trail",
					"audit_integrity",
				},
				MilestoneIDs: []string{
					"ca_sb53.effective",
				},
			},
			{
				ID:    "sec_22757_12f_recordkeeping",
				Title: "5-year recordkeeping",
				Requirement: "§22757.12(f): 5-year retention " +
					"of unredacted documents related " +
					"to the frontier AI framework.",
				Criterion: "An append-only audit ledger with " +
					"continuous WORM/SIEM export and " +
					"governed change records.",
				Capabilities: []CapabilityKey{
					"audit_trail",
					"audit_export",
					"audit_immutability",
					"change_management",
				},
				MilestoneIDs: []string{
					"ca_sb53.effective",
				},
			},
			{
				ID:    "sec_22757_12a_security",
				Title: "Cybersecurity protections",
				Requirement: "§22757.12(a): the published " +
					"framework must address " +
					"cybersecurity protections for " +
					"frontier models.",
				Criterion: "Secure defaults, supply-chain " +
					"integrity, encryption in transit " +
					"and RBAC + fail-closed isolation.",
				Capabilities: []CapabilityKey{
					"secure_defaults",
					"supply_chain",
					"encryption_transit",
					"access_control_rbac",
				},
				MilestoneIDs: []string{
					"ca_sb53.effective",
				},
			},
		},
	},
	{
		ID:      "il_hb3773",
		Name:    "Illinois HB 3773 — AI in Employment (IHRA amendment)",
		Version: "HB 3773, Public Act 103-0804 (103rd GA, signed 2024-08-09)",
		Pin: FrameworkPin{
			Document:    "Illinois HB 3773 (Public Act 103-0804), amending 775 ILCS 5/ (Illinois Human Rights Act)",
			PublishedOn: "2024-08-09",
			SourceURL:   "https://www.ilga.gov/legislation/publicacts/fulltext.asp?Name=103-0804",
			VerifiedOn:  "2026-06-28",
			Status:      PinInForce,
		},
		Authority: "Illinois Department of Human Rights " +
			"(IDHR); AG for pattern-or-practice; " +
			"private right of action via IHRA framework",
		Disclaimer: "Technical control mapping for an AI " +
			"control plane only; not legal advice " +
			"and not a certification of compliance " +
			"with Illinois HB 3773. " +
			"HB 3773 is an employment " +
			"anti-discrimination amendment to the " +
			"IHRA, NOT a broad AI governance law; " +
			"it prohibits AI use with disparate " +
			"impact on protected classes in " +
			"employment decisions. No NIST AI RMF " +
			"affirmative defense exists. IDHR draft " +
			"rules (Subpart J) were withdrawn " +
			"2026-06-02; statutory obligations " +
			"(notice + anti-discrimination) apply " +
			"regardless.",
		Controls: []Control{
			{
				ID:    "sec_2_102_notice",
				Title: "Employee notice of AI use",
				Requirement: "§2-102(L): employers must notify " +
					"employees of AI use in employment " +
					"decisions (details delegated to " +
					"IDHR rulemaking, which was " +
					"withdrawn — see linked watchlist).",
				Criterion: "A maintained system/agent " +
					"inventory (transparency record) " +
					"identifying AI systems used in " +
					"employment decisions.",
				Capabilities: []CapabilityKey{
					"transparency_record",
				},
				Note: "IDHR rulemaking on notice details " +
					"was withdrawn; the statutory " +
					"notice duty applies but procedural " +
					"specifics are pending — see the " +
					"il_hb3773_rulemaking watchlist item.",
				MilestoneIDs: []string{
					"il_hb3773.effective",
				},
			},
			{
				ID:    "sec_2_102_discrimination",
				Title: "AI disparate-impact prohibition",
				Requirement: "§2-102(A): prohibits AI use that " +
					"has disparate impact on protected " +
					"classes in employment (recruitment, " +
					"hiring, promotion, discharge, " +
					"discipline, tenure, terms); also " +
					"prohibits zip-code proxies.",
				Criterion: "Per-agent risk classifications " +
					"identifying AI systems used in " +
					"employment decisions, plus an " +
					"audit trail recording the " +
					"decisions and their outcomes.",
				Capabilities: []CapabilityKey{
					"risk_classification",
					"audit_trail",
				},
				Note: "Strict liability: discriminatory " +
					"EFFECT suffices; intent is NOT " +
					"required. Bias detection/ " +
					"measurement in model outputs is " +
					"NOT evidenced by the control " +
					"plane (honest gap for the " +
					"fairness dimension).",
				MilestoneIDs: []string{
					"il_hb3773.effective",
				},
			},
			{
				ID:    "audit_evidence",
				Title: "Audit trail for AI employment decisions",
				Requirement: "Supporting evidence for IDHR " +
					"complaint-driven investigations " +
					"and AG pattern-or-practice " +
					"enforcement: records of AI system " +
					"inputs, outputs and decisions.",
				Criterion: "An append-only, hash-chained " +
					"audit ledger with verified " +
					"integrity and continuous WORM/SIEM " +
					"export preserving decision records.",
				Capabilities: []CapabilityKey{
					"audit_trail",
					"audit_integrity",
					"audit_export",
				},
				MilestoneIDs: []string{
					"il_hb3773.effective",
				},
			},
		},
	},
	{
		ID:      "co_sb26_189",
		Name:    "Colorado ADMT Framework (SB 26-189)",
		Version: "SB 26-189 (2026, signed 2026-05-14)",
		Pin: FrameworkPin{
			Document:    "Colorado SB 26-189 (Automated Decision-Making Technology)",
			PublishedOn: "2026-05-14",
			SourceURL:   "https://leg.colorado.gov/bills/sb26-189",
			VerifiedOn:  "2026-06-28",
			Status:      PinInForce,
		},
		Authority: "State of Colorado (Attorney General " +
			"enforcement, AG rulemaking authority)",
		Disclaimer: "Technical control mapping for an AI " +
			"control plane only; not legal advice " +
			"and not a certification of compliance " +
			"with Colorado SB 26-189. " +
			"SB 26-189 is a transparency/disclosure " +
			"model (NOT a duty-of-care/risk-based " +
			"model like its predecessor SB 24-205). " +
			"The NIST AI RMF affirmative defense " +
			"from SB 24-205 was ELIMINATED. AG " +
			"rulemaking is due by 2027-01-01. " +
			"Litigation (xAI v. Colorado) may delay " +
			"enforcement.",
		Controls: []Control{
			{
				ID:    "developer_documentation",
				Title: "Developer documentation to deployers",
				Requirement: "Developers must provide deployers " +
					"with technical documentation: system " +
					"capabilities, limitations, intended " +
					"uses, risk assessment, data practices " +
					"and performance metrics.",
				Criterion: "A maintained system/agent inventory " +
					"(transparency record), change/deployment " +
					"records and per-inference resource " +
					"accounting.",
				Capabilities: []CapabilityKey{
					"transparency_record",
					"change_management",
					"resource_accounting",
				},
				MilestoneIDs: []string{
					"colorado_admt.obligations_apply",
				},
			},
			{
				ID:    "deployer_disclosure",
				Title: "Deployer disclosure to consumers",
				Requirement: "Deployers must disclose to " +
					"consumers that ADMT is being used " +
					"in consequential decisions and " +
					"provide information about how to " +
					"contest adverse outcomes.",
				Criterion: "A maintained system/agent inventory " +
					"with per-agent risk classifications.",
				Capabilities: []CapabilityKey{
					"transparency_record",
					"risk_classification",
				},
				MilestoneIDs: []string{
					"colorado_admt.obligations_apply",
				},
			},
			{
				ID:    "impact_assessment",
				Title: "Impact assessment for ADMT",
				Requirement: "Deployers must conduct impact " +
					"assessments for ADMT used in " +
					"consequential decisions.",
				Criterion: "Per-agent risk classifications " +
					"with documented rationale, plus " +
					"the compliance module's structured " +
					"gap analysis.",
				Capabilities: []CapabilityKey{
					"risk_classification",
					"audit_trail",
				},
				MilestoneIDs: []string{
					"colorado_admt.obligations_apply",
				},
			},
			{
				ID:    "risk_management",
				Title: "Risk management program",
				Requirement: "Developers and deployers must " +
					"implement a risk management " +
					"program governing ADMT.",
				Criterion: "Risk classifications, adversarial " +
					"testing, quality evaluation and " +
					"threat detection evidence a " +
					"risk-management program.",
				Capabilities: []CapabilityKey{
					"risk_classification",
					"adversarial_testing",
					"quality_evaluation",
					"threat_detection",
				},
				MilestoneIDs: []string{
					"colorado_admt.obligations_apply",
				},
			},
			{
				ID:    "recordkeeping",
				Title: "Recordkeeping",
				Requirement: "Developers and deployers must " +
					"maintain records of ADMT operation, " +
					"risk management and disclosures.",
				Criterion: "An append-only audit ledger with " +
					"integrity verification and continuous " +
					"WORM/SIEM export.",
				Capabilities: []CapabilityKey{
					"audit_trail",
					"audit_integrity",
					"audit_export",
				},
				MilestoneIDs: []string{
					"colorado_admt.obligations_apply",
				},
			},
		},
	},

	// ──: sector-overlay frameworks ───────────────────────────────────────

	{
		ID:      "hipaa_clinical_ai",
		Name:    "HIPAA Clinical AI Overlay",
		Version: "HIPAA (45 CFR Parts 160, 164) + HHS AI guidance (2024-2025)",
		Pin: FrameworkPin{
			Document:    "HIPAA Privacy/Security Rules (45 CFR Parts 160, 164) + HHS OCR AI Guidance",
			PublishedOn: "1996-08-21",
			SourceURL:   "https://www.hhs.gov/hipaa/for-professionals/index.html",
			VerifiedOn:  "2026-06-28",
			Status:      PinInForce,
		},
		Authority: "U.S. Department of Health and Human " +
			"Services (HHS), Office for Civil Rights (OCR)",
		Disclaimer: "Sector overlay mapping control-plane " +
			"capabilities to HIPAA requirements as " +
			"applied to clinical AI systems processing " +
			"protected health information (PHI); not " +
			"legal advice and not a certification of " +
			"HIPAA compliance. HIPAA " +
			"compliance requires organizational, " +
			"administrative and physical safeguards " +
			"beyond the control plane's scope.",
		Controls: []Control{
			{
				ID:    "phi_minimization",
				Title: "PHI minimization in AI systems",
				Requirement: "HIPAA Minimum Necessary (45 CFR " +
					"§164.502(b)): use or disclose only " +
					"the minimum necessary PHI for the " +
					"AI system's intended purpose.",
				Criterion: "Data minimization by construction " +
					"(only relations/metadata persisted) " +
					"and deterministic PII/sensitivity " +
					"discovery scans labeling governed " +
					"content.",
				Capabilities: []CapabilityKey{
					"data_minimization",
					"pii_discovery",
				},
			},
			{
				ID:    "phi_access_control",
				Title: "Access controls for PHI in AI",
				Requirement: "HIPAA Security Rule (45 CFR " +
					"§164.312(a)): implement technical " +
					"access controls to allow only " +
					"authorized persons/systems to " +
					"access ePHI.",
				Criterion: "Engine-enforced RBAC with " +
					"fail-closed multi-tenant isolation, " +
					"governed non-human identities and " +
					"observed access edges.",
				Capabilities: []CapabilityKey{
					"access_control_rbac",
					"identity_governance",
					"access_observability",
				},
			},
			{
				ID:    "phi_audit_controls",
				Title: "Audit controls for AI/PHI access",
				Requirement: "HIPAA Security Rule (45 CFR " +
					"§164.312(b)): implement mechanisms " +
					"to record and examine activity in " +
					"information systems that contain or " +
					"use ePHI.",
				Criterion: "An append-only, hash-chained " +
					"audit ledger with verified integrity " +
					"and continuous WORM/SIEM export.",
				Capabilities: []CapabilityKey{
					"audit_trail",
					"audit_integrity",
					"audit_export",
				},
			},
			{
				ID:    "phi_transmission_security",
				Title: "Transmission security for AI/PHI",
				Requirement: "HIPAA Security Rule (45 CFR " +
					"§164.312(e)): implement technical " +
					"security measures to guard against " +
					"unauthorized access to ePHI " +
					"transmitted electronically.",
				Criterion: "TLS by default on every channel " +
					"the plane controls.",
				Capabilities: []CapabilityKey{
					"encryption_transit",
				},
			},
			{
				ID:    "phi_risk_analysis",
				Title: "Risk analysis for AI systems",
				Requirement: "HIPAA Security Rule (45 CFR " +
					"§164.308(a)(1)(ii)(A)): conduct an " +
					"accurate and thorough assessment of " +
					"risks to ePHI, including risks from " +
					"AI systems processing PHI.",
				Criterion: "Per-agent risk classifications " +
					"and continuous threat detection " +
					"evidence a risk-analysis posture.",
				Capabilities: []CapabilityKey{
					"risk_classification",
					"threat_detection",
				},
			},
			{
				ID:    "phi_breach_notification",
				Title: "Breach notification for AI incidents",
				Requirement: "HIPAA Breach Notification Rule " +
					"(45 CFR §§164.400-414): notify " +
					"affected individuals and HHS of " +
					"breaches of unsecured PHI, " +
					"including AI-related breaches.",
				Criterion: "Threat-detection findings, a " +
					"reconstructable forensic timeline " +
					"and an integrity-verified audit " +
					"trail support breach assessment " +
					"and the required documentation.",
				Capabilities: []CapabilityKey{
					"threat_detection",
					"forensic_capability",
					"audit_trail",
					"audit_integrity",
				},
			},
			{
				ID:    "dlp_phi",
				Title: "DLP for PHI in AI contexts",
				Requirement: "Prevent unauthorized disclosure " +
					"of PHI through AI system outputs, " +
					"including data leakage prevention " +
					"in retrieval and generation.",
				Criterion: "A deny-closed DLP egress gate " +
					"keyed on sensitivity classes " +
					"restricts classified content " +
					"from leaving the perimeter.",
				Capabilities: []CapabilityKey{
					"dlp_enforcement",
				},
			},
		},
	},
	{
		ID:      "pci_dss_401_ai",
		Name:    "PCI DSS 4.0.1 — AI in Cardholder Data Environments",
		Version: "PCI DSS v4.0.1 (June 2024)",
		Pin: FrameworkPin{
			Document:    "PCI DSS v4.0.1",
			PublishedOn: "2024-06",
			SourceURL:   "https://www.pcisecuritystandards.org/document_library/?document=pci_dss",
			VerifiedOn:  "2026-07-05",
			Status:      PinFinal,
		},
		Authority: "PCI Security Standards Council (PCI SSC)",
		Disclaimer: "Sector overlay mapping control-plane " +
			"capabilities to PCI DSS v4.0.1 requirements " +
			"as applied to AI systems operating within or " +
			"connected to cardholder data environments " +
			"(CDEs); not legal advice and not a PCI " +
			"compliance certification or Attestation of " +
			"Compliance (AoC). PCI " +
			"compliance requires a Qualified Security " +
			"Assessor (QSA) or Internal Security Assessor " +
			"(ISA) validation. The voice-agent mapping " +
			"follows the PCI SSC information supplement " +
			"Protecting Telephone-Based Payment Card Data, " +
			"which prefers DTMF masking over pause-and-resume " +
			"recording control. The PCI SSC has published no " +
			"AI-agent-specific guidance as of the pin's " +
			"verification date; AI-specific interpretations " +
			"here are the vendor's, not the Council's.",
		Controls: []Control{
			{
				ID:    "req_3_3_1_sad_voice",
				Title: "Req 3.3.1: SAD in voice artifacts",
				Requirement: "PCI DSS Req 3.3.1: sensitive " +
					"authentication data, including full " +
					"track data, card verification codes " +
					"and PINs, is not retained after " +
					"authorization, even when encrypted; " +
					"for voice agents this includes " +
					"recordings and transcripts that " +
					"capture spoken or DTMF card data.",
				Criterion: "The control plane evidences governed " +
					"call admission, recording posture " +
					"declarations for DTMF masking or " +
					"pause-and-resume controls with SAD-risk " +
					"findings when recording is active " +
					"without them, and in-memory transcript " +
					"DLP that never persists transcript text.",
				Capabilities: []CapabilityKey{
					"voice_call_governance",
					"voice_transcript_dlp",
					"dlp_enforcement",
					"data_minimization",
				},
				Note: "The recording store itself lives in " +
					"the operator's telephony estate; the " +
					"control plane evidences the governance " +
					"and detection layer, not the recording " +
					"repository. Per the telephone-payment " +
					"supplement, DTMF masking is the preferred " +
					"control and pause-and-resume is the " +
					"weaker fallback.",
			},
			{
				ID:    "req_4_2_1_voice_transmission",
				Title: "Req 4.2.1: Voice transmission protection",
				Requirement: "PCI DSS Req 4.2.1: PAN is " +
					"protected with strong cryptography " +
					"during transmission over open, public " +
					"networks, including voice media and " +
					"signaling paths.",
				Criterion: "Explicit gap: the control plane does " +
					"not terminate or transport call media " +
					"and cannot prove media-path cryptography " +
					"for voice streams.",
				Capabilities: nil,
				Note: "The governed call plane pins TLS-only " +
					"provider endpoints for SIP over TLS and " +
					"wss sideband control, but media-path " +
					"encryption is the telephony provider's " +
					"and operator's surface.",
			},
			{
				ID:    "req_6_4_3_payment_scripts",
				Title: "Req 6.4.3: Payment-page script governance",
				Requirement: "PCI DSS Req 6.4.3: payment-page " +
					"scripts loaded and executed in the " +
					"consumer browser are inventoried, " +
					"authorized and integrity-assured, " +
					"including when AI agents render or " +
					"manipulate payment pages.",
				Criterion: "Explicit gap: the control plane does " +
					"not inventory, authorize or integrity-check " +
					"browser payment-page scripts or AI-driven " +
					"script changes.",
				Capabilities: nil,
				Note: "Existing change-management and supply-chain " +
					"signals cover governed platform artifacts, " +
					"not runtime browser scripts on payment " +
					"pages or agent-driven DOM/script changes.",
				MilestoneIDs: []string{
					"pci_dss_401.future_dated",
				},
			},
			{
				ID:    "req_11_6_1_payment_tamper",
				Title: "Req 11.6.1: Payment-page tamper detection",
				Requirement: "PCI DSS Req 11.6.1: change- and " +
					"tamper-detection mechanisms monitor " +
					"payment-page HTTP headers and script " +
					"content at the documented cadence.",
				Criterion: "Explicit gap: the control plane does " +
					"not monitor payment-page HTTP headers " +
					"or browser script content for change " +
					"and tamper detection.",
				Capabilities: nil,
				Note: "The AI-agent angle is in scope when an " +
					"agent can render, modify or operate a " +
					"payment page, but the catalog has no " +
					"browser-page tamper-monitoring signal.",
				MilestoneIDs: []string{
					"pci_dss_401.future_dated",
				},
			},
			{
				ID:    "req_12_3_1_targeted_risk",
				Title: "Req 12.3.1: Targeted risk analysis",
				Requirement: "PCI DSS Req 12.3.1: a documented " +
					"targeted risk analysis is performed " +
					"for each requirement met at a flexible " +
					"frequency.",
				Criterion: "Per-agent risk classifications, the " +
					"maintained system/agent inventory and " +
					"sealed evidence packages supply the " +
					"inputs a targeted risk analysis documents.",
				Capabilities: []CapabilityKey{
					"risk_classification",
					"transparency_record",
					"audit_export",
				},
				Note: "The targeted risk analysis itself is the " +
					"entity's organizational artifact.",
				MilestoneIDs: []string{
					"pci_dss_401.future_dated",
				},
			},
			{
				ID:    "req_6_ai_dev",
				Title: "Req 6: Secure AI system development",
				Requirement: "PCI DSS Req 6: develop and maintain " +
					"secure systems and software, " +
					"including AI/ML systems in the CDE.",
				Criterion: "Governed change/deployment records, " +
					"supply-chain integrity (signed " +
					"releases + SBOM + pinned deps) and " +
					"secure-by-default configuration.",
				Capabilities: []CapabilityKey{
					"change_management",
					"supply_chain",
					"secure_defaults",
				},
				MilestoneIDs: []string{
					"pci_dss_401.future_dated",
				},
			},
			{
				ID:    "req_7_ai_access",
				Title: "Req 7: AI access control in CDE",
				Requirement: "PCI DSS Req 7: restrict access to " +
					"system components and cardholder " +
					"data by business need to know, " +
					"including AI system access.",
				Criterion: "Engine-enforced RBAC, governed " +
					"non-human identities and " +
					"permitted-vs-observed " +
					"least-privilege drift.",
				Capabilities: []CapabilityKey{
					"access_control_rbac",
					"identity_governance",
					"least_privilege_drift",
				},
				MilestoneIDs: []string{
					"pci_dss_401.future_dated",
				},
			},
			{
				ID:    "req_10_ai_logging",
				Title: "Req 10: AI system activity logging",
				Requirement: "PCI DSS Req 10: log and monitor " +
					"all access to system components " +
					"and cardholder data, including " +
					"AI system operations.",
				Criterion: "An append-only, hash-chained audit " +
					"ledger with verified integrity, " +
					"continuous WORM/SIEM export and " +
					"recorded access edges.",
				Capabilities: []CapabilityKey{
					"audit_trail",
					"audit_integrity",
					"audit_export",
					"access_observability",
				},
				MilestoneIDs: []string{
					"pci_dss_401.future_dated",
				},
			},
			{
				ID:    "req_11_ai_testing",
				Title: "Req 11: AI security testing",
				Requirement: "PCI DSS Req 11: regularly test " +
					"security systems and processes, " +
					"including AI/ML system security.",
				Criterion: "Adversarial/red-team robustness " +
					"testing and quality-evaluation " +
					"findings exist as auditable " +
					"security-testing evidence.",
				Capabilities: []CapabilityKey{
					"adversarial_testing",
					"quality_evaluation",
				},
				MilestoneIDs: []string{
					"pci_dss_401.future_dated",
				},
			},
			{
				ID:    "req_12_ai_risk",
				Title: "Req 12: AI risk assessment",
				Requirement: "PCI DSS Req 12: maintain a policy " +
					"that addresses information security " +
					"for all personnel, including risk " +
					"assessment for AI systems in the CDE.",
				Criterion: "Per-agent risk classifications and " +
					"a maintained system/agent inventory.",
				Capabilities: []CapabilityKey{
					"risk_classification",
					"transparency_record",
				},
				MilestoneIDs: []string{
					"pci_dss_401.future_dated",
				},
			},
			{
				ID:    "req_3_ai_data",
				Title: "Req 3: AI data protection in CDE",
				Requirement: "PCI DSS Req 3: protect stored " +
					"account data, including data used " +
					"by AI/ML systems in the CDE.",
				Criterion: "Data minimization by construction, " +
					"data-lineage within the perimeter, " +
					"a residency attestation, a DLP " +
					"egress gate and encryption " +
					"attestation.",
				Capabilities: []CapabilityKey{
					"data_minimization",
					"data_lineage",
					"data_residency",
					"dlp_enforcement",
					"encryption_at_rest",
				},
				MilestoneIDs: []string{
					"pci_dss_401.future_dated",
				},
			},
		},
	},
	{
		ID:      "finra_genai",
		Name:    "FINRA GenAI Supervision & Recordkeeping",
		Version: "FINRA Regulatory Notice 24-09 + existing rules",
		Pin: FrameworkPin{
			Document:    "FINRA Regulatory Notice 24-09 (AI Governance)",
			PublishedOn: "2024-06-27",
			SourceURL:   "https://www.finra.org/rules-guidance/notices/24-09",
			VerifiedOn:  "2026-06-28",
			Status:      PinGuidance,
		},
		Authority: "Financial Industry Regulatory Authority " +
			"(FINRA)",
		Disclaimer: "Sector overlay mapping control-plane " +
			"capabilities to FINRA's AI supervision " +
			"expectations (Regulatory Notice 24-09) and " +
			"existing rules (Rule 3110, Rule 2210, " +
			"Rule 4511, SEA Rule 17a-4) as applied to " +
			"GenAI use by member firms; not legal advice " +
			"and not a certification of FINRA compliance.",
		Controls: []Control{
			{
				ID:    "rule_3110_supervision",
				Title: "Rule 3110: AI supervision system",
				Requirement: "FINRA Rule 3110: member firms must " +
					"establish and maintain a system to " +
					"supervise the activities of each " +
					"associated person, including " +
					"supervision of AI/GenAI tools used " +
					"in business activities.",
				Criterion: "HITL/approval gates with " +
					"deny-by-default, observed access " +
					"edges, governed identities and " +
					"least-privilege drift provide " +
					"the supervision substrate.",
				Capabilities: []CapabilityKey{
					"human_oversight",
					"access_observability",
					"identity_governance",
					"least_privilege_drift",
				},
				MilestoneIDs: []string{
					"finra_genai.notice_25_06",
				},
			},
			{
				ID:    "rule_2210_communications",
				Title: "Rule 2210: AI-generated communications",
				Requirement: "FINRA Rule 2210: communications " +
					"with the public (including " +
					"AI-generated content) must be fair, " +
					"balanced and not misleading; " +
					"supervision and review obligations " +
					"apply.",
				Criterion: "Guardrail threat detection over " +
					"AI-generated content and quality " +
					"evaluation results.",
				Capabilities: []CapabilityKey{
					"threat_detection",
					"quality_evaluation",
				},
				Note: "The control plane detects guardrail " +
					"violations and evaluates quality; " +
					"the substantive fair-and-balanced " +
					"determination for securities " +
					"communications is a human/compliance " +
					"duty.",
				MilestoneIDs: []string{
					"finra_genai.notice_25_06",
				},
			},
			{
				ID:    "rule_4511_recordkeeping",
				Title: "Rule 4511 / SEA 17a-4: AI recordkeeping",
				Requirement: "FINRA Rule 4511 and SEA Rule 17a-4: " +
					"member firms must make and preserve " +
					"books and records, including records " +
					"of AI system inputs, outputs and " +
					"decisions, in a non-rewriteable, " +
					"non-erasable (WORM) format.",
				Criterion: "An append-only, hash-chained, " +
					"integrity-verified audit ledger " +
					"(immutable by construction) with " +
					"continuous WORM/SIEM export — the " +
					"WORM requirement is satisfied by " +
					"design.",
				Capabilities: []CapabilityKey{
					"audit_trail",
					"audit_integrity",
					"audit_immutability",
					"audit_export",
				},
				MilestoneIDs: []string{
					"finra_genai.notice_25_06",
				},
			},
			{
				ID:    "model_risk_mgmt",
				Title: "Model risk management for AI",
				Requirement: "FINRA expects member firms to " +
					"apply model risk management " +
					"principles (aligned with SR 11-7 / " +
					"OCC 2011-12) to AI/ML models used " +
					"in business activities.",
				Criterion: "Per-agent risk classifications, " +
					"adversarial testing and quality " +
					"evaluation evidence a model " +
					"risk-management posture.",
				Capabilities: []CapabilityKey{
					"risk_classification",
					"adversarial_testing",
					"quality_evaluation",
				},
				MilestoneIDs: []string{
					"finra_genai.notice_25_06",
				},
			},
			{
				ID:    "data_governance",
				Title: "Data governance for AI systems",
				Requirement: "FINRA expects member firms to " +
					"implement data governance for " +
					"AI/GenAI systems, including data " +
					"quality, lineage and access " +
					"controls.",
				Criterion: "Data-lineage within the perimeter, " +
					"data minimization by construction, " +
					"PII/sensitivity discovery and " +
					"RBAC over data access.",
				Capabilities: []CapabilityKey{
					"data_lineage",
					"data_minimization",
					"pii_discovery",
					"access_control_rbac",
				},
				MilestoneIDs: []string{
					"finra_genai.notice_25_06",
				},
			},
			{
				ID:    "third_party_oversight",
				Title: "Third-party AI vendor oversight",
				Requirement: "FINRA expects member firms to " +
					"conduct due diligence and ongoing " +
					"oversight of third-party AI/GenAI " +
					"vendors.",
				Criterion: "An operator-verified per-provider " +
					"GPAI compliance posture, " +
					"supply-chain integrity and " +
					"governed change records.",
				Capabilities: []CapabilityKey{
					"supplier_gpai_posture",
					"supply_chain",
					"change_management",
				},
				MilestoneIDs: []string{
					"finra_genai.notice_25_06",
				},
			},
		},
	},
	{
		ID:      "ferpa",
		Name:    "FERPA — Education Records in AI",
		Version: "FERPA (20 U.S.C. §1232g; 34 CFR Part 99)",
		Pin: FrameworkPin{
			Document:    "Family Educational Rights and Privacy Act — 20 U.S.C. §1232g; 34 CFR Part 99",
			PublishedOn: "1974-08-21",
			SourceURL:   "https://www.ecfr.gov/current/title-34/part-99",
			VerifiedOn:  "2026-07-12",
			Status:      PinInForce,
		},
		Authority: "U.S. Department of Education — Student " +
			"Privacy Policy Office (SPPO); enforced by " +
			"the Family Policy Compliance Office (FPCO)",
		Disclaimer: "Sector overlay mapping control-plane " +
			"capabilities to FERPA (20 U.S.C. §1232g; " +
			"34 CFR Part 99) as applied to education " +
			"records processed by AI systems; a technical " +
			"crosswalk, not legal advice and not a " +
			"certification of FERPA compliance (docs/08 " +
			"§9). FERPA compliance requires institutional " +
			"policies, annual notification and consent " +
			"processes beyond the control plane's scope.",
		Controls: []Control{
			{
				ID:    "education_records_access",
				Title: "Access controls for education records",
				Requirement: "FERPA (34 CFR §99.31(a)(1)): " +
					"disclose education records only to " +
					"school officials with a legitimate " +
					"educational interest.",
				Criterion: "Engine-enforced RBAC with " +
					"fail-closed multi-tenant isolation, " +
					"governed identities and observed " +
					"access edges.",
				Capabilities: []CapabilityKey{
					"access_control_rbac",
					"identity_governance",
					"access_observability",
				},
			},
			{
				ID:    "directory_information_scoping",
				Title: "Directory-information vs education-record scoping",
				Requirement: "FERPA (34 CFR §99.3, §99.37): " +
					"distinguish directory information " +
					"(disclosable) from protected " +
					"education records (consent-gated) and " +
					"scope access by classification.",
				Criterion: "Deterministic PII/sensitivity " +
					"discovery labels content with " +
					"explainable classes; source scoping " +
					"by class governs which subjects and " +
					"surfaces may reach it.",
				Capabilities: []CapabilityKey{
					"pii_discovery",
					"dlp_enforcement",
				},
			},
			{
				ID:    "consent_gated_disclosure",
				Title: "Consent-gated disclosure of education records",
				Requirement: "FERPA (34 CFR §99.30): obtain " +
					"prior written consent before " +
					"disclosing education records, absent " +
					"a §99.31 exception.",
				Criterion: "A deny-closed DLP egress gate " +
					"keyed on sensitivity classes " +
					"withholds education-record content " +
					"from non-authorized surfaces; " +
					"HITL/approval gates govern exceptions.",
				Capabilities: []CapabilityKey{
					"dlp_enforcement",
					"human_oversight",
				},
			},
			{
				ID:    "disclosure_recordkeeping",
				Title: "Record of disclosures",
				Requirement: "FERPA (34 CFR §99.32): maintain " +
					"a record of each request for and " +
					"each disclosure of education records " +
					"(who received it and the legitimate " +
					"interest).",
				Criterion: "An append-only, hash-chained " +
					"audit ledger with verified integrity " +
					"records access edges and disclosures " +
					"and is continuously exportable.",
				Capabilities: []CapabilityKey{
					"audit_trail",
					"audit_integrity",
					"access_observability",
					"audit_export",
				},
			},
			{
				ID:    "education_records_minimization",
				Title: "Minimization of education-record data",
				Requirement: "Limit the education-record data " +
					"the AI system uses to what its " +
					"purpose requires.",
				Criterion: "Data minimization by " +
					"construction: only relations and " +
					"metadata are persisted, never " +
					"payloads.",
				Capabilities: []CapabilityKey{
					"data_minimization",
				},
			},
			{
				ID:    "education_records_transmission",
				Title: "Transmission security for education records",
				Requirement: "Protect education records in " +
					"transit against unauthorized access.",
				Criterion: "TLS by default on every channel " +
					"the plane controls.",
				Capabilities: []CapabilityKey{
					"encryption_transit",
				},
			},
			{
				ID:    "annual_notification",
				Title: "Annual notification of FERPA rights",
				Requirement: "FERPA (34 CFR §99.7): the " +
					"institution must annually notify " +
					"parents and eligible students of " +
					"their rights under FERPA.",
				Criterion: "An institutional duty (notice, " +
					"consent and complaint processes) " +
					"outside the control plane's technical " +
					"scope — an honest gap, not a mapped " +
					"capability.",
			},
		},
	},
}

// frameworkByID indexes the catalog for O(1) lookup.
var frameworkByID = func() map[string]Framework {
	m := make(map[string]Framework, len(catalog))
	for _, fw := range catalog {
		m[fw.ID] = fw
	}
	return m
}()
