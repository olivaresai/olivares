// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudewif

import (
	"context"
	"strconv"

	"github.com/olivaresai/olivares/connectors/identitysource"
)

// Identity kinds and resource/collection vocabulary this connector emits. The
// external_id of every row is the RAW Anthropic id (apikey_…, wrkspc_…, user_…,
// svac_…, fdis_…) so it converges with the FinOps/observed side on the same id.
const (
	kindAPIKey         = "api_key"
	kindMember         = "member"
	kindInvite         = "invite"
	kindServiceAccount = "service_account"
	kindIssuer         = "federation_issuer"
)

// Snapshot reads the organization roster (read-only Admin API) and the declared
// federation, and assembles the NHI graph: API keys / service accounts / federation
// issuers as non-human Identities, org members / invites as human Identities,
// workspaces / the organization / workspace-roles / federation-rules as Collections,
// and the belongings between them as Memberships. With no admin credential it returns
// just the declared-federation roster (offline). It never returns a key secret — only
// the masked hint.
func (s *Source) Snapshot(ctx context.Context) (identitysource.Graph, error) {
	g := identitysource.Graph{Source: identitysource.SourceAnthropic, CapturedAt: s.clock().UTC()}

	// Federation NHI (config-driven) is always modeled, even offline.
	s.addFederationRoster(&g)

	if s.adminKey == "" || s.client == nil {
		return g, nil // offline: declared federation only
	}

	// Organization scaffold: an org Collection (if its id is known) that members
	// belong to.
	if s.orgID != "" {
		g.Collections = append(g.Collections, identitysource.Collection{
			Ref: s.orgID, Kind: identitysource.KindGroup, DisplayName: "organization",
			Source:     identitysource.SourceAnthropic,
			Attributes: map[string]string{"object": "organization"},
		})
	}

	users, err := s.fetchUsers(ctx)
	if err != nil {
		return identitysource.Graph{}, err
	}
	for _, u := range users {
		g.Identities = append(g.Identities, identitysource.Identity{
			Ref: u.ID, Type: identitysource.PrincipalHuman, Kind: kindMember,
			DisplayName: nameOrEmail(u.Name, u.Email), Source: identitysource.SourceAnthropic,
			Attributes: pruneAttrs(map[string]string{
				"email": u.Email, "org_role": u.Role, "added_at": u.AddedAt,
			}),
		})
		if s.orgID != "" {
			g.Memberships = append(g.Memberships, identitysource.Membership{
				MemberRef: u.ID, MemberKind: identitysource.MemberIdentity,
				CollectionRef: s.orgID, Source: identitysource.SourceAnthropic,
			})
		}
	}

	invites, err := s.fetchInvites(ctx)
	if err != nil {
		return identitysource.Graph{}, err
	}
	for _, inv := range invites {
		g.Identities = append(g.Identities, identitysource.Identity{
			Ref: inv.ID, Type: identitysource.PrincipalHuman, Kind: kindInvite,
			DisplayName: inv.Email, Source: identitysource.SourceAnthropic,
			Disabled: inv.Status != "pending", // accepted/expired/deleted invites are not live
			Attributes: pruneAttrs(map[string]string{
				"email": inv.Email, "org_role": inv.Role, "status": inv.Status, "expires_at": inv.ExpiresAt,
			}),
		})
	}

	keys, err := s.fetchAPIKeys(ctx)
	if err != nil {
		return identitysource.Graph{}, err
	}
	for _, k := range keys {
		g.Identities = append(g.Identities, identitysource.Identity{
			Ref: k.ID, Type: identitysource.PrincipalNHI, Kind: kindAPIKey,
			DisplayName: k.Name, Source: identitysource.SourceAnthropic,
			Disabled: k.Status != "active",
			Attributes: pruneAttrs(map[string]string{
				"workspace": k.WorkspaceID, "status": k.Status,
				"key_hint": k.PartialKeyHint, "created_at": k.CreatedAt,
			}),
		})
		if k.WorkspaceID != "" {
			g.Memberships = append(g.Memberships, identitysource.Membership{
				MemberRef: k.ID, MemberKind: identitysource.MemberIdentity,
				CollectionRef: k.WorkspaceID, Source: identitysource.SourceAnthropic,
			})
		}
	}

	if err := s.addWorkspaces(ctx, &g); err != nil {
		return identitysource.Graph{}, err
	}
	return g, nil
}

