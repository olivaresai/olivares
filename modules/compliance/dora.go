// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

// the DORA export mode: the tenant's AI estate rendered ICT-risk-compatible
// for a FINANCIAL-ENTITY customer's DORA (Regulation (EU) 2022/2554) program. It
// exports what the module already records — the governed AI risk register, the
// incident/threat timeline and the third-party AI-provider register — anchored to
// the ledger, with the DORA provisions each section evidences cited as data.
//
// ANCHOR CORRECTION (verified against CELEX:32022R2554 on 2026-06-10): the session
// prompt cited "DORA Art 9(10)", which DOES NOT EXIST — Article 9 ('Protection and
// prevention') has only paragraphs 1–4 (Article 6 is the one with a paragraph 10,
// on outsourcing verification of ICT-risk compliance). The export anchors to the
// provisions that actually govern its content: Art 6(1)+6(5) (documented, reviewed
// ICT risk-management framework), Art 8(1) (identify/classify/document ICT-supported
// functions, assets and dependencies), Art 10(1) (detection), Art 17(1)-(2) (record
// ALL ICT-related incidents and significant cyber threats), Art 28(3) (register of
// information on ICT third-party arrangements).
//
// HONESTY (docs/SECURITY-HARDENING.md): the control plane is not a financial entity and this is not
// a DORA compliance claim — it is the evidence INPUT a financial entity folds into
// its own DORA framework. Sections are populated only from real tenant rows; an
// empty register is exported empty.

