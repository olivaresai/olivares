// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// Surface is which piece of agent text a guardrail input is.
type Surface string

// The inspectable surfaces. Inline enforcement (when governed-on) typically gates
// output and tool_args; input is usually detective (you flag a malicious prompt,
// you rarely block the user mid-stream).
const (
	SurfaceInput      Surface = "input"       // a prompt/message INTO the agent
	SurfaceOutput     Surface = "output"      // the agent's response OUT
	SurfaceToolArgs   Surface = "tool_args"   // arguments to a tool/function call
	SurfaceToolResult Surface = "tool_result" // UNTRUSTED third-party/tool content returned to the model
)

func validSurface(s Surface) bool {
	switch s {
	case SurfaceInput, SurfaceOutput, SurfaceToolArgs, SurfaceToolResult:
		return true
	default:
		return false
	}
}

// untrustedSurface reports whether a surface carries third-party / tool-returned
// content that, per Anthropic's prompt-injection guidance, must be treated as
// inert DATA and confined to tool_result blocks — never as instructions. These
// are the surfaces the structural-injection detector screens hardest.
func untrustedSurface(s Surface) bool { return s == SurfaceToolResult || s == SurfaceToolArgs }

// GuardrailInput is one piece of agent text to inspect plus its non-sensitive
// context. The Text is inspected in memory and NEVER stored or returned raw — the
// module redacts before it hashes/returns (docs/SECURITY-HARDENING.md).
type GuardrailInput struct {
	Surface     Surface
	Text        string
	AgentRef    string
	SessionRef  string
	ResourceRef string
}

// subject resolves the most specific non-sensitive subject for a finding from the
// input's context.
func (in GuardrailInput) subject() (kind, ref string) {
	switch {
	case in.SessionRef != "":
		return "session", in.SessionRef
	case in.AgentRef != "":
		return "agent", in.AgentRef
	case in.ResourceRef != "":
		return "resource", in.ResourceRef
	default:
		return "guardrail", string(in.Surface)
	}
}

// Detection is one guardrail trip. It is MINIMAL EVIDENCE (docs/SECURITY-HARDENING.md): a class, a
// specific rule, a severity, a short redacted excerpt and the framework reference —
// never the raw matched value.
type Detection struct {
	// Class is the detector family (pii, prompt_injection, jailbreak, content,
	// output_validation, owasp_agentic).
	Class string
	// Rule is the specific rule within the class.
	Rule string
	// Severity grades the trip on the shared ordered scale.
	Severity sdkmodel.Severity
	// Title is a short, non-sensitive summary safe to display.
	Title string
	// Excerpt is a short, ALREADY-REDACTED snippet (a placeholder/label, never the
	// raw secret/PII) for the operator to see why it tripped.
	Excerpt string
	// OWASPLLM, OWASPASI and ATLAS are the detection's multi-taxonomy references
	//, each a SET so one detection can map to several frameworks at once:
	// OWASP Top 10 for LLM Applications ids ("LLM01:2025"), OWASP Top 10 for Agentic
	// Applications ids ("ASI01"), and MITRE ATLAS technique ids ("AML.T0051.001").
	// They are populated by scan() (prefix-routed, sorted, de-duped); a detection with
	// no framework reference leaves them nil.
	OWASPLLM []string
	OWASPASI []string
	ATLAS    []string
}

// detail builds the canonical, redacted fingerprint string that is hashed into a
// finding's DetailHash. It is composed only of already-redacted, non-sensitive
// parts so the hash is a stable dedup/audit reference, never a way to recover the
// payload (docs/SECURITY-HARDENING.md). The OWASP axis folds the LLM+ASI sets into one ordered list
// so a single-id finding hashes byte-identically to the pre form (continuity).
func (d Detection) detail() string {
	return strings.Join([]string{d.Class, d.Rule, d.Excerpt, joinAxes(d.OWASPLLM, d.OWASPASI), joinAxes(d.ATLAS)}, "|")
}

// Verdict is the overall outcome of an inspection.
type Verdict string

// The verdicts. allow/flag are DETECTIVE (read-first, docs/SECURITY-HARDENING.md); block is the
// inline-enforcement outcome, reachable only when the persisted enforcement policy
// is enabled for the class at the detection's severity AND the caller asked to
// enforce.
const (
	VerdictAllow Verdict = "allow"
	VerdictFlag  Verdict = "flag"
	VerdictBlock Verdict = "block"
)

