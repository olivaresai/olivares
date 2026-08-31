// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/olivaresai/olivares/core/model"
)

// ConfineWorkspace returns a Scope that can only ever see and touch rows
// belonging to one workspace of the bound tenant (FASE X / B-03).
//
// It exists because the confinement guarantee had no SEAT. The scoped-authz
// engine forbids a confined principal's cross-workspace and indeterminate
// WRITES, but deliberately leaves an indeterminate READ to abstain, "row-
// filtered to its workspace at the handler" (modules/governance/grants.go) — and
// that handler-side filter only ever existed in a handful of core REST handlers
// that remembered to ask for it. Every module route received a raw tenant-wide
// Scope, so a confined operator read the whole tenant through /v1/m/... The fix
// is not to teach each handler to filter: it is that the handle a request-scoped
// caller receives cannot be un-filtered.
//
// The policy is deliberately strict, because a confined membership means the
// principal "may act ONLY within that workspace" (core/auth.Principal):
//
//   - an entity that DECLARES workspace lineage (model.WorkspaceLineageSpec) is
//     row-filtered in SQL on every List, and checked on Get and on every write;
//   - an entity that declares none is REFUSED with ErrWorkspaceLineageRequired,
//     not served tenant-wide and not served as an empty page. An empty page would
//     assert "this collection was inspected in full and holds nothing of yours",
//     which is exactly the claim the engine cannot make.
//
// It is a decorator, not an embedding, on purpose: embedding Scope would
// silently promote any method added to the interface later, re-opening the class
// this closes. Adding a method to Scope must break this file's compilation until
// somebody decides what a confined caller may do with it.
//
// The returned Scope is valid only for the lifetime of raw (i.e. inside the
// View/Mutate callback it came from), exactly like the Scope it wraps.
func ConfineWorkspace(ctx context.Context, raw Scope, workspaceID model.ID) (Scope, error) {
	if raw == nil {
		return nil, errors.New("store: confine workspace on a nil scope")
	}
	if workspaceID.IsZero() {
		// A zero id is never a confinement: Principal.ConfinedWorkspaceIn reports
		// ok=false for it. Reaching here with zero means a caller inverted the
		// test, and confining to "no workspace" would either show everything or
		// nothing depending on the entity. Refuse instead of guessing.
		return nil, fmt.Errorf("%w: confinement workspace id is zero", ErrWorkspaceConfinement)
	}
	// ModuleData already applies the request boundary before invoking a module.
	// A module may then pass that same scope through a narrower service seam.
	// Treating the same boundary as idempotent prevents a second confinement from
	// trying to read the tenant's default workspace through an already-confined
	// non-default scope. A different boundary is never a retargeting license.
	if existing, ok := raw.(workspaceConfinement); ok {
		if got := existing.confinedWorkspaceID(); got != workspaceID {
			return nil, fmt.Errorf(
				"%w: scope is confined to workspace %s, not %s",
				ErrWorkspaceConfinement, got, workspaceID,
			)
		}
		return raw, nil
	}
	ws, err := raw.Workspaces().Get(ctx, workspaceID)
	if err != nil {
		// A membership pointing at a workspace that no longer exists is not a
		// tenant-wide license: fail closed.
		return nil, fmt.Errorf("confinement workspace %s: %w", workspaceID, err)
	}
	def, err := raw.DefaultWorkspace(ctx)
	if err != nil {
		// Without the default workspace we cannot decide what an UNSET lineage
		// value means, and the two possible answers differ by whether pre-FASE-X
		// rows are visible. Never degrade to one of them silently.
		return nil, fmt.Errorf("confinement default workspace: %w", err)
	}
	confined := &workspaceConfinedScope{
		raw: raw,
		b: workspaceBoundary{
			id:        workspaceID,
			slug:      ws.Slug,
			defaultID: def.ID,
		},
	}
	// Preserve each optional transaction capability exactly. A raw Scope without
	// one must remain non-assertable, while a SQL Scope must not lose one merely
	// because the caller is confined. Every adapter embeds the already-confined
	// decorator, never the raw Scope, so it cannot promote an unreviewed
	// tenant-wide accessor.
	clock, hasClock := raw.(TransactionClock)
	locker, hasLocker := raw.(TransactionLocker)
	authority, hasAuthority := raw.(AuthoritySnapshotLocker)
	directory, hasDirectory := raw.(DirectorySnapshotReader)
	// The authorization generation is one complete read/bump capability. A
	// partial method set cannot establish a usable generation, so confinement
	// preserves both ports together or exposes neither.
	authorization, hasAuthorization := raw.(AuthorizationEpochStore)
	var authorizationPorts *workspaceConfinedAuthorizationEpochPorts
	if hasAuthorization {
		authorizationPorts = &workspaceConfinedAuthorizationEpochPorts{
			store: authorization, tenant: raw.Tenant(),
		}
	}
	switch {
	case hasClock && hasLocker && hasAuthority:
		base := &workspaceConfinedClockLockerAuthorityScope{
			workspaceConfinedClockLockerScope: &workspaceConfinedClockLockerScope{
				workspaceConfinedClockScope: &workspaceConfinedClockScope{
					workspaceConfinedScope: confined,
					clock:                  clock,
				},
				locker: locker,
			},
			authority: authority,
		}
		if hasDirectory {
			out := &workspaceConfinedClockLockerAuthorityDirectoryScope{
				workspaceConfinedClockLockerAuthorityScope: base,
				workspaceConfinedDirectoryReader: &workspaceConfinedDirectoryReader{
					reader: directory, boundary: confined.b,
				},
			}
			if hasAuthorization {
				return &workspaceConfinedClockLockerAuthorityDirectoryAuthorizationEpochScope{
					workspaceConfinedClockLockerAuthorityDirectoryScope: out,
					workspaceConfinedAuthorizationEpochPorts:            authorizationPorts,
				}, nil
			}
			return out, nil
		}
		if hasAuthorization {
			return &workspaceConfinedClockLockerAuthorityAuthorizationEpochScope{
				workspaceConfinedClockLockerAuthorityScope: base,
				workspaceConfinedAuthorizationEpochPorts:   authorizationPorts,
			}, nil
		}
		return base, nil
	case hasClock && hasAuthority:
		base := &workspaceConfinedClockAuthorityScope{
			workspaceConfinedClockScope: &workspaceConfinedClockScope{
				workspaceConfinedScope: confined,
				clock:                  clock,
			},
			authority: authority,
		}
		if hasDirectory {
			out := &workspaceConfinedClockAuthorityDirectoryScope{
				workspaceConfinedClockAuthorityScope: base,
				workspaceConfinedDirectoryReader: &workspaceConfinedDirectoryReader{
					reader: directory, boundary: confined.b,
				},
			}
			if hasAuthorization {
				return &workspaceConfinedClockAuthorityDirectoryAuthorizationEpochScope{
					workspaceConfinedClockAuthorityDirectoryScope: out,
					workspaceConfinedAuthorizationEpochPorts:      authorizationPorts,
				}, nil
			}
			return out, nil
		}
		if hasAuthorization {
			return &workspaceConfinedClockAuthorityAuthorizationEpochScope{
				workspaceConfinedClockAuthorityScope:     base,
				workspaceConfinedAuthorizationEpochPorts: authorizationPorts,
			}, nil
		}
		return base, nil
	case hasLocker && hasAuthority:
		base := &workspaceConfinedLockerAuthorityScope{
			workspaceConfinedLockerScope: &workspaceConfinedLockerScope{
				workspaceConfinedScope: confined,
				locker:                 locker,
			},
			authority: authority,
		}
		if hasDirectory {
			out := &workspaceConfinedLockerAuthorityDirectoryScope{
				workspaceConfinedLockerAuthorityScope: base,
				workspaceConfinedDirectoryReader: &workspaceConfinedDirectoryReader{
					reader: directory, boundary: confined.b,
				},
			}
			if hasAuthorization {
				return &workspaceConfinedLockerAuthorityDirectoryAuthorizationEpochScope{
					workspaceConfinedLockerAuthorityDirectoryScope: out,
					workspaceConfinedAuthorizationEpochPorts:       authorizationPorts,
				}, nil
			}
			return out, nil
		}
		if hasAuthorization {
			return &workspaceConfinedLockerAuthorityAuthorizationEpochScope{
				workspaceConfinedLockerAuthorityScope:    base,
				workspaceConfinedAuthorizationEpochPorts: authorizationPorts,
			}, nil
		}
		return base, nil
	case hasAuthority:
		base := &workspaceConfinedAuthorityScope{
			workspaceConfinedScope: confined,
			authority:              authority,
		}
		if hasDirectory {
			out := &workspaceConfinedAuthorityDirectoryScope{
				workspaceConfinedAuthorityScope: base,
				workspaceConfinedDirectoryReader: &workspaceConfinedDirectoryReader{
					reader: directory, boundary: confined.b,
				},
			}
			if hasAuthorization {
				return &workspaceConfinedAuthorityDirectoryAuthorizationEpochScope{
					workspaceConfinedAuthorityDirectoryScope: out,
					workspaceConfinedAuthorizationEpochPorts: authorizationPorts,
				}, nil
			}
			return out, nil
		}
		if hasAuthorization {
			return &workspaceConfinedAuthorityAuthorizationEpochScope{
				workspaceConfinedAuthorityScope:          base,
				workspaceConfinedAuthorizationEpochPorts: authorizationPorts,
			}, nil
		}
		return base, nil
	case hasClock && hasLocker:
		base := &workspaceConfinedClockLockerScope{
			workspaceConfinedClockScope: &workspaceConfinedClockScope{
				workspaceConfinedScope: confined,
				clock:                  clock,
			},
			locker: locker,
		}
		if hasDirectory {
			out := &workspaceConfinedClockLockerDirectoryScope{
				workspaceConfinedClockLockerScope: base,
				workspaceConfinedDirectoryReader: &workspaceConfinedDirectoryReader{
					reader: directory, boundary: confined.b,
				},
			}
			if hasAuthorization {
				return &workspaceConfinedClockLockerDirectoryAuthorizationEpochScope{
					workspaceConfinedClockLockerDirectoryScope: out,
					workspaceConfinedAuthorizationEpochPorts:   authorizationPorts,
				}, nil
			}
			return out, nil
		}
		if hasAuthorization {
			return &workspaceConfinedClockLockerAuthorizationEpochScope{
				workspaceConfinedClockLockerScope:        base,
				workspaceConfinedAuthorizationEpochPorts: authorizationPorts,
			}, nil
		}
		return base, nil
	case hasClock:
		base := &workspaceConfinedClockScope{workspaceConfinedScope: confined, clock: clock}
		if hasDirectory {
			out := &workspaceConfinedClockDirectoryScope{
				workspaceConfinedClockScope: base,
				workspaceConfinedDirectoryReader: &workspaceConfinedDirectoryReader{
					reader: directory, boundary: confined.b,
				},
			}
			if hasAuthorization {
				return &workspaceConfinedClockDirectoryAuthorizationEpochScope{
					workspaceConfinedClockDirectoryScope:     out,
					workspaceConfinedAuthorizationEpochPorts: authorizationPorts,
				}, nil
			}
			return out, nil
		}
		if hasAuthorization {
			return &workspaceConfinedClockAuthorizationEpochScope{
				workspaceConfinedClockScope:              base,
				workspaceConfinedAuthorizationEpochPorts: authorizationPorts,
			}, nil
		}
		return base, nil
	case hasLocker:
		base := &workspaceConfinedLockerScope{workspaceConfinedScope: confined, locker: locker}
		if hasDirectory {
			out := &workspaceConfinedLockerDirectoryScope{
				workspaceConfinedLockerScope: base,
				workspaceConfinedDirectoryReader: &workspaceConfinedDirectoryReader{
					reader: directory, boundary: confined.b,
				},
			}
			if hasAuthorization {
				return &workspaceConfinedLockerDirectoryAuthorizationEpochScope{
					workspaceConfinedLockerDirectoryScope:    out,
					workspaceConfinedAuthorizationEpochPorts: authorizationPorts,
				}, nil
			}
			return out, nil
		}
		if hasAuthorization {
			return &workspaceConfinedLockerAuthorizationEpochScope{
				workspaceConfinedLockerScope:             base,
				workspaceConfinedAuthorizationEpochPorts: authorizationPorts,
			}, nil
		}
		return base, nil
	case hasDirectory:
		out := &workspaceConfinedDirectoryScope{
			workspaceConfinedScope: confined,
			workspaceConfinedDirectoryReader: &workspaceConfinedDirectoryReader{
				reader: directory, boundary: confined.b,
			},
		}
		if hasAuthorization {
			return &workspaceConfinedDirectoryAuthorizationEpochScope{
				workspaceConfinedDirectoryScope:          out,
				workspaceConfinedAuthorizationEpochPorts: authorizationPorts,
			}, nil
		}
		return out, nil
	case hasAuthorization:
		return &workspaceConfinedAuthorizationEpochScope{
			workspaceConfinedScope:                   confined,
			workspaceConfinedAuthorizationEpochPorts: authorizationPorts,
		}, nil
	}
	return confined, nil
}

