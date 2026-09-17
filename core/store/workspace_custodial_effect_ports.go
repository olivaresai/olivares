// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import "context"

// The custodial effect is the FOURTH optional capability bit of a confined
// Scope, beside the authority bundle, the directory-authority locker and the
// bounded-read factory. It is attached by the SAME single final step as those
// three (workspaceAuthorityPorts): the port set is chosen from the RAW scope
// and attached to the named decorator by exactly one switch, so no method set
// is lost and nothing is promoted.
//
// The eight switches below are the custody bit crossed with the existing four
// non-custody sets; each lists the same 32 named decorators. Together with the
// eight sets that carry no custody port, the final step selects one of SIXTEEN
// presence/absence combinations. Optionality stays a COMPILE-TIME property of
// the returned type: a raw scope that provides only the bundle locker must not
// gain LockDirectoryAuthoritySnapshot, NewBoundedReader or BindCustodialEffect
// merely because another scope elsewhere does.
//
// A second attachment step cannot exist. Attaching a port re-wraps the named
// decorator in an anonymous struct type, which is no longer one of those 32
// names, so a chained second switch would answer ErrWorkspaceConfinement for
// every confined SQL scope and silently drop whatever the first step attached.

// workspaceCustodialEffect is the confined custody port. It embeds only the
// reviewed raw claimer and installs its OWN boundary into every binding, so a
// caller can neither widen the confinement nor present an unconfined binding
// through a confined scope. The engine — not the module — then applies that
// boundary to the target read.
type workspaceCustodialEffect struct {
	claimer  CustodialEffectClaimer
	boundary workspaceBoundary
}

// BindCustodialEffect forwards the binding with this wrapper's confinement
// installed. It overwrites whatever boundary the value carried: the field is
// unexported to package store, so the only other producer is another confined
// wrapper, and taking the OUTERMOST wrapper's boundary is what makes a
// same-boundary re-confinement idempotent instead of accumulating.
func (s *workspaceCustodialEffect) BindCustodialEffect(
	ctx context.Context, b CustodialEffectBinding,
) (CustodialEffectHandle, error) {
	return s.claimer.BindCustodialEffect(ctx, b.confine(s.boundary))
}

// forwardWorkspacePortsWithCustody attaches the custody port together with
// exactly the selected bundle, directory and bounded-reader ports. It is the
// custody half of the single final selection step and is reached only from it.
func forwardWorkspacePortsWithCustody(
	confined Scope,
	bundle AuthoritySnapshotBundleLocker, wantBundle bool,
	directory DirectoryAuthoritySnapshotLocker, wantDirectory bool,
	factory BoundedReaderFactory, wantFactory bool,
	claimer CustodialEffectClaimer,
) (Scope, error) {
	holder, ok := confined.(workspaceBoundaryHolder)
	if !ok {
		return nil, ErrWorkspaceConfinement
	}
	boundary := holder.confinedBoundary()
	custody := &workspaceCustodialEffect{claimer: claimer, boundary: boundary}
	if wantFactory {
		reader := &workspaceBoundedReaderFactory{factory: factory, boundary: boundary}
		switch {
		case wantBundle && wantDirectory:
			return forwardWorkspaceBundleDirectoryBoundedReaderCustody(confined,
				&workspaceAuthorityBundle{locker: bundle},
				&workspaceDirectoryAuthority{locker: directory}, reader, custody)
		case wantBundle:
			return forwardWorkspaceBundleBoundedReaderCustody(confined,
				&workspaceAuthorityBundle{locker: bundle}, reader, custody)
		case wantDirectory:
			return forwardWorkspaceDirectoryBoundedReaderCustody(confined,
				&workspaceDirectoryAuthority{locker: directory}, reader, custody)
		default:
			return forwardWorkspaceBoundedReaderCustody(confined, reader, custody)
		}
	}
	switch {
	case wantBundle && wantDirectory:
		return forwardWorkspaceBundleDirectoryCustody(confined,
			&workspaceAuthorityBundle{locker: bundle},
			&workspaceDirectoryAuthority{locker: directory}, custody)
	case wantBundle:
		return forwardWorkspaceBundleCustody(confined,
			&workspaceAuthorityBundle{locker: bundle}, custody)
	case wantDirectory:
		return forwardWorkspaceDirectoryCustody(confined,
			&workspaceDirectoryAuthority{locker: directory}, custody)
	default:
		return forwardWorkspaceCustody(confined, custody)
	}
}

