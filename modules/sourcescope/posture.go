// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// posture.go implements the F2/F5 enforcement-POSTURE controls (ADR-0022 §5): a
// mutation that RELAXES enforcement — widens who may reach a source, or weakens a
// restriction — is never applied by a single actor. It is recorded as a PENDING
// posture-change request and applied only when a SECOND, distinct principal approves it
// (dual-control), with the whole two-person trail in the immutable audit ledger. TIGHTENING
// changes bypass this and apply immediately (audited). This mirrors AWS's one-way, audited
// qbusiness:DisableAclOnDataSource (F2) and gives the mutable-yet-governed posture that
// differentiates us from Google's immutable data-store ACL (F5).

// posture operations and request statuses.
const (
	postureOpCreate          = "create"
	postureOpUpdate          = "update"
	postureOpDelete          = "delete"
	postureOpDisableScoping  = "disable_scoping"
	postureOpGuardPublicOnly = "public_only"

	// the ASSIGNMENT surface. sourcescope decides access in TWO places, not one:
	// a source's bindings (above) and a connector's workspace assignments, whose
	// ConnectorAssigned is the deny-closed gate for every UNCONFINED source
	// (resolver.go:257-264). Until the second surface was classified by nothing at
	// all — three writers, no gate — so the same relaxation that needs two people as a
	// binding delete was a single-actor 204 as an assignment delete.
	postureOpAssignCreate = "assignment_create"
	postureOpAssignUpdate = "assignment_update"
	postureOpAssignDelete = "assignment_delete"

	postureStatusPending  = "pending"
	postureStatusApproved = "approved"
	postureStatusRejected = "rejected"
)

// assignmentSourceType is the source_type a posture request over the ASSIGNMENT surface
// carries. It is deliberately NOT a member of validSourceTypes (binding.go:29-31) — an
// assignment names a connector, which is not one of the five source kinds a binding may
// target — and applyPosture routes on the OP, never on this value.
const assignmentSourceType = "connector"

// assignmentOps is the set of posture ops that act on the assignment surface. It decides
// which side of the wire shape toPostureRequestDTO decodes, and nothing else.
var assignmentOps = map[string]bool{
	postureOpAssignCreate: true, postureOpAssignUpdate: true, postureOpAssignDelete: true,
}

// postureRequestDTO is the wire shape of a pending/decided posture-change request.
type postureRequestDTO struct {
	ID         string      `json:"id,omitempty"`
	SourceType string      `json:"source_type"`
	SourceRef  string      `json:"source_ref"`
	Op         string      `json:"op"`
	TargetID   string      `json:"target_id,omitempty"`
	Proposed   *bindingDTO `json:"proposed,omitempty"`
	// ProposedAssignment carries the proposal for an ASSIGNMENT op, in its OWN field
	//. The stored proposal is one JSON column for both surfaces, and the two DTOs
	// share the `enabled` and `note` tags, so decoding an assignment proposal into a
	// bindingDTO does not fail — it SUCCEEDS and yields a binding with an empty
	// source_type and scope_tree carrying the assignment's enabled flag. A reviewer would
	// be shown a coherent-looking binding that is not what was proposed, which is the E-4
	// defect (an approval that does not say what it approved) reintroduced on the wire.
	ProposedAssignment *assignmentDTO `json:"proposed_assignment,omitempty"`
	Reason             string         `json:"reason,omitempty"`
	Proposer           string         `json:"proposer,omitempty"`
	// ProposerUser / DecidedByUser are the HUMAN behind each leg, surfaced on the
	// wire and not only in the ledger: a reviewer looking at the pending queue must be able
	// to see that "user:U proposed, token:X decided" is one person, without joining to the
	// token table to find out.
	ProposerUser  string `json:"proposer_user,omitempty"`
	DecidedByUser string `json:"decided_by_user,omitempty"`
	Status        string `json:"status"`
	DecidedBy     string `json:"decided_by,omitempty"`
	Note          string `json:"note,omitempty"`
	GuardProfile  string `json:"guard_profile,omitempty"`
}

func toPostureRequestDTO(rec model.Record) postureRequestDTO {
	d := postureRequestDTO{
		ID:            rec.String(model.ColID),
		SourceType:    rec.String(colPRSourceType),
		SourceRef:     rec.String(colPRSourceRef),
		Op:            rec.String(colPROp),
		TargetID:      rec.String(colPRTargetID),
		Reason:        rec.String(colPRReason),
		Proposer:      rec.String(colPRProposer),
		ProposerUser:  rec.String(colPRProposerUser),
		DecidedByUser: rec.String(colPRDecidedByUser),
		Status:        rec.String(colPRStatus),
		DecidedBy:     rec.String(colPRDecidedBy),
		Note:          rec.String(colPRNote),
	}
	if p := rec.String(colPRProposed); p != "" {
		if assignmentOps[d.Op] {
			var a assignmentDTO
			if json.Unmarshal([]byte(p), &a) == nil {
				d.ProposedAssignment = &a
			}
		} else {
			var b bindingDTO
			if json.Unmarshal([]byte(p), &b) == nil {
				d.Proposed = &b
			}
		}
	}
	if d.Op == postureOpGuardPublicOnly {
		d.GuardProfile = GuardProfilePublicOnly
	}
	return d
}

// postureScope is a binding's ENFORCEMENT scope: the (tree, ref) pair naming the POPULATION
// of actors the row acts on, in the canonical form the resolver matches. The workspace tree's
// empty ref means the tenant default workspace — resolveScope stores it that way
// (binding.go:221-226) and containsActor reads it that way (resolver.go:437-442) — so "" and
// the default slug are the SAME population and a write that only spells it differently has
// not moved anything.
type postureScope struct{ tree, ref string }

func scopeOf(d bindingDTO) postureScope {
	ref := d.ScopeRef
	if d.ScopeTree == scopeWorkspace && ref == "" {
		ref = model.DefaultWorkspaceSlug
	}
	return postureScope{tree: d.ScopeTree, ref: ref}
}

func (s postureScope) String() string { return s.tree + ":" + s.ref }

