// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

// the REGULATORY CALENDAR as data. A control plane that sells compliance
// evidence cannot cite dead dates or let dates decay silently inside prose: every
// regulatory date the product relies on lives HERE as a datum with its primary
// source and the date this repo last verified it (the §Regulación currency
// audit is the trigger; the bar is "dates an auditor can re-check", not text).
//
// Three honesty tiers (docs/SECURITY-HARDENING.md applied to dates):
//   - STATUS over optimism: a date from a provisional political agreement is labeled
//     provisional_agreement; a formally adopted amending act that still awaits OJ
//     publication is labeled adopted_pending_oj; only published/in-force law is
//     in_force or applies_from. The calendar never pretends a pending amendment
//     already happened, and the in-force date it would replace stays in the
//     calendar until publication.
//   - Every milestone carries Source (primary, canonical URL) + VerifiedOn. A date
//     without a source does not ship (tested).
//
// The catalog links here via Control.MilestoneIDs; nothing in frameworks.go carries
// an application date as prose.

import (
	"net/http"
	"sort"
	"strings"

	"github.com/olivaresai/olivares/core/api"
)

// SourceRef is the primary-source citation a milestone or watchlist item rests on.
type SourceRef struct {
	URL       string `json:"url"`
	Title     string `json:"title"`
	Publisher string `json:"publisher"`
}

// MilestoneStatus labels how solid a calendar date is — the line between law and
// expectation.
type MilestoneStatus string

const (
	// MilestoneInForce: the provision is binding law and its date has passed.
	MilestoneInForce MilestoneStatus = "in_force"
	// MilestoneAppliesFrom: the provision is binding — already-in-force law or a
	// formal program/policy requirement (e.g. a CMVP transition, a CNSSP mandate) —
	// whose application date lies in the future.
	MilestoneAppliesFrom MilestoneStatus = "applies_from"
	// MilestoneProvisional: the date comes from a provisional political agreement on
	// an amending act that is NOT yet adopted or published — an expectation, not law.
	MilestoneProvisional MilestoneStatus = "provisional_agreement"
	// MilestoneAdoptedPendingOJ: the amending act is formally adopted by both
	// co-legislators but awaits OJ publication — it is NOT in-force law; the dates it
	// will replace stay in the calendar until publication.
	MilestoneAdoptedPendingOJ MilestoneStatus = "adopted_pending_oj"
	// MilestonePassed: a past deadline kept for reference only (e.g. a federal memo's
	// implementation date) — nothing future hangs on it.
	MilestonePassed MilestoneStatus = "passed"
)

// RegulatoryMilestone is one dated regulatory fact, as verifiable data.
type RegulatoryMilestone struct {
	ID string `json:"id"`
	// Regime is the human label of the instrument ("EU AI Act", "FIPS 140-2", …).
	Regime string `json:"regime"`
	// FrameworkID links to the catalog entry the milestone governs ("" when the
	// regime has no catalog entry, e.g. DORA, FIPS).
	FrameworkID string `json:"framework_id,omitempty"`
	// Date is the ISO 8601 date the milestone carries.
	Date  string `json:"date"`
	Title string `json:"title"`
	// Effect states what applies or changes on Date.
	Effect     string          `json:"effect"`
	Status     MilestoneStatus `json:"status"`
	Source     SourceRef       `json:"source"`
	VerifiedOn string          `json:"verified_on"`
	Note       string          `json:"note,omitempty"`
}

// WatchlistItem is an undated-but-coming instrument the module tracks as data — a
// CISO sees what is on the horizon without the product pretending it is final.
type WatchlistItem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	FrameworkID string `json:"framework_id,omitempty"`
	// Status is the item's lifecycle ("in_development", "beta", "provisional_agreement",
	// "adopted_pending_oj", "fdis", "pre_1_0", "enacted_future_obligations").
	Status string `json:"status"`
	// Expected is the (free-text but sourced) expectation of what happens next.
	Expected   string    `json:"expected,omitempty"`
	Source     SourceRef `json:"source"`
	VerifiedOn string    `json:"verified_on"`
	Note       string    `json:"note,omitempty"`
}

