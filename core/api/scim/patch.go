// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package scim

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// PATCH errors (RFC 7644 §3.5.2). The handler maps them to 400 with the matching
// scimType.
var (
	// ErrNoTarget means a remove op omitted its required path (scimType noTarget).
	ErrNoTarget = errors.New("scim: patch remove requires a path")
	// ErrBadPatch means a malformed PatchOp body or value (scimType invalidSyntax).
	ErrBadPatch = errors.New("scim: malformed patch")
)

// PatchOp is one operation of a PatchOp body (RFC 7644 §3.5.2).
type PatchOp struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value"`
}

// PatchBody is the parsed PATCH request (RFC 7644 §3.5.2).
type PatchBody struct {
	Schemas    []string  `json:"schemas"`
	Operations []PatchOp `json:"Operations"`
}

// ApplyPatch applies a PatchOp body to the current directory state and returns
// the resulting InboundUser. It supports add/replace/remove (case-insensitive
// op), with or without a path, on the userName/displayName/externalId/active
// attributes — and the three shapes IdPs use to disable a user:
//
//	{op:"replace", path:"active", value:false}        (Okta)
//	{op:"replace", value:{"active":false}}            (Azure/Entra, no path)
//	{op:"replace", path:"active", value:"False"}      (stringified bool)
//
// Unmodeled paths are ignored leniently (a SCIM provider need not honor every
// attribute an IdP sends). A remove with no path is rejected (noTarget).
func ApplyPatch(current InboundUser, body PatchBody) (InboundUser, error) {
	if len(body.Operations) == 0 {
		return current, ErrBadPatch
	}
	out := current
	for _, op := range body.Operations {
		verb := strings.ToLower(strings.TrimSpace(op.Op))
		rawPath := strings.TrimSpace(op.Path)
		path := normalizePath(op.Path)
		switch verb {
		case "add", "replace":
			// A path targeting the whole enterprise extension carries the extension
			// object as its value (rare but valid). A `replace` of a complex attribute
			// replaces its WHOLE value (RFC 7644 §3.5.2.3), so clear the modeled
			// sub-attributes first — a sub-attribute absent from the new value must not
			// survive from the old one. An `add` merges (leaves the unmentioned ones).
			if strings.EqualFold(rawPath, SchemaEnterpriseUser) {
				if verb == "replace" {
					out.EmployeeNumber, out.Department, out.Manager = "", "", ""
				}
				if err := applyEnterpriseObject(&out, op.Value); err != nil {
					return out, err
				}
				continue
			}
			// Agent extension (draft-abbey-scim-agent-extension-00). Same
			// replace-clears/add-merges semantics as the enterprise extension.
			if strings.EqualFold(rawPath, SchemaAgentExtension) {
				if verb == "replace" {
					out.AgentKind, out.AgentSponsorRef, out.AgentDelegation = "", "", ""
				}
				if err := applyAgentObject(&out, op.Value); err != nil {
					return out, err
				}
				continue
			}
			if path == "" {
				// No path: value MUST be an object of attribute/value pairs. A key that
				// IS the enterprise-extension URN nests the extension object (the shape
				// Azure/Entra send the extension in for a no-path op).
				var obj map[string]json.RawMessage
				if err := json.Unmarshal(op.Value, &obj); err != nil {
					return out, ErrBadPatch
				}
				for k, v := range obj {
					if k == SchemaEnterpriseUser {
						if err := applyEnterpriseObject(&out, v); err != nil {
							return out, err
						}
						continue
					}
					if k == SchemaAgentExtension {
						if err := applyAgentObject(&out, v); err != nil {
							return out, err
						}
						continue
					}
					if err := applyAttr(&out, normalizePath(k), v); err != nil {
						return out, err
					}
				}
				continue
			}
			if err := applyAttr(&out, path, op.Value); err != nil {
				return out, err
			}
		case "remove":
			if path == "" {
				return out, ErrNoTarget
			}
			// Removing the whole extension clears every modeled extension attribute.
			if strings.EqualFold(rawPath, SchemaEnterpriseUser) {
				out.EmployeeNumber, out.Department, out.Manager = "", "", ""
				continue
			}
			// Removing the agent extension clears agent attributes.
			if strings.EqualFold(rawPath, SchemaAgentExtension) {
				out.AgentKind, out.AgentSponsorRef, out.AgentDelegation = "", "", ""
				continue
			}
			clearAttr(&out, path)
		default:
			return out, ErrBadPatch
		}
	}
	return out, nil
}