// workspaceBoundary is the resolved confinement: which workspace, its slug (for
// a slug-encoded lineage column) and the tenant's default workspace id (which
// decides what an unset lineage value means).
type workspaceBoundary struct {
	id        model.ID
	slug      string
	defaultID model.ID
}

// isDefault reports whether the confined workspace IS the tenant's default one —
// the case in which rows carrying no workspace at all belong to the caller.
func (b workspaceBoundary) isDefault() bool { return b.id == b.defaultID }

// effectiveID resolves a stored lineage id to the workspace it actually belongs
// to: an unset id means the tenant's default workspace (core/model/scoping.go).
func (b workspaceBoundary) effectiveID(stored model.ID) model.ID {
	if stored.IsZero() {
		return b.defaultID
	}
	return stored
}

// filterFor returns the forced predicate for a lineage spec. For an entity whose
// unset value means "default workspace", a caller confined TO the default must
// also match the rows that carry no value, which is the one predicate OpEq
// cannot express (hence model.OpEqOrUnset).
func (b workspaceBoundary) filterFor(spec model.WorkspaceLineageSpec) model.Filter {
	value := b.id.String()
	if spec.Encoding == model.WorkspaceLineageSlug {
		value = b.slug
	}
	op := model.OpEq
	if spec.Unset == model.WorkspaceUnsetMeansDefault && b.isDefault() {
		op = model.OpEqOrUnset
	}
	return model.Filter{Column: spec.Column, Op: op, Value: value}
}

