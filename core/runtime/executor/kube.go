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
	"sort"
	"strconv"
	"strings"
	"time"
)

// KubeBackend is the IMPERATIVE Kubernetes backend. It is used where GitOps does
// not apply (the safe K8s default is GitOps) — i.e. when the control
// plane must drive a Deployment directly against the cluster API rather than
// through a Git-reconciled controller. It actuates a single apps/v1 Deployment per
// managed unit and NOTHING ELSE: it is the narrowest imperative surface that still
// covers the agent / mcp_server workload shape.
//
// CONNECTION & LEAST PRIVILEGE (docs/SECURITY-HARDENING.md,§2,§4). The connection is resolved from
// Desired.Target ("k8s.namespace/<ns>") and a CONFIGURED API base URL — NEVER an
// ambient kubeconfig (~/.kube/config, in-cluster service-account file, or any
// inherited token). The HTTPS client pins the API server with a configured CA
// bundle via tlsBearerClient (TLS 1.2 floor); insecure-skip is an explicit
// operator opt-in only. The bearer is the short-lived, per-namespace
// ServiceAccount token the Executor mints and passes in (cred.Token) — set ONLY in
// the Authorization header (httpclient.go), never in a URL or a log. A namespace is
// the credential's least-privilege boundary: the minted SA token is scoped to the
// one namespace this unit lives in.
//
// IDEMPOTENCY. plan GETs the Deployment: 404 => create; present but
// image/replicas/command differ => update; equal => an EMPTY diff (a no-op).
// apply is Server-Side Apply (PATCH, fieldManager=olivares-deploy, force=true,
// content-type application/apply-patch+yaml), which is idempotent by construction:
// re-applying the same manifest converges to the same object with no error.
//
// MINIMAL DATA (docs/SECURITY-HARDENING.md). A Diff/Result/RealState carries only the kind
// ("k8s.deployment"), the non-sensitive ref ("<ns>/<name>") and a short detail
// (e.g. "image change"). The SSA manifest body is NEVER placed in a returned
// struct or a log; it lives only on the internal Plan.Handle for the matching
// apply (anti-blind-apply: the applied manifest is exactly the planned one). Env
// values are referenced by k8s-native secretKeyRef — the secret material never
// enters this process's memory, a returned struct, or a log line.
type KubeBackend struct {
	cfg    KubeConfig
	client *http.Client // injectable; nil until the first call builds the pinned client
}

// KubeConfig configures the imperative Kubernetes backend (operator-provisioned;
// it holds NO credential material — the bearer is minted per call).
type KubeConfig struct {
	// APIBaseURL is the cluster API server base ("https://<host>:6443"). The
	// Deployment path is appended to it. Required; there is no ambient fallback.
	APIBaseURL string
	// CABundlePEM pins the API server certificate (TLS 1.2 floor, tlsBearerClient).
	// Empty + InsecureSkipVerify=false means the system roots are used.
	CABundlePEM []byte
	// InsecureSkipVerify disables server verification. Explicit operator opt-in
	// only (never the default); mirrors tlsBearerClient's insecure switch.
	InsecureSkipVerify bool
	// DefaultNamespace is used only when Desired.Target carries no namespace. Empty
	// => a target without a namespace is refused (fail-closed, no implicit "default").
	DefaultNamespace string
	// Timeout bounds a single API call (default 30s).
	Timeout time.Duration
}

// kubeFieldManager is the Server-Side Apply field manager that owns the fields this
// backend sets. A stable, dedicated manager keeps SSA ownership clean across
// re-applies and lets a human / GitOps own other fields without conflict.
const kubeFieldManager = "olivares-deploy"

// kubeApplyContentType is the Server-Side Apply media type. k8s accepts a JSON body
// under this content type (it need not be literal YAML), so the backend sends the
// canonical JSON manifest with this content type.
const kubeApplyContentType = "application/apply-patch+yaml"

// kubeDefaultReplicas is the replica count used when Desired.Replicas <= 0.
const kubeDefaultReplicas = 1