// classifyUpdate reports whether updating a binding from old→updated RELAXES enforcement
// (ADR-0022 §5), and a human reason for the audit/UI. otherEnabledAllows is the number of
// OTHER enabled allow bindings on the same source, to detect an unconfine when the last
// allow is disabled.
//
// It is a WHITELIST: the writes that provably cannot widen who may reach the source
// are ENUMERATED below and everything else — including any shape this function does not
// recognize — is a relaxation. It was a blacklist until (it listed the relaxing shapes
// and applied the rest immediately), and a blacklist leaks by construction. It leaked in
// three places HERE, plus a fourth in creation, which was classified by nothing at all
// (classifyCreate):
//
//	(1) forbid→forbid with a different scope was on no list, so SHRINKING a standing
//	    restriction — which un-denies everyone it stops covering — applied in the act, by one
//	    actor, while DELETING that same forbid needed two people (classifyDelete). The gate
//	    was bypassable by editing instead of deleting.
//	(2) an allow moved to a more-specific TREE was taken for a pure narrowing via
//	    specificityRank, but workspace:eng → user:U ranks "more specific" and reaches a user
//	    who was out of scope a moment earlier.
//	(3) the LAST enabled allow turned into a forbid read as a tightening of that ROW while
//	    removing the CONFINEMENT the row also carried, leaving the source global. (1) and (2)
//	    come from reading a scope operation with the polarity of an allow; (3) comes from
//	    reading a row's effect and forgetting what else the row was holding up.
//
// THE POLICY, stated once. Two scopes are comparable only when they are the SAME scope.
// specificityRank (resolver.go:392-393) is a total order over TREES for CREDENTIAL selection;
// it is not a containment relation and is deliberately not consulted here — `role:admin` and
// `user_group:g1`, `workspace:eng` and `agent_group:core`, a folder and its child are
// different POPULATIONS and neither side contains the other. Membership is also not fixed:
// a superset proved by reading rows today is not a superset tomorrow. So the certificate for
// "this write cannot widen" is identity of the scope and nothing weaker: if the scope of an
// ENFORCING row changes at all, it is a relaxation. "I cannot compare them" resolves to
// relaxation, because a false positive costs one extra approval and a false negative is the
// bypass of a two-person gate.
//
// The POLARITY of a scope change depends on the EFFECT — the trap ADR-0022 §5 fell into. For
// an allow a smaller scope reaches fewer actors; for a forbid it PROTECTS fewer. Both are
// relaxations here, for opposite reasons.
//
// This function may only ever ADD a gate relative to the pre classifier, never remove
// one; posture_classify_test.go pins that as a property over the whole input space.
func classifyUpdate(old, updated bindingDTO, otherEnabledAllows int) (bool, string) {
	oldEff, newEff := normalizeEffect(old.Effect), normalizeEffect(updated.Effect)
	oldScope, newScope := scopeOf(old), scopeOf(updated)
	moved := oldScope != newScope

	// A restriction becoming a grant inverts the row's declared intent. Gated even for a
	// PARKED row (disabled on both sides), which is a DELIBERATE false positive and the only
	// one here: such a row enforces nothing, so the resolver cannot observe the write at all.
	// It is kept because the two-person record should capture the moment the intent changed,
	// not only the moment it took effect; because its cost is bounded (the row still cannot
	// grant anything until a SECOND, separately gated write enables it); and because removing
	// it would be a gate removal in a change whose value rests on removing none. It is the
	// standing candidate if the approval queue ever proves noisy.
	if oldEff == effectForbid && newEff == effectAllow {
		return true, "forbid changed to allow (a restriction becomes a grant)"
	}

	switch {
	// NEUTRAL — the row enforces nothing before and nothing after. loadEnabledBindings skips
	// a disabled row (resolver.go:540-543), so scope, effect, note and credential are all
	// free to change: no actor's decision moves.
	case !old.Enabled && !updated.Enabled:
		return false, ""

	// A parked row is switched ON. A forbid adds a restriction — TIGHTENING. An allow adds a
	// grant, which is relaxing (it also confines an unconfined source, but the conservative
	// reading is the pre one and it is the safe direction).
	case !old.Enabled && updated.Enabled:
		if newEff == effectForbid {
			return false, ""
		}
		return true, "allow enabled (a grant added)"

	// --- the row WAS enforcing (old.Enabled) --------------------------------------------

	// FORBID: it denies exactly the actors its scope names. Every actor it stops naming is
	// un-denied by this one write, so the ONLY ordinary write is the one that leaves an
	// ENABLED forbid over the SAME population — i.e. a note or credential edit.
	case oldEff == effectForbid:
		if !updated.Enabled {
			return true, "forbid disabled (a restriction removed)"
		}
		if moved {
			return true, "forbid scope changed, un-denying part of its population (" +
				oldScope.String() + " → " + newScope.String() + ")"
		}
		return false, ""

	// The row STOPS BEING AN ENABLED ALLOW — switched off, or turned into a forbid.
	//
	// An enabled allow carries TWO things, and it is easy to see only the first: the grant to
	// its own population, AND the CONFINEMENT that keeps the source non-global. A source is
	// confined iff it has ≥1 enabled ALLOW binding (hasAllowBinding, resolver.go:350-358);
	// lose the last one and the source is unconfined, which means reachable by EVERYONE
	// (resolver.go:327-337). That is the largest relaxation in the module and it does not
	// care which of the two words caused it: `enabled:false` and `effect:forbid` produce a
	// bit-identical enforcement state, because loadEnabledBindings skips a disabled row and
	// hasAllowBinding skips a forbid one. Classifying them differently would gate one spelling
	// of a write and wave the other through — which is the defect over again, in the leg
	// that was supposed to be the safe one.
	case newEff == effectForbid || !updated.Enabled:
		if otherEnabledAllows == 0 {
			if newEff == effectForbid {
				return true, "the last enabled allow became a forbid (the source becomes global)"
			}
			return true, "last allow disabled (the source becomes global)"
		}
		// A non-last allow: the source stays confined with one grant fewer, and a forbid can
		// only subtract from here. TIGHTENING, whatever scope it lands on.
		return false, ""

	// ALLOW: a still-enabled grant whose population changes. Narrower, broader or sideways
	// are indistinguishable without a containment relation the trees do not have (see THE
	// POLICY above), so any move is a relaxation.
	case moved:
		return true, "allow scope moved to a different population (" +
			oldScope.String() + " → " + newScope.String() + ")"

	// NEUTRAL — a still-enabled allow over the SAME population: a note or credential edit.
	// The credential locator selects WHICH reference an already-authorized actor receives,
	// never WHETHER it is authorized (resolver.go:341-346).
	case oldEff == effectAllow && newEff == effectAllow && updated.Enabled:
		return false, ""
	}

	// THE WHITELIST DEFAULT, and the load-bearing half of the inversion. The cases above
	// are exhaustive over the three dimensions that decide access (effect, enabled, scope),
	// so nothing reaches this line today and posture_classify_test.go proves it. It stands
	// for the next edit: a shape this function stops recognizing is gated, not applied.
	return true, "unclassified posture change (conservative default)"
}

