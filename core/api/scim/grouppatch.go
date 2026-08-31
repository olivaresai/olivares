// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package scim

import (
	"encoding/json"
	"strings"
)

// Group PATCH engine (RFC 7644 §3.5.2). It is deliberately SEPARATE from the
// user ApplyPatch: that engine models flat scalar attributes and its
// normalizePath DROPS value-path containers, so reusing it would silently no-op
// the membership operations real IdPs send — a data-integrity bug, not an
// error. This engine understands the multi-valued "members" attribute in the
// shapes Okta and Entra actually use:
//
//	{op:"add",     path:"members", value:[{"value":"<id>"}, ...]}   (Okta/Entra add)
//	{op:"remove",  path:"members[value eq \"<id>\"]"}               (Okta remove-one)
//	{op:"remove",  path:"members", value:[{"value":"<id>"}]}        (Entra remove)
//	{op:"remove",  path:"members"}                                  (remove ALL, §3.5.2.2)
//	{op:"replace", path:"members", value:[...]}                     (replace the set)
//	{op:"Replace", value:{"displayName":"...", "members":[...]}}    (Entra no-path, capitalized op)
//
// Member values are accepted both as objects ({"value":"id", ...}) and as bare
// strings ("id"). A member whose type is "Group" (nesting) is ErrNestedGroup.
// There is NO role-like attribute here at all: model.UserGroup.MappedRole is
// not modeled in the SCIM wire, so no PATCH path can reach it — the unmodeled
// paths are ignored leniently exactly like the user engine.

// ApplyGroupPatch applies a PatchOp body to the current group state and returns
// the resulting InboundGroup. Unmodeled paths are ignored leniently; a remove
// with no path at all is ErrNoTarget; removing displayName is ErrBadPatch (the
// attribute is required).
func ApplyGroupPatch(current InboundGroup, body PatchBody) (InboundGroup, error) {
	if len(body.Operations) == 0 {
		return current, ErrBadPatch
	}
	out := current
	for _, op := range body.Operations {
		verb := strings.ToLower(strings.TrimSpace(op.Op))
		path, memberFilter, err := normalizeGroupPath(op.Path)
		if err != nil {
			return out, err
		}
		switch verb {
		case "add", "replace":
			if memberFilter != "" {
				// add/replace against members[value eq X] would rewrite one member's
				// sub-attributes — nothing mutable is modeled there. Ignore leniently.
				continue
			}
			if path == "" {
				// No path: value MUST be an object of attribute/value pairs (the
				// Entra shape).
				var obj map[string]json.RawMessage
				if err := json.Unmarshal(op.Value, &obj); err != nil {
					return out, ErrBadPatch
				}
				for k, v := range obj {
					kPath, kFilter, err := normalizeGroupPath(k)
					if err != nil {
						return out, err
					}
					if kFilter != "" {
						// A filtered key (members[value eq X]) inside the object form
						// targets one member's sub-attributes — nothing mutable is
						// modeled there. Ignore leniently, exactly like the top-level
						// value-path case: folding it into a plain "members" write
						// would silently REPLACE the whole member set.
						continue
					}
					if err := applyGroupAttr(&out, verb, kPath, v); err != nil {
						return out, err
					}
				}
				continue
			}
			if err := applyGroupAttr(&out, verb, path, op.Value); err != nil {
				return out, err
			}
		case "remove":
			switch {
			case memberFilter != "":
				out.Members = removeMembers(out.Members, []string{memberFilter})
			case path == "members":
				if len(op.Value) == 0 || string(op.Value) == "null" {
					// remove with path "members" and no value clears the whole set
					// (RFC 7644 §3.5.2.2: no value => remove every member).
					out.Members = nil
					continue
				}
				vals, err := memberValues(op.Value)
				if err != nil {
					return out, err
				}
				out.Members = removeMembers(out.Members, vals)
			case path == "externalid":
				out.ExternalID = ""
			case path == "displayname":
				// displayName is REQUIRED (RFC 7643 §4.2); removing it cannot yield a
				// valid resource.
				return out, ErrBadPatch
			case path == "":
				return out, ErrNoTarget
			default:
				// Unmodeled path: ignore leniently.
			}
		default:
			return out, ErrBadPatch
		}
	}
	return out, nil
}

