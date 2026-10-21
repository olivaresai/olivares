// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/connectors/claude"
	"github.com/olivaresai/olivares/connectors/managedsettings"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk"
)

// PolicyConsole is the B authoring backend the Claude Code policy console
// already calls at /v1/m/claude-policy/{surface}/{validate,dry-run,publish,versions}.
// It is a route-only console module (it owns no store entities — it reads/writes the
// governance module's append-only policy_revision table via the shared Scope) so it
// can mount the hyphenated REST namespace the web declared. It imports the Apache
// connectors (managedsettings, claude) — the legal arrow module→connector — to reuse
// the VERIFIED render/drift/validation logic; it never re-implements it.
//
// EDITIONS: validate and dry-run are every edition's. Publish, the revision history, the
// signed pull, the attested check-in and the drift view are Business; Community mounts the
// same routes and answers 501 (claudepolicy_fleet_noenterprise.go).
//
// POSTURE (docs/SECURITY-HARDENING.md): authoring is EMISSION of policy, not autonomous enforcement.
// validate/dry-run are no-effect reads (read tier). publish is a privileged,
// CONFIRMED, AUDITED mutation (admin tier) that persists an immutable revision and
// hands distribution to the seam — it NEVER writes host files. Deny-closed: an
// invalid document, an inline credential, or any persist/audit error does NOT publish.
//
// Closed the truth loop behind both seams (the Business overlay): publish
// hands the rendered bytes to the PolicyArtifactDistributor (signed artifact in
// the plane + agent PULL with attestation — the decided v1 mechanism; deny-closed
// on any failure), agents check in their OBSERVED config, and drift is computed
// with the verified connector logic and recorded as REAL findings.
type PolicyConsole struct {
	log         *slog.Logger
	data        api.ModuleData
	host        sdk.Host
	clock       model.Clock
	distributor ManagedDistributor
	observed    ObservedConfigProvider
}

// Compile-time proof PolicyConsole satisfies the SDK + API seams.
var (
	_ sdk.Module       = (*PolicyConsole)(nil)
	_ api.Module       = (*PolicyConsole)(nil)
	_ api.DataConsumer = (*PolicyConsole)(nil)
)

// PolicyConsoleNamespace mounts the console at /v1/m/claude-policy/.
const PolicyConsoleNamespace = "claude-policy"

// ManagedDistributor is the distribution seam. publish hands the rendered policy
// to it; the MATERIAL per-host write is deploy/VII, NEVER this authoring path. nil ⇒
// distribution is honestly "declared, not actuated" (the revision is still published).
type ManagedDistributor interface {
	// Distribute enqueues a published revision for distribution behind the seam. It
	// returns an error only on a genuine enqueue failure (which does NOT roll back the
	// already-persisted revision; the response reports the distribution status).
	Distribute(ctx context.Context, tenant model.TenantID, surface, scope string, revision int64, rendered []byte) error
}

// ObservedConfig is one OBSERVED (live host) configuration for a surface: the
// raw document a scope (host id / org-distribution name) reports, as recorded by
// the attested check-in (the Business overlay).
type ObservedConfig struct {
	Scope      string
	Content    []byte
	ObservedAt string // store timestamp of the observation (informational)
}

// ObservedConfigProvider returns every OBSERVED host configuration for a surface
// (one entry per scope) so publish/dry-run can run the PERMITTED-policy-vs-
// OBSERVED-config drift check fleet-wide. nil provider or an empty slice ⇒ no
// observation is available (honest: drift is not computed, the response says so);
// an error ⇒ the source could not answer (reported as such) — NEVER a fabricated
// "no drift / compliant".
type ObservedConfigProvider interface {
	Observed(ctx context.Context, tenant model.TenantID, surface string) ([]ObservedConfig, error)
}

// PolicyConsoleOption configures a PolicyConsole.
type PolicyConsoleOption func(*PolicyConsole)

// WithManagedDistributor wires the distribution seam.
func WithManagedDistributor(d ManagedDistributor) PolicyConsoleOption {
	return func(c *PolicyConsole) { c.distributor = d }
}

// WithObservedConfig wires the OBSERVED-config provider for publish-time drift.
func WithObservedConfig(o ObservedConfigProvider) PolicyConsoleOption {
	return func(c *PolicyConsole) { c.observed = o }
}

