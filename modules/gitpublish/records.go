// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package gitpublish

import (
	"context"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Intent states (CONTRACT-J10-S3 §3.14).
const (
	StateDispatching   = "dispatching"
	StateApplied       = "applied"
	StateAdopted       = "adopted"
	StateRejected      = "rejected"
	StateNotDispatched = "not_dispatched"
	StateUncertain     = "uncertain"
	StateAbandoned     = "abandoned"
)

// Receipt kinds: whether OUR request caused the effect, or an existing
// matching effect was adopted.
const (
	ReceiptCaused         = "caused"
	ReceiptExistingEffect = "existing_effect"
	ReceiptNone           = "none"
)

// Effects.
const (
	effectPush        = "push"
	effectPullRequest = "pull_request"
	effectMerge       = "merge"
)

// Target is an approved publication target.
type Target struct {
	ID                model.ID
	Workspace         model.ID
	CredentialBinding string
	RepositoryBinding string
	PushPrefix        string
	MergeBases        []string
	Version           int64
}

func targetFrom(r model.Record) Target {
	var bases []string
	if s := r.String("merge_bases"); s != "" {
		bases = strings.Split(s, ",")
	}
	return Target{
		ID: model.ID(r.String(model.ColID)), Workspace: model.ID(r.String("workspace_id")),
		CredentialBinding: r.String("credential_binding"), RepositoryBinding: r.String("repository_binding"),
		PushPrefix: r.String("push_prefix"), MergeBases: bases, Version: r.Int(model.ColVersion),
	}
}

// Requested is what the caller asked for.
type Requested struct {
	Ref, ExpectedOld, Commit, Tree string
	HeadRef, Base, Title           string
	Number                         int
	ExpectedHead, Method           string
}

// Observed is the latest host observation, with its source and time.
type Observed struct {
	Present        bool
	SHA            string
	HeadSHA        string
	Number         int
	Merged         bool
	MergeCommitSHA string
	MergeTree      string
	Source         string
	At             time.Time
}

// Acknowledged records a host success response to OUR dispatched request.
type Acknowledged struct {
	Acknowledged bool
	Status       int
	RequestID    string
	At           time.Time
}

// Intent is one publication effect.
type Intent struct {
	ID                model.ID
	Target            model.ID
	Workspace         model.ID
	TargetVersion     int64
	CredentialBinding string
	CredentialVersion int64
	RepositoryBinding string
	RepositoryVersion int64
	RepoID            string
	Effect            string
	OperationID       string
	ScopeKey          string
	Digest            string
	SubjectActor      string
	SubjectActorKind  string
	SubjectUser       string
	AgentIdentity     string
	AuthorizedBy      string
	Attempt           int64
	State             string
	Reason            string
	Receipt           string
	Requested         Requested
	Observed          Observed
	ContentMatch      bool
	Acknowledged      Acknowledged
	AcknowledgeIntent string
	ClaimedAt         time.Time
	DispatchDeadline  time.Time
	ReleaseFailure    string
	// Proposal is the approved session request this intent carries out; its
	// workspace is the intent's.
	Proposal Proposal
	Version  int64
}

func ts(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTS(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (in Intent) record() model.Record {
	r := model.Record{
		"target_id": in.Target.String(), "workspace_id": in.Workspace.String(), "target_version": in.TargetVersion,
		"credential_binding": in.CredentialBinding, "credential_binding_version": in.CredentialVersion,
		"repository_binding": in.RepositoryBinding, "repository_binding_version": in.RepositoryVersion, "repo_id": in.RepoID,
		"effect": in.Effect, "operation_id": in.OperationID, "scope_key": in.ScopeKey, "request_digest": in.Digest,
		"subject_actor": in.SubjectActor, "subject_actor_kind": in.SubjectActorKind, "subject_user": nullable(in.SubjectUser),
		"agent_identity": nullable(in.AgentIdentity), "authorized_by": nullable(in.AuthorizedBy),
		"attempt": in.Attempt, "state": in.State, "reason": in.Reason, "receipt": in.Receipt,
		"req_ref": in.Requested.Ref, "req_expected_old": in.Requested.ExpectedOld, "req_commit": in.Requested.Commit, "req_tree": in.Requested.Tree,
		"req_head_ref": in.Requested.HeadRef, "req_base": in.Requested.Base, "req_number": int64(in.Requested.Number),
		"req_expected_head": in.Requested.ExpectedHead, "req_method": in.Requested.Method, "req_title": in.Requested.Title,
		"obs_present": in.Observed.Present, "obs_sha": in.Observed.SHA, "obs_head_sha": in.Observed.HeadSHA, "obs_number": int64(in.Observed.Number),
		"obs_merged": in.Observed.Merged, "obs_merge_commit_sha": in.Observed.MergeCommitSHA, "obs_merge_tree": in.Observed.MergeTree, "obs_source": in.Observed.Source, "obs_at": ts(in.Observed.At),
		"content_match": in.ContentMatch,
		"ack":           in.Acknowledged.Acknowledged, "ack_status": int64(in.Acknowledged.Status), "ack_request_id": in.Acknowledged.RequestID, "ack_at": ts(in.Acknowledged.At),
		"acknowledge_intent": nullable(in.AcknowledgeIntent),
		"claimed_at":         ts(in.ClaimedAt), "dispatch_deadline": ts(in.DispatchDeadline), "release_failure": in.ReleaseFailure,
		"proposal_session_run": nullable(in.Proposal.SessionRun), "proposal_approval": nullable(in.Proposal.Approval),
	}
	if in.ID != "" {
		r[model.ColID] = in.ID.String()
		r[model.ColVersion] = in.Version
	}
	return r
}

func intentFrom(r model.Record) Intent {
	var p Proposal
	if run := r.String("proposal_session_run"); run != "" {
		p = Proposal{SessionRun: run, Workspace: model.ID(r.String("workspace_id")), Approval: r.String("proposal_approval")}
	}
	return Intent{
		ID: model.ID(r.String(model.ColID)), Target: model.ID(r.String("target_id")), Workspace: model.ID(r.String("workspace_id")),
		TargetVersion: r.Int("target_version"), CredentialBinding: r.String("credential_binding"), CredentialVersion: r.Int("credential_binding_version"),
		RepositoryBinding: r.String("repository_binding"), RepositoryVersion: r.Int("repository_binding_version"), RepoID: r.String("repo_id"),
		Effect: r.String("effect"), OperationID: r.String("operation_id"), ScopeKey: r.String("scope_key"), Digest: r.String("request_digest"),
		SubjectActor: r.String("subject_actor"), SubjectActorKind: r.String("subject_actor_kind"), SubjectUser: r.String("subject_user"),
		AgentIdentity: r.String("agent_identity"), AuthorizedBy: r.String("authorized_by"),
		Attempt: r.Int("attempt"), State: r.String("state"), Reason: r.String("reason"), Receipt: r.String("receipt"),
		Requested: Requested{
			Ref: r.String("req_ref"), ExpectedOld: r.String("req_expected_old"), Commit: r.String("req_commit"), Tree: r.String("req_tree"),
			HeadRef: r.String("req_head_ref"), Base: r.String("req_base"), Number: int(r.Int("req_number")),
			ExpectedHead: r.String("req_expected_head"), Method: r.String("req_method"), Title: r.String("req_title"),
		},
		Observed: Observed{
			Present: r.Bool("obs_present"), SHA: r.String("obs_sha"), HeadSHA: r.String("obs_head_sha"), Number: int(r.Int("obs_number")),
			Merged: r.Bool("obs_merged"), MergeCommitSHA: r.String("obs_merge_commit_sha"), MergeTree: r.String("obs_merge_tree"), Source: r.String("obs_source"), At: parseTS(r.String("obs_at")),
		},
		ContentMatch:      r.Bool("content_match"),
		Acknowledged:      Acknowledged{Acknowledged: r.Bool("ack"), Status: int(r.Int("ack_status")), RequestID: r.String("ack_request_id"), At: parseTS(r.String("ack_at"))},
		AcknowledgeIntent: r.String("acknowledge_intent"),
		ClaimedAt:         parseTS(r.String("claimed_at")), DispatchDeadline: parseTS(r.String("dispatch_deadline")),
		ReleaseFailure: r.String("release_failure"), Version: r.Int(model.ColVersion), Proposal: p,
	}
}

// Observation is one appended host read or dispatcher outcome.
type Observation struct {
	Attempt    int64
	Source     string
	Result     string
	HostObject string
	Status     int
	RequestID  string
	At         time.Time
}

func eq(col string, v any) model.Filter { return model.Filter{Column: col, Op: model.OpEq, Value: v} }

// listAll walks every page matching filters.
func listAll(ctx context.Context, sc store.Scope, kind model.Kind, filters ...model.Filter) ([]model.Record, error) {
	repo, err := sc.Ext(kind)
	if err != nil {
		return nil, err
	}
	var all []model.Record
	q := model.Query{Filters: filters, Limit: 200}
	for {
		recs, page, err := repo.List(ctx, q)
		if err != nil {
			return nil, err
		}
		all = append(all, recs...)
		if !page.HasMore || page.Cursor == "" {
			return all, nil
		}
		q.Cursor = page.Cursor
	}
}

// txNow is the database transaction time when the scope offers it.
func txNow(ctx context.Context, sc store.Scope, fallback time.Time) time.Time {
	if c, ok := sc.(store.TransactionClock); ok {
		if t, err := c.TransactionNow(ctx); err == nil {
			return t.Time().UTC()
		}
	}
	return fallback
}

func loadTarget(ctx context.Context, sc store.Scope, id model.ID) (Target, error) {
	repo, err := sc.Ext(kindTarget)
	if err != nil {
		return Target{}, err
	}
	rec, err := repo.Get(ctx, id)
	if err != nil {
		return Target{}, err
	}
	return targetFrom(rec), nil
}

func loadIntent(ctx context.Context, sc store.Scope, id model.ID) (Intent, error) {
	repo, err := sc.Ext(kindIntent)
	if err != nil {
		return Intent{}, err
	}
	rec, err := repo.Get(ctx, id)
	if err != nil {
		return Intent{}, err
	}
	return intentFrom(rec), nil
}

func findByOperation(ctx context.Context, sc store.Scope, target model.ID, effect, op string) (Intent, bool, error) {
	recs, err := listAll(ctx, sc, kindIntent, eq("target_id", target.String()), eq("effect", effect), eq("operation_id", op))
	if err != nil || len(recs) == 0 {
		return Intent{}, false, err
	}
	return intentFrom(recs[0]), true, nil
}

// unresolvedInScope returns an intent of the conflict scope that still holds
// it: dispatching, uncertain, or abandoned without the caller's explicit
// acknowledgment.
func unresolvedInScope(ctx context.Context, sc store.Scope, target model.ID, scope, acknowledged string) (Intent, bool, error) {
	recs, err := listAll(ctx, sc, kindIntent, eq("target_id", target.String()), eq("scope_key", scope))
	if err != nil {
		return Intent{}, false, err
	}
	for _, r := range recs {
		in := intentFrom(r)
		switch in.State {
		case StateDispatching, StateUncertain:
			return in, true, nil
		case StateAbandoned:
			if in.ID.String() != acknowledged {
				return in, true, nil
			}
		}
	}
	return Intent{}, false, nil
}

// update writes in with optimistic concurrency on its version.
func update(ctx context.Context, sc store.Scope, in Intent) (Intent, error) {
	repo, err := sc.Ext(kindIntent)
	if err != nil {
		return Intent{}, err
	}
	rec, err := repo.Update(ctx, in.record())
	if err != nil {
		return Intent{}, err
	}
	return intentFrom(rec), nil
}

func appendObservation(ctx context.Context, sc store.Scope, in Intent, o Observation, actor string) error {
	repo, err := sc.Ext(kindObservation)
	if err != nil {
		return err
	}
	_, err = repo.Create(ctx, model.Record{
		"intent_id": in.ID.String(), "workspace_id": in.Workspace.String(), "attempt": in.Attempt,
		"source": o.Source, "result": o.Result, "host_object": o.HostObject, "status": int64(o.Status),
		"request_id": o.RequestID, "at": ts(o.At), "actor": nullable(actor),
	})
	return err
}
