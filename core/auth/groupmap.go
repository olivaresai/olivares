// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import "github.com/olivaresai/olivares/core/model"

// GroupMapper is the RESERVED ENTERPRISE capability (U2): resolving the
// directory groups an IdP asserts at SSO login (FederatedIdentity.Groups) to the
// tenant's own directory groups, so login-driven group membership lights up the
// group→role (UserGroup.MappedRole) and group-subject (S256) machinery that
// already exists open-core. It is injected ONLY by the enterprise composition
// root (enterprise/federation, under -tags enterprise); it is nil in the base
// AGPL build, which is what enforces the honest cap — the open binary literally
// has no code to turn an asserted group into a grant (the GitLab ee/ seam, not a
// flag that disables linked code). Symmetric with MultiIDP: the open build
// CARRIES the asserted groups (open-core extraction U1) but never MAPS them.
//
// The capability is deliberately PURE — a selector over data the open core loads,
// exactly like MultiIDP.SelectActive. The Authenticator (open-core) owns the I/O:
// it loads the tenant's directory groups, asks the mapper which the login's
// asserted groups correspond to, and reconciles the user's membership rows. The
// enterprise module owns only the MATCHING (the capability the open build lacks),
// never a Store — credentials stay unreachable from module/commercial code.
type GroupMapper interface {
	// MapAssertedGroups returns the ids of `groups` (the tenant's directory groups)
	// that the login's `asserted` IdP group identifiers correspond to. A group WITH
	// an ExternalID (the IdP's stable correlation key) matches ONLY by that key
	// (exact); a group WITHOUT one falls back to a case-insensitive DisplayName
	// match — mirroring how the open-core SCIM group provider correlates. A keyed
	// group is never matched by its DisplayName, because DisplayName is not unique
	// (Entra provisions duplicate names), so a name assertion can never silently
	// escalate a user into a same-named-but-higher-role group. The result is
	// deduplicated; an asserted identifier with no matching directory group is
	// dropped (the operator provisions the groups they intend to honor — an unknown
	// IdP group grants nothing, deny-closed).
	MapAssertedGroups(asserted []string, groups []model.UserGroup) []model.ID
}
