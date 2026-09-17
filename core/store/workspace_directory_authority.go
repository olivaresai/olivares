// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import "context"

// Workspace confinement attaches the OPTIONAL authority ports in ONE final
// step, and that single step is the whole design of this file (DAB1-F1).
//
// confineWorkspace already builds one of 32 NAMED concrete decorators from the
// observed clock/locker/authority/directory/authorization-epoch combination.
// Attaching a port re-wraps that named decorator in an anonymous struct type,
// which is no longer one of those names — so a SECOND attachment step chained
// after the first would reach its own switch with a type it cannot match and
// answer ErrWorkspaceConfinement for every confined SQL scope. The existing
// bundle wrapper already consumed that composable position.
//
// So the port SET is selected first, from the RAW capabilities, and exactly
// that set is attached to the named decorator by exactly one switch. The three
// sets are three distinct struct shapes on purpose: optionality is a
// compile-time property of the returned type, so a raw scope carrying only
// AuthoritySnapshotBundleLocker must not gain LockDirectoryAuthoritySnapshot,
// and a raw scope carrying only DirectoryAuthoritySnapshotLocker must not gain
// LockAuthoritySnapshotBundle. Neither adapter embeds the raw Scope or the
// Scope interface: embedding the interface would promote only Scope's own
// methods and silently drop clock, locker, authority, directory and
// authorization-epoch capabilities.

// This adapter embeds only reviewed, already-confined concrete scopes, exactly
// like workspaceAuthorityBundle. LockDirectoryAuthoritySnapshot returns no rows
// and accepts only opaque version references, so forwarding it reveals nothing
// the confinement withholds.
type workspaceDirectoryAuthority struct {
	locker DirectoryAuthoritySnapshotLocker
}

func (s *workspaceDirectoryAuthority) LockDirectoryAuthoritySnapshot(
	ctx context.Context, bundle AuthoritySnapshotBundle,
) error {
	return s.locker.LockDirectoryAuthoritySnapshot(ctx, bundle)
}

// forwardWorkspaceAuthorityPorts selects the port set from the RAW scope and
// attaches that exact set to the already-confined decorator.
//
// The "already forwarded" guard is PER PORT. ConfineWorkspace is idempotent for
// the same workspace — confineWorkspace returns an already-confined scope
// unchanged — and on that path the attached struct already carries its ports,
// so re-attaching would be wrong. But a guard that returned early as soon as
// ANY selected port was present would silently drop the other one, which is
// precisely how a two-port design loses a capability on re-confinement. The
// existing scope is therefore returned only when EVERY selected port is
// already exposed.
func forwardWorkspaceAuthorityPorts(confined Scope, raw Scope) (Scope, error) {
	bundle, wantBundle := raw.(AuthoritySnapshotBundleLocker)
	directory, wantDirectory := raw.(DirectoryAuthoritySnapshotLocker)
	factory, wantFactory := raw.(BoundedReaderFactory)
	claimer, wantCustody := raw.(CustodialEffectClaimer)
	if !wantBundle && !wantDirectory && !wantFactory && !wantCustody {
		return confined, nil
	}
	_, hasBundle := confined.(AuthoritySnapshotBundleLocker)
	_, hasDirectory := confined.(DirectoryAuthoritySnapshotLocker)
	_, hasFactory := confined.(BoundedReaderFactory)
	_, hasCustody := confined.(CustodialEffectClaimer)
	if (!wantBundle || hasBundle) && (!wantDirectory || hasDirectory) &&
		(!wantFactory || hasFactory) && (!wantCustody || hasCustody) {
		return confined, nil
	}
	// The custodial effect is an independent sixteenth combination bit. Its
	// eight sets — custody crossed with the four non-custody sets — are
	// attached by this same single step, in workspace_custodial_effect_ports.go.
	// It is selected FIRST because it is the only bit whose presence changes
	// which of the two attachment halves runs; the bounded-read factory remains
	// an independent bit WITHIN each half, so a scope that carries both never
	// loses either.
	if wantCustody {
		return forwardWorkspacePortsWithCustody(confined,
			bundle, wantBundle, directory, wantDirectory, factory, wantFactory, claimer)
	}
	// The bounded-read factory is an independent eighth bit. Its sets are
	// attached by the same single step, in workspace_bounded_reader_ports.go.
	if wantFactory {
		return forwardWorkspacePortsWithBoundedReader(confined, bundle, wantBundle, directory, wantDirectory, factory)
	}
	switch {
	case wantBundle && wantDirectory:
		return forwardWorkspaceAuthorityAndDirectory(confined, bundle, directory)
	case wantDirectory:
		return forwardWorkspaceDirectoryAuthority(confined, directory)
	default:
		return forwardWorkspaceAuthorityBundle(confined, bundle)
	}
}