// owns reports whether a stored lineage VALUE (as read from a row) is inside the
// boundary. ok=false means the value is unreadable for the declared encoding —
// which is not "unset": a lineage column holding something that is not a
// workspace is a fault, and the caller denies rather than admits the row.
func (b workspaceBoundary) owns(spec model.WorkspaceLineageSpec, raw string) (inside, ok bool) {
	if raw == "" {
		// Unset: belongs to the default workspace, or to nobody.
		//
		// ⛔ THE TWO SPELLINGS ARE NOT INTERCHANGEABLE, AND THE WRONG ONE FAILS SILENTLY.
		// This branch is the whole difference: with WorkspaceUnsetMeansDefault an empty
		// lineage is INSIDE the default workspace; without it the same row belongs to
		// NOBODY and simply stops being returned. Neither spelling errors, neither logs,
		// and a query that quietly returns fewer rows reads like "there is nothing there".
		//
		// The two live cases deliberately do not share a lineage declaration:
		//   modules/sessions/work_schema.go  work items -> WorkspaceUnsetHidden
		//   sessions.identity (K2)           no WorkspaceLineage on the entity descriptor
		// The K2 participant resolver reads identity.workspace_id explicitly and maps an
		// unset value to the tenant's default workspace. A work item with no workspace is
		// instead invisible. Declaring a new lineage by copying the neighboring table's
		// constant is how those meanings get crossed.
		//
		// ⇒ Whoever adds a lineage spec CHOOSES this field deliberately and says why in the
		// same commit. Named by the K2 lane on 2026-08-12, before it bit anyone.
		if spec.Unset == model.WorkspaceUnsetMeansDefault {
			return b.isDefault(), true
		}
		return false, true
	}
	switch spec.Encoding {
	case model.WorkspaceLineageSlug:
		return raw == b.slug, true
	case model.WorkspaceLineageID:
		id, err := model.ParseID(raw)
		if err != nil || id.IsZero() {
			return false, false
		}
		return b.effectiveID(id) == b.id, true
	default:
		return false, false
	}
}