// kubeResourceKind is the non-sensitive resource class surfaced in every diff item.
const kubeResourceKind = "k8s.deployment"

// NewKubeBackend builds the imperative Kubernetes backend. The pinned HTTPS client
// is built lazily on first use (so a misconfigured CA surfaces as an honest error
// on the call, not at construction); tests inject a client + base URL directly.
func NewKubeBackend(cfg KubeConfig) *KubeBackend {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	return &KubeBackend{cfg: cfg}
}

// Kind returns the runtime selector.
func (k *KubeBackend) Kind() string { return "k8s" }

// httpClient returns the pinned client, building it once from the CA bundle.
func (k *KubeBackend) httpClient() (*http.Client, error) {
	if k.client != nil {
		return k.client, nil
	}
	c, err := tlsBearerClient(k.cfg.CABundlePEM, k.cfg.InsecureSkipVerify, k.cfg.Timeout)
	if err != nil {
		return nil, err
	}
	k.client = c
	return c, nil
}

// kubeConn is the resolved connection for one operation: the base URL, the
// namespace, and the Deployment name. It is derived purely from config + Desired;
// it carries NO credential material.
type kubeConn struct {
	baseURL   string
	namespace string
	name      string
}

// kubeNamespaceOf extracts the namespace from a "k8s.namespace/<ns>" target ref,
// falling back to the configured default. The locator is the path after the first
// "/"; a ref with no "/" (e.g. a bare "k8s.namespace") carries no namespace and
// yields the fallback. A target with neither yields "" (the caller refuses — there
// is no implicit "default" namespace).
func kubeNamespaceOf(target, fallback string) string {
	var ns string
	if i := strings.IndexByte(target, '/'); i >= 0 {
		ns = strings.TrimSpace(target[i+1:])
	}
	// keep only the first path segment of a multi-segment locator.
	if i := strings.IndexByte(ns, '/'); i >= 0 {
		ns = strings.TrimSpace(ns[:i])
	}
	if ns == "" {
		return strings.TrimSpace(fallback)
	}
	return ns
}

// kubeDeploymentName derives the Deployment name from the desired spec. SubjectRef
// is the logical subject (agent / mcp_server external id) and is the stable name
// the brief mandates; Name is a fallback. The result is a non-sensitive ref.
func kubeDeploymentName(d Desired) string {
	if n := strings.TrimSpace(d.SubjectRef); n != "" {
		return n
	}
	return strings.TrimSpace(d.Name)
}

// conn resolves the connection for a desired spec, failing closed when the API base
// URL, namespace, or Deployment name is missing (no ambient fallback anywhere).
func (k *KubeBackend) conn(d Desired) (kubeConn, error) {
	base := strings.TrimRight(strings.TrimSpace(k.cfg.APIBaseURL), "/")
	if base == "" {
		return kubeConn{}, errors.New("executor: kube backend has no APIBaseURL configured (no ambient kubeconfig fallback)")
	}
	ns := kubeNamespaceOf(d.Target, k.cfg.DefaultNamespace)
	if ns == "" {
		return kubeConn{}, errors.New("executor: kube target has no namespace (expected \"k8s.namespace/<ns>\") and no DefaultNamespace configured")
	}
	name := kubeDeploymentName(d)
	if name == "" {
		return kubeConn{}, errors.New("executor: kube desired spec has no SubjectRef/Name to derive a Deployment name")
	}
	return kubeConn{baseURL: base, namespace: ns, name: name}, nil
}

// kubeRef is the non-sensitive natural reference of the managed Deployment.
func (c kubeConn) kubeRef() string { return c.namespace + "/" + c.name }

// kubeDeploymentPath is the apps/v1 Deployment resource path.
func (c kubeConn) kubeDeploymentPath() string {
	return "/apis/apps/v1/namespaces/" + c.namespace + "/deployments/" + c.name
}

// --- desired-manifest model ------------------------------------------------------
//
// A minimal, typed model of just the fields this backend owns. It marshals to the
// SSA manifest body (which lives only on the internal Plan.Handle, never in a
// returned struct). Env values are referenced by secretKeyRef — never a value.

