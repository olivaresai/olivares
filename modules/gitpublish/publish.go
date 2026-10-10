// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package gitpublish

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	gp "github.com/olivaresai/olivares/connectors/gitpublish"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// PushInput is one leased push of an exact commit.
type PushInput struct {
	Target            model.ID
	OperationID       string
	Ref               string
	ExpectedOld       string
	Commit, Tree      string
	AcknowledgeIntent model.ID
	// SessionRun, when set, names the session run whose folder holds Commit:
	// the commit is fetched from it into the server repository first. It is
	// transport, not request semantics: it is not in the digest or the intent.
	SessionRun string
	Proposal   Proposal
}

// PullRequestInput opens one pull request. The host binds branches, not SHAs.
type PullRequestInput struct {
	Target              model.ID
	OperationID         string
	HeadRef, Base       string
	Commit, Title, Body string
	Draft               bool
	AcknowledgeIntent   model.ID
	Proposal            Proposal
}

// Proposal is a session's publish request that a person approved. The
// composition root sets it in process, after it spent that approval, and the
// caller is the session's launcher; no request body carries it. The target
// must be in the session's workspace, and the intent records the run and the
// approval.
type Proposal struct {
	SessionRun string
	Workspace  model.ID
	Approval   string
}

func (p Proposal) valid() bool {
	if p == (Proposal{}) {
		return true
	}
	id, err := model.ParseID(p.SessionRun)
	return err == nil && id.String() == p.SessionRun && !p.Workspace.IsZero() && opRe.MatchString(p.Approval)
}

// MergeInput merges one pull request with the reviewed source head as its
// precondition. ExpectedResultTree and ExpectedBase are unsupported.
type MergeInput struct {
	Target             model.ID
	OperationID        string
	Number             int
	ExpectedHead       string
	Method             string
	ExpectedResultTree string
	ExpectedBase       string
	AcknowledgeIntent  model.ID
}

// Receipt is an intent and the answer for this call.
type Receipt struct {
	Intent Intent
	// Answer is the intent state, or "dispatching" for a replay that does not
	// own the dispatch.
	Answer string
}

var (
	opRe  = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
	shaRe = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
)

// spec is one effect request, normalized.
type spec struct {
	effect       string
	perm         auth.Permission
	action       auth.CedarAction
	aal          int
	host         gp.Effect
	target       model.ID
	op           string
	ack          model.ID
	req          Requested
	body         string
	draft        bool
	scope        string
	branch       string                // push: the ref's branch name
	capabilities gp.TargetCapabilities // facts of the credential binding's kind
	session      string                // push: the session run whose folder feeds the server repository
	proposal     Proposal
}

// Validate reports the push's own shape refusal, or nil: the checks Push
// makes before any target or authority is read.
func (in PushInput) Validate() error {
	if e := in.check(); e != nil {
		return e
	}
	return nil
}

func (in PushInput) check() *Error {
	if !strings.HasPrefix(in.Ref, "refs/heads/") || !validBranch(strings.TrimPrefix(in.Ref, "refs/heads/")) {
		return refuse("ref_not_allowed", http.StatusUnprocessableEntity)
	}
	if !shaRe.MatchString(in.Commit) || !shaRe.MatchString(in.Tree) || (in.ExpectedOld != "" && !shaRe.MatchString(in.ExpectedOld)) {
		return errInvalid
	}
	if in.SessionRun != "" {
		if id, err := model.ParseID(in.SessionRun); err != nil || id.String() != in.SessionRun {
			return errInvalid
		}
	}
	return nil
}

// Push publishes an exact commit to a named ref with a lease.
func (m *Module) Push(ctx context.Context, c Caller, in PushInput) (Receipt, error) {
	if e := in.check(); e != nil {
		return Receipt{}, e
	}
	s := spec{effect: effectPush, perm: permPush, action: actionPush, host: gp.EffectPush, target: in.Target, op: in.OperationID, ack: in.AcknowledgeIntent,
		req: Requested{Ref: in.Ref, ExpectedOld: in.ExpectedOld, Commit: in.Commit, Tree: in.Tree}, branch: strings.TrimPrefix(in.Ref, "refs/heads/"), session: in.SessionRun, proposal: in.Proposal}
	s.scope = "push:" + in.Ref
	return m.publish(ctx, c, s)
}

// Validate reports the pull request's own shape refusal, or nil: the checks
// OpenPullRequest makes before any target or authority is read.
func (in PullRequestInput) Validate() error {
	if e := in.check(); e != nil {
		return e
	}
	return nil
}

func (in PullRequestInput) check() *Error {
	if !validBranch(in.HeadRef) {
		return refuse("ref_not_allowed", http.StatusUnprocessableEntity)
	}
	if !validBranch(in.Base) || !shaRe.MatchString(in.Commit) || in.Title == "" || len(in.Title) > 256 || len(in.Body) > 65536 {
		return errInvalid
	}
	return nil
}

