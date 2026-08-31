// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// templateapply.go turns a workspace template from a DESCRIPTION of a restriction into
// an APPLIED one. Until the /apply endpoint read the row and answered
// `{"applied":true,"conflicts":[]}` unconditionally, with a comment saying the caller
// performed the real merge "because the target configuration is client-side" — and the
// caller did not: the console's only reaction was a toast
// (web/src/features/workspace-templates/template-card.tsx). Eight built-in templates
// are seeded into EVERY tenant describing themselves as security postures ("strict DLP
// and read-only"), a dialog promised in seven languages that applying one overwrites
// the session's settings, and nothing anywhere read a single one of their fields: no
// run ever carried a template reference.
//
// ⛔ THE RULE THIS FILE EXISTS TO KEEP, and it is the reason the merge cannot go back
// to the client: THESE TEMPLATES ARE SOLD AS RESTRICTIONS, AND A RESTRICTION APPLIED BY
// THE CLIENT IS NOT A RESTRICTION. It is skipped by not using the client. So the merge
// is the server's, it happens before the governance gates see the launch (so the gates
// judge the RESTRICTED launch, not the requested one), and its result reaches the child
// as argv the operator never chose.
//
// ⛔ AND THE SECOND RULE, which is the one that is easy to get backwards: A TERM THIS
// LAUNCH CANNOT KEEP REFUSES THE LAUNCH. It is never accepted and dropped. "Accepted and
// not fulfilled" is precisely the defect above, and re-introducing it one field at a
// time is how it would come back. unenforceableTerms names every such field and
// applyLaunchTemplate turns it into a 422 that says which field and why.

// ---------------------------------------------------------------------------
// The enforcement facts this file is built on — measured, not assumed.
// ---------------------------------------------------------------------------

// permModeDontAsk is the only permission mode in which the DENIAL of a tool that matches
// no allow rule is DOCUMENTED rather than inferred, which is why an allowlist pins it
// rather than merely coexisting with it.
//
// ⚠ THAT SENTENCE IS NARROWER THAN THE ONE THIS COMMENT FIRST MADE, and the correction is
// the point. It said dontAsk was the ONLY mode in which an allowlist confines anything —
// and the repository's own model (connectors/claude SDKEvaluationOrder /
// ResolveSDKDecision, verified 2026-06-19) does not say that. Walking a tool that is NOT
// on the allowlist through the order:
//
//   - bypassPermissions approves EVERYTHING at the mode step, before the allow rules are
//     ever consulted — the allowlist is dead code.
//   - acceptEdits approves the whole file/write CLASS at that same step, so a Write
//     survives an allowlist that never mentioned it. This is the trap: it looks restrictive.
//   - plan diverts writes to the resolver BEFORE the allow rules, so a listed write is not
//     auto-approved either — it under-applies the list instead of over-applying it.
//   - default, auto and manual reach the resolver step, and the model returns DENY there
//     when no resolver is wired — which is this runtime's case (nothing wires canUseTool;
//     the child is launched headless with `--print`). So an allowlist plausibly DOES
//     confine under default. But that branch is labeled in agentsdk.go as the control
//     plane's own modeling of an unresolvable call — "the SDK host would prompt or error"
//     — not as a vendor guarantee, and `auto` runs a second classifier the model says it
//     deliberately does not encode.
//   - dontAsk SKIPS the resolver step and denies. That one is documented behavior.
//
// ⇒ an allowlist pins dontAsk, and a template declaring both an allowlist and a different
// mode refuses the launch. The refusal of `default` + allowlist is therefore CONSERVATIVE,
// not proven: it declines a combination that probably works, because the only evidence for
// it is a modeled inference and the failure direction is a session that looks confined and
// is not. Widening it needs the resolver-absent behavior verified against the binary — at
// which point this refusal should be relaxed, not quietly kept.
// (Both the original overstatement and this correction came from the Codex sol max
// contrast, 2026-08-11.)
//
// The consequence is deliberate and is NOT a side effect to be engineered away:
// dontAsk is a CRITICAL launch (cmd/olivares/sessiongov.go isCriticalLaunch
// 2026-06-16), so a tool-restricted session needs its governed human approval and is
// recorded. A session that acts without a human is privileged whether or not its tool
// set is small.
const permModeDontAsk = "dontAsk"

