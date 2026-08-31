// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// This file renders structured Olivares governance rows into the closed
// AgentCore Cedar template. The exporter deliberately does NOT transpile
// arbitrary Olivares Cedar: AgentCore's schema is gateway-generated and
// claim-based, so the safe surface is the operator-mapped row projection.
package agentcore

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// CedarNamespace constants for the two Cedar surfaces.
const (
	// NamespaceAgentCore is the fixed namespace AgentCore Cedar policies use.
	// All entity types are prefixed AgentCore:: (OAuthUser, IamEntity, Action,
	// Gateway).
	NamespaceAgentCore = "AgentCore"

	// NamespaceOlivares is the namespace Olivares' own Cedar engine uses. The
	// Exporter consumes structured Olivares rows instead of trying to
	// compile Olivares Cedar into AgentCore's gateway-local schema.
	NamespaceOlivares = "Olivares"
)

const (
	exportPolicyNameVersion = "agentcore-export-policy-v1"

	exportKindGrant       = "grant"
	exportKindModelAccess = "model_access"
	exportKindSourceScope = "source_scope"

	exportEffectPermit = "permit"
	exportEffectForbid = "forbid"

	enforcementModeActive  = "ACTIVE"
	enforcementModeLogOnly = "LOG_ONLY"
)

// Unsupported reason constants are intentionally a small closed vocabulary so
// module VI can aggregate and localize them without parsing prose.
const (
	reasonNoGatewayMapping  = "no_gateway_mapping"
	reasonTenantWideScope   = "tenant_wide_scope"
	reasonNoSubjectClaim    = "no_subject_claim"
	reasonNoActionMapping   = "no_action_mapping"
	reasonNoReadOnlyActions = "no_read_only_actions"
	reasonAgentGroupScope   = "agent_group_scope"
	reasonSurfaceScoped     = "surface_scoped"
	reasonBadEffect         = "bad_effect"
	reasonBadKind           = "bad_kind"
	reasonBadSourceAccess   = "bad_source_access"
)

// ExportItem is one structured Olivares rule to project (decision D1).
type ExportItem struct {
	Kind        string // "grant" | "model_access" | "source_scope"
	Tenant      string
	SubjectKind string // "user" | "role" | "group" | "workspace" | "agent_group"
	SubjectRef  string
	ScopeKind   string   // "workspace" | "agent_group" | "tenant"; "" + Workspace != "" behaves as workspace
	Workspace   string   // scope workspace ref; "" = tenant-wide
	Effect      string   // "permit" | "forbid"
	Perms       []string // grant: "<kind>:<verb>" permission names
	Models      []string // model_access: model-group refs
	Sources     []string // source_scope: connector/source refs
	Surfaces    []string // Surface dimension; remote schema cannot express it
	Access      string   // source_scope: "r" | "rw"
}

// ClaimBinding maps an Olivares subject onto an OAuth claim tag equality.
type ClaimBinding struct {
	Tag   string
	Value string
}

// ExportMapping is the operator-provided projection table (contract §2).
type ExportMapping struct {
	WorkspaceGateways map[string][]string     // workspace ref -> gateway ARNs
	SubjectClaims     map[string]ClaimBinding // "<SubjectKind>:<SubjectRef>" -> binding
	PermActions       map[string][]string     // perm -> AgentCore action names
	ModelActions      map[string][]string     // model-group -> action names
	SourceActions     map[string][]string     // source -> action names (rw)
	SourceReadActions map[string][]string     // source -> read-only action names
}

// RenderOptions controls the remote AgentCore policy mode. Empty means ACTIVE;
// LOG_ONLY is the only supported non-active mode in the verified AgentCore wire
// contract.
type RenderOptions struct {
	EnforcementMode string
}

// RenderedPolicy is one desired AgentCore Cedar policy. Description carries
// the drift anchor marker and Statement is the exact Cedar text to write.
type RenderedPolicy struct {
	Name            string
	Statement       string
	Description     string
	EnforcementMode string
}

// UnsupportedItem records an Olivares row that could not be conservatively
// projected onto AgentCore. It is never silently dropped.
type UnsupportedItem struct {
	Item   ExportItem
	Reason string
}

