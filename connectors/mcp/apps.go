// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/connectors/internal/textscan"
	"github.com/olivaresai/olivares/sdk/model"
)

// apps.go governs MCP APPS (SEP-1865, the first official MCP extension; stable
// 2026-01-26 — VERIFIED 2026-06-10 against modelcontextprotocol/ext-apps
// specification/2026-01-26/apps.mdx) as a GOVERNED RESOURCE CLASS (CUR-7). An MCP App is interactive UI a server ships as an HTML template under
// the reserved `ui://` scheme; the host renders it sandboxed and the embedded
// View talks JSON-RPC over postMessage — including tools/call — making every
// template an actuation surface the plane must govern, not just render.
//
// Two halves, mirroring the rest of this connector:
//
//   - DETECTIVE (appsFindings, wired into Gather): inventories the ui://
//     templates a server actually exposes (resources/list + each tool's
//     `_meta.ui.resourceUri` — the spec lets servers omit UI-only resources
//     from resources/list, so tool metadata is also authoritative discovery),
//     diffs them against the operator's PRE-DECLARED inventory (serverSpec
//     ui_templates), and checks spec conformance: the exact
//     `text/html;profile=mcp-app` mimeType (the proposal-era `text/html+mcp`
//     was superseded before GA), the deprecated flat `ui/resourceUri` key,
//     app-only tools (visibility ["app"] — excluded from the model's list,
//     callable only from UI), over-broad CSP domain declarations and sensitive
//     device permissions.
//   - PREVENTIVE (the RS PEP, rs.go): `resources/read` of a ui:// URI — the
//     RENDER moment — resolves against the server-owned UI-template policy
//     (newAppSet): an UNDECLARED template is DENIED (deny-closed, like the
//     toolset), a template marked require_consent is denied until the wired
//     ConsentStore records the subject's consent, and every decision is
//     audited (GateAuditor) — the auditable trail for the postMessage surface
//     the plane cannot see directly (the iframe↔host channel is client-side;
//     what crosses the wire is the render fetch and the resulting tool calls,
//     and BOTH are gated/audited here).
//
// MINIMAL-DATA: findings carry sanitized URIs + hashed details. The template
// HTML itself is never fetched by the detective (resources/read is the host's
// render path, not introspection). Adds content inspection of the HTML
// via the RenderInspector seam in handleUIRead (rs.go).
//
// OPENAI APPS SDK (verification): the OpenAI Apps SDK builds UI over
// MCP — it uses the same ui:// scheme, the same text/html;profile=mcp-app
// mimeType, and the same resources/read + tools/call surface. The detective
// and render-gate cover it as a first-class MCP citizen. If the SDK were to
// diverge in wire format (a different mimeType or _meta structure), the
// conformance checks below would surface the drift as a finding, and the
// render-gate's deny-closed inventory would gate its templates identically.
// No separate connector or conformance rule is needed.

// SEP-1865 wire vocabulary (VERIFIED 2026-06-10).
const (
	// uiScheme is the reserved URI scheme for MCP App templates.
	uiScheme = "ui://"
	// appMimeType is the REQUIRED template mimeType in the stable extension
	// ("other types reserved for future extensions").
	appMimeType = "text/html;profile=mcp-app"
	// preGAMimeType is the proposal-era mimeType (2025-11-21 announcement),
	// superseded before the 2026-01-26 GA.
	preGAMimeType = "text/html+mcp"
	// deprecatedFlatUIKey is the pre-GA flat tool-meta key replaced by the
	// nested `_meta.ui.resourceUri` ("will be removed before GA").
	deprecatedFlatUIKey = "ui/resourceUri"
)

// findingApp marks an MCP Apps governance finding (inventory drift + spec
// conformance), alongside the finding kinds in mcp.go.
const findingApp = "mcp_app"

// toolUIMeta is a tool's parsed `_meta.ui` (SEP-1865 McpUiToolMeta).
type toolUIMeta struct {
	ResourceURI string   `json:"resourceUri"`
	Visibility  []string `json:"visibility"`
}

// resourceUIMeta is a resource's parsed `_meta.ui` (SEP-1865 UIResourceMeta) —
// the sandbox posture a template declares.
type resourceUIMeta struct {
	CSP *struct {
		ConnectDomains  []string `json:"connectDomains"`
		ResourceDomains []string `json:"resourceDomains"`
		FrameDomains    []string `json:"frameDomains"`
		BaseURIDomains  []string `json:"baseUriDomains"`
	} `json:"csp"`
	Permissions *struct {
		Camera         bool `json:"camera"`
		Microphone     bool `json:"microphone"`
		Geolocation    bool `json:"geolocation"`
		ClipboardWrite bool `json:"clipboardWrite"`
	} `json:"permissions"`
}