import (
	"encoding/hex"
	"net/http"
	"sort"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// doraRegulationTitle is DORA's FULL official title — auditor-facing citation data
// must carry it (verified against EUR-Lex CELEX:32022R2554, 2026-06-10).
const doraRegulationTitle = "Regulation (EU) 2022/2554 of the European Parliament and of the Council of 14 December 2022 on digital operational resilience for the financial sector and amending Regulations (EC) No 1060/2009, (EU) No 648/2012, (EU) No 600/2014, (EU) No 909/2014 and (EU) 2016/1011 (DORA)"

const doraDisclaimer = "ICT-risk-compatible view of the tenant's AI estate for a financial entity's program under " + doraRegulationTitle + ". The control plane provides evidence input; it does not make the tenant DORA-compliant and this is NOT legal advice or a certification. The incident timeline carries ALL governed finding kinds (guardrail/anomaly, red-team, eval-regression, external activity), each labeled by kind."

// doraBasis cites the DORA provisions each export section evidences — as data, with
// the primary source and verification date (the dates-as-data bar).
var doraBasis = []map[string]string{
	{"provision": "Art 6(1), 6(5)", "evidences": "risk_register", "summary": "Sound, comprehensive and WELL-DOCUMENTED ICT risk-management framework, documented and reviewed — the governed AI risk register is the AI slice of that documentation.", "source_url": "https://eur-lex.europa.eu/legal-content/EN/TXT/?uri=CELEX:32022R2554", "verified_on": "2026-06-10"},
	{"provision": "Art 8(1)", "evidences": "risk_register", "summary": "Identify, classify and adequately document ICT-supported business functions, information assets and their dependencies — per-agent classifications with observed access signals.", "source_url": "https://eur-lex.europa.eu/legal-content/EN/TXT/?uri=CELEX:32022R2554", "verified_on": "2026-06-10"},
	{"provision": "Art 10(1)", "evidences": "incident_timeline", "summary": "Mechanisms to promptly detect anomalous activities, including ICT-related incidents — the guardrail/anomaly findings feeding the timeline.", "source_url": "https://eur-lex.europa.eu/legal-content/EN/TXT/?uri=CELEX:32022R2554", "verified_on": "2026-06-10"},
	{"provision": "Art 17(1), 17(2)", "evidences": "incident_timeline", "summary": "ICT-related incident management process; record ALL ICT-related incidents and significant cyber threats — the exported timeline with its tamper-evident ledger anchor.", "source_url": "https://eur-lex.europa.eu/legal-content/EN/TXT/?uri=CELEX:32022R2554", "verified_on": "2026-06-10"},
	{"provision": "Art 28(3)", "evidences": "third_party_register", "summary": "Register of information on all contractual arrangements with ICT third-party service providers — the per-provider GPAI posture rows are the AI-provider slice.", "source_url": "https://eur-lex.europa.eu/legal-content/EN/TXT/?uri=CELEX:32022R2554", "verified_on": "2026-06-10"},
}

// doraRegisterEntry is one AI risk-register row (Art 6/8 slice).
type doraRegisterEntry struct {
	SubjectKind   string   `json:"subject_kind"`
	SubjectRef    string   `json:"subject_ref"`
	Tier          string   `json:"tier"`
	SuggestedTier string   `json:"suggested_tier"`
	State         string   `json:"state"`
	NISTFunctions []string `json:"nist_functions,omitempty"`
	Rationale     string   `json:"rationale,omitempty"`
	ClassifiedAt  string   `json:"classified_at"`
	ReviewedBy    string   `json:"reviewed_by,omitempty"`
}

// doraIncidentEntry is one timeline row (Art 10/17 slice) — minimal data: kind,
// severity, subject, when; never payloads.
type doraIncidentEntry struct {
	Kind        string `json:"kind"`
	Severity    string `json:"severity"`
	Status      string `json:"status"`
	Source      string `json:"source,omitempty"`
	SubjectKind string `json:"subject_kind,omitempty"`
	SubjectRef  string `json:"subject_ref,omitempty"`
	Title       string `json:"title,omitempty"`
	OccurredAt  string `json:"occurred_at"`
}

// doraThirdPartyEntry is one AI-provider register row (Art 28(3) slice), reusing the
// FIN-13 GPAI posture by kind string (decoupled, like every sibling probe).
type doraThirdPartyEntry struct {
	ProviderRef        string `json:"provider_ref"`
	PostureVerified    bool   `json:"posture_verified"`
	VerificationMethod string `json:"verification_method,omitempty"`
}

// handleDORAExport assembles and returns the DORA export. Exporting the tenant's
// risk register + incident timeline is a SENSITIVE evidence read, so it self-audits
// in a committed transaction, exactly like the evidence export.
func (m *Module) handleDORAExport(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	// Non-nil initializers: an empty register exports as [], never null (the file's
	// own contract — "an empty register is exported empty").
	register := []doraRegisterEntry{}
	incidents := []doraIncidentEntry{}
	thirdParty := []doraThirdPartyEntry{}
	var anchor map[string]any
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(riskKind)
		if err != nil {
			return err
		}
		recs, err := listAll(r.Context(), repo)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			register = append(register, doraRegisterEntry{
				SubjectKind:   rec.String(colSubjectKind),
				SubjectRef:    rec.String(colSubjectRef),
				Tier:          rec.String(colTier),
				SuggestedTier: rec.String(colSuggested),
				State:         rec.String(colRiskState),
				NISTFunctions: decodeStrings(rec.String(colNistFns)),
				Rationale:     rec.String(colRationale),
				ClassifiedAt:  rec.String(colClassifiedAt),
				ReviewedBy:    rec.String(colReviewedBy),
			})
		}

		// Page the findings FULLY (the Art 17 basis says "record ALL ICT-related
		// incidents" — truncating to one page would silently drop the newest ones)
		// and sort the timeline by occurrence, newest first.
		q := model.Query{Limit: listCap}
		for {
			finds, page, err := sc.Findings().List(r.Context(), q)
			if err != nil {
				return err
			}
			for _, f := range finds {
				incidents = append(incidents, doraIncidentEntry{
					Kind: f.Kind, Severity: string(f.Severity), Status: string(f.Status), Source: f.Source,
					SubjectKind: f.SubjectKind, SubjectRef: f.SubjectID.String(), Title: f.Title,
					OccurredAt: f.OccurredAt.String(),
				})
			}
			if !page.HasMore || page.Cursor == "" {
				break
			}
			q.Cursor = page.Cursor
		}
		sort.SliceStable(incidents, func(i, j int) bool { return incidents[i].OccurredAt > incidents[j].OccurredAt })

		// Third-party AI-provider register (Art 28(3) slice) — graceful when the
		// models module is not registered (an honest empty register, never a fake).
		if pRepo, perr := sc.Ext(gpaiPostureExtKind); perr == nil {
			pRecs, lerr := listAll(r.Context(), pRepo)
			if lerr != nil {
				return lerr
			}
			for _, rec := range pRecs {
				thirdParty = append(thirdParty, doraThirdPartyEntry{
					ProviderRef:        rec.String("provider_ref"),
					PostureVerified:    rec.Bool("verified"),
					VerificationMethod: rec.String("verification_method"),
				})
			}
		}

		// Tamper-evidence: the ledger anchor + live verify, same proof shape as the
		// evidence package (docs/SECURITY-HARDENING.md).
		head, headOK, err := sc.Audit().Head(r.Context())
		if err != nil {
			return err
		}
		rep, err := sc.Audit().Verify(r.Context(), 0)
		if err != nil {
			return err
		}
		anchor = map[string]any{
			"ledger_seq":        head.Seq,
			"integrity_ok":      headOK && rep.OK && rep.Checked > 0,
			"integrity_checked": rep.Checked,
		}
		if headOK {
			anchor["ledger_hash"] = hex.EncodeToString(head.Hash)
		}

		return auditEvent(r.Context(), sc, mc, "compliance.dora.export", riskKind, model.ID(""), map[string]any{
			"register_entries": len(register), "incident_entries": len(incidents), "third_party_entries": len(thirdParty),
		})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"generated_at":         m.clock.Now().String(),
		"regulation":           doraRegulationTitle,
		"basis":                doraBasis,
		"risk_register":        register,
		"incident_timeline":    incidents,
		"third_party_register": thirdParty,
		"ledger_anchor":        anchor,
		"disclaimer":           doraDisclaimer,
	})
}
