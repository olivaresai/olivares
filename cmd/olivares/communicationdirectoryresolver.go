// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

// ⛔ THE EPOCH FENCE NEEDS THREE SEPARATE TRANSACTIONS, AND THE NATURAL FORM IS DEAD.
//
// The addendum (§3.2) requires: read epoch-before, then roster and tombstones,
// then epoch-after; return only when before == after >= 1; on a change retry
// with a bound and then answer 503. That guarantee only means something if the
// two epoch reads CAN differ.
//
// The port header (core/store/directory.go) says callers take the three steps
// "without opening a nested Store transaction", which reads naturally as "do it
// all inside one View". It cannot be that: Store.View is documented as "one
// consistent read-only transaction snapshot" and is implemented as
// sql.LevelRepeatableRead on Postgres and SQLite's default stable snapshot
// (core/internal/store/sqlstore/store.go, viewTxOptions). Inside a single View
// the two reads are GUARANTEED equal, so the retry and the 503 become
// unreachable code — a guard that compiles, reviews clean, and never fires.
//
// Adjudicated 2026-08-26: the addendum governs, "without nesting" means DO NOT
// NEST rather than "use one transaction", and this resolver therefore takes the
// three reads in three separate Views so a concurrent bump is observable
// between them. The acceptance criterion is the discriminating mutant: inject an
// epoch bump between the roster read and epoch-after; a correct resolver retries
// and then answers UNKNOWN, and the single-View form does NOT fire on it.
//
// ⛔ AND A MISSING TOMBSTONE IS NOT A LIVE PRINCIPAL. Until 2026-09-05 this
// adapter answered Eligible = (tombstone == nil): a user that never existed, an
// inactive account, a non-member of the tenant and an agent bound to another
// workspace were all "eligible" because nobody had retired them. Eligibility
// now requires current existence, active status and a covering membership or
// workspace binding, read through the engine-owned projections in
// communicationdirectoryreads.go, under the same fence as the tombstone.
type communicationDirectoryResolver struct {
	view        directoryScopeRunner
	reads       *communicationDirectoryReads
	now         func() time.Time
	freshness   time.Duration
	maxAttempts int
}

// observedAt floors the process observation to SQLite's documented
// TransactionNow precision. The evidence interval becomes slightly shorter,
// never longer, while a later same-millisecond transactional observation can
// no longer appear to predate it merely because SQLite cannot represent the
// process clock's remaining nanoseconds.
func (r *communicationDirectoryResolver) observedAt() time.Time {
	return r.now().UTC().Truncate(time.Millisecond)
}

// directoryScopeRunner is Store.View bound by the composition root AND already
// narrowed to the one capability this adapter needs. The adapter never sees a
// store.Scope, let alone a store.Store: the type assertion for the optional
// DirectorySnapshotReader capability happens once, at the binding site, so a
// scope that lacks it fails there instead of inside every read.
//
// It also keeps the fence honest under test: a double implements two methods,
// not the fifteen of store.Scope.
type directoryScopeRunner func(
	ctx context.Context, tenant model.TenantID, fn func(store.DirectorySnapshotReader) error,
) error

const (
	directoryResolverDefaultFreshness = 5 * time.Minute
	directoryResolverDefaultAttempts  = 3
	directoryResolverSelectorBound    = 1024
	directoryResolverContributionCap  = 4096

	directoryPrincipalEvidenceRef = "sessions.directory_principal.v1"
)

func newCommunicationDirectoryResolver(
	view directoryScopeRunner,
	reads *communicationDirectoryReads,
	now func() time.Time,
) *communicationDirectoryResolver {
	return &communicationDirectoryResolver{
		view:        view,
		reads:       reads,
		now:         now,
		freshness:   directoryResolverDefaultFreshness,
		maxAttempts: directoryResolverDefaultAttempts,
	}
}

// newDirectoryScopeRunner narrows a Store to the DirectorySnapshotReader
// capability once, at the binding site. A Scope that lacks the capability is
// UNKNOWN for every read instead of failing inside each one.
func newDirectoryScopeRunner(st store.Store) directoryScopeRunner {
	return func(ctx context.Context, tenant model.TenantID, fn func(store.DirectorySnapshotReader) error) error {
		return st.View(ctx, tenant, func(sc store.Scope) error {
			reader, ok := sc.(store.DirectorySnapshotReader)
			if !ok {
				return fmt.Errorf("%w: store scope does not expose the directory snapshot reader",
					store.ErrDirectoryUnavailable)
			}
			return fn(reader)
		})
	}
}