// parseToolUIMeta extracts the SEP-1865 ui block (and the deprecated flat key)
// from a tool's raw `_meta`. Malformed metadata yields zero values — the
// catalog is untrusted; a parse failure is simply "no declared UI link".
func parseToolUIMeta(raw json.RawMessage) (ui toolUIMeta, flatKey bool) {
	if len(raw) == 0 {
		return ui, false
	}
	var meta map[string]json.RawMessage
	if json.Unmarshal(raw, &meta) != nil {
		return ui, false
	}
	if v, ok := meta[deprecatedFlatUIKey]; ok {
		flatKey = true
		var uri string
		if json.Unmarshal(v, &uri) == nil && ui.ResourceURI == "" {
			ui.ResourceURI = uri
		}
	}
	if v, ok := meta["ui"]; ok {
		var nested toolUIMeta
		if json.Unmarshal(v, &nested) == nil {
			if nested.ResourceURI != "" {
				ui.ResourceURI = nested.ResourceURI
			}
			ui.Visibility = nested.Visibility
		}
	}
	return ui, flatKey
}

// parseResourceUIMeta extracts a resource's `_meta.ui` sandbox declarations.
func parseResourceUIMeta(raw json.RawMessage) resourceUIMeta {
	var out resourceUIMeta
	if len(raw) == 0 {
		return out
	}
	var meta struct {
		UI json.RawMessage `json:"ui"`
	}
	if json.Unmarshal(raw, &meta) != nil || len(meta.UI) == 0 {
		return out
	}
	_ = json.Unmarshal(meta.UI, &out)
	return out
}

// appOnly reports whether a tool's visibility is exactly app-only (the model
// must never see it in tools/list; only the rendered View may call it).
func appOnly(visibility []string) bool {
	if len(visibility) == 0 {
		return false // default is ["model","app"]
	}
	for _, v := range visibility {
		if v == "model" {
			return false
		}
	}
	return true
}

// appsFindings inventories and grades one introspected server's MCP Apps
// surface. Pure function of (spec, cat) — no network, UNTRUSTED metadata in,
// minimal-data findings out.
func appsFindings(spec serverSpec, cat catalog, at time.Time) []model.FindingReport {
	server := spec.Name
	var out []model.FindingReport
	add := func(sev model.Severity, title, key string) {
		out = append(out, model.FindingReport{
			Kind:        findingApp,
			Severity:    sev,
			SubjectKind: subjectMCPServer,
			SubjectRef:  server,
			Title:       title,
			DetailHash:  redact.Hash("mcp-app server=" + server + " " + key),
			OccurredAt:  at,
		})
	}

	declared := map[string]bool{}
	for _, u := range spec.UITemplates {
		if u = strings.TrimSpace(u); u != "" {
			declared[u] = true
		}
	}

	// Observed templates: ui:// resources + every tool's _meta.ui.resourceUri
	// (the spec allows UI-only resources to be omitted from resources/list).
	observed := map[string]string{} // uri → where it was seen
	for _, r := range cat.resources {
		uri := strings.TrimSpace(r.URI)
		if !hasUIScheme(uri) {
			// A non-ui:// resource claiming the app mimeType sidesteps the
			// reserved-scheme inventory — app content outside governance.
			if mimeIsApp(r.MimeType) {
				add(model.SeverityLow,
					"resource "+quoteURI(r.URI)+" carries the MCP App mimeType outside the reserved ui:// scheme",
					"app-mime-outside-scheme uri="+textscan.SanitizeDisplay(r.URI))
			}
			continue
		}
		if _, dup := observed[uri]; !dup {
			observed[uri] = "resources/list"
		}
		out = append(out, uiResourceFindings(server, r, at)...)
	}
	for _, t := range cat.tools {
		ui, flatKey := parseToolUIMeta(t.Meta)
		elem := "tool " + textscan.SanitizeDisplay(firstNonEmpty(t.Name, t.Title))
		if flatKey {
			add(model.SeverityLow,
				elem+" uses the deprecated pre-GA flat _meta[\""+deprecatedFlatUIKey+"\"] key (superseded by _meta.ui.resourceUri at the 2026-01-26 GA)",
				"deprecated-flat-ui-key tool="+textscan.SanitizeDisplay(t.Name))
		}
		if uri := strings.TrimSpace(ui.ResourceURI); uri != "" {
			if !hasUIScheme(uri) {
				add(model.SeverityMedium,
					elem+" declares a UI template outside the reserved ui:// scheme: "+quoteURI(uri),
					"tool-ui-bad-scheme tool="+textscan.SanitizeDisplay(t.Name))
			} else if _, dup := observed[uri]; !dup {
				observed[uri] = "tool _meta"
			}
		}
		if appOnly(ui.Visibility) {
			if textscan.LooksExecutional(t.Name, t.Description) {
				add(model.SeverityMedium,
					elem+" is APP-ONLY (hidden from the model's tools/list) and exposes an exec surface — actuation reachable only from rendered UI",
					"app-only-exec tool="+textscan.SanitizeDisplay(t.Name))
			} else {
				add(model.SeverityInfo,
					elem+" is APP-ONLY (visibility [app]: excluded from the model's tools/list; callable only from the rendered View)",
					"app-only tool="+textscan.SanitizeDisplay(t.Name))
			}
		}
	}
	// A ui:// URI offered as a resource TEMPLATE: SEP-1865 predeclares CONCRETE
	// resources; a parameterized ui:// template defeats hash-allowlisting.
	for _, tpl := range cat.templates {
		if hasUIScheme(strings.TrimSpace(tpl.URITemplate)) {
			add(model.SeverityMedium,
				"resource template "+quoteURI(tpl.URITemplate)+" parameterizes the reserved ui:// scheme (SEP-1865 predeclares concrete templates; a parameterized one defeats pre-declaration)",
				"ui-in-template uri="+textscan.SanitizeDisplay(tpl.URITemplate))
		}
	}

	// PERMITTED-vs-OBSERVED inventory diff.
	switch {
	case len(declared) > 0:
		for _, uri := range sortedKeysOf(observed) {
			if !declared[uri] {
				add(model.SeverityHigh,
					"UNDECLARED ui:// template observed via "+observed[uri]+": "+quoteURI(uri)+" — not in the pre-declared inventory (the RS PEP denies its render)",
					"undeclared-template uri="+textscan.SanitizeDisplay(uri))
			}
		}
		for _, uri := range sortedBoolKeys(declared) {
			if _, ok := observed[uri]; !ok {
				add(model.SeverityInfo,
					"pre-declared ui:// template not observed on the server: "+quoteURI(uri)+" (stale inventory entry)",
					"stale-template uri="+textscan.SanitizeDisplay(uri))
			}
		}
	case len(observed) > 0:
		add(model.SeverityMedium,
			"server exposes "+strconv.Itoa(len(observed))+" ui:// template(s) with NO pre-declared inventory configured — ungoverned UI surface (declare ui_templates to govern renders)",
			"no-declared-inventory count="+strconv.Itoa(len(observed)))
	}
	return out
}