type kubeManifest struct {
	APIVersion string       `json:"apiVersion"`
	Kind       string       `json:"kind"`
	Metadata   kubeMetadata `json:"metadata"`
	Spec       kubeSpec     `json:"spec"`
}

type kubeMetadata struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	Labels    map[string]string `json:"labels,omitempty"`
}

type kubeSpec struct {
	Replicas int                 `json:"replicas"`
	Selector kubeLabelSelector   `json:"selector"`
	Template kubePodTemplateSpec `json:"template"`
}

type kubeLabelSelector struct {
	MatchLabels map[string]string `json:"matchLabels"`
}

type kubePodTemplateSpec struct {
	Metadata kubeMetadata `json:"metadata"`
	Spec     kubePodSpec  `json:"spec"`
}

type kubePodSpec struct {
	Containers []kubeContainer `json:"containers"`
}

type kubeContainer struct {
	Name    string       `json:"name"`
	Image   string       `json:"image"`
	Command []string     `json:"command,omitempty"`
	Env     []kubeEnvVar `json:"env,omitempty"`
}

// kubeEnvVar references a value by secretKeyRef ONLY — never a cleartext value.
type kubeEnvVar struct {
	Name      string            `json:"name"`
	ValueFrom *kubeEnvVarSource `json:"valueFrom,omitempty"`
}

type kubeEnvVarSource struct {
	SecretKeyRef *kubeSecretKeySelector `json:"secretKeyRef,omitempty"`
}

type kubeSecretKeySelector struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

// kubeDesiredReplicas applies the default replica count.
func kubeDesiredReplicas(d Desired) int {
	if d.Replicas <= 0 {
		return kubeDefaultReplicas
	}
	return d.Replicas
}

// kubeParseSecretRef splits a "<scheme>:<locator>" secret reference into the k8s
// Secret name and key. The locator form is "<secretName>/<key>"; a missing key
// defaults to the env var name. The scheme and the locator are non-sensitive
// REFERENCES — the secret material is never read here (docs/SECURITY-HARDENING.md). It returns ok
// only when a Secret name can be derived.
func kubeParseSecretRef(envName, secretRef string) (name, key string, ok bool) {
	ref := strings.TrimSpace(secretRef)
	if ref == "" {
		return "", "", false
	}
	// drop a leading "<scheme>:" if present (e.g. "k8s:db-creds/password").
	if i := strings.IndexByte(ref, ':'); i >= 0 {
		ref = ref[i+1:]
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", "", false
	}
	if i := strings.IndexByte(ref, '/'); i >= 0 {
		name = strings.TrimSpace(ref[:i])
		key = strings.TrimSpace(ref[i+1:])
	} else {
		name = ref
	}
	if name == "" {
		return "", "", false
	}
	if key == "" {
		key = envName
	}
	return name, key, true
}

// buildManifest assembles the desired Deployment manifest. Env values are bound by
// secretKeyRef references only; an env binding whose ref does not resolve to a
// Secret name is skipped (it is never injected as cleartext — fail-safe, not
// fail-leaky). Containers/env are sorted for a deterministic, comparable body.
func (k *KubeBackend) buildManifest(d Desired, c kubeConn) kubeManifest {
	labels := map[string]string{
		"app.kubernetes.io/managed-by": kubeFieldManager,
		"app.kubernetes.io/name":       c.name,
	}
	var env []kubeEnvVar
	for _, b := range d.EnvRefs {
		name, key, ok := kubeParseSecretRef(b.Name, b.SecretRef)
		if !ok {
			continue // never inject a value; only a resolvable secret reference
		}
		env = append(env, kubeEnvVar{
			Name:      b.Name,
			ValueFrom: &kubeEnvVarSource{SecretKeyRef: &kubeSecretKeySelector{Name: name, Key: key}},
		})
	}
	sort.Slice(env, func(i, j int) bool { return env[i].Name < env[j].Name })

	container := kubeContainer{Name: c.name, Image: d.Image, Env: env}
	if cmd := strings.TrimSpace(d.Command); cmd != "" {
		container.Command = strings.Fields(cmd)
	}
	return kubeManifest{
		APIVersion: "apps/v1",
		Kind:       "Deployment",
		Metadata:   kubeMetadata{Name: c.name, Namespace: c.namespace, Labels: labels},
		Spec: kubeSpec{
			Replicas: kubeDesiredReplicas(d),
			Selector: kubeLabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": c.name}},
			Template: kubePodTemplateSpec{
				Metadata: kubeMetadata{Labels: map[string]string{"app.kubernetes.io/name": c.name}},
				Spec:     kubePodSpec{Containers: []kubeContainer{container}},
			},
		},
	}
}