// readDirectoryEpoch opens its OWN View. That is the point: a second call can
// observe a different committed state, which is what makes the fence able to
// fire at all.
func (r *communicationDirectoryResolver) readDirectoryEpoch(
	ctx context.Context, tenant model.TenantID,
) (int64, error) {
	var version int64
	err := r.view(ctx, tenant, func(reader store.DirectorySnapshotReader) error {
		epoch, err := reader.ReadDirectoryEpoch(ctx)
		if err != nil {
			return err
		}
		version = epoch.Version
		return nil
	})
	if err != nil {
		return 0, err
	}
	if version < 1 {
		return 0, fmt.Errorf("%w: directory epoch below one", store.ErrDirectoryUnavailable)
	}
	return version, nil
}

// fenced runs read between two epoch observations taken in their own
// transactions and accepts the result only when both agree. A disagreement is a
// concurrent directory mutation: the roster just read may already describe a
// membership that no longer holds, so it is discarded and retried rather than
// labelled with the later epoch — "never read the roster first and label it with
// a later epoch".
func (r *communicationDirectoryResolver) fenced(
	ctx context.Context,
	tenant model.TenantID,
	read func(ctx context.Context, epoch int64) error,
) (int64, error) {
	attempts := r.maxAttempts
	if attempts < 1 {
		attempts = 1
	}
	for attempt := 0; attempt < attempts; attempt++ {
		before, err := r.readDirectoryEpoch(ctx, tenant)
		if err != nil {
			return 0, err
		}
		if err := read(ctx, before); err != nil {
			return 0, err
		}
		after, err := r.readDirectoryEpoch(ctx, tenant)
		if err != nil {
			return 0, err
		}
		if before == after {
			return before, nil
		}
	}
	// Exhausted: the directory moved under every attempt. UNKNOWN, never a
	// snapshot and never a business denial.
	return 0, fmt.Errorf("%w: directory epoch changed under %d fenced attempts",
		store.ErrDirectoryUnavailable, attempts)
}

// recipientResolution is one recipient's complete fenced evidence: the public
// snapshot plus the private fact that proved its current standing (membership
// or agent row) and the exact Claim tuple for a session.
type recipientResolution struct {
	snapshot     sessions.RecipientSnapshot
	fact         *store.AuthorizationFactRef
	code         string
	found        bool
	sessionFence int64
	sessionRun   string
	sessionAgent string
}