// dlpStrictness ranks the workspace DLP postures so a template can require a FLOOR.
// The vocabulary is the workspace's own (workspace_schema.go) — off | label | deny —
// and a template that speaks any other word is refused rather than guessed at.
var dlpStrictness = map[string]int{dlpOff: 0, dlpLabel: 1, dlpDeny: 2}

// maxTemplateSessionMinutes bounds the duration a template may impose. It is a sanity
// ceiling on a stored integer, not a policy: 7 days.
const maxTemplateSessionMinutes = 7 * 24 * 60

// maxTemplateInstructions and maxTemplateTools bound what a template may push into the
// child's ARGV. They are not policy either — they are the point at which an unbounded
// stored string stops being a launch parameter and becomes a launch FAILURE: argv is
// capped by the kernel (ARG_MAX), and a template over it would fail every launch with
// the runner's opaque "could not start the process" 502.
//
// Bounded HERE, at the merge, so the answer names the field and the limit instead of
// blaming the spawn. That is the same rule as everything else in this file: refuse a
// term this launch cannot carry, do not accept it and fail later.
const (
	maxTemplateInstructions = 8 << 10 // 8 KiB of appended system prompt
	maxTemplateTools        = 256     // allow rules in one allowlist
)

// ---------------------------------------------------------------------------
// The terms a template imposes on a launch.
// ---------------------------------------------------------------------------

// tplTerms is a template body reduced to the launch parameters this runtime can
// actually impose, plus the list of declared fields it cannot. It is a pure value:
// templateTerms does no I/O and no defaulting, so an absent term stays absent and is
// distinguishable from one the request happened to leave empty.
type tplTerms struct {
	permissionMode string
	effort         string
	model          string
	instructions   string
	allowedTools   []string
	recordIO       bool
	maxDuration    time.Duration
	// requiresDLP is the workspace DLP posture the template declares (""= none). It is
	// ONLY ever a metadata precondition — see the refusal in templateTerms for why that is
	// not the same thing as the field's name, and why anything above `off` refuses the
	// launch on the isolation this release actually wires.
	requiresDLP string
	// unenforceable names every field the template declares that this launch cannot keep.
	// Non-empty ⇒ the launch is refused (422) and the /apply preview reports applied=false.
	unenforceable []string
}