// Primary sources reused across milestones.
var (
	srcAIActOJ = SourceRef{
		URL:       "https://eur-lex.europa.eu/eli/reg/2024/1689/oj",
		Title:     "Regulation (EU) 2024/1689 (Artificial Intelligence Act), OJ L, 2024/1689, 12.7.2024",
		Publisher: "EUR-Lex (Publications Office of the EU)",
	}
	srcOmnibusCouncilAdoption = SourceRef{
		URL:       "https://www.consilium.europa.eu/en/press/press-releases/2026/06/29/artificial-intelligence-council-gives-final-green-light-to-simplify-and-streamline-rules/",
		Title:     "Artificial Intelligence: Council gives final green light to simplify and streamline rules (29 June 2026)",
		Publisher: "Council of the European Union",
	}
	srcOmnibusEPApproval = SourceRef{
		URL:       "https://www.europarl.europa.eu/news/en/press-room/20260611IPR45207/ai-act-ep-approves-simplification-measures-and-nudifier-app-ban",
		Title:     "AI Act: EP approves simplification measures and 'nudifier' app ban",
		Publisher: "European Parliament",
	}
	srcPLDOJ = SourceRef{
		URL:       "https://eur-lex.europa.eu/eli/dir/2024/2853/oj",
		Title:     "Directive (EU) 2024/2853 on liability for defective products, OJ L, 2024/2853, 18.11.2024",
		Publisher: "EUR-Lex (Publications Office of the EU)",
	}
	srcDORAOJ = SourceRef{
		URL:       "https://eur-lex.europa.eu/legal-content/EN/TXT/?uri=CELEX:32022R2554",
		Title:     "Regulation (EU) 2022/2554 (DORA), OJ L 333, 27.12.2022",
		Publisher: "EUR-Lex (Publications Office of the EU)",
	}
	// the DORA Level-2 instruments the enterprise named-regulation add-on (doraregister)
	// structures to — cited here as verified DATA (the open calendar owns the dates; the
	// enterprise add-on owns the depth). EUR-Lex HTML was JS-gated on the verification date, so
	// the texts were corroborated across ESA artifacts + legal-text mirrors and NOT byte-diffed
	// against the OJ (the export disclaimers say so).
	srcDORARoIITS = SourceRef{
		URL:       "https://eur-lex.europa.eu/eli/reg_impl/2024/2956/oj/eng",
		Title:     "Commission Implementing Regulation (EU) 2024/2956 of 29 November 2024 (ITS: standard templates for the DORA register of information), OJ L, 2.12.2024",
		Publisher: "EUR-Lex (Publications Office of the EU)",
	}
	srcDORAClassRTS = SourceRef{
		URL:       "https://eur-lex.europa.eu/eli/reg_del/2024/1772/oj/eng",
		Title:     "Commission Delegated Regulation (EU) 2024/1772 of 13 March 2024 (RTS: criteria for the classification of ICT-related incidents and cyber threats, materiality thresholds), OJ L, 25.6.2024",
		Publisher: "EUR-Lex (Publications Office of the EU)",
	}
	srcDORAReportRTS = SourceRef{
		URL:       "https://eur-lex.europa.eu/eli/reg_del/2025/301/oj/eng",
		Title:     "Commission Delegated Regulation (EU) 2025/301 of 23 October 2024 (RTS: content and time limits for the initial notification, and intermediate and final reports on major ICT-related incidents), OJ L, 20.2.2025",
		Publisher: "EUR-Lex (Publications Office of the EU)",
	}
)

