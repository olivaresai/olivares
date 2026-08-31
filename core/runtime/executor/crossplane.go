// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"time"
)

// CrossplaneBackend is the INTEROP backend (decision 3): it APPLIES a
// Composite Resource (XR) or Claim and READS its status, and lets the in-cluster
// Crossplane provider reconcile the real cloud infrastructure. It does NOT build a
// Crossplane control plane and does NOT manage Managed Resources (MRs) directly —
// the provider owns the MR lifecycle; this backend only declares the XR's desired
// spec and observes the XR/MR status the provider rolls up onto the XR.
//
// AN XR IS A KUBERNETES CUSTOM RESOURCE, so this backend reaches it via the K8s API
// at /apis/<group>/<version>/[namespaces/<ns>/]<plural>/<name>:
//
//   - Plan   GET the XR. 404 => create; present but the desired spec derived from
//     Desired differs => update; equal => empty Diff (idempotent noop).
//   - Apply  Server-Side Apply: PATCH .../<name>?fieldManager=<fm>&force=true with
//     Content-Type application/apply-patch+yaml (a JSON body is valid YAML and
//     is accepted by the apiserver). Idempotent — re-applying the same spec is
//     a noop server-side.
//   - DestroyPlan  if the XR exists => one delete ChangeItem (always Destructive).
//   - Apply(destroy)  DELETE the XR; the provider then tears down the backing MRs.
//   - Observe  GET the XR and read .status.conditions[] (Synced / Ready). Synced=True
//     & Ready=True => InSync; otherwise Drift describing the unready condition;
//     404 => Exists:false; unreachable => Observable:false (an HONEST gap).
//     The provider reconciles CONTINUOUSLY; observe reads MR/XR status only and
//     never mutates.
//
// CREDENTIALS (least privilege): every call runs under the short-lived, attested
// Credential the Executor mints. cred.Token is the K8s bearer token, set ONLY in the
// Authorization header (apiRequest.bearer) — never in a URL, an argv, a log, or a
// returned struct. The TLS floor is 1.2 with a pinned CA (tlsBearerClient); insecure
// is an explicit operator opt-in, never the default.
//
// MINIMAL DATA (docs/SECURITY-HARDENING.md): the desired XR spec body this backend builds may
// reference secrets only by the runtime's native mechanism (the provider resolves a
// connection secret by reference); the spec BODY is sent on the wire to the apiserver
// but is NEVER placed into a returned Diff/Result/RealState or a log line — those
// carry only a kind, a non-sensitive ref (the XR name) and a short detail.
type CrossplaneBackend struct {
	cfg    CrossplaneConfig
	client *http.Client
}

// CrossplaneConfig configures the interop backend (operator-provisioned, no secrets).
// The GVR (group/version/plural) and scope are fixed by the operator who provisioned
// the XRD, never inferred at runtime.
type CrossplaneConfig struct {
	// APIServer is the Kubernetes apiserver base URL (scheme+host), e.g.
	// "https://kube.internal:6443". The XR path is appended by the backend.
	APIServer string
	// CABundle pins the apiserver certificate (PEM). Empty + Insecure=false means the
	// system roots are used; Insecure=true skips verification (explicit opt-in only).
	CABundle []byte
	// Insecure skips TLS verification. Operator opt-in for a dev cluster only — never
	// the default; production pins CABundle.
	Insecure bool
	// APIGroup is the XR's API group, e.g. "platform.acme.io".
	APIGroup string
	// APIVersion is the XR's version, e.g. "v1alpha1".
	APIVersion string
	// Plural is the XR's plural resource name, e.g. "xagents" / "agentclaims".
	Plural string
	// Kind is the XR's Kind, e.g. "XAgent" (used in the apply body's "kind"). Optional;
	// when empty the apply body omits "kind" and relies on the path's GVR.
	Kind string
	// Namespaced is true for a namespaced Claim, false for a cluster-scoped XR.
	Namespaced bool
	// Namespace is the namespace for a namespaced XR/Claim (required when Namespaced).
	Namespace string
	// FieldManager is the Server-Side Apply field manager (default "olivares-deploy").
	FieldManager string
	// Timeout bounds a single apiserver call (default 30s).
	Timeout time.Duration
}