// RenderExport translates structured Olivares rows into deterministic
// AgentCore Cedar policies. Each workspace-scoped item fans out to one policy
// per mapped gateway; every non-renderable expansion is returned in unsupported
// with a machine-readable reason.
func RenderExport(items []ExportItem, m ExportMapping, opts RenderOptions) ([]RenderedPolicy, []UnsupportedItem) {
	mode := normalizeEnforcementMode(opts.EnforcementMode)
	desired := make([]RenderedPolicy, 0, len(items))
	unsupported := make([]UnsupportedItem, 0)

	// Merge rows sharing one export identity FIRST: the policy name is derived
	// from (kind, tenant, subject, scope kind, workspace, gateway, effect,
	// access), so two rows differing only in their Perms/Models/Sources/Surfaces
	// sets would render two policies with the SAME name and the planner's
	// by-name map would silently keep one — a no-silent-loss violation. Cedar
	// semantics make the union of the action sets exactly equivalent to the two
	// separate statements.
	items = mergeExportItems(items)

	for _, item := range items {
		scopeKind := strings.ToLower(strings.TrimSpace(item.ScopeKind))
		if scopeKind == "agent_group" {
			unsupported = appendUnsupported(unsupported, item, reasonAgentGroupScope)
			continue
		}
		if scopeKind == "tenant" || item.Workspace == "" {
			unsupported = appendUnsupported(unsupported, item, reasonTenantWideScope)
			continue
		}
		gateways := sortedCopy(m.WorkspaceGateways[item.Workspace])
		if len(gateways) == 0 {
			unsupported = appendUnsupported(unsupported, item, reasonNoGatewayMapping)
			continue
		}

		for _, gateway := range gateways {
			claim, ok := m.SubjectClaims[subjectClaimKey(item.SubjectKind, item.SubjectRef)]
			if !ok {
				unsupported = appendUnsupported(unsupported, item, reasonNoSubjectClaim)
				continue
			}
			effect := strings.TrimSpace(item.Effect)
			if effect != exportEffectPermit && effect != exportEffectForbid {
				unsupported = appendUnsupported(unsupported, item, reasonBadEffect)
				continue
			}
			// AgentCore's policy schema has no surface dimension. A
			// surface-scoped permit would become a global permit and over-permit,
			// so it is unsupported; a surface-scoped forbid remains safe because
			// the conservative failure mode is over-forbidding.
			if effect == exportEffectPermit && len(item.Surfaces) > 0 {
				unsupported = appendUnsupported(unsupported, item, reasonSurfaceScoped)
				continue
			}
			actions, reason := actionsForItem(item, m)
			if reason != "" {
				unsupported = appendUnsupported(unsupported, item, reason)
				continue
			}

			statement := renderAgentCoreStatement(effect, actions, gateway, claim)
			desired = append(desired, RenderedPolicy{
				Name:            exportPolicyName(item, gateway, effect),
				Statement:       statement,
				Description:     exportMarker(item.Tenant, statement),
				EnforcementMode: mode,
			})
		}
	}

	sort.SliceStable(desired, func(i, j int) bool {
		if desired[i].Name == desired[j].Name {
			return desired[i].Statement < desired[j].Statement
		}
		return desired[i].Name < desired[j].Name
	})
	sort.SliceStable(unsupported, func(i, j int) bool {
		return unsupportedSortKey(unsupported[i]) < unsupportedSortKey(unsupported[j])
	})
	return desired, unsupported
}