// forceQuery returns q with every caller-supplied predicate on the lineage
// column REPLACED by the mandatory one. Replacing rather than appending is the
// difference between a confinement and a suggestion: a route that appends the
// caller's ?workspace_id (as one module route did) would otherwise let a
// confined caller name someone else's workspace and be answered — and an
// AND of two workspaces would return an empty page, which is a different lie.
// Every other filter, the sort, the cursor and the limit are preserved, so
// paging stays keyset-native in SQL.
func forceQuery(q model.Query, f model.Filter) model.Query {
	out := q
	out.Filters = make([]model.Filter, 0, len(q.Filters)+1)
	for _, existing := range q.Filters {
		if existing.Column == f.Column {
			continue
		}
		out.Filters = append(out.Filters, existing)
	}
	out.Filters = append(out.Filters, f)
	return out
}

// denied is the refusal for a member of Scope that carries no workspace lineage.
func denied(what string) error {
	return fmt.Errorf("%w: %s carries no workspace lineage", ErrWorkspaceLineageRequired, what)
}

// deniedWrite is the refusal for a write that would land outside the boundary.
func deniedWrite(what string) error {
	return fmt.Errorf("%w: %s", ErrWorkspaceConfinement, what)
}

// workspaceConfinedScope implements Scope with row-level workspace confinement.
//
// The compile-time assertion below is load-bearing: it is what makes a NEW
// accessor on Scope a build failure here instead of a silent tenant-wide hole.
var _ Scope = (*workspaceConfinedScope)(nil)

type workspaceConfinedScope struct {
	raw Scope
	b   workspaceBoundary
}

// workspaceConfinement is deliberately private to this package. It makes the
// decorator idempotent without exporting a capability that an arbitrary module
// scope could forge. Every optional-capability adapter embeds this base, so the
// marker is promoted without exposing raw.
type workspaceConfinement interface {
	confinedWorkspaceID() model.ID
}

func (s *workspaceConfinedScope) confinedWorkspaceID() model.ID { return s.b.id }

// workspaceConfinedClockScope preserves TransactionClock across workspace
// confinement without broadening Scope itself. Time has no row payload or
// workspace identity to disclose; delegating this read cannot bypass any of the
// repository decorators on workspaceConfinedScope.
type workspaceConfinedClockScope struct {
	*workspaceConfinedScope
	clock TransactionClock
}

var _ Scope = (*workspaceConfinedClockScope)(nil)
var _ TransactionClock = (*workspaceConfinedClockScope)(nil)

func (s *workspaceConfinedClockScope) TransactionNow(ctx context.Context) (model.Timestamp, error) {
	return s.clock.TransactionNow(ctx)
}

// workspaceConfinedLockerScope preserves TransactionLocker without exposing any
// tenant-wide repository. A transaction lock carries no row payload or workspace
// identity; the caller's key selects only which transactions serialize.
type workspaceConfinedLockerScope struct {
	*workspaceConfinedScope
	locker TransactionLocker
}

var _ Scope = (*workspaceConfinedLockerScope)(nil)
var _ TransactionLocker = (*workspaceConfinedLockerScope)(nil)

func (s *workspaceConfinedLockerScope) LockTransaction(ctx context.Context, key string) error {
	return s.locker.LockTransaction(ctx, key)
}

// workspaceConfinedClockLockerScope is the explicit intersection of the two
// optional capabilities. A separate adapter is necessary: embedding either
// single-capability adapter alone would silently hide the other capability.
type workspaceConfinedClockLockerScope struct {
	*workspaceConfinedClockScope
	locker TransactionLocker
}

var _ Scope = (*workspaceConfinedClockLockerScope)(nil)
var _ TransactionClock = (*workspaceConfinedClockLockerScope)(nil)
var _ TransactionLocker = (*workspaceConfinedClockLockerScope)(nil)

func (s *workspaceConfinedClockLockerScope) LockTransaction(ctx context.Context, key string) error {
	return s.locker.LockTransaction(ctx, key)
}

// The four authority adapters complete the optional-capability matrix without
// embedding the raw Scope. LockAuthoritySnapshot returns no row payload, so it
// can pin an allowlisted tenant-wide decision while every data accessor remains
// workspace-confined.
type workspaceConfinedAuthorityScope struct {
	*workspaceConfinedScope
	authority AuthoritySnapshotLocker
}

var _ Scope = (*workspaceConfinedAuthorityScope)(nil)
var _ AuthoritySnapshotLocker = (*workspaceConfinedAuthorityScope)(nil)

func (s *workspaceConfinedAuthorityScope) LockAuthoritySnapshot(
	ctx context.Context,
	refs []AuthorizationFactRef,
) error {
	return s.authority.LockAuthoritySnapshot(ctx, refs)
}

type workspaceConfinedClockAuthorityScope struct {
	*workspaceConfinedClockScope
	authority AuthoritySnapshotLocker
}

var _ Scope = (*workspaceConfinedClockAuthorityScope)(nil)
var _ TransactionClock = (*workspaceConfinedClockAuthorityScope)(nil)
var _ AuthoritySnapshotLocker = (*workspaceConfinedClockAuthorityScope)(nil)

func (s *workspaceConfinedClockAuthorityScope) LockAuthoritySnapshot(
	ctx context.Context,
	refs []AuthorizationFactRef,
) error {
	return s.authority.LockAuthoritySnapshot(ctx, refs)
}

type workspaceConfinedLockerAuthorityScope struct {
	*workspaceConfinedLockerScope
	authority AuthoritySnapshotLocker
}

var _ Scope = (*workspaceConfinedLockerAuthorityScope)(nil)
var _ TransactionLocker = (*workspaceConfinedLockerAuthorityScope)(nil)
var _ AuthoritySnapshotLocker = (*workspaceConfinedLockerAuthorityScope)(nil)

func (s *workspaceConfinedLockerAuthorityScope) LockAuthoritySnapshot(
	ctx context.Context,
	refs []AuthorizationFactRef,
) error {
	return s.authority.LockAuthoritySnapshot(ctx, refs)
}

