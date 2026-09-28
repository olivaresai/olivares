// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package gitpublish

import (
	"context"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	gp "github.com/olivaresai/olivares/connectors/gitpublish"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Reconcile observes an intent's host state. It never dispatches. An
// uncertain intent moves only to adopted, when the requested effect is
// observed; every other observation is appended and uncertainty is kept.
func (m *Module) Reconcile(ctx context.Context, c Caller, id model.ID) (Receipt, error) {
	in, e := m.admitIntent(ctx, c, id, actionReconcile, 0, false)
	if e != nil {
		return Receipt{}, e
	}
	if in.State != StateUncertain {
		return Receipt{Intent: in, Answer: in.State}, nil
	}
	return m.observe(ctx, c, in, "reconcile", "")
}

// Abandon records an administrator's responsibility for an unresolved
// intent. It proves nothing about the host and keeps the conflict scope held
// until a new request names this intent in acknowledge_intent.
func (m *Module) Abandon(ctx context.Context, c Caller, id model.ID, reason string) (Receipt, error) {
	reason = boundedText(reason, 256)
	in, e := m.admitIntent(ctx, c, id, actionAbandon, auth.AAL3, true)
	if e != nil {
		return Receipt{}, e
	}
	switch in.State {
	case StateUncertain, StateDispatching, StateNotDispatched:
	default:
		return Receipt{}, withIntent(refuse("intent_settled", http.StatusConflict), in.ID)
	}
	actor := SubjectOf(c.Principal).Actor
	var out Intent
	err := m.data.Mutate(ctx, c.Tenant, func(sc store.Scope) error {
		cur, err := loadIntent(ctx, sc, id)
		if err != nil {
			return err
		}
		if cur.Version != in.Version {
			return store.ErrConflict
		}
		cur.State, cur.AuthorizedBy = StateAbandoned, actor
		if reason != "" {
			cur.Reason = reason
		}
		out, err = update(ctx, sc, cur)
		if err != nil {
			return err
		}
		if err := appendObservation(ctx, sc, out, Observation{Source: "abandon", Result: StateAbandoned, At: m.now()}, actor); err != nil {
			return err
		}
		meta := intentMeta(out)
		meta["prior_state"], meta["prior_reason"] = in.State, in.Reason
		return appendAudit(ctx, sc, actor, c.Principal.ActorKind(), "gitpublish.intent.abandoned", kindIntent, out.ID, meta)
	})
	if err != nil {
		return Receipt{}, storeError(err)
	}
	return Receipt{Intent: out, Answer: out.State}, nil
}

// Observations lists an intent's appended observations.
func (m *Module) Observations(ctx context.Context, c Caller, id model.ID) ([]Observation, error) {
	if _, e := m.admitIntent(ctx, c, id, "", 0, false); e != nil {
		return nil, e
	}
	var out []Observation
	err := m.data.View(ctx, c.Tenant, func(sc store.Scope) error {
		recs, err := listAll(ctx, sc, kindObservation, eq("intent_id", id.String()))
		for _, r := range recs {
			out = append(out, Observation{Attempt: r.Int("attempt"), Source: r.String("source"), Result: r.String("result"),
				HostObject: r.String("host_object"), Status: int(r.Int("status")), RequestID: r.String("request_id"), At: parseTS(r.String("at"))})
		}
		return err
	})
	return out, storeError(err)
}

// Intents lists a target's intents.
func (m *Module) Intents(ctx context.Context, c Caller, target model.ID) ([]Intent, error) {
	if !m.wired() {
		return nil, errUnavailable
	}
	var tg Target
	if err := m.data.View(ctx, c.Tenant, func(sc store.Scope) error {
		var err error
		tg, err = loadTarget(ctx, sc, target)
		return err
	}); err != nil {
		return nil, storeError(err)
	}
	if _, err := m.opts.Authority.Admit(ctx, c.Principal, c.Tenant, Question{Permission: permTargetRead, Target: tg.ID, Workspace: tg.Workspace}); err != nil {
		return nil, authError(err)
	}
	var out []Intent
	err := m.data.View(ctx, c.Tenant, func(sc store.Scope) error {
		recs, err := listAll(ctx, sc, kindIntent, eq("target_id", target.String()))
		for _, r := range recs {
			out = append(out, intentFrom(r))
		}
		return err
	})
	return out, storeError(err)
}

// admitIntent loads an intent and authorizes the caller over its stored
// target: the intent's own effect permission (reconcile), target:admin
// (abandon) or target:read (reads).
func (m *Module) admitIntent(ctx context.Context, c Caller, id model.ID, action auth.CedarAction, aal int, admin bool) (Intent, *Error) {
	if !m.wired() {
		return Intent{}, errUnavailable
	}
	var in Intent
	if err := m.data.View(ctx, c.Tenant, func(sc store.Scope) error {
		var err error
		in, err = loadIntent(ctx, sc, id)
		return err
	}); err != nil {
		return Intent{}, errNotFound
	}
	perm := permTargetRead
	switch {
	case admin:
		perm = permTargetAdmin
	case action == actionReconcile:
		perm = map[string]auth.Permission{effectPush: permPush, effectPullRequest: permPullRequest, effectMerge: permMerge}[in.Effect]
	}
	if _, err := m.opts.Authority.Admit(ctx, c.Principal, c.Tenant, Question{Permission: perm, Action: action, MinimumAAL: aal, Target: in.Target, Workspace: in.Workspace}); err != nil {
		return Intent{}, authError(err)
	}
	return in, nil
}

// observe mints a read-only capability, observes, and releases it.
func (m *Module) observe(ctx context.Context, c Caller, in Intent, source, actor string) (Receipt, error) {
	var tg Target
	if err := m.data.View(ctx, c.Tenant, func(sc store.Scope) error {
		var err error
		tg, err = loadTarget(ctx, sc, in.Target)
		return err
	}); err != nil {
		return Receipt{Intent: in, Answer: in.State}, nil
	}
	cb, rb, e := m.bindings(ctx, c.Tenant, tg.Workspace, in.CredentialBinding, in.RepositoryBinding)
	if e != nil {
		return Receipt{Intent: in, Answer: in.State}, nil
	}
	if cb.Version != in.CredentialVersion || rb.Version != in.RepositoryVersion || rb.RepoID != in.RepoID {
		// The binding now names another credential or repository: what it
		// shows is not evidence about this intent, which stays uncertain.
		return m.recordUnknown(ctx, c, in, source, actor, "binding_changed")
	}
	host, err := m.opts.Custody.OpenHost(ctx, c.Tenant, cb, rb)
	if err != nil {
		return Receipt{Intent: in, Answer: in.State}, nil
	}
	tok, err := host.Mint(ctx, gp.EffectObserve)
	if err != nil {
		return Receipt{Intent: in, Answer: in.State}, nil
	}
	rel := &releaser{host: host, tok: tok}
	defer func() {
		if code := rel.release(ctx); code != "" {
			m.recordReleaseFailure(ctx, c, in.ID, in.Target, code)
		}
	}()
	return m.observeWith(ctx, c, host, tok, in, source, actor)
}

// recordUnknown appends an observation that could not be made about this
// intent and keeps its state.
func (m *Module) recordUnknown(ctx context.Context, c Caller, in Intent, source, actor, why string) (Receipt, error) {
	out := in
	_ = m.data.Mutate(context.WithoutCancel(ctx), c.Tenant, func(sc store.Scope) error {
		cur, err := loadIntent(ctx, sc, in.ID)
		if err != nil {
			return err
		}
		out = cur
		if err := appendObservation(ctx, sc, cur, Observation{Attempt: cur.Attempt, Source: source, Result: "unknown", HostObject: why, At: m.now()}, actor); err != nil {
			return err
		}
		meta := intentMeta(cur)
		meta["result"], meta["source"] = "unknown:"+why, source
		kind := model.ActorUser
		if actor == "" {
			kind = ""
		}
		return appendAudit(ctx, sc, actor, kind, "gitpublish.intent.reconciled", kindIntent, cur.ID, meta)
	})
	return Receipt{Intent: out, Answer: out.State}, nil
}

// observeWith reads the host and records one observation. Only a matching
// effect ends uncertainty (adopted, existing_effect, acknowledgment kept as
// it was); a 404, an incomplete lookup, an old ref or an open unmerged pull
// request keeps it.
func (m *Module) observeWith(ctx context.Context, c Caller, host gp.Host, tok gp.Token, in Intent, source, actor string) (Receipt, error) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	now := m.now()
	obs := Observation{Attempt: in.Attempt, Source: source, Result: "unknown", At: now}
	seen := Observed{Source: source, At: now}
	adopt := false
	switch in.Effect {
	case effectPush:
		o, err := host.Ref(rctx, tok, strings.TrimPrefix(in.Requested.Ref, "refs/heads/"))
		if err == nil && o.State == gp.RefPresent {
			seen.SHA, obs.HostObject = o.SHA, o.SHA
			switch {
			case o.SHA == in.Requested.Commit:
				adopt, seen.Present, obs.Result = true, true, "present"
			case o.SHA == in.Requested.ExpectedOld:
				obs.Result = "absent_or_old"
			default:
				obs.Result = "contradiction"
			}
		}
	case effectPullRequest:
		open, err := host.OpenChanges(rctx, tok, in.Requested.HeadRef, in.Requested.Base)
		if err == nil {
			obs.Result = "absent_or_old"
			for _, ch := range openOnly(open) {
				if ch.HeadSHA == in.Requested.Commit && !ch.CreatedAt.Before(in.ClaimedAt.Add(-m.opts.Skew)) {
					adopt, obs.Result = true, "present"
					seen.Present, seen.HeadSHA, seen.Number = true, ch.HeadSHA, ch.Number
				}
			}
		}
	case effectMerge:
		ch, err := host.GetChange(rctx, tok, in.Requested.Number)
		if err == nil {
			seen.Number, seen.HeadSHA, seen.Merged, seen.MergeCommitSHA = ch.Number, ch.HeadSHA, ch.Merged, ch.MergeCommitSHA
			switch {
			case ch.Merged && ch.HeadSHA == in.Requested.ExpectedHead:
				adopt, seen.Present, obs.Result = true, true, "present"
			case ch.Open:
				obs.Result = "absent_or_old"
			default:
				obs.Result = "contradiction"
			}
		}
	}
	var out Intent
	err := m.data.Mutate(rctx, c.Tenant, func(sc store.Scope) error {
		cur, err := loadIntent(rctx, sc, in.ID)
		if err != nil {
			return err
		}
		if err := appendObservation(rctx, sc, cur, obs, actor); err != nil {
			return err
		}
		out = cur
		if cur.State != StateUncertain {
			return nil
		}
		cur.Observed = seen
		if adopt {
			cur.State, cur.Receipt = StateAdopted, ReceiptExistingEffect
		}
		if out, err = update(rctx, sc, cur); err != nil {
			return err
		}
		meta := intentMeta(out)
		meta["result"], meta["source"] = obs.Result, source
		kind := model.ActorUser
		if actor == "" {
			kind = ""
		} else if strings.HasPrefix(actor, "token:") {
			kind = "token"
		}
		return appendAudit(rctx, sc, actor, kind, "gitpublish.intent.reconciled", kindIntent, out.ID, meta)
	})
	if err != nil {
		return Receipt{Intent: in, Answer: in.State}, nil
	}
	return Receipt{Intent: out, Answer: out.State}, nil
}

// boundedText keeps at most n bytes of printable text, cut on a rune boundary.
func boundedText(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, strings.TrimSpace(s))
	for len(s) > n {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return s
}