// WithPolicyConsoleClock overrides the clock (tests inject a deterministic clock).
func WithPolicyConsoleClock(clk model.Clock) PolicyConsoleOption {
	return func(c *PolicyConsole) { c.clock = clk }
}

// NewPolicyConsole constructs the managed-* authoring console.
func NewPolicyConsole(opts ...PolicyConsoleOption) *PolicyConsole {
	c := &PolicyConsole{clock: model.SystemClock{}}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Descriptor / lifecycle (sdk.Module).
func (c *PolicyConsole) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name: "olivares.claude-policy", Version: "0.1.0", APIVersion: sdk.APIVersion,
		Type: sdk.TypeModule, Title: "Claude Code managed-policy authoring",
		Description: "Versioned validate/dry-run/publish authoring for the Claude Code managed-* surfaces (managed-settings, hooks, managed-mcp, sandbox), over the same API the console calls.",
	}
}

func (c *PolicyConsole) UseData(d api.ModuleData) { c.data = d }
func (c *PolicyConsole) Init(_ context.Context, host sdk.Host) error {
	c.log = host.Logger()
	// kept for the post-commit finding.reported emission of drift findings
	// (the bus half; persistence is direct — security only bridges ≥High).
	c.host = host
	return nil
}
func (c *PolicyConsole) Start(context.Context) error {
	if c.data == nil && c.log != nil {
		c.log.Warn("claude-policy: started without a data handle; authoring will not persist")
	}
	return nil
}
func (c *PolicyConsole) Stop(context.Context) error { return nil }

// APINamespace mounts under /v1/m/claude-policy/.
func (c *PolicyConsole) APINamespace() string { return PolicyConsoleNamespace }

// Permissions: read gates view/dry-run/validate/versions plus the agent-facing
// artifact pull and the distribution truth view; write gates the distribution
// agent's attested check-in (an editor-tier machine token, never publish rights);
// admin gates publish. The permission namespace is "governance" (the RBAC verbs
// the console mirrors); the engine grants module permissions by VERB tier
// regardless of the route namespace.
func (c *PolicyConsole) Permissions() []auth.Permission {
	return []auth.Permission{permClaudePolicyRead, permClaudePolicyWrite, permClaudePolicyAdmin}
}

// APIRoutes mounts the managed-* authoring reads the web already calls (validate,
// dry-run) plus the fleet routes (publish, versions, signed-artifact pull, attested
// check-in, distribution truth view). The fleet routes are Business: Community answers
// them 501 (claudepolicy_fleet_noenterprise.go).
func (c *PolicyConsole) APIRoutes(reg api.RouteRegistrar) {
	reg.Handle("POST", "/{surface}/validate", permClaudePolicyRead, c.handleValidate)
	reg.Handle("POST", "/{surface}/dry-run", permClaudePolicyRead, c.handleDryRun)
	c.registerFleetRoutes(reg)
}

// --- request/response DTOs (mirror the frontend) -----------------------------

type contentBody struct {
	Content string `json:"content"`
	Note    string `json:"note,omitempty"`
}

type validateResult struct {
	OK          bool           `json:"ok"`
	Diagnostics []validateDiag `json:"diagnostics"`
	Surface     string         `json:"surface"`
	Notes       []string       `json:"notes,omitempty"`
}

type validateDiag struct {
	Message  string `json:"message"`
	Severity string `json:"severity"` // error | warning
}

type dryRunChange struct {
	Path   string `json:"path"`
	Before any    `json:"before,omitempty"`
	After  any    `json:"after,omitempty"`
}

type dryRunResult struct {
	Surface  string                       `json:"surface"`
	Resolved []managedsettings.DryRunLine `json:"resolved,omitempty"`
	Changes  []dryRunChange               `json:"changes,omitempty"`
	Notes    []string                     `json:"notes,omitempty"`
}

// validSurface reports whether s is a managed-* authoring surface of the
// revision store's registry.
func validSurface(s string) bool {
	e, ok := surfaces.base(s)
	return ok && e.family == surfaceFamilyManaged
}