type workspaceConfinedClockLockerAuthorityScope struct {
	*workspaceConfinedClockLockerScope
	authority AuthoritySnapshotLocker
}

var _ Scope = (*workspaceConfinedClockLockerAuthorityScope)(nil)
var _ TransactionClock = (*workspaceConfinedClockLockerAuthorityScope)(nil)
var _ TransactionLocker = (*workspaceConfinedClockLockerAuthorityScope)(nil)
var _ AuthoritySnapshotLocker = (*workspaceConfinedClockLockerAuthorityScope)(nil)

func (s *workspaceConfinedClockLockerAuthorityScope) LockAuthoritySnapshot(
	ctx context.Context,
	refs []AuthorizationFactRef,
) error {
	return s.authority.LockAuthoritySnapshot(ctx, refs)
}

// workspaceConfinedDirectoryReader preserves only the typed, payload-free
// directory evidence surface. It cannot enumerate directory rows or retire a
// principal, and every read remains bound to the raw Scope's tenant and
// transaction. This is the K3 seam that lets a workspace-confined route prove
// epoch/tombstone evidence without widening back to a tenant-wide repository.
type workspaceConfinedDirectoryReader struct {
	reader   DirectorySnapshotReader
	boundary workspaceBoundary
}

func (s *workspaceConfinedDirectoryReader) ReadDirectoryEpoch(
	ctx context.Context,
) (model.DirectoryEpoch, error) {
	return s.reader.ReadDirectoryEpoch(ctx)
}

func (s *workspaceConfinedDirectoryReader) ReadDirectoryTombstone(
	ctx context.Context,
	ref DirectoryPrincipalRef,
) (DirectoryTombstoneWitness, bool, error) {
	if err := ref.Validate(); err != nil {
		return DirectoryTombstoneWitness{}, false, err
	}
	// User and Identity retirement evidence is tenant-wide and carries no
	// workspace axis. An Agent is workspace-bearing: permitting an empty or
	// foreign workspace there would turn this narrow witness into a tenant-wide
	// existence oracle. Conversely, accepting a workspace on an Identity would
	// create a spelling that the canonical tombstone model never emits.
	workspaceMatches := ref.PrincipalKind == model.DirectoryPrincipalAgent &&
		ref.WorkspaceRef == s.boundary.id
	workspaceAbsent := ref.PrincipalKind != model.DirectoryPrincipalAgent &&
		ref.WorkspaceRef == ""
	if !workspaceMatches && !workspaceAbsent {
		return DirectoryTombstoneWitness{}, false, fmt.Errorf(
			"%w: directory principal workspace %q is outside confined workspace %q",
			ErrWorkspaceConfinement, ref.WorkspaceRef, s.boundary.id,
		)
	}
	return s.reader.ReadDirectoryTombstone(ctx, ref)
}

// The eight adapters below complete the optional-capability matrix. Each
// embeds an already-confined adapter and the narrow directory reader; none
// embeds the raw Scope or exposes an unreviewed tenant-wide accessor.
type workspaceConfinedDirectoryScope struct {
	*workspaceConfinedScope
	*workspaceConfinedDirectoryReader
}

var _ Scope = (*workspaceConfinedDirectoryScope)(nil)
var _ DirectorySnapshotReader = (*workspaceConfinedDirectoryScope)(nil)

type workspaceConfinedClockDirectoryScope struct {
	*workspaceConfinedClockScope
	*workspaceConfinedDirectoryReader
}

var _ Scope = (*workspaceConfinedClockDirectoryScope)(nil)
var _ TransactionClock = (*workspaceConfinedClockDirectoryScope)(nil)
var _ DirectorySnapshotReader = (*workspaceConfinedClockDirectoryScope)(nil)

type workspaceConfinedLockerDirectoryScope struct {
	*workspaceConfinedLockerScope
	*workspaceConfinedDirectoryReader
}

var _ Scope = (*workspaceConfinedLockerDirectoryScope)(nil)
var _ TransactionLocker = (*workspaceConfinedLockerDirectoryScope)(nil)
var _ DirectorySnapshotReader = (*workspaceConfinedLockerDirectoryScope)(nil)

type workspaceConfinedClockLockerDirectoryScope struct {
	*workspaceConfinedClockLockerScope
	*workspaceConfinedDirectoryReader
}

var _ Scope = (*workspaceConfinedClockLockerDirectoryScope)(nil)
var _ TransactionClock = (*workspaceConfinedClockLockerDirectoryScope)(nil)
var _ TransactionLocker = (*workspaceConfinedClockLockerDirectoryScope)(nil)
var _ DirectorySnapshotReader = (*workspaceConfinedClockLockerDirectoryScope)(nil)

type workspaceConfinedAuthorityDirectoryScope struct {
	*workspaceConfinedAuthorityScope
	*workspaceConfinedDirectoryReader
}

var _ Scope = (*workspaceConfinedAuthorityDirectoryScope)(nil)
var _ AuthoritySnapshotLocker = (*workspaceConfinedAuthorityDirectoryScope)(nil)
var _ DirectorySnapshotReader = (*workspaceConfinedAuthorityDirectoryScope)(nil)

type workspaceConfinedClockAuthorityDirectoryScope struct {
	*workspaceConfinedClockAuthorityScope
	*workspaceConfinedDirectoryReader
}

var _ Scope = (*workspaceConfinedClockAuthorityDirectoryScope)(nil)
var _ TransactionClock = (*workspaceConfinedClockAuthorityDirectoryScope)(nil)
var _ AuthoritySnapshotLocker = (*workspaceConfinedClockAuthorityDirectoryScope)(nil)
var _ DirectorySnapshotReader = (*workspaceConfinedClockAuthorityDirectoryScope)(nil)

