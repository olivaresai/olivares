// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package gitpublish

import (
	"context"
	"errors"
	"net/http"
	"strings"

	gp "github.com/olivaresai/olivares/connectors/gitpublish"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// hostBaseHead is the commit the host reports for a branch. A failed or
// absent read refuses; it is never taken as "no base".
func hostBaseHead(ctx context.Context, host gp.Host, tok gp.Token, base string) (string, *Error) {
	o, err := host.Ref(ctx, tok, base)
	if err != nil || o.State != gp.RefPresent {
		return "", refuse("lookup_incomplete", http.StatusServiceUnavailable)
	}
	return o.SHA, nil
}

// pushBase is the commit a push is compared with: the lease it names, else the
// head of the target's first merge base.
func pushBase(ctx context.Context, host gp.Host, tok gp.Token, tg Target, s spec) (string, *Error) {
	if s.req.ExpectedOld != "" {
		return s.req.ExpectedOld, nil
	}
	if len(tg.MergeBases) == 0 {
		return "", refuse("content_mismatch", http.StatusUnprocessableEntity)
	}
	return hostBaseHead(ctx, host, tok, tg.MergeBases[0])
}

// PushChanges lists the paths the commit of a push would change on the host,
// before anyone is asked to approve it. The caller is the session's launcher
// and workspace the session's: the same target, authority, ref rules and
// session feed as the push itself, and the same base it is compared with. It
// reads the host with an observation capability and publishes nothing; like the
// publish, it first feeds the session's commit into the managed repository.
func (m *Module) PushChanges(ctx context.Context, c Caller, workspace model.ID, in PushInput) ([]string, error) {
	if e := in.check(); e != nil {
		return nil, e
	}
	s := spec{effect: effectPush, perm: permPush, action: actionPush, host: gp.EffectPush, target: in.Target,
		req:    Requested{Ref: in.Ref, ExpectedOld: in.ExpectedOld, Commit: in.Commit, Tree: in.Tree},
		branch: strings.TrimPrefix(in.Ref, "refs/heads/"), session: in.SessionRun}
	return m.changes(ctx, c, workspace, s)
}

// PullRequestChanges lists the paths a pull request would bring into its base:
// those of its head commit since the base's head. Same checks as PushChanges.
func (m *Module) PullRequestChanges(ctx context.Context, c Caller, workspace model.ID, in PullRequestInput) ([]string, error) {
	if e := in.check(); e != nil {
		return nil, e
	}
	s := spec{effect: effectPullRequest, perm: permPullRequest, action: actionPullRequest, host: gp.EffectPullRequest, target: in.Target,
		req: Requested{HeadRef: in.HeadRef, Base: in.Base, Commit: in.Commit, Title: in.Title}}
	return m.changes(ctx, c, workspace, s)
}

// changes is the shared read: admission as the publish would ask it, the
// target's rules, the commit in the managed repository, then the paths between
// the base and the commit.
func (m *Module) changes(ctx context.Context, c Caller, workspace model.ID, s spec) ([]string, error) {
	if !m.wired() {
		return nil, errUnavailable
	}
	if c.Principal.SessionIdentity != "" || c.Principal.SessionRunRef != "" {
		return nil, errRuntimeCredential
	}
	if s.target.IsZero() {
		return nil, errInvalid
	}
	actx, cancel := m.admissionContext(ctx)
	defer cancel()
	var tg Target
	if err := m.data.View(actx, c.Tenant, func(sc store.Scope) error {
		var err error
		tg, err = loadTarget(actx, sc, s.target)
		return err
	}); err != nil {
		return nil, storeError(err)
	}
	if tg.Workspace != workspace {
		// A session reads only its own workspace's targets.
		return nil, errNotFound
	}
	if _, err := m.opts.Authority.Admit(actx, c.Principal, c.Tenant, Question{Permission: s.perm, Action: s.action, Target: tg.ID, Workspace: tg.Workspace}); err != nil {
		return nil, authError(err)
	}
	cb, rb, e := m.bindings(actx, c.Tenant, tg.Workspace, tg.CredentialBinding, tg.RepositoryBinding)
	if e != nil {
		return nil, e
	}
	s.capabilities = gp.TargetCapabilitiesFor(cb.Host)
	if !s.capabilities.Supports(s.host, s.req.Method) {
		return nil, errUnsupported
	}
	if e := targetRules(tg, s); e != nil {
		return nil, e
	}
	if e := m.feed(actx, c, tg, rb, s); e != nil {
		return nil, e
	}
	// What a person is asked to approve is refused here as the publish would:
	// the commit is in the repository and has the tree the proposal names.
	tree, err := m.opts.Git.CommitTree(actx, rb.LocalPath, s.req.Commit)
	if err != nil || (s.req.Tree != "" && tree != s.req.Tree) {
		return nil, refuse("content_mismatch", http.StatusUnprocessableEntity)
	}
	host, err := m.opts.Custody.OpenHost(actx, c.Tenant, cb, rb)
	if err != nil {
		return nil, refuse("host_unavailable", http.StatusBadGateway)
	}
	tok, err := host.Mint(actx, gp.EffectObserve)
	if err != nil {
		return nil, refuse("host_unavailable", http.StatusBadGateway)
	}
	rel := &releaser{host: host, tok: tok}
	defer rel.release(ctx) // a panic still releases; release runs once
	paths, rerr := m.readChangedPaths(actx, host, tok, tg, rb, s)
	// A capability that could not be released is as refused as a read that
	// failed: the answer would otherwise leave a live token behind unrecorded.
	released := rel.release(ctx)
	if rerr != nil {
		return nil, rerr
	}
	if released != "" {
		return nil, refuse("host_unavailable", http.StatusBadGateway)
	}
	return paths, nil
}

func (m *Module) readChangedPaths(ctx context.Context, host gp.Host, tok gp.Token, tg Target, rb RepositoryBinding, s spec) ([]string, error) {
	var from string
	var e *Error
	if s.effect == effectPush {
		from, e = pushBase(ctx, host, tok, tg, s)
	} else {
		from, e = hostBaseHead(ctx, host, tok, s.req.Base)
	}
	if e != nil {
		return nil, e
	}
	// A push that names a lease moves a branch from that commit: what only the
	// old tip had goes away, so every path that differs counts. Otherwise the
	// change is what the commit adds since it left the base.
	leased := s.effect == effectPush && s.req.ExpectedOld != ""
	paths, err := m.opts.Git.ChangedPaths(ctx, rb.LocalPath, from, s.req.Commit, !leased)
	switch {
	case errors.Is(err, gp.ErrContent):
		return nil, refuse("content_mismatch", http.StatusUnprocessableEntity)
	case err != nil:
		return nil, refuse("session_source_unavailable", http.StatusServiceUnavailable)
	}
	return paths, nil
}
