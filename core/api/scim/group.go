// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package scim

import (
	"errors"
	"strings"

	"github.com/olivaresai/olivares/core/model"
)

// ErrNestedGroup means a group body names a member of type Group. The provider
// has no nesting counterpart (group membership maps onto per-tenant user
// memberships), so nesting is rejected honestly rather than silently flattened;
// the handler maps it to SCIM 400 invalidValue.
var ErrNestedGroup = errors.New("scim: nested groups (member type Group) are not supported")

// InboundGroup is the directory attribute set parsed from a SCIM Group
// create/replace body, neutral of the store. The handler maps it to
// auth.SCIMGroupInput. There is deliberately NO role attribute: the group→role
// mapping (model.UserGroup.MappedRole) is operator-only and never travels an
// inbound SCIM path.
type InboundGroup struct {
	// DisplayName is the SCIM displayName (REQUIRED by RFC 7643 §4.2).
	DisplayName string
	// ExternalID is the IdP's stable id (optional; Entra sends one, Okta may not).
	ExternalID string
	// Members are the member User ids (the SCIM members[].value entries).
	Members []string
}

// groupBody is the subset of the SCIM Group JSON the provider reads. Fields the
// IdP sends but the provider does not model are ignored (lenient parsing, the
// same posture as userBody).
type groupBody struct {
	Schemas     []string      `json:"schemas"`
	DisplayName string        `json:"displayName"`
	ExternalID  string        `json:"externalId"`
	Members     []groupMember `json:"members"`
}

// groupMember is one members[] entry. Only value (the user id) and type are
// read; display/$ref are derived on encode, never trusted inbound.
type groupMember struct {
	Value string `json:"value"`
	Type  string `json:"type"`
}

// GroupBodyType is the exported alias the handler decodes into.
type GroupBodyType = groupBody

// DecodeGroup maps a SCIM Group body to an InboundGroup. A member whose type is
// "Group" (nesting) is rejected with ErrNestedGroup; empty member values are
// dropped; values are de-duplicated preserving order.
func DecodeGroup(b groupBody) (InboundGroup, error) {
	in := InboundGroup{
		DisplayName: strings.TrimSpace(b.DisplayName),
		ExternalID:  strings.TrimSpace(b.ExternalID),
	}
	seen := map[string]bool{}
	for _, m := range b.Members {
		if strings.EqualFold(strings.TrimSpace(m.Type), "Group") {
			return InboundGroup{}, ErrNestedGroup
		}
		v := strings.TrimSpace(m.Value)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		in.Members = append(in.Members, v)
	}
	return in, nil
}

// EncodeGroup renders a stored group and its resolved member users as a SCIM
// Group resource. groupsURL/usersURL are the absolute collection URLs (the
// member $ref points into /Users). MappedRole is deliberately NOT rendered:
// the role mapping is operator-only surface (GET /v1/groups), invisible to the
// IdP.
func EncodeGroup(g model.UserGroup, members []model.User, groupsURL, usersURL string) map[string]any {
	ms := make([]map[string]any, 0, len(members))
	for _, u := range members {
		display := u.DisplayName
		if display == "" {
			display = u.Email
		}
		ms = append(ms, map[string]any{
			"value":   u.ID.String(),
			"display": display,
			"type":    "User",
			"$ref":    usersURL + "/" + u.ID.String(),
		})
	}
	res := map[string]any{
		"schemas":     []string{SchemaGroup},
		"id":          g.ID.String(),
		"displayName": g.DisplayName,
		"members":     ms,
		"meta": map[string]any{
			"resourceType": "Group",
			"created":      g.CreatedAt.String(),
			"lastModified": g.UpdatedAt.String(),
			"location":     groupsURL + "/" + g.ID.String(),
			"version":      etag(g.Version),
		},
	}
	if g.ExternalID != "" {
		res["externalId"] = g.ExternalID
	}
	return res
}