// templateTerms reduces a template body to the terms a launch can impose, and names
// every declared field it cannot. It NEVER silently drops a field: anything it does not
// return in an enforceable slot is returned in unenforceable, and both callers refuse
// on that.
func templateTerms(body tplBody) tplTerms {
	var t tplTerms
	if s := body.Settings; s != nil {
		t.permissionMode = strings.TrimSpace(s.PermissionMode)
		t.effort = strings.TrimSpace(s.Effort)
		t.model = strings.TrimSpace(s.Model)
		t.instructions = strings.TrimSpace(s.CustomInstructions)
	}
	if p := body.Policies; p != nil {
		t.allowedTools = normalizeTools(p.AllowedTools)
		if p.RecordIO != nil {
			t.recordIO = *p.RecordIO
		}
		if p.MaxSessionDurationMinutes > 0 {
			t.maxDuration = time.Duration(p.MaxSessionDurationMinutes) * time.Minute
		}
		t.requiresDLP = strings.TrimSpace(p.DLPMode)
	}

	// --- what this launch cannot keep, named field by field ---

	if h := body.Hooks; h != nil && h.declaresAny() {
		// The launch builds argv and env (runtime_bridge.go buildLaunchSpec); it does not
		// write the child's settings, and Claude Code takes hooks from settings files, not
		// from a flag. Provisioning them is the operator's managed-settings posture
		// (the governed hooks/PEP contract) — a different surface, with its own
		// anti-tamper argument. Emitting a settings document from here would mean guessing
		// a schema this repository deliberately never writes: connectors/claude-config
		// reads hook settings for PRESENCE only and says so (feeder.go emitSettingsHooks).
		// A guessed schema that the child ignores is the same defect in a new place.
		t.unenforceable = append(t.unenforceable,
			"hooks: the session launch does not provision hooks into the child (they are an "+
				"operator managed-settings posture, not a launch parameter)")
	}
	if len(body.Connectors) > 0 {
		t.unenforceable = append(t.unenforceable,
			"connectors: a launch binds no connectors to a session; there is no launch-time consumer of this field")
	}
	if t.permissionMode != "" && !validPermissionModes[t.permissionMode] {
		t.unenforceable = append(t.unenforceable, "settings.permission_mode: unknown mode "+strconv.Quote(t.permissionMode))
	}
	if t.effort != "" && !validEffortLevels[t.effort] {
		t.unenforceable = append(t.unenforceable, "settings.effort: unknown effort level "+strconv.Quote(t.effort))
	}
	if t.requiresDLP != "" {
		if _, ok := dlpStrictness[t.requiresDLP]; !ok {
			// This is not hypothetical: two seeded built-ins shipped "classify" and "block",
			// neither of which is a value this product has ever had (off|label|deny).
			t.unenforceable = append(t.unenforceable,
				"policies.dlp_mode: unknown mode "+strconv.Quote(t.requiresDLP)+" (want off|label|deny)")
		} else if dlpStrictness[t.requiresDLP] > 0 {
			// ⛔ THE CORRECTION THAT MATTERS MOST IN THIS FILE, and it reversed my own first
			// answer. I had this as a PRECONDITION: the resolved workspace must already
			// declare at least this posture, else refuse. That check is real, and it is not
			// what the field's name promises.
			//
			// The classifier runs on ONE path — the workspace's own governed file API
			// (workspace.go readFile → classifyContent). A NATIVE launch hands the workspace's
			// host directory to the child as its working directory (runtime_bridge.go), and
			// the contract for that plane says so with all the letters: native does not
			// isolate the filesystem, the process can walk out of the root, and the API jail
			// is a SEPARATE thing. So the child's own Read and Bash never traverse the
			// classifier at all.
			//
			// ⇒ a template that says "strict DLP" and passes a metadata comparison would
			// present a session as DLP-governed while the reads that matter bypass it. That is
			// the exact defect this whole pack exists to remove, rebuilt one field over. A
			// metadata floor is a fine control, but it must not wear this field's name.
			//
			// Refused until the child's file access is ON the enforcement path (container or
			// sandbox isolation, neither wired this release). Found by the Codex sol max
			// contrast, 2026-08-11 — I had shipped it as enforcement.
			t.unenforceable = append(t.unenforceable,
				"policies.dlp_mode="+strconv.Quote(t.requiresDLP)+": a native session reads the workspace "+
					"directly and never traverses the DLP classifier, which runs only on the workspace file API — "+
					"so this cannot be enforced on the child and is refused rather than displayed as governance")
		}
	}
	if p := body.Policies; p != nil && p.RecordIO != nil && !*p.RecordIO {
		// An explicit `false` is DISTINGUISHABLE from omission (the field is *bool) and was
		// being accepted and quietly ignored: recording may only ever go UP, because the
		// launch gate ORs its own CRITICAL floor over it and a template able to switch
		// evidence off would be a way to launder a privileged session past that floor.
		//
		// Silently no-op'ing it was still "accepted and not done". The honest answer is to
		// refuse the term (also found by the contrast).
		t.unenforceable = append(t.unenforceable,
			"policies.record_io=false: a template cannot switch I/O recording off — the launch gate's "+
				"recording floor outranks it, so honoring this would be a promise the gate is free to break")
	}
	if p := body.Policies; p != nil && p.MaxSessionDurationMinutes < 0 {
		t.unenforceable = append(t.unenforceable,
			"policies.max_session_duration_minutes: a negative duration is not a ceiling")
	}
	if p := body.Policies; p != nil && len(p.AllowedTools) > 0 && len(t.allowedTools) == 0 {
		// A list that normalizes to nothing (all entries blank) is an author asking for
		// SOMETHING, and this runtime has no way to know what: an empty allowlist could mean
		// "deny every tool" or "impose no allowlist", and the two are opposites. Refuse
		// rather than pick one — picking the permissive one silently is the failure mode.
		t.unenforceable = append(t.unenforceable,
			"policies.allowed_tools: the list has entries but every one of them is blank; an empty allowlist "+
				"is ambiguous between \"deny every tool\" and \"no allowlist\" and this runtime will not guess")
	}
	if p := body.Policies; p != nil && p.MaxSessionDurationMinutes > maxTemplateSessionMinutes {
		t.unenforceable = append(t.unenforceable,
			"policies.max_session_duration_minutes: exceeds the "+strconv.Itoa(maxTemplateSessionMinutes)+"-minute ceiling")
	}
	if len(t.instructions) > maxTemplateInstructions {
		t.unenforceable = append(t.unenforceable,
			"settings.custom_instructions: "+strconv.Itoa(len(t.instructions))+" bytes exceeds the "+
				strconv.Itoa(maxTemplateInstructions)+"-byte argv budget")
	}
	if len(t.allowedTools) > maxTemplateTools {
		t.unenforceable = append(t.unenforceable,
			"policies.allowed_tools: "+strconv.Itoa(len(t.allowedTools))+" rules exceed the "+
				strconv.Itoa(maxTemplateTools)+"-rule argv budget")
	}
	if strings.ContainsRune(t.instructions, 0) {
		t.unenforceable = append(t.unenforceable,
			"settings.custom_instructions: contains a NUL byte, which cannot be passed as a process argument")
	}
	for _, tool := range t.allowedTools {
		if strings.ContainsRune(tool, 0) {
			t.unenforceable = append(t.unenforceable,
				"policies.allowed_tools: tool "+strconv.Quote(tool)+" contains a NUL byte, which cannot be passed as a process argument")
			continue
		}
		if strings.ContainsAny(tool, ",\n\r") {
			// The allowlist reaches the child as ONE comma-separated argv value, so a comma
			// inside a name would split it into two rules — silently widening the allowlist,
			// which is the one direction that must never happen by accident.
			t.unenforceable = append(t.unenforceable,
				"policies.allowed_tools: tool "+strconv.Quote(tool)+" contains a comma or newline and cannot be expressed as one allow rule")
		}
	}
	if len(t.allowedTools) > 0 && t.permissionMode != "" && t.permissionMode != permModeDontAsk {
		t.unenforceable = append(t.unenforceable,
			"policies.allowed_tools with settings.permission_mode="+strconv.Quote(t.permissionMode)+
				": only "+permModeDontAsk+" is DOCUMENTED to deny a tool that matches no allow rule; the other "+
				"modes either auto-approve a class before the allow rules are read, or leave the outcome to a "+
				"resolver step this runtime wires nothing into")
	}
	return t
}