// OpenPullRequest opens a pull request from head_ref into base.
func (m *Module) OpenPullRequest(ctx context.Context, c Caller, in PullRequestInput) (Receipt, error) {
	if e := in.check(); e != nil {
		return Receipt{}, e
	}
	s := spec{effect: effectPullRequest, perm: permPullRequest, action: actionPullRequest, host: gp.EffectPullRequest, target: in.Target, op: in.OperationID,
		ack: in.AcknowledgeIntent, req: Requested{HeadRef: in.HeadRef, Base: in.Base, Commit: in.Commit, Title: in.Title}, body: in.Body, draft: in.Draft, proposal: in.Proposal}
	s.scope = "pull_request:" + in.HeadRef + "\x00" + in.Base
	return m.publish(ctx, c, s)
}

// Merge merges a pull request whose head is the reviewed expected head.
func (m *Module) Merge(ctx context.Context, c Caller, in MergeInput) (Receipt, error) {
	if in.ExpectedResultTree != "" || in.ExpectedBase != "" {
		return Receipt{}, errUnsupported
	}
	switch in.Method {
	case "merge", "squash", "rebase":
	default:
		return Receipt{}, errInvalid
	}
	if in.Number <= 0 || !shaRe.MatchString(in.ExpectedHead) {
		return Receipt{}, errInvalid
	}
	s := spec{effect: effectMerge, perm: permMerge, action: actionMerge, aal: auth.AAL3, host: gp.EffectMerge, target: in.Target, op: in.OperationID,
		ack: in.AcknowledgeIntent, req: Requested{Number: in.Number, ExpectedHead: in.ExpectedHead, Method: in.Method}}
	s.scope = "merge:" + strconv.Itoa(in.Number)
	return m.publish(ctx, c, s)
}

// digestOf frames the request semantics, the tenant and the subject.
func digestOf(tenant model.TenantID, s spec, sub Subject) string {
	h := sha256.New()
	h.Write([]byte("olivares.gitpublish.intent.v1\x00"))
	for _, f := range []string{tenant.String(), s.target.String(), s.effect, s.op, sub.ActorKind, sub.Actor,
		s.req.Ref, s.req.ExpectedOld, s.req.Commit, s.req.Tree, s.req.HeadRef, s.req.Base, s.req.Title,
		strconv.Itoa(s.req.Number), s.req.ExpectedHead, s.req.Method, s.body, strconv.FormatBool(s.draft)} {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(f)))
		h.Write(n[:])
		h.Write([]byte(f))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// sameSubject compares users when both sides name one; otherwise the exact
