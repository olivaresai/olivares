// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

// The bounded-read factory is the eighth optional capability bit of a confined
// Scope. It is attached by the SAME single final step as the bundle and DAB1
// ports (workspace_directory_authority.go): the port set is chosen from the RAW
// scope and attached to the named decorator by exactly one switch, so no
// method set is lost and nothing is promoted. The four sets below are the
// factory crossed with the existing three port sets plus none; each switch
// lists the same 32 named decorators.

// workspaceBoundaryHolder is promoted from workspaceConfinedScope through every
// named decorator. A confined scope without it cannot carry a bounded reader.
type workspaceBoundaryHolder interface {
	confinedBoundary() workspaceBoundary
}

// forwardWorkspacePortsWithBoundedReader attaches the factory port together
// with exactly the selected bundle and DAB1 ports.
func forwardWorkspacePortsWithBoundedReader(
	confined Scope,
	bundle AuthoritySnapshotBundleLocker, wantBundle bool,
	directory DirectoryAuthoritySnapshotLocker, wantDirectory bool,
	factory BoundedReaderFactory,
) (Scope, error) {
	holder, ok := confined.(workspaceBoundaryHolder)
	if !ok {
		return nil, ErrWorkspaceConfinement
	}
	reader := &workspaceBoundedReaderFactory{factory: factory, boundary: holder.confinedBoundary()}
	switch {
	case wantBundle && wantDirectory:
		return forwardWorkspaceBundleDirectoryBoundedReader(confined,
			&workspaceAuthorityBundle{locker: bundle}, &workspaceDirectoryAuthority{locker: directory}, reader)
	case wantBundle:
		return forwardWorkspaceBundleBoundedReader(confined, &workspaceAuthorityBundle{locker: bundle}, reader)
	case wantDirectory:
		return forwardWorkspaceDirectoryBoundedReader(confined, &workspaceDirectoryAuthority{locker: directory}, reader)
	default:
		return forwardWorkspaceBoundedReader(confined, reader)
	}
}

func forwardWorkspaceBoundedReader(confined Scope, reader *workspaceBoundedReaderFactory) (Scope, error) {
	switch scoped := confined.(type) {
	case *workspaceConfinedScope:
		return &struct {
			*workspaceConfinedScope
			*workspaceBoundedReaderFactory
		}{scoped, reader}, nil
	case *workspaceConfinedClockScope:
		return &struct {
			*workspaceConfinedClockScope
			*workspaceBoundedReaderFactory
		}{scoped, reader}, nil
	case *workspaceConfinedLockerScope:
		return &struct {
			*workspaceConfinedLockerScope
			*workspaceBoundedReaderFactory
		}{scoped, reader}, nil
	case *workspaceConfinedClockLockerScope:
		return &struct {
			*workspaceConfinedClockLockerScope
			*workspaceBoundedReaderFactory
		}{scoped, reader}, nil
	case *workspaceConfinedAuthorityScope:
		return &struct {
			*workspaceConfinedAuthorityScope
			*workspaceBoundedReaderFactory
		}{scoped, reader}, nil
	case *workspaceConfinedClockAuthorityScope:
		return &struct {
			*workspaceConfinedClockAuthorityScope
			*workspaceBoundedReaderFactory
		}{scoped, reader}, nil
	case *workspaceConfinedLockerAuthorityScope:
		return &struct {
			*workspaceConfinedLockerAuthorityScope
			*workspaceBoundedReaderFactory
		}{scoped, reader}, nil
	case *workspaceConfinedClockLockerAuthorityScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityScope
			*workspaceBoundedReaderFactory
		}{scoped, reader}, nil
	case *workspaceConfinedDirectoryScope:
		return &struct {
			*workspaceConfinedDirectoryScope
			*workspaceBoundedReaderFactory
		}{scoped, reader}, nil
	case *workspaceConfinedClockDirectoryScope:
		return &struct {
			*workspaceConfinedClockDirectoryScope
			*workspaceBoundedReaderFactory
		}{scoped, reader}, nil
	case *workspaceConfinedLockerDirectoryScope:
		return &struct {
			*workspaceConfinedLockerDirectoryScope
			*workspaceBoundedReaderFactory
		}{scoped, reader}, nil
	case *workspaceConfinedClockLockerDirectoryScope:
		return &struct {
			*workspaceConfinedClockLockerDirectoryScope
			*workspaceBoundedReaderFactory
		}{scoped, reader}, nil
	case *workspaceConfinedAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedAuthorityDirectoryScope
			*workspaceBoundedReaderFactory
		}{scoped, reader}, nil
	case *workspaceConfinedClockAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedClockAuthorityDirectoryScope
			*workspaceBoundedReaderFactory
		}{scoped, reader}, nil
	case *workspaceConfinedLockerAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedLockerAuthorityDirectoryScope
			*workspaceBoundedReaderFactory
		}{scoped, reader}, nil
	case *workspaceConfinedClockLockerAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityDirectoryScope
			*workspaceBoundedReaderFactory
		}{scoped, reader}, nil
	case *workspaceConfinedAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedAuthorizationEpochScope
			*workspaceBoundedReaderFactory
		}{scoped, reader}, nil
	case *workspaceConfinedClockAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockAuthorizationEpochScope
			*workspaceBoundedReaderFactory
		}{scoped, reader}, nil
	case *workspaceConfinedLockerAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerAuthorizationEpochScope
			*workspaceBoundedReaderFactory
		}{scoped, reader}, nil
	case *workspaceConfinedClockLockerAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorizationEpochScope
			*workspaceBoundedReaderFactory
		}{scoped, reader}, nil
	case *workspaceConfinedAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedAuthorityAuthorizationEpochScope
			*workspaceBoundedReaderFactory
		}{scoped, reader}, nil
	case *workspaceConfinedClockAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockAuthorityAuthorizationEpochScope
			*workspaceBoundedReaderFactory
		}{scoped, reader}, nil
	case *workspaceConfinedLockerAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerAuthorityAuthorizationEpochScope
			*workspaceBoundedReaderFactory
		}{scoped, reader}, nil
	case *workspaceConfinedClockLockerAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityAuthorizationEpochScope
			*workspaceBoundedReaderFactory
		}{scoped, reader}, nil
	case *workspaceConfinedDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedDirectoryAuthorizationEpochScope
			*workspaceBoundedReaderFactory
		}{scoped, reader}, nil
	case *workspaceConfinedClockDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockDirectoryAuthorizationEpochScope
			*workspaceBoundedReaderFactory
		}{scoped, reader}, nil
	case *workspaceConfinedLockerDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerDirectoryAuthorizationEpochScope
			*workspaceBoundedReaderFactory
		}{scoped, reader}, nil
	case *workspaceConfinedClockLockerDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerDirectoryAuthorizationEpochScope
			*workspaceBoundedReaderFactory
		}{scoped, reader}, nil
	case *workspaceConfinedAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedAuthorityDirectoryAuthorizationEpochScope
			*workspaceBoundedReaderFactory
		}{scoped, reader}, nil
	case *workspaceConfinedClockAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockAuthorityDirectoryAuthorizationEpochScope
			*workspaceBoundedReaderFactory
		}{scoped, reader}, nil
	case *workspaceConfinedLockerAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerAuthorityDirectoryAuthorizationEpochScope
			*workspaceBoundedReaderFactory
		}{scoped, reader}, nil
	case *workspaceConfinedClockLockerAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityDirectoryAuthorizationEpochScope
			*workspaceBoundedReaderFactory
		}{scoped, reader}, nil
	default:
		return nil, ErrWorkspaceConfinement
	}
}