// classifyCreate reports whether CREATING a binding RELAXES enforcement (ADR-0022 §5), and
// a human reason for the audit/UI. otherEnabledAllows is the number of enabled ALLOW
// bindings the source ALREADY carries — the confinement signal, and the whole decision.
//
// Creation was classified by NOTHING until: handleCreateBinding wrote the row and
// returned 201 whatever it was. That is the third leak of the one class this session closed
// — the ADR's general rule ("a mutation that could widen who may reach a source is a
// relaxation") never stopped applying to create; §5's enumeration simply named "adding a
// forbid" as tightening and forgot to say anything about adding an ALLOW.
//
// The whitelist, and why the confinement signal is the whole of it:
//
//   - a FORBID only ever subtracts access: tightening, whatever its scope or enabled state.
//
//   - a row created DISABLED is skipped by loadEnabledBindings: it enforces nothing.
//
//   - the FIRST enabled allow CONFINES an unconfined source: after it, only the actors it
//     names may reach the source. That is the largest tightening in the module and it is
//     deliberately NOT gated — bringing a source under governance must never need two people,
//     or the safe move is the expensive one.
//
//     THE CAVEAT NAMED, CLOSED HERE BY (assignmentRows). "Before it, the source was
//     global" holds for an unbound source but NOT for one whose ref carries assignment
//     rows: ConnectorAssigned is consulted only while the source has no allow binding
//     (resolver.go:257-264), so the first allow switches that gate OFF. Measured and
//     reproduced: connector assigned to `sales` only, agent in `eng` denied; POST the first
//     allow workspace:eng and eng resolves. The first allow both confines AND widens, so it
//     is gated exactly when there is an assignment gate for it to switch off.
//
//     It is gated even when the allow names the SAME workspace the assignment set already
//     grants, which looks like a false positive and is a deliberate one. Proving no widening
//     would mean comparing a SET of workspace slugs against one binding scope — a containment
//     relation these trees do not have (see THE POLICY on classifyUpdate) — and the two sets
//     are not stable anyway: an assignment row written a second later moves the population
//     the certificate was issued against. One extra approval is the cheaper error.
//
//     NOT keyed on source_type: there is no `connector` source type (validSourceTypes,
//     binding.go:29-31) and the resolver does not consult one — it passes the source REF to
//     ConnectorAssigned whatever the type. The condition is therefore "this ref has assignment
//     rows", which is what the gate itself reads.
//
//   - every FURTHER enabled allow on an ALREADY-CONFINED source ADDS a grant — a population
//     that could not reach the source now can. Gated.
func classifyCreate(created bindingDTO, otherEnabledAllows, assignmentRows int) (bool, string) {
	if normalizeEffect(created.Effect) == effectForbid {
		return false, ""
	}
	if !created.Enabled {
		return false, ""
	}
	if otherEnabledAllows == 0 {
		if assignmentRows > 0 {
			return true, "the first allow on an assigned source switches the connector " +
				"assignment gate off (it can admit a workspace the assignment set denies)"
		}
		return false, "" // the first allow confines the source
	}
	return true, "allow added to an already-confined source (a grant added for a new population)"
}

// --- the ASSIGNMENT surface ---------------------------------------------------
//
// What an assignment row decides, stated once, because the whole classifier below is a
// reading of ConnectorAssigned (assignment.go:286-300) and nothing else. For a connector C,
// let rows(C) be its assignment rows and V(C) the workspaces that may reach it:
//
//	rows(C) = ∅        ⇒  V(C) = EVERY workspace          (unassigned ⇒ globally visible)
//	rows(C) ≠ ∅        ⇒  V(C) = { r.workspace : r ∈ rows(C), r.enabled }
//
// A write RELAXES iff V(C) GROWS. Three consequences that are easy to get wrong, and the
// first two are why this is not a copy of the binding classifier:
//
//   - THE CONFINEMENT SIGNAL COUNTS ROWS, NOT ENABLED ROWS. `len(allAssign) == 0` is tested
//     before the enabled filter (assignment.go:291-293), so a connector whose only row is
//     DISABLED is not global — it is denied EVERYWHERE. Deleting that disabled row makes it
//     global. On the binding surface the mirror-image write is neutral (classifyDelete
//     discards a disabled row outright), so a classifier that reused `otherEnabledAllows`
//     here would wave through the single widest write on this surface.
//
//   - THE SCOPE CANNOT MOVE, so there is no analog of the forbid-shrink. connector_name
//     and workspace_ref are forced back to the stored row on update (assignment.go:206-207);
//     the classifier still gates a move it cannot see today, because "the handler happens to
//     prevent it" is a property of the handler, not of the classifier.
//
//   - THERE IS NO EFFECT COLUMN. Every row is a grant; `enabled` is the whole polarity. So
//     the trap — a scope operation whose polarity inverts with the effect — cannot arise
//     here, and `mode` (r|rw) is not a polarity either: nothing in this product reads it for
//     access (its only consumer outside assignment.go is the AgentCore export,
//     agentcoreexport.go:45). An external system that enforces that export's `access` field
//     would see r→rw as a widening; that is named here and deliberately not gated, because
//     gating on a field this product does not enforce would be a claim we cannot keep.
//
//     `mode` is not alone in that category and the other half was missing until an
//     adversarial pass named it: creating the FIRST enabled row also emits a new
//     `Effect:"permit"` item into the AgentCore export (agentcoreexport.go:38-46). In THIS
//     product that create is the tightening that confines the connector; in an external
//     policy engine consuming the export it is a permit that did not exist before, applied
//     by one actor. Same reason for not gating it — we do not enforce that export — and the
//     same obligation to say so rather than let the asymmetry read as an oversight.
//
//   - THE FALSE GATE THIS SURFACE PAYS, measured and previously undocumented. The three
//     classifiers below read no BINDING state, so an assignment write is gated even when
//     every source sharing that ref is confined and the assignment gate therefore decides
//     nothing. Proving the write inert would mean showing an enabled allow binding exists
//     for that ref under all five validSourceTypes — a read whose answer stops being true
//     the moment any of those bindings is deleted. The conservative direction is the right
//     one, and its cost is named here so it is not rediscovered as a bug.
//
// otherRows is the number of assignment rows for the connector OTHER than the one being
// written — the confinement signal, counted over ALL rows per the first point above.