// applyEnterpriseObject applies a SCIM enterprise-extension object (the value of an
// op targeting the whole extension, by path or as a nested key in a no-path bulk
// object) to u. Sub-attribute names are matched case-insensitively.
func applyEnterpriseObject(u *InboundUser, raw json.RawMessage) error {
	var ext map[string]json.RawMessage
	if err := json.Unmarshal(raw, &ext); err != nil {
		return ErrBadPatch
	}
	for k, v := range ext {
		if err := applyAttr(u, strings.ToLower(strings.TrimSpace(k)), v); err != nil {
			return err
		}
	}
	return nil
}

// applyAgentObject applies a SCIM agent-extension object (draft-abbey-scim-agent-extension-00) to u. Sub-attribute names matched
// case-insensitively, unrecognized keys ignored leniently.
func applyAgentObject(u *InboundUser, raw json.RawMessage) error {
	var ext map[string]json.RawMessage
	if err := json.Unmarshal(raw, &ext); err != nil {
		return ErrBadPatch
	}
	for k, v := range ext {
		if err := applyAttr(u, strings.ToLower(strings.TrimSpace(k)), v); err != nil {
			return err
		}
	}
	return nil
}

// normalizePath lowercases a path and reduces it to its trailing attribute name,
// dropping any schema-URN prefix (e.g. "urn:…:User:active" -> "active") and any
// sub-attribute container (we model flat attributes).
func normalizePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if i := strings.LastIndex(p, ":"); i >= 0 {
		p = p[i+1:]
	}
	return strings.ToLower(p)
}

// applyAttr sets one modeled attribute from a JSON value.
func applyAttr(u *InboundUser, attr string, raw json.RawMessage) error {
	switch attr {
	case "username":
		s, err := jsonString(raw)
		if err != nil {
			return err
		}
		u.UserName = s
	case "displayname":
		s, err := jsonString(raw)
		if err != nil {
			return err
		}
		u.DisplayName = s
	case "externalid":
		s, err := jsonString(raw)
		if err != nil {
			return err
		}
		u.ExternalID = s
	case "active":
		b, err := jsonBool(raw)
		if err != nil {
			return err
		}
		u.Active = b
	case "employeenumber":
		s, err := jsonString(raw)
		if err != nil {
			return err
		}
		u.EmployeeNumber = s
	case "department":
		s, err := jsonString(raw)
		if err != nil {
			return err
		}
		u.Department = s
	case "manager", "manager.value":
		// manager is complex: the value may be the {value} object (path …:manager) or
		// a bare string (path …:manager.value, or a loose IdP). managerValue accepts both.
		u.Manager = managerValue(raw)
	// Agent extension attributes (draft-abbey-scim-agent-extension-00).
	case "agentkind":
		s, err := jsonString(raw)
		if err != nil {
			return err
		}
		u.AgentKind = s
	case "sponsorref":
		s, err := jsonString(raw)
		if err != nil {
			return err
		}
		u.AgentSponsorRef = s
	case "delegationscope":
		s, err := jsonString(raw)
		if err != nil {
			return err
		}
		u.AgentDelegation = s
	default:
		// Unmodeled attribute (e.g. name.givenName): ignore leniently.
	}
	return nil
}

// clearAttr applies a remove to a modeled attribute.
func clearAttr(u *InboundUser, attr string) {
	switch attr {
	case "displayname":
		u.DisplayName = ""
	case "externalid":
		u.ExternalID = ""
	case "active":
		u.Active = false
	case "employeenumber":
		u.EmployeeNumber = ""
	case "department":
		u.Department = ""
	case "manager", "manager.value":
		u.Manager = ""
	// Agent extension attributes (draft-abbey-scim-agent-extension-00).
	case "agentkind":
		u.AgentKind = ""
	case "sponsorref":
		u.AgentSponsorRef = ""
	case "delegationscope":
		u.AgentDelegation = ""
	}
}

// jsonString decodes a JSON string value.
func jsonString(raw json.RawMessage) (string, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", ErrBadPatch
	}
	return s, nil
}

// jsonBool decodes a JSON boolean OR a stringified boolean ("true"/"false",
// case-insensitive) — some IdPs send active as a string.
func jsonBool(raw json.RawMessage) (bool, error) {
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return b, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		v, perr := strconv.ParseBool(strings.ToLower(strings.TrimSpace(s)))
		if perr == nil {
			return v, nil
		}
	}
	return false, ErrBadPatch
}