// crossplaneDefaultFieldManager is the Server-Side Apply field manager this backend
// owns. SSA conflicts are resolved with force=true (the deploy module is the declared
// owner of the XR spec it manages).
const crossplaneDefaultFieldManager = "olivares-deploy"

// crossplaneApplyContentType is the Server-Side Apply media type. A JSON document is
// valid YAML, so the JSON body the backend builds is accepted under this type.
const crossplaneApplyContentType = "application/apply-patch+yaml"

// crossplaneKind is the ChangeItem.Kind / RealState resource class for an XR.
const crossplaneKind = "crossplane.xr"

// NewCrossplaneBackend builds the interop backend. It constructs the TLS bearer
// client once from the operator's CA/insecure config; the client is overridable in
// tests (point cfg.APIServer at an httptest.Server and set .client). A bad CA bundle
// is surfaced lazily on first use so construction never panics — see resolveClient.
func NewCrossplaneBackend(cfg CrossplaneConfig) *CrossplaneBackend {
	if cfg.FieldManager == "" {
		cfg.FieldManager = crossplaneDefaultFieldManager
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	b := &CrossplaneBackend{cfg: cfg}
	// Build the client eagerly when the CA parses; an invalid CA leaves client nil and
	// resolveClient() returns the honest error on first call (never a silent fallback).
	if c, err := tlsBearerClient(cfg.CABundle, cfg.Insecure, cfg.Timeout); err == nil {
		b.client = c
	}
	return b
}

// Kind returns the runtime selector.
func (c *CrossplaneBackend) Kind() string { return "crossplane" }

// resolveClient returns the configured client or rebuilds it, surfacing an invalid CA
// as an honest error rather than acting without TLS verification.
func (c *CrossplaneBackend) resolveClient() (*http.Client, error) {
	if c.client != nil {
		return c.client, nil
	}
	cl, err := tlsBearerClient(c.cfg.CABundle, c.cfg.Insecure, c.cfg.Timeout)
	if err != nil {
		return nil, fmt.Errorf("executor: crossplane apiserver client unavailable: %w", err)
	}
	c.client = cl
	return cl, nil
}

// crossplaneXRPath builds the collection or named-resource path for the configured
// GVR and scope. name=="" yields the collection path. The XR name is path-escaped so
// it can never alter the request structure.
func (c *CrossplaneBackend) crossplaneXRPath(name string) (string, error) {
	if strings.TrimSpace(c.cfg.APIGroup) == "" || strings.TrimSpace(c.cfg.APIVersion) == "" || strings.TrimSpace(c.cfg.Plural) == "" {
		return "", errors.New("executor: crossplane GVR (APIGroup/APIVersion/Plural) is not configured")
	}
	var sb strings.Builder
	sb.WriteString("/apis/")
	sb.WriteString(c.cfg.APIGroup)
	sb.WriteString("/")
	sb.WriteString(c.cfg.APIVersion)
	if c.cfg.Namespaced {
		ns := strings.TrimSpace(c.cfg.Namespace)
		if ns == "" {
			return "", errors.New("executor: crossplane backend is Namespaced but no Namespace is configured")
		}
		sb.WriteString("/namespaces/")
		sb.WriteString(url.PathEscape(ns))
	}
	sb.WriteString("/")
	sb.WriteString(c.cfg.Plural)
	if name != "" {
		sb.WriteString("/")
		sb.WriteString(url.PathEscape(name))
	}
	return sb.String(), nil
}

// crossplaneXRName is the XR's metadata.name — the logical subject the Desired pins.
func crossplaneXRName(d Desired) string {
	if n := strings.TrimSpace(d.SubjectRef); n != "" {
		return n
	}
	return strings.TrimSpace(d.Name)
}

// crossplaneDesiredSpec derives the XR's .spec from the Desired. It carries only
// non-sensitive params (image, replicas, compute requests) and references secrets by
// the runtime's native mechanism (the provider resolves a connection secret by name);
// it NEVER embeds cleartext secret material. This body is sent to the apiserver but is
// never placed into a returned struct or log line (docs/SECURITY-HARDENING.md).
func crossplaneDesiredSpec(d Desired) map[string]any {
	params := map[string]any{}
	if d.Image != "" {
		params["image"] = d.Image
	}
	if d.Command != "" {
		params["command"] = d.Command
	}
	// replicas is ALWAYS emitted (default 1 per the module's spec contract: 0 = single
	// instance / runtime default), so the owned-field compare and SSA agree.
	params["replicas"] = crossplaneReplicas(d)
	if len(d.Resources) > 0 {
		res := make(map[string]any, len(d.Resources))
		for k, v := range d.Resources {
			res[k] = v
		}
		params["resources"] = res
	}
	// EnvRefs / Wiring secrets are passed BY REFERENCE only: the XR spec names the
	// secret (the provider's native secretKeyRef-style mechanism resolves it). The
	// reference string ("<scheme>:<locator>") is not a secret; cleartext never appears.
	if len(d.EnvRefs) > 0 {
		envRefs := make([]map[string]any, 0, len(d.EnvRefs))
		for _, e := range d.EnvRefs {
			envRefs = append(envRefs, map[string]any{"name": e.Name, "secretRef": e.SecretRef})
		}
		params["envFrom"] = envRefs
	}
	return map[string]any{"parameters": params}
}

// crossplaneApplyBody builds the Server-Side Apply document: a fully-specified XR
// object (apiVersion, kind, metadata.name, spec) — SSA requires the object to carry
// its own identity. The result is marshaled to JSON (valid YAML under the
// apply-patch+yaml content type).
func (c *CrossplaneBackend) crossplaneApplyBody(d Desired) ([]byte, error) {
	obj := map[string]any{
		"apiVersion": c.cfg.APIGroup + "/" + c.cfg.APIVersion,
		"metadata":   map[string]any{"name": crossplaneXRName(d)},
		"spec":       crossplaneDesiredSpec(d),
	}
	if c.cfg.Kind != "" {
		obj["kind"] = c.cfg.Kind
	}
	if c.cfg.Namespaced {
		md := obj["metadata"].(map[string]any)
		md["namespace"] = c.cfg.Namespace
	}
	body, err := json.Marshal(obj)
	if err != nil {
		return nil, errors.New("executor: cannot encode crossplane XR apply body")
	}
	return body, nil
}

// crossplaneXR is the minimal slice of an XR the backend reads: metadata.name, the
// observed .spec (for drift), and .status.conditions (for observe).
type crossplaneXR struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec   map[string]any `json:"spec"`
	Status struct {
		Conditions []crossplaneCondition `json:"conditions"`
	} `json:"status"`
}