// inspect runs every detector (and the optional classifier) over the input and
// returns the merged detections, sorted most-severe first. It is pure: it persists
// nothing and never mutates the input.
func (m *Module) inspect(ctx context.Context, in GuardrailInput) []Detection {
	var out []Detection
	for _, d := range m.detectors {
		out = append(out, d.Inspect(in)...)
	}
	if m.classifier != nil {
		extra, err := m.classifier.Classify(ctx, in)
		if err != nil {
			// A classifier failure never fails the inspection (read-first): the
			// deterministic detections still stand.
			m.debugf("security: classifier failed; using deterministic detections only", "err", err)
		} else {
			out = append(out, extra...)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return sevRank(out[i].Severity) > sevRank(out[j].Severity)
	})
	return out
}

// sevRank orders the wire severities for "most severe first" sorting; unknown ranks
// lowest.
func sevRank(s sdkmodel.Severity) int {
	switch s {
	case sdkmodel.SeverityCritical:
		return 4
	case sdkmodel.SeverityHigh:
		return 3
	case sdkmodel.SeverityMedium:
		return 2
	case sdkmodel.SeverityLow:
		return 1
	default:
		return 0
	}
}

// inspectRequest is the body of POST /guardrails/inspect.
type inspectRequest struct {
	Surface     string `json:"surface"`
	Text        string `json:"text"`
	AgentRef    string `json:"agent_ref,omitempty"`
	SessionRef  string `json:"session_ref,omitempty"`
	ResourceRef string `json:"resource_ref,omitempty"`
	// Enforce asks the module to BLOCK on a sufficiently severe detection. It only
	// takes effect when the tenant's enforcement policy is enabled for the class
	// (off by default) — otherwise the verdict is detective (allow/flag).
	Enforce bool `json:"enforce,omitempty"`
}

// detectionDTO is the redacted wire shape of a detection. The taxonomy is exposed as
// three independent arrays so a console/SIEM can filter a finding by OWASP
// LLM, OWASP Agentic (ASI) or MITRE ATLAS without the axes colliding in one slot.
type detectionDTO struct {
	Class    string   `json:"class"`
	Rule     string   `json:"rule"`
	Severity string   `json:"severity"`
	Title    string   `json:"title"`
	Excerpt  string   `json:"excerpt,omitempty"`
	OWASPLLM []string `json:"owasp_llm,omitempty"`
	OWASPASI []string `json:"owasp_asi,omitempty"`
	ATLAS    []string `json:"atlas,omitempty"`
	Enforced bool     `json:"enforced"` // this detection would block under the active policy
}

// inspectResponse is the result of an inspection.
type inspectResponse struct {
	Verdict     Verdict        `json:"verdict"`
	Detections  []detectionDTO `json:"detections"`
	FindingIDs  []string       `json:"finding_ids"`
	Enforcement string         `json:"enforcement"` // "detective" | "enforced" — the posture that produced the verdict
}