// forwardWorkspaceCustody attaches the custody port ALONE.
func forwardWorkspaceCustody(confined Scope, custody *workspaceCustodialEffect) (Scope, error) {
	switch scoped := confined.(type) {
	case *workspaceConfinedScope:
		return &struct {
			*workspaceConfinedScope
			*workspaceCustodialEffect
		}{scoped, custody}, nil
	case *workspaceConfinedClockScope:
		return &struct {
			*workspaceConfinedClockScope
			*workspaceCustodialEffect
		}{scoped, custody}, nil
	case *workspaceConfinedLockerScope:
		return &struct {
			*workspaceConfinedLockerScope
			*workspaceCustodialEffect
		}{scoped, custody}, nil
	case *workspaceConfinedClockLockerScope:
		return &struct {
			*workspaceConfinedClockLockerScope
			*workspaceCustodialEffect
		}{scoped, custody}, nil
	case *workspaceConfinedAuthorityScope:
		return &struct {
			*workspaceConfinedAuthorityScope
			*workspaceCustodialEffect
		}{scoped, custody}, nil
	case *workspaceConfinedClockAuthorityScope:
		return &struct {
			*workspaceConfinedClockAuthorityScope
			*workspaceCustodialEffect
		}{scoped, custody}, nil
	case *workspaceConfinedLockerAuthorityScope:
		return &struct {
			*workspaceConfinedLockerAuthorityScope
			*workspaceCustodialEffect
		}{scoped, custody}, nil
	case *workspaceConfinedClockLockerAuthorityScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityScope
			*workspaceCustodialEffect
		}{scoped, custody}, nil
	case *workspaceConfinedDirectoryScope:
		return &struct {
			*workspaceConfinedDirectoryScope
			*workspaceCustodialEffect
		}{scoped, custody}, nil
	case *workspaceConfinedClockDirectoryScope:
		return &struct {
			*workspaceConfinedClockDirectoryScope
			*workspaceCustodialEffect
		}{scoped, custody}, nil
	case *workspaceConfinedLockerDirectoryScope:
		return &struct {
			*workspaceConfinedLockerDirectoryScope
			*workspaceCustodialEffect
		}{scoped, custody}, nil
	case *workspaceConfinedClockLockerDirectoryScope:
		return &struct {
			*workspaceConfinedClockLockerDirectoryScope
			*workspaceCustodialEffect
		}{scoped, custody}, nil
	case *workspaceConfinedAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedAuthorityDirectoryScope
			*workspaceCustodialEffect
		}{scoped, custody}, nil
	case *workspaceConfinedClockAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedClockAuthorityDirectoryScope
			*workspaceCustodialEffect
		}{scoped, custody}, nil
	case *workspaceConfinedLockerAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedLockerAuthorityDirectoryScope
			*workspaceCustodialEffect
		}{scoped, custody}, nil
	case *workspaceConfinedClockLockerAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityDirectoryScope
			*workspaceCustodialEffect
		}{scoped, custody}, nil
	case *workspaceConfinedAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedAuthorizationEpochScope
			*workspaceCustodialEffect
		}{scoped, custody}, nil
	case *workspaceConfinedClockAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockAuthorizationEpochScope
			*workspaceCustodialEffect
		}{scoped, custody}, nil
	case *workspaceConfinedLockerAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerAuthorizationEpochScope
			*workspaceCustodialEffect
		}{scoped, custody}, nil
	case *workspaceConfinedClockLockerAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorizationEpochScope
			*workspaceCustodialEffect
		}{scoped, custody}, nil
	case *workspaceConfinedAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedAuthorityAuthorizationEpochScope
			*workspaceCustodialEffect
		}{scoped, custody}, nil
	case *workspaceConfinedClockAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockAuthorityAuthorizationEpochScope
			*workspaceCustodialEffect
		}{scoped, custody}, nil
	case *workspaceConfinedLockerAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerAuthorityAuthorizationEpochScope
			*workspaceCustodialEffect
		}{scoped, custody}, nil
	case *workspaceConfinedClockLockerAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityAuthorizationEpochScope
			*workspaceCustodialEffect
		}{scoped, custody}, nil
	case *workspaceConfinedDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedDirectoryAuthorizationEpochScope
			*workspaceCustodialEffect
		}{scoped, custody}, nil
	case *workspaceConfinedClockDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockDirectoryAuthorizationEpochScope
			*workspaceCustodialEffect
		}{scoped, custody}, nil
	case *workspaceConfinedLockerDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerDirectoryAuthorizationEpochScope
			*workspaceCustodialEffect
		}{scoped, custody}, nil
	case *workspaceConfinedClockLockerDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerDirectoryAuthorizationEpochScope
			*workspaceCustodialEffect
		}{scoped, custody}, nil
	case *workspaceConfinedAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedAuthorityDirectoryAuthorizationEpochScope
			*workspaceCustodialEffect
		}{scoped, custody}, nil
	case *workspaceConfinedClockAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockAuthorityDirectoryAuthorizationEpochScope
			*workspaceCustodialEffect
		}{scoped, custody}, nil
	case *workspaceConfinedLockerAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerAuthorityDirectoryAuthorizationEpochScope
			*workspaceCustodialEffect
		}{scoped, custody}, nil
	case *workspaceConfinedClockLockerAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityDirectoryAuthorizationEpochScope
			*workspaceCustodialEffect
		}{scoped, custody}, nil
	default:
		return nil, ErrWorkspaceConfinement
	}
}