// declaresAny reports whether the hook block declares at least one entry.
func (h *tplHooks) declaresAny() bool {
	return len(h.PreTool) > 0 || len(h.PostTool) > 0 || len(h.PreSession) > 0 || len(h.PostSession) > 0
}

// normalizeTools trims, drops empties and de-duplicates a tool allowlist, preserving
// the author's order (the argv value is easier to read back that way).
func normalizeTools(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, t := range in {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ---------------------------------------------------------------------------
// The merge.
// ---------------------------------------------------------------------------

// applyTo imposes the template's terms on p and returns the REAL conflicts: one entry
// per field where the request asked for something and the template overrode it. An
// empty result now means "nothing was contradicted", which is what the field always
// claimed and never was.
//
// The template WINS every contest, and that is the point rather than a convenience: it
// is the governed configuration, the request is the caller's preference, and a
// restriction that yields to the thing it restricts is not one. The override is never
// silent — it is returned here, recorded on the run's lifecycle ledger, and (for a mode
// that escalates privilege) still has to pass the CRITICAL approval the launch gate
// opens, which no template can wave through.
//
// It must run BEFORE validateCreate: that helper defaults an empty permission_mode to
// "default", and a defaulted value is indistinguishable from a chosen one, so merging
// after it would report a conflict against a value the caller never asked for.
func (t tplTerms) applyTo(p *CreateRunParams) []mergeConflict {
	var conflicts []mergeConflict
	set := func(field string, cur *string, want string) {
		if want == "" || *cur == want {
			return
		}
		if strings.TrimSpace(*cur) != "" {
			conflicts = append(conflicts, mergeConflict{Field: field, OldValue: *cur, NewValue: want})
		}
		*cur = want
	}

	// A tool allowlist carries its own enforcing mode with it (see permModeDontAsk).
	// templateTerms has already refused a template that asks for a different one, so this
	// can only be filling in a mode the template left unstated.
	wantMode := t.permissionMode
	if len(t.allowedTools) > 0 {
		wantMode = permModeDontAsk
	}
	set("permission_mode", &p.PermissionMode, wantMode)
	set("effort", &p.Effort, t.effort)
	set("model", &p.Model, t.model)

	if len(t.allowedTools) > 0 {
		if len(p.AllowedTools) > 0 && !sameTools(p.AllowedTools, t.allowedTools) {
			conflicts = append(conflicts, mergeConflict{
				Field: "allowed_tools", OldValue: p.AllowedTools, NewValue: t.allowedTools,
			})
		}
		p.AllowedTools = t.allowedTools
	}
	if t.instructions != "" {
		if strings.TrimSpace(p.Instructions) != "" && p.Instructions != t.instructions {
			conflicts = append(conflicts, mergeConflict{
				Field: "custom_instructions", OldValue: p.Instructions, NewValue: t.instructions,
			})
		}
		p.Instructions = t.instructions
	}
	if t.recordIO && !p.RecordRequested {
		// Not a conflict: recording only ever goes from off to on here, and turning
		// evidence ON contradicts nothing the caller is entitled to.
		p.RecordRequested = true
	}
	if t.maxDuration > 0 {
		if p.MaxDuration > 0 && p.MaxDuration != t.maxDuration {
			conflicts = append(conflicts, mergeConflict{
				Field:    "max_session_duration_minutes",
				OldValue: int64(p.MaxDuration / time.Minute),
				NewValue: int64(t.maxDuration / time.Minute),
			})
		}
		p.MaxDuration = t.maxDuration
	}
	return conflicts
}

// unenforceableForTransport names the terms this template cannot keep on the transport
// the launch actually uses. It is separate from templateTerms because the reduction is a
// pure value with no launch in it, and this question has no answer without one.
//
// ⛔ The case that exists, and it was shipping as a false EVIDENCE claim: a
// remote-control session relays its I/O to Anthropic's cloud and Olivares never sees a
// frame of it (runtime_ports.go: the transport is lifecycle-only, honestly declared,
// never faked). The bridge can only offer the recorder what comes out of the process, so
// `record_io: true` on that transport anchors an empty chain — while the run row and the
// governance panel both say the session is recorded. A CRITICAL launch is exactly where
// somebody relies on that. Refused instead (Codex sol max contrast, 2026-08-11).
func unenforceableForTransport(t tplTerms, transport Transport) []string {
	if t.recordIO && transport == TransportRemoteControl {
		return []string{
			"policies.record_io=true with transport=" + string(TransportRemoteControl) +
				": this transport relays the session's I/O to the model provider and Olivares bridges none of it, " +
				"so there is nothing to anchor as evidence — the recording would be a claim with an empty chain",
		}
	}
	return nil
}

// sameTools reports whether two allowlists hold the same members, order-insensitively.
func sameTools(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Resolution + the launch-side application.
// ---------------------------------------------------------------------------

// loadTemplate reads one template by id within the tenant, refusing an ARCHIVED one.
// An archived template is hidden from the catalog and must not keep governing launches
// through a reference somebody kept — the operator retired it.
func (m *Module) loadTemplate(ctx context.Context, tenant model.TenantID, id string) (templateDTO, error) {
	tid := model.ID(strings.TrimSpace(id))
	if tid.IsZero() {
		return templateDTO{}, badRequest("invalid template_id")
	}
	var dto templateDTO
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, rerr := sc.Ext(templateKind)
		if rerr != nil {
			return rerr
		}
		rec, rerr := repo.Get(ctx, tid)
		if rerr != nil {
			return rerr
		}
		dto = toTemplateDTO(rec)
		return nil
	})
	if err != nil {
		return templateDTO{}, err
	}
	if dto.ArchivedAt != "" {
		return templateDTO{}, &runErr{http.StatusUnprocessableEntity, "template " + dto.Name + " is archived and cannot govern a launch"}
	}
	return dto, nil
}

// unenforceableErr refuses a launch (or an apply) whose template declares terms this
// runtime cannot keep, naming each one. 422 rather than 400: the request is well-formed
// and the template exists — what cannot be done is honor it, which is a semantic
// refusal and needs to read differently from a typo in the body.
func unenforceableErr(name string, terms []string) error {
	return &runErr{
		status: http.StatusUnprocessableEntity,
		msg: "template " + strconv.Quote(name) + " declares terms this launch cannot enforce, so it is refused rather than " +
			"applied in part: " + strings.Join(terms, "; "),
	}
}

// applyLaunchTemplate resolves p.TemplateID and imposes its terms on p, returning the
// resolved template and the real conflicts. It is the ONLY path by which a template
// reaches a launch, and it runs inside createRun/resumeRun BEFORE validation and before
// the governance gates — so what the budget, the CRITICAL determination and the HITL
// approval all judge is the RESTRICTED launch, never the one that was asked for.
//
// A run with no template_id is untouched: it returns immediately, no template is read,
// no parameter changes, and no conflict is reported. That is load-bearing — this pack
// must not move a single existing launch.
func (m *Module) applyLaunchTemplate(ctx context.Context, tenant model.TenantID, p *CreateRunParams) (templateDTO, []mergeConflict, error) {
	p.TemplateID = strings.TrimSpace(p.TemplateID)
	if p.TemplateID == "" {
		return templateDTO{}, nil, nil
	}
	if m.data == nil {
		return templateDTO{}, nil, &runErr{http.StatusServiceUnavailable, "templates are not available on this node"}
	}
	dto, err := m.loadTemplate(ctx, tenant, p.TemplateID)
	if err != nil {
		return templateDTO{}, nil, err
	}
	terms := templateTerms(dto.Body)
	// The transport is already decided by the caller and never by the template, so the
	// transport-dependent refusals are asked here, where a launch exists.
	if bad := append(terms.unenforceable, unenforceableForTransport(terms, p.Transport)...); len(bad) > 0 {
		return templateDTO{}, nil, unenforceableErr(dto.Name, bad)
	}
	conflicts := terms.applyTo(p)
	// The template's identity AND revision travel with the launch. The revision is what
	// binds a governed human approval to the terms that were approved: templates are
	// mutable, they are re-read on every launch and resume, and without the version in the
	// intent an approval opened for "allow Read" could be spent on a template that now
	// says "allow Read, Bash" with every other launch parameter unchanged. That is the
	// exact anti-TOCTOU boundary the approval's plan hash exists to draw (Codex sol max
	// contrast, 2026-08-11).
	p.TemplateVersion = dto.Version
	return dto, conflicts, nil
}
