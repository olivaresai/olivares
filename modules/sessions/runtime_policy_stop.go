// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import "github.com/olivaresai/olivares/core/auth"

// StopSessionForPolicy ends the supervised run the session credential belongs to, for a
// reason the hook PEP's content inspection decided (the Business decoy credentials, an
// operator who chose to stop a session that touches one). detail is the ledger's note of
// why and must not carry a matched value. It is the same stop the duration ceiling and
// the denial budget use; the principal must be this run's current session credential,
// and a run somebody else already ended is left alone.
//
// The stop is handled off the caller's path: the verdict the agent is waiting for must
// not wait for the child to exit.
func (m *Module) StopSessionForPolicy(principal auth.Principal, detail string) error {
	tenant := principal.SessionScope()
	if tenant.IsZero() || tenant.IsSystem() || !principal.IsMember(tenant) || principal.SessionIdentity == "" || principal.SessionRunRef == "" || principal.SessionFence < 1 {
		return auth.ErrUnauthenticated
	}
	lr, ok := m.rt.getLive(tenant, principal.SessionRunRef)
	if !ok || lr.claim.SID != principal.SessionIdentity || lr.claim.Fence != principal.SessionFence {
		return auth.ErrUnauthenticated
	}
	go m.stopOwnedRun(lr, detail)
	return nil
}