// kubeManifestHandle is the saved-plan handle: the canonical JSON SSA body. It is
// internal to a single apply call (Plan.Handle), never persisted, never a secret
// (the body references secrets by k8s-native secretKeyRef only).
func kubeManifestHandle(m kubeManifest) (string, error) {
	body, err := json.Marshal(m)
	if err != nil {
		return "", errors.New("executor: cannot encode kube manifest")
	}
	return string(body), nil
}

// --- live-object model (the subset we compare for drift) -------------------------

type kubeLiveDeployment struct {
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
		Replicas *int `json:"replicas"`
		Template struct {
			Spec struct {
				Containers []struct {
					Image   string   `json:"image"`
					Command []string `json:"command"`
				} `json:"containers"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
}

// kubeLiveImage returns the first container image of the live object ("" if none).
func (l kubeLiveDeployment) kubeLiveImage() string {
	if len(l.Spec.Template.Spec.Containers) == 0 {
		return ""
	}
	return l.Spec.Template.Spec.Containers[0].Image
}

// kubeLiveCommand returns the first container command of the live object.
func (l kubeLiveDeployment) kubeLiveCommand() []string {
	if len(l.Spec.Template.Spec.Containers) == 0 {
		return nil
	}
	return l.Spec.Template.Spec.Containers[0].Command
}

// kubeLiveReplicas returns the live replica count (k8s defaults absent to 1).
func (l kubeLiveDeployment) kubeLiveReplicas() int {
	if l.Spec.Replicas == nil {
		return 1
	}
	return *l.Spec.Replicas
}

// kubeStringSlicesEqual compares two command slices for equality.
func kubeStringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// kubeDriftDetail describes how the live object differs from desired (non-sensitive).
func kubeDriftDetail(d Desired, c kubeConn, live kubeLiveDeployment) string {
	var diffs []string
	if live.kubeLiveImage() != d.Image {
		diffs = append(diffs, "image change")
	}
	if live.kubeLiveReplicas() != kubeDesiredReplicas(d) {
		diffs = append(diffs, "replicas "+strconv.Itoa(live.kubeLiveReplicas())+"→"+strconv.Itoa(kubeDesiredReplicas(d)))
	}
	wantCmd := kubeWantCommand(d)
	if !kubeStringSlicesEqual(live.kubeLiveCommand(), wantCmd) {
		diffs = append(diffs, "command change")
	}
	if len(diffs) == 0 {
		return "update deployment " + c.kubeRef()
	}
	return strings.Join(diffs, ", ")
}

// kubeWantCommand returns the desired command slice (nil when none).
func kubeWantCommand(d Desired) []string {
	if cmd := strings.TrimSpace(d.Command); cmd != "" {
		return strings.Fields(cmd)
	}
	return nil
}

// kubeDiffers reports whether the live Deployment diverges from the desired spec on
// the fields this backend owns (image, replicas, command).
func kubeDiffers(d Desired, live kubeLiveDeployment) bool {
	if live.kubeLiveImage() != d.Image {
		return true
	}
	if live.kubeLiveReplicas() != kubeDesiredReplicas(d) {
		return true
	}
	if !kubeStringSlicesEqual(live.kubeLiveCommand(), kubeWantCommand(d)) {
		return true
	}
	return false
}

// --- API calls -------------------------------------------------------------------

// getDeployment GETs the Deployment. It returns (live, found, error): found=false
// on a 404 (the object is absent), error on any non-2xx-non-404 status or a
// transport failure (an honest gap, never a faked present/absent).
func (k *KubeBackend) getDeployment(ctx context.Context, cred Credential, c kubeConn) (kubeLiveDeployment, bool, error) {
	client, err := k.httpClient()
	if err != nil {
		return kubeLiveDeployment{}, false, err
	}
	code, body, err := doAPI(ctx, client, apiRequest{
		method:  http.MethodGet,
		baseURL: c.baseURL,
		path:    c.kubeDeploymentPath(),
		bearer:  cred.Token,
		accept:  "application/json",
	}, maxAPIBody)
	if err != nil {
		return kubeLiveDeployment{}, false, err
	}
	switch {
	case code == http.StatusNotFound:
		return kubeLiveDeployment{}, false, nil
	case ok2xx(code):
		var live kubeLiveDeployment
		if jerr := json.Unmarshal(body, &live); jerr != nil {
			return kubeLiveDeployment{}, false, errors.New("executor: kube API returned a malformed Deployment")
		}
		return live, true, nil
	default:
		return kubeLiveDeployment{}, false, fmt.Errorf("executor: kube API GET deployment returned status %d", code)
	}
}

// Plan computes the forward diff (create | update | noop). Read-only: it only GETs
// the Deployment. The saved Plan.Handle carries the SSA manifest body so the
// matching Apply executes exactly the planned manifest (anti-blind-apply).
func (k *KubeBackend) Plan(ctx context.Context, d Desired, cred Credential) (Plan, error) {
	c, err := k.conn(d)
	if err != nil {
		return Plan{}, err
	}
	manifest := k.buildManifest(d, c)
	handle, err := kubeManifestHandle(manifest)
	if err != nil {
		return Plan{}, err
	}
	live, found, err := k.getDeployment(ctx, cred, c)
	if err != nil {
		return Plan{}, err
	}
	if !found {
		item := ChangeItem{Action: "create", Kind: kubeResourceKind, Ref: c.kubeRef(), Detail: "create deployment (image " + d.Image + ")"}
		diff := NewDiff([]ChangeItem{item}, nil, nil, true, "retire (delete) the deployment to reverse this create", "create deployment "+c.kubeRef())
		return Plan{Runtime: k.Kind(), Intent: IntentApply, Diff: diff, Handle: handle}, nil
	}
	if !kubeDiffers(d, live) {
		// already in desired state — idempotent noop (empty diff)
		return Plan{Runtime: k.Kind(), Intent: IntentApply, Diff: NewDiff(nil, nil, nil, true, "", "deployment "+c.kubeRef()+" already in desired state")}, nil
	}
	item := ChangeItem{Action: "update", Kind: kubeResourceKind, Ref: c.kubeRef(), Detail: kubeDriftDetail(d, c, live)}
	diff := NewDiff(nil, []ChangeItem{item}, nil, true, "re-apply the prior manifest to roll back", "update deployment "+c.kubeRef())
	return Plan{Runtime: k.Kind(), Intent: IntentApply, Diff: diff, Handle: handle}, nil
}

// DestroyPlan computes the teardown diff: one Destructive delete if the Deployment
// exists, else an empty diff (idempotent — nothing to retire). Read-only.
func (k *KubeBackend) DestroyPlan(ctx context.Context, d Desired, cred Credential) (Plan, error) {
	c, err := k.conn(d)
	if err != nil {
		return Plan{}, err
	}
	_, found, err := k.getDeployment(ctx, cred, c)
	if err != nil {
		return Plan{}, err
	}
	if !found {
		return Plan{Runtime: k.Kind(), Intent: IntentDestroy, Diff: NewDiff(nil, nil, nil, true, "", "deployment "+c.kubeRef()+" already absent")}, nil
	}
	item := ChangeItem{Action: "delete", Kind: kubeResourceKind, Ref: c.kubeRef(), Detail: "delete deployment", Destructive: true}
	// The destroy handle carries the resource path (non-secret) so Apply knows what
	// to DELETE without re-deriving from a Desired it no longer has.
	diff := NewDiff(nil, nil, []ChangeItem{item}, false, "re-apply the deployment manifest to recreate it", "delete deployment "+c.kubeRef())
	return Plan{Runtime: k.Kind(), Intent: IntentDestroy, Diff: diff, Handle: c.kubeDeploymentPath()}, nil
}

// Apply executes a SAVED plan. For IntentApply it Server-Side Applies the saved
// manifest (PATCH with the apply-patch content type, fieldManager + force). For
// IntentDestroy it DELETEs with Foreground propagation. Both are idempotent. An
// empty diff (noop plan) applies nothing.
func (k *KubeBackend) Apply(ctx context.Context, p Plan, cred Credential) (Result, error) {
	if p.Diff.Empty() {
		return Result{Applied: nil, Detail: "no changes to apply"}, nil
	}
	if p.Intent == IntentDestroy {
		return k.applyDestroy(ctx, p, cred)
	}
	return k.applyForward(ctx, p, cred)
}

// applyForward performs the Server-Side Apply of the saved manifest handle. The
// resource path is re-derived from the saved manifest's namespace/name (so the
// applied object is exactly the planned one); the API base URL is the backend's
// configured server (the manifest carries no base URL — a base in a saved plan
// would be redundant and a leak vector).
func (k *KubeBackend) applyForward(ctx context.Context, p Plan, cred Credential) (Result, error) {
	if strings.TrimSpace(p.Handle) == "" {
		return Result{}, errors.New("executor: kube apply has no saved manifest to apply")
	}
	path, err := kubeForwardPath(p.Handle)
	if err != nil {
		return Result{}, err
	}
	baseURL := strings.TrimRight(strings.TrimSpace(k.cfg.APIBaseURL), "/")
	if baseURL == "" {
		return Result{}, errors.New("executor: kube backend has no APIBaseURL configured")
	}
	client, err := k.httpClient()
	if err != nil {
		return Result{}, err
	}
	code, _, err := doAPI(ctx, client, apiRequest{
		method:      http.MethodPatch,
		baseURL:     baseURL,
		path:        path + "?fieldManager=" + kubeFieldManager + "&force=true",
		bearer:      cred.Token,
		body:        []byte(p.Handle),
		contentType: kubeApplyContentType,
		accept:      "application/json",
	}, maxAPIBody)
	if err != nil {
		return Result{}, err
	}
	if !ok2xx(code) {
		return Result{}, fmt.Errorf("executor: kube server-side apply returned status %d", code)
	}
	return Result{Applied: p.Diff.Items(), Detail: p.Diff.Summary}, nil
}

// applyDestroy performs the Foreground-propagation DELETE of the saved path handle.
// The resource path is the saved (non-secret) handle; the API base URL is the
// backend's configured server (a destroy handle carries no base).
func (k *KubeBackend) applyDestroy(ctx context.Context, p Plan, cred Credential) (Result, error) {
	path := strings.TrimSpace(p.Handle)
	if path == "" || !strings.HasPrefix(path, "/apis/apps/v1/namespaces/") {
		return Result{}, errors.New("executor: kube destroy has no saved deployment path to delete")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(k.cfg.APIBaseURL), "/")
	if baseURL == "" {
		return Result{}, errors.New("executor: kube backend has no APIBaseURL configured")
	}
	client, err := k.httpClient()
	if err != nil {
		return Result{}, err
	}
	code, _, err := doAPI(ctx, client, apiRequest{
		method:  http.MethodDelete,
		baseURL: baseURL,
		path:    path + "?propagationPolicy=Foreground",
		bearer:  cred.Token,
		accept:  "application/json",
	}, maxAPIBody)
	if err != nil {
		return Result{}, err
	}
	// 404 on delete is idempotent success: the object is already gone.
	if !ok2xx(code) && code != http.StatusNotFound {
		return Result{}, fmt.Errorf("executor: kube delete deployment returned status %d", code)
	}
	return Result{Applied: p.Diff.Items(), Detail: p.Diff.Summary}, nil
}

// kubeForwardPath re-derives the Deployment resource path from a saved forward-apply
// manifest handle (the JSON body), so Apply hits exactly the planned object.
func kubeForwardPath(handle string) (string, error) {
	var m kubeManifest
	if err := json.Unmarshal([]byte(handle), &m); err != nil {
		return "", errors.New("executor: kube saved manifest is unreadable")
	}
	if m.Metadata.Namespace == "" || m.Metadata.Name == "" {
		return "", errors.New("executor: kube saved manifest lacks namespace/name")
	}
	c := kubeConn{namespace: m.Metadata.Namespace, name: m.Metadata.Name}
	return c.kubeDeploymentPath(), nil
}

// Observe reads the REAL Deployment and the desired-vs-real drift. 404 =>
// Exists:false, Observable:true, InSync:false, Drift=[create]. A reachable, equal
// object => InSync. An unreachable API => Observable:false (an honest gap, never a
// faked in-sync). Never mutates.
func (k *KubeBackend) Observe(ctx context.Context, d Desired, cred Credential) (RealState, error) {
	c, err := k.conn(d)
	if err != nil {
		// A misconfigured connection is a gap, not a crash of the drift loop.
		return RealState{Observable: false, Detail: "kube connection not resolvable"}, nil
	}
	live, found, gerr := k.getDeployment(ctx, cred, c)
	if gerr != nil {
		// Unreachable / non-404 error: an honest gap.
		return RealState{Observable: false, Detail: "kube Deployment " + c.kubeRef() + " unobservable"}, nil
	}
	if !found {
		return RealState{
			Exists:     false,
			Observable: true,
			InSync:     false,
			Drift:      []ChangeItem{{Action: "create", Kind: kubeResourceKind, Ref: c.kubeRef(), Detail: "deployment is absent"}},
			Detail:     "deployment " + c.kubeRef() + " does not exist",
		}, nil
	}
	if kubeDiffers(d, live) {
		return RealState{
			Exists:     true,
			Observable: true,
			InSync:     false,
			Drift:      []ChangeItem{{Action: "update", Kind: kubeResourceKind, Ref: c.kubeRef(), Detail: kubeDriftDetail(d, c, live)}},
			Detail:     "deployment " + c.kubeRef() + " has drifted from desired",
		}, nil
	}
	return RealState{Exists: true, Observable: true, InSync: true, Detail: "deployment " + c.kubeRef() + " matches desired"}, nil
}

// Rollback re-applies a prior known-good plan when the handle carries the previous
// manifest (a forward-apply handle). For a destroy plan (a path-only handle) there
// is no prior manifest to re-apply, so it reports the honest limitation: recreating
// the deployment is a forward apply of the prior revision, owned by the deploy
// module's revision history, not a single inverse call here.
func (k *KubeBackend) Rollback(ctx context.Context, p Plan, cred Credential) (Result, error) {
	if p.Intent == IntentDestroy || !kubeIsManifestHandle(p.Handle) {
		return Result{}, errors.New("executor: kube rollback needs the prior deployment manifest; recreate it via a forward apply of the prior revision (deploy module revision history)")
	}
	// Re-apply the saved manifest under a rollback intent (Server-Side Apply is
	// idempotent: re-applying the prior manifest converges the object back).
	rp := Plan{Runtime: k.Kind(), Intent: IntentApply, Diff: p.Diff, Handle: p.Handle}
	return k.applyForward(ctx, rp, cred)
}

// kubeIsManifestHandle reports whether a saved handle is a forward-apply manifest
// body (JSON object) rather than a path-only destroy handle.
func kubeIsManifestHandle(handle string) bool {
	h := strings.TrimSpace(handle)
	return strings.HasPrefix(h, "{")
}