// resolveRecipientWithin reads one recipient's tombstone and current standing.
// It runs INSIDE a fence: the caller supplies the epoch-before value and takes
// epoch-after itself.
func (r *communicationDirectoryResolver) resolveRecipientWithin(
	ctx context.Context,
	scope sessions.DirectoryScopeRef,
	recipient sessions.RecipientRef,
	epoch int64,
) (recipientResolution, error) {
	if r.reads == nil {
		return recipientResolution{}, fmt.Errorf("%w: directory reads are not bound", store.ErrDirectoryUnavailable)
	}
	out := recipientResolution{snapshot: sessions.RecipientSnapshot{
		Scope: scope, Recipient: recipient, RecipientEpoch: epoch, DirectoryEpoch: epoch,
	}}
	switch recipient.Kind {
	case sessions.RecipientUser, sessions.RecipientAgent:
		ref, err := directoryPrincipalRefFor(scope, recipient)
		if err != nil {
			return recipientResolution{}, err
		}
		var tombstone *store.DirectoryTombstoneWitness
		if err := r.view(ctx, scope.TenantID, func(reader store.DirectorySnapshotReader) error {
			witness, found, readErr := reader.ReadDirectoryTombstone(ctx, ref)
			if readErr != nil {
				return readErr
			}
			if found {
				copied := witness
				tombstone = &copied
			}
			return nil
		}); err != nil {
			return recipientResolution{}, err
		}
		out.snapshot.Tombstone = tombstone
		if recipient.Kind == sessions.RecipientUser {
			user, err := r.reads.user(ctx, scope.TenantID, scope.WorkspaceID, model.ID(recipient.Ref))
			if err != nil {
				return recipientResolution{}, err
			}
			out.found = user.Found
			if user.Version > 0 {
				out.snapshot.RecipientEpoch = user.Version
			}
			switch {
			case tombstone != nil:
				out.code = "principal_retired"
			case !user.Found:
				out.code = "principal_not_found"
			case !user.Active:
				out.code = "principal_inactive"
			case !user.Member:
				out.code = "principal_not_member"
			default:
				out.code = "principal_current"
				out.snapshot.Eligible = true
				fact := user.MembershipFact
				out.fact = &fact
			}
			return out, nil
		}
		agent, err := r.reads.agent(ctx, scope.TenantID, scope.WorkspaceID, model.ID(recipient.Ref))
		if err != nil {
			return recipientResolution{}, err
		}
		out.found = agent.IdentityFound
		if agent.IdentityVersion > 0 {
			out.snapshot.RecipientEpoch = agent.IdentityVersion
		}
		switch {
		case tombstone != nil:
			out.code = "principal_retired"
		case !agent.IdentityFound:
			out.code = "principal_not_found"
		case !agent.BoundInWorkspace:
			out.code = "principal_not_in_workspace"
		case !agent.Active:
			out.code = "principal_inactive"
		case !agent.LifecycleEligible:
			out.code = "principal_lifecycle_ineligible"
		default:
			out.code = "principal_current"
			out.snapshot.Eligible = true
			fact := agent.AgentFact
			out.fact = &fact
		}
		return out, nil
	case sessions.RecipientSession:
		witness, err := r.reads.session(ctx, scope.TenantID, scope.WorkspaceID, recipient.Ref)
		if err != nil {
			return recipientResolution{}, err
		}
		out.found = witness.Found
		out.sessionFence, out.sessionRun, out.sessionAgent = witness.Fence, witness.RunRef, witness.AgentRef
		if witness.Fence > 0 {
			out.snapshot.RecipientEpoch = witness.Fence
		}
		switch {
		case !witness.Found:
			out.code = "session_not_found"
		case !witness.WorkspaceEligible:
			out.code = "session_not_in_workspace"
		case !witness.Active:
			out.code = "session_claim_absent"
		default:
			out.code = "session_current"
			out.snapshot.Eligible = true
		}
		return out, nil
	default:
		return recipientResolution{}, fmt.Errorf("%w: unknown recipient kind %q",
			store.ErrDirectoryUnavailable, recipient.Kind)
	}
}

// ResolveRecipient answers whether one canonical recipient is currently
// eligible, under the same fence.
func (r *communicationDirectoryResolver) ResolveRecipient(
	ctx context.Context,
	scope sessions.DirectoryScopeRef,
	recipient sessions.RecipientRef,
) (sessions.RecipientSnapshot, error) {
	if err := scope.Validate(); err != nil {
		return sessions.RecipientSnapshot{}, err
	}
	if err := recipient.Validate(); err != nil {
		return sessions.RecipientSnapshot{}, err
	}
	var resolution recipientResolution
	_, err := r.fenced(ctx, scope.TenantID, func(ctx context.Context, epoch int64) error {
		var readErr error
		resolution, readErr = r.resolveRecipientWithin(ctx, scope, recipient, epoch)
		return readErr
	})
	if err != nil {
		return sessions.RecipientSnapshot{}, err
	}
	return resolution.snapshot, nil
}