// applyGroupAttr sets one modeled attribute from a JSON value for add/replace.
func applyGroupAttr(g *InboundGroup, verb, attr string, raw json.RawMessage) error {
	switch attr {
	case "displayname":
		s, err := jsonString(raw)
		if err != nil {
			return err
		}
		g.DisplayName = strings.TrimSpace(s)
	case "externalid":
		s, err := jsonString(raw)
		if err != nil {
			return err
		}
		g.ExternalID = strings.TrimSpace(s)
	case "members":
		vals, err := memberValues(raw)
		if err != nil {
			return err
		}
		if verb == "replace" {
			g.Members = vals
			return nil
		}
		// add = union, preserving existing order then appending new values.
		seen := make(map[string]bool, len(g.Members))
		for _, m := range g.Members {
			seen[m] = true
		}
		for _, v := range vals {
			if !seen[v] {
				seen[v] = true
				g.Members = append(g.Members, v)
			}
		}
	default:
		// Unmodeled attribute: ignore leniently (a SCIM provider need not honor
		// every attribute an IdP sends). MappedRole is unreachable by construction:
		// it is not a modeled SCIM attribute at all.
	}
	return nil
}

// memberValues decodes a members value: an array of {"value":"id"} objects, an
// array of bare id strings, or a single object/string. A member typed "Group"
// is ErrNestedGroup. Empty values are dropped; the result is de-duplicated.
func memberValues(raw json.RawMessage) ([]string, error) {
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		arr = []json.RawMessage{raw} // single object/string form
	}
	var out []string
	seen := map[string]bool{}
	for _, item := range arr {
		var m groupMember
		if err := json.Unmarshal(item, &m); err != nil {
			var s string
			if err := json.Unmarshal(item, &s); err != nil {
				return nil, ErrBadPatch
			}
			m = groupMember{Value: s}
		}
		if strings.EqualFold(strings.TrimSpace(m.Type), "Group") {
			return nil, ErrNestedGroup
		}
		v := strings.TrimSpace(m.Value)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out, nil
}

// removeMembers returns members minus the listed values (idempotent: removing
// an absent member is not an error — RFC semantics and what IdP retries need).
func removeMembers(members, drop []string) []string {
	gone := make(map[string]bool, len(drop))
	for _, d := range drop {
		gone[d] = true
	}
	var out []string
	for _, m := range members {
		if !gone[m] {
			out = append(out, m)
		}
	}
	return out
}

// normalizeGroupPath lowercases a path, strips any schema-URN prefix, and
// recognizes the one value-path shape group PATCHes use: members[value eq "id"]
// (single or double quotes), returned as (path "members", memberFilter id).
// Any other value-path container is unsupported (ErrUnsupportedFilter -> 400
// invalidPath at the handler).
func normalizeGroupPath(p string) (path, memberFilter string, err error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", "", nil
	}
	// Strip a schema URN prefix (urn:...:Group:members -> members). A ':' never
	// appears inside the bracket filter, so split before bracket parsing.
	if i := strings.LastIndex(p, ":"); i >= 0 && !strings.Contains(p[:i], "[") {
		p = p[i+1:]
	}
	open := strings.IndexByte(p, '[')
	if open < 0 {
		return strings.ToLower(p), "", nil
	}
	attr := strings.ToLower(strings.TrimSpace(p[:open]))
	rest := p[open:]
	if attr != "members" || !strings.HasSuffix(rest, "]") {
		return "", "", ErrUnsupportedFilter
	}
	inner := strings.TrimSpace(rest[1 : len(rest)-1])
	// Exactly: value eq "<id>" (case-insensitive attr/op, either quote style).
	fields := strings.SplitN(inner, " ", 3)
	if len(fields) != 3 || !strings.EqualFold(strings.TrimSpace(fields[0]), "value") ||
		!strings.EqualFold(strings.TrimSpace(fields[1]), "eq") {
		return "", "", ErrUnsupportedFilter
	}
	val := strings.TrimSpace(fields[2])
	if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') && val[len(val)-1] == val[0] {
		val = val[1 : len(val)-1]
	}
	if val == "" {
		return "", "", ErrUnsupportedFilter
	}
	return "members", val, nil
}
