// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import "context"

// This adapter embeds only reviewed, already-confined concrete scopes. It
// preserves the exact optional capability set without embedding the raw Scope.
type workspaceAuthorityBundle struct{ locker AuthoritySnapshotBundleLocker }

func (s *workspaceAuthorityBundle) LockAuthoritySnapshotBundle(ctx context.Context, bundle AuthoritySnapshotBundle) error {
	return s.locker.LockAuthoritySnapshotBundle(ctx, bundle)
}

// forwardWorkspaceAuthorityBundle attaches the bundle port ALONE. The
// "already forwarded" early return that used to live here became the PER-PORT
// guard in forwardWorkspaceAuthorityPorts, which is the only caller: a guard
// that answered for the bundle alone would drop the DAB1 port on the idempotent
// same-workspace re-confinement path.
func forwardWorkspaceAuthorityBundle(confined Scope, locker AuthoritySnapshotBundleLocker) (Scope, error) {
	port := &workspaceAuthorityBundle{locker: locker}
	switch scoped := confined.(type) {
	case *workspaceConfinedScope:
		return &struct {
			*workspaceConfinedScope
			*workspaceAuthorityBundle
		}{scoped, port}, nil
	case *workspaceConfinedClockScope:
		return &struct {
			*workspaceConfinedClockScope
			*workspaceAuthorityBundle
		}{scoped, port}, nil
	case *workspaceConfinedLockerScope:
		return &struct {
			*workspaceConfinedLockerScope
			*workspaceAuthorityBundle
		}{scoped, port}, nil
	case *workspaceConfinedClockLockerScope:
		return &struct {
			*workspaceConfinedClockLockerScope
			*workspaceAuthorityBundle
		}{scoped, port}, nil
	case *workspaceConfinedAuthorityScope:
		return &struct {
			*workspaceConfinedAuthorityScope
			*workspaceAuthorityBundle
		}{scoped, port}, nil
	case *workspaceConfinedClockAuthorityScope:
		return &struct {
			*workspaceConfinedClockAuthorityScope
			*workspaceAuthorityBundle
		}{scoped, port}, nil
	case *workspaceConfinedLockerAuthorityScope:
		return &struct {
			*workspaceConfinedLockerAuthorityScope
			*workspaceAuthorityBundle
		}{scoped, port}, nil
	case *workspaceConfinedClockLockerAuthorityScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityScope
			*workspaceAuthorityBundle
		}{scoped, port}, nil
	case *workspaceConfinedDirectoryScope:
		return &struct {
			*workspaceConfinedDirectoryScope
			*workspaceAuthorityBundle
		}{scoped, port}, nil
	case *workspaceConfinedClockDirectoryScope:
		return &struct {
			*workspaceConfinedClockDirectoryScope
			*workspaceAuthorityBundle
		}{scoped, port}, nil
	case *workspaceConfinedLockerDirectoryScope:
		return &struct {
			*workspaceConfinedLockerDirectoryScope
			*workspaceAuthorityBundle
		}{scoped, port}, nil
	case *workspaceConfinedClockLockerDirectoryScope:
		return &struct {
			*workspaceConfinedClockLockerDirectoryScope
			*workspaceAuthorityBundle
		}{scoped, port}, nil
	case *workspaceConfinedAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedAuthorityDirectoryScope
			*workspaceAuthorityBundle
		}{scoped, port}, nil
	case *workspaceConfinedClockAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedClockAuthorityDirectoryScope
			*workspaceAuthorityBundle
		}{scoped, port}, nil
	case *workspaceConfinedLockerAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedLockerAuthorityDirectoryScope
			*workspaceAuthorityBundle
		}{scoped, port}, nil
	case *workspaceConfinedClockLockerAuthorityDirectoryScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityDirectoryScope
			*workspaceAuthorityBundle
		}{scoped, port}, nil
	case *workspaceConfinedAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedAuthorizationEpochScope
			*workspaceAuthorityBundle
		}{scoped, port}, nil
	case *workspaceConfinedClockAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockAuthorizationEpochScope
			*workspaceAuthorityBundle
		}{scoped, port}, nil
	case *workspaceConfinedLockerAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerAuthorizationEpochScope
			*workspaceAuthorityBundle
		}{scoped, port}, nil
	case *workspaceConfinedClockLockerAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorizationEpochScope
			*workspaceAuthorityBundle
		}{scoped, port}, nil
	case *workspaceConfinedAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedAuthorityAuthorizationEpochScope
			*workspaceAuthorityBundle
		}{scoped, port}, nil
	case *workspaceConfinedClockAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockAuthorityAuthorizationEpochScope
			*workspaceAuthorityBundle
		}{scoped, port}, nil
	case *workspaceConfinedLockerAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerAuthorityAuthorizationEpochScope
			*workspaceAuthorityBundle
		}{scoped, port}, nil
	case *workspaceConfinedClockLockerAuthorityAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityAuthorizationEpochScope
			*workspaceAuthorityBundle
		}{scoped, port}, nil
	case *workspaceConfinedDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
		}{scoped, port}, nil
	case *workspaceConfinedClockDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
		}{scoped, port}, nil
	case *workspaceConfinedLockerDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
		}{scoped, port}, nil
	case *workspaceConfinedClockLockerDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
		}{scoped, port}, nil
	case *workspaceConfinedAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedAuthorityDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
		}{scoped, port}, nil
	case *workspaceConfinedClockAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockAuthorityDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
		}{scoped, port}, nil
	case *workspaceConfinedLockerAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedLockerAuthorityDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
		}{scoped, port}, nil
	case *workspaceConfinedClockLockerAuthorityDirectoryAuthorizationEpochScope:
		return &struct {
			*workspaceConfinedClockLockerAuthorityDirectoryAuthorizationEpochScope
			*workspaceAuthorityBundle
		}{scoped, port}, nil
	default:
		return nil, ErrWorkspaceConfinement
	}
}
