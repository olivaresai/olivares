// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package entraagent

import (
	"context"
	"net/url"

	"github.com/olivaresai/olivares/connectors/internal/httpx"
)

// Microsoft Graph wire shapes. Only the fields the connector maps are decoded —
// minimal data at decode time; a credential value is never among them.

// page is one @odata page of a Graph collection response.
type page[T any] struct {
	NextLink string `json:"@odata.nextLink"`
	Value    []T    `json:"value"`
}

// collectPages follows @odata.nextLink pagination (the next link is an ABSOLUTE
// URL; httpx passes a fully-qualified path through verbatim) and concatenates
// the pages, bounded by maxPages so a hostile or runaway feed cannot loop the
// connector forever.
func collectPages[T any](ctx context.Context, client *httpx.Client, path string, query url.Values, maxPages int) ([]T, error) {
	var out []T
	for i := 0; i < maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var p page[T]
		if err := client.GetJSON(ctx, path, query, &p); err != nil {
			return nil, err
		}
		out = append(out, p.Value...)
		if p.NextLink == "" {
			break
		}
		path, query = p.NextLink, nil // the next link is absolute and self-contained
	}
	return out, nil
}

// agentIdentity is the slice of microsoft.graph.agentIdentity (a servicePrincipal
// subtype; servicePrincipalType=="ServiceIdentity") the connector reads. The
// same shape serves the soft-deleted servicePrincipal rows, where deletedDateTime
// is additionally present. tags is documented on the resource but not mapped.
// Verified against learn.microsoft.com on 2026-06-11:
// /v1.0/servicePrincipals/microsoft.graph.agentIdentity and
// /v1.0/directory/deletedItems/microsoft.graph.servicePrincipal.
type agentIdentity struct {
	ID                       string `json:"id"`
	DisplayName              string `json:"displayName"`
	AccountEnabled           bool   `json:"accountEnabled"`
	AgentIdentityBlueprintID string `json:"agentIdentityBlueprintId"` // appId of the parent blueprint
	CreatedByAppID           string `json:"createdByAppId"`
	CreatedDateTime          string `json:"createdDateTime"`
	// DisabledByMicrosoftStatus is null | "NotDisabled" |
	// "DisabledDueToViolationOfServicesAgreement" (Graph v1.0, verified
	// 2026-06-11). JSON null decodes to "".
	DisabledByMicrosoftStatus string `json:"disabledByMicrosoftStatus"`
	ServicePrincipalType      string `json:"servicePrincipalType"`
	DeletedDateTime           string `json:"deletedDateTime"` // deletedItems rows only
}

// blueprint is the slice of microsoft.graph.agentIdentityBlueprint (an
// application subtype) the connector reads. AppID is the key the agents'
// agentIdentityBlueprintId points at. passwordCredentials is decoded ONLY for
// Gather's drift check, and ONLY its expiry: secretText/hint/customKeyIdentifier
// are never decoded (minimal data — the connector counts secrets, it never sees
// them). keyCredentials (certificates) are deliberately not decoded at all:
// certificate-based auth is the recommended replacement for static secrets, so
// their presence is not a finding.
// Verified against learn.microsoft.com on 2026-06-11:
// /v1.0/applications/microsoft.graph.agentIdentityBlueprint.
type blueprint struct {
	ID                  string               `json:"id"`
	AppID               string               `json:"appId"`
	DisplayName         string               `json:"displayName"`
	PasswordCredentials []passwordCredential `json:"passwordCredentials"`
}

// passwordCredential reads only the expiry of a blueprint client secret. A
// missing/null endDateTime means the static secret never expires — the
// severity-escalation signal in Gather.
type passwordCredential struct {
	EndDateTime string `json:"endDateTime"`
}

// blueprintPrincipal is the slice of microsoft.graph.agentIdentityBlueprintPrincipal
// (a servicePrincipal subtype) the connector reads. It is the principal that
// holds the credential shared by ALL the blueprint's agents.
// Verified against learn.microsoft.com on 2026-06-11:
// /v1.0/servicePrincipals/microsoft.graph.agentIdentityBlueprintPrincipal.
type blueprintPrincipal struct {
	ID             string `json:"id"`
	AppID          string `json:"appId"`
	DisplayName    string `json:"displayName"`
	AccountEnabled bool   `json:"accountEnabled"`
}

// agentUser is the microsoft.graph.agentUser OData cast under users. It is a
// user subtype linked 1:1 to the parent agentIdentity by identityParentId.
// Verified against learn.microsoft.com on 2026-07-04:
// /v1.0/users/microsoft.graph.agentUser.
type agentUser struct {
	ID                string `json:"id"`
	DisplayName       string `json:"displayName"`
	AccountEnabled    bool   `json:"accountEnabled"`
	IdentityParentID  string `json:"identityParentId"`
	UserPrincipalName string `json:"userPrincipalName"`
	CreatedDateTime   string `json:"createdDateTime"`
}