// classifyAssignmentCreate reports whether creating an assignment RELAXES enforcement.
//
// The whitelist:
//
//   - THE FIRST row (otherRows == 0) takes the connector from globally visible to at most one
//     workspace. That is the largest tightening on this surface and is not gated, for the same
//     reason the first allow binding is not: bringing a source under governance must never
//     need two people, or the safe move is the expensive one.
//
//   - a row created DISABLED adds nothing to V(C): the connector is already non-global
//     (otherRows > 0) and a disabled row contributes no workspace.
//
// Everything else — an ENABLED row added to an already-assigned connector — admits a
// workspace that could not reach the connector a moment earlier. The unique index on
// (tenant, connector_name, workspace_ref) (schema.go:241-244) means that workspace cannot
// already be in V(C) through some other row, so this is always a strict growth.
func classifyAssignmentCreate(created assignmentDTO, otherRows int) (bool, string) {
	if otherRows == 0 {
		return false, "" // the first assignment confines the connector
	}
	if !created.Enabled {
		return false, ""
	}
	return true, "assignment added to an already-assigned connector (workspace " +
		created.WorkspaceRef + " gains access to " + created.ConnectorName + ")"
}

// classifyAssignmentUpdate reports whether updating an assignment RELAXES enforcement.
// rows(C) is unchanged by an update, so the global flip cannot happen here and the whole
// decision is whether this row's workspace ENTERS V(C).
func classifyAssignmentUpdate(old, updated assignmentDTO) (bool, string) {
	// The natural key is immutable and the handler forces it back to the stored row before
	// this runs (assignment.go:206-207), so this branch is unreachable today. It is kept
	// because a move would silently transplant a grant onto a different population, and the
	// classifier must not depend on a caller's discipline to be sound. If it ever fires, the
	// gate is the correct answer, not a panic.
	if old.ConnectorName != updated.ConnectorName || old.WorkspaceRef != updated.WorkspaceRef {
		return true, "assignment moved to a different connector/workspace pair (" +
			old.ConnectorName + "→" + old.WorkspaceRef + " ⇒ " +
			updated.ConnectorName + "→" + updated.WorkspaceRef + ")"
	}
	switch {
	// NEUTRAL — the row is parked before and after. It contributes no workspace to V(C) in
	// either state, so note and mode are free to move.
	case !old.Enabled && !updated.Enabled:
		return false, ""

	// The row is switched ON: its workspace ENTERS V(C).
	case !old.Enabled && updated.Enabled:
		return true, "assignment enabled (workspace " + updated.WorkspaceRef +
			" gains access to " + updated.ConnectorName + ")"

	// The row is switched OFF: its workspace LEAVES V(C). rows(C) is untouched, so the
	// connector cannot flip global here — that needs a delete. TIGHTENING.
	case old.Enabled && !updated.Enabled:
		return false, ""

	// NEUTRAL — enabled on both sides over the same pair: a note or mode edit. See the
	// `mode` note above for why r→rw is named and deliberately not gated.
	case old.Enabled && updated.Enabled:
		return false, ""
	}

	// THE WHITELIST DEFAULT, and the reason this is a switch rather than the two ifs it
	// started as. The cases above are exhaustive over the ONE field that decides access
	// today, so nothing reaches this line and assignmentposture_test.go pins that. It stands
	// for the next edit: the day assignmentDTO grows a second access-deciding field — an
	// effect column, or `mode` becoming enforced — a blacklist ending in `return false`
	// would apply that field's every combination silently, which is the exact shape
	// classifyUpdate's own doc calls "leaks by construction". Reviewed and reshaped after an
	// adversarial correctness pass named it as a future hole rather than a present one.
	return true, "unclassified assignment change (conservative default)"
}

// classifyAssignmentDelete reports whether deleting an assignment RELAXES enforcement.
//
// Deleting the LAST row empties rows(C), which does not merely shrink V(C) — it flips the
// connector back to GLOBALLY VISIBLE (assignment.go:291-293). That is the widest single
// relaxation in the module, wider than deleting the last allow binding, and it applied on a
// 204 with one actor. It is gated whatever the row's `enabled` state, because the flip is
// driven by the row COUNT and a disabled last row reaches it just as well.
func classifyAssignmentDelete(deleted assignmentDTO, otherRows int) (bool, string) {
	if otherRows == 0 {
		return true, "the last assignment was deleted (connector " + deleted.ConnectorName +
			" becomes visible to EVERY workspace)"
	}
	if !deleted.Enabled {
		return false, "" // a disabled row contributed no workspace to V(C)
	}
	return false, "" // a non-last enabled row: the connector keeps its other workspaces
}

// countOtherAssignments counts the assignment rows for a connector other than excludeID.
// It counts rows REGARDLESS of `enabled` — the confinement signal ConnectorAssigned tests
// is the row count, not the enabled count (assignment.go:291-293). It also reports whether
// `want` is already taken, so the create path can refuse a duplicate BEFORE classifying it:
// a proposal the unique index will reject later is one an approver cannot rescue.
func countOtherAssignments(ctx context.Context, sc store.Scope, connectorName, excludeID, want string) (other int, duplicate bool, err error) {
	recs, err := allExt(ctx, sc, assignmentKind, eq(colAssignConnector, connectorName))
	if err != nil {
		return 0, false, err
	}
	for _, rec := range recs {
		if rec.String(model.ColID) == excludeID {
			continue
		}
		other++
		if want != "" && rec.String(colAssignWorkspace) == want {
			duplicate = true
		}
	}
	return other, duplicate, nil
}

// classifyDelete reports whether deleting a binding RELAXES enforcement: removing an
// enabled forbid (a restriction), or removing the last enabled allow (the source becomes
// global). Deleting an already-disabled binding changes no enforcement.
func classifyDelete(deleted bindingDTO, otherEnabledAllows int) (bool, string) {
	if !deleted.Enabled {
		return false, ""
	}
	if normalizeEffect(deleted.Effect) == effectForbid {
		return true, "forbid deleted (a restriction removed)"
	}
	if otherEnabledAllows == 0 {
		return true, "last allow deleted (the source becomes global)"
	}
	return false, ""
}

// countOtherEnabledAllows counts the enabled ALLOW bindings for a source other than
// excludeID — the confinement signal used to detect an unconfine.
func countOtherEnabledAllows(ctx context.Context, sc store.Scope, sourceType, sourceRef, excludeID string) (int, error) {
	bs, err := loadEnabledBindings(ctx, sc, sourceType, sourceRef)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, b := range bs {
		if b.id.String() == excludeID || b.isForbid() {
			continue
		}
		n++
	}
	return n, nil
}

