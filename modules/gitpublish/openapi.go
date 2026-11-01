// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package gitpublish

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/oas"
)

// The gitpublish module's request bodies (CONTRACT-J10-S3 §3.10). Every body
// decodes strictly: a field outside these schemas, such as a secret
// reference, an endpoint, an installation id or a path, is refused.

type gitpublishRequestBodyKind uint8

const (
	gitpublishBodyless gitpublishRequestBodyKind = iota + 1
	gitpublishBodyful
)

type gitpublishRequestBodyDeclaration struct {
	kind   gitpublishRequestBodyKind
	schema map[string]any
}

func gitpublishRequestBody(method, pattern string) (map[string]any, bool) {
	decl, ok := gitpublishRequestBodyDeclarationFor(method, pattern)
	if !ok || decl.kind != gitpublishBodyful {
		return nil, false
	}
	return oas.Obj(
		"required", true,
		"description", "The handler decodes one strict JSON document, bounded at 1 MiB; an unknown field is field_not_accepted.",
		"content", oas.Obj("application/json", oas.Obj("schema", decl.schema)),
	), true
}

func gitpublishRequestBodyDeclarationFor(method, pattern string) (gitpublishRequestBodyDeclaration, bool) {
	body := func(s map[string]any) (gitpublishRequestBodyDeclaration, bool) {
		return gitpublishRequestBodyDeclaration{kind: gitpublishBodyful, schema: s}, true
	}
	switch method + " " + pattern {
	case http.MethodPost + " /targets":
		return body(gitpublishTargetSchema(true))
	case http.MethodPut + " /targets/{id}":
		return body(gitpublishTargetSchema(false))
	case http.MethodPost + " /targets/{id}/pushes":
		return body(gitpublishPushSchema())
	case http.MethodPost + " /targets/{id}/ships":
		return body(gitpublishShipSchema())
	case http.MethodPost + " /targets/{id}/pull-requests":
		return body(gitpublishPullRequestSchema())
	case http.MethodPost + " /targets/{id}/merges":
		return body(gitpublishMergeSchema())
	case http.MethodPost + " /targets/{id}/issue-comments":
		return body(gitpublishIssueCommentSchema())
	case http.MethodPost + " /intents/{id}/abandon":
		return body(oas.Obj("type", "object", "additionalProperties", false, "properties", oas.Obj("reason", oas.Obj("type", "string", "maxLength", 1024))))
	case http.MethodDelete + " /targets/{id}", http.MethodPost + " /intents/{id}/reconcile":
		return gitpublishRequestBodyDeclaration{kind: gitpublishBodyless}, true
	}
	return gitpublishRequestBodyDeclaration{}, false
}

const (
	gitpublishSHA    = "^[0-9a-f]{40}([0-9a-f]{24})?$"
	gitpublishOp     = "^[A-Za-z0-9._:-]{1,128}$"
	gitpublishBranch = "^[A-Za-z0-9._/-]+$"
)

func gitpublishString(pattern, description string) map[string]any {
	return oas.Obj("type", "string", "pattern", pattern, "description", description)
}

func gitpublishTargetSchema(create bool) map[string]any {
	props := oas.Obj(
		"credential_binding_id", oas.Obj("type", "string", "description", "An approved credential binding in the caller's permitted scope; never a secret reference."),
		"repository_binding_id", oas.Obj("type", "string", "description", "An approved server repository binding; never a path."),
		"push_prefix", gitpublishString(gitpublishBranch, "Branch prefix ending in '/'; pushes and pull-request heads must be under it."),
		"merge_bases", oas.Obj("type", "array", "maxItems", 16, "items", gitpublishString(gitpublishBranch, "An allowed target branch.")),
	)
	required := oas.Enum("push_prefix")
	if create {
		props["workspace_id"] = oas.Obj("type", "string", "description", "The workspace the target belongs to.")
		required = oas.Enum("workspace_id", "credential_binding_id", "repository_binding_id", "push_prefix")
	} else {
		props["expected_version"] = oas.Obj("type", "integer", "description", "Optimistic concurrency: the version read.")
		required = oas.Enum("expected_version", "push_prefix")
	}
	return oas.Obj("type", "object", "additionalProperties", false, "properties", props, "required", required)
}

func gitpublishPushSchema() map[string]any {
	return oas.Obj("type", "object", "additionalProperties", false,
		"properties", oas.Obj(
			"operation_id", gitpublishString(gitpublishOp, "Client idempotency key."),
			"ref", gitpublishString("^refs/heads/[A-Za-z0-9._/-]+$", "A branch under push_prefix. A merge base, the host's default branch and any branch the host reports as protected are refused; tags are refused."),
			"expected_old", oas.Obj("type", "string", "description", "The lease: the ref's expected current value; empty requires the ref to be absent."),
			"commit", gitpublishString(gitpublishSHA, "The exact commit."),
			"tree", gitpublishString(gitpublishSHA, "The commit's tree, checked in the server repository."),
			"acknowledge_intent", oas.Obj("type", "string", "description", "Names an abandoned intent of the same scope this request knowingly proceeds past; requires target admin at AAL3."),
			"session_run", oas.Obj("type", "string", "format", "uuid", "description", "A session run in the target's workspace whose folder holds the commit; the engine fetches the commit from it into the server repository first. Never a path."),
		),
		"required", oas.Enum("operation_id", "ref", "commit", "tree"),
	)
}