// crossplaneCondition is one Crossplane status condition (Synced / Ready).
type crossplaneCondition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

// crossplaneGetXR GETs the XR by name. found=false on a 404; observable=false on an
// unreachable apiserver (an honest gap, never faked). A non-2xx, non-404 status is a
// surfaced error.
func (c *CrossplaneBackend) crossplaneGetXR(ctx context.Context, cred Credential, name string) (xr crossplaneXR, found, observable bool, err error) {
	client, cerr := c.resolveClient()
	if cerr != nil {
		return crossplaneXR{}, false, false, cerr
	}
	path, perr := c.crossplaneXRPath(name)
	if perr != nil {
		return crossplaneXR{}, false, false, perr
	}
	status, body, derr := doAPI(ctx, client, apiRequest{
		method:  http.MethodGet,
		baseURL: c.cfg.APIServer,
		path:    path,
		bearer:  cred.Token,
		accept:  "application/json",
	}, maxAPIBody)
	if derr != nil {
		// Transport failure: an honest gap, not a surfaced error (Observe wants this).
		return crossplaneXR{}, false, false, nil
	}
	switch {
	case status == http.StatusNotFound:
		return crossplaneXR{}, false, true, nil
	case ok2xx(status):
		var parsed crossplaneXR
		if jerr := json.Unmarshal(body, &parsed); jerr != nil {
			return crossplaneXR{}, false, false, errors.New("executor: crossplane XR response is malformed JSON")
		}
		return parsed, true, true, nil
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return crossplaneXR{}, false, false, fmt.Errorf("executor: crossplane apiserver denied the request (status %d)", status)
	default:
		return crossplaneXR{}, false, false, fmt.Errorf("executor: crossplane apiserver GET returned status %d", status)
	}
}

