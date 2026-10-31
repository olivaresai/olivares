// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package gitpublish is the Community publication module (J10-S3): authorized
// push, pull request and merge on a Git host, bound to the exact caller and
// its current authority at each effect.
//
// Every effect is one intent. The module rebuilds the caller's authority from
// its exact credential and admits the effect question over the STORED target
// (A1). It replays or refuses by operation id and conflict scope (A2). It
// mints a narrowed host capability and runs its preflight (A3). It burns the
// intent before any repository mutation, with the admitted authority locked
// on that transaction (W1). Immediately before dispatch it validates local
// authority and the pinned target, credential binding and repository binding
// once more (A4). A4 is the last LOCAL check: a remote operation already sent
// can race a later local revocation, and the Git service offers no atomic
// revocation.
//
// An intent keeps three separate records: what was requested, what later
// host reads observed, and whether the host acknowledged OUR request. An
// observation is never a completion barrier: a timeout, a deadline, an old
// ref, an empty lookup, a 404 or an open unmerged pull request cannot prove
// that an earlier request will not complete later, so an uncertain intent is
// never re-armed. Only a proven no-dispatch or a documented definitive
// non-application permits a new attempt, under fresh authority.
package gitpublish

import (
	"context"
	"errors"
	"time"

	gp "github.com/olivaresai/olivares/connectors/gitpublish"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// Namespace is the module's API and store namespace.
const Namespace = "gitpublish"

// Post-dispatch work can settle, then observe an uncertain outcome, then release
// the capability. Keep the HTTP budget tied to these same finite phase limits.
const (
	settleTimeout  = 30 * time.Second
	releaseTimeout = 15 * time.Second
	replyTimeout   = 15 * time.Second
)

// The module permissions, granted by verb tier.
const (
	permTargetRead   auth.Permission = "gitpublish:target:read"
	permTargetAdmin  auth.Permission = "gitpublish:target:admin"
	permPush         auth.Permission = "gitpublish:push:write"
	permPullRequest  auth.Permission = "gitpublish:pull_request:write"
	permMerge        auth.Permission = "gitpublish:merge:admin"
	permIssueComment auth.Permission = "gitpublish:issue_comment:write"
)

// The Cedar actions the module's governed routes name.
const (
	actionPush         auth.CedarAction = "publication:push"
	actionPullRequest  auth.CedarAction = "publication:open_pull_request"
	actionMerge        auth.CedarAction = "publication:merge"
	actionIssueComment auth.CedarAction = "publication:issue_comment"
	actionTarget       auth.CedarAction = "publication_target:write"
	actionReconcile    auth.CedarAction = "publication_intent:reconcile"
	actionAbandon      auth.CedarAction = "publication_intent:abandon"
)

// Options composes the module. The composition root supplies the production
// ports; tests supply fakes.
type Options struct {
	Custody   Custody
	Git       Git
	Authority Authority
	// Sessions resolves a push's session run to its folder. Nil refuses a
	// push that names a run; a push that names none never reads it.
	Sessions SessionFolders
	// Work commits a session run's folder for a ship, and Gate asks the tenant's
	// approval policy before its push. Nil refuses a ship: authority_unavailable.
	Work SessionWork
	Gate ShipGate
	Now  func() time.Time
	// AdmissionTimeout bounds A1 through A4 (default 30s).
	AdmissionTimeout time.Duration
	// DispatchTimeout bounds the single host write (default 2m).
	DispatchTimeout time.Duration
	// Skew is added to dispatch_deadline before the sweep treats a
	// dispatching intent as uncertain (default 2m).
	Skew time.Duration
	// SweepObserveInterval bounds how often the sweep re-observes one
	// uncertain intent (default 5m).
	SweepObserveInterval time.Duration
	// OnReleaseFailure receives a bounded release failure that has no intent
	// to be recorded on (a refusal before W1). It never receives the token.
	OnReleaseFailure func(target model.ID, code string)
	// OnLookupFailure receives why a host lookup failed when a request is refused
	// with lookup_incomplete: an error kind, git's exit code and its first
	// line. It never receives the token or the key, and the answer stays the code.
	OnLookupFailure func(target model.ID, cause string)
}

// lookupFailed hands a lookup_incomplete refusal's cause to OnLookupFailure.
func (m *Module) lookupFailed(target model.ID, e *Error) {
	if e != nil && e.cause != nil && m.opts.OnLookupFailure != nil {
		cause := e.cause.Error()
		var host *gp.HostError
		if errors.As(e.cause, &host) {
			// Request IDs come from the remote and can echo credentials. The
			// closed status/code still names the failure without that value.
			bounded := *host
			bounded.RequestID = ""
			cause = bounded.Error()
		}
		m.opts.OnLookupFailure(target, cause)
	}
}

// Module is the gitpublish module.
type Module struct {
	opts Options
	data api.ModuleData
	// beforeClaim is a test seam run just before W1.
	beforeClaim func()
}

// New returns the module.
func New(o Options) *Module {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.AdmissionTimeout <= 0 {
		o.AdmissionTimeout = 30 * time.Second
	}
	if o.DispatchTimeout <= 0 {
		o.DispatchTimeout = 2 * time.Minute
	}
	if o.Skew <= 0 {
		o.Skew = 2 * time.Minute
	}
	if o.SweepObserveInterval <= 0 {
		o.SweepObserveInterval = 5 * time.Minute
	}
	return &Module{opts: o}
}

// APINamespace implements api.Module.
func (m *Module) APINamespace() string { return Namespace }

// Permissions implements api.Module.
func (m *Module) Permissions() []auth.Permission {
	return []auth.Permission{permTargetRead, permTargetAdmin, permPush, permPullRequest, permMerge, permIssueComment}
}

// Actions implements api.ActionDeclarer.
func (m *Module) Actions() []auth.CedarAction {
	return []auth.CedarAction{actionPush, actionPullRequest, actionMerge, actionIssueComment, actionTarget, actionReconcile, actionAbandon}
}

// UseData implements api.DataConsumer.
func (m *Module) UseData(d api.ModuleData) { m.data = d }

// UseAuthority late-binds the production authority: the serving
// Authenticator (exact credential reconstruction) and the composed
// Authorizer. Nil is deny-closed: every effect answers authority_unavailable.
func (m *Module) UseAuthority(resolver PrincipalResolver, authorizer *auth.Authorizer) {
	if resolver == nil || authorizer == nil {
		m.opts.Authority = nil
		return
	}
	m.opts.Authority = exactAuthority{resolver: resolver, authorizer: authorizer}
}

// UseCustody late-binds the approved credential and repository bindings.
func (m *Module) UseCustody(c Custody) { m.opts.Custody = c }

// UseGit late-binds the closed git executor.
func (m *Module) UseGit(g Git) { m.opts.Git = g }

// UseSessions late-binds the session folder reader.
func (m *Module) UseSessions(s SessionFolders) { m.opts.Sessions = s }

// UseSessionWork late-binds the sessions module's commit of a run's folder.
func (m *Module) UseSessionWork(w SessionWork) { m.opts.Work = w }

// UseShipGate late-binds the approval policy's door before a ship's push.
func (m *Module) UseShipGate(g ShipGate) { m.opts.Gate = g }

var _ Git = (*gp.Executor)(nil)

func (m *Module) wired() bool {
	return m.data != nil && m.opts.Authority != nil && m.opts.Custody != nil && m.opts.Git != nil
}

func (m *Module) now() time.Time { return m.opts.Now().UTC() }

// admissionContext is the earlier of the caller's deadline and now + T.
func (m *Module) admissionContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, m.opts.AdmissionTimeout)
}