// mergeExportItems unions the action-source lists (Perms/Models/Sources) of
// items that share one export identity — the exact tuple exportIdentityHash
// keys the policy name on, minus the gateway (fan-out happens later per
// gateway). Order-independent: the merged lists are sorted, and group order
// follows first appearance for a stable, input-order-preserving result.
func mergeExportItems(items []ExportItem) []ExportItem {
	type group struct {
		item    ExportItem
		perms   map[string]bool
		models  map[string]bool
		sources map[string]bool
		surfs   map[string]bool
	}
	byKey := make(map[string]*group, len(items))
	order := make([]string, 0, len(items))
	for _, item := range items {
		key := hashLengthPrefixed(
			strings.TrimSpace(item.Kind),
			item.Tenant,
			item.SubjectKind,
			item.SubjectRef,
			strings.ToLower(strings.TrimSpace(item.ScopeKind)),
			item.Workspace,
			strings.TrimSpace(item.Effect),
			strings.TrimSpace(item.Access),
		)
		g, ok := byKey[key]
		if !ok {
			g = &group{item: item, perms: map[string]bool{}, models: map[string]bool{}, sources: map[string]bool{}, surfs: map[string]bool{}}
			byKey[key] = g
			order = append(order, key)
		}
		for _, p := range item.Perms {
			g.perms[p] = true
		}
		for _, mm := range item.Models {
			g.models[mm] = true
		}
		for _, s := range item.Sources {
			g.sources[s] = true
		}
		for _, surface := range item.Surfaces {
			g.surfs[surface] = true
		}
	}
	out := make([]ExportItem, 0, len(order))
	for _, key := range order {
		g := byKey[key]
		g.item.Perms = sortedSetOrNil(g.perms)
		g.item.Models = sortedSetOrNil(g.models)
		g.item.Sources = sortedSetOrNil(g.sources)
		g.item.Surfaces = sortedSetOrNil(g.surfs)
		out = append(out, g.item)
	}
	return out
}

// sortedSetOrNil keeps a merged item's absent list nil (not empty) so an
// unsupported item round-trips exactly as its source rows declared it.
func sortedSetOrNil(set map[string]bool) []string {
	if len(set) == 0 {
		return nil
	}
	return mapKeys(set)
}

func actionsForItem(item ExportItem, m ExportMapping) ([]string, string) {
	switch strings.TrimSpace(item.Kind) {
	case exportKindGrant:
		actions := unionMappedActions(item.Perms, m.PermActions)
		if len(actions) == 0 {
			return nil, reasonNoActionMapping
		}
		return actions, ""
	case exportKindModelAccess:
		actions := unionMappedActions(item.Models, m.ModelActions)
		if len(actions) == 0 {
			return nil, reasonNoActionMapping
		}
		return actions, ""
	case exportKindSourceScope:
		switch strings.TrimSpace(item.Access) {
		case "r":
			actions := make(map[string]bool)
			for _, source := range item.Sources {
				mapped, ok := m.SourceReadActions[source]
				if !ok || len(mapped) == 0 {
					return nil, reasonNoReadOnlyActions
				}
				for _, action := range mapped {
					if action != "" {
						actions[action] = true
					}
				}
			}
			out := mapKeys(actions)
			if len(out) == 0 {
				return nil, reasonNoReadOnlyActions
			}
			return out, ""
		case "rw":
			actions := unionMappedActions(item.Sources, m.SourceActions)
			if len(actions) == 0 {
				return nil, reasonNoActionMapping
			}
			return actions, ""
		default:
			return nil, reasonBadSourceAccess
		}
	default:
		return nil, reasonBadKind
	}
}

func unionMappedActions(keys []string, mapping map[string][]string) []string {
	set := make(map[string]bool)
	for _, key := range keys {
		for _, action := range mapping[key] {
			if action != "" {
				set[action] = true
			}
		}
	}
	return mapKeys(set)
}

func mapKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func renderAgentCoreStatement(effect string, actions []string, gatewayARN string, claim ClaimBinding) string {
	var b strings.Builder
	b.WriteString(effect)
	b.WriteString("(\n")
	b.WriteString("  principal is AgentCore::OAuthUser,\n")
	b.WriteString("  action in [")
	for i, action := range actions {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("AgentCore::Action::")
		b.WriteString(cedarStr(action))
	}
	b.WriteString("],\n")
	b.WriteString("  resource == AgentCore::Gateway::")
	b.WriteString(cedarStr(gatewayARN))
	b.WriteString("\n")
	b.WriteString(") when { principal.getTag(")
	b.WriteString(cedarStr(claim.Tag))
	b.WriteString(") == ")
	b.WriteString(cedarStr(claim.Value))
	b.WriteString(" };\n")
	return b.String()
}