func gitpublishPullRequestSchema() map[string]any {
	return oas.Obj("type", "object", "additionalProperties", false,
		"properties", oas.Obj(
			"operation_id", gitpublishString(gitpublishOp, "Client idempotency key."),
			"head_ref", gitpublishString(gitpublishBranch, "Source branch under push_prefix; the host binds branches, not SHAs."),
			"base", gitpublishString(gitpublishBranch, "An allowed merge base."),
			"commit", gitpublishString(gitpublishSHA, "The requested head commit, recorded beside the observed one."),
			"title", oas.Obj("type", "string", "maxLength", 256),
			"body", oas.Obj("type", "string", "maxLength", 65536),
			"draft", oas.Obj("type", "boolean"),
			"acknowledge_intent", oas.Obj("type", "string"),
		),
		"required", oas.Enum("operation_id", "head_ref", "base", "commit", "title"),
	)
}

func gitpublishShipSchema() map[string]any {
	return oas.Obj("type", "object", "additionalProperties", false,
		"properties", oas.Obj(
			"operation_id", gitpublishString(gitpublishOp, "Client idempotency key of the whole ship, at most 100 characters; its push and pull request replay under it."),
			"session_run", oas.Obj("type", "string", "format", "uuid", "description", "The session run in the target's workspace whose folder is shipped. Never a path."),
			"message", oas.Obj("type", "string", "maxLength", 8192, "description", "The commit message, as the caller's profile commits it; needed only when the folder has changes."),
			"title", oas.Obj("type", "string", "maxLength", 256, "description", "The pull request's title: one line."),
			"body", oas.Obj("type", "string", "maxLength", 65536),
			"base", gitpublishString(gitpublishBranch, "An allowed merge base; the target's first when absent."),
			"draft", oas.Obj("type", "boolean"),
		),
		"required", oas.Enum("operation_id", "session_run", "title"),
	)
}

func gitpublishMergeSchema() map[string]any {
	return oas.Obj("type", "object", "additionalProperties", false,
		"properties", oas.Obj(
			"operation_id", gitpublishString(gitpublishOp, "Client idempotency key."),
			"number", oas.Obj("type", "integer", "minimum", 1),
			"expected_head", gitpublishString(gitpublishSHA, "The reviewed source head; the host refuses the merge if the head differs."),
			"method", oas.Obj("type", "string", "enum", []any{"merge", "squash", "rebase"}, "description", "rebase is refused as unsupported_requirement on a GitLab target."),
			"expected_result_tree", oas.Obj("type", "string", "description", "Unsupported: refused with unsupported_requirement."),
			"expected_base", oas.Obj("type", "string", "description", "Unsupported: refused with unsupported_requirement."),
			"acknowledge_intent", oas.Obj("type", "string"),
		),
		"required", oas.Enum("operation_id", "number", "expected_head", "method"),
	)
}

func gitpublishIssueCommentSchema() map[string]any {
	return oas.Obj("type", "object", "additionalProperties", false,
		"properties", oas.Obj(
			"operation_id", gitpublishString(gitpublishOp, "Client idempotency key."),
			"number", oas.Obj("type", "integer", "minimum", 1, "description", "The issue number on the approved repository."),
			"state", oas.Obj("type", "string", "enum", []any{"completed", "failed", "stopped"}, "description", "The run result to publish."),
			"pull_request_url", oas.Obj("type", "string", "format", "uri", "maxLength", 2048, "description", "An optional HTTPS pull-request URL on the approved repository."),
			"cost_micro_usd", oas.Obj("type", "integer", "minimum", 0, "maximum", maxCommentCostMicroUSD, "description", "Optional run cost in micro-USD, as the caller reports it; absent means unknown."),
			"acknowledge_intent", oas.Obj("type", "string"),
		),
		"required", oas.Enum("operation_id", "number", "state"),
	)
}

// OperationDocumentation publishes the request body each mutation's handler decodes.
// A route this module has not classified stays unclassified.
func (*Module) OperationDocumentation(method, pattern string) (api.ModuleOperationDocumentation, bool) {
	decl, ok := gitpublishRequestBodyDeclarationFor(method, pattern)
	if !ok {
		return api.ModuleOperationDocumentation{}, false
	}
	var doc api.ModuleOperationDocumentation
	switch decl.kind {
	case gitpublishBodyful:
		doc.BodyKind = api.ModuleOperationJSONBody
		doc.RequestBody, _ = gitpublishRequestBody(method, pattern)
	case gitpublishBodyless:
		doc.BodyKind = api.ModuleOperationBodyless
	}
	return doc, true
}