func forwardWorkspaceBundleBoundedReader(confined Scope, p0 *workspaceAuthorityBundle, reader *workspaceBoundedReaderFactory) (Scope, error) {
	switch scoped := confined.(type) {
	case *workspaceConfinedScope:
		return &struct {
			*workspaceConfinedScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedClockScope:
		return &struct {
			*workspaceConfinedClockScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedLockerScope:
		return &struct {
			*workspaceConfinedLockerScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedClockLockerScope:
		return &struct {
			*workspaceConfinedClockLockerScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedAuthorityScope:
		return &struct {
			*workspaceConfinedAuthorityScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedClockAuthorityScope:
		return &struct {
			*workspaceConfinedClockAuthorityScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedLockerAuthorityScope:
		return &struct {
			*workspaceConfinedLockerAuthorityScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedClockLockerAuthorityScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedDirectoryScope:
		return &struct {
			*workspaceConfinedDirectoryScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedClockDirectoryScope:
		return &struct {
			*workspaceConfinedClockDirectoryScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedLockerDirectoryScope:
		return &struct {
			*workspaceConfinedLockerDirectoryScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedClockLockerDirectoryScope:
		return &struct {
			*workspaceConfinedClockLockerDirectoryScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedAuthorityDirectoryScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedClockAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedClockAuthorityDirectoryScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedLockerAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedLockerAuthorityDirectoryScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedClockLockerAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityDirectoryScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedClockAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedLockerAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedClockLockerAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedAuthorityAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedClockAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockAuthorityAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedLockerAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerAuthorityAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedClockLockerAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedClockDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedLockerDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedClockLockerDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedAuthorityDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedClockAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockAuthorityDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedLockerAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerAuthorityDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedClockLockerAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	default:
		return nil, ErrWorkspaceConfinement
	}
}

func forwardWorkspaceDirectoryBoundedReader(confined Scope, p0 *workspaceDirectoryAuthority, reader *workspaceBoundedReaderFactory) (Scope, error) {
	switch scoped := confined.(type) {
	case *workspaceConfinedScope:
		return &struct {
			*workspaceConfinedScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedClockScope:
		return &struct {
			*workspaceConfinedClockScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedLockerScope:
		return &struct {
			*workspaceConfinedLockerScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedClockLockerScope:
		return &struct {
			*workspaceConfinedClockLockerScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedAuthorityScope:
		return &struct {
			*workspaceConfinedAuthorityScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedClockAuthorityScope:
		return &struct {
			*workspaceConfinedClockAuthorityScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedLockerAuthorityScope:
		return &struct {
			*workspaceConfinedLockerAuthorityScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedClockLockerAuthorityScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedDirectoryScope:
		return &struct {
			*workspaceConfinedDirectoryScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedClockDirectoryScope:
		return &struct {
			*workspaceConfinedClockDirectoryScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedLockerDirectoryScope:
		return &struct {
			*workspaceConfinedLockerDirectoryScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedClockLockerDirectoryScope:
		return &struct {
			*workspaceConfinedClockLockerDirectoryScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedAuthorityDirectoryScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedClockAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedClockAuthorityDirectoryScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedLockerAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedLockerAuthorityDirectoryScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedClockLockerAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityDirectoryScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedClockAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedLockerAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedClockLockerAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedAuthorityAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedClockAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockAuthorityAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedLockerAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerAuthorityAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedClockLockerAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedDirectoryAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedClockDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockDirectoryAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedLockerDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerDirectoryAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedClockLockerDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerDirectoryAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedAuthorityDirectoryAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedClockAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockAuthorityDirectoryAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedLockerAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerAuthorityDirectoryAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	case *workspaceConfinedClockLockerAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityDirectoryAuthorizationEpochScope
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, reader}, nil
	default:
		return nil, ErrWorkspaceConfinement
	}
}

func forwardWorkspaceBundleDirectoryBoundedReader(confined Scope, p0 *workspaceAuthorityBundle, p1 *workspaceDirectoryAuthority, reader *workspaceBoundedReaderFactory) (Scope, error) {
	switch scoped := confined.(type) {
	case *workspaceConfinedScope:
		return &struct {
			*workspaceConfinedScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, p1, reader}, nil
	case *workspaceConfinedClockScope:
		return &struct {
			*workspaceConfinedClockScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, p1, reader}, nil
	case *workspaceConfinedLockerScope:
		return &struct {
			*workspaceConfinedLockerScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, p1, reader}, nil
	case *workspaceConfinedClockLockerScope:
		return &struct {
			*workspaceConfinedClockLockerScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, p1, reader}, nil
	case *workspaceConfinedAuthorityScope:
		return &struct {
			*workspaceConfinedAuthorityScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, p1, reader}, nil
	case *workspaceConfinedClockAuthorityScope:
		return &struct {
			*workspaceConfinedClockAuthorityScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, p1, reader}, nil
	case *workspaceConfinedLockerAuthorityScope:
		return &struct {
			*workspaceConfinedLockerAuthorityScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, p1, reader}, nil
	case *workspaceConfinedClockLockerAuthorityScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, p1, reader}, nil
	case *workspaceConfinedDirectoryScope:
		return &struct {
			*workspaceConfinedDirectoryScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, p1, reader}, nil
	case *workspaceConfinedClockDirectoryScope:
		return &struct {
			*workspaceConfinedClockDirectoryScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, p1, reader}, nil
	case *workspaceConfinedLockerDirectoryScope:
		return &struct {
			*workspaceConfinedLockerDirectoryScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, p1, reader}, nil
	case *workspaceConfinedClockLockerDirectoryScope:
		return &struct {
			*workspaceConfinedClockLockerDirectoryScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, p1, reader}, nil
	case *workspaceConfinedAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedAuthorityDirectoryScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, p1, reader}, nil
	case *workspaceConfinedClockAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedClockAuthorityDirectoryScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, p1, reader}, nil
	case *workspaceConfinedLockerAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedLockerAuthorityDirectoryScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, p1, reader}, nil
	case *workspaceConfinedClockLockerAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityDirectoryScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, p1, reader}, nil
	case *workspaceConfinedAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, p1, reader}, nil
	case *workspaceConfinedClockAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, p1, reader}, nil
	case *workspaceConfinedLockerAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, p1, reader}, nil
	case *workspaceConfinedClockLockerAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, p1, reader}, nil
	case *workspaceConfinedAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedAuthorityAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, p1, reader}, nil
	case *workspaceConfinedClockAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockAuthorityAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, p1, reader}, nil
	case *workspaceConfinedLockerAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerAuthorityAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, p1, reader}, nil
	case *workspaceConfinedClockLockerAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, p1, reader}, nil
	case *workspaceConfinedDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, p1, reader}, nil
	case *workspaceConfinedClockDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, p1, reader}, nil
	case *workspaceConfinedLockerDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, p1, reader}, nil
	case *workspaceConfinedClockLockerDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, p1, reader}, nil
	case *workspaceConfinedAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedAuthorityDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, p1, reader}, nil
	case *workspaceConfinedClockAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockAuthorityDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, p1, reader}, nil
	case *workspaceConfinedLockerAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerAuthorityDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, p1, reader}, nil
	case *workspaceConfinedClockLockerAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
			*workspaceDirectoryAuthority
			*workspaceBoundedReaderFactory
		}{scoped, p0, p1, reader}, nil
	default:
		return nil, ErrWorkspaceConfinement
	}
}