// createPostureRequest records a pending relaxation and self-audits the proposal, in the
// caller's transaction. It never applies the change — approval does.
func (m *Module) createPostureRequest(ctx context.Context, sc store.Scope, mc api.ModuleContext, op, sourceType, sourceRef, targetID, reason string, proposed *bindingDTO) (postureRequestDTO, error) {
	proposedJSON := ""
	if proposed != nil {
		b, merr := json.Marshal(proposed)
		if merr != nil {
			return postureRequestDTO{}, merr
		}
		proposedJSON = string(b)
	}
	return m.createPostureRequestJSON(ctx, sc, mc, op, sourceType, sourceRef, targetID, reason, proposedJSON)
}

// createAssignmentPostureRequest is the same, for a proposal on the ASSIGNMENT surface
//. The two surfaces share one request table and one approval flow — the dual-control
// machinery was never binding-specific — and differ only in the shape stored in `proposed`.
func (m *Module) createAssignmentPostureRequest(ctx context.Context, sc store.Scope, mc api.ModuleContext, op, targetID, reason string, proposed *assignmentDTO) (postureRequestDTO, error) {
	proposedJSON := ""
	if proposed != nil {
		b, merr := json.Marshal(proposed)
		if merr != nil {
			return postureRequestDTO{}, merr
		}
		proposedJSON = string(b)
	}
	connector := ""
	if proposed != nil {
		connector = proposed.ConnectorName
	}
	return m.createPostureRequestJSON(ctx, sc, mc, op, assignmentSourceType, connector, targetID, reason, proposedJSON)
}

// createPostureRequestJSON is the one writer of a pending request. The proposal arrives
// ALREADY marshaled rather than as an `any`: a typed nil pointer boxed into an interface is
// non-nil, so an `any` parameter would silently store the string "null" for a delete op and
// hand the approver a proposal that decodes to a zero-valued row.
func (m *Module) createPostureRequestJSON(ctx context.Context, sc store.Scope, mc api.ModuleContext, op, sourceType, sourceRef, targetID, reason, proposedJSON string) (postureRequestDTO, error) {
	repo, err := sc.Ext(postureRequestKind)
	if err != nil {
		return postureRequestDTO{}, err
	}
	rec, err := repo.Create(ctx, model.Record{
		colPRSourceType: sourceType, colPRSourceRef: sourceRef,
		colPROp: op, colPRTargetID: targetID, colPRProposed: proposedJSON,
		colPRReason: reason, colPRProposer: mc.Principal.Actor(),
		colPRProposerUser: auth.PersonRefOf(mc.Principal).User,
		colPRStatus:       postureStatusPending, colPRDecidedBy: "", colPRDecidedByUser: "",
	})
	if err != nil {
		return postureRequestDTO{}, err
	}
	out := toPostureRequestDTO(rec)
	if err := auditPosture(ctx, sc, mc, "propose", out); err != nil {
		return postureRequestDTO{}, err
	}
	return out, nil
}

// auditPosture appends a posture-lifecycle audit event (propose | approve | reject),
// attributed to the real principal, in the caller's transaction.
func auditPosture(ctx context.Context, sc store.Scope, mc api.ModuleContext, verb string, pr postureRequestDTO) error {
	_, err := sc.Audit().Append(ctx, model.AuditDraft{
		Actor:      mc.Principal.Actor(),
		ActorKind:  mc.Principal.ActorKind(),
		Action:     "sourcescope.posture." + verb,
		TargetKind: postureRequestKind,
		Meta: map[string]any{
			"op": pr.Op, "source_type": pr.SourceType, "source_ref": pr.SourceRef,
			"target_id": pr.TargetID, "reason": pr.Reason, "status": pr.Status,
			"guard_profile": pr.GuardProfile,
			// BOTH identities on BOTH legs, the breakglass activated_by /
			// activated_by_user pattern. Actor() alone made a self-approval read as two
			// actors — "user:U" proposed and "token:X" decided — so the ledger showed a
			// clean two-person trail and the only way to see it was one person was a join
			// against core.api_token. A trail that needs a join to expose a bypass is not
			// a trail.
			"proposer": pr.Proposer, "proposer_user": pr.ProposerUser,
			"decided_by": pr.DecidedBy, "decided_by_user": pr.DecidedByUser,
		},
	})
	return err
}