// credential actor, so a second userless token never passes.
func sameSubject(in Intent, sub Subject) bool {
	if in.SubjectUser != "" && !sub.UserID.IsZero() {
		return in.SubjectUser == sub.UserID.String()
	}
	return in.SubjectActor == sub.Actor
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// releaser releases a minted capability exactly once, on every exit path.
type releaser struct {
	host gp.Host
	tok  gp.Token
	done bool
}

func (r *releaser) release(ctx context.Context) string {
	if r == nil || r.done || r.host == nil {
		return ""
	}
	r.done = true
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
	defer cancel()
	if err := r.host.Release(rctx, r.tok); err != nil {
		return hostCode(err)
	}
	return ""
}

// preflight is what A3 established before the claim.
type preflight struct {
	adopt    bool
	observed Observed
	number   int
	head     string // merge: the change's source branch, as the host reports it
}

func (m *Module) publish(ctx context.Context, c Caller, s spec) (Receipt, error) {
	if !m.wired() {
		return Receipt{}, errUnavailable
	}
	if c.Principal.SessionIdentity != "" || c.Principal.SessionRunRef != "" {
		return Receipt{}, errRuntimeCredential
	}
	if !opRe.MatchString(s.op) || s.target.IsZero() || !s.proposal.valid() {
		return Receipt{}, errInvalid
	}
	actx, cancel := m.admissionContext(ctx)
	defer cancel()
	view := func(ctx context.Context, fn func(store.Scope) error) error { return m.data.View(ctx, c.Tenant, fn) }

	var tg Target
	if err := view(actx, func(sc store.Scope) error {
		var err error
		tg, err = loadTarget(actx, sc, s.target)
		return err
	}); err != nil {
		return Receipt{}, storeError(err)
	}
	if s.proposal != (Proposal{}) && tg.Workspace != s.proposal.Workspace {
		// A session publishes only in its own workspace, as its launcher's
		// denial on another workspace would read.
		return Receipt{}, errNotFound
	}
	// A1: the exact caller, the stored target and its workspace.
	adm, err := m.opts.Authority.Admit(actx, c.Principal, c.Tenant, Question{Permission: s.perm, Action: s.action, MinimumAAL: s.aal, Target: tg.ID, Workspace: tg.Workspace})
	if err != nil {
		return Receipt{}, authError(err)
	}
	sub := adm.Subject()
	if !s.ack.IsZero() {
		// Proceeding past an abandoned intent carries at least the
		// abandonment's authority: target:admin at AAL3.
		if _, err := m.opts.Authority.Admit(actx, c.Principal, c.Tenant, Question{Permission: permTargetAdmin, Action: actionAbandon, MinimumAAL: auth.AAL3, Target: tg.ID, Workspace: tg.Workspace}); err != nil {
			return Receipt{}, authError(err)
		}
	}
	cb, rb, e := m.bindings(actx, c.Tenant, tg.Workspace, tg.CredentialBinding, tg.RepositoryBinding)
	if e != nil {
		return Receipt{}, e
	}
	s.capabilities = gp.TargetCapabilitiesFor(cb.Host)
	if !s.capabilities.Supports(s.host, s.req.Method) {
		return Receipt{}, errUnsupported
	}
	if e := targetRules(tg, s); e != nil {
		return Receipt{}, e
	}
	digest := digestOf(c.Tenant, s, sub)

	// A2: replay by operation id; refuse a new or re-armed operation in a held scope.
	var existing Intent
	var found bool
	var blocker Intent
	var blocked bool
	if err := view(actx, func(sc store.Scope) error {
		var err error
		if existing, found, err = findByOperation(actx, sc, tg.ID, s.effect, s.op); err != nil {
			return err
		}
		blocker, blocked, err = unresolvedInScope(actx, sc, tg.ID, s.scope, s.ack.String())
		return err
	}); err != nil {
		return Receipt{}, storeError(err)
	}
	var rearm *Intent
	if found {
		if !sameSubject(existing, sub) {
			return Receipt{}, withIntent(refuse("subject_mismatch", http.StatusForbidden), existing.ID)
		}
		if existing.Digest != digest {
			return Receipt{}, withIntent(refuse("operation_conflict", http.StatusConflict), existing.ID)
		}
		switch existing.State {
		case StateNotDispatched:
			if blocked {
				return Receipt{}, withIntent(refuse("unresolved_intent", http.StatusConflict), blocker.ID)
			}
			rearm = &existing
		case StateUncertain:
			return m.observe(ctx, c, existing, "reconcile", "")
		case StateDispatching:
			return Receipt{Intent: existing, Answer: StateDispatching}, nil
		default:
			return Receipt{Intent: existing, Answer: existing.State}, nil
		}
	} else if blocked {
		return Receipt{}, withIntent(refuse("unresolved_intent", http.StatusConflict), blocker.ID)
	}

	// A3: local content, then the narrowed capability and the host preflight.
	if e := m.feed(actx, c, tg, rb, s); e != nil {
		return Receipt{}, e
	}
	if s.effect != effectMerge {
		tree, err := m.opts.Git.CommitTree(actx, rb.LocalPath, s.req.Commit)
		if err != nil || (s.req.Tree != "" && tree != s.req.Tree) {
			return Receipt{}, refuse("content_mismatch", http.StatusUnprocessableEntity)
		}
	}
	host, err := m.opts.Custody.OpenHost(actx, c.Tenant, cb, rb)
	if err != nil {
		return Receipt{}, refuse("host_unavailable", http.StatusBadGateway)
	}
	tok, err := host.Mint(actx, s.host)
	if err != nil {
		if errors.Is(err, gp.ErrCredentialRefused) || errors.Is(err, gp.ErrTokenScope) {
			return Receipt{}, refuse("host_credential_refused", http.StatusBadGateway)
		}
		return Receipt{}, refuse("host_unavailable", http.StatusBadGateway)
	}
	rel := &releaser{host: host, tok: tok}
	var ownIntent model.ID
	defer func() {
		if code := rel.release(ctx); code != "" {
			m.recordReleaseFailure(ctx, c, ownIntent, tg.ID, code)
		}
	}()
	pre, e := m.preflight(actx, host, tok, tg, rb, s)
	if e != nil {
		return Receipt{}, e
	}
	if s.effect == effectMerge {
		var authored bool
		if err := view(actx, func(sc store.Scope) error {
			var err error
			authored, err = sessionAuthored(actx, sc, rb.RepoID, s.req, pre.head, sub)
			return err
		}); err != nil {
			return Receipt{}, storeError(err)
		}
		if authored {
			return Receipt{}, errSeparationOfDuty
		}
	}

	in := Intent{
		Target: tg.ID, Workspace: tg.Workspace, TargetVersion: tg.Version,
		CredentialBinding: cb.ID, CredentialVersion: cb.Version, RepositoryBinding: rb.ID, RepositoryVersion: rb.Version, RepoID: rb.RepoID,
		Effect: s.effect, OperationID: s.op, ScopeKey: s.scope, Digest: digest,
		SubjectActor: sub.Actor, SubjectActorKind: sub.ActorKind, AgentIdentity: sub.AgentIdentity,
		Attempt: 1, State: StateDispatching, Receipt: ReceiptNone, Requested: s.req, AcknowledgeIntent: s.ack.String(), Proposal: s.proposal,
	}
	if !sub.UserID.IsZero() {
		in.SubjectUser = sub.UserID.String()
	}
	if pre.adopt {
		in.State, in.Receipt, in.Observed = StateAdopted, ReceiptExistingEffect, pre.observed
	}
	if m.beforeClaim != nil {
		m.beforeClaim()
	}
	// W1: burn the intent before any repository mutation.
	claimed, won, e := m.claim(actx, c, adm, in, rearm)
	if e != nil {
		return Receipt{}, e
	}
	if won {
		ownIntent = claimed.ID
	}
	if !won {
		ans := claimed.State
		if ans == StateDispatching {
			ans = StateDispatching
		}
		return Receipt{Intent: claimed, Answer: ans}, nil
	}
	if claimed.State == StateAdopted {
		return m.finish(ctx, c, rel, claimed)
	}
	writeDeadline := m.now().Add(claimed.DispatchDeadline.Sub(claimed.ClaimedAt))

	// A4: the last local check before dispatch.
	if e := m.effectGate(actx, c, adm, claimed, writeDeadline, view); e != nil {
		claimed = m.settleNotDispatched(ctx, c, claimed, e.Code)
		return Receipt{Intent: claimed, Answer: claimed.State}, withIntent(e, claimed.ID)
	}
	if err := m.data.Mutate(actx, c.Tenant, func(sc store.Scope) error { return auditIntent(actx, sc, claimed, "dispatched") }); err != nil {
		claimed = m.settleNotDispatched(ctx, c, claimed, "audit_unavailable")
		return Receipt{Intent: claimed, Answer: claimed.State}, withIntent(errUnavailable, claimed.ID)
	}
	wctx, wcancel := context.WithDeadline(context.WithoutCancel(ctx), writeDeadline)
	out := m.dispatch(wctx, host, tok, rb, claimed, s)
	wcancel()
	settled := m.settle(ctx, c, host, tok, claimed, out)
	return m.finish(ctx, c, rel, settled)
}

// finish releases the capability and records a bounded release failure.
func (m *Module) finish(ctx context.Context, c Caller, rel *releaser, in Intent) (Receipt, error) {
	if code := rel.release(ctx); code != "" {
		if out, ok := m.recordReleaseFailure(ctx, c, in.ID, in.Target, code); ok {
			in = out
		}
	}
	return Receipt{Intent: in, Answer: in.State}, nil
}

// recordReleaseFailure keeps a bounded release failure: on the intent when
// one exists, otherwise through OnReleaseFailure. It never authorizes a
// replay and never carries the token.
func (m *Module) recordReleaseFailure(ctx context.Context, c Caller, intent, target model.ID, code string) (Intent, bool) {
	if intent.IsZero() {
		if m.opts.OnReleaseFailure != nil {
			m.opts.OnReleaseFailure(target, code)
		}
		return Intent{}, false
	}
	var out Intent
	err := m.data.Mutate(context.WithoutCancel(ctx), c.Tenant, func(sc store.Scope) error {
		cur, err := loadIntent(ctx, sc, intent)
		if err != nil {
			return err
		}
		cur.ReleaseFailure = code
		out, err = update(ctx, sc, cur)
		return err
	})
	return out, err == nil
}

// openOnly keeps the open changes of a lookup, whatever the host returned:
// a closed change never blocks and is never adopted.
func openOnly(list []gp.Change) []gp.Change {
	var out []gp.Change
	for _, ch := range list {
		if ch.Open {
			out = append(out, ch)
		}
	}
	return out
}

// feed fetches the pushed commit from the named session run's folder into the
// server repository, after A1 admitted the caller on the target's workspace.
// The run is read in that workspace only, and any run there may be named: the
// authority to publish is the workspace's, as for every other push. A push that
// names no run is unchanged.
//
// ponytail: the fetch runs inside the admission window (AdmissionTimeout, 30s by
// default), so a first fetch of a very large history answers
// session_source_unavailable; a repository that hits it is the trigger for a
// feed step of its own.
func (m *Module) feed(ctx context.Context, c Caller, tg Target, rb RepositoryBinding, s spec) *Error {
	if s.session == "" {
		return nil
	}
	if m.opts.Sessions == nil {
		return refuse("session_source_unavailable", http.StatusServiceUnavailable)
	}
	dir, err := m.opts.Sessions.ReadRunWorkspacePath(ctx, c.Tenant, tg.Workspace, s.session)
	if errors.Is(err, store.ErrNotFound) {
		return refuse("session_source_refused", http.StatusUnprocessableEntity)
	}
	if err != nil {
		return refuse("session_source_unavailable", http.StatusServiceUnavailable)
	}
	switch err := m.opts.Git.Fetch(ctx, rb.LocalPath, dir, s.req.Commit); {
	case err == nil:
		return nil
	case ctx.Err() != nil:
		return refuse("session_source_unavailable", http.StatusServiceUnavailable)
	case errors.Is(err, gp.ErrSource):
		return refuse("session_source_refused", http.StatusUnprocessableEntity)
	case errors.Is(err, gp.ErrContent):
		return refuse("content_mismatch", http.StatusUnprocessableEntity)
	case errors.Is(err, gp.ErrRepositoryConfig):
		return refuse("repository_config_refused", http.StatusUnprocessableEntity)
	default:
		return refuse("session_source_unavailable", http.StatusServiceUnavailable)
	}
}

// targetRules applies the target's ref and base rules.
func targetRules(tg Target, s spec) *Error {
	switch s.effect {
	case effectPush:
		// A push lands only under the push prefix. A merge base, like the
		// default branch, changes only through the merge path (merge:admin and
		// AAL3); push access never substitutes for it.
		if !strings.HasPrefix(s.branch, tg.PushPrefix) || contains(tg.MergeBases, s.branch) {
			return refuse("ref_not_allowed", http.StatusUnprocessableEntity)
		}
	case effectPullRequest:
		if !strings.HasPrefix(s.req.HeadRef, tg.PushPrefix) {
			return refuse("ref_not_allowed", http.StatusUnprocessableEntity)
		}
		if !contains(tg.MergeBases, s.req.Base) {
			return refuse("base_not_allowed", http.StatusUnprocessableEntity)
		}
	}
	return nil
}

// sessionAuthored reports whether the merging subject launched a session
// whose approved proposal, on this repository, opened a pull request from the
// change's head branch or pushed its head commit. That launcher is its author,
// and a second person merges it: the host rule of approval by someone other
// than the last pusher. head is the change's branch as the host reports it, so
// a pull request whose number was never observed still matches.
func sessionAuthored(ctx context.Context, sc store.Scope, repoID string, req Requested, head string, sub Subject) (bool, error) {
	recs, err := listAll(ctx, sc, kindIntent, eq("repo_id", repoID))
	if err != nil {
		return false, err
	}
	for _, rec := range recs {
		in := intentFrom(rec)
		if in.Proposal.SessionRun == "" || !sameSubject(in, sub) {
			continue
		}
		if in.Requested.Commit == req.ExpectedHead || (in.Effect == effectPullRequest && in.Requested.HeadRef == head) {
			return true, nil
		}
	}
	return false, nil
}

// preflight is A3's host part: adoption, staleness, head binding and
// workflow files, all before any repository mutation.
func (m *Module) preflight(ctx context.Context, host gp.Host, tok gp.Token, tg Target, rb RepositoryBinding, s spec) (preflight, *Error) {
	refused := func(code string, status int) (preflight, *Error) { return preflight{}, refuse(code, status) }
	workflows := func(from, to string) *Error {
		changed, err := m.opts.Git.PathsChanged(ctx, rb.LocalPath, from, to, s.capabilities.CIPaths)
		if err != nil {
			return refuse("content_mismatch", http.StatusUnprocessableEntity)
		}
		if changed {
			return refuse("workflow_change_refused", http.StatusUnprocessableEntity)
		}
		return nil
	}
	baseHead := func(base string) (string, *Error) {
		o, err := host.Ref(ctx, tok, base)
		if err != nil || o.State != gp.RefPresent {
			return "", refuse("lookup_incomplete", http.StatusServiceUnavailable)
		}
		return o.SHA, nil
	}
	now := m.now()
	switch s.effect {
	case effectPush:
		// The host's own view of the branch: its default branch and any
		// branch it protects refuse by name before any dispatch. A failed
		// read refuses; it is never taken as "unprotected".
		info, err := host.Branch(ctx, tok, s.branch)
		if err != nil {
			return refused("lookup_incomplete", http.StatusServiceUnavailable)
		}
		if info.Default == s.branch {
			return refused("default_branch_refused", http.StatusUnprocessableEntity)
		}
		if info.Protected {
			return refused("protected_branch_refused", http.StatusUnprocessableEntity)
		}
		o, err := host.Ref(ctx, tok, s.branch)
		if err == nil && o.State == gp.RefPresent {
			if o.SHA == s.req.Commit {
				return preflight{adopt: true, observed: Observed{Present: true, SHA: o.SHA, Source: "preflight", At: now}}, nil
			}
			if s.req.ExpectedOld == "" || o.SHA != s.req.ExpectedOld {
				return refused("stale_lease", http.StatusConflict)
			}
		}
		from := s.req.ExpectedOld
		if from == "" {
			if len(tg.MergeBases) == 0 {
				return refused("content_mismatch", http.StatusUnprocessableEntity)
			}
			sha, e := baseHead(tg.MergeBases[0])
			if e != nil {
				return preflight{}, e
			}
			from = sha
		}
		if e := workflows(from, s.req.Commit); e != nil {
			return preflight{}, e
		}
	case effectPullRequest:
		open, err := host.OpenChanges(ctx, tok, s.req.HeadRef, s.req.Base)
		if err != nil {
			return refused("lookup_incomplete", http.StatusServiceUnavailable)
		}
		for _, ch := range openOnly(open) {
			if ch.HeadSHA == s.req.Commit {
				return preflight{adopt: true, number: ch.Number, observed: Observed{Present: true, HeadSHA: ch.HeadSHA, Number: ch.Number, Source: "preflight", At: now}}, nil
			}
		}
		if len(openOnly(open)) > 0 {
			return refused("head_mismatch", http.StatusConflict)
		}
		o, err := host.Ref(ctx, tok, s.req.HeadRef)
		if err != nil || o.State != gp.RefPresent || o.SHA != s.req.Commit {
			return refused("content_mismatch", http.StatusUnprocessableEntity)
		}
		sha, e := baseHead(s.req.Base)
		if e != nil {
			return preflight{}, e
		}
		if e := workflows(sha, s.req.Commit); e != nil {
			return preflight{}, e
		}
	case effectMerge:
		ch, err := host.GetChange(ctx, tok, s.req.Number)
		if err != nil {
			return refused("lookup_incomplete", http.StatusServiceUnavailable)
		}
		if !ch.Open {
			if ch.Merged && ch.HeadSHA == s.req.ExpectedHead {
				return preflight{adopt: true, head: ch.HeadRef, observed: Observed{Present: true, Merged: true, HeadSHA: ch.HeadSHA, Number: ch.Number, MergeCommitSHA: ch.MergeCommitSHA, Source: "preflight", At: now}}, nil
			}
			return refused("not_mergeable", http.StatusConflict)
		}
		if !contains(tg.MergeBases, ch.BaseRef) {
			return refused("base_not_allowed", http.StatusUnprocessableEntity)
		}
		if ch.HeadSHA != s.req.ExpectedHead {
			return refused("head_mismatch", http.StatusConflict)
		}
		sha, e := baseHead(ch.BaseRef)
		if e != nil {
			return preflight{}, e
		}
		if e := workflows(sha, s.req.ExpectedHead); e != nil {
			return preflight{}, e
		}
		return preflight{head: ch.HeadRef}, nil
	}
	return preflight{}, nil
}

var errLostClaim = errors.New("gitpublish: lost the claim")

// lockScope takes the conflict scope's row in the claim transaction: it
// creates the row (unique per target and scope) or updates it under
// optimistic concurrency. The successful write holds the row until the
// transaction ends; a unique-key or version conflict refuses the claim.
// Callers must read unresolved intents after this succeeds. The store's
// per-tenant writer lock and the admitted authority precede this scope lock.
func lockScope(ctx context.Context, sc store.Scope, target, workspace model.ID, scope, holder string) error {
	repo, err := sc.Ext(kindScope)
	if err != nil {
		return err
	}
	recs, err := listAll(ctx, sc, kindScope, eq("target_id", target.String()), eq("scope_key", scope))
	if err != nil {
		return err
	}
	busy := refuse("unresolved_intent", http.StatusConflict)
	if len(recs) == 0 {
		if _, err := repo.Create(ctx, model.Record{"target_id": target.String(), "workspace_id": workspace.String(), "scope_key": scope, "holder": holder}); err != nil {
			return busy
		}
		return nil
	}
	rec := recs[0]
	rec["holder"] = holder
	if _, err := repo.Update(ctx, rec); err != nil {
		return busy
	}
	return nil
}

// claim is W1. It locks the admitted authority on the claim transaction,
// refuses a held conflict scope, and inserts (or re-arms) the intent. won is
// false when another call holds the operation.
func (m *Module) claim(ctx context.Context, c Caller, adm Admission, in Intent, rearm *Intent) (Intent, bool, *Error) {
	var out Intent
	won := false
	err := m.data.Mutate(ctx, c.Tenant, func(sc store.Scope) error {
		now := txNow(ctx, sc, m.now())
		if err := adm.Lock(ctx, sc, now); err != nil {
			return err
		}
		if rearm != nil {
			cur, err := loadIntent(ctx, sc, rearm.ID)
			if err != nil {
				return err
			}
			if cur.State != StateNotDispatched || cur.Attempt != rearm.Attempt {
				out = cur
				return nil
			}
			if err := lockScope(ctx, sc, cur.Target, cur.Workspace, cur.ScopeKey, cur.OperationID); err != nil {
				return err
			}
			// The early decision may predate another claim in this scope.
			if blocker, held, err := unresolvedInScope(ctx, sc, cur.Target, cur.ScopeKey, in.AcknowledgeIntent); err != nil || held {
				if err == nil {
					err = withIntent(refuse("unresolved_intent", http.StatusConflict), blocker.ID)
				}
				return err
			}
			cur.State, cur.Reason, cur.Attempt = StateDispatching, "", cur.Attempt+1
			cur.TargetVersion, cur.CredentialVersion, cur.RepositoryVersion = in.TargetVersion, in.CredentialVersion, in.RepositoryVersion
			cur.ClaimedAt, cur.DispatchDeadline = now, now.Add(m.opts.DispatchTimeout)
			out, err = update(ctx, sc, cur)
			if err != nil {
				return err
			}
			won = true
			if err := auditIntent(ctx, sc, out, "admitted"); err != nil {
				return err
			}
			return auditIntent(ctx, sc, out, "claimed")
		}
		if existing, found, err := findByOperation(ctx, sc, in.Target, in.Effect, in.OperationID); err != nil || found {
			out = existing
			return err
		}
		if err := lockScope(ctx, sc, in.Target, in.Workspace, in.ScopeKey, in.OperationID); err != nil {
			return err
		}
		if blocker, held, err := unresolvedInScope(ctx, sc, in.Target, in.ScopeKey, in.AcknowledgeIntent); err != nil || held {
			if err == nil {
				err = withIntent(refuse("unresolved_intent", http.StatusConflict), blocker.ID)
			}
			return err
		}
		in.ClaimedAt, in.DispatchDeadline = now, now.Add(m.opts.DispatchTimeout)
		repo, err := sc.Ext(kindIntent)
		if err != nil {
			return err
		}
		rec, err := repo.Create(ctx, in.record())
		if err != nil {
			return errLostClaim
		}
		out, won = intentFrom(rec), true
		if err := auditIntent(ctx, sc, out, "admitted"); err != nil {
			return err
		}
		if out.AcknowledgeIntent != "" {
			meta := intentMeta(out)
			meta["acknowledged_by_intent"] = out.ID.String()
			if err := appendAudit(ctx, sc, out.SubjectActor, out.SubjectActorKind, "gitpublish.intent.acknowledged", kindIntent, model.ID(out.AcknowledgeIntent), meta); err != nil {
				return err
			}
		}
		if out.State == StateAdopted {
			return auditIntent(ctx, sc, out, "adopted")
		}
		return auditIntent(ctx, sc, out, "claimed")
	})
	if errors.Is(err, errLostClaim) {
		err = m.data.View(ctx, c.Tenant, func(sc store.Scope) error {
			var found bool
			var e error
			out, found, e = findByOperation(ctx, sc, in.Target, in.Effect, in.OperationID)
			if e == nil && !found {
				return errLostClaim
			}
			return e
		})
		won = false
	}
	if err != nil {
		var e *Error
		if errors.As(err, &e) {
			return Intent{}, false, e
		}
		return Intent{}, false, authError(err)
	}
	return out, won, nil
}

// effectGate is A4.
func (m *Module) effectGate(ctx context.Context, c Caller, adm Admission, in Intent, writeDeadline time.Time, view View) *Error {
	if err := adm.Recheck(ctx, view, m.now()); err != nil {
		if errors.Is(err, auth.ErrRouteDenied) {
			return refuse("forbidden", http.StatusForbidden)
		}
		return authError(err)
	}
	var tg Target
	if err := view(ctx, func(sc store.Scope) error {
		var err error
		tg, err = loadTarget(ctx, sc, in.Target)
		return err
	}); err != nil || tg.Version != in.TargetVersion {
		return refuse("target_changed", http.StatusConflict)
	}
	cb, rb, e := m.bindings(ctx, c.Tenant, tg.Workspace, tg.CredentialBinding, tg.RepositoryBinding)
	if e != nil || cb.ID != in.CredentialBinding || cb.Version != in.CredentialVersion ||
		rb.ID != in.RepositoryBinding || rb.Version != in.RepositoryVersion || rb.RepoID != in.RepoID {
		return refuse("target_changed", http.StatusConflict)
	}
	if !m.now().Before(writeDeadline) {
		return refuse("dispatch_deadline_passed", http.StatusServiceUnavailable)
	}
	return nil
}

// settleNotDispatched records a proven no-dispatch.
func (m *Module) settleNotDispatched(ctx context.Context, c Caller, in Intent, reason string) Intent {
	out := in
	_ = m.data.Mutate(context.WithoutCancel(ctx), c.Tenant, func(sc store.Scope) error {
		cur, err := loadIntent(ctx, sc, in.ID)
		if err != nil || cur.State != StateDispatching || cur.Attempt != in.Attempt {
			if err == nil {
				out = cur
			}
			return err
		}
		cur.State, cur.Reason = StateNotDispatched, reason
		if out, err = update(ctx, sc, cur); err != nil {
			return err
		}
		if err := appendObservation(ctx, sc, out, Observation{Source: "gate", Result: StateNotDispatched, HostObject: reason, At: m.now()}, out.SubjectActor); err != nil {
			return err
		}
		return auditIntent(ctx, sc, out, "settled")
	})
	return out
}

// outcome is what one dispatch returned.
type outcome struct {
	res    gp.Result
	local  error
	change gp.Change
	merge  gp.MergeOutcome
}

func (m *Module) dispatch(ctx context.Context, host gp.Host, tok gp.Token, rb RepositoryBinding, in Intent, s spec) outcome {
	switch in.Effect {
	case effectPush:
		u, scheme, hdr := host.PushTarget(tok)
		req := gp.PushRequest{RepoPath: rb.LocalPath, URL: u, Scheme: scheme, Header: hdr, Ref: in.Requested.Ref, ExpectedOld: in.Requested.ExpectedOld, Commit: in.Requested.Commit}
		if scheme == "ssh" {
			// The ssh transport authenticates with the binding's own key,
			// which the minted token carries.
			req.Key = tok.Value()
		}
		res, err := m.opts.Git.Push(ctx, req)
		return outcome{res: res, local: err}
	case effectPullRequest:
		ch, res := host.CreateChange(ctx, tok, gp.ChangeSpec{Head: in.Requested.HeadRef, Base: in.Requested.Base, Title: in.Requested.Title, Body: s.body, Draft: s.draft})
		return outcome{res: res, change: ch}
	default:
		mo, res := host.MergeChange(ctx, tok, in.Requested.Number, in.Requested.ExpectedHead, in.Requested.Method)
		return outcome{res: res, merge: mo}
	}
}

// settle reads the host evidence and writes W2. It never re-arms.
func (m *Module) settle(ctx context.Context, c Caller, host gp.Host, tok gp.Token, in Intent, out outcome) Intent {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), settleTimeout)
	defer cancel()
	now := m.now()
	next := in
	switch {
	case out.local != nil:
		reason := "repository_config_refused"
		switch {
		case errors.Is(out.local, gp.ErrDestination), errors.Is(out.local, gp.ErrTransportStart):
			reason = "destination_refused"
		case errors.Is(out.local, gp.ErrContent):
			reason = "content_mismatch"
		}
		return m.settleNotDispatched(ctx, c, in, reason)
	case out.res.Class == gp.Applied:
		next.Acknowledged = Acknowledged{Acknowledged: true, Status: out.res.Host.Status, RequestID: out.res.Host.RequestID, At: now}
		next.State, next.Receipt = StateApplied, ReceiptCaused
		if out.res.Reason == "up_to_date" {
			next.State, next.Receipt, next.Acknowledged = StateAdopted, ReceiptExistingEffect, Acknowledged{}
		}
		var why string
		next.Observed, why = m.evidence(rctx, host, tok, in, out)
		if next.State == StateApplied {
			// Acknowledged by the host, so applied; evidence that is missing or
			// contradicts it stays visible in the reason.
			next.Reason = why
		}
		if in.Effect == effectPullRequest {
			next.ContentMatch = next.Observed.HeadSHA == in.Requested.Commit
		}
	case out.res.Class == gp.Rejected:
		next.State, next.Reason = StateRejected, out.res.Reason
		if in.Effect == effectPullRequest {
			if open, err := host.OpenChanges(rctx, tok, in.Requested.HeadRef, in.Requested.Base); err == nil {
				for _, ch := range openOnly(open) {
					if ch.HeadSHA == in.Requested.Commit {
						next.State, next.Receipt, next.Reason = StateAdopted, ReceiptExistingEffect, ""
						next.Observed = Observed{Present: true, HeadSHA: ch.HeadSHA, Number: ch.Number, Source: "dispatcher", At: now}
						next.ContentMatch = true
					}
				}
				if next.State == StateRejected && len(openOnly(open)) > 0 {
					next.Reason = "pull_request_exists"
				}
			}
		}
	default:
		next.State, next.Reason = StateUncertain, out.res.Reason
	}
	var result Intent
	err := m.data.Mutate(rctx, c.Tenant, func(sc store.Scope) error {
		cur, err := loadIntent(rctx, sc, in.ID)
		if err != nil {
			return err
		}
		if cur.State != StateDispatching || cur.Attempt != in.Attempt {
			// W2 lost: keep the winner, record ours as an observation; an
			// acknowledgment of THIS attempt still resolves its uncertainty.
			result = cur
			if err := appendObservation(rctx, sc, cur, Observation{Source: "w2_loser", Result: next.State, Status: next.Acknowledged.Status, RequestID: next.Acknowledged.RequestID, At: now}, in.SubjectActor); err != nil {
				return err
			}
			if cur.State == StateUncertain && cur.Attempt == in.Attempt && (next.State == StateApplied || next.State == StateRejected) {
				// This attempt's own acknowledgment or documented refusal
				// resolves the uncertainty the sweep recorded for it.
				cur.State, cur.Receipt, cur.Acknowledged, cur.Observed, cur.Reason = next.State, next.Receipt, next.Acknowledged, next.Observed, next.Reason
				if result, err = update(rctx, sc, cur); err != nil {
					return err
				}
				return auditIntent(rctx, sc, result, "settled")
			}
			return err
		}
		next.Version = cur.Version
		if err := appendObservation(rctx, sc, next, Observation{Source: "dispatcher", Result: next.State, HostObject: next.Observed.SHA + next.Observed.HeadSHA, Status: out.res.Host.Status, RequestID: out.res.Host.RequestID, At: now}, in.SubjectActor); err != nil {
			return err
		}
		if result, err = update(rctx, sc, next); err != nil {
			return err
		}
		return auditIntent(rctx, sc, result, "settled")
	})
	if err != nil {
		return in
	}
	if result.State == StateUncertain {
		if r, e := m.observeWith(ctx, c, host, tok, result, "dispatcher", ""); e == nil {
			result = r.Intent
		}
	}
	return result
}