// uiResourceFindings checks one ui:// resource's spec conformance and declared
// sandbox posture.
func uiResourceFindings(server string, r Resource, at time.Time) []model.FindingReport {
	var out []model.FindingReport
	add := func(sev model.Severity, title, key string) {
		out = append(out, model.FindingReport{
			Kind:        findingApp,
			Severity:    sev,
			SubjectKind: subjectMCPServer,
			SubjectRef:  server,
			Title:       title,
			DetailHash:  redact.Hash("mcp-app server=" + server + " " + key),
			OccurredAt:  at,
		})
	}
	elem := "ui template " + quoteURI(r.URI)

	switch mime := strings.TrimSpace(r.MimeType); {
	case mimeIsApp(mime):
		// conformant
	case strings.EqualFold(mime, preGAMimeType):
		add(model.SeverityMedium,
			elem+" declares the proposal-era mimeType text/html+mcp (superseded before the 2026-01-26 GA; hosts expect "+appMimeType+")",
			"pre-ga-mime uri="+textscan.SanitizeDisplay(r.URI))
	default:
		add(model.SeverityMedium,
			elem+" does not declare the required mimeType "+appMimeType,
			"bad-mime uri="+textscan.SanitizeDisplay(r.URI)+" mime="+textscan.SanitizeDisplay(mime))
	}

	ui := parseResourceUIMeta(r.Meta)
	if ui.CSP != nil {
		wildcard := func(domains []string, field string) {
			for _, d := range domains {
				if strings.TrimSpace(d) == "*" {
					add(model.SeverityMedium,
						elem+" declares a WILDCARD "+field+" CSP domain — the sandbox may reach any origin (exfiltration surface)",
						"csp-wildcard uri="+textscan.SanitizeDisplay(r.URI)+" field="+field)
					return
				}
			}
		}
		wildcard(ui.CSP.ConnectDomains, "connect")
		wildcard(ui.CSP.ResourceDomains, "resource")
		wildcard(ui.CSP.FrameDomains, "frame")
		wildcard(ui.CSP.BaseURIDomains, "base-uri")
	}
	if p := ui.Permissions; p != nil {
		var requested []string
		for _, perm := range []struct {
			on   bool
			name string
		}{{p.Camera, "camera"}, {p.Microphone, "microphone"}, {p.Geolocation, "geolocation"}, {p.ClipboardWrite, "clipboardWrite"}} {
			if perm.on {
				requested = append(requested, perm.name)
			}
		}
		if len(requested) > 0 {
			add(model.SeverityInfo,
				elem+" requests sensitive device permission(s): ["+strings.Join(requested, ",")+"]",
				"sensitive-permissions uri="+textscan.SanitizeDisplay(r.URI)+" perms="+strings.Join(requested, ","))
		}
	}
	return out
}

