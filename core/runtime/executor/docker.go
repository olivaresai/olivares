// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// dockerSpecHashLabel records a non-sensitive fingerprint of the desired spec
// (image + command + env REFERENCES, never values) on the container, so Plan/Observe
// detect a command/env change — not only an image change — and stay truly idempotent.
const dockerSpecHashLabel = "olivares.io/spec-hash"

// dockerSpecHash is a short, non-sensitive fingerprint of the apply spec. It hashes
// the image, command, and the SORTED env-reference locators (never a secret value).
func dockerSpecHash(s dockerApply) string {
	refs := make([]string, 0, len(s.envRefs))
	for _, e := range s.envRefs {
		refs = append(refs, strings.TrimSpace(e.Name)+"="+strings.TrimSpace(e.SecretRef))
	}
	sort.Strings(refs)
	sum := sha256.Sum256([]byte(s.image + "\x00" + s.command + "\x00" + strings.Join(refs, "\x00")))
	return hex.EncodeToString(sum[:])[:16]
}

// DockerBackend is the imperative backend for the Docker Engine API. Unlike the
// declarative TofuBackend (which shells out to a binary against remote state), this
// backend speaks the Docker daemon's HTTP API directly over its unix socket and
// reconciles a single named container toward Desired.
//
// LEAST PRIVILEGE / CREDENTIALS (docs/SECURITY-HARDENING.md,§4). For the v1 LOCAL unix-socket
// transport the boundary IS the socket permission: membership of the docker group
// (an explicit, audited operator grant) is what lets this process talk to the
// daemon. There is NO bearer token to inject into a unix-socket transport, so the
// minted Credential's MATERIAL is deliberately NOT placed on the wire — the socket
// grant is the privilege. The Credential is still REQUIRED and still gates actuation:
// the Executor mints it deny-closed (no source => no action) and rejects an expired
// one before this backend is ever called. For a REMOTE TCP+TLS daemon (RemoteBaseURL
// set) the minted cred IS used as the API bearer via apiRequest.bearer, and the
// server is pinned with a CA bundle (tlsBearerClient). The v1 focus is the socket.
//
// MINIMAL DATA (docs/SECURITY-HARDENING.md). Every Diff/Result/RealState carries only a kind
// ("container"), the non-sensitive container name as the ref, and a short detail
// (the image, which is non-sensitive). Environment values are passed to the daemon
// ONLY by reference (NAME=<secret-ref>) — the cleartext value is never resolved,
// never placed in the created-container body's cleartext, never logged, and never
// returned in any struct. The reference is what the runtime's own secret mechanism
// (an env file / a swarm secret / a sidecar) later resolves.
type DockerBackend struct {
	cfg    DockerConfig
	client *http.Client
	// baseURL is the placeholder host the unix transport ignores ("http://docker"),
	// or the remote daemon's scheme+host when RemoteBaseURL is configured.
	baseURL string
	// bearerFn yields the per-call API bearer. For the unix socket it returns "" (the
	// socket grant is the boundary); for a remote TLS daemon it returns cred.Token.
	bearerFn func(cred Credential) string
}

// DockerConfig configures the Docker backend (operator-provisioned, no secrets).
type DockerConfig struct {
	// SocketPath is the Docker daemon unix socket (default "/var/run/docker.sock").
	// Ignored when RemoteBaseURL is set.
	SocketPath string
	// RemoteBaseURL, when set, points the backend at a REMOTE TCP+TLS daemon
	// (e.g. "https://docker.host:2376"); the minted credential is then used as the
	// API bearer and the server is pinned with RemoteCAPEM. Empty => local socket.
	RemoteBaseURL string
	// RemoteCAPEM pins the remote daemon's TLS server certificate (PEM). Required
	// for a remote daemon unless RemoteInsecure is explicitly set.
	RemoteCAPEM []byte
	// RemoteInsecure disables TLS verification for the remote daemon. Explicit
	// operator opt-in only; never the default.
	RemoteInsecure bool
	// Timeout bounds a single daemon API call (default 30s).
	Timeout time.Duration
}