// regulatoryCalendar is the dated regulatory truth the module exposes. Entries are
// authored regime-grouped and SORTED BY DATE at init (sortMilestonesByDate), so the
// API serves a chronological timeline. Re-baselined for 2026-H2:
// Digital Omnibus deferrals encoded as adopted_pending_oj alongside the in-force
// dates they would replace until OJ publication; AILD recorded as WITHDRAWN (never
// cite it as pending law).
var regulatoryCalendar = sortMilestonesByDate([]RegulatoryMilestone{
	{
		ID: "eu_ai_act.entry_into_force", Regime: "EU AI Act", FrameworkID: "eu_ai_act",
		Date: "2024-08-01", Title: "AI Act enters into force",
		Effect: "Regulation (EU) 2024/1689 in force (20 days after OJ publication of 2024-07-12); application staggered per Art 113.",
		Status: MilestoneInForce, Source: srcAIActOJ, VerifiedOn: "2026-06-10",
	},
	{
		ID: "eu_ai_act.prohibitions_literacy_apply", Regime: "EU AI Act", FrameworkID: "eu_ai_act",
		Date: "2025-02-02", Title: "Prohibitions (Art 5) and AI literacy (Art 4) apply",
		Effect: "Chapters I and II apply (Art 113(a)): unacceptable-risk practices prohibited; AI-literacy duty in effect.",
		Status: MilestoneInForce, Source: srcAIActOJ, VerifiedOn: "2026-06-10",
	},
	{
		ID: "eu_ai_act.gpai_governance_penalties_apply", Regime: "EU AI Act", FrameworkID: "eu_ai_act",
		Date: "2025-08-02", Title: "GPAI obligations, governance and penalties apply",
		Effect: "Chapter V (GPAI), Chapter III Section 4, Chapter VII (governance), Art 78 and Chapter XII (penalties) apply (Art 113(b)) — EXCEPT Art 101 (Commission fines on GPAI providers), which follows on 2026-08-02.",
		Status: MilestoneInForce, Source: srcAIActOJ, VerifiedOn: "2026-06-10",
	},
	{
		ID: "eu_ai_act.art50_transparency_applies", Regime: "EU AI Act", FrameworkID: "eu_ai_act",
		Date: "2026-08-02", Title: "Art 50 transparency obligations apply (general application date)",
		Effect: "General application (Art 113): Art 50 duties bite — disclosing AI interaction to natural persons, machine-readable marking of synthetic content, deepfake disclosure. The adopted Digital Omnibus amending act, pending OJ publication, keeps this date.",
		Status: MilestoneAppliesFrom, Source: srcAIActOJ, VerifiedOn: "2026-07-03",
		Note: "Marking grace for pre-existing generative systems runs to 2026-12-02 under the adopted Digital Omnibus amending act, pending OJ publication (see eu_ai_act.art50_marking_grace_ends, adopted_pending_oj).",
	},
	{
		ID: "eu_ai_act.gpai_enforcement_fines", Regime: "EU AI Act", FrameworkID: "eu_ai_act",
		Date: "2026-08-02", Title: "Commission fining powers over GPAI providers apply (Art 101)",
		Effect: "Art 101 fines (up to 3% worldwide annual turnover or EUR 15M, whichever higher) become applicable — Art 113(b) explicitly excepted Art 101 from the 2025-08-02 GPAI date.",
		Status: MilestoneAppliesFrom, Source: srcAIActOJ, VerifiedOn: "2026-06-10",
	},
	{
		ID: "eu_ai_act.omnibus_adopted", Regime: "EU AI Act (Digital Omnibus)", FrameworkID: "eu_ai_act",
		Date: "2026-06-29", Title: "Digital Omnibus amending act formally adopted by Council",
		Effect: "Council formally adopted the amending act following the EP's first-reading position of 2026-06-16 (423/57/174); it enters into force on the 3rd day after OJ publication; until then the in-force Art 113 dates stand.",
		Status: MilestonePassed, Source: srcOmnibusCouncilAdoption, VerifiedOn: "2026-07-03",
	},
	{
		ID: "eu_ai_act.high_risk_annex3_original", Regime: "EU AI Act", FrameworkID: "eu_ai_act",
		Date: "2026-08-02", Title: "Annex III high-risk obligations apply (IN-FORCE law; deferral adopted, pending OJ)",
		Effect: "Under the in-force Art 113, Chapter III obligations for stand-alone (Annex III) high-risk systems apply from the general application date.",
		Status: MilestoneAppliesFrom, Source: srcAIActOJ, VerifiedOn: "2026-07-03",
		Note: "The Digital Omnibus amending act has been adopted by the EP and Council and defers this to 2027-12-02 (eu_ai_act.high_risk_annex3_omnibus), but awaits OJ publication; this in-force date stands until the amendment is published.",
	},
	{
		ID: "eu_ai_act.high_risk_annex3_omnibus", Regime: "EU AI Act (Digital Omnibus)", FrameworkID: "eu_ai_act",
		Date: "2027-12-02", Title: "Annex III high-risk obligations apply (omnibus, FIXED date)",
		Effect: "Adopted amending act (EP position 2026-06-16; Council adoption 2026-06-29), pending OJ publication: obligations for stand-alone (Annex III) high-risk AI systems apply from a FIXED 2027-12-02 — the co-legislators rejected conditioning application on standards availability.",
		Status: MilestoneAdoptedPendingOJ, Source: srcOmnibusCouncilAdoption, VerifiedOn: "2026-07-03",
		Note: "Adopted amending act (EP position 2026-06-16; Council adoption 2026-06-29), pending OJ publication; it enters into force on the 3rd day after publication, so the in-force Art 113 date remains in the calendar until then.",
	},
	{
		ID: "eu_ai_act.high_risk_annex1_original", Regime: "EU AI Act", FrameworkID: "eu_ai_act",
		Date: "2027-08-02", Title: "Art 6(1)/Annex I high-risk obligations apply (IN-FORCE law; deferral adopted, pending OJ)",
		Effect: "Under the in-force Art 113(c), Art 6(1) product-embedded (Annex I) high-risk classification and obligations apply from 2027-08-02.",
		Status: MilestoneAppliesFrom, Source: srcAIActOJ, VerifiedOn: "2026-07-03",
		Note: "The Digital Omnibus amending act has been adopted by the EP and Council and defers this to 2028-08-02 (eu_ai_act.high_risk_annex1_omnibus), but awaits OJ publication; this in-force date stands until the amendment is published.",
	},
	{
		ID: "eu_ai_act.high_risk_annex1_omnibus", Regime: "EU AI Act (Digital Omnibus)", FrameworkID: "eu_ai_act",
		Date: "2028-08-02", Title: "Annex I (product-embedded) high-risk obligations apply (omnibus, FIXED date)",
		Effect: "Adopted amending act (EP position 2026-06-16; Council adoption 2026-06-29), pending OJ publication: obligations for high-risk AI systems embedded in products (Annex I, Art 6(1)) apply from a FIXED 2028-08-02.",
		Status: MilestoneAdoptedPendingOJ, Source: srcOmnibusCouncilAdoption, VerifiedOn: "2026-07-03",
		Note: "Adopted amending act (EP position 2026-06-16; Council adoption 2026-06-29), pending OJ publication; it enters into force on the 3rd day after publication, so the in-force Art 113 date remains in the calendar until then.",
	},
	{
		ID: "eu_ai_act.art50_marking_grace_ends", Regime: "EU AI Act (Digital Omnibus)", FrameworkID: "eu_ai_act",
		Date: "2026-12-02", Title: "Machine-readable marking grace period ends (pre-existing generative systems)",
		Effect: "Adopted amending act (EP position 2026-06-16; Council adoption 2026-06-29), pending OJ publication: providers of generative AI systems already on the market before 2026-08-02 have until 2026-12-02 to comply with the Art 50 machine-readable marking (watermarking) duty.",
		Status: MilestoneAdoptedPendingOJ, Source: srcOmnibusCouncilAdoption, VerifiedOn: "2026-07-03",
		Note: "Adopted amending act (EP position 2026-06-16; Council adoption 2026-06-29), pending OJ publication; the Commission had proposed 2027-02-02 and the EP mandate 2026-11-02, with 2026-12-02 adopted as the landing.",
	},
	{
		ID: "eu_ai_act.new_art5_ncii_csam_compliance", Regime: "EU AI Act (Digital Omnibus)", FrameworkID: "eu_ai_act",
		Date: "2026-12-02", Title: "New Art 5 prohibition (NCII/CSAM generation) compliance deadline",
		Effect: "Adopted amending act (EP position 2026-06-16; Council adoption 2026-06-29), pending OJ publication: adds a NEW prohibited practice for AI systems generating non-consensual sexual/intimate content of identifiable persons ('nudifier' apps) or child sexual abuse material. Systems must comply by 2026-12-02 — the new ban is not enforceable on 2026-08-02.",
		Status: MilestoneAdoptedPendingOJ, Source: srcOmnibusEPApproval, VerifiedOn: "2026-07-03",
		Note: "Adopted amending act (EP position 2026-06-16; Council adoption 2026-06-29), pending OJ publication; it enters into force on the 3rd day after publication.",
	},
	{
		ID: "eu_pld.entry_into_force", Regime: "EU Product Liability Directive (revised)", FrameworkID: "eu_pld",
		Date: "2024-12-08", Title: "Revised PLD enters into force",
		Effect: "Directive (EU) 2024/2853 in force: software — including AI systems — is a 'product' for no-fault liability (Art 4(1), recital 13).",
		Status: MilestoneInForce, Source: srcPLDOJ, VerifiedOn: "2026-06-10",
	},
	{
		ID: "eu_pld.transposition_deadline", Regime: "EU Product Liability Directive (revised)", FrameworkID: "eu_pld",
		Date: "2026-12-09", Title: "Member-State transposition deadline (Art 22(1))",
		Effect: "National implementing law due; the new regime applies to products placed on the market or put into service after 2026-12-09 (Art 2(1)) — Directive 85/374/EEC continues to govern older products.",
		Status: MilestoneAppliesFrom, Source: srcPLDOJ, VerifiedOn: "2026-06-10",
	},
	{
		ID: "eu_aild.withdrawn", Regime: "EU AI Liability Directive (withdrawn proposal)",
		Date: "2025-10-06", Title: "AILD proposal withdrawal completed (OJ notice)",
		Effect: "The AI Liability Directive proposal (COM(2022) 496) was withdrawn: announced in Commission Work Programme 2025 (COM(2025) 45 final, 2025-02-11, Annex IV item 32) and completed with the OJ withdrawal notice C/2025/5423. It is NOT pending law — never cite it as an upcoming obligation.",
		Status: MilestonePassed,
		Source: SourceRef{
			URL:       "https://eur-lex.europa.eu/legal-content/EN/TXT/?uri=OJ:C_202505423",
			Title:     "Withdrawal of Commission proposals, OJ C/2025/5423 (6.10.2025)",
			Publisher: "EUR-Lex (Publications Office of the EU)",
		},
		VerifiedOn: "2026-06-10",
	},
	{
		ID: "nis2.entry_into_force", Regime: "NIS 2 Directive",
		FrameworkID: "nis2",
		Date:        "2023-01-16", Title: "NIS 2 Directive enters into force",
		Effect: "Directive (EU) 2022/2555 enters into force (Art 46): Member States have 21 months to transpose it into national law. The directive replaces the original NIS Directive (Directive (EU) 2016/1148).",
		Status: MilestoneInForce, Source: SourceRef{
			URL:       "https://eur-lex.europa.eu/eli/dir/2022/2555/oj",
			Title:     "Directive (EU) 2022/2555 (NIS 2 Directive), Art 46",
			Publisher: "EUR-Lex (Publications Office of the EU)",
		}, VerifiedOn: "2026-06-30",
	},
	{
		ID: "nis2.transposition_deadline", Regime: "NIS 2 Directive",
		FrameworkID: "nis2",
		Date:        "2024-10-17", Title: "Member-State transposition deadline (Art 41(1))",
		Effect: "By this date, Member States must adopt and publish national measures transposing the directive, and must apply those measures from 18 October 2024 (Art 41(1)). Essential and important entities are bound by the transposed national laws from this date. The Commission adopted implementing acts on cybersecurity risk-management measures for certain entity types (Commission Implementing Regulation (EU) 2024/2690, 17 October 2024).",
		Status: MilestonePassed, Source: SourceRef{
			URL:       "https://eur-lex.europa.eu/eli/dir/2022/2555/oj",
			Title:     "Directive (EU) 2022/2555 (NIS 2 Directive), Art 41(1)",
			Publisher: "EUR-Lex (Publications Office of the EU)",
		}, VerifiedOn: "2026-06-30",
	},
	{
		ID: "nis2.essential_important_register", Regime: "NIS 2 Directive",
		FrameworkID: "nis2",
		Date:        "2025-04-17", Title: "Member States establish list of essential and important entities (Art 3(3))",
		Effect: "By 17 April 2025, each Member State must establish a list of essential and important entities and entities providing domain-name registration services (Art 3(3)). Entities must submit the required information (name, address, sector, contact details) to the competent authority.",
		Status: MilestonePassed, Source: SourceRef{
			URL:       "https://eur-lex.europa.eu/eli/dir/2022/2555/oj",
			Title:     "Directive (EU) 2022/2555 (NIS 2 Directive), Art 3(3)",
			Publisher: "EUR-Lex (Publications Office of the EU)",
		}, VerifiedOn: "2026-06-30",
	},
	{
		ID: "dora.applies", Regime: "DORA (EU digital operational resilience, financial sector)",
		Date: "2025-01-17", Title: "DORA applies to financial entities (Art 64)",
		Effect: "Regulation (EU) 2022/2554 applies: ICT risk-management framework (Art 6), identification (Art 8), detection (Art 10), incident recording (Art 17) and the register of information (Art 28(3)) bind financial entities — the anchors of this module's DORA export mode (dora.go).",
		Status: MilestoneInForce, Source: srcDORAOJ, VerifiedOn: "2026-06-10",
	},
	{
		ID: "dora.roi_its.in_force", Regime: "DORA (EU digital operational resilience, financial sector)",
		Date: "2024-12-22", Title: "DORA Register-of-Information templates in force (ITS (EU) 2024/2956)",
		Effect: "The standard templates for the register of information (Art 28(3)) take effect: B_01.01..B_07.01 + B_99.01 (maintaining entity, entities in scope, contractual arrangements, ICT third-party providers, supply chains, functions and ICT-service assessments). The Olivares enterprise add-on (enterprise/doraregister) drafts and validates this register from operator-supplied data — it does not make the entity compliant.",
		Status: MilestoneInForce, Source: srcDORARoIITS, VerifiedOn: "2026-06-24",
		Note: "In force 20 days after OJ publication (2 Dec 2024). Labels rest on ESA artifacts + legal-text mirrors, not byte-diffed against the OJ.",
	},
	{
		ID: "dora.incident_reporting.in_force", Regime: "DORA (EU digital operational resilience, financial sector)",
		Date: "2025-03-12", Title: "DORA major-incident reporting content + time limits in force (RTS (EU) 2025/301)",
		Effect: "Major-incident classification (Art 18 + RTS (EU) 2024/1772: 7 criteria, materiality thresholds, the 'major' rule) and reporting (Art 19 + RTS (EU) 2025/301): initial notification within 4h of classification-as-major and 24h of awareness, intermediate within 72h of the initial notification, final within 1 month of the intermediate report. The Olivares enterprise add-on classifies operator-supplied impact against the criteria and drafts the reports — provisional, the duty to report rests with the entity.",
		Status: MilestoneInForce, Source: srcDORAReportRTS, VerifiedOn: "2026-06-24",
		Note: "Classification RTS: Commission Delegated Regulation (EU) 2024/1772 (CELEX 32024R1772). Reporting templates: Commission Implementing Regulation (EU) 2025/302 (CELEX 32025R0302). Thresholds/deadlines verified against ESA artifacts, not byte-diffed against the OJ.",
	},
	{
		ID: "colorado_admt.obligations_apply", Regime: "Colorado ADMT framework (SB26-189)",
		Date: "2027-01-01", Title: "Colorado ADMT obligations start",
		Effect: "SB26-189 'Automated Decision-Making Technology' (signed 2026-05-14) repealed and reenacted the 2024 Colorado AI Act (SB24-205) as an ADMT framework for consequential decisions; developer obligations (technical documentation to deployers) start 2027-01-01, with AG rulemaking on adverse-outcome disclosures due by the same date.",
		Status: MilestoneAppliesFrom,
		Source: SourceRef{
			URL:       "https://leg.colorado.gov/bills/sb26-189",
			Title:     "Colorado SB26-189 (2026) — Automated Decision-Making Technology",
			Publisher: "Colorado General Assembly",
		},
		VerifiedOn: "2026-06-10",
		Note:       "The SB24-205 obligations never took effect: originally 2026-02-01, delayed to 2026-06-30 by SB25B-004 (2025 special session), then repealed/reenacted by SB26-189 before applying.",
	},
	{
		ID: "omb_m_25_21.high_impact_deadline", Regime: "OMB M-25-21 (US federal agencies)",
		Date: "2026-04-03", Title: "Minimum risk-management practices deadline for high-impact AI (PASSED)",
		Effect: "US federal agencies had to document implementation of M-25-21 §4(b) minimum risk-management practices for high-impact AI within 365 days of issuance (2025-04-03 → 2026-04-03) and discontinue non-compliant high-impact AI use. Reference only — the deadline has passed.",
		Status: MilestonePassed,
		Source: SourceRef{
			URL:       "https://www.whitehouse.gov/wp-content/uploads/2025/02/M-25-21-Accelerating-Federal-Use-of-AI-through-Innovation-Governance-and-Public-Trust.pdf",
			Title:     "OMB Memorandum M-25-21, Accelerating Federal Use of AI through Innovation, Governance, and Public Trust (2025-04-03)",
			Publisher: "Executive Office of the President (OMB)",
		},
		VerifiedOn: "2026-06-10",
	},
	{
		ID: "fips_140_2.historical", Regime: "FIPS 140-2 (CMVP)",
		Date: "2026-09-22", Title: "FIPS 140-2 certificates move to the CMVP Historical List",
		Effect: "All remaining FIPS 140-2 validation certificates are placed on the Historical List (active through 2026-09-21). Historical-list modules remain usable for EXISTING systems; new acquisitions need FIPS 140-3 validated modules.",
		Status: MilestoneAppliesFrom,
		Source: SourceRef{
			URL:       "https://csrc.nist.gov/projects/fips-140-3-transition-effort",
			Title:     "FIPS 140-3 Transition Effort (CMVP transition schedule)",
			Publisher: "NIST Computer Security Resource Center",
		},
		VerifiedOn: "2026-06-10",
		Note:       "The product's FIPS-mode build already pins a FIPS 140-3 module (docs/SEC-G3).",
	},
	{
		ID: "cnsa_2_0.new_nss_acquisitions", Regime: "CNSA 2.0 (US National Security Systems)",
		Date: "2027-01-01", Title: "New NSS acquisitions must be CNSA 2.0 compliant (CNSSP 15)",
		Effect: "Per CNSSP 15 (quoted in the NSA CNSA 2.0 FAQ, Ver. 2.1 Dec 2024), all NEW acquisitions for National Security Systems must be CNSA 2.0 compliant from 2027-01-01 — the suite specifies ML-DSA-87 (FIPS 204) for signatures and ML-KEM-1024 (FIPS 203) for key establishment at all classification levels.",
		Status: MilestoneAppliesFrom,
		Source: SourceRef{
			URL:       "https://media.defense.gov/2022/Sep/07/2003071836/-1/-1/0/CSI_CNSA_2.0_FAQ_.PDF",
			Title:     "NSA CNSA 2.0 FAQ (U/OO/194427-22, Ver. 2.1, December 2024)",
			Publisher: "National Security Agency",
		},
		VerifiedOn: "2026-06-10",
		Note:       "'CNSA 2.0 compliant' (2027-01-01) is NOT 'exclusively use': exclusive-use endpoints are per-technology 2030/2033, phase-out of non-supporting equipment by 2030-12-31, algorithms mandated by 2031-12-31, NSM-10 goal 2035. The product's PQC posture (X25519MLKEM768 in transit) is tracked in docs/SEC-G3; ML-DSA-87 signing is roadmap, not shipped.",
	},

	// ──: US state AI law milestones ──────────────────────────────────────

	{
		ID: "tx_traiga.effective", Regime: "Texas TRAIGA",
		FrameworkID: "tx_traiga",
		Date:        "2026-01-01", Title: "Texas TRAIGA (HB 149) takes effect",
		Effect: "The Texas Responsible AI Governance Act " +
			"(89(R) HB 149, signed 2025-06-22) takes " +
			"effect: government/healthcare AI disclosure, " +
			"behavioral manipulation ban, social scoring " +
			"ban, biometric restrictions, discrimination " +
			"prohibition. An affirmative defense pathway " +
			"(§552.105(e)(2)(D)) is available for those " +
			"substantially complying with NIST AI 600-1 " +
			"or another recognized framework. AG " +
			"exclusive enforcement; no private right of " +
			"action; 60-day cure period.",
		Status: MilestoneAppliesFrom,
		Source: SourceRef{
			URL:       "https://capitol.texas.gov/BillLookup/History.aspx?LegSess=89R&Bill=HB149",
			Title:     "Texas HB 149 (89th Legislature, Regular Session)",
			Publisher: "Texas Legislature Online",
		},
		VerifiedOn: "2026-06-28",
	},
	{
		ID: "ca_sb53.effective", Regime: "California SB 53 (TFAIA)",
		FrameworkID: "ca_sb53",
		Date:        "2026-01-01", Title: "California SB 53 (TFAIA) takes effect",
		Effect: "The California Transparent and Fair AI Act " +
			"(SB 53, 2025-2026 session) takes effect: " +
			"pre-deployment safety testing for frontier AI " +
			"models, incident reporting, shutdown capability, " +
			"and safety-and-security protocols apply to " +
			"covered developers.",
		Status: MilestoneAppliesFrom,
		Source: SourceRef{
			URL:       "https://leginfo.legislature.ca.gov/faces/billNavClient.xhtml?bill_id=202520260SB53",
			Title:     "California SB 53 (2025-2026 session)",
			Publisher: "California Legislative Information",
		},
		VerifiedOn: "2026-06-28",
	},
	{
		ID: "il_hb3773.effective", Regime: "Illinois HB 3773",
		FrameworkID: "il_hb3773",
		Date:        "2026-01-01", Title: "Illinois HB 3773 (IHRA AI amendment) takes effect",
		Effect: "Illinois HB 3773 (Public Act 103-0804, " +
			"103rd General Assembly, signed 2024-08-09) " +
			"takes effect: amends the Illinois Human " +
			"Rights Act to prohibit AI use with " +
			"disparate impact on protected classes in " +
			"employment decisions and requires employer " +
			"notice of AI use to employees. IDHR draft " +
			"rulemaking (Subpart J) was withdrawn " +
			"2026-06-02; statutory obligations apply " +
			"regardless.",
		Status: MilestoneAppliesFrom,
		Source: SourceRef{
			URL:       "https://www.ilga.gov/legislation/publicacts/fulltext.asp?Name=103-0804",
			Title:     "Illinois HB 3773 (Public Act 103-0804, 103rd General Assembly)",
			Publisher: "Illinois General Assembly",
		},
		VerifiedOn: "2026-06-28",
	},

	// ──: sector-overlay milestones ───────────────────────────────────────

	{
		ID:          "pci_dss_401.future_dated",
		Regime:      "PCI DSS 4.0.1",
		FrameworkID: "pci_dss_401_ai",
		Date:        "2025-03-31", Title: "PCI DSS 4.0.1 future-dated requirements become mandatory (PASSED)",
		Effect: "All PCI DSS v4.0.1 requirements (including " +
			"previously future-dated best practices for AI/ML " +
			"in cardholder data environments — targeted risk " +
			"analysis, customized controls, authenticated " +
			"vulnerability scanning) became mandatory for all " +
			"entities processing, storing or transmitting " +
			"cardholder data.",
		Status: MilestonePassed,
		Source: SourceRef{
			URL:       "https://www.pcisecuritystandards.org/document_library/?document=pci_dss",
			Title:     "PCI DSS v4.0.1 (June 2024)",
			Publisher: "PCI Security Standards Council",
		},
		VerifiedOn: "2026-06-28",
		Note: "The deadline has passed. Entities must be " +
			"validated against v4.0.1 including all formerly " +
			"future-dated requirements.",
	},
	{
		ID:          "finra_genai.notice_25_06",
		Regime:      "FINRA GenAI Guidance",
		FrameworkID: "finra_genai",
		Date:        "2024-07-01",
		Title:       "FINRA Regulatory Notice 24-09: AI-related supervision expectations (PASSED)",
		Effect: "FINRA Regulatory Notice 24-09 articulated AI " +
			"supervision expectations under existing rules " +
			"(Rule 3110 supervision, Rule 2210 communications, " +
			"SEA Rule 17a-4 / FINRA Rule 4511 recordkeeping) " +
			"for member firms using generative AI. Not a new " +
			"compliance deadline but a clarification of " +
			"existing obligations applied to AI.",
		Status: MilestonePassed,
		Source: SourceRef{
			URL:       "https://www.finra.org/rules-guidance/notices/24-09",
			Title:     "FINRA Regulatory Notice 24-09 (Artificial Intelligence)",
			Publisher: "FINRA (Financial Industry Regulatory Authority)",
		},
		VerifiedOn: "2026-06-28",
		Note: "FINRA's GenAI guidance applies existing " +
			"supervisory, recordkeeping and communications " +
			"rules to AI — it creates no new compliance " +
			"deadline but makes examination expectations " +
			"explicit.",
	},
})