// Plan computes the forward diff: GET the XR, then create / update / noop.
func (c *CrossplaneBackend) Plan(ctx context.Context, d Desired, cred Credential) (Plan, error) {
	name := crossplaneXRName(d)
	if name == "" {
		return Plan{}, errors.New("executor: crossplane Desired has no XR name (SubjectRef/Name)")
	}
	xr, found, observable, err := c.crossplaneGetXR(ctx, cred, name)
	if err != nil {
		return Plan{}, err
	}
	if !observable {
		// Cannot read the XR to plan against — fail closed rather than apply blind.
		return Plan{}, errors.New("executor: crossplane apiserver is unreachable; cannot plan (fail-closed)")
	}
	if found && crossplaneInSync(d, xr.Spec) {
		// Already converged — idempotent noop (empty diff).
		return Plan{Runtime: c.Kind(), Intent: IntentApply, Diff: NewDiff(nil, nil, nil, true, "", "no changes (XR up to date)")}, nil
	}
	// Build the Server-Side Apply body now (at plan time) and carry it on the saved
	// plan's Handle so Apply executes EXACTLY this spec (anti-blind-apply). The Handle
	// is opaque, internal to one apply call, never persisted, and non-secret — the body
	// carries only non-sensitive params and secret REFERENCES (crossplaneDesiredSpec).
	handle, herr := c.crossplaneEncodeHandle(name, d)
	if herr != nil {
		return Plan{}, herr
	}
	if !found {
		item := ChangeItem{Action: "create", Kind: crossplaneKind, Ref: name, Detail: "create composite resource"}
		diff := NewDiff([]ChangeItem{item}, nil, nil, true, "retire the XR to roll back the create", "crossplane plan: 1 create")
		return Plan{Runtime: c.Kind(), Intent: IntentApply, Diff: diff, Handle: handle}, nil
	}
	// An update is reversible only by re-declaring the prior revision (owned by the
	// deploy module's revision history), not Destructive (SSA updates in place).
	item := ChangeItem{Action: "update", Kind: crossplaneKind, Ref: name, Detail: "update composite resource spec"}
	diff := NewDiff(nil, []ChangeItem{item}, nil, true, "re-declare the prior revision and apply to roll back", "crossplane plan: 1 update")
	return Plan{Runtime: c.Kind(), Intent: IntentApply, Diff: diff, Handle: handle}, nil
}

// crossplaneHandle is the opaque payload carried on a forward Plan.Handle: the XR
// name and the Server-Side Apply body computed at plan time. It is JSON-encoded into
// the Handle string so Apply runs exactly the planned spec without re-deriving it.
// Internal to a single apply call, never persisted; non-secret (the body carries only
// non-sensitive params and secret references — crossplaneDesiredSpec guarantees this).
type crossplaneHandle struct {
	Name string          `json:"name"`
	Body json.RawMessage `json:"body"`
}

// crossplaneEncodeHandle builds and JSON-encodes the forward-apply handle.
func (c *CrossplaneBackend) crossplaneEncodeHandle(name string, d Desired) (string, error) {
	body, err := c.crossplaneApplyBody(d)
	if err != nil {
		return "", err
	}
	h, err := json.Marshal(crossplaneHandle{Name: name, Body: body})
	if err != nil {
		return "", errors.New("executor: cannot encode crossplane plan handle")
	}
	return string(h), nil
}

// crossplaneDecodeHandle parses a forward-apply handle back into its name and body.
func crossplaneDecodeHandle(handle string) (name string, body []byte, err error) {
	var h crossplaneHandle
	if jerr := json.Unmarshal([]byte(handle), &h); jerr != nil || h.Name == "" {
		return "", nil, errors.New("executor: crossplane apply handle is malformed")
	}
	return h.Name, h.Body, nil
}