// mimeIsApp reports whether a mimeType is the required SEP-1865 value
// (whitespace-tolerant around the profile parameter; otherwise exact).
func mimeIsApp(mime string) bool {
	return strings.EqualFold(strings.ReplaceAll(strings.TrimSpace(mime), " ", ""), appMimeType)
}

// hasUIScheme reports whether a URI carries the reserved ui:// scheme,
// CASE-INSENSITIVELY (RFC 3986 §3.1: scheme comparison is case-insensitive —
// "UI://x" is the same scheme, and a gate matching only the lowercase spelling
// would be bypassable). Detection is case-insensitive; the DECLARED inventory
// stays canonical lowercase (newAppSet), so a case-variant template never
// matches a declared entry and denies — fail-closed, never fail-open.
func hasUIScheme(uri string) bool {
	return len(uri) >= len(uiScheme) && strings.EqualFold(uri[:len(uiScheme)], uiScheme)
}

// quoteURI renders a sanitized, quoted URI for a finding title.
func quoteURI(uri string) string {
	return strconv.Quote(textscan.SanitizeDisplay(uri))
}

func sortedKeysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedBoolKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// --- RS PEP side: the server-owned UI-template policy + consent seam -----------

// UITemplatePolicy is the operator-declared policy for ONE ui:// template the
// RS may serve: the pre-declared inventory entry (deny-by-default — a
// resources/read of an undeclared ui:// URI is refused, exactly like a tool
// absent from the Toolset) plus its consent requirement.
type UITemplatePolicy struct {
	URI string `json:"uri"`
	// RequireConsent gates the template's RENDER (the resources/read fetch) on a
	// recorded user consent for (subject, template) via the ConsentStore seam —
	// SEP-1865 leaves UI consent to host discretion; this RS makes it a policy
	// decision with a deny-closed default.
	RequireConsent bool `json:"require_consent"`
}

// appSet is the server-owned ui:// template policy map (nil ⇒ deny ALL ui://
// reads, the deny-closed default — a server that serves UI must declare it).
type appSet struct {
	byURI map[string]UITemplatePolicy
}

// newAppSet builds a validated template policy set. Every URI MUST carry the
// reserved ui:// scheme; a duplicate is a configuration error.
func newAppSet(policies []UITemplatePolicy) (*appSet, error) {
	if len(policies) == 0 {
		return nil, nil
	}
	as := &appSet{byURI: make(map[string]UITemplatePolicy, len(policies))}
	for _, p := range policies {
		uri := strings.TrimSpace(p.URI)
		if !strings.HasPrefix(uri, uiScheme) {
			return nil, &dispatchError{"mcp: rs: ui template policy URI " + strconv.Quote(uri) + " must use the reserved ui:// scheme"}
		}
		if _, dup := as.byURI[uri]; dup {
			return nil, &dispatchError{"mcp: rs: duplicate ui template policy for " + strconv.Quote(uri)}
		}
		p.URI = uri
		as.byURI[uri] = p
	}
	return as, nil
}

// resolve returns the policy for a template URI; ok=false (deny) when the set
// is nil or the URI is undeclared.
func (a *appSet) resolve(uri string) (UITemplatePolicy, bool) {
	if a == nil {
		return UITemplatePolicy{}, false
	}
	p, ok := a.byURI[uri]
	return p, ok
}

// ConsentStore is the consent-tracking seam for MCP App renders: it answers
// whether the subject has a RECORDED consent for rendering the template. The
// real adapter (the control plane's consent registry) is wired by the
// composition root; a connector cannot import the AGPL modules.
type ConsentStore interface {
	Granted(ctx context.Context, subject, templateURI string) (bool, error)
}

// denyConsentStore is the deny-closed default: with no store wired, every
// consent-gated template render is refused (never silently rendered).
type denyConsentStore struct{}

func (denyConsentStore) Granted(context.Context, string, string) (bool, error) {
	return false, nil
}