// handleDisableScoping proposes disabling ALL scoping for a source (it becomes global).
// This one-way relaxation is always dual-controlled: the handler records a pending
// request that is applied only on approval by a second principal.
func (m *Module) handleDisableScoping(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in struct {
		SourceType string `json:"source_type"`
		SourceRef  string `json:"source_ref"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	in.SourceType = strings.TrimSpace(strings.ToLower(in.SourceType))
	in.SourceRef = strings.TrimSpace(in.SourceRef)
	if !validSourceTypes[in.SourceType] || in.SourceRef == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("source_type and source_ref are required"))
		return
	}
	var out postureRequestDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		// The reason an APPROVER reads has to be what the code does. It used to say the
		// source "becomes global", full stop, and that is false for a ref carrying
		// assignment rows: disabling the bindings UNCONFINES the source, which hands it back
		// to ConnectorAssigned (resolver.go:257-264) rather than opening it. Measured — an
		// unassigned workspace still resolves "connector not assigned to workspace
		// (deny-closed)" after approval. The enforcement was right; the sentence was not,
		// and a two-person control whose copy overstates the change teaches approvers that
		// the text is decoration.
		reason := "disable all scoping for the source (it becomes global) — a one-way relaxation"
		if assigned, aerr := allExt(r.Context(), sc, assignmentKind, eq(colAssignConnector, in.SourceRef)); aerr != nil {
			return aerr
		} else if len(assigned) > 0 {
			reason = "disable all scoping for the source — a one-way relaxation. The source " +
				"becomes UNCONFINED, not global: its ref carries connector assignment rows, so " +
				"visibility falls back to the assignment gate, which admits only the assigned workspaces"
		}
		pr, perr := m.createPostureRequest(r.Context(), sc, mc, postureOpDisableScoping, in.SourceType, in.SourceRef, "", reason, nil)
		out = pr
		return perr
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, out)
}

// handleListPostureRequests lists posture-change requests, optionally filtered by
// ?status / ?source_type / ?source_ref (default surfaces the pending queue for reviewers).
func (m *Module) handleListPostureRequests(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	for _, f := range []struct{ param, col string }{
		{"status", colPRStatus}, {"source_type", colPRSourceType}, {"source_ref", colPRSourceRef},
	} {
		if v := r.URL.Query().Get(f.param); v != "" {
			q.Filters = append(q.Filters, eq(f.col, v))
		}
	}
	out := listResponse[postureRequestDTO]{Items: []postureRequestDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(postureRequestKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toPostureRequestDTO(rec))
		}
		out.Cursor, out.HasMore = page.Cursor, page.HasMore
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleGetPostureRequest returns one posture-change request.
func (m *Module) handleGetPostureRequest(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var (
		out   postureRequestDTO
		found bool
	)
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(postureRequestKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		found, out = true, toPostureRequestDTO(rec)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleApprovePostureRequest is the SECOND-person leg of dual-control: a DISTINCT
// principal (never the proposer) approves a pending relaxation, which then applies inside
// the approver's transaction and is audited. It requires the posture:admin permission.
func (m *Module) handleApprovePostureRequest(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.decidePostureRequest(w, r, mc, true)
}

// handleRejectPostureRequest rejects a pending relaxation (no change applied).
func (m *Module) handleRejectPostureRequest(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.decidePostureRequest(w, r, mc, false)
}

func (m *Module) decidePostureRequest(w http.ResponseWriter, r *http.Request, mc api.ModuleContext, approve bool) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var (
		out     postureRequestDTO
		applied *bindingDTO
	)
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(postureRequestKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		if rec.String(colPRStatus) != postureStatusPending {
			return validationError("posture request is not pending")
		}
		// Anclajes que traía la versión de este mismo arreglo (colisión de dos carriles,
		// resuelta a favor de la primitiva compartida): Actor() es "user:<UserID>" para una
		// sesión y "token:<CredID>" para un token (core/auth/principal.go:126-131), y un
		// principal de token se construye con el UserID de su emisor
		// (core/auth/principal_lookup.go:235) — por eso la persona es la identidad correcta.
		// El mismo patrón ya estaba en governance/killswitch.go:816-818 y :843-849,
		// governance/breakglass.go:177 y :501-540, y recording/handlers.go:180: sourcescope
		// era el rezagado, no el inventor.
		// DUAL-CONTROL: the APPROVER must be a DIFFERENT PERSON than the proposer, which
		// is not the same question as a different actor string. Comparing actors let one
		// human propose from a session and approve with a token they minted for
		// themselves — two strings, one person, gate satisfied (the same defect
		// core/api/dr_handler.go carried, reproduced independently).
		//
		// THE CHECK GUARDS APPROVE ONLY. It used to guard this whole function, and
		// the external contrast on PR #615 measured what that cost: approve and reject are
		// the only two decision routes this module exposes (api.go) and there is no
		// cancellation or administrative close, so any request the check could not resolve
		// stayed pending FOREVER. That is not a corner case — proposer_user is Nullable
		// (schema.go), so every request in flight during the upgrade arrives without a
		// person, and a token-proposed one cannot be recovered from its actor string.
		//
		// Refusing a relaxation grants nothing. The rule is the one already written for
		// the governance quorum (modules/governance/approvals.go): an unattributable party
		// may STOP an action, it may never authorize one. So reject needs no second person
		// — which also gives a proposer the withdrawal route they never had — while
		// approve stays deny-closed and now tells the operator where the exit is.
		//
		// WHAT THIS COSTS, named rather than glossed (contrast). It is not free: any
		// single posture:admin credential can now terminalize someone else's pending
		// request as rejected, where the old check also blocked that. It widens no access,
		// removes no binding and applies no relaxation — only approve reaches applyPosture
		// — the interruption is audited, recoverable by re-proposing, and available only
		// to a role that can already harden posture unilaterally. That is the trade taken
		// deliberately: a recoverable, attributed interruption is a smaller defect than a
		// row no one on earth can close.
		//
		// RefuseWhenUndetermined because this gate's promise is two people: when no person
		// stands behind a party there is nobody to be the second human, and admitting it
		// would re-open the hole from the other end (a person-less proposer compares
		// unequal to everyone and would pass every time).
		// A row written BEFORE this column existed carries no person. Recover what its
		// actor string still says (a session actor literally contains the user id); a
		// token-proposed legacy row stays undetermined and cannot be approved, which is
		// the honest outcome for it — it can still be rejected and re-proposed.
		proposer := auth.PersonRefFromActor(rec.String(colPRProposer))
		if u := rec.String(colPRProposerUser); u != "" {
			proposer.User = u
		}
		decider := auth.PersonRefOf(mc.Principal)
		if approve {
			switch ok, verdict := auth.TwoDistinctPeople(proposer, decider, auth.RefuseWhenUndetermined); {
			case ok:
				// two distinct people — proceed
			case verdict == auth.PersonSame:
				return validationError("dual-control: the approver must be a different person than the proposer; a second credential of the same user is the same person. Another person can approve it, or you can reject it and propose the change again")
			case verdict == auth.PersonSameCredential:
				// Named separately since: this is ONE credential on both sides with
				// no person behind it. Saying "the same person" here would accuse an
				// operator of self-approval for an act no human is attributed to.
				return validationError("dual-control: the same credential is on both sides of this request and no person stands behind it, so the engine cannot confirm two people; approve it from a user session, or reject it and propose the change again")
			default:
				return validationError("dual-control: this request cannot be approved — no stable person stands behind the proposer or the approver, so the engine cannot confirm two people. A request proposed before the person was recorded can be rejected and proposed again from a user session")
			}
		}
		verb := "reject"
		rec[colPRStatus] = postureStatusRejected
		rec[colPRDecidedBy] = mc.Principal.Actor()
		rec[colPRDecidedByUser] = decider.User
		if approve {
			b, aerr := m.applyPosture(r.Context(), sc, mc, rec)
			if aerr != nil {
				return aerr
			}
			applied = b
			rec[colPRStatus] = postureStatusApproved
			verb = "approve"
		}
		rec, err = repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		out = toPostureRequestDTO(rec)
		return auditPosture(r.Context(), sc, mc, verb, out)
	})
	if ferr, ok := err.(forbiddenError); ok {
		writeJSON(w, http.StatusForbidden, errorBody(string(ferr)))
		return
	}
	if verr, ok := err.(validationError); ok {
		writeJSON(w, http.StatusConflict, errorBody(string(verr)))
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// Re-project the access map for an applied UPDATE (post-commit, best-effort); delete /
	// disable-scoping retract nothing (the documented access-map retraction gap).
	if applied != nil {
		m.publishBindingEdges(r.Context(), mc.Tenant, *applied)
	}
	writeJSON(w, http.StatusOK, out)
}

// El ayudante `dualControlVerdict` vivia aqui y se ha RETIRADO al integrar: PR #615
// sustituyo su unico llamante por la primitiva compartida (auth.PersonRefFromActor +
// auth.TwoDistinctPeople), y los dos carriles habian arreglado el mismo defecto por
// separado. Dejar la copia habria sido la deuda que este repo tiene prohibida: dos
// implementaciones de la regla de dos personas en el mismo modulo, una de ellas sin
// llamante y con un test de once casos que ya no podia ponerse rojo. Los once casos
// siguen vivos en dualcontrol_unit_test.go, ahora contra el camino real, y DOS de ellos
// cambiaron de veredicto porque la primitiva es mas estricta que el ayudante que
// sustituye: la fila legacy que declaro incerrable ahora SI se cierra, y una
// propuesta de token de servicio ya no cuenta como primera persona.

// errPostureMoved is the refusal when the state a request was CLASSIFIED against is no
// longer the state the approval would apply to (E-6). It surfaces as a 409 through
// decidePostureRequest's validationError mapping, and the answer is to re-propose against
// the current posture — not to force the stale one through.
const errPostureMoved validationError = "the posture this request was classified against has " +
	"changed since it was proposed; re-propose it against the current state"

// checkPremiseUnchanged re-runs the SAME classification the propose path ran, against the
// state as it is NOW, and refuses if the answer moved (E-6).
//
// applyPosture used to apply a stored proposal to whatever the row had become. The window is
// real and the worst case is not a lost edit — it is the defect through the approval
// door. Take an enabled allow B on a confined source: propose moving its scope (gated, 202);
// while it sits pending, a second actor turns B into a FORBID, which classifyUpdate correctly
// applies immediately as a tightening; then the approver approves the pending move, and the
// proposal — which still says effect:allow — overwrites the forbid. A standing restriction is
// removed, by one actor's write plus an approval nobody gave for it. The reason string is what
// the approver was shown, and each classifier branch returns a distinct one, so comparing it
// against a fresh classification is exactly the question "is this still the change you were
// asked about".
//
// Ops with no classifier (disable_scoping, public_only) have nothing to re-derive: they are
// unconditionally dual-controlled and their reason is a fixed literal. They are not checked
// here, and that is a gap with a name — their target state can still move under them; what
// cannot change is that two people decided.
func checkPremiseUnchanged(stored string, relaxing bool, reason string) error {
	if !relaxing || reason != stored {
		return errPostureMoved
	}
	return nil
}

// applyPosture executes an APPROVED posture change, in the approver's transaction, and
// records the RESULTING state in the ledger. It returns the resulting binding for a
// create/update (so the caller re-projects its access-map edges), nil otherwise.
//
// E-4: it takes the ModuleContext because an approval that does not say what it approved
// is not evidence. Before this, an approved create/update/delete wrote only
// `sourcescope.posture.approve`, whose Meta carries op/source/target/reason/status and NOT
// scope_tree, scope_ref or effect — so the ledger recorded that a change was approved without
// recording what the binding ended up being. auditBinding was called from binding.go only,
// i.e. from exactly the writes that did NOT need a second person.
func (m *Module) applyPosture(ctx context.Context, sc store.Scope, mc api.ModuleContext, rec model.Record) (*bindingDTO, error) {
	brepo, err := sc.Ext(bindingKind)
	if err != nil {
		return nil, err
	}
	storedReason := rec.String(colPRReason)
	switch rec.String(colPROp) {
	case postureOpDelete:
		target, gerr := brepo.Get(ctx, model.ID(rec.String(colPRTargetID)))
		if gerr != nil {
			return nil, gerr
		}
		snap := toBindingDTO(target)
		otherAllows, cerr := countOtherEnabledAllows(ctx, sc, snap.SourceType, snap.SourceRef, snap.ID)
		if cerr != nil {
			return nil, cerr
		}
		relaxing, reason := classifyDelete(snap, otherAllows)
		if perr := checkPremiseUnchanged(storedReason, relaxing, reason); perr != nil {
			return nil, perr
		}
		if derr := brepo.Delete(ctx, model.ID(rec.String(colPRTargetID))); derr != nil {
			return nil, derr
		}
		return nil, auditBinding(ctx, sc, mc, "delete", snap)

	case postureOpCreate:
		var in bindingDTO
		if uerr := json.Unmarshal([]byte(rec.String(colPRProposed)), &in); uerr != nil {
			return nil, validationError("stored proposal is unreadable")
		}
		// The source identity is the request's, not the proposal's — the same natural-key
		// rule the update case applies to a stored row.
		in.SourceType = rec.String(colPRSourceType)
		in.SourceRef = rec.String(colPRSourceRef)
		if msg := in.validate(); msg != "" {
			return nil, validationError(msg)
		}
		wsID, folderPath, rerr := resolveScope(ctx, sc, in.ScopeTree, &in.ScopeRef)
		if rerr != nil {
			return nil, rerr
		}
		in.FolderPath = folderPath
		// The scope the duplicate is compared on is the CANONICAL one resolveScope just
		// produced, the same value the unique index will see.
		duplicate, otherAllows, assignRows, perr := createPreflight(ctx, sc, in.SourceType, in.SourceRef, scopeOf(in))
		if perr != nil {
			return nil, perr
		}
		if duplicate {
			return nil, store.ErrConflict
		}
		relaxing, reason := classifyCreate(in, otherAllows, assignRows)
		if cerr := checkPremiseUnchanged(storedReason, relaxing, reason); cerr != nil {
			return nil, cerr
		}
		// created_by is the PROPOSER: they authored the binding. Who authorized it is the
		// approve event in the ledger, which is the record that must not be conflated.
		created, cerr := brepo.Create(ctx, in.fields(wsID, rec.String(colPRProposer)))
		if cerr != nil {
			return nil, cerr
		}
		out := toBindingDTO(created)
		return &out, auditBinding(ctx, sc, mc, "create", out)

	case postureOpUpdate:
		var in bindingDTO
		if uerr := json.Unmarshal([]byte(rec.String(colPRProposed)), &in); uerr != nil {
			return nil, validationError("stored proposal is unreadable")
		}
		target, gerr := brepo.Get(ctx, model.ID(rec.String(colPRTargetID)))
		if gerr != nil {
			return nil, gerr
		}
		old := toBindingDTO(target) // the posture as it is NOW, not as it was at propose time
		// The source identity is the immutable natural key; force it to the stored row.
		in.SourceType = target.String(colSourceType)
		in.SourceRef = target.String(colSourceRef)
		if msg := in.validate(); msg != "" {
			return nil, validationError(msg)
		}
		wsID, folderPath, rerr := resolveScope(ctx, sc, in.ScopeTree, &in.ScopeRef)
		if rerr != nil {
			return nil, rerr
		}
		in.FolderPath = folderPath
		otherAllows, cerr := countOtherEnabledAllows(ctx, sc, old.SourceType, old.SourceRef, old.ID)
		if cerr != nil {
			return nil, cerr
		}
		relaxing, reason := classifyUpdate(old, in, otherAllows)
		if perr := checkPremiseUnchanged(storedReason, relaxing, reason); perr != nil {
			return nil, perr
		}
		for k, v := range in.fields(wsID, target.String(colCreatedBy)) {
			target[k] = v
		}
		if target, err = brepo.Update(ctx, target); err != nil {
			return nil, err
		}
		out := toBindingDTO(target)
		return &out, auditBinding(ctx, sc, mc, "update", out)

	case postureOpDisableScoping:
		recs, lerr := allExt(ctx, sc, bindingKind, eq(colSourceType, rec.String(colPRSourceType)), eq(colSourceRef, rec.String(colPRSourceRef)))
		if lerr != nil {
			return nil, lerr
		}
		for _, b := range recs {
			if !b.Bool(colEnabled) {
				continue
			}
			b[colEnabled] = false
			updated, uerr := brepo.Update(ctx, b)
			if uerr != nil {
				return nil, uerr
			}
			// E-4: one event PER ROW, because "scoping was disabled" is not a state — the
			// state is which bindings ended up off, and a reader reconstructing the posture
			// from the ledger needs the rows, not the verb.
			if aerr := auditBinding(ctx, sc, mc, "update", toBindingDTO(updated)); aerr != nil {
				return nil, aerr
			}
		}
		return nil, nil

	// --- the ASSIGNMENT surface ------------------------------------------------
	//
	// These return nil: the access-map projection publishes BINDING edges, and an assignment
	// carries none. They are otherwise the same contract as the binding ops — applied inside
	// the approver's transaction, refusing anything the propose path would have refused.
	case postureOpAssignCreate:
		var in assignmentDTO
		if uerr := json.Unmarshal([]byte(rec.String(colPRProposed)), &in); uerr != nil {
			return nil, validationError("stored proposal is unreadable")
		}
		if msg := in.validate(); msg != "" {
			return nil, validationError(msg)
		}
		wsID, _, rerr := resolveScope(ctx, sc, scopeWorkspace, &in.WorkspaceRef)
		if rerr != nil {
			return nil, rerr
		}
		// The world moved while the request sat pending: the pair may have been created by
		// someone else in the meantime. Re-checked HERE and not only at propose time, because
		// the unique index would otherwise surface as an opaque conflict on the approver, and
		// because an approval must not apply to a state nobody proposed.
		otherRows, duplicate, cerr := countOtherAssignments(ctx, sc, in.ConnectorName, "", in.WorkspaceRef)
		if cerr != nil {
			return nil, cerr
		}
		if duplicate {
			return nil, store.ErrConflict
		}
		relaxing, reason := classifyAssignmentCreate(in, otherRows)
		if perr := checkPremiseUnchanged(storedReason, relaxing, reason); perr != nil {
			return nil, perr
		}
		arepo, aerr := sc.Ext(assignmentKind)
		if aerr != nil {
			return nil, aerr
		}
		// created_by is the PROPOSER, as on the binding side: they authored the row, and who
		// authorized it is the approve event in the ledger.
		if _, cerr := arepo.Create(ctx, in.fields(wsID, rec.String(colPRProposer))); cerr != nil {
			return nil, cerr
		}
		return nil, auditAssignment(ctx, sc, mc, "create", in)

	case postureOpAssignUpdate:
		var in assignmentDTO
		if uerr := json.Unmarshal([]byte(rec.String(colPRProposed)), &in); uerr != nil {
			return nil, validationError("stored proposal is unreadable")
		}
		arepo, aerr := sc.Ext(assignmentKind)
		if aerr != nil {
			return nil, aerr
		}
		target, gerr := arepo.Get(ctx, model.ID(rec.String(colPRTargetID)))
		if gerr != nil {
			return nil, gerr
		}
		old := toAssignmentDTO(target) // as it is NOW, not as it was at propose time
		// The (connector, workspace) pair is the immutable natural key; force it to the
		// stored row, the same rule the binding update case applies to source identity.
		in.ConnectorName = target.String(colAssignConnector)
		in.WorkspaceRef = target.String(colAssignWorkspace)
		if msg := in.validate(); msg != "" {
			return nil, validationError(msg)
		}
		relaxing, reason := classifyAssignmentUpdate(old, in)
		if perr := checkPremiseUnchanged(storedReason, relaxing, reason); perr != nil {
			return nil, perr
		}
		wsID := model.ID(target.String(colAssignWsID))
		for k, v := range in.fields(wsID, target.String(colAssignCreatedBy)) {
			target[k] = v
		}
		if _, uerr := arepo.Update(ctx, target); uerr != nil {
			return nil, uerr
		}
		return nil, auditAssignment(ctx, sc, mc, "update", in)

	case postureOpAssignDelete:
		arepo, aerr := sc.Ext(assignmentKind)
		if aerr != nil {
			return nil, aerr
		}
		target, gerr := arepo.Get(ctx, model.ID(rec.String(colPRTargetID)))
		if gerr != nil {
			return nil, gerr
		}
		snap := toAssignmentDTO(target)
		otherRows, _, cerr := countOtherAssignments(ctx, sc, snap.ConnectorName, snap.ID, "")
		if cerr != nil {
			return nil, cerr
		}
		relaxing, reason := classifyAssignmentDelete(snap, otherRows)
		if perr := checkPremiseUnchanged(storedReason, relaxing, reason); perr != nil {
			return nil, perr
		}
		if derr := arepo.Delete(ctx, model.ID(rec.String(colPRTargetID))); derr != nil {
			return nil, derr
		}
		return nil, auditAssignment(ctx, sc, mc, "delete", snap)

	case postureOpGuardPublicOnly:
		updatedBy := rec.String(colPRDecidedBy)
		if updatedBy == "" {
			updatedBy = rec.String(colPRProposer)
		}
		gp, gerr := applyGuardPosture(ctx, sc, rec.String(colPRSourceType), rec.String(colPRSourceRef), GuardProfilePublicOnly, rec.String(colPRReason), updatedBy)
		if gerr != nil {
			return nil, gerr
		}
		// E-4: applyGuardPosture does not audit. The TIGHTEN path audits at its caller
		// (guardposture.go:168); the RELAX path — the one that needed two people — audited
		// nowhere, so the ledger recorded that a guard relaxation was approved without
		// recording which profile the source ended up on.
		return nil, auditGuardPosture(ctx, sc, mc, "relax", gp)

	default:
		return nil, validationError("unknown posture op")
	}
}