// DestroyPlan computes the teardown diff: if the XR exists, one Destructive delete.
func (c *CrossplaneBackend) DestroyPlan(ctx context.Context, d Desired, cred Credential) (Plan, error) {
	name := crossplaneXRName(d)
	if name == "" {
		return Plan{}, errors.New("executor: crossplane Desired has no XR name (SubjectRef/Name)")
	}
	_, found, observable, err := c.crossplaneGetXR(ctx, cred, name)
	if err != nil {
		return Plan{}, err
	}
	if !observable {
		return Plan{}, errors.New("executor: crossplane apiserver is unreachable; cannot plan a teardown (fail-closed)")
	}
	if !found {
		// Already absent — idempotent noop.
		return Plan{Runtime: c.Kind(), Intent: IntentDestroy, Diff: NewDiff(nil, nil, nil, false, "", "nothing to retire (XR absent)")}, nil
	}
	item := ChangeItem{Action: "delete", Kind: crossplaneKind, Ref: name, Detail: "delete composite resource (provider tears down backing managed resources)", Destructive: true}
	diff := NewDiff(nil, nil, []ChangeItem{item}, false, "recreate the XR from a prior revision to restore", "crossplane plan: 1 delete")
	return Plan{Runtime: c.Kind(), Intent: IntentDestroy, Diff: diff, Handle: name}, nil
}

// Apply executes a SAVED plan. A forward apply is a Server-Side Apply PATCH of the
// body computed at plan time (anti-blind-apply); a destroy is a DELETE. An empty plan
// changes nothing. Idempotent: re-applying the same spec is a server-side noop, and
// deleting an already-absent XR (404) is treated as success.
func (c *CrossplaneBackend) Apply(ctx context.Context, p Plan, cred Credential) (Result, error) {
	if p.Diff.Empty() || p.Handle == "" {
		return Result{Applied: nil, Detail: "no changes to apply"}, nil
	}
	client, err := c.resolveClient()
	if err != nil {
		return Result{}, err
	}
	if p.Intent == IntentDestroy {
		// A destroy plan stores the bare XR name in the Handle.
		return c.crossplaneDelete(ctx, client, cred, p, p.Handle)
	}
	name, body, derr := crossplaneDecodeHandle(p.Handle)
	if derr != nil {
		return Result{}, derr
	}
	return c.crossplaneServerSideApply(ctx, client, cred, p, name, body)
}

// crossplaneServerSideApply PATCHes the XR with the apply-patch content type and
// force=true (the deploy module is the declared owner of the spec fields it manages),
// using the body the plan saved. SSA is idempotent: a re-apply of the same spec is a
// server-side noop.
func (c *CrossplaneBackend) crossplaneServerSideApply(ctx context.Context, client *http.Client, cred Credential, p Plan, name string, body []byte) (Result, error) {
	path, perr := c.crossplaneXRPath(name)
	if perr != nil {
		return Result{}, perr
	}
	path += "?fieldManager=" + url.QueryEscape(c.cfg.FieldManager) + "&force=true"
	status, _, derr := doAPI(ctx, client, apiRequest{
		method:      http.MethodPatch,
		baseURL:     c.cfg.APIServer,
		path:        path,
		bearer:      cred.Token,
		body:        body,
		contentType: crossplaneApplyContentType,
		accept:      "application/json",
	}, maxAPIBody)
	if derr != nil {
		return Result{}, fmt.Errorf("executor: crossplane server-side apply failed: %w", derr)
	}
	if !ok2xx(status) {
		return Result{}, fmt.Errorf("executor: crossplane server-side apply returned status %d", status)
	}
	return Result{Applied: p.Diff.Items(), Detail: p.Diff.Summary}, nil
}