// forwardWorkspaceDirectoryAuthority attaches the DAB1 port ALONE, for a raw
// scope that provides directory-authority admission and no bundle locker.
func forwardWorkspaceDirectoryAuthority(confined Scope, locker DirectoryAuthoritySnapshotLocker) (Scope, error) {
	port := &workspaceDirectoryAuthority{locker: locker}
	switch scoped := confined.(type) {
	case *workspaceConfinedScope:
		return &struct {
			*workspaceConfinedScope
			*workspaceDirectoryAuthority
		}{scoped, port}, nil
	case *workspaceConfinedClockScope:
		return &struct {
			*workspaceConfinedClockScope
			*workspaceDirectoryAuthority
		}{scoped, port}, nil
	case *workspaceConfinedLockerScope:
		return &struct {
			*workspaceConfinedLockerScope
			*workspaceDirectoryAuthority
		}{scoped, port}, nil
	case *workspaceConfinedClockLockerScope:
		return &struct {
			*workspaceConfinedClockLockerScope
			*workspaceDirectoryAuthority
		}{scoped, port}, nil
	case *workspaceConfinedAuthorityScope:
		return &struct {
			*workspaceConfinedAuthorityScope
			*workspaceDirectoryAuthority
		}{scoped, port}, nil
	case *workspaceConfinedClockAuthorityScope:
		return &struct {
			*workspaceConfinedClockAuthorityScope
			*workspaceDirectoryAuthority
		}{scoped, port}, nil
	case *workspaceConfinedLockerAuthorityScope:
		return &struct {
			*workspaceConfinedLockerAuthorityScope
			*workspaceDirectoryAuthority
		}{scoped, port}, nil
	case *workspaceConfinedClockLockerAuthorityScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityScope
			*workspaceDirectoryAuthority
		}{scoped, port}, nil
	case *workspaceConfinedDirectoryScope:
		return &struct {
			*workspaceConfinedDirectoryScope
			*workspaceDirectoryAuthority
		}{scoped, port}, nil
	case *workspaceConfinedClockDirectoryScope:
		return &struct {
			*workspaceConfinedClockDirectoryScope
			*workspaceDirectoryAuthority
		}{scoped, port}, nil
	case *workspaceConfinedLockerDirectoryScope:
		return &struct {
			*workspaceConfinedLockerDirectoryScope
			*workspaceDirectoryAuthority
		}{scoped, port}, nil
	case *workspaceConfinedClockLockerDirectoryScope:
		return &struct {
			*workspaceConfinedClockLockerDirectoryScope
			*workspaceDirectoryAuthority
		}{scoped, port}, nil
	case *workspaceConfinedAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedAuthorityDirectoryScope
			*workspaceDirectoryAuthority
		}{scoped, port}, nil
	case *workspaceConfinedClockAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedClockAuthorityDirectoryScope
			*workspaceDirectoryAuthority
		}{scoped, port}, nil
	case *workspaceConfinedLockerAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedLockerAuthorityDirectoryScope
			*workspaceDirectoryAuthority
		}{scoped, port}, nil
	case *workspaceConfinedClockLockerAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityDirectoryScope
			*workspaceDirectoryAuthority
		}{scoped, port}, nil
	case *workspaceConfinedAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedAuthorizationEpochScope
			*workspaceDirectoryAuthority
		}{scoped, port}, nil
	case *workspaceConfinedClockAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockAuthorizationEpochScope
			*workspaceDirectoryAuthority
		}{scoped, port}, nil
	case *workspaceConfinedLockerAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerAuthorizationEpochScope
			*workspaceDirectoryAuthority
		}{scoped, port}, nil
	case *workspaceConfinedClockLockerAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorizationEpochScope
			*workspaceDirectoryAuthority
		}{scoped, port}, nil
	case *workspaceConfinedAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedAuthorityAuthorizationEpochScope
			*workspaceDirectoryAuthority
		}{scoped, port}, nil
	case *workspaceConfinedClockAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockAuthorityAuthorizationEpochScope
			*workspaceDirectoryAuthority
		}{scoped, port}, nil
	case *workspaceConfinedLockerAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerAuthorityAuthorizationEpochScope
			*workspaceDirectoryAuthority
		}{scoped, port}, nil
	case *workspaceConfinedClockLockerAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityAuthorizationEpochScope
			*workspaceDirectoryAuthority
		}{scoped, port}, nil
	case *workspaceConfinedDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedDirectoryAuthorizationEpochScope
			*workspaceDirectoryAuthority
		}{scoped, port}, nil
	case *workspaceConfinedClockDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockDirectoryAuthorizationEpochScope
			*workspaceDirectoryAuthority
		}{scoped, port}, nil
	case *workspaceConfinedLockerDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerDirectoryAuthorizationEpochScope
			*workspaceDirectoryAuthority
		}{scoped, port}, nil
	case *workspaceConfinedClockLockerDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerDirectoryAuthorizationEpochScope
			*workspaceDirectoryAuthority
		}{scoped, port}, nil
	case *workspaceConfinedAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedAuthorityDirectoryAuthorizationEpochScope
			*workspaceDirectoryAuthority
		}{scoped, port}, nil
	case *workspaceConfinedClockAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockAuthorityDirectoryAuthorizationEpochScope
			*workspaceDirectoryAuthority
		}{scoped, port}, nil
	case *workspaceConfinedLockerAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerAuthorityDirectoryAuthorizationEpochScope
			*workspaceDirectoryAuthority
		}{scoped, port}, nil
	case *workspaceConfinedClockLockerAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityDirectoryAuthorizationEpochScope
			*workspaceDirectoryAuthority
		}{scoped, port}, nil
	default:
		return nil, ErrWorkspaceConfinement
	}
}