// sortMilestonesByDate orders the calendar chronologically (stable: same-date entries
// keep their authored regime order) so the API serves a ready timeline.
func sortMilestonesByDate(ms []RegulatoryMilestone) []RegulatoryMilestone {
	sort.SliceStable(ms, func(i, j int) bool { return ms[i].Date < ms[j].Date })
	return ms
}

// regulatoryWatchlist tracks instruments that are coming but not final — as data, so
// the horizon is visible without pretending anything is law/standard yet.
var regulatoryWatchlist = []WatchlistItem{
	{
		ID: "digital_omnibus_ai", Name: "EU Digital Omnibus on AI (amending Regulation (EU) 2024/1689)",
		FrameworkID: "eu_ai_act", Status: "adopted_pending_oj",
		Expected: "Formally adopted: EP first-reading position 2026-06-16 (423/57/174), Council final adoption 2026-06-29; publication in the OJ pending; in force 3rd day after publication.",
		Source:   srcOmnibusCouncilAdoption, VerifiedOn: "2026-07-03",
		Note: "On OJ publication: flip the four adopted_pending_oj milestones to in_force/applies_from, retire the superseded Art 113 originals per the compliance watchlist, and update this item.",
	},
	{
		ID: "nistir_8596", Name: "NIST IR 8596 — Cybersecurity Framework Profile for Artificial Intelligence (Cyber AI Profile)",
		Status:   "in_development",
		Expected: "Still initial PRELIMINARY draft (iprd); comments closed 2026-01-30; initial public draft expected in 2026; no IPD exists as of 2026-07-03.",
		Source: SourceRef{
			URL:       "https://csrc.nist.gov/pubs/ir/8596/iprd",
			Title:     "NIST IR 8596 (initial preliminary draft)",
			Publisher: "NIST Computer Security Resource Center",
		},
		VerifiedOn: "2026-07-03",
		Note:       "Stage precision matters: calling this an 'Initial Public Draft' would be wrong — the /ipd URL does not exist.",
	},
	{
		ID: "nist_cosais_overlays", Name: "NIST SP 800-53 Control Overlays for Securing AI Systems (COSAiS)",
		FrameworkID: "nist_cosais", Status: "in_development",
		Expected: "Predictive-AI annotated outline (2026-01-08) is still the most advanced artifact; NO agentic overlay draft exists as of 2026-07-03.",
		Source: SourceRef{
			URL:       "https://csrc.nist.gov/projects/cosais",
			Title:     "Control Overlays for Securing AI Systems (COSAiS) project page",
			Publisher: "NIST Computer Security Resource Center",
		},
		VerifiedOn: "2026-07-03",
		Note:       "The nist_cosais catalog entry is explicitly design-toward; it hardens into a crosswalk when overlays publish.",
	},
	{
		ID:       "iso_27090",
		Name:     "ISO/IEC 27090 — Cybersecurity for AI systems",
		Status:   "fdis",
		Expected: "At FDIS stage as of 2026-07-03; publication expected 2026-H2. Publication triggers a pinned catalog framework and crosswalk per the compliance watchlist.",
		Source: SourceRef{
			URL:       "https://www.iso.org/standard/56581.html",
			Title:     "ISO/IEC 27090 — Cybersecurity — Artificial Intelligence — Addressing security threats and compromises to artificial intelligence systems",
			Publisher: "ISO/IEC JTC 1/SC 27",
		},
		VerifiedOn: "2026-07-03",
		Note:       "No catalog entry until the standard is published and can be pinned.",
	},
	{
		ID:       "owasp_aivss",
		Name:     "OWASP AIVSS — Agentic AI vulnerability scoring",
		Status:   "pre_1_0",
		Expected: "Current release is v0.8; official ASI crosswalk to the Agentic Top 10 is in Appendix B; v1.0 not published as of 2026-07-03. On v1.0, re-verify formula constants and reference scores in modules/security/aivss.go.",
		Source: SourceRef{
			URL:       "https://aivss.owasp.org",
			Title:     "AIVSS Scoring System For OWASP Agentic AI Core Security Risks v0.8",
			Publisher: "OWASP Foundation — AIVSS Project",
		},
		VerifiedOn: "2026-07-03",
		Note:       "Watch item only in compliance; the security implementation is owned by modules/security/aivss.go.",
	},
	{
		ID: "en_18286", Name: "prEN 18286 — Quality Management System for EU AI Act regulatory purposes (CEN/CENELEC JTC 21)",
		FrameworkID: "eu_ai_act", Status: "in_development",
		Expected: "Public enquiry announced 2025-10-23, opened 2025-10-30, closed early 2026; out for Formal Vote by national standardization bodies as of 2026-05-27.",
		Source: SourceRef{
			URL:       "https://www.cencenelec.eu/news-events/news/2026/newsletter/ots-73-etuc/",
			Title:     "CEN/CENELEC newsletter — prEN 18286 status (2026-05-27)",
			Publisher: "CEN/CENELEC",
		},
		VerifiedOn: "2026-06-10",
		Note:       "A harmonised QMS standard for high-risk providers; once cited in the OJEU it grants presumption of conformity — watch for publication. The Q4-2026 availability target for AI Act harmonised standards is CEN/CENELEC's accelerated-procedure commitment (news item 2025-10-23, cencelelec.eu → news/2025/brief-news/2025-10-23-ai-standardization).",
	},
	{
		ID: "il_hb3773_rulemaking", Name: "Illinois IDHR AI rulemaking (HB 3773 Subpart J)",
		FrameworkID: "il_hb3773", Status: "withdrawn_pending_revision",
		Expected: "Draft rules (Subpart J) proposed 2026-05-15, withdrawn 2026-06-02 citing need for continued collaboration with other state agencies. No revised timeline.",
		Source: SourceRef{
			URL:       "https://www.ilga.gov/legislation/publicacts/fulltext.asp?Name=103-0804",
			Title:     "Illinois HB 3773 (Public Act 103-0804, 103rd GA)",
			Publisher: "Illinois General Assembly",
		},
		VerifiedOn: "2026-06-28",
		Note:       "The underlying statutory obligations (notice under §2-102(L) and anti-discrimination under §2-102(A)) are in effect regardless of the rulemaking status.",
	},
	{
		ID: "owasp_mcp_top10", Name: "OWASP MCP Top 10",
		Status:   "beta",
		Expected: "OWASP Foundation incubator project in 'Phase 3 — Beta Release and Pilot Testing', document v0.1; not final.",
		Source: SourceRef{
			URL:       "https://owasp.org/www-project-mcp-top-10/",
			Title:     "OWASP MCP Top 10 project page",
			Publisher: "OWASP Foundation",
		},
		VerifiedOn: "2026-06-10",
		Note:       "The product's MCP guardrail lens (owasp_mcp.go, module IX) tracks the beta; it is NOT cited as a final standard.",
	},
}