// forwardWorkspaceBundleCustody attaches the bundle locker and custody.
func forwardWorkspaceBundleCustody(confined Scope, p0 *workspaceAuthorityBundle, custody *workspaceCustodialEffect) (Scope, error) {
	switch scoped := confined.(type) {
	case *workspaceConfinedScope:
		return &struct {
			*workspaceConfinedScope
			*workspaceAuthorityBundle
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedClockScope:
		return &struct {
			*workspaceConfinedClockScope
			*workspaceAuthorityBundle
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedLockerScope:
		return &struct {
			*workspaceConfinedLockerScope
			*workspaceAuthorityBundle
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedClockLockerScope:
		return &struct {
			*workspaceConfinedClockLockerScope
			*workspaceAuthorityBundle
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedAuthorityScope:
		return &struct {
			*workspaceConfinedAuthorityScope
			*workspaceAuthorityBundle
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedClockAuthorityScope:
		return &struct {
			*workspaceConfinedClockAuthorityScope
			*workspaceAuthorityBundle
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedLockerAuthorityScope:
		return &struct {
			*workspaceConfinedLockerAuthorityScope
			*workspaceAuthorityBundle
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedClockLockerAuthorityScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityScope
			*workspaceAuthorityBundle
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedDirectoryScope:
		return &struct {
			*workspaceConfinedDirectoryScope
			*workspaceAuthorityBundle
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedClockDirectoryScope:
		return &struct {
			*workspaceConfinedClockDirectoryScope
			*workspaceAuthorityBundle
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedLockerDirectoryScope:
		return &struct {
			*workspaceConfinedLockerDirectoryScope
			*workspaceAuthorityBundle
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedClockLockerDirectoryScope:
		return &struct {
			*workspaceConfinedClockLockerDirectoryScope
			*workspaceAuthorityBundle
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedAuthorityDirectoryScope
			*workspaceAuthorityBundle
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedClockAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedClockAuthorityDirectoryScope
			*workspaceAuthorityBundle
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedLockerAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedLockerAuthorityDirectoryScope
			*workspaceAuthorityBundle
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedClockLockerAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityDirectoryScope
			*workspaceAuthorityBundle
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedClockAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedLockerAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedClockLockerAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedAuthorityAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedClockAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockAuthorityAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedLockerAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerAuthorityAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedClockLockerAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedClockDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedLockerDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedClockLockerDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedAuthorityDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedClockAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockAuthorityDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedLockerAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerAuthorityDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedClockLockerAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	default:
		return nil, ErrWorkspaceConfinement
	}
}

// forwardWorkspaceDirectoryCustody attaches the directory-authority locker
// and custody.
func forwardWorkspaceDirectoryCustody(confined Scope, p0 *workspaceDirectoryAuthority, custody *workspaceCustodialEffect) (Scope, error) {
	switch scoped := confined.(type) {
	case *workspaceConfinedScope:
		return &struct {
			*workspaceConfinedScope
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedClockScope:
		return &struct {
			*workspaceConfinedClockScope
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedLockerScope:
		return &struct {
			*workspaceConfinedLockerScope
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedClockLockerScope:
		return &struct {
			*workspaceConfinedClockLockerScope
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedAuthorityScope:
		return &struct {
			*workspaceConfinedAuthorityScope
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedClockAuthorityScope:
		return &struct {
			*workspaceConfinedClockAuthorityScope
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedLockerAuthorityScope:
		return &struct {
			*workspaceConfinedLockerAuthorityScope
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedClockLockerAuthorityScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityScope
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedDirectoryScope:
		return &struct {
			*workspaceConfinedDirectoryScope
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedClockDirectoryScope:
		return &struct {
			*workspaceConfinedClockDirectoryScope
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedLockerDirectoryScope:
		return &struct {
			*workspaceConfinedLockerDirectoryScope
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedClockLockerDirectoryScope:
		return &struct {
			*workspaceConfinedClockLockerDirectoryScope
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedAuthorityDirectoryScope
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedClockAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedClockAuthorityDirectoryScope
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedLockerAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedLockerAuthorityDirectoryScope
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedClockLockerAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityDirectoryScope
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedClockAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedLockerAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedClockLockerAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedAuthorityAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedClockAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockAuthorityAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedLockerAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerAuthorityAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedClockLockerAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedDirectoryAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedClockDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockDirectoryAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedLockerDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerDirectoryAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedClockLockerDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerDirectoryAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedAuthorityDirectoryAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedClockAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockAuthorityDirectoryAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedLockerAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerAuthorityDirectoryAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	case *workspaceConfinedClockLockerAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityDirectoryAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, custody}, nil
	default:
		return nil, ErrWorkspaceConfinement
	}
}

// forwardWorkspaceBundleDirectoryCustody attaches both authority lockers and
// custody. Every ordinary SQL tenantScope without a bounded-read factory is in
// this case.
func forwardWorkspaceBundleDirectoryCustody(confined Scope, p0 *workspaceAuthorityBundle, p1 *workspaceDirectoryAuthority, custody *workspaceCustodialEffect) (Scope, error) {
	switch scoped := confined.(type) {
	case *workspaceConfinedScope:
		return &struct {
			*workspaceConfinedScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, p1, custody}, nil
	case *workspaceConfinedClockScope:
		return &struct {
			*workspaceConfinedClockScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, p1, custody}, nil
	case *workspaceConfinedLockerScope:
		return &struct {
			*workspaceConfinedLockerScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, p1, custody}, nil
	case *workspaceConfinedClockLockerScope:
		return &struct {
			*workspaceConfinedClockLockerScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, p1, custody}, nil
	case *workspaceConfinedAuthorityScope:
		return &struct {
			*workspaceConfinedAuthorityScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, p1, custody}, nil
	case *workspaceConfinedClockAuthorityScope:
		return &struct {
			*workspaceConfinedClockAuthorityScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, p1, custody}, nil
	case *workspaceConfinedLockerAuthorityScope:
		return &struct {
			*workspaceConfinedLockerAuthorityScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, p1, custody}, nil
	case *workspaceConfinedClockLockerAuthorityScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, p1, custody}, nil
	case *workspaceConfinedDirectoryScope:
		return &struct {
			*workspaceConfinedDirectoryScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, p1, custody}, nil
	case *workspaceConfinedClockDirectoryScope:
		return &struct {
			*workspaceConfinedClockDirectoryScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, p1, custody}, nil
	case *workspaceConfinedLockerDirectoryScope:
		return &struct {
			*workspaceConfinedLockerDirectoryScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, p1, custody}, nil
	case *workspaceConfinedClockLockerDirectoryScope:
		return &struct {
			*workspaceConfinedClockLockerDirectoryScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, p1, custody}, nil
	case *workspaceConfinedAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedAuthorityDirectoryScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, p1, custody}, nil
	case *workspaceConfinedClockAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedClockAuthorityDirectoryScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, p1, custody}, nil
	case *workspaceConfinedLockerAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedLockerAuthorityDirectoryScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, p1, custody}, nil
	case *workspaceConfinedClockLockerAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityDirectoryScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, p1, custody}, nil
	case *workspaceConfinedAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, p1, custody}, nil
	case *workspaceConfinedClockAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, p1, custody}, nil
	case *workspaceConfinedLockerAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, p1, custody}, nil
	case *workspaceConfinedClockLockerAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, p1, custody}, nil
	case *workspaceConfinedAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedAuthorityAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, p1, custody}, nil
	case *workspaceConfinedClockAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockAuthorityAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, p1, custody}, nil
	case *workspaceConfinedLockerAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerAuthorityAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, p1, custody}, nil
	case *workspaceConfinedClockLockerAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, p1, custody}, nil
	case *workspaceConfinedDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, p1, custody}, nil
	case *workspaceConfinedClockDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, p1, custody}, nil
	case *workspaceConfinedLockerDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, p1, custody}, nil
	case *workspaceConfinedClockLockerDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, p1, custody}, nil
	case *workspaceConfinedAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedAuthorityDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, p1, custody}, nil
	case *workspaceConfinedClockAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockAuthorityDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, p1, custody}, nil
	case *workspaceConfinedLockerAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerAuthorityDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, p1, custody}, nil
	case *workspaceConfinedClockLockerAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceCustodialEffect
		}{scoped, p0, p1, custody}, nil
	default:
		return nil, ErrWorkspaceConfinement
	}
}

