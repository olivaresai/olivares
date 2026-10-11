// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

// GET /v1/auth/effective-rights answers one question for an administrator: what does this
// subject hold at this node, and where does the node sit. It is an aggregation of real
// decisions, never a second algebra: every trustee right (auth.TrusteeRights) builds the
// Request the trustee table defines and the Authorizer answers it, all eight in one
// AuthorizeBatch (enumerate.go), so a role and every scoped grant are in the answer. The
// path is read from the node's stored row by the lineage module (store.Ancestors), never
// from the caller.
//
// The questions carry no route metadata, exactly as the AuthZEN evaluation and the access
// review ask theirs: a route that narrows the role (RequireScopedGrant, RBACMinimumRole) or
// lets a session inherit its agent's groups decides differently, so the answer is what the
// Authorizer says of the trustee question, not a promise about one route. For the same
// reason a session's path lists no agent group: the decision used none.
//
// Read-only and admin-gated (authz:admin, admin and owner): reading another subject's
// rights reconstructs the access matrix, which a workspace-confined administrator has no
// tenant-wide view of, so the handler refuses that caller itself. The subject is resolved
// from the store, as the AuthZEN adapter does, at the highest assurance (AAL3): an access
// review must not under-report standing entitlement.

// effectiveRightsKinds are the node kinds with a stored lineage (store.Ancestors).
var effectiveRightsKinds = []string{"agent", "session", "resource"}

// The state of one right. A deny is a statement about policy only when the typed evidence
// path agrees (see rightState); an engine that could not decide is "unknown", never
// "not_held". A right with no honest question at the node is "not_applicable"; the three
// kinds served today always have one, so it is the guard for a kind added later.
// ponytail: unreachable today; cut it when the kind list is fixed for good.
const (
	rightHeld          = "held"
	rightNotHeld       = "not_held"
	rightUnknown       = "unknown"
	rightNotApplicable = "not_applicable"
)

type effectiveRightsRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// effectiveRightsStep is one container on the node's path. Workspace is set only on an
// agent group that lives in a different workspace than the node.
type effectiveRightsStep struct {
	Kind      string `json:"kind"`
	Ref       string `json:"ref"`
	Workspace string `json:"workspace,omitempty"`
}

type effectiveRight struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

type effectiveRightsResponse struct {
	Subject effectiveRightsRef `json:"subject"`
	Node    effectiveRightsRef `json:"node"`
	// Assurance is the authenticator assurance level the subject was evaluated at.
	Assurance int                   `json:"assurance"`
	Path      []effectiveRightsStep `json:"path"`
	Rights    []effectiveRight      `json:"rights"`
}

type effectiveRightsQuery struct {
	subjectType, subjectID, kind string
	nodeID                       model.ID
}

// parseEffectiveRightsQuery reads and validates the question; the string is the 400 message.
func parseEffectiveRightsQuery(r *http.Request) (effectiveRightsQuery, string) {
	v := r.URL.Query()
	q := effectiveRightsQuery{
		subjectType: strings.ToLower(strings.TrimSpace(v.Get("subject_type"))),
		subjectID:   strings.TrimSpace(v.Get("subject_id")),
		kind:        strings.TrimSpace(v.Get("kind")),
	}
	if q.subjectType == "" {
		q.subjectType = "user"
	}
	id, err := model.ParseID(strings.TrimSpace(v.Get("id")))
	q.nodeID = id
	_, subjectErr := model.ParseID(q.subjectID)
	switch {
	case subjectErr != nil:
		// An id, never an email: an email in a URL is logged by whatever sits in front of
		// the engine, and a guessable key would make the 404 an oracle for who has an account.
		return q, "subject_id must be the id of the user or token"
	case !slices.Contains([]string{"user", "token"}, q.subjectType):
		return q, "unsupported subject_type (use user or token)"
	case !slices.Contains(effectiveRightsKinds, q.kind):
		return q, "kind must be agent, session or resource"
	case err != nil || id.IsZero():
		return q, "id must be the id of the node"
	}
	return q, ""
}