// ResolveAudience expands every selector under ONE fence and returns the
// complete roster plus one contribution per eligible selector-recipient arc,
// each carrying the exact fact (membership, agent, group member) that proved
// the indirect relation. Subscribers cannot be expanded here: the directory
// does not own channels, so that selector is refused and the publication
// attestor expands it into direct/group selectors first.
func (r *communicationDirectoryResolver) ResolveAudience(
	ctx context.Context,
	scope sessions.DirectoryScopeRef,
	selectors []sessions.AudienceSelector,
) (sessions.DirectorySnapshot, error) {
	if err := scope.Validate(); err != nil {
		return sessions.DirectorySnapshot{}, err
	}
	if len(selectors) > directoryResolverSelectorBound {
		return sessions.DirectorySnapshot{}, fmt.Errorf("%w: %d selectors exceed the directory bound",
			store.ErrDirectoryUnavailable, len(selectors))
	}
	for _, selector := range selectors {
		if err := selector.Validate(); err != nil {
			return sessions.DirectorySnapshot{}, err
		}
		if selector.Kind == sessions.AudienceSubscribers {
			return sessions.DirectorySnapshot{}, fmt.Errorf(
				"%w: subscribers selector requires the publication attestor", store.ErrDirectoryUnavailable)
		}
	}
	var (
		roster        map[sessions.RecipientRef]sessions.RecipientSnapshot
		contributions []sessions.ResolvedAudienceContribution
	)
	epoch, err := r.fenced(ctx, scope.TenantID, func(ctx context.Context, epoch int64) error {
		roster = make(map[sessions.RecipientRef]sessions.RecipientSnapshot)
		contributions = contributions[:0]
		resolved := make(map[sessions.RecipientRef]recipientResolution)
		resolve := func(recipient sessions.RecipientRef) (recipientResolution, error) {
			if cached, ok := resolved[recipient]; ok {
				return cached, nil
			}
			resolution, err := r.resolveRecipientWithin(ctx, scope, recipient, epoch)
			if err != nil {
				return recipientResolution{}, err
			}
			resolved[recipient] = resolution
			roster[recipient] = resolution.snapshot
			return resolution, nil
		}
		add := func(contribution sessions.ResolvedAudienceContribution) error {
			if len(contributions) >= directoryResolverContributionCap {
				return fmt.Errorf("%w: audience exceeds %d contributions",
					store.ErrDirectoryUnavailable, directoryResolverContributionCap)
			}
			contributions = append(contributions, contribution)
			return nil
		}
		for index, selector := range selectors {
			ordinal := int64(index + 1)
			base := sessions.ResolvedAudienceContribution{
				SelectorOrdinal: ordinal, Selector: selector, Required: selector.Required,
				WakePolicy: selector.WakePolicy,
			}
			switch selector.Kind {
			case sessions.AudienceUser, sessions.AudienceAgent, sessions.AudienceSession:
				recipient := sessions.RecipientRef{Kind: directRecipientKind(selector.Kind), Ref: selector.Ref}
				resolution, err := resolve(recipient)
				if err != nil {
					return err
				}
				if !resolution.snapshot.Eligible {
					continue
				}
				contribution := base
				contribution.Recipient = resolution.snapshot
				contribution.RouteReasons = []sessions.RouteReason{"direct"}
				contribution.CausalKind = sessions.CausalDirect
				contribution.CausalRef = selector.Ref
				if recipient.Kind == sessions.RecipientSession {
					contribution.ObservedSessionSID = recipient.Ref
					contribution.ObservedClaimFence = resolution.sessionFence
				}
				if err := add(contribution); err != nil {
					return err
				}
			case sessions.AudienceUserGroup:
				group, err := r.reads.userGroup(ctx, scope.TenantID, model.ID(selector.Ref))
				if err != nil {
					return err
				}
				if !group.Found {
					continue
				}
				for _, member := range group.Members {
					resolution, err := resolve(sessions.RecipientRef{Kind: sessions.RecipientUser, Ref: member.Ref.String()})
					if err != nil {
						return err
					}
					if !resolution.snapshot.Eligible {
						continue
					}
					contribution := base
					contribution.Recipient = resolution.snapshot
					contribution.RouteReasons = []sessions.RouteReason{"user_group"}
					contribution.CausalKind = sessions.CausalUserGroup
					contribution.CausalRef = selector.Ref
					fact := member.Fact
					contribution.CausalFact = &fact
					if err := add(contribution); err != nil {
						return err
					}
				}
			case sessions.AudienceAgentGroup:
				group, err := r.reads.agentGroup(ctx, scope.TenantID, scope.WorkspaceID, model.ID(selector.Ref))
				if err != nil {
					return err
				}
				if !group.Found || !group.InWorkspace || !group.Active {
					continue
				}
				seen := make(map[model.ID]struct{}, len(group.Members))
				for _, member := range group.Members {
					if _, done := seen[member.Ref]; done {
						continue
					}
					seen[member.Ref] = struct{}{}
					resolution, err := resolve(sessions.RecipientRef{Kind: sessions.RecipientAgent, Ref: member.Ref.String()})
					if err != nil {
						return err
					}
					if !resolution.snapshot.Eligible {
						continue
					}
					contribution := base
					contribution.Recipient = resolution.snapshot
					contribution.RouteReasons = []sessions.RouteReason{"agent_group"}
					contribution.CausalKind = sessions.CausalAgentGroup
					contribution.CausalRef = selector.Ref
					fact := member.Fact
					contribution.CausalFact = &fact
					if err := add(contribution); err != nil {
						return err
					}
				}
			case sessions.AudienceWorkspaceMembers:
				members, err := r.reads.workspaceMembers(ctx, scope.TenantID, scope.WorkspaceID)
				if err != nil {
					return err
				}
				for _, member := range members.Users {
					resolution, err := resolve(sessions.RecipientRef{Kind: sessions.RecipientUser, Ref: member.Ref.String()})
					if err != nil {
						return err
					}
					if !resolution.snapshot.Eligible {
						continue
					}
					contribution := base
					contribution.Recipient = resolution.snapshot
					contribution.RouteReasons = []sessions.RouteReason{"workspace_members"}
					contribution.CausalKind = sessions.CausalWorkspaceMember
					contribution.CausalRef = scope.WorkspaceID.String()
					fact := member.Fact
					contribution.CausalFact = &fact
					if err := add(contribution); err != nil {
						return err
					}
				}
				for _, member := range members.Agents {
					resolution, err := resolve(sessions.RecipientRef{Kind: sessions.RecipientAgent, Ref: member.Ref.String()})
					if err != nil {
						return err
					}
					if !resolution.snapshot.Eligible {
						continue
					}
					contribution := base
					contribution.Recipient = resolution.snapshot
					contribution.RouteReasons = []sessions.RouteReason{"workspace_members"}
					contribution.CausalKind = sessions.CausalWorkspaceMember
					contribution.CausalRef = scope.WorkspaceID.String()
					fact := member.Fact
					contribution.CausalFact = &fact
					if err := add(contribution); err != nil {
						return err
					}
				}
			default:
				return fmt.Errorf("%w: unsupported selector kind %q", store.ErrDirectoryUnavailable, selector.Kind)
			}
		}
		return nil
	})
	if err != nil {
		return sessions.DirectorySnapshot{}, err
	}
	recipients := make([]sessions.RecipientSnapshot, 0, len(roster))
	for _, snapshot := range roster {
		recipients = append(recipients, snapshot)
	}
	sort.Slice(recipients, func(i, j int) bool {
		if recipients[i].Recipient.Kind != recipients[j].Recipient.Kind {
			return recipients[i].Recipient.Kind < recipients[j].Recipient.Kind
		}
		return recipients[i].Recipient.Ref < recipients[j].Recipient.Ref
	})
	rosterHash, err := sessions.CanonicalDirectoryRosterHash(scope, epoch, recipients)
	if err != nil {
		return sessions.DirectorySnapshot{}, err
	}
	observedAt := r.observedAt()
	return sessions.DirectorySnapshot{
		Scope: scope, Epoch: epoch,
		Selectors:     append([]sessions.AudienceSelector(nil), selectors...),
		Recipients:    recipients,
		Contributions: append([]sessions.ResolvedAudienceContribution(nil), contributions...),
		RosterHash:    rosterHash,
		ObservedAt:    observedAt, FreshUntil: observedAt.Add(r.freshness),
	}, nil
}