// crossplaneDelete DELETEs the XR. A 404 is treated as already-absent (idempotent).
func (c *CrossplaneBackend) crossplaneDelete(ctx context.Context, client *http.Client, cred Credential, p Plan, name string) (Result, error) {
	path, perr := c.crossplaneXRPath(name)
	if perr != nil {
		return Result{}, perr
	}
	status, _, derr := doAPI(ctx, client, apiRequest{
		method:  http.MethodDelete,
		baseURL: c.cfg.APIServer,
		path:    path,
		bearer:  cred.Token,
		accept:  "application/json",
	}, maxAPIBody)
	if derr != nil {
		return Result{}, fmt.Errorf("executor: crossplane XR delete failed: %w", derr)
	}
	if !ok2xx(status) && status != http.StatusNotFound {
		return Result{}, fmt.Errorf("executor: crossplane XR delete returned status %d", status)
	}
	return Result{Applied: p.Diff.Items(), Detail: p.Diff.Summary}, nil
}

// Rollback reverses a prior apply. For an XR a rollback is a re-declare of a prior
// revision (owned by the deploy module's revision history), not a control-plane state
// operation — reported honestly rather than faked.
func (c *CrossplaneBackend) Rollback(ctx context.Context, p Plan, cred Credential) (Result, error) {
	return Result{}, errors.New("executor: crossplane rollback is a re-declare of a prior revision (deploy module revision history), not a control-plane operation")
}

// Observe reads the REAL state: GET the XR and read .status.conditions[]. The provider
// reconciles CONTINUOUSLY; this only reads the MR/XR status it rolls up and never
// mutates. Synced=True & Ready=True => InSync; otherwise Drift names the unready
// condition; 404 => Exists:false; unreachable => Observable:false (an honest gap).
func (c *CrossplaneBackend) Observe(ctx context.Context, d Desired, cred Credential) (RealState, error) {
	name := crossplaneXRName(d)
	if name == "" {
		return RealState{Observable: false, Detail: "crossplane Desired has no XR name"}, nil
	}
	xr, found, observable, err := c.crossplaneGetXR(ctx, cred, name)
	if err != nil {
		// Auth/protocol error reading status — an honest gap, never a faked in-sync.
		return RealState{Observable: false, Detail: "crossplane XR status could not be read"}, nil
	}
	if !observable {
		return RealState{Observable: false, Detail: "crossplane apiserver unreachable"}, nil
	}
	if !found {
		return RealState{Exists: false, Observable: true, InSync: false, Detail: "composite resource does not exist"}, nil
	}
	synced := crossplaneConditionTrue(xr.Status.Conditions, "Synced")
	ready := crossplaneConditionTrue(xr.Status.Conditions, "Ready")
	if synced && ready {
		return RealState{Exists: true, Observable: true, InSync: true, Detail: "XR Synced=True Ready=True (provider reconciled)"}, nil
	}
	// Not converged: describe the unready condition as drift (the provider is still
	// reconciling or has reported a problem). No payload, just the condition class.
	drift := crossplaneDriftFromConditions(name, xr.Status.Conditions, synced, ready)
	return RealState{Exists: true, Observable: true, InSync: false, Drift: drift, Detail: "XR not yet converged (provider reconciling)"}, nil
}

// crossplaneConditionTrue reports whether the named condition is present with
// status=="True".
func crossplaneConditionTrue(conds []crossplaneCondition, typ string) bool {
	for _, cd := range conds {
		if cd.Type == typ {
			return strings.EqualFold(cd.Status, "True")
		}
	}
	return false
}

// crossplaneDriftFromConditions builds a minimal, non-sensitive drift list naming the
// unready Synced/Ready condition(s). It carries the condition reason (a non-sensitive
// enum like "ReconcileError" / "Creating"), never a message body that might leak data.
func crossplaneDriftFromConditions(name string, conds []crossplaneCondition, synced, ready bool) []ChangeItem {
	out := make([]ChangeItem, 0, 2)
	if !synced {
		out = append(out, ChangeItem{Action: "update", Kind: crossplaneKind, Ref: name, Detail: "condition Synced is not True: " + crossplaneConditionReason(conds, "Synced")})
	}
	if !ready {
		out = append(out, ChangeItem{Action: "update", Kind: crossplaneKind, Ref: name, Detail: "condition Ready is not True: " + crossplaneConditionReason(conds, "Ready")})
	}
	if len(out) == 0 {
		// Neither named condition present at all — the XR exists but reports no status yet.
		out = append(out, ChangeItem{Action: "update", Kind: crossplaneKind, Ref: name, Detail: "XR reports no Synced/Ready condition yet (provisioning)"})
	}
	return out
}

