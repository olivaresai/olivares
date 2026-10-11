// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package gitpublish

import (
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The module's store entities. The engine creates their tables from these
// descriptors; there is no core version.
const (
	kindTarget      model.Kind = "gitpublish.target"
	kindIntent      model.Kind = "gitpublish.intent"
	kindObservation model.Kind = "gitpublish.observation"
	// kindScope is one row per conflict scope. W1 takes it (create or
	// optimistic update) in the claim transaction, so two claims in one scope
	// serialize on that row on every engine.
	kindScope model.Kind = "gitpublish.scope"
)

// The None reasons of the text columns below: what each stored value is, with
// the validator, writer or reader lines that show no reader resolves it to an
// account. The subject, author and abandoning-actor columns are the only ones
// that hold a principal, and they are declared Ref.
const (
	// Shared by the target, intent, scope and observation rows.
	reasonTargetWorkspace    = "the workspace a target belongs to, required and used only as its authorization lineage and the admission question's workspace: targets.go:114, targets.go:78, publish.go:219"
	reasonIntentWorkspace    = "the stored target's workspace copied onto the intent, used only as its authorization lineage: publish.go:316, publish.go:219"
	reasonScopeWorkspace     = "the claiming intent's workspace, used only as the scope row's authorization lineage: publish.go:574, publish.go:634"
	reasonObservedWorkspace  = "the observed intent's workspace, used only as the observation's authorization lineage: records.go:317"
	reasonTargetID           = "a publication target row id, used only as a lookup filter: records.go:176, records.go:280"
	reasonScopeTargetID      = "a publication target row id, used only as the scope row's lookup filter: publish.go:568, publish.go:574"
	reasonTargetCredential   = "a custody credential binding id, resolved only through the custody port to an approved binding: targets.go:54, targets.go:114"
	reasonTargetRepository   = "a custody repository binding id, resolved only through the custody port to an approved binding: targets.go:61, targets.go:114"
	reasonIntentCredential   = "the credential binding id pinned at the claim, compared only with the binding current at dispatch: publish.go:317, publish.go:707"
	reasonIntentRepository   = "the repository binding id pinned at the claim, compared only with the binding current at dispatch: publish.go:317, publish.go:708"
	reasonRepoID             = "the host repository identity from the repository binding, compared only with the current binding: publish.go:317, publish.go:708, reconcile.go:165"
	reasonPushPrefix         = "a branch-name prefix ending in a slash, validated and compared only with branch names: targets.go:47-48, targets.go:98, publish.go:429"
	reasonMergeBases         = "comma-joined branch names, each validated and compared only with branch names: targets.go:101-102, publish.go:429, publish.go:436"
	reasonEffect             = "an effect from a closed set: records.go:37-39, records.go:269"
	reasonOperationID        = "a client idempotency key of at most 128 characters from a fixed alphabet, used only as a lookup filter: publish.go:67, publish.go:203, records.go:269"
	reasonScopeKey           = "a conflict scope built from the effect and its ref, branches or pull request number: publish.go:99, publish.go:113, publish.go:132"
	reasonScopeRowKey        = "the conflict scope of the claiming intent, used only as the scope row's lookup filter: publish.go:568, publish.go:634"
	reasonScopeHolder        = "the operation id of the intent that last claimed the scope, never read back as a principal: publish.go:607, publish.go:634, publish.go:67"
	reasonDigest             = "a hex SHA-256 of the framed request, compared only for equality: publish.go:137-148, publish.go:265"
	reasonSubjectActorKind   = "the credential's actor kind, user, token or system, never an actor: ports.go:44, core/auth/principal.go:243-251"
	reasonState              = "an intent state from a closed set: records.go:18-24"
	reasonReason             = "a bounded refusal, result or evidence code, or an administrator's printable reason cut to 256 bytes and rendered only: publish.go:728, publish.go:796, sweep.go:82, reconcile.go:38, reconcile.go:298-310"
	reasonReceipt            = "a receipt kind from a closed set: records.go:30-32"
	reasonRef                = "a refs/heads/ branch name, validated as a branch: publish.go:91, targets.go:42-44"
	reasonHeadRef            = "a pull request source branch name, validated as a branch: publish.go:105, targets.go:42-44"
	reasonBase               = "a pull request base branch name, validated as a branch: publish.go:108, targets.go:42-44"
	reasonRequestedCommit    = "a commit or tree id of 40 or 64 lowercase hex digits: publish.go:68, publish.go:94, publish.go:108"
	reasonExpectedOld        = "the lease's expected commit id, empty or 40 or 64 lowercase hex digits: publish.go:94, connectors/gitpublish/git.go:304"
	reasonExpectedHead       = "the reviewed head commit id of 40 or 64 lowercase hex digits: publish.go:127"
	reasonMethod             = "a merge method from a closed set: publish.go:122-126"
	reasonTitle              = "caller prose of at most 256 bytes, sent to the host as the pull request title and rendered only: publish.go:108, publish.go:755"
	reasonObservedCommit     = "a commit id read from the host, compared only with the requested commit: publish.go:868, publish.go:793"
	reasonObservedMerge      = "the merge commit id or its tree id read from the host after a merge: publish.go:882, publish.go:896"
	reasonObservedSource     = "the module's label for where an observation came from, from a fixed set: publish.go:483, publish.go:868, reconcile.go:31, sweep.go:106"
	reasonInstant            = "an RFC 3339 UTC instant written and parsed as a time: records.go:129-139"
	reasonHostRequestID      = "the host's request id, cut to 128 characters of a fixed alphabet and kept for correlation: connectors/gitpublish/result.go:119-132, publish.go:780"
	reasonAcknowledgeIntent  = "an intent row id, compared only with the ids of abandoned intents in the scope: records.go:290, publish.go:224"
	reasonReleaseFailure     = "a bounded host error code from a failed capability release: publish.go:183-184, errors.go:61-67"
	reasonObservationIntent  = "the observed intent's row id, used only as a lookup filter: records.go:317, reconcile.go:86"
	reasonObservationSource  = "the module's label for where an observation came from, from a fixed set: publish.go:732, publish.go:839, reconcile.go:66, sweep.go:83"
	reasonObservationResult  = "an intent state or a fixed result label: publish.go:732, reconcile.go:197, sweep.go:83"
	reasonObservationObject  = "a refusal or evidence code, or the observed commit ids: publish.go:732, publish.go:839, reconcile.go:197"
	reasonObservationRequest = "the host's request id, cut to 128 characters of a fixed alphabet: connectors/gitpublish/result.go:119-132, publish.go:839"
	reasonObservationAt      = "an RFC 3339 UTC instant written as a time: records.go:319, records.go:129-134"
	reasonProposalRun        = "the session run whose approved proposal the intent carries out, a validated run id, only checked for presence, rendered and audited: publish.go:62-68, publish.go:585-600, audit.go:43"
	reasonProposalApproval   = "the governance approval id the proposal spent, matched against the operation alphabet and only rendered and audited: publish.go:62-68, audit.go:43, routes.go:202"
)

func none(reason string) *model.ColumnDecl { return model.None(reason) }

func text(name, reason string) model.FieldSpec {
	return model.FieldSpec{Name: name, Kind: model.KindText, Principal: none(reason)}
}

func textIdx(name, reason string) model.FieldSpec {
	f := text(name, reason)
	f.Indexed = true
	return f
}

func nullText(name, reason string) model.FieldSpec {
	f := text(name, reason)
	f.Nullable = true
	return f
}

func intField(name string) model.FieldSpec  { return model.FieldSpec{Name: name, Kind: model.KindInt} }
func boolField(name string) model.FieldSpec { return model.FieldSpec{Name: name, Kind: model.KindBool} }

func lineage() model.WorkspaceLineageSpec {
	return model.WorkspaceLineageSpec{Column: "workspace_id", Encoding: model.WorkspaceLineageID, Unset: model.WorkspaceUnsetHidden}
}

// RegisterSchema declares the target, intent and observation entities with a
// C18 classification on every text column.
func (m *Module) RegisterSchema(reg store.ExtensionRegistry) error {
	if err := reg.Register(model.EntityDescriptor{
		Kind:  kindTarget,
		Table: "gitpublish_target",
		Fields: []model.FieldSpec{
			textIdx("workspace_id", reasonTargetWorkspace),
			text("credential_binding", reasonTargetCredential),
			text("repository_binding", reasonTargetRepository),
			text("push_prefix", reasonPushPrefix),
			text("merge_bases", reasonMergeBases),
			{Name: "created_by", Kind: model.KindText, Principal: model.Ref(model.EncodeUserRef, model.ClassEvidence)},
		},
		Indexes: []model.IndexSpec{{
			Name: "gitpublish_target_binding_uniq", Columns: []string{model.ColTenantID, "credential_binding", "repository_binding"}, Unique: true,
		}},
		WorkspaceLineage: lineage(),
	}); err != nil {
		return err
	}
	intent := []model.FieldSpec{
		textIdx("target_id", reasonTargetID),
		textIdx("workspace_id", reasonIntentWorkspace),
		intField("target_version"),
		text("credential_binding", reasonIntentCredential),
		intField("credential_binding_version"),
		text("repository_binding", reasonIntentRepository),
		intField("repository_binding_version"),
		text("repo_id", reasonRepoID),
		text("effect", reasonEffect),
		text("operation_id", reasonOperationID),
		textIdx("scope_key", reasonScopeKey),
		text("request_digest", reasonDigest),
		{Name: "subject_actor", Kind: model.KindText, Principal: model.Ref(model.EncodeUserRef, model.ClassEvidence)},
		text("subject_actor_kind", reasonSubjectActorKind),
		{Name: "subject_user", Kind: model.KindText, Nullable: true, Principal: model.Ref(model.EncodeUserID, model.ClassEvidence)},
		{Name: "agent_identity", Kind: model.KindText, Nullable: true, Principal: model.Ref(model.EncodeExternalID, model.ClassEvidence)},
		{Name: "authorized_by", Kind: model.KindText, Nullable: true, Principal: model.Ref(model.EncodeUserRef, model.ClassEvidence)},
		intField("attempt"),
		textIdx("state", reasonState),
		text("reason", reasonReason),
		text("receipt", reasonReceipt),
		text("req_ref", reasonRef), text("req_expected_old", reasonExpectedOld), text("req_commit", reasonRequestedCommit), text("req_tree", reasonRequestedCommit),
		text("req_head_ref", reasonHeadRef), text("req_base", reasonBase), intField("req_number"),
		text("req_expected_head", reasonExpectedHead), text("req_method", reasonMethod), text("req_title", reasonTitle),
		boolField("obs_present"), text("obs_sha", reasonObservedCommit), text("obs_head_sha", reasonObservedCommit), intField("obs_number"),
		boolField("obs_merged"), text("obs_merge_commit_sha", reasonObservedMerge), text("obs_merge_tree", reasonObservedMerge), text("obs_source", reasonObservedSource), text("obs_at", reasonInstant),
		boolField("content_match"),
		boolField("ack"), intField("ack_status"), text("ack_request_id", reasonHostRequestID), text("ack_at", reasonInstant),
		nullText("acknowledge_intent", reasonAcknowledgeIntent),
		text("claimed_at", reasonInstant), textIdx("dispatch_deadline", reasonInstant),
		text("release_failure", reasonReleaseFailure),
		// Appended last and nullable: the additive reconcile adds them to an
		// existing table.
		nullText("proposal_session_run", reasonProposalRun),
		nullText("proposal_approval", reasonProposalApproval),
	}
	if err := reg.Register(model.EntityDescriptor{
		Kind:   kindIntent,
		Table:  "gitpublish_intent",
		Fields: intent,
		Indexes: []model.IndexSpec{{
			Name: "gitpublish_intent_op_uniq", Columns: []string{model.ColTenantID, "target_id", "effect", "operation_id"}, Unique: true,
		}},
		WorkspaceLineage: lineage(),
	}); err != nil {
		return err
	}
	if err := reg.Register(model.EntityDescriptor{
		Kind:  kindScope,
		Table: "gitpublish_scope",
		Fields: []model.FieldSpec{
			textIdx("target_id", reasonScopeTargetID),
			textIdx("workspace_id", reasonScopeWorkspace),
			text("scope_key", reasonScopeRowKey),
			text("holder", reasonScopeHolder),
		},
		Indexes: []model.IndexSpec{{
			Name: "gitpublish_scope_uniq", Columns: []string{model.ColTenantID, "target_id", "scope_key"}, Unique: true,
		}},
		WorkspaceLineage: lineage(),
	}); err != nil {
		return err
	}
	return reg.Register(model.EntityDescriptor{
		Kind:  kindObservation,
		Table: "gitpublish_observation",
		Fields: []model.FieldSpec{
			textIdx("intent_id", reasonObservationIntent),
			textIdx("workspace_id", reasonObservedWorkspace),
			intField("attempt"),
			text("source", reasonObservationSource),
			text("result", reasonObservationResult),
			text("host_object", reasonObservationObject),
			intField("status"),
			text("request_id", reasonObservationRequest),
			text("at", reasonObservationAt),
			{Name: "actor", Kind: model.KindText, Nullable: true, Principal: model.Ref(model.EncodeUserRef, model.ClassEvidence)},
		},
		AppendOnly:       true,
		WorkspaceLineage: lineage(),
	})
}