func directRecipientKind(kind sessions.AudienceSelectorKind) sessions.RecipientKind {
	switch kind {
	case sessions.AudienceUser:
		return sessions.RecipientUser
	case sessions.AudienceAgent:
		return sessions.RecipientAgent
	default:
		return sessions.RecipientSession
	}
}

// ResolvePrincipal maps an authenticated K3 principal to its canonical, current
// recipient. It is tri-state: resolved with an eligible recipient, not_found
// (the principal names nothing current: missing, inactive, non-member, retired,
// or a session whose exact Claim tuple no longer holds), or unknown when the
// directory could not be observed.
func (r *communicationDirectoryResolver) ResolvePrincipal(
	ctx context.Context,
	scope sessions.DirectoryScopeRef,
	principal sessions.CommunicationPrincipal,
) (sessions.PrincipalResolution, error) {
	if err := sessions.ValidateCommunicationPrincipalForScope(principal, scope); err != nil {
		return sessions.PrincipalResolution{}, err
	}
	observedAt := r.observedAt()
	result := sessions.PrincipalResolution{
		Outcome: sessions.PrincipalUnknown, Code: "principal_unresolved", Scope: scope,
		Principal: principal, ObservedAt: observedAt, FreshUntil: observedAt.Add(r.freshness),
	}
	if principal.System {
		// A system actor is not a directory principal; the validator requires a
		// canonical User/session or an external AgentIdentity, and a system grant
		// agent id alone is neither. Deny-closed until a caller supplies one.
		result.Code = "system_principal_unsupported"
		return result, nil
	}
	var (
		resolution recipientResolution
		recipient  sessions.RecipientRef
		notFound   string
	)
	_, err := r.fenced(ctx, scope.TenantID, func(ctx context.Context, epoch int64) error {
		notFound = ""
		switch {
		case principal.SessionID != "":
			recipient = sessions.RecipientRef{Kind: sessions.RecipientSession, Ref: principal.SessionID}
			var readErr error
			resolution, readErr = r.resolveRecipientWithin(ctx, scope, recipient, epoch)
			if readErr != nil {
				return readErr
			}
			// The exact tuple is SID, Claim fence, operated run and (when the
			// bearer names one) the run's agent. A session the plane never launched
			// has no operated run, and a bearer always names one (the validator
			// requires it), so an absent run is a mismatch, never a pass.
			switch {
			case !resolution.snapshot.Eligible:
				notFound = resolution.code
			case resolution.sessionFence != principal.SessionFence:
				notFound = "session_claim_stale"
			case resolution.sessionRun != principal.SessionRunRef:
				notFound = "session_run_mismatch"
			case principal.AgentExternalID != "" && resolution.sessionAgent != principal.AgentExternalID:
				notFound = "session_agent_mismatch"
			}
			return nil
		case principal.AgentExternalID != "":
			identityID, found, readErr := r.reads.agentByExternalID(ctx, scope.TenantID, principal.AgentExternalID)
			if readErr != nil {
				return readErr
			}
			if !found {
				notFound = "principal_not_found"
				return nil
			}
			recipient = sessions.RecipientRef{Kind: sessions.RecipientAgent, Ref: identityID.String()}
		default:
			recipient = sessions.RecipientRef{Kind: sessions.RecipientUser, Ref: principal.UserID.String()}
		}
		var readErr error
		resolution, readErr = r.resolveRecipientWithin(ctx, scope, recipient, epoch)
		if readErr != nil {
			return readErr
		}
		if !resolution.snapshot.Eligible {
			notFound = resolution.code
		}
		return nil
	})
	if err != nil {
		return sessions.PrincipalResolution{}, err
	}
	if notFound != "" {
		result.Outcome, result.Code = sessions.PrincipalNotFound, notFound
		return result, nil
	}
	snapshot := resolution.snapshot
	result.Outcome, result.Code, result.Recipient = sessions.PrincipalResolved, "principal_resolved", &snapshot
	return result, nil
}

// directoryPrincipalRefFor maps a K3 recipient onto the core tombstone lookup
// key. A session recipient has no core tombstone: its liveness is the Claim's
// job, not the directory's.
func directoryPrincipalRefFor(
	scope sessions.DirectoryScopeRef, recipient sessions.RecipientRef,
) (store.DirectoryPrincipalRef, error) {
	switch recipient.Kind {
	case sessions.RecipientUser:
		return store.DirectoryPrincipalRef{
			PrincipalKind: model.DirectoryPrincipalUser,
			PrincipalRef:  model.ID(recipient.Ref),
		}, nil
	case sessions.RecipientAgent:
		return store.DirectoryPrincipalRef{
			PrincipalKind: model.DirectoryPrincipalAgent,
			PrincipalRef:  model.ID(recipient.Ref),
			WorkspaceRef:  scope.WorkspaceID,
		}, nil
	default:
		return store.DirectoryPrincipalRef{}, fmt.Errorf(
			"%w: recipient kind %q has no core directory tombstone",
			store.ErrDirectoryUnavailable, recipient.Kind)
	}
}

var _ sessions.DirectorySnapshotResolver = (*communicationDirectoryResolver)(nil)