// forwardWorkspaceBoundedReaderCustody attaches the bounded-read factory and
// custody.
func forwardWorkspaceBoundedReaderCustody(confined Scope, reader *workspaceBoundedReaderFactory, custody *workspaceCustodialEffect) (Scope, error) {
	switch scoped := confined.(type) {
	case *workspaceConfinedScope:
		return &struct {
			*workspaceConfinedScope
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, reader, custody}, nil
	case *workspaceConfinedClockScope:
		return &struct {
			*workspaceConfinedClockScope
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, reader, custody}, nil
	case *workspaceConfinedLockerScope:
		return &struct {
			*workspaceConfinedLockerScope
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, reader, custody}, nil
	case *workspaceConfinedClockLockerScope:
		return &struct {
			*workspaceConfinedClockLockerScope
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, reader, custody}, nil
	case *workspaceConfinedAuthorityScope:
		return &struct {
			*workspaceConfinedAuthorityScope
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, reader, custody}, nil
	case *workspaceConfinedClockAuthorityScope:
		return &struct {
			*workspaceConfinedClockAuthorityScope
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, reader, custody}, nil
	case *workspaceConfinedLockerAuthorityScope:
		return &struct {
			*workspaceConfinedLockerAuthorityScope
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, reader, custody}, nil
	case *workspaceConfinedClockLockerAuthorityScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityScope
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, reader, custody}, nil
	case *workspaceConfinedDirectoryScope:
		return &struct {
			*workspaceConfinedDirectoryScope
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, reader, custody}, nil
	case *workspaceConfinedClockDirectoryScope:
		return &struct {
			*workspaceConfinedClockDirectoryScope
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, reader, custody}, nil
	case *workspaceConfinedLockerDirectoryScope:
		return &struct {
			*workspaceConfinedLockerDirectoryScope
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, reader, custody}, nil
	case *workspaceConfinedClockLockerDirectoryScope:
		return &struct {
			*workspaceConfinedClockLockerDirectoryScope
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, reader, custody}, nil
	case *workspaceConfinedAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedAuthorityDirectoryScope
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, reader, custody}, nil
	case *workspaceConfinedClockAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedClockAuthorityDirectoryScope
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, reader, custody}, nil
	case *workspaceConfinedLockerAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedLockerAuthorityDirectoryScope
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, reader, custody}, nil
	case *workspaceConfinedClockLockerAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityDirectoryScope
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, reader, custody}, nil
	case *workspaceConfinedAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedAuthorizationEpochScope
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, reader, custody}, nil
	case *workspaceConfinedClockAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockAuthorizationEpochScope
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, reader, custody}, nil
	case *workspaceConfinedLockerAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerAuthorizationEpochScope
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, reader, custody}, nil
	case *workspaceConfinedClockLockerAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorizationEpochScope
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, reader, custody}, nil
	case *workspaceConfinedAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedAuthorityAuthorizationEpochScope
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, reader, custody}, nil
	case *workspaceConfinedClockAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockAuthorityAuthorizationEpochScope
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, reader, custody}, nil
	case *workspaceConfinedLockerAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerAuthorityAuthorizationEpochScope
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, reader, custody}, nil
	case *workspaceConfinedClockLockerAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityAuthorizationEpochScope
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, reader, custody}, nil
	case *workspaceConfinedDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedDirectoryAuthorizationEpochScope
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, reader, custody}, nil
	case *workspaceConfinedClockDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockDirectoryAuthorizationEpochScope
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, reader, custody}, nil
	case *workspaceConfinedLockerDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerDirectoryAuthorizationEpochScope
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, reader, custody}, nil
	case *workspaceConfinedClockLockerDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerDirectoryAuthorizationEpochScope
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, reader, custody}, nil
	case *workspaceConfinedAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedAuthorityDirectoryAuthorizationEpochScope
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, reader, custody}, nil
	case *workspaceConfinedClockAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockAuthorityDirectoryAuthorizationEpochScope
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, reader, custody}, nil
	case *workspaceConfinedLockerAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerAuthorityDirectoryAuthorizationEpochScope
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, reader, custody}, nil
	case *workspaceConfinedClockLockerAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityDirectoryAuthorizationEpochScope
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, reader, custody}, nil
	default:
		return nil, ErrWorkspaceConfinement
	}
}