func exportPolicyName(item ExportItem, gatewayARN, effect string) string {
	return "olv_" + tenantSlug(item.Tenant) + "_" + kindCode(item.Kind) + "_" + exportIdentityHash(item, gatewayARN, effect)[:10]
}

func exportIdentityHash(item ExportItem, gatewayARN, effect string) string {
	// ScopeKind and Access are part of the identity: tenant/workspace/agent-group
	// scope and an "r" vs "rw" projection are DIFFERENT remote policies. Without
	// those fields they would collide on one name and the planner would silently
	// keep one.
	return hashLengthPrefixed(
		exportPolicyNameVersion,
		strings.TrimSpace(item.Kind),
		item.Tenant,
		item.SubjectKind,
		item.SubjectRef,
		strings.ToLower(strings.TrimSpace(item.ScopeKind)),
		item.Workspace,
		gatewayARN,
		effect,
		strings.TrimSpace(item.Access),
	)
}

func kindCode(kind string) string {
	switch strings.TrimSpace(kind) {
	case exportKindGrant:
		return "g"
	case exportKindModelAccess:
		return "ma"
	case exportKindSourceScope:
		return "ss"
	default:
		return "x"
	}
}

func tenantSlug(tenant string) string {
	var b strings.Builder
	for _, r := range tenant {
		if b.Len() >= 16 {
			break
		}
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

func exportMarker(tenant, statement string) string {
	return "olivares-export v1 tenant=" + tenant + " sha256=" + sha256Hex(statement)
}

func parseExportMarker(desc string) (tenant, fingerprint string, ok bool) {
	const prefix = "olivares-export v1 tenant="
	const hashSep = " sha256="
	if !strings.HasPrefix(desc, prefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(desc, prefix)
	idx := strings.LastIndex(rest, hashSep)
	if idx < 0 {
		return "", "", false
	}
	tenant = rest[:idx]
	fingerprint = rest[idx+len(hashSep):]
	if !hex64(fingerprint) {
		return "", "", false
	}
	return tenant, fingerprint, true
}

func hex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

func subjectClaimKey(kind, ref string) string {
	return kind + ":" + ref
}

func normalizeEnforcementMode(mode string) string {
	switch strings.ToUpper(strings.TrimSpace(mode)) {
	case enforcementModeLogOnly:
		return enforcementModeLogOnly
	default:
		return enforcementModeActive
	}
}

func appendUnsupported(out []UnsupportedItem, item ExportItem, reason string) []UnsupportedItem {
	return append(out, UnsupportedItem{Item: item, Reason: reason})
}

func unsupportedSortKey(u UnsupportedItem) string {
	parts := []string{
		u.Reason,
		u.Item.Tenant,
		u.Item.Kind,
		u.Item.SubjectKind,
		u.Item.SubjectRef,
		strings.ToLower(strings.TrimSpace(u.Item.ScopeKind)),
		u.Item.Workspace,
		u.Item.Effect,
		u.Item.Access,
		strings.Join(u.Item.Perms, "\x00"),
		strings.Join(u.Item.Models, "\x00"),
		strings.Join(u.Item.Sources, "\x00"),
		strings.Join(u.Item.Surfaces, "\x00"),
	}
	return strings.Join(parts, "\x01")
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// cedarStr renders s as a Cedar double-quoted string literal, escaping
// backslash and quote exactly like modules/governance/scopedadmin.go. The
// connector copies this tiny routine to preserve the Apache/AGPL boundary.
func cedarStr(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func hashLengthPrefixed(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		writeLengthPrefixedHashPart(h, part)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func writeLengthPrefixedHashPart(h interface{ Write([]byte) (int, error) }, part string) {
	var lenbuf [8]byte
	n := len(part)
	for i := 0; i < 8; i++ {
		lenbuf[i] = byte(n >> (8 * (7 - i)))
	}
	_, _ = h.Write(lenbuf[:])
	_, _ = h.Write([]byte(part))
}