// forwardWorkspaceAuthorityAndDirectory attaches BOTH ports in one step, for a
// raw scope that provides both. Every ordinary SQL tenantScope is in this case.
func forwardWorkspaceAuthorityAndDirectory(confined Scope, bundle AuthoritySnapshotBundleLocker,
	directory DirectoryAuthoritySnapshotLocker) (Scope, error) {
	bundlePort := &workspaceAuthorityBundle{locker: bundle}
	directoryPort := &workspaceDirectoryAuthority{locker: directory}
	switch scoped := confined.(type) {
	case *workspaceConfinedScope:
		return &struct {
			*workspaceConfinedScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
		}{scoped, bundlePort, directoryPort}, nil
	case *workspaceConfinedClockScope:
		return &struct {
			*workspaceConfinedClockScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
		}{scoped, bundlePort, directoryPort}, nil
	case *workspaceConfinedLockerScope:
		return &struct {
			*workspaceConfinedLockerScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
		}{scoped, bundlePort, directoryPort}, nil
	case *workspaceConfinedClockLockerScope:
		return &struct {
			*workspaceConfinedClockLockerScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
		}{scoped, bundlePort, directoryPort}, nil
	case *workspaceConfinedAuthorityScope:
		return &struct {
			*workspaceConfinedAuthorityScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
		}{scoped, bundlePort, directoryPort}, nil
	case *workspaceConfinedClockAuthorityScope:
		return &struct {
			*workspaceConfinedClockAuthorityScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
		}{scoped, bundlePort, directoryPort}, nil
	case *workspaceConfinedLockerAuthorityScope:
		return &struct {
			*workspaceConfinedLockerAuthorityScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
		}{scoped, bundlePort, directoryPort}, nil
	case *workspaceConfinedClockLockerAuthorityScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
		}{scoped, bundlePort, directoryPort}, nil
	case *workspaceConfinedDirectoryScope:
		return &struct {
			*workspaceConfinedDirectoryScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
		}{scoped, bundlePort, directoryPort}, nil
	case *workspaceConfinedClockDirectoryScope:
		return &struct {
			*workspaceConfinedClockDirectoryScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
		}{scoped, bundlePort, directoryPort}, nil
	case *workspaceConfinedLockerDirectoryScope:
		return &struct {
			*workspaceConfinedLockerDirectoryScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
		}{scoped, bundlePort, directoryPort}, nil
	case *workspaceConfinedClockLockerDirectoryScope:
		return &struct {
			*workspaceConfinedClockLockerDirectoryScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
		}{scoped, bundlePort, directoryPort}, nil
	case *workspaceConfinedAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedAuthorityDirectoryScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
		}{scoped, bundlePort, directoryPort}, nil
	case *workspaceConfinedClockAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedClockAuthorityDirectoryScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
		}{scoped, bundlePort, directoryPort}, nil
	case *workspaceConfinedLockerAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedLockerAuthorityDirectoryScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
		}{scoped, bundlePort, directoryPort}, nil
	case *workspaceConfinedClockLockerAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityDirectoryScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
		}{scoped, bundlePort, directoryPort}, nil
	case *workspaceConfinedAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
		}{scoped, bundlePort, directoryPort}, nil
	case *workspaceConfinedClockAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
		}{scoped, bundlePort, directoryPort}, nil
	case *workspaceConfinedLockerAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
		}{scoped, bundlePort, directoryPort}, nil
	case *workspaceConfinedClockLockerAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
		}{scoped, bundlePort, directoryPort}, nil
	case *workspaceConfinedAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedAuthorityAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
		}{scoped, bundlePort, directoryPort}, nil
	case *workspaceConfinedClockAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockAuthorityAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
		}{scoped, bundlePort, directoryPort}, nil
	case *workspaceConfinedLockerAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerAuthorityAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
		}{scoped, bundlePort, directoryPort}, nil
	case *workspaceConfinedClockLockerAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
		}{scoped, bundlePort, directoryPort}, nil
	case *workspaceConfinedDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
		}{scoped, bundlePort, directoryPort}, nil
	case *workspaceConfinedClockDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
		}{scoped, bundlePort, directoryPort}, nil
	case *workspaceConfinedLockerDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
		}{scoped, bundlePort, directoryPort}, nil
	case *workspaceConfinedClockLockerDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
		}{scoped, bundlePort, directoryPort}, nil
	case *workspaceConfinedAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedAuthorityDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
		}{scoped, bundlePort, directoryPort}, nil
	case *workspaceConfinedClockAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockAuthorityDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
		}{scoped, bundlePort, directoryPort}, nil
	case *workspaceConfinedLockerAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerAuthorityDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
		}{scoped, bundlePort, directoryPort}, nil
	case *workspaceConfinedClockLockerAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
		}{scoped, bundlePort, directoryPort}, nil
	default:
		return nil, ErrWorkspaceConfinement
	}
}
