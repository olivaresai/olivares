// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package scim

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/core/model"
)

// InboundUser is the directory attribute set parsed from a SCIM create/replace
// body, neutral of the store. The handler maps it to auth.SCIMUserInput.
type InboundUser struct {
	// UserName is the SCIM userName (REQUIRED), mapped to the user's email.
	UserName string
	// ExternalID is the IdP's stable id (optional).
	ExternalID string
	// DisplayName is a human label.
	DisplayName string
	// Active is the administrative status; defaults to true on create when absent.
	Active bool
	// The enterprise User extension attributes (RFC 7643 §4.3) the provider honors
	// read- and write-through. Empty when the IdP did not send the extension.
	EmployeeNumber string
	Department     string
	// Manager is the enterprise extension manager.value (the manager's id/externalId
	// as the IdP sends it), stored verbatim — never resolved to a local id.
	Manager string
	// Agent extension (draft-abbey-scim-agent-extension-00 — defensive/
	// opt-in, never mandatory). Populated when the IdP sends the extension;
	// empty when absent (the user provisions normally).
	AgentKind       string
	AgentSponsorRef string
	AgentDelegation string
}

// userBody is the subset of the SCIM User JSON the provider reads. Fields the IdP
// sends but the provider does not model are ignored (lenient parsing). The
// enterprise extension rides a top-level key whose name IS the schema URN — Go's
// json tag matches the literal string, colons and all.
type userBody struct {
	Schemas     []string `json:"schemas"`
	UserName    string   `json:"userName"`
	ExternalID  string   `json:"externalId"`
	DisplayName string   `json:"displayName"`
	Active      *bool    `json:"active"`
	Name        *struct {
		GivenName  string `json:"givenName"`
		FamilyName string `json:"familyName"`
		Formatted  string `json:"formatted"`
	} `json:"name"`
	Emails []struct {
		Value   string `json:"value"`
		Type    string `json:"type"`
		Primary bool   `json:"primary"`
	} `json:"emails"`
	Enterprise *enterpriseExt `json:"urn:ietf:params:scim:schemas:extension:enterprise:2.0:User"`
	Agent      *agentExt      `json:"urn:ietf:params:scim:schemas:extension:agent:2.0:User"`
}

// enterpriseExt is the subset of the enterprise User extension the provider stores
// (RFC 7643 §4.3). manager is complex (value/$ref/displayName) but an IdP may also
// send it as a bare string; it is captured raw and resolved by managerValue.
type enterpriseExt struct {
	EmployeeNumber string          `json:"employeeNumber"`
	Department     string          `json:"department"`
	Manager        json.RawMessage `json:"manager"`
}

// agentExt is the agent identity extension (tracking
// draft-abbey-scim-agent-extension-00). Defensive/opt-in: parsed when present,
// not required, never wired to enforcement. All fields are optional strings.
type agentExt struct {
	AgentKind       string `json:"agentKind"`
	SponsorRef      string `json:"sponsorRef"`
	DelegationScope string `json:"delegationScope"`
}

// managerValue resolves an enterprise-extension manager value from its raw JSON.
// It accepts the RFC complex form {"value":"…"} (taking .value) and the bare-string
// form some IdPs send. Anything else yields "" (lenient).
func managerValue(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s)
	}
	var obj struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		return strings.TrimSpace(obj.Value)
	}
	return ""
}

// DecodeUser maps a SCIM User body to an InboundUser. userName falls back to the
// primary (or first) email when absent, and active defaults to true on create.
func DecodeUser(b userBody) InboundUser {
	in := InboundUser{
		UserName:    strings.TrimSpace(b.UserName),
		ExternalID:  strings.TrimSpace(b.ExternalID),
		DisplayName: strings.TrimSpace(b.DisplayName),
		Active:      true,
	}
	if b.Active != nil {
		in.Active = *b.Active
	}
	if in.UserName == "" {
		in.UserName = primaryEmail(b)
	}
	if in.DisplayName == "" && b.Name != nil {
		if b.Name.Formatted != "" {
			in.DisplayName = b.Name.Formatted
		} else {
			in.DisplayName = strings.TrimSpace(b.Name.GivenName + " " + b.Name.FamilyName)
		}
	}
	if b.Enterprise != nil {
		in.EmployeeNumber = strings.TrimSpace(b.Enterprise.EmployeeNumber)
		in.Department = strings.TrimSpace(b.Enterprise.Department)
		in.Manager = managerValue(b.Enterprise.Manager)
	}
	if b.Agent != nil {
		in.AgentKind = strings.TrimSpace(b.Agent.AgentKind)
		in.AgentSponsorRef = strings.TrimSpace(b.Agent.SponsorRef)
		in.AgentDelegation = strings.TrimSpace(b.Agent.DelegationScope)
	}
	return in
}

// UserBodyType is the exported alias the handler decodes into.
type UserBodyType = userBody

func primaryEmail(b userBody) string {
	for _, e := range b.Emails {
		if e.Primary && e.Value != "" {
			return e.Value
		}
	}
	if len(b.Emails) > 0 {
		return b.Emails[0].Value
	}
	return ""
}

// EncodeUser renders a model.User as a SCIM User resource. location is the
// absolute base URL of the Users collection (e.g. https://host/v1/scim/v2/Users);
// the resource's own location is location+"/"+id.
func EncodeUser(u model.User, usersURL string) map[string]any {
	res := map[string]any{
		"schemas":  []string{SchemaUser},
		"id":       u.ID.String(),
		"userName": u.Email,
		"active":   u.Status == model.StatusActive,
		"emails":   []map[string]any{{"value": u.Email, "type": "work", "primary": true}},
		"meta": map[string]any{
			"resourceType": "User",
			"created":      u.CreatedAt.String(),
			"lastModified": u.UpdatedAt.String(),
			"location":     usersURL + "/" + u.ID.String(),
			"version":      etag(u.Version),
		},
	}
	if u.ExternalID != "" {
		res["externalId"] = u.ExternalID
	}
	if u.DisplayName != "" {
		res["displayName"] = u.DisplayName
	}
	// Emit the enterprise extension only when a value is present, and add its URN to
	// schemas in lock-step (RFC 7644 §3.3 — a resource declares an extension schema
	// it actually carries). manager renders as the complex {value} form.
	if ext := encodeEnterprise(u); len(ext) > 0 {
		res["schemas"] = []string{SchemaUser, SchemaEnterpriseUser}
		res[SchemaEnterpriseUser] = ext
	}
	return res
}

// encodeEnterprise renders the enterprise extension object from the stored
// attributes, or nil when none is set.
func encodeEnterprise(u model.User) map[string]any {
	ext := map[string]any{}
	if u.EmployeeNumber != "" {
		ext["employeeNumber"] = u.EmployeeNumber
	}
	if u.Department != "" {
		ext["department"] = u.Department
	}
	if u.Manager != "" {
		ext["manager"] = map[string]any{"value": u.Manager}
	}
	if len(ext) == 0 {
		return nil
	}
	return ext
}

// etag renders an opaque weak ETag from the row version.
func etag(version int64) string {
	return "W/\"" + strconv.FormatInt(version, 10) + "\""
}