// forwardWorkspaceBundleBoundedReaderCustody attaches the bundle locker, the
// bounded-read factory and custody.
func forwardWorkspaceBundleBoundedReaderCustody(confined Scope, p0 *workspaceAuthorityBundle, reader *workspaceBoundedReaderFactory, custody *workspaceCustodialEffect) (Scope, error) {
	switch scoped := confined.(type) {
	case *workspaceConfinedScope:
		return &struct {
			*workspaceConfinedScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedClockScope:
		return &struct {
			*workspaceConfinedClockScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedLockerScope:
		return &struct {
			*workspaceConfinedLockerScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedClockLockerScope:
		return &struct {
			*workspaceConfinedClockLockerScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedAuthorityScope:
		return &struct {
			*workspaceConfinedAuthorityScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedClockAuthorityScope:
		return &struct {
			*workspaceConfinedClockAuthorityScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedLockerAuthorityScope:
		return &struct {
			*workspaceConfinedLockerAuthorityScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedClockLockerAuthorityScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedDirectoryScope:
		return &struct {
			*workspaceConfinedDirectoryScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedClockDirectoryScope:
		return &struct {
			*workspaceConfinedClockDirectoryScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedLockerDirectoryScope:
		return &struct {
			*workspaceConfinedLockerDirectoryScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedClockLockerDirectoryScope:
		return &struct {
			*workspaceConfinedClockLockerDirectoryScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedAuthorityDirectoryScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedClockAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedClockAuthorityDirectoryScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedLockerAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedLockerAuthorityDirectoryScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedClockLockerAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityDirectoryScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedClockAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedLockerAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedClockLockerAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedAuthorityAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedClockAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockAuthorityAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedLockerAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerAuthorityAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedClockLockerAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedClockDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedLockerDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedClockLockerDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedAuthorityDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedClockAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockAuthorityDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedLockerAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerAuthorityDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedClockLockerAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	default:
		return nil, ErrWorkspaceConfinement
	}
}

// forwardWorkspaceDirectoryBoundedReaderCustody attaches the
// directory-authority locker, the bounded-read factory and custody.
func forwardWorkspaceDirectoryBoundedReaderCustody(confined Scope, p0 *workspaceDirectoryAuthority, reader *workspaceBoundedReaderFactory, custody *workspaceCustodialEffect) (Scope, error) {
	switch scoped := confined.(type) {
	case *workspaceConfinedScope:
		return &struct {
			*workspaceConfinedScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedClockScope:
		return &struct {
			*workspaceConfinedClockScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedLockerScope:
		return &struct {
			*workspaceConfinedLockerScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedClockLockerScope:
		return &struct {
			*workspaceConfinedClockLockerScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedAuthorityScope:
		return &struct {
			*workspaceConfinedAuthorityScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedClockAuthorityScope:
		return &struct {
			*workspaceConfinedClockAuthorityScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedLockerAuthorityScope:
		return &struct {
			*workspaceConfinedLockerAuthorityScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedClockLockerAuthorityScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedDirectoryScope:
		return &struct {
			*workspaceConfinedDirectoryScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedClockDirectoryScope:
		return &struct {
			*workspaceConfinedClockDirectoryScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedLockerDirectoryScope:
		return &struct {
			*workspaceConfinedLockerDirectoryScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedClockLockerDirectoryScope:
		return &struct {
			*workspaceConfinedClockLockerDirectoryScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedAuthorityDirectoryScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedClockAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedClockAuthorityDirectoryScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedLockerAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedLockerAuthorityDirectoryScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedClockLockerAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityDirectoryScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedClockAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedLockerAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedClockLockerAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedAuthorityAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedClockAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockAuthorityAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedLockerAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerAuthorityAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedClockLockerAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedDirectoryAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedClockDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockDirectoryAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedLockerDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerDirectoryAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedClockLockerDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerDirectoryAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedAuthorityDirectoryAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedClockAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockAuthorityDirectoryAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedLockerAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerAuthorityDirectoryAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	case *workspaceConfinedClockLockerAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityDirectoryAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, reader, custody}, nil
	default:
		return nil, ErrWorkspaceConfinement
	}
}

// forwardWorkspaceBundleDirectoryBoundedReaderCustody attaches the COMPLETE
// four-port set. Every ordinary SQL tenantScope of a store that exposes the
// bounded-read factory and a valid custodial relation is in this case.
func forwardWorkspaceBundleDirectoryBoundedReaderCustody(confined Scope, p0 *workspaceAuthorityBundle, p1 *workspaceDirectoryAuthority, reader *workspaceBoundedReaderFactory, custody *workspaceCustodialEffect) (Scope, error) {
	switch scoped := confined.(type) {
	case *workspaceConfinedScope:
		return &struct {
			*workspaceConfinedScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, p1, reader, custody}, nil
	case *workspaceConfinedClockScope:
		return &struct {
			*workspaceConfinedClockScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, p1, reader, custody}, nil
	case *workspaceConfinedLockerScope:
		return &struct {
			*workspaceConfinedLockerScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, p1, reader, custody}, nil
	case *workspaceConfinedClockLockerScope:
		return &struct {
			*workspaceConfinedClockLockerScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, p1, reader, custody}, nil
	case *workspaceConfinedAuthorityScope:
		return &struct {
			*workspaceConfinedAuthorityScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, p1, reader, custody}, nil
	case *workspaceConfinedClockAuthorityScope:
		return &struct {
			*workspaceConfinedClockAuthorityScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, p1, reader, custody}, nil
	case *workspaceConfinedLockerAuthorityScope:
		return &struct {
			*workspaceConfinedLockerAuthorityScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, p1, reader, custody}, nil
	case *workspaceConfinedClockLockerAuthorityScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, p1, reader, custody}, nil
	case *workspaceConfinedDirectoryScope:
		return &struct {
			*workspaceConfinedDirectoryScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, p1, reader, custody}, nil
	case *workspaceConfinedClockDirectoryScope:
		return &struct {
			*workspaceConfinedClockDirectoryScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, p1, reader, custody}, nil
	case *workspaceConfinedLockerDirectoryScope:
		return &struct {
			*workspaceConfinedLockerDirectoryScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, p1, reader, custody}, nil
	case *workspaceConfinedClockLockerDirectoryScope:
		return &struct {
			*workspaceConfinedClockLockerDirectoryScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, p1, reader, custody}, nil
	case *workspaceConfinedAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedAuthorityDirectoryScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, p1, reader, custody}, nil
	case *workspaceConfinedClockAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedClockAuthorityDirectoryScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, p1, reader, custody}, nil
	case *workspaceConfinedLockerAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedLockerAuthorityDirectoryScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, p1, reader, custody}, nil
	case *workspaceConfinedClockLockerAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityDirectoryScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, p1, reader, custody}, nil
	case *workspaceConfinedAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, p1, reader, custody}, nil
	case *workspaceConfinedClockAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, p1, reader, custody}, nil
	case *workspaceConfinedLockerAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, p1, reader, custody}, nil
	case *workspaceConfinedClockLockerAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, p1, reader, custody}, nil
	case *workspaceConfinedAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedAuthorityAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, p1, reader, custody}, nil
	case *workspaceConfinedClockAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockAuthorityAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, p1, reader, custody}, nil
	case *workspaceConfinedLockerAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerAuthorityAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, p1, reader, custody}, nil
	case *workspaceConfinedClockLockerAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, p1, reader, custody}, nil
	case *workspaceConfinedDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, p1, reader, custody}, nil
	case *workspaceConfinedClockDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, p1, reader, custody}, nil
	case *workspaceConfinedLockerDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, p1, reader, custody}, nil
	case *workspaceConfinedClockLockerDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, p1, reader, custody}, nil
	case *workspaceConfinedAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedAuthorityDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, p1, reader, custody}, nil
	case *workspaceConfinedClockAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockAuthorityDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, p1, reader, custody}, nil
	case *workspaceConfinedLockerAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerAuthorityDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, p1, reader, custody}, nil
	case *workspaceConfinedClockLockerAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
			*workspaceCustodialEffect
		}{scoped, p0, p1, reader, custody}, nil
	default:
		return nil, ErrWorkspaceConfinement
	}
}