// NewDockerBackend builds the imperative Docker backend. For the local socket it
// wires a unix-socket HTTP client (the placeholder base URL "http://docker"); for a
// remote daemon it wires a TLS bearer client pinned with the configured CA. A
// remote-config error is deferred to first use so the constructor stays total
// (mirroring how the composition root wires backends without I/O).
func NewDockerBackend(cfg DockerConfig) *DockerBackend {
	if cfg.SocketPath == "" {
		cfg.SocketPath = "/var/run/docker.sock"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	b := &DockerBackend{cfg: cfg}
	if cfg.RemoteBaseURL != "" {
		b.baseURL = strings.TrimRight(cfg.RemoteBaseURL, "/")
		// On a remote daemon the minted, short-lived credential is the API bearer.
		b.bearerFn = func(cred Credential) string { return cred.Token }
		if cl, err := tlsBearerClient(cfg.RemoteCAPEM, cfg.RemoteInsecure, cfg.Timeout); err == nil {
			b.client = cl
		}
		// A nil client (bad CA) surfaces as errDockerNoClient at first use, not panic.
	} else {
		b.baseURL = dockerSocketBaseURL
		// LOCAL socket: the socket permission is the privilege boundary; no token on
		// the wire. The cred is still required/validated by the Executor.
		b.bearerFn = func(Credential) string { return "" }
		b.client = unixHTTPClient(cfg.SocketPath, cfg.Timeout)
	}
	return b
}

// dockerSocketBaseURL is the placeholder host for the unix-socket transport; the
// socket carries the connection, the host is ignored (httpclient.go).
const dockerSocketBaseURL = "http://docker"

// dockerKind is the resource class and runtime selector for this backend.
const dockerKind = "docker"

// errDockerNoClient is returned when the remote TLS client could not be built (a bad
// CA bundle); it fails closed rather than acting against an unverified daemon.
var errDockerNoClient = errors.New("executor: docker remote client is not configured (invalid CA bundle?)")

// errDockerName is returned when a Desired has no container name (SubjectRef); the
// backend will not act on an anonymous container.
var errDockerName = errors.New("executor: docker backend requires a container name (Desired.SubjectRef)")

// Kind returns the runtime selector.
func (b *DockerBackend) Kind() string { return dockerKind }

// dockerName derives the managed container name from Desired.SubjectRef.
func (b *DockerBackend) dockerName(d Desired) (string, error) {
	name := strings.TrimSpace(d.SubjectRef)
	if name == "" {
		return "", errDockerName
	}
	return name, nil
}

// dockerContainer is the minimal subset of the daemon's container record this
// backend needs: identity and image (for the drift compare). No env/secret material
// is read or retained.
type dockerContainer struct {
	id       string
	image    string
	state    string
	specHash string // the olivares.io/spec-hash label, when set ("" for a legacy/foreign container)
	found    bool
}

// Plan computes the forward diff for the managed container. READ-ONLY: it lists the
// daemon's containers and compares the named one against Desired.
//
//   - absent          => create (additive)
//   - present, image differs => replace (Destructive: stop+remove+recreate)
//   - present, image matches  => empty Diff (idempotent noop)
func (b *DockerBackend) Plan(ctx context.Context, d Desired, cred Credential) (Plan, error) {
	name, err := b.dockerName(d)
	if err != nil {
		return Plan{}, err
	}
	cur, err := b.dockerFind(ctx, name, cred)
	if err != nil {
		return Plan{}, err
	}
	item := ChangeItem{Kind: dockerKind, Ref: name, Detail: d.Image}
	// In sync iff the image matches AND (the recorded spec-hash matches, or the
	// container predates the label — then fall back to image-only). A command/env
	// change moves the spec-hash and is correctly detected as drift.
	desiredHash := dockerSpecHash(dockerApplyFor(d))
	inSync := dockerImageMatches(cur.image, d.Image) && (cur.specHash == "" || cur.specHash == desiredHash)
	switch {
	case !cur.found:
		item.Action = "create"
		diff := NewDiff([]ChangeItem{item}, nil, nil, true,
			"reverse by retiring (stop+remove) the created container",
			fmt.Sprintf("create container %q", name))
		return Plan{Runtime: dockerKind, Intent: IntentApply, Diff: diff, Handle: name, payload: dockerApplyFor(d)}, nil

	case inSync:
		// Already in desired state — idempotent noop.
		return Plan{Runtime: dockerKind, Intent: IntentApply,
			Diff: NewDiff(nil, nil, nil, true, "", fmt.Sprintf("container %q already at desired spec", name))}, nil

	default:
		// Image drift => replace (stop+remove old, create+start new). Destructive.
		item.Action, item.Destructive = "replace", true
		diff := NewDiff(nil, []ChangeItem{item}, nil, false,
			"replace is not auto-reversible without the prior revision (deploy module revision history)",
			fmt.Sprintf("replace container %q (image or command/env change)", name))
		return Plan{Runtime: dockerKind, Intent: IntentApply, Diff: diff, Handle: name, payload: dockerApplyFor(d)}, nil
	}
}

// DestroyPlan computes the teardown diff: if the container exists, one delete item
// (always Destructive); if absent, an empty Diff (idempotent — nothing to retire).
func (b *DockerBackend) DestroyPlan(ctx context.Context, d Desired, cred Credential) (Plan, error) {
	name, err := b.dockerName(d)
	if err != nil {
		return Plan{}, err
	}
	cur, err := b.dockerFind(ctx, name, cred)
	if err != nil {
		return Plan{}, err
	}
	if !cur.found {
		return Plan{Runtime: dockerKind, Intent: IntentDestroy,
			Diff: NewDiff(nil, nil, nil, true, "", fmt.Sprintf("container %q already absent", name))}, nil
	}
	item := ChangeItem{Action: "delete", Kind: dockerKind, Ref: name, Detail: cur.image, Destructive: true}
	diff := NewDiff(nil, nil, []ChangeItem{item}, false,
		"teardown is not auto-reversible (re-apply the desired spec to recreate)",
		fmt.Sprintf("delete container %q", name))
	return Plan{Runtime: dockerKind, Intent: IntentDestroy, Diff: diff, Handle: name}, nil
}

// Apply executes a SAVED plan (forward or destroy). It re-resolves the container by
// name at apply time (the daemon is the source of truth; the saved Diff says WHAT to
// do, the live lookup says against WHICH id) and is idempotent at every branch.
//
// The Desired image/command/env-references needed to (re)create a container are NOT
// carried in the neutral Plan (minimal data). Apply therefore reconstructs the
// create body from the Diff's single change item: the item's Ref is the name and its
// Detail is the image — both non-sensitive. Env references are re-resolved by the
// caller's seam at create time only when present; this backend never holds secrets.
func (b *DockerBackend) Apply(ctx context.Context, p Plan, cred Credential) (Result, error) {
	if p.Diff.Empty() {
		// An empty plan changes nothing (idempotent).
		return Result{Detail: "no changes to apply"}, nil
	}
	if p.Intent == IntentDestroy {
		return b.applyDestroy(ctx, p, cred)
	}
	return b.applyForward(ctx, p, cred)
}

// applyForward creates or replaces the managed container.
func (b *DockerBackend) applyForward(ctx context.Context, p Plan, cred Credential) (Result, error) {
	name := p.Handle
	if name == "" {
		return Result{}, errDockerName
	}
	item, ok := dockerSoleItem(p.Diff)
	if !ok {
		return Result{}, errors.New("executor: docker apply expects exactly one container change")
	}
	spec := dockerSpecFrom(p, item)
	cur, err := b.dockerFind(ctx, name, cred)
	if err != nil {
		return Result{}, err
	}
	switch item.Action {
	case "create":
		if cur.found {
			// Raced: a container appeared at this name AFTER Plan classified an additive
			// "create". If it matches the desired image this is an idempotent noop.
			if dockerImageMatches(cur.image, spec.image) {
				return Result{Applied: p.Diff.Items(), Detail: "container already present at desired image"}, nil
			}
			// Otherwise a stop+remove here would be a DESTRUCTIVE change the blast-radius
			// gate NEVER saw (it gated a "create"). Refuse (fail-closed) and force a fresh
			// plan/gate cycle, which will classify this as a gated "replace" (TOCTOU guard).
			return Result{}, errors.New("executor: docker create conflict — a different container now exists at this name; re-plan so the blast-radius gate sees the replace")
		}
		if err := b.dockerCreateStart(ctx, name, spec, cred); err != nil {
			return Result{}, err
		}
		return Result{Applied: p.Diff.Items(), Detail: p.Diff.Summary}, nil

	case "replace":
		// A "replace" was already classified Destructive and passed the blast-radius
		// gate, so the stop+remove below is authorized.
		if cur.found {
			if dockerImageMatches(cur.image, spec.image) {
				// Drift resolved itself before apply — idempotent noop.
				return Result{Applied: p.Diff.Items(), Detail: "container already at desired image"}, nil
			}
			if err := b.dockerStopRemove(ctx, cur, cred); err != nil {
				return Result{}, err
			}
		}
		if err := b.dockerCreateStart(ctx, name, spec, cred); err != nil {
			return Result{}, err
		}
		return Result{Applied: p.Diff.Items(), Detail: p.Diff.Summary}, nil

	default:
		return Result{}, fmt.Errorf("executor: docker apply: unsupported forward action %q", item.Action)
	}
}

// applyDestroy stops and force-removes the managed container; absence is success.
func (b *DockerBackend) applyDestroy(ctx context.Context, p Plan, cred Credential) (Result, error) {
	name := p.Handle
	if name == "" {
		return Result{}, errDockerName
	}
	cur, err := b.dockerFind(ctx, name, cred)
	if err != nil {
		return Result{}, err
	}
	if !cur.found {
		// Already gone — idempotent teardown.
		return Result{Applied: p.Diff.Items(), Detail: "container already absent"}, nil
	}
	if err := b.dockerStopRemove(ctx, cur, cred); err != nil {
		return Result{}, err
	}
	return Result{Applied: p.Diff.Items(), Detail: p.Diff.Summary}, nil
}

// Rollback reverses a prior apply. A create rolls back by stop+remove of the created
// container (reversible). A replace/destroy is NOT auto-reversible without the prior
// image revision, which is owned by the deploy module's revision history — the honest
// limitation is reported rather than faking a recovery.
func (b *DockerBackend) Rollback(ctx context.Context, p Plan, cred Credential) (Result, error) {
	if p.Intent == IntentDestroy {
		return Result{}, errors.New("executor: docker destroy is not auto-reversible (re-apply the desired spec to recreate)")
	}
	item, ok := dockerSoleItem(p.Diff)
	if !ok || item.Action != "create" {
		return Result{}, errors.New("executor: docker rollback supports only reversing a create (replace needs the prior image revision)")
	}
	name := p.Handle
	if name == "" {
		return Result{}, errDockerName
	}
	cur, err := b.dockerFind(ctx, name, cred)
	if err != nil {
		return Result{}, err
	}
	if !cur.found {
		return Result{Detail: "nothing to roll back (container already absent)"}, nil
	}
	if err := b.dockerStopRemove(ctx, cur, cred); err != nil {
		return Result{}, err
	}
	return Result{Applied: []ChangeItem{{Action: "delete", Kind: dockerKind, Ref: name, Destructive: true}},
		Detail: fmt.Sprintf("rolled back: removed created container %q", name)}, nil
}

// Observe reads the REAL state of the managed container and the desired-vs-real
// drift. It NEVER mutates. A daemon that cannot be reached yields Observable:false (an
// honest gap, never a faked in-sync). A missing container is Observable:true,
// Exists:false with a create-drift item.
func (b *DockerBackend) Observe(ctx context.Context, d Desired, cred Credential) (RealState, error) {
	name, err := b.dockerName(d)
	if err != nil {
		return RealState{}, err
	}
	if b.client == nil {
		return RealState{Observable: false, Detail: "docker client not configured"}, nil
	}
	status, body, err := b.dockerCall(ctx, http.MethodGet, "/containers/"+name+"/json", cred, nil)
	if err != nil {
		// Transport failure (daemon down / socket gone): an honest gap.
		return RealState{Observable: false, Detail: "docker daemon unreachable"}, nil
	}
	switch {
	case status == http.StatusNotFound:
		return RealState{
			Exists: false, Observable: true, InSync: false,
			Drift:  []ChangeItem{{Action: "create", Kind: dockerKind, Ref: name, Detail: d.Image}},
			Detail: fmt.Sprintf("container %q does not exist", name),
		}, nil
	case !ok2xx(status):
		return RealState{Observable: false, Detail: fmt.Sprintf("docker inspect returned status %d", status)}, nil
	}
	var insp struct {
		Config struct {
			Image  string            `json:"Image"`
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
		State struct {
			Status string `json:"Status"`
		} `json:"State"`
	}
	if jerr := json.Unmarshal(body, &insp); jerr != nil {
		return RealState{Observable: false, Detail: "docker inspect response malformed"}, nil
	}
	desiredHash := dockerSpecHash(dockerApplyFor(d))
	liveHash := insp.Config.Labels[dockerSpecHashLabel]
	if dockerImageMatches(insp.Config.Image, d.Image) && (liveHash == "" || liveHash == desiredHash) {
		return RealState{Exists: true, Observable: true, InSync: true,
			Detail: fmt.Sprintf("container %q matches desired spec", name)}, nil
	}
	return RealState{
		Exists: true, Observable: true, InSync: false,
		Drift:  []ChangeItem{{Action: "replace", Kind: dockerKind, Ref: name, Detail: d.Image, Destructive: true}},
		Detail: fmt.Sprintf("container %q has drifted from desired (image or command/env)", name),
	}, nil
}

// --- daemon API helpers ----------------------------------------------------------

// dockerCall issues one Docker daemon API call via the frozen doAPI primitive,
// bounded by maxDockerBody. The bearer (for a remote daemon) is set in the
// Authorization header only — never a URL or argv; for the local socket it is empty.
func (b *DockerBackend) dockerCall(ctx context.Context, method, path string, cred Credential, body []byte) (int, []byte, error) {
	if b.client == nil {
		return 0, nil, errDockerNoClient
	}
	req := apiRequest{
		method:  method,
		baseURL: b.baseURL,
		path:    path,
		bearer:  b.bearerFn(cred),
		accept:  "application/json",
	}
	if len(body) > 0 {
		req.body = body
		req.contentType = "application/json"
	}
	return doAPI(ctx, b.client, req, maxDockerBody)
}

// dockerFind lists the daemon's containers and returns the named one (if any). It
// uses GET /containers/json?all=1 with a name filter so stopped containers are seen.
func (b *DockerBackend) dockerFind(ctx context.Context, name string, cred Credential) (dockerContainer, error) {
	// The filter is a JSON object: {"name":["<name>"]}. The daemon matches the name
	// as a substring/regex, so we still verify an exact "/<name>" below.
	filter := `{"name":["` + name + `"]}`
	path := "/containers/json?all=1&filters=" + dockerQueryEscape(filter)
	status, body, err := b.dockerCall(ctx, http.MethodGet, path, cred, nil)
	if err != nil {
		return dockerContainer{}, fmt.Errorf("executor: docker list failed: %w", err)
	}
	if !ok2xx(status) {
		return dockerContainer{}, fmt.Errorf("executor: docker list returned status %d", status)
	}
	var list []struct {
		ID     string            `json:"Id"`
		Names  []string          `json:"Names"`
		Image  string            `json:"Image"`
		State  string            `json:"State"`
		Labels map[string]string `json:"Labels"`
	}
	if jerr := json.Unmarshal(body, &list); jerr != nil {
		return dockerContainer{}, errors.New("executor: docker list response malformed")
	}
	want := "/" + name
	for _, c := range list {
		for _, n := range c.Names {
			if n == want {
				return dockerContainer{id: c.ID, image: c.Image, state: c.State, specHash: c.Labels[dockerSpecHashLabel], found: true}, nil
			}
		}
	}
	return dockerContainer{found: false}, nil
}

// dockerApply is the backend-specific apply data carried on a docker Plan's payload:
// the image, the optional command override, and the env-by-REFERENCE bindings a
// create/replace needs (the neutral Diff carries none of these). It holds references
// only — never a cleartext secret.
type dockerApply struct {
	image   string
	command string
	envRefs []SecretBinding
	// Workspace / hardening: carried by reference/value (non-sensitive host
	// paths only). Not part of dockerSpecHash (mounts are infra, not drift, in v1).
	mounts         []Mount
	readonlyRootfs bool
	tmpfs          []string
	user           string
	workdir        string
}

// dockerApplyFor builds the apply payload from a Desired (references only).
func dockerApplyFor(d Desired) dockerApply {
	return dockerApply{
		image: d.Image, command: d.Command, envRefs: d.EnvRefs,
		mounts: d.Mounts, readonlyRootfs: d.ReadonlyRootfs, tmpfs: d.TmpfsMounts,
		user: strings.TrimSpace(d.RunAsUser), workdir: strings.TrimSpace(d.WorkingDir),
	}
}

// dockerSpecFrom recovers the create spec from a saved plan's payload, falling back to
// the (image-only) Diff item when no payload is present (e.g. a hand-built plan).
func dockerSpecFrom(p Plan, item ChangeItem) dockerApply {
	if s, ok := p.payload.(dockerApply); ok {
		if s.image == "" {
			s.image = item.Detail
		}
		return s
	}
	return dockerApply{image: item.Detail}
}

// dockerCreateStart creates a container from the apply spec (command + env passed BY
// REFERENCE only) and starts it. The created-container body references secret env by
// the runtime's native mechanism (NAME=<secret-ref>); no cleartext secret is ever
// placed in the body, logged, or returned.
func (b *DockerBackend) dockerCreateStart(ctx context.Context, name string, spec dockerApply, cred Credential) error {
	cb := dockerCreateBody{Image: spec.image, Labels: map[string]string{dockerSpecHashLabel: dockerSpecHash(spec)}}
	if c := strings.TrimSpace(spec.command); c != "" {
		cb.Cmd = strings.Fields(c) // v1: whitespace-split argv (non-sensitive override)
	}
	for _, er := range spec.envRefs {
		n, ref := strings.TrimSpace(er.Name), strings.TrimSpace(er.SecretRef)
		if n == "" || ref == "" {
			continue
		}
		cb.Env = append(cb.Env, n+"="+ref) // reference only — the workload resolves it; never a value
	}
	applyDockerHardening(&cb, spec) // workspace bind mounts + read-only rootfs + tmpfs + non-root user
	body, err := json.Marshal(cb)
	if err != nil {
		return errors.New("executor: docker create body could not be encoded")
	}
	status, _, err := b.dockerCall(ctx, http.MethodPost, "/containers/create?name="+dockerQueryEscape(name), cred, body)
	if err != nil {
		return fmt.Errorf("executor: docker create failed: %w", err)
	}
	if !ok2xx(status) {
		return fmt.Errorf("executor: docker create returned status %d", status)
	}
	// Start by name (the daemon accepts the name as the id-or-name path segment).
	sstatus, _, err := b.dockerCall(ctx, http.MethodPost, "/containers/"+name+"/start", cred, nil)
	if err != nil {
		return fmt.Errorf("executor: docker start failed: %w", err)
	}
	// 204 = started, 304 = already started (idempotent).
	if !ok2xx(sstatus) && sstatus != http.StatusNotModified {
		return fmt.Errorf("executor: docker start returned status %d", sstatus)
	}
	return nil
}

// dockerStopRemove stops then force-removes a container by its id. A stop on an
// already-stopped container (304) is fine; the force delete removes a running one too.
func (b *DockerBackend) dockerStopRemove(ctx context.Context, c dockerContainer, cred Credential) error {
	id := c.id
	if id == "" {
		return errors.New("executor: docker stop/remove requires a resolved container id")
	}
	sstatus, _, err := b.dockerCall(ctx, http.MethodPost, "/containers/"+id+"/stop", cred, nil)
	if err != nil {
		return fmt.Errorf("executor: docker stop failed: %w", err)
	}
	// 204 = stopped, 304 = already stopped, 404 = already gone — all acceptable.
	if !ok2xx(sstatus) && sstatus != http.StatusNotModified && sstatus != http.StatusNotFound {
		return fmt.Errorf("executor: docker stop returned status %d", sstatus)
	}
	dstatus, _, err := b.dockerCall(ctx, http.MethodDelete, "/containers/"+id+"?force=1", cred, nil)
	if err != nil {
		return fmt.Errorf("executor: docker remove failed: %w", err)
	}
	// 204 = removed, 404 = already gone (idempotent).
	if !ok2xx(dstatus) && dstatus != http.StatusNotFound {
		return fmt.Errorf("executor: docker remove returned status %d", dstatus)
	}
	return nil
}

// --- pure helpers ----------------------------------------------------------------

// dockerCreateBody is the minimal Docker create payload. Env, when present, is a
// list of NAME=<secret-ref> REFERENCE strings — never resolved cleartext. The v1
// create reconciles image+name; richer fields (Cmd/Env wiring) are added by the seam
// when it builds the Desired, always by reference.
type dockerCreateBody struct {
	Image      string            `json:"Image"`
	Cmd        []string          `json:"Cmd,omitempty"`
	Env        []string          `json:"Env,omitempty"`
	Labels     map[string]string `json:"Labels,omitempty"`
	User       string            `json:"User,omitempty"`       // non-root identity, e.g. "65532:65532"
	WorkingDir string            `json:"WorkingDir,omitempty"` // container working dir
	HostConfig *dockerHostConfig `json:"HostConfig,omitempty"` // bind mounts + read-only rootfs + tmpfs
}

// dockerHostConfig is the subset of the Docker create HostConfig needs for a
// hardened workspace mount: bind mounts, a read-only root filesystem, and writable
// tmpfs scratch. It is omitted entirely when no hardening is requested, so a
// pre create body is byte-identical.
type dockerHostConfig struct {
	Binds          []string          `json:"Binds,omitempty"`          // "src:dst" or "src:dst:ro"
	ReadonlyRootfs bool              `json:"ReadonlyRootfs,omitempty"` // mount the rest of the box read-only
	Tmpfs          map[string]string `json:"Tmpfs,omitempty"`          // writable scratch, e.g. {"/tmp": ""}
}

// applyDockerHardening populates the create body's mount/hardening fields from the
// apply spec. It is a no-op when the spec carries none of them, so existing
// (workspace-less) deployments produce the exact pre body. The bind sources are
// trusted as already-canonicalized/jailed by the caller (modules/sessions) — this
// layer only renders the Docker payload, it never resolves a path.
func applyDockerHardening(cb *dockerCreateBody, spec dockerApply) {
	cb.User = spec.user
	cb.WorkingDir = spec.workdir
	var hc dockerHostConfig
	used := false
	for _, m := range spec.mounts {
		src, dst := strings.TrimSpace(m.Source), strings.TrimSpace(m.Target)
		if src == "" || dst == "" {
			continue
		}
		bind := src + ":" + dst
		if m.ReadOnly {
			bind += ":ro"
		}
		hc.Binds = append(hc.Binds, bind)
		used = true
	}
	if spec.readonlyRootfs {
		hc.ReadonlyRootfs = true
		used = true
	}
	for _, t := range spec.tmpfs {
		if t = strings.TrimSpace(t); t != "" {
			if hc.Tmpfs == nil {
				hc.Tmpfs = map[string]string{}
			}
			hc.Tmpfs[t] = ""
			used = true
		}
	}
	if used {
		cb.HostConfig = &hc
	}
}

// dockerSoleItem returns the single change item of a one-item Diff (create/replace/
// delete), used by Apply to know what to do against the daemon.
func dockerSoleItem(d Diff) (ChangeItem, bool) {
	items := d.Items()
	if len(items) != 1 {
		return ChangeItem{}, false
	}
	return items[0], true
}

// dockerImageMatches compares a desired image ref against the daemon's reported
// image. Docker may report a digest or a tag; an empty desired image matches nothing
// meaningful (treated as a mismatch so the operator notices). The compare is exact on
// the normalized tag form, with a tolerant "implicit :latest" normalization.
func dockerImageMatches(actual, desired string) bool {
	if desired == "" {
		return false
	}
	return dockerNormalizeImage(actual) == dockerNormalizeImage(desired)
}

// dockerNormalizeImage normalizes an image ref for comparison: an untagged,
// undigested ref gets an implicit ":latest" (matching Docker's own default) so
// "nginx" and "nginx:latest" compare equal. A digest ref is left untouched.
func dockerNormalizeImage(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	if strings.Contains(ref, "@") {
		return ref // digest-pinned — compare verbatim
	}
	// Find the last path segment to look for a tag colon (avoid a registry-port colon).
	lastSlash := strings.LastIndex(ref, "/")
	seg := ref
	if lastSlash >= 0 {
		seg = ref[lastSlash+1:]
	}
	if !strings.Contains(seg, ":") {
		return ref + ":latest"
	}
	return ref
}

// dockerQueryEscape percent-encodes a query-string value (the Docker API filter
// JSON and the container name) using the stdlib net/url for correctness — keeping a
// thin, backend-prefixed wrapper so call sites read clearly.
func dockerQueryEscape(s string) string { return url.QueryEscape(s) }