type workspaceConfinedLockerAuthorityDirectoryScope struct {
	*workspaceConfinedLockerAuthorityScope
	*workspaceConfinedDirectoryReader
}

var _ Scope = (*workspaceConfinedLockerAuthorityDirectoryScope)(nil)
var _ TransactionLocker = (*workspaceConfinedLockerAuthorityDirectoryScope)(nil)
var _ AuthoritySnapshotLocker = (*workspaceConfinedLockerAuthorityDirectoryScope)(nil)
var _ DirectorySnapshotReader = (*workspaceConfinedLockerAuthorityDirectoryScope)(nil)

type workspaceConfinedClockLockerAuthorityDirectoryScope struct {
	*workspaceConfinedClockLockerAuthorityScope
	*workspaceConfinedDirectoryReader
}

var _ Scope = (*workspaceConfinedClockLockerAuthorityDirectoryScope)(nil)
var _ TransactionClock = (*workspaceConfinedClockLockerAuthorityDirectoryScope)(nil)
var _ TransactionLocker = (*workspaceConfinedClockLockerAuthorityDirectoryScope)(nil)
var _ AuthoritySnapshotLocker = (*workspaceConfinedClockLockerAuthorityDirectoryScope)(nil)
var _ DirectorySnapshotReader = (*workspaceConfinedClockLockerAuthorityDirectoryScope)(nil)

// workspaceConfinedAuthorizationEpochPorts forwards only the payload-free
// authorization-generation ports. It validates both directions against the
// tenant captured before confinement, so a malformed implementation cannot
// turn a foreign or discontinuous generation into usable evidence.
type workspaceConfinedAuthorizationEpochPorts struct {
	store  AuthorizationEpochStore
	tenant model.TenantID
}

var _ AuthorizationEpochStore = (*workspaceConfinedAuthorizationEpochPorts)(nil)

func (s *workspaceConfinedAuthorizationEpochPorts) ReadAuthorizationEpoch(
	ctx context.Context,
) (AuthorizationFactRef, error) {
	fact, err := s.store.ReadAuthorizationEpoch(ctx)
	if err != nil {
		return AuthorizationFactRef{}, authorizationEpochUnavailable("confined read", err)
	}
	if err := validateAuthorizationEpochFact(s.tenant, fact); err != nil {
		return AuthorizationFactRef{}, err
	}
	return fact, nil
}

func (s *workspaceConfinedAuthorizationEpochPorts) BumpAuthorizationEpoch(
	ctx context.Context,
	expected AuthorizationFactRef,
) (AuthorizationFactRef, error) {
	if err := validateAuthorizationEpochFact(s.tenant, expected); err != nil {
		return AuthorizationFactRef{}, err
	}
	next, err := s.store.BumpAuthorizationEpoch(ctx, expected)
	if err != nil {
		if errors.Is(err, ErrReadOnly) {
			return AuthorizationFactRef{}, err
		}
		return AuthorizationFactRef{}, authorizationEpochUnavailable("confined bump", err)
	}
	if err := validateAuthorizationEpochAdvance(s.tenant, expected, next); err != nil {
		return AuthorizationFactRef{}, err
	}
	return next, nil
}

// These sixteen adapters add the one complete authorization epoch capability
// to every existing clock/locker/authority/directory combination. None embeds
// raw Scope: each starts from the already-confined adapter above it.
type workspaceConfinedAuthorizationEpochScope struct {
	*workspaceConfinedScope
	*workspaceConfinedAuthorizationEpochPorts
}

type workspaceConfinedClockAuthorizationEpochScope struct {
	*workspaceConfinedClockScope
	*workspaceConfinedAuthorizationEpochPorts
}

type workspaceConfinedLockerAuthorizationEpochScope struct {
	*workspaceConfinedLockerScope
	*workspaceConfinedAuthorizationEpochPorts
}

type workspaceConfinedClockLockerAuthorizationEpochScope struct {
	*workspaceConfinedClockLockerScope
	*workspaceConfinedAuthorizationEpochPorts
}

type workspaceConfinedAuthorityAuthorizationEpochScope struct {
	*workspaceConfinedAuthorityScope
	*workspaceConfinedAuthorizationEpochPorts
}

type workspaceConfinedClockAuthorityAuthorizationEpochScope struct {
	*workspaceConfinedClockAuthorityScope
	*workspaceConfinedAuthorizationEpochPorts
}

type workspaceConfinedLockerAuthorityAuthorizationEpochScope struct {
	*workspaceConfinedLockerAuthorityScope
	*workspaceConfinedAuthorizationEpochPorts
}

type workspaceConfinedClockLockerAuthorityAuthorizationEpochScope struct {
	*workspaceConfinedClockLockerAuthorityScope
	*workspaceConfinedAuthorizationEpochPorts
}

type workspaceConfinedDirectoryAuthorizationEpochScope struct {
	*workspaceConfinedDirectoryScope
	*workspaceConfinedAuthorizationEpochPorts
}

type workspaceConfinedClockDirectoryAuthorizationEpochScope struct {
	*workspaceConfinedClockDirectoryScope
	*workspaceConfinedAuthorizationEpochPorts
}

type workspaceConfinedLockerDirectoryAuthorizationEpochScope struct {
	*workspaceConfinedLockerDirectoryScope
	*workspaceConfinedAuthorizationEpochPorts
}

type workspaceConfinedClockLockerDirectoryAuthorizationEpochScope struct {
	*workspaceConfinedClockLockerDirectoryScope
	*workspaceConfinedAuthorizationEpochPorts
}

type workspaceConfinedAuthorityDirectoryAuthorizationEpochScope struct {
	*workspaceConfinedAuthorityDirectoryScope
	*workspaceConfinedAuthorizationEpochPorts
}