// crossplaneConditionReason returns the named condition's non-sensitive reason (an
// enum), or "absent"/"unknown" when missing. The free-text message is deliberately
// NOT surfaced (it could carry provider detail that is not minimal data).
func crossplaneConditionReason(conds []crossplaneCondition, typ string) string {
	for _, cd := range conds {
		if cd.Type == typ {
			if cd.Reason != "" {
				return cd.Reason
			}
			return "status=" + cd.Status
		}
	}
	return "absent"
}

// crossplaneInSync reports whether the observed XR .spec.parameters matches the
// desired state for EVERY field this backend OWNS (image, command, replicas,
// resources, envFrom) — a FULL owned-field compare, not a subset. A subset compare
// (only the keys present in the desired-derived spec) silently misses a CLEARED or
// DIVERGED owned field (e.g. the operator removed the command but the live XR still
// carries one) and reports a false in-sync. Server-managed fields outside the owned
// set are ignored (we never own them). Empty desired == absent observed for the
// collection fields (resources/envFrom), so an empty spec does not perpetually drift.
func crossplaneInSync(d Desired, observedSpec map[string]any) bool {
	obs, _ := observedSpec["parameters"].(map[string]any)
	if obs == nil {
		obs = map[string]any{}
	}
	if crossplaneAsString(obs["image"]) != d.Image {
		return false
	}
	if crossplaneAsString(obs["command"]) != d.Command {
		return false
	}
	if crossplaneAsInt(obs["replicas"]) != crossplaneReplicas(d) {
		return false
	}
	if !crossplaneValuesEqual(crossplaneResourcesOf(d), crossplaneNormalizeEmpty(obs["resources"])) {
		return false
	}
	return crossplaneValuesEqual(crossplaneEnvOf(d), crossplaneNormalizeEmpty(obs["envFrom"]))
}

// crossplaneReplicas applies the module's replica contract (0 = single instance /
// runtime default = 1), consistent with the imperative kube backend.
func crossplaneReplicas(d Desired) int {
	if d.Replicas > 0 {
		return d.Replicas
	}
	return 1
}

func crossplaneAsString(v any) string { s, _ := v.(string); return s }

func crossplaneAsInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	default:
		return 0
	}
}

// crossplaneResourcesOf returns the desired resources map, or nil when empty.
func crossplaneResourcesOf(d Desired) any {
	if len(d.Resources) == 0 {
		return nil
	}
	res := make(map[string]any, len(d.Resources))
	for k, v := range d.Resources {
		res[k] = v
	}
	return res
}

// crossplaneEnvOf returns the desired envFrom list (references only), or nil when empty.
func crossplaneEnvOf(d Desired) any {
	if len(d.EnvRefs) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(d.EnvRefs))
	for _, e := range d.EnvRefs {
		out = append(out, map[string]any{"name": e.Name, "secretRef": e.SecretRef})
	}
	return out
}

// crossplaneNormalizeEmpty maps an empty observed map/slice (or nil) to nil so an
// absent field and an empty collection compare equal (no perpetual drift).
func crossplaneNormalizeEmpty(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case map[string]any:
		if len(t) == 0 {
			return nil
		}
		return t
	case []any:
		if len(t) == 0 {
			return nil
		}
		return t
	default:
		return v
	}
}

// crossplaneValuesEqual compares two leaf values after normalising via a JSON round
// trip, so an int desired value (e.g. replicas=3) matches a float64 decoded from the
// apiserver's JSON (3.0). This avoids spurious drift from numeric type differences.
func crossplaneValuesEqual(a, b any) bool {
	na, ea := json.Marshal(a)
	nb, eb := json.Marshal(b)
	if ea == nil && eb == nil {
		return string(na) == string(nb)
	}
	return reflect.DeepEqual(a, b)
}