func (s *Server) handleEffectiveRights(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	// An authorization-dependent answer is not for a cache, whatever sits in front of the engine.
	for name, value := range NoStoreResponseHeaders() {
		w.Header().Set(name, value)
	}
	// The answer spans the whole tenant, and authz:admin alone does not keep a
	// workspace-confined administrator out of it (the engine would admit the role), so the
	// refusal is made here, as for the other tenant-wide reads.
	if _, confined := mc.Principal.ConfinedWorkspaceIn(mc.Tenant); confined {
		s.writeError(w, r, errForbidden)
		return
	}
	q, bad := parseEffectiveRightsQuery(r)
	if bad != "" {
		s.badRequest(w, r, bad)
		return
	}

	// A subject the store does not know and one with no membership here answer alike: the
	// read must not tell an administrator which ids exist in another tenant. A superadmin
	// holds every right without a membership, and the answer says so rather than under-report.
	subject, found, err := s.resolveAuthzenSubject(r.Context(), azSubject{Type: q.subjectType, ID: q.subjectID}, auth.AAL3)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if !found || !(subject.Superadmin || subject.IsMember(mc.Tenant)) {
		s.writeError(w, r, store.ErrNotFound)
		return
	}

	// The lineage is read in its own short View: Authorize opens its own scope-resolution
	// read, and store transactions are not nested.
	var ancestry store.Ancestry
	var nodeFound bool
	err = s.st.View(r.Context(), mc.Tenant, func(sc store.Scope) error {
		var e error
		ancestry, nodeFound, e = store.Ancestors(r.Context(), sc, q.kind, q.nodeID, store.AncestryOptions{})
		return e
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	// The caller must be able to read the node itself: a forbid or confinement written over
	// it applies to the administrator too, and an ordinary read of it would be refused. The
	// refusal is the same 404 as a missing node.
	node := auth.ResourceAttrs{Kind: q.kind, ID: q.nodeID.String()}
	if !nodeFound || !s.authz.Authorize(r.Context(), auth.Request{
		Principal: mc.Principal, Permission: auth.Permission(q.kind + ":" + auth.VerbRead), Tenant: mc.Tenant, Resource: node,
	}).Allow {
		s.writeError(w, r, store.ErrNotFound)
		return
	}

	rights, err := s.askTrusteeRights(r.Context(), subject, mc.Tenant, node)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	subjectKind, subjectRef := authzenSubjectRef(subject)
	writeJSON(w, http.StatusOK, effectiveRightsResponse{
		Subject:   effectiveRightsRef{Kind: subjectKind, ID: subjectRef},
		Node:      effectiveRightsRef{Kind: node.Kind, ID: node.ID},
		Assurance: auth.AAL3,
		Path:      effectiveRightsPath(q.kind, q.nodeID, ancestry),
		Rights:    rights,
	})
}

// askTrusteeRights asks the Authorizer each trustee right's question at node, in table order.
func (s *Server) askTrusteeRights(ctx context.Context, subject auth.Principal, tenant model.TenantID, node auth.ResourceAttrs) ([]effectiveRight, error) {
	// The typed evidence behind a denial (rightState) needs a finite horizon, and the real
	// transport supplies none: install the server-owned budget the capability projection
	// uses. Without it every denial of the real engine would read "unknown".
	ctx, cancel := context.WithTimeout(ctx, capabilityBatchBudget)
	defer cancel()
	rights := auth.TrusteeRights()
	out := make([]effectiveRight, len(rights))
	var reqs []auth.Request
	var asked []int
	for i, right := range rights {
		out[i] = effectiveRight{Name: right.Name, State: rightNotApplicable}
		if req, ok := right.Question(subject, tenant, node); ok {
			req.Purpose = sdk.PurposeCurrentWhatIf
			reqs = append(reqs, req)
			asked = append(asked, i)
		}
	}
	decisions, err := s.authz.AuthorizeBatch(ctx, reqs)
	if err != nil {
		return nil, err
	}
	for j, d := range decisions {
		out[asked[j]].State = s.rightState(ctx, rights[asked[j]].Name, reqs[j], d)
	}
	return out, nil
}

// rightState turns a decision into a state. Authorize answers Allow:false both when policy
// refuses and when the scoped engine or the deny-overlay fails (it fails closed, which is
// right for serving a request and wrong as a statement about policy), so a deny counts as
// "not_held" only when the typed evidence path also establishes a denial; anything else is
// "unknown". This is the deny half of the capability projection's reading
// (projectOuterDecision); an allow stays the Authorize boolean, which is what the route obeys.
func (s *Server) rightState(ctx context.Context, name string, req auth.Request, d auth.Decision) string {
	if d.Allow {
		return rightHeld
	}
	evidence := s.authz.AuthorizeEvidence(ctx, req)
	if evidence.Outcome == auth.EvidenceDeny {
		return rightNotHeld
	}
	// The cause stays in the log: an operator needs it, and the response is not the place for it.
	if s.log != nil {
		s.log.Warn("effective-rights: a right could not be decided", "right", name, "code", evidence.CorePermission.Code, "request_id", requestID(ctx))
	}
	return rightUnknown
}

// effectiveRightsPath lists what contains the node, outermost first: its workspace, a
// resource's folders root first, the agent groups it inherits through, then the node.
func effectiveRightsPath(kind string, id model.ID, a store.Ancestry) []effectiveRightsStep {
	path := []effectiveRightsStep{{Kind: "workspace", Ref: a.Workspace}}
	for _, folder := range a.Folders {
		path = append(path, effectiveRightsStep{Kind: "folder", Ref: folder.String()})
	}
	for _, g := range a.Groups {
		step := effectiveRightsStep{Kind: "agent_group", Ref: g.Slug}
		if g.Workspace != a.Workspace {
			step.Workspace = g.Workspace
		}
		path = append(path, step)
	}
	return append(path, effectiveRightsStep{Kind: kind, Ref: id.String()})
}