const calendarDateDisclaimer = "Dates are data with primary source + verified_on; provisional_agreement and adopted_pending_oj entries are NOT in-force law."

// milestoneByID indexes the calendar for O(1) lookup (tests + MilestoneIDs resolution).
var milestoneByID = func() map[string]RegulatoryMilestone {
	m := make(map[string]RegulatoryMilestone, len(regulatoryCalendar))
	for _, ms := range regulatoryCalendar {
		m[ms.ID] = ms
	}
	return m
}()

// handleCalendar returns the regulatory calendar + watchlist (read-tier; static
// verified data, not tenant evidence). ?framework= filters both lists to the
// milestones/items linked to one catalog framework.
func (m *Module) handleCalendar(w http.ResponseWriter, r *http.Request, _ api.ModuleContext) {
	fwFilter := strings.TrimSpace(r.URL.Query().Get("framework"))
	milestones := regulatoryCalendar
	watch := regulatoryWatchlist
	if fwFilter != "" {
		// Non-nil initializers: an empty filtered list serializes as [], never null
		// (the UI contract documents arrays).
		milestones = []RegulatoryMilestone{}
		for _, ms := range regulatoryCalendar {
			if ms.FrameworkID == fwFilter {
				milestones = append(milestones, ms)
			}
		}
		watch = []WatchlistItem{}
		for _, wi := range regulatoryWatchlist {
			if wi.FrameworkID == fwFilter {
				watch = append(watch, wi)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"milestones": milestones,
		"watchlist":  watch,
		"disclaimer": reportDisclaimer + " " + calendarDateDisclaimer,
	})
}