// addWorkspaces lists workspaces (as Collections) and their members (as Memberships,
// plus a per-workspace-role Collection so the workspace_role is not lost). A
// configured workspace_id scopes the enumeration to one workspace, bounding the
// per-workspace member calls.
func (s *Source) addWorkspaces(ctx context.Context, g *identitysource.Graph) error {
	workspaces, err := s.fetchWorkspaces(ctx)
	if err != nil {
		return err
	}
	seenRole := map[string]struct{}{}
	for _, w := range workspaces {
		if s.wsFilter != "" && w.ID != s.wsFilter {
			continue
		}
		g.Collections = append(g.Collections, identitysource.Collection{
			Ref: w.ID, Kind: identitysource.KindGroup, DisplayName: nameOrEmail(w.Name, w.ID),
			Source: identitysource.SourceAnthropic,
			Attributes: pruneAttrs(map[string]string{
				"object": "workspace", "archived": boolStr(w.ArchivedAt != ""), "created_at": w.CreatedAt,
			}),
		})

		members, err := s.fetchWorkspaceMembers(ctx, w.ID)
		if err != nil {
			return err
		}
		for _, m := range members {
			g.Memberships = append(g.Memberships, identitysource.Membership{
				MemberRef: m.UserID, MemberKind: identitysource.MemberIdentity,
				CollectionRef: m.WorkspaceID, Source: identitysource.SourceAnthropic,
			})
			roleRef := w.ID + "#role:" + m.WorkspaceRole
			if _, ok := seenRole[roleRef]; !ok {
				seenRole[roleRef] = struct{}{}
				g.Collections = append(g.Collections, identitysource.Collection{
					Ref: roleRef, Kind: identitysource.KindRole, DisplayName: m.WorkspaceRole,
					Source:     identitysource.SourceAnthropic,
					Attributes: map[string]string{"workspace": w.ID},
				})
			}
			g.Memberships = append(g.Memberships, identitysource.Membership{
				MemberRef: m.UserID, MemberKind: identitysource.MemberIdentity,
				CollectionRef: roleRef, Source: identitysource.SourceAnthropic,
			})
		}
	}
	return nil
}

// addFederationRoster models the declared federation as NHI: each rule's service
// account (svac_) and issuer (fdis_) become Identities, the rule (fdrl_) becomes a
// policy Collection, and the service account belongs to its rule. It runs
// independent of the admin credential.
func (s *Source) addFederationRoster(g *identitysource.Graph) {
	seenSA := map[string]struct{}{}
	seenIss := map[string]struct{}{}
	for _, r := range s.federation {
		if _, ok := seenSA[r.ServiceAccountID]; !ok {
			seenSA[r.ServiceAccountID] = struct{}{}
			g.Identities = append(g.Identities, identitysource.Identity{
				Ref: r.ServiceAccountID, Type: identitysource.PrincipalNHI, Kind: kindServiceAccount,
				DisplayName: nameOrEmail(r.ServiceAccountName, r.ServiceAccountID),
				Source:      identitysource.SourceAnthropic,
				Attributes: pruneAttrs(map[string]string{
					"oauth_scope": r.scope(), "workspace": r.WorkspaceID, "rule_id": r.RuleID, "issuer_id": r.IssuerID,
				}),
			})
		}
		if r.IssuerID != "" {
			if _, ok := seenIss[r.IssuerID]; !ok {
				seenIss[r.IssuerID] = struct{}{}
				g.Identities = append(g.Identities, identitysource.Identity{
					Ref: r.IssuerID, Type: identitysource.PrincipalNHI, Kind: kindIssuer,
					DisplayName: nameOrEmail(r.IssuerURL, r.IssuerID), Source: identitysource.SourceAnthropic,
					Attributes: pruneAttrs(map[string]string{"issuer_url": r.IssuerURL}),
				})
			}
		}
		// ANT2-08: carry the security-boundary match metadata as rule attributes so the
		// WIF lint can flag an over-broad CEL, an unconstrained subject/audience,
		// or an over-long token lifetime — without the connector evaluating CEL itself.
		ruleAttrs := map[string]string{
			"object": "federation_rule", "oauth_scope": r.scope(),
			"workspace": r.WorkspaceID, "issuer_id": r.IssuerID,
			"subject_prefix": r.SubjectPrefix, "audience": r.Audience,
			"cel_condition": r.CELCondition, "jwks_mode": r.JWKSMode,
		}
		if r.TokenLifetimeSeconds > 0 {
			ruleAttrs["token_lifetime_seconds"] = strconv.Itoa(r.TokenLifetimeSeconds)
		}
		if len(r.Claims) > 0 {
			ruleAttrs["claims_count"] = strconv.Itoa(len(r.Claims))
		}
		if r.CACertConfigured {
			ruleAttrs["ca_cert_configured"] = "true"
		}
		g.Collections = append(g.Collections, identitysource.Collection{
			Ref: r.RuleID, Kind: identitysource.KindPolicy, DisplayName: r.RuleID,
			Source:     identitysource.SourceAnthropic,
			Attributes: pruneAttrs(ruleAttrs),
		})
		g.Memberships = append(g.Memberships, identitysource.Membership{
			MemberRef: r.ServiceAccountID, MemberKind: identitysource.MemberIdentity,
			CollectionRef: r.RuleID, Source: identitysource.SourceAnthropic,
		})
	}
}

// nameOrEmail returns the first non-empty of name then fallback.
func nameOrEmail(name, fallback string) string {
	if name != "" {
		return name
	}
	return fallback
}

// boolStr renders a bool as "true"/"false".
func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// pruneAttrs drops empty values so the attribute map carries only present metadata,
// and returns nil when nothing remains (keeping Snapshots diff-stable).
func pruneAttrs(m map[string]string) map[string]string {
	for k, v := range m {
		if v == "" {
			delete(m, k)
		}
	}
	if len(m) == 0 {
		return nil
	}
	return m
}
