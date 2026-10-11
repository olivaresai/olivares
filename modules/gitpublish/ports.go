// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package gitpublish

import (
	"context"
	"errors"
	"time"

	gp "github.com/olivaresai/olivares/connectors/gitpublish"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Caller is the authenticated request principal and its single resolved
// tenant. Nothing in it comes from a request body.
type Caller struct {
	Principal auth.Principal
	Tenant    model.TenantID
}

// Question is one authorization question over a STORED target.
type Question struct {
	Permission auth.Permission
	Action     auth.CedarAction
	MinimumAAL int
	Target     model.ID
	Workspace  model.ID
}

// Subject is who acts, as the exact credential names it.
type Subject struct {
	Actor         string
	ActorKind     string
	UserID        model.ID
	AgentIdentity string
}

// SubjectOf derives the subject from a principal.
func SubjectOf(p auth.Principal) Subject {
	return Subject{Actor: p.Actor(), ActorKind: p.ActorKind(), UserID: p.UserID, AgentIdentity: p.AgentIdentity}
}

// View runs fn read-only in the caller's tenant.
type View func(ctx context.Context, fn func(store.Scope) error) error

// Authority is the one authorization model: the existing exact-credential
// reconstruction and the composed route authorizer.
type Authority interface {
	// Admit is A1: rebuild the caller from its exact credential and authorize
	// q. It returns auth.ErrStepUpRequired, auth.ErrScopedGrantRequired,
	// auth.ErrRouteDenied or an undecided error unchanged.
	Admit(ctx context.Context, p auth.Principal, tenant model.TenantID, q Question) (Admission, error)
}

// Admission is the retained result of A1.
type Admission interface {
	Subject() Subject
	// Lock pins the admitted authority on the claim transaction (W1).
	Lock(ctx context.Context, sc store.Scope, now time.Time) error
	// Recheck is A4: the last local validation before dispatch.
	Recheck(ctx context.Context, view View, now time.Time) error
}

// ErrBindingNotApproved: the binding is not approved in this scope.
var ErrBindingNotApproved = errors.New("gitpublish: binding not approved in this scope")

// CredentialBinding is an approved host credential, selected by id in the
// caller's permitted scope. It never carries the secret.
type CredentialBinding struct {
	ID            string
	Version       int64
	Host          string // github | gitlab | git
	AllowedOwners []string
}

// RepositoryBinding is an approved, server-provisioned repository: the
// immutable host repository identity and the engine-owned managed repository
// the push runs from. LocalPath never leaves the module.
type RepositoryBinding struct {
	ID        string
	Version   int64
	RepoID    string
	Owner     string
	Name      string
	LocalPath string
}

// Custody is the composition root's custody seam. It selects approved
// bindings in the caller's permitted scope and opens the host adapter with
// the binding's own credential; no request supplies a secret reference, an
// endpoint or a path.
type Custody interface {
	CredentialBinding(ctx context.Context, tenant model.TenantID, workspace model.ID, id string) (CredentialBinding, error)
	RepositoryBinding(ctx context.Context, tenant model.TenantID, workspace model.ID, id string) (RepositoryBinding, error)
	OpenHost(ctx context.Context, tenant model.TenantID, cb CredentialBinding, rb RepositoryBinding) (gp.Host, error)
}

// Git is the closed git executor (connectors/gitpublish.Executor).
type Git interface {
	CommitTree(ctx context.Context, repo, commit string) (string, error)
	// PathsChanged reports whether any CI-configuration path differs.
	PathsChanged(ctx context.Context, repo, from, to string, paths []string) (bool, error)
	Push(ctx context.Context, r gp.PushRequest) (gp.Result, error)
	// Fetch feeds commit from a session folder into the server repository.
	Fetch(ctx context.Context, repo, source, commit string) error
}

// SessionFolders resolves the folder a session run worked in, confined to the
// target's workspace; the composition root binds the sessions module. A run
// that is absent, foreign or has no recorded folder is store.ErrNotFound.
type SessionFolders interface {
	ReadRunWorkspacePath(ctx context.Context, tenant model.TenantID, workspace model.ID, run string) (string, error)
}

// PrincipalResolver rebuilds a principal from its exact credential
// reference. *auth.Authenticator implements it.
type PrincipalResolver interface {
	ResolvePrincipalScope(ctx context.Context, ref auth.PrincipalRef, tenant model.TenantID) (auth.Principal, error)
}