// surfaceOf resolves + validates the {surface} path param, writing a 400 on an
// unknown surface and returning ok=false.
func surfaceOf(w http.ResponseWriter, r *http.Request) (string, bool) {
	s := chi.URLParam(r, "surface")
	if !validSurface(s) {
		writeJSON(w, http.StatusBadRequest, errorBody("unknown surface (want managed-settings|hooks|managed-mcp|sandbox)"))
		return "", false
	}
	return s, true
}

// --- handlers ----------------------------------------------------------------

// handleValidate validates a document server-side (defense in depth — the UI is never
// the security boundary). No effect, no persistence; read tier.
func (c *PolicyConsole) handleValidate(w http.ResponseWriter, r *http.Request, _ api.ModuleContext) {
	surface, ok := surfaceOf(w, r)
	if !ok {
		return
	}
	var in contentBody
	if !decodeJSON(w, r, &in) {
		return
	}
	issues, notes := validateSurfaceContent(surface, in.Content)
	res := validateResult{OK: len(issues) == 0, Surface: surface, Notes: notes, Diagnostics: []validateDiag{}}
	for _, msg := range issues {
		res.Diagnostics = append(res.Diagnostics, validateDiag{Message: msg, Severity: "error"})
	}
	writeJSON(w, http.StatusOK, res)
}

// handleDryRun previews the resolved precedence/effect WITHOUT publishing or touching
// any host. Read tier. For managed-settings it resolves the verified no-merge tier
// precedence and diffs against the observed config when available.
func (c *PolicyConsole) handleDryRun(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	surface, ok := surfaceOf(w, r)
	if !ok {
		return
	}
	var in contentBody
	if !decodeJSON(w, r, &in) {
		return
	}
	if issues, _ := validateSurfaceContent(surface, in.Content); len(issues) > 0 {
		writeJSON(w, http.StatusBadRequest, errorBody("document is invalid: "+strings.Join(issues, "; ")))
		return
	}
	out := dryRunResult{Surface: surface, Changes: []dryRunChange{}, Notes: []string{}}
	switch surface {
	case surfaceManagedSettings:
		nonEmpty := managedsettings.HasAnyKeys([]byte(in.Content))
		out.Resolved = managedsettings.DeliveryPreview(nonEmpty)
		if !nonEmpty {
			out.Notes = append(out.Notes, "this document delivers no keys — it would leave the managed tier empty")
		}
		// Diff vs every observed host config when an observation source is wired.
		if c.observed != nil {
			observations, oerr := c.observed.Observed(r.Context(), mc.Tenant, surface)
			switch {
			case oerr != nil:
				out.Notes = append(out.Notes, "observed-config source could not be read — precedence resolved, effect not diffed")
			case len(observations) == 0:
				out.Notes = append(out.Notes, "no observed host config available — precedence resolved, effect not diffed")
			default:
				at := c.clock.Now().Time()
				diffed := 0
				for _, obs := range observations {
					findings, derr := managedsettings.VerifyDriftJSON(obs.Scope, []byte(in.Content), obs.Content, at)
					if derr != nil {
						// Only a malformed AUTHORED doc errs (unreachable after the
						// validation above) — named per scope, never counted as diffed.
						out.Notes = append(out.Notes, "diff not computed for scope "+obs.Scope+": "+derr.Error())
						continue
					}
					diffed++
					for _, f := range findings {
						out.Changes = append(out.Changes, dryRunChange{Path: obs.Scope + " " + f.SubjectKind + ":" + f.Title})
					}
				}
				out.Notes = append(out.Notes, "diff computed against "+itoa(diffed)+" observed scope(s) (no host was written)")
			}
		} else {
			out.Notes = append(out.Notes, "no observed-config source in this edition — precedence resolved, effect not diffed")
		}
	default:
		out.Notes = append(out.Notes, "validated structurally; "+surface+" is authored at the highest managed tier (above CLI/local/project/user). No host is written by a dry-run.")
	}
	writeJSON(w, http.StatusOK, out)
}

// --- per-surface validation (server-side authoritative, forward-compatible) ---