// handleInspect runs the guardrails over a submitted piece of agent text. It is the
// inline INSPECTION seam a caller (a model gateway, an orchestrator, an agent
// integration) uses; the module detects, produces findings with minimal evidence,
// and returns a DETECTIVE verdict — or a BLOCK only when inline enforcement is
// enabled and governed. The raw Text is never stored or echoed.
func (m *Module) handleInspect(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var req inspectRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	surface := Surface(strings.TrimSpace(req.Surface))
	if !validSurface(surface) {
		writeJSON(w, http.StatusBadRequest, errorBody("surface must be input, output or tool_args"))
		return
	}
	if len(req.Text) > maxTextLen {
		writeJSON(w, http.StatusBadRequest, errorBody("text too large"))
		return
	}
	in := GuardrailInput{
		Surface:     surface,
		Text:        req.Text,
		AgentRef:    clamp(strings.TrimSpace(req.AgentRef), maxRefLen),
		SessionRef:  clamp(strings.TrimSpace(req.SessionRef), maxRefLen),
		ResourceRef: clamp(strings.TrimSpace(req.ResourceRef), maxRefLen),
	}

	detections := m.inspect(r.Context(), in)
	subjectKind, subjectRef := in.subject()

	out := inspectResponse{Verdict: VerdictAllow, Detections: []detectionDTO{}, FindingIDs: []string{}, Enforcement: "detective"}
	if len(detections) == 0 {
		writeJSON(w, http.StatusOK, out)
		return
	}

	// Persist a finding per detection + the inspection self-audit, in one tx; decide
	// the enforcement outcome from the persisted policy inside the same scope.
	enforced := false
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		policies, perr := loadEnforcement(r.Context(), sc)
		if perr != nil {
			return perr
		}
		for _, d := range detections {
			id, ferr := m.persistFinding(r.Context(), sc, finding{
				kind:        findingKindGuardrail,
				severity:    d.Severity,
				source:      d.Class,
				subjectKind: subjectKind,
				subjectRef:  subjectRef,
				title:       d.Title,
				detail:      d.detail(),
				meta:        aivssMeta(d, taxonomyMeta(d, map[string]any{"rule": d.Rule, "surface": string(surface)})),
			})
			if ferr != nil {
				return ferr
			}
			dd := detectionDTO{
				Class: d.Class, Rule: d.Rule, Severity: string(d.Severity), Title: d.Title,
				Excerpt: d.Excerpt, OWASPLLM: d.OWASPLLM, OWASPASI: d.OWASPASI, ATLAS: d.ATLAS,
			}
			if req.Enforce && policies.blocks(d.Class, d.Severity) {
				dd.Enforced = true
				enforced = true
			}
			out.Detections = append(out.Detections, dd)
			out.FindingIDs = append(out.FindingIDs, id.String())
		}
		return auditEvent(r.Context(), sc, mc, "security.guardrail.inspect", caseKind, "", map[string]any{
			"surface": string(surface), "detections": len(detections), "subject_kind": subjectKind, "enforced": enforced,
		})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}

	switch {
	case enforced:
		out.Verdict, out.Enforcement = VerdictBlock, "enforced"
	default:
		out.Verdict = VerdictFlag // detective: detected and recorded, never blocked
	}
	// Findings also go on the bus for real-time delivery and correlation.
	for _, d := range detections {
		m.emitFinding(r.Context(), mc.Tenant, busGuardrail, d.Severity, subjectKind, subjectRef, d.Title, d.detail(), axesOf(d))
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- enforcement policy ----------------------------------------------------------

// enforcementSet is the tenant's loaded inline-enforcement posture, keyed by class
// (plus an optional "*" wildcard row).
type enforcementSet struct {
	byClass map[string]enforcementRow
}

type enforcementRow struct {
	enabled bool
	minSev  model.Severity
}

// blocks reports whether a detection of class/severity would BLOCK under this
// posture: the class (or the wildcard) must be enabled and the detection at/above
// the configured minimum severity. With no matching row it returns false — the
// safe, detective default (docs/SECURITY-HARDENING.md).
func (s enforcementSet) blocks(class string, sev sdkmodel.Severity) bool {
	row, ok := s.byClass[class]
	if !ok {
		row, ok = s.byClass["*"]
	}
	if !ok || !row.enabled {
		return false
	}
	return coreAtLeast(sevToCore(sev), row.minSev)
}

// loadEnforcement reads the tenant's enforcement policy rows. A tenant with no rows
// is fully detective.
func loadEnforcement(ctx context.Context, sc store.Scope) (enforcementSet, error) {
	out := enforcementSet{byClass: map[string]enforcementRow{}}
	repo, err := sc.Ext(enforcementKind)
	if err != nil {
		return out, err
	}
	recs, err := listAll(ctx, repo)
	if err != nil {
		return out, err
	}
	for _, rec := range recs {
		out.byClass[rec.String(colClass)] = enforcementRow{
			enabled: rec.Bool(colEnabled),
			minSev:  model.Severity(rec.String(colMinSeverity)),
		}
	}
	return out, nil
}

// coreAtLeast reports whether severity s is at least floor on the 4-value core
// scale. An unknown value fails closed on either side.
func coreAtLeast(s, floor model.Severity) bool {
	rank := map[model.Severity]int{
		model.SeverityLow: 1, model.SeverityMedium: 2, model.SeverityHigh: 3, model.SeverityCritical: 4,
	}
	sr, ok := rank[s]
	if !ok {
		return false
	}
	fr, ok := rank[floor]
	if !ok {
		return false
	}
	return sr >= fr
}

// enforcementDTO is the wire shape of one class's enforcement posture.
type enforcementDTO struct {
	Class       string `json:"class"`
	Enabled     bool   `json:"enabled"`
	MinSeverity string `json:"min_severity"`
	Governed    bool   `json:"governed"`
	SetBy       string `json:"set_by,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
}

// handleGetEnforcement returns the tenant's inline-enforcement posture (all classes).
// An empty list means fully detective — the default (docs/SECURITY-HARDENING.md).
func (m *Module) handleGetEnforcement(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	out := listResponse[enforcementDTO]{Items: []enforcementDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(enforcementKind)
		if err != nil {
			return err
		}
		recs, err := listAll(r.Context(), repo)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, enforcementDTO{
				Class: rec.String(colClass), Enabled: rec.Bool(colEnabled),
				MinSeverity: rec.String(colMinSeverity), Governed: rec.Bool(colGoverned),
				SetBy: rec.String(colSetBy), UpdatedAt: rec.String(colUpdatedAt),
			})
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// setEnforcementRequest is the body of PUT /enforcement.
type setEnforcementRequest struct {
	Class       string `json:"class"`
	Enabled     bool   `json:"enabled"`
	MinSeverity string `json:"min_severity"`
	Reason      string `json:"reason,omitempty"`
}

// handleSetEnforcement sets the inline-enforcement posture for a guardrail class.
// This is the ONLY production-affecting action in the module, so it is admin-tier
// AND governed by the ApprovalGate where wired: enabling enforcement asks the
// ApprovalGate, and the result records whether it was governed. The change is
// always self-audited.
func (m *Module) handleSetEnforcement(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var req setEnforcementRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	class := strings.TrimSpace(req.Class)
	if class == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("class is required (a guardrail class or \"*\")"))
		return
	}
	minSev := model.Severity(strings.TrimSpace(req.MinSeverity))
	if minSev == "" {
		minSev = model.SeverityHigh // a conservative default: only block serious trips
	}
	if !coreAtLeast(minSev, model.SeverityLow) {
		writeJSON(w, http.StatusBadRequest, errorBody("min_severity must be low, medium, high or critical"))
		return
	}

	// Governance: enabling enforcement is gated by where wired. Disabling
	// (returning to detective, the safe state) is always allowed.
	governed := false
	if req.Enabled {
		dec, err := m.gate.Authorize(r.Context(), mc.Tenant, ApprovalRequest{
			Action: "security.enforcement.enable", SubjectKind: "guardrail_class", SubjectRef: class,
			Reason: clamp(strings.TrimSpace(req.Reason), maxReasonLen), Actor: mc.Principal.Actor(),
		})
		if err != nil || !dec.Approved {
			writeJSON(w, http.StatusForbidden, errorBody("enabling inline enforcement was not approved by governance ("+statusOf(dec, err)+")"))
			return
		}
		governed = dec.Governed
	}

	out := enforcementDTO{Class: class, Enabled: req.Enabled, MinSeverity: string(minSev), Governed: governed, SetBy: mc.Principal.Actor()}
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(enforcementKind)
		if err != nil {
			return err
		}
		now := m.clock.Now()
		existing, found, err := findOne(r.Context(), repo, eq(colClass, class))
		if err != nil {
			return err
		}
		rec := model.Record{
			colClass: class, colEnabled: req.Enabled, colMinSeverity: string(minSev),
			colGoverned: governed, colSetBy: mc.Principal.Actor(), colUpdatedAt: now.String(),
		}
		if found {
			rec[model.ColID] = existing.String(model.ColID)
			rec[model.ColVersion] = existing.Int(model.ColVersion)
			if _, err := repo.Update(r.Context(), rec); err != nil {
				return err
			}
		} else if _, err := repo.Create(r.Context(), rec); err != nil {
			return err
		}
		out.UpdatedAt = now.String()
		return auditEvent(r.Context(), sc, mc, "security.enforcement.set", enforcementKind, "", map[string]any{
			"class": class, "enabled": req.Enabled, "min_severity": string(minSev), "governed": governed,
		})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if req.Enabled && !governed {
		m.log.Warn("security: inline enforcement ENABLED without governance (no gate wired)", "class", class, "actor", mc.Principal.Actor())
	}
	writeJSON(w, http.StatusOK, out)
}

// statusOf renders a short reason for a denied enforcement change.
func statusOf(dec ApprovalDecision, err error) string {
	if err != nil {
		return "gate error"
	}
	if dec.Status != "" {
		return dec.Status
	}
	return "rejected"
}