type workspaceConfinedClockAuthorityDirectoryAuthorizationEpochScope struct {
	*workspaceConfinedClockAuthorityDirectoryScope
	*workspaceConfinedAuthorizationEpochPorts
}

type workspaceConfinedLockerAuthorityDirectoryAuthorizationEpochScope struct {
	*workspaceConfinedLockerAuthorityDirectoryScope
	*workspaceConfinedAuthorizationEpochPorts
}

type workspaceConfinedClockLockerAuthorityDirectoryAuthorizationEpochScope struct {
	*workspaceConfinedClockLockerAuthorityDirectoryScope
	*workspaceConfinedAuthorizationEpochPorts
}

var (
	_ AuthorizationEpochStore = (*workspaceConfinedAuthorizationEpochScope)(nil)
	_ AuthorizationEpochStore = (*workspaceConfinedClockAuthorizationEpochScope)(nil)
	_ AuthorizationEpochStore = (*workspaceConfinedLockerAuthorizationEpochScope)(nil)
	_ AuthorizationEpochStore = (*workspaceConfinedClockLockerAuthorizationEpochScope)(nil)
	_ AuthorizationEpochStore = (*workspaceConfinedAuthorityAuthorizationEpochScope)(nil)
	_ AuthorizationEpochStore = (*workspaceConfinedClockAuthorityAuthorizationEpochScope)(nil)
	_ AuthorizationEpochStore = (*workspaceConfinedLockerAuthorityAuthorizationEpochScope)(nil)
	_ AuthorizationEpochStore = (*workspaceConfinedClockLockerAuthorityAuthorizationEpochScope)(nil)
	_ AuthorizationEpochStore = (*workspaceConfinedDirectoryAuthorizationEpochScope)(nil)
	_ AuthorizationEpochStore = (*workspaceConfinedClockDirectoryAuthorizationEpochScope)(nil)
	_ AuthorizationEpochStore = (*workspaceConfinedLockerDirectoryAuthorizationEpochScope)(nil)
	_ AuthorizationEpochStore = (*workspaceConfinedClockLockerDirectoryAuthorizationEpochScope)(nil)
	_ AuthorizationEpochStore = (*workspaceConfinedAuthorityDirectoryAuthorizationEpochScope)(nil)
	_ AuthorizationEpochStore = (*workspaceConfinedClockAuthorityDirectoryAuthorizationEpochScope)(nil)
	_ AuthorizationEpochStore = (*workspaceConfinedLockerAuthorityDirectoryAuthorizationEpochScope)(nil)
	_ AuthorizationEpochStore = (*workspaceConfinedClockLockerAuthorityDirectoryAuthorizationEpochScope)(nil)
)

// Tenant is unchanged: the tenant is already resolved, authorized and visible to
// the caller; naming it grants no rows.
func (s *workspaceConfinedScope) Tenant() model.TenantID { return s.raw.Tenant() }

// Org and its settings are the tenant's own row — configuration of the PARENT of
// every workspace, with no workspace axis. A confined principal gets neither a
// partial view (there is no such thing) nor the whole one.
func (s *workspaceConfinedScope) Org(ctx context.Context) (model.Org, error) {
	return model.Org{}, denied("the tenant org row")
}

func (s *workspaceConfinedScope) SetOrgSettings(ctx context.Context, settings map[string]any) (model.Org, error) {
	return model.Org{}, deniedWrite("tenant-wide org settings are not writable by a workspace-confined caller")
}

// The four core entities that carry the axis. Their lineage spec lives in
// the catalog descriptor, so the same declaration drives core and modules.
func (s *workspaceConfinedScope) Agents() Repository[model.Agent] {
	return confinedRepo[model.Agent]{
		raw: s.raw.Agents(), b: s.b, spec: agentLineage,
		workspaceOf: func(a model.Agent) model.ID { return a.WorkspaceID },
	}
}

func (s *workspaceConfinedScope) Sessions() Repository[model.Session] {
	return confinedRepo[model.Session]{
		raw: s.raw.Sessions(), b: s.b, spec: agentLineage,
		workspaceOf: func(v model.Session) model.ID { return v.WorkspaceID },
	}
}

func (s *workspaceConfinedScope) AgentGroups() Repository[model.AgentGroup] {
	return confinedRepo[model.AgentGroup]{
		raw: s.raw.AgentGroups(), b: s.b, spec: agentLineage,
		workspaceOf: func(v model.AgentGroup) model.ID { return v.WorkspaceID },
	}
}

func (s *workspaceConfinedScope) Resources() ResourceRepo {
	return confinedResourceRepo{
		raw: s.raw.Resources(),
		flat: confinedRepo[model.Resource]{
			raw: s.raw.Resources(), b: s.b, spec: agentLineage,
			workspaceOf: func(v model.Resource) model.ID { return v.WorkspaceID },
		},
		b: s.b,
	}
}

// agentLineage is the lineage spec shared by the four core entities that carry
// workspace_id with FASE X back-compat semantics (unset == default workspace).
// It mirrors what their descriptors declare; the descriptor remains the source
// of truth for the SQL layer and for the module path.
var agentLineage = model.WorkspaceLineageSpec{
	Column:   "workspace_id",
	Encoding: model.WorkspaceLineageID,
	Unset:    model.WorkspaceUnsetMeansDefault,
}

// Workspaces is confined BY IDENTITY rather than by a lineage column: a
// workspace does not carry a workspace_id, it IS the node. A confined caller
// sees exactly its own, so a federated search or a picker cannot enumerate the
// names and ids of the tenant's other workspaces.
func (s *workspaceConfinedScope) Workspaces() Repository[model.Workspace] {
	return confinedSelfRepo{raw: s.raw.Workspaces(), b: s.b}
}