// evidence reads the host state an applied write must show, and names what
// is missing or contradictory ("" when the evidence matches).
func (m *Module) evidence(ctx context.Context, host gp.Host, tok gp.Token, in Intent, out outcome) (Observed, string) {
	now := m.now()
	switch in.Effect {
	case effectPush:
		o, err := host.Ref(ctx, tok, strings.TrimPrefix(in.Requested.Ref, "refs/heads/"))
		if err != nil || o.State != gp.RefPresent {
			return Observed{Source: "dispatcher", At: now}, "evidence_unavailable"
		}
		obs := Observed{Present: o.SHA == in.Requested.Commit, SHA: o.SHA, Source: "dispatcher", At: now}
		if !obs.Present {
			return obs, "evidence_contradiction"
		}
		return obs, ""
	case effectPullRequest:
		ch, err := host.GetChange(ctx, tok, out.change.Number)
		why := ""
		if err != nil {
			ch, why = out.change, "evidence_unavailable"
		}
		return Observed{Present: ch.Open, HeadSHA: ch.HeadSHA, Number: ch.Number, Source: "dispatcher", At: now}, why
	default:
		ch, err := host.GetChange(ctx, tok, in.Requested.Number)
		o := Observed{Merged: out.merge.Merged, MergeCommitSHA: out.merge.MergeCommitSHA, Number: in.Requested.Number, Source: "dispatcher", At: now}
		why := ""
		if err != nil {
			why = "evidence_unavailable"
		} else {
			o.Merged, o.HeadSHA, o.Present = ch.Merged, ch.HeadSHA, ch.Merged
			if o.MergeCommitSHA == "" {
				o.MergeCommitSHA = ch.MergeCommitSHA
			}
			if !ch.Merged {
				why = "evidence_contradiction"
			}
		}
		if o.MergeCommitSHA != "" {
			if tree, terr := host.CommitTree(ctx, tok, o.MergeCommitSHA); terr == nil {
				o.MergeTree = tree
			}
		}
		return o, why
	}
}