// validateSurfaceContent runs the verified, server-side validation for a surface and
// returns (issues, notes). It is forward-compatible (it does not reject unknown keys —
// Claude Code adds keys frequently) but catches structural errors and the verified
// PermissionRequest correction.
func validateSurfaceContent(surface, content string) (issues, notes []string) {
	if strings.TrimSpace(content) == "" {
		return []string{"document is empty"}, nil
	}
	switch surface {
	case surfaceManagedSettings:
		return managedsettings.ValidateJSON([]byte(content)), nil
	case surfaceHooks:
		return validateHooksContent(content)
	case surfaceManagedMCP:
		return validateMCPContent(content)
	case surfaceSandbox:
		return validateSandboxContent(content)
	default:
		return []string{"unknown surface"}, nil
	}
}

// validateHooksContent structurally validates a hooks config and applies the VERIFIED
// PermissionRequest correction: the permission-rule persistence field
// (applyPermissionRule) belongs to PermissionRequest, NOT PreToolUse. Unknown event
// names are a WARNING (note), never a hard error (the event set is version-dated; see
//).
func validateHooksContent(content string) (issues, notes []string) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &root); err != nil {
		return []string{"hooks config must be a JSON object: " + err.Error()}, nil
	}
	hooksRaw, ok := root["hooks"]
	if !ok {
		// The document may BE the hooks map directly.
		hooksRaw = json.RawMessage(content)
	}
	var hooksMap map[string]json.RawMessage
	if err := json.Unmarshal(hooksRaw, &hooksMap); err != nil {
		return []string{"`hooks` must be an object mapping event names to matcher arrays"}, nil
	}
	for event := range hooksMap {
		if !claude.IsKnownHook(event) {
			notes = append(notes, "hook event "+event+" is not in the connector's recognized set (verified 2026-06-08); it is accepted and observed, but verify it against code.claude.com/docs/en/hooks")
		}
	}
	// The applyPermissionRule field is PermissionRequest-only (verified correction). A
	// PreToolUse block that tries to use it is misconfigured.
	if pre, ok := hooksMap["PreToolUse"]; ok && strings.Contains(string(pre), "applyPermissionRule") {
		issues = append(issues, "applyPermissionRule is a PermissionRequest-only output field; it has no effect under PreToolUse (PreToolUse uses decision.behavior + updatedInput)")
	}
	return issues, notes
}

// validateMCPContent structurally validates a managed-mcp.json document.
func validateMCPContent(content string) (issues, notes []string) {
	var doc struct {
		AllowedMcpServers []json.RawMessage `json:"allowedMcpServers"`
		DeniedMcpServers  []json.RawMessage `json:"deniedMcpServers"`
	}
	dec := json.NewDecoder(strings.NewReader(content))
	if err := dec.Decode(&doc); err != nil {
		return []string{"managed-mcp must be a JSON object with allowedMcpServers/deniedMcpServers arrays: " + err.Error()}, nil
	}
	check := func(entries []json.RawMessage, field string) {
		for i, raw := range entries {
			var m map[string]any
			if json.Unmarshal(raw, &m) != nil {
				issues = append(issues, field+"["+itoa(i)+"] is not an object")
				continue
			}
			if m["serverName"] == nil && m["serverUrl"] == nil && m["serverCommand"] == nil {
				issues = append(issues, field+"["+itoa(i)+"] must carry one of serverName|serverUrl|serverCommand")
			}
		}
	}
	check(doc.AllowedMcpServers, "allowedMcpServers")
	check(doc.DeniedMcpServers, "deniedMcpServers")
	if len(doc.AllowedMcpServers) == 0 && len(doc.DeniedMcpServers) == 0 {
		notes = append(notes, "no allowed/denied MCP servers declared (the document governs nothing)")
	}
	return issues, notes
}

// validateSandboxContent structurally validates a sandbox config document.
func validateSandboxContent(content string) (issues, notes []string) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &obj); err != nil {
		return []string{"sandbox config must be a JSON object: " + err.Error()}, nil
	}
	// The egress allowlist is deny-by-default; surface the verified domain-fronting
	// caveat as a note so the operator knows the standard proxy does not inspect TLS.
	if _, ok := obj["network"]; ok {
		notes = append(notes, "sandbox egress is deny-by-default; the built-in proxy decides on the client-supplied hostname and does NOT inspect TLS (domain-fronting caveat) — a custom TLS-terminating proxy is the stronger mitigation")
	}
	return issues, notes
}

// itoa renders an index for a diagnostic message.
func itoa(i int) string { return strconv.Itoa(i) }