// DefaultWorkspace answers only when the caller IS confined to the default
// workspace. Otherwise it is a second workspace's row, which the caller may not
// see — the boundary resolved it internally from the RAW scope precisely so this
// method never has to hand it over.
func (s *workspaceConfinedScope) DefaultWorkspace(ctx context.Context) (model.Workspace, error) {
	if !s.b.isDefault() {
		return model.Workspace{}, ErrNotFound
	}
	return s.raw.DefaultWorkspace(ctx)
}

// AgentGroupMembers holds only group_id and agent_id, so a List cannot be
// row-filtered without a join the query model does not have. The point
// operations CAN prove both ends are visible, so they are allowed and List is
// refused — rather than the reverse, which is how an unfiltered enumeration
// would have shipped.
func (s *workspaceConfinedScope) AgentGroupMembers() Repository[model.AgentGroupMember] {
	return confinedMemberRepo{raw: s.raw.AgentGroupMembers(), scope: s}
}

// Identities carry no workspace, but an identity REACHED FROM a visible agent is
// already disclosed to the caller (the agent row names it). Get therefore
// answers only for an identity some visible agent is bound to; List and every
// write are refused. This keeps /v1/m/governance/bindings working for the rows
// the caller may see, without turning "knows a UUID" into authority.
func (s *workspaceConfinedScope) Identities() MutableRepository[model.Identity] {
	return confinedIdentityRepo{raw: s.raw.Identities(), agents: s.Agents()}
}

// Tenant-wide catalogs and ledgers with no workspace axis. Denied rather than
// passed through: if one of them is later ruled to be legitimately shared across
// workspaces, that must be an explicit, tested read-only decision on THAT
// accessor — never the accident of having no lineage.
func (s *workspaceConfinedScope) Providers() Repository[model.Provider] {
	return deniedRepo[model.Provider]{what: "providers"}
}
func (s *workspaceConfinedScope) Models() Repository[model.Model] {
	return deniedRepo[model.Model]{what: "models"}
}
func (s *workspaceConfinedScope) MCPServers() Repository[model.MCPServer] {
	return deniedRepo[model.MCPServer]{what: "mcp servers"}
}
func (s *workspaceConfinedScope) Skills() Repository[model.Skill] {
	return deniedRepo[model.Skill]{what: "skills"}
}
func (s *workspaceConfinedScope) Tools() Repository[model.Tool] {
	return deniedRepo[model.Tool]{what: "tools"}
}
func (s *workspaceConfinedScope) Policies() Repository[model.Policy] {
	return deniedRepo[model.Policy]{what: "policies"}
}
func (s *workspaceConfinedScope) Costs() Repository[model.CostRecord] {
	return deniedRepo[model.CostRecord]{what: "cost records"}
}
func (s *workspaceConfinedScope) Evals() Repository[model.EvalResult] {
	return deniedRepo[model.EvalResult]{what: "eval results"}
}
func (s *workspaceConfinedScope) Findings() Repository[model.Finding] {
	return deniedRepo[model.Finding]{what: "findings"}
}
func (s *workspaceConfinedScope) Health() Repository[model.HealthStatus] {
	return deniedRepo[model.HealthStatus]{what: "health statuses"}
}
func (s *workspaceConfinedScope) Deployments() Repository[model.Deployment] {
	return deniedRepo[model.Deployment]{what: "deployments"}
}

// AccessEdges is the cross-workspace object by construction — an edge joins two
// nodes that need not share a workspace — so no row filter can be total. The
// scoped engine already forbids the tenant-wide recon reads by permission; this
// is the belt for a route that reaches the graph under some other permission.
func (s *workspaceConfinedScope) AccessEdges() AccessEdgeRepo {
	return deniedAccessEdgeRepo{}
}

// Audit is append-only for a confined caller: a sensitive read must still be
// able to self-audit (the access-map pattern several governed reads follow),
// while Walk/Verify/Head would expose the tenant's whole evidence chain.
func (s *workspaceConfinedScope) Audit() AuditLog {
	return confineAuditLog(s.raw.Audit(), s.b.id)
}

// EvidenceOperations is the tenant-wide journal of governed external effects;
// none of its primitives can prove a workspace.
func (s *workspaceConfinedScope) EvidenceOperations() EvidenceOperationRepo {
	return deniedEvidenceOps{}
}

// Ext is where the module half of the guarantee lands: the descriptor's declared
// lineage drives the filter, and an entity that declares none is refused BEFORE
// a repo exists — so a module handler cannot receive a handle it would read as
// "empty means nothing to see".
func (s *workspaceConfinedScope) Ext(kind model.Kind) (GenericRepo, error) {
	raw, err := s.raw.Ext(kind)
	if err != nil {
		return nil, err
	}
	spec := raw.Descriptor().WorkspaceLineage
	if !spec.Declared() {
		return nil, denied(string(kind))
	}
	confined := confinedGenericRepo{raw: raw, b: s.b, spec: spec}
	stamped, hasStamped := raw.(TransactionStampedGenericRepo)
	locker, hasLocker := raw.(RowLocker[model.Record])
	switch {
	case hasStamped && hasLocker:
		return confinedTransactionStampedRowLockingGenericRepo{
			confinedTransactionStampedGenericRepo: confinedTransactionStampedGenericRepo{
				confinedGenericRepo: confined,
				stamped:             stamped,
			},
			locker: locker,
		}, nil
	case hasStamped:
		return confinedTransactionStampedGenericRepo{
			confinedGenericRepo: confined,
			stamped:             stamped,
		}, nil
	case hasLocker:
		return confinedRowLockingGenericRepo{
			confinedGenericRepo: confined,
			locker:              locker,
		}, nil
	}
	return confined, nil
}
