// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package gitpublish

import (
	"context"
	"strconv"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The sweep's audit actor: no caller acts, the engine does.
const sweepActor = "system:gitpublish-sweep"

// appendAudit appends one event to the tenant ledger in the caller's transaction.
// Metadata is bounded and names host objects, states and reasons only:
// never a token, key, secret name, server path or request body.
func appendAudit(ctx context.Context, sc store.Scope, actor, actorKind, action string, kind model.Kind, id model.ID, meta map[string]any) error {
	if actor == "" {
		actor, actorKind = sweepActor, model.ActorSystem
	}
	_, err := sc.Audit().Append(ctx, model.AuditDraft{Actor: actor, ActorKind: actorKind, Action: action, TargetKind: kind, TargetID: id, Meta: meta})
	return err
}

// intentMeta is the bounded metadata of an intent event.
func intentMeta(in Intent) map[string]any {
	m := map[string]any{
		"target_id": in.Target.String(), "effect": in.Effect, "operation_id": in.OperationID,
		"attempt": strconv.FormatInt(in.Attempt, 10), "state": in.State, "receipt": in.Receipt,
		"target_version": strconv.FormatInt(in.TargetVersion, 10), "repo_id": in.RepoID,
	}
	if in.Reason != "" {
		m["reason"] = in.Reason
	}
	if in.AgentIdentity != "" {
		m["agent_identity"] = in.AgentIdentity
	}
	if in.Proposal.SessionRun != "" {
		m["session_run"], m["approval_id"] = in.Proposal.SessionRun, in.Proposal.Approval
	}
	switch in.Effect {
	case effectPush:
		m["ref"], m["expected_old"], m["commit"] = in.Requested.Ref, in.Requested.ExpectedOld, in.Requested.Commit
	case effectPullRequest:
		m["head_ref"], m["base"], m["commit"] = in.Requested.HeadRef, in.Requested.Base, in.Requested.Commit
	case effectMerge:
		m["number"], m["expected_head"], m["method"] = strconv.Itoa(in.Requested.Number), in.Requested.ExpectedHead, in.Requested.Method
	}
	if in.Observed.Number != 0 {
		m["observed_number"] = strconv.Itoa(in.Observed.Number)
	}
	if in.Observed.MergeCommitSHA != "" {
		m["merge_commit_sha"] = in.Observed.MergeCommitSHA
	}
	return m
}

// auditIntent records an intent event attributed to the intent's subject.
func auditIntent(ctx context.Context, sc store.Scope, in Intent, action string) error {
	return appendAudit(ctx, sc, in.SubjectActor, in.SubjectActorKind, "gitpublish."+in.Effect+"."+action, kindIntent, in.ID, intentMeta(in))
}