// directoryObjectPage is one page of an owners/sponsors relationship listing:
// heterogeneous directoryObject rows discriminated by "@odata.type".
// Verified against learn.microsoft.com on 2026-06-11:
// /v1.0/servicePrincipals/{id}/microsoft.graph.agentIdentity/owners and
// /v1.0/servicePrincipals/{id}/microsoft.graph.agentIdentity/sponsors.
type directoryObjectPage struct {
	NextLink string `json:"@odata.nextLink"`
	Value    []struct {
		ODataType string `json:"@odata.type"`
		ID        string `json:"id"`
	} `json:"value"`
}

// conditionalAccessPolicy is the beta Conditional Access policy shape needed to
// detect whether live enabled policies target agent identities or agent users.
// The agent identity condition fields are beta-only and were verified against
// learn.microsoft.com on 2026-07-04:
// /beta/identity/conditionalAccess/policies. The agent-user sentinel
// conditions.users.includeUsers=="AllAgentIdUsers" was verified 2026-07-04 in
// the conditionalAccessRoot list-policies beta doc's Example 2.
type conditionalAccessPolicy struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	State       string `json:"state"`
	Conditions  struct {
		Users struct {
			IncludeUsers []string `json:"includeUsers"`
		} `json:"users"`
		ClientApplications struct {
			IncludeAgentIDServicePrincipals []string                       `json:"includeAgentIdServicePrincipals"`
			ExcludeAgentIDServicePrincipals []string                       `json:"excludeAgentIdServicePrincipals"`
			AgentIDServicePrincipalFilter   *agentIDServicePrincipalFilter `json:"agentIdServicePrincipalFilter"`
		} `json:"clientApplications"`
		AgentIDRiskLevels []string `json:"agentIdRiskLevels"`
	} `json:"conditions"`
}

type agentIDServicePrincipalFilter struct {
	Mode string `json:"mode"`
	Rule string `json:"rule"`
}

// riskyAgent is the beta ID Protection risky-agent shape. Enum fields remain
// plain strings because Microsoft Graph marks them evolvable. Verified against
// learn.microsoft.com on 2026-07-04:
// /beta/identityProtection/riskyAgents.
type riskyAgent struct {
	ID                       string `json:"id"`
	AgentDisplayName         string `json:"agentDisplayName"`
	BlueprintID              string `json:"blueprintId"`
	IdentityType             string `json:"identityType"`
	IsDeleted                bool   `json:"isDeleted"`
	IsEnabled                bool   `json:"isEnabled"`
	IsProcessing             bool   `json:"isProcessing"`
	RiskLevel                string `json:"riskLevel"`
	RiskState                string `json:"riskState"`
	RiskDetail               string `json:"riskDetail"`
	RiskLastModifiedDateTime string `json:"riskLastModifiedDateTime"`
}

// signIn is the beta auditLogs/signIns row shape used for opt-in observed
// agent sign-in edges. Verified 2026-07-04 against the learn.microsoft.com
// Entra Agent ID sign-in and audit logs for agents doc (updated 2026-06-17)
// and the beta signIn resource page (updated 2026-07-04): the agent property is
// beta-only, and agentIdentityBlueprintPrincipal / agentIDuser enum values need
// Prefer: include-unknown-enum-members. Microsoft's filter example uses
// agent/agentType eq 'AgentIdentity', while the resource page names the agent
// identity value agenticAppInstance; the connector copies that example as the
// default filter but maps confidence case-insensitively. Minimal-data: only
// agent attribution and target-resource fields are decoded, never user
// telemetry, IP/location, device, browser or other user-shaped sign-in fields.
type signIn struct {
	ID                   string        `json:"id"`
	CreatedDateTime      string        `json:"createdDateTime"`
	ServicePrincipalID   string        `json:"servicePrincipalId"`
	ServicePrincipalName string        `json:"servicePrincipalName"`
	AppID                string        `json:"appId"`
	ResourceID           string        `json:"resourceId"`
	ResourceDisplayName  string        `json:"resourceDisplayName"`
	Status               signInStatus  `json:"status"`
	Agent                signInAgentic `json:"agent"`
}

type signInStatus struct {
	ErrorCode int `json:"errorCode"`
}

type signInAgentic struct {
	AgentType            string `json:"agentType"`
	AgentSubjectType     string `json:"agentSubjectType"`
	AgentSubjectParentID string `json:"agentSubjectParentId"`
	ParentAppID          string `json:"parentAppId"`
}

// accessPackageAssignmentPolicy is the v1.0 entitlement-management assignment
// policy shape. allowedTargetScope=="allDirectoryAgentIdentities" is the
// documented signal that a policy is open to agent identities. Verified against
// learn.microsoft.com on 2026-07-04:
// /v1.0/identityGovernance/entitlementManagement/assignmentPolicies.
type accessPackageAssignmentPolicy struct {
	ID                 string `json:"id"`
	DisplayName        string `json:"displayName"`
	AllowedTargetScope string `json:"allowedTargetScope"`
}
