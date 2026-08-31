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
	"strings"
	"time"
)

// NomadBackend is the imperative backend for HashiCorp Nomad. It actuates a single
// Nomad job (the managed unit) over the Nomad HTTP API using the NATIVE plan
// endpoint — it NEVER applies blind:
//
//   - Plan:        POST /v1/job/<id>/plan with {"Job":{...},"Diff":true}. Nomad
//     returns a structured JobDiff whose Type is Added/Edited/Deleted/
//     None (with nested TaskGroup diffs); we first GET /v1/job/<id> so a
//     404 unambiguously means "create". Type None => empty Diff (a true
//     idempotent noop). The saved plan carries the exact job JSON that
//     Apply will register (anti-blind-apply).
//   - DestroyPlan: GET /v1/job/<id>; a present job yields one Destructive delete item,
//     an absent job yields an empty Diff (already gone).
//   - Apply:       a forward plan registers via POST /v1/job/<id> (idempotent — Nomad
//     register of an unchanged job is a no-op edit); a destroy plan
//     deregisters via DELETE /v1/job/<id>?purge=true.
//   - Observe:     GET /v1/job/<id>. 404 => Observable, Exists=false, drift=[create].
//     Present + Status "running" with the desired group count => InSync.
//     Unreachable transport => Observable=false (an HONEST gap, never a
//     faked in-sync).
//
// CREDENTIALS (least privilege, docs/SECURITY-HARDENING.md,§4): the Executor mints a short-lived,
// environment-attested credential; its token is sent ONLY in the X-Nomad-Token
// request header (never in a URL, never in argv, never logged). The TLS client pins
// the API server CA. No ambient long-lived token is ever read.
//
// MINIMAL DATA (docs/SECURITY-HARDENING.md): a Diff/Result/RealState carries only a kind, the
// non-sensitive job id, and a short detail — never the job JSON, an env value, or a
// secret. Secret env values are emitted into the job spec only BY REFERENCE through
// Nomad's native template mechanism (a templated env file), never as cleartext, never
// returned in a struct or logged.
type NomadBackend struct {
	cfg        NomadConfig
	client     nomadDoer
	clientErr  error
	credHeader string // the request header the token is sent in (X-Nomad-Token)
}

// NomadConfig configures the Nomad backend (operator-provisioned, no secrets).
type NomadConfig struct {
	// BaseURL is the Nomad API server base, e.g. "https://nomad.internal:4646". The
	// region from Desired.Target is sent as a ?region= query, not baked into the host.
	BaseURL string
	// CABundle is the PEM CA bundle that pins the Nomad API server's TLS certificate.
	CABundle []byte
	// InsecureSkipVerify disables TLS verification — an explicit operator opt-in for a
	// dev cluster only, NEVER the default.
	InsecureSkipVerify bool
	// Namespace is the Nomad namespace the job lives in (default "default"). Sent as a
	// ?namespace= query; it is non-sensitive routing metadata.
	Namespace string
	// Datacenters lists the datacenters the job may run in (default ["dc1"]). Non-secret.
	Datacenters []string
	// Timeout bounds a single API call (default 30s).
	Timeout time.Duration
}

// nomadDoer is the minimal HTTP seam the backend calls. The production impl wraps the
// frozen doAPI over a tlsBearerClient (built by NewNomadBackend); tests inject a fake
// doer (or one pointed at an httptest server) so no real Nomad daemon is needed.
type nomadDoer interface {
	do(ctx context.Context, req apiRequest) (status int, body []byte, err error)
}

// nomadHTTPDoer is the production nomadDoer: it forwards an apiRequest to the frozen
// doAPI over the pinned TLS *http.Client. It introduces NO custom transport.
type nomadHTTPDoer struct {
	httpClient *http.Client
}

func (d nomadHTTPDoer) do(ctx context.Context, req apiRequest) (int, []byte, error) {
	return doAPI(ctx, d.httpClient, req, maxAPIBody)
}

// NewNomadBackend builds the Nomad backend. It eagerly builds the pinned TLS client
// from the configured CA bundle; a bad CA bundle is captured and surfaced at first use
// so construction never panics (the Executor sees the error on Plan/Observe).
func NewNomadBackend(cfg NomadConfig) *NomadBackend {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	if strings.TrimSpace(cfg.Namespace) == "" {
		cfg.Namespace = "default"
	}
	if len(cfg.Datacenters) == 0 {
		cfg.Datacenters = []string{"dc1"}
	}
	b := &NomadBackend{cfg: cfg, credHeader: nomadTokenHeader}
	hc, err := tlsBearerClient(cfg.CABundle, cfg.InsecureSkipVerify, cfg.Timeout)
	if err != nil {
		b.clientErr = err
		return b
	}
	b.client = nomadHTTPDoer{httpClient: hc}
	return b
}

// Kind returns the runtime selector.
func (b *NomadBackend) Kind() string { return nomadKind }

// --- Backend interface -----------------------------------------------------------

// Plan computes the forward diff using Nomad's native job-plan API. Read-only.
func (b *NomadBackend) Plan(ctx context.Context, d Desired, cred Credential) (Plan, error) {
	if b.clientErr != nil {
		return Plan{}, fmt.Errorf("executor: nomad client unavailable: %w", b.clientErr)
	}
	id := nomadJobID(d)
	jobBody, err := b.buildJobJSON(d)
	if err != nil {
		return Plan{}, err
	}

	// 1) Existence probe: a 404 on GET is the unambiguous "new job => create" signal,
	//    independent of how the plan endpoint classifies a brand-new job.
	exists, _, err := b.getJob(ctx, d, id, cred)
	if err != nil {
		return Plan{}, err
	}

	// 2) Native plan: ask Nomad for the structured diff.
	reqBody, mErr := json.Marshal(nomadPlanRequest{Job: jobBody.Job, Diff: true})
	if mErr != nil {
		return Plan{}, errors.New("executor: cannot encode nomad plan request")
	}
	status, body, err := b.call(ctx, d, apiRequest{
		method:      "POST",
		path:        b.path("/v1/job/"+id+"/plan", d),
		body:        reqBody,
		contentType: "application/json",
		accept:      "application/json",
	}, cred)
	if err != nil {
		return Plan{}, err
	}
	if !ok2xx(status) {
		return Plan{}, fmt.Errorf("executor: nomad plan rejected (status %d)", status)
	}
	var pr nomadPlanResponse
	if jErr := json.Unmarshal(body, &pr); jErr != nil {
		return Plan{}, errors.New("executor: nomad plan response is malformed")
	}

	diff := b.mapPlanDiff(id, exists, pr)
	p := Plan{Runtime: nomadKind, Intent: IntentApply, Diff: diff}
	if !diff.Empty() {
		// The saved handle is the exact job JSON Apply will register (anti-blind-apply).
		// It carries NO secret: env is referenced via templates only.
		p.Handle = string(b.registerBody(jobBody))
	}
	return p, nil
}

// DestroyPlan computes the teardown diff: a present job => one Destructive delete.
func (b *NomadBackend) DestroyPlan(ctx context.Context, d Desired, cred Credential) (Plan, error) {
	if b.clientErr != nil {
		return Plan{}, fmt.Errorf("executor: nomad client unavailable: %w", b.clientErr)
	}
	id := nomadJobID(d)
	exists, _, err := b.getJob(ctx, d, id, cred)
	if err != nil {
		return Plan{}, err
	}
	if !exists {
		return Plan{Runtime: nomadKind, Intent: IntentDestroy,
			Diff: NewDiff(nil, nil, nil, true, "", "nomad job already absent")}, nil
	}
	del := ChangeItem{Action: "delete", Kind: nomadJobKind, Ref: id, Detail: "deregister + purge nomad job", Destructive: true}
	diff := NewDiff(nil, nil, []ChangeItem{del}, false,
		"re-register the job from a prior revision to restore it",
		"nomad destroy: 1 delete")
	// The handle is the concrete target job id; Apply branches on Intent==IntentDestroy.
	return Plan{Runtime: nomadKind, Intent: IntentDestroy, Diff: diff, Handle: nomadDestroyHandle(d, id)}, nil
}

// Apply executes a SAVED plan: register a forward plan, or deregister a destroy plan.
func (b *NomadBackend) Apply(ctx context.Context, p Plan, cred Credential) (Result, error) {
	if b.clientErr != nil {
		return Result{}, fmt.Errorf("executor: nomad client unavailable: %w", b.clientErr)
	}
	if p.Diff.Empty() {
		return Result{Applied: nil, Detail: "no changes to apply"}, nil
	}
	switch p.Intent {
	case IntentDestroy:
		return b.applyDestroy(ctx, p, cred)
	default:
		return b.applyForward(ctx, p, cred)
	}
}

// applyForward registers the saved job JSON. Nomad register is idempotent: registering
// an unchanged job is a no-op edit, so a retried Apply is safe.
func (b *NomadBackend) applyForward(ctx context.Context, p Plan, cred Credential) (Result, error) {
	if p.Handle == "" {
		return Result{}, errors.New("executor: nomad apply has no saved job to register")
	}
	// Recover the desired-spec routing (region/namespace) from the saved job body so the
	// register call targets the same place the plan did.
	var jb nomadJobBody
	if err := json.Unmarshal([]byte(p.Handle), &jb); err != nil {
		return Result{}, errors.New("executor: saved nomad plan is unreadable")
	}
	id := nomadJobIDOf(jb)
	if id == "" {
		return Result{}, errors.New("executor: saved nomad plan has no job id")
	}
	d := b.desiredFromJob(jb)
	body, mErr := json.Marshal(nomadRegisterRequest(jb))
	if mErr != nil {
		return Result{}, errors.New("executor: cannot encode nomad register request")
	}
	status, _, err := b.call(ctx, d, apiRequest{
		method:      "POST",
		path:        b.path("/v1/job/"+id, d),
		body:        body,
		contentType: "application/json",
		accept:      "application/json",
	}, cred)
	if err != nil {
		return Result{}, err
	}
	if !ok2xx(status) {
		return Result{}, fmt.Errorf("executor: nomad register failed (status %d)", status)
	}
	return Result{Applied: p.Diff.Items(), Detail: p.Diff.Summary}, nil
}

// applyDestroy deregisters the job with purge=true. A destroy handle encodes the
// region+id so the DELETE targets the correct region/namespace.
func (b *NomadBackend) applyDestroy(ctx context.Context, p Plan, cred Credential) (Result, error) {
	region, id := nomadParseDestroyHandle(p.Handle)
	if id == "" {
		return Result{}, errors.New("executor: nomad destroy has no target job id")
	}
	d := Desired{Runtime: nomadKind, Target: "nomad.region/" + region}
	status, _, err := b.call(ctx, d, apiRequest{
		method: "DELETE",
		path:   b.pathWith("/v1/job/"+id, d, "purge=true"),
		accept: "application/json",
	}, cred)
	if err != nil {
		return Result{}, err
	}
	// A 404 here means the job was already gone — an idempotent destroy, not a failure.
	if !ok2xx(status) && status != http.StatusNotFound {
		return Result{}, fmt.Errorf("executor: nomad deregister failed (status %d)", status)
	}
	return Result{Applied: p.Diff.Items(), Detail: p.Diff.Summary}, nil
}

// Rollback reverses a prior apply. For Nomad a rollback is a re-register of a prior job
// version (owned by the deploy module's revision history / Nomad's own job versions),
// not a state-file operation; we report that honestly rather than faking it.
func (b *NomadBackend) Rollback(_ context.Context, _ Plan, _ Credential) (Result, error) {
	return Result{}, errors.New("executor: nomad rollback is a re-register of a prior job version (deploy module revision history / nomad job revert), not handled as a state operation here")
}

// Observe reads the REAL state of the job. An unreachable API is an honest gap.
func (b *NomadBackend) Observe(ctx context.Context, d Desired, cred Credential) (RealState, error) {
	if b.clientErr != nil {
		return RealState{Observable: false, Detail: "nomad client unavailable (TLS/CA misconfigured)"}, nil
	}
	id := nomadJobID(d)
	exists, job, err := b.getJob(ctx, d, id, cred)
	if err != nil {
		// A transport/non-404 error is an honest gap — never a faked in-sync.
		return RealState{Observable: false, Detail: "nomad job unreadable: " + err.Error()}, nil
	}
	if !exists {
		return RealState{
			Exists:     false,
			Observable: true,
			InSync:     false,
			Drift:      []ChangeItem{{Action: "create", Kind: nomadJobKind, Ref: id, Detail: "job not registered in nomad"}},
			Detail:     "job absent",
		}, nil
	}
	want := d.Replicas
	if want <= 0 {
		want = 1
	}
	got := nomadGroupCount(job)
	if strings.EqualFold(job.Status, "running") && got == want {
		return RealState{Exists: true, Observable: true, InSync: true,
			Detail: fmt.Sprintf("running with %d/%d group count", got, want)}, nil
	}
	drift := ChangeItem{Action: "update", Kind: nomadJobKind, Ref: id,
		Detail: fmt.Sprintf("status=%s count=%d/%d", nomadStatusLabel(job.Status), got, want)}
	return RealState{Exists: true, Observable: true, InSync: false,
		Drift: []ChangeItem{drift}, Detail: "job present but not matching desired"}, nil
}

// --- HTTP helpers ----------------------------------------------------------------

// getJob fetches /v1/job/<id>. Returns exists=false on 404 (and on an empty/null body,
// which some Nomad versions return for an unknown id).
func (b *NomadBackend) getJob(ctx context.Context, d Desired, id string, cred Credential) (bool, nomadJob, error) {
	status, body, err := b.call(ctx, d, apiRequest{
		method: "GET",
		path:   b.path("/v1/job/"+id, d),
		accept: "application/json",
	}, cred)
	if err != nil {
		return false, nomadJob{}, err
	}
	if status == http.StatusNotFound {
		return false, nomadJob{}, nil
	}
	if !ok2xx(status) {
		return false, nomadJob{}, fmt.Errorf("status %d", status)
	}
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" || trimmed == "null" {
		return false, nomadJob{}, nil
	}
	var job nomadJob
	if jErr := json.Unmarshal(body, &job); jErr != nil {
		return false, nomadJob{}, errors.New("malformed job response")
	}
	if strings.TrimSpace(job.ID) == "" {
		return false, nomadJob{}, nil
	}
	return true, job, nil
}

// call issues one API request with the X-Nomad-Token header carrying the credential.
func (b *NomadBackend) call(ctx context.Context, _ Desired, req apiRequest, cred Credential) (int, []byte, error) {
	if b.client == nil {
		return 0, nil, errors.New("executor: nomad client not initialized")
	}
	req.baseURL = strings.TrimRight(b.cfg.BaseURL, "/")
	if req.headers == nil {
		req.headers = map[string]string{}
	}
	if cred.Token != "" {
		// The token goes in the header ONLY — never a URL, never argv, never logged.
		req.headers[b.credHeader] = cred.Token
	}
	return b.client.do(ctx, req)
}

// path appends the region/namespace query (non-sensitive routing) to a base path.
func (b *NomadBackend) path(p string, d Desired) string {
	return b.pathWith(p, d, "")
}

// pathWith appends an optional extra query plus the region/namespace routing.
func (b *NomadBackend) pathWith(p string, d Desired, extra string) string {
	parts := []string{}
	if extra != "" {
		parts = append(parts, extra)
	}
	if r := nomadRegion(d.Target); r != "" {
		parts = append(parts, "region="+nomadQueryEscape(r))
	}
	if b.cfg.Namespace != "" {
		parts = append(parts, "namespace="+nomadQueryEscape(b.cfg.Namespace))
	}
	if len(parts) == 0 {
		return p
	}
	return p + "?" + strings.Join(parts, "&")
}

// --- job JSON construction -------------------------------------------------------

// buildJobJSON builds the minimal Nomad job from the desired spec. Env values are
// emitted only BY REFERENCE (a template directive resolved by Nomad's own secret
// mechanism), never as cleartext.
func (b *NomadBackend) buildJobJSON(d Desired) (nomadJobBody, error) {
	if strings.TrimSpace(d.SubjectRef) == "" && strings.TrimSpace(d.Name) == "" {
		return nomadJobBody{}, errors.New("executor: nomad job requires a subject ref (job id)")
	}
	if strings.TrimSpace(d.Image) == "" {
		return nomadJobBody{}, errors.New("executor: nomad job requires an image")
	}
	id := nomadJobID(d)
	count := d.Replicas
	if count <= 0 {
		count = 1
	}

	cfg := map[string]any{"image": d.Image}
	if cmd := strings.TrimSpace(d.Command); cmd != "" {
		// Pass the command override as args; the value is non-sensitive.
		cfg["args"] = nomadSplitArgs(cmd)
	}

	task := nomadTask{Name: id, Driver: "docker", Config: cfg}
	if res := nomadResources(d.Resources); res != nil {
		task.Resources = res
	}
	if tmpl := nomadEnvTemplate(d.EnvRefs); tmpl != nil {
		task.Templates = []nomadTemplate{*tmpl}
	}

	job := nomadJob{
		ID:          id,
		Name:        id,
		Type:        "service",
		Datacenters: b.cfg.Datacenters,
		Namespace:   b.cfg.Namespace,
		Region:      nomadRegion(d.Target),
		TaskGroups: []nomadTaskGroup{{
			Name:  id,
			Count: count,
			Tasks: []nomadTask{task},
		}},
	}
	return nomadJobBody{Job: job}, nil
}

// registerBody serializes the saved job body as the plan handle (the exact JSON Apply
// registers). It carries NO secret: env is referenced via templates only.
func (b *NomadBackend) registerBody(jb nomadJobBody) []byte {
	out, err := json.Marshal(jb)
	if err != nil {
		return []byte("{}")
	}
	return out
}

// desiredFromJob recovers the minimal routing (region) from a saved job body so a
// register call targets the same place the plan ran.
func (b *NomadBackend) desiredFromJob(jb nomadJobBody) Desired {
	return Desired{Runtime: nomadKind, Target: "nomad.region/" + jb.Job.Region}
}

// mapPlanDiff maps a Nomad JobPlanResponse to a neutral Diff.
//
//   - A 404 on the prior GET (exists=false) => create.
//   - Diff.Type "Added"   => create.
//   - Diff.Type "Edited"  => update.
//   - Diff.Type "Deleted" => delete (Destructive).
//   - Diff.Type "None" (and the job already exists) => empty Diff (idempotent noop).
func (b *NomadBackend) mapPlanDiff(id string, exists bool, pr nomadPlanResponse) Diff {
	typ := strings.ToLower(strings.TrimSpace(pr.Diff.Type))

	// A brand-new job (404 on GET) is always a create regardless of how the plan labels
	// it — the most honest classification for the operator.
	if !exists {
		item := ChangeItem{Action: "create", Kind: nomadJobKind, Ref: id, Detail: "register new nomad job"}
		return NewDiff([]ChangeItem{item}, nil, nil, true, "deregister the job to roll back", "nomad plan: 1 create")
	}

	switch typ {
	case "added":
		item := ChangeItem{Action: "create", Kind: nomadJobKind, Ref: id, Detail: "register new nomad job"}
		return NewDiff([]ChangeItem{item}, nil, nil, true, "deregister the job to roll back", "nomad plan: 1 create")
	case "edited":
		item := ChangeItem{Action: "update", Kind: nomadJobKind, Ref: id, Detail: nomadEditDetail(pr.Diff)}
		return NewDiff(nil, []ChangeItem{item}, nil, true, "re-register the prior job version to roll back", "nomad plan: 1 update")
	case "deleted":
		item := ChangeItem{Action: "delete", Kind: nomadJobKind, Ref: id, Detail: "job would be removed", Destructive: true}
		return NewDiff(nil, nil, []ChangeItem{item}, false, "re-register the job from a prior revision", "nomad plan: 1 delete")
	default: // "None" or empty => no change
		return NewDiff(nil, nil, nil, true, "", "nomad plan: no changes (up to date)")
	}
}

// --- nomad wire types ------------------------------------------------------------

// nomadJobBody is the {"Job":{...}} envelope used by register and carried as the plan
// handle. It is the canonical Nomad job submission shape.
type nomadJobBody struct {
	Job nomadJob `json:"Job"`
}

// nomadJob is the minimal Nomad job structure we build and read back.
type nomadJob struct {
	ID          string           `json:"ID"`
	Name        string           `json:"Name,omitempty"`
	Type        string           `json:"Type,omitempty"`
	Status      string           `json:"Status,omitempty"`
	Namespace   string           `json:"Namespace,omitempty"`
	Region      string           `json:"Region,omitempty"`
	Datacenters []string         `json:"Datacenters,omitempty"`
	TaskGroups  []nomadTaskGroup `json:"TaskGroups,omitempty"`
}

type nomadTaskGroup struct {
	Name  string      `json:"Name"`
	Count int         `json:"Count"`
	Tasks []nomadTask `json:"Tasks,omitempty"`
}

type nomadTask struct {
	Name      string          `json:"Name"`
	Driver    string          `json:"Driver"`
	Config    map[string]any  `json:"Config,omitempty"`
	Resources *nomadResource  `json:"Resources,omitempty"`
	Templates []nomadTemplate `json:"Templates,omitempty"`
}

type nomadResource struct {
	CPU      int `json:"CPU,omitempty"`
	MemoryMB int `json:"MemoryMB,omitempty"`
}

// nomadTemplate is Nomad's native template stanza — the runtime-side mechanism that
// materializes secret env values BY REFERENCE at task start. We never put a secret in
// the data; we emit reference directives only.
type nomadTemplate struct {
	EmbeddedTmpl string `json:"EmbeddedTmpl"`
	DestPath     string `json:"DestPath"`
	Envvars      bool   `json:"Envvars"`
}

// nomadPlanRequest is the body of POST /v1/job/<id>/plan.
type nomadPlanRequest struct {
	Job  nomadJob `json:"Job"`
	Diff bool     `json:"Diff"`
}

// nomadRegisterRequest is the body of POST /v1/job/<id> (register).
type nomadRegisterRequest struct {
	Job nomadJob `json:"Job"`
}

// nomadPlanResponse is the subset of Nomad's JobPlanResponse we use.
type nomadPlanResponse struct {
	Diff           nomadJobDiff          `json:"Diff"`
	Annotations    *nomadPlanAnnotations `json:"Annotations,omitempty"`
	FailedTGAllocs map[string]any        `json:"FailedTGAllocs,omitempty"`
	JobModifyIndex uint64                `json:"JobModifyIndex,omitempty"`
}

// nomadJobDiff is the structured plan diff. Type is Added/Edited/Deleted/None.
type nomadJobDiff struct {
	Type       string               `json:"Type"`
	ID         string               `json:"ID,omitempty"`
	Fields     []nomadFieldDiff     `json:"Fields,omitempty"`
	TaskGroups []nomadTaskGroupDiff `json:"TaskGroups,omitempty"`
}

type nomadFieldDiff struct {
	Type string `json:"Type"`
	Name string `json:"Name"`
}

type nomadTaskGroupDiff struct {
	Type string `json:"Type"`
	Name string `json:"Name"`
}

type nomadPlanAnnotations struct {
	DesiredTGUpdates map[string]any `json:"DesiredTGUpdates,omitempty"`
}

// --- helpers ---------------------------------------------------------------------

// nomadJobID derives the Nomad job id from the desired subject ref (or Name as a
// fallback). Nomad ids are non-sensitive; it never embeds a secret.
func nomadJobID(d Desired) string {
	id := strings.TrimSpace(d.SubjectRef)
	if id == "" {
		id = strings.TrimSpace(d.Name)
	}
	return id
}

// nomadJobIDOf reads the id from a saved job body.
func nomadJobIDOf(jb nomadJobBody) string { return strings.TrimSpace(jb.Job.ID) }

// nomadRegion extracts the region from a "nomad.region/<region>" target ref. An empty
// or scheme-less target yields "" (the API server's default region applies).
func nomadRegion(target string) string {
	t := strings.TrimSpace(target)
	if t == "" {
		return ""
	}
	if i := strings.Index(t, "/"); i >= 0 {
		return strings.TrimSpace(t[i+1:])
	}
	return ""
}

// nomadGroupCount sums the desired task-group counts of a job (the replica count).
func nomadGroupCount(job nomadJob) int {
	n := 0
	for _, g := range job.TaskGroups {
		n += g.Count
	}
	return n
}

// nomadStatusLabel returns a short, non-sensitive status label.
func nomadStatusLabel(s string) string {
	if s = strings.TrimSpace(s); s != "" {
		return s
	}
	return "unknown"
}

// nomadEditDetail summarizes an Edited diff without leaking any value: it counts the
// changed task groups / fields. No field value is included (minimal data).
func nomadEditDetail(d nomadJobDiff) string {
	switch {
	case len(d.TaskGroups) > 0:
		return fmt.Sprintf("job edited (%d task group change(s))", len(d.TaskGroups))
	case len(d.Fields) > 0:
		return fmt.Sprintf("job edited (%d field change(s))", len(d.Fields))
	default:
		return "job edited"
	}
}

// nomadSplitArgs splits a command override into whitespace-separated args (non-secret).
func nomadSplitArgs(cmd string) []string { return strings.Fields(cmd) }

// nomadResources maps non-sensitive compute requests to a Nomad resource stanza. Only
// the well-known keys are read; unknown keys are ignored (minimal, explicit).
func nomadResources(res map[string]string) *nomadResource {
	if len(res) == 0 {
		return nil
	}
	out := &nomadResource{}
	changed := false
	if n := nomadAtoi(res["cpu"]); n > 0 {
		out.CPU = n
		changed = true
	}
	if n := nomadAtoi(res["memory_mb"]); n > 0 {
		out.MemoryMB = n
		changed = true
	}
	if !changed {
		return nil
	}
	return out
}

// nomadAtoi parses a small non-negative integer; returns 0 on any malformed input.
func nomadAtoi(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// nomadEnvTemplate builds a Nomad template stanza that wires secret env values BY
// REFERENCE. For each binding we emit a template directive that names the secret
// reference (a non-secret locator) — Nomad's own template engine resolves it at task
// start. The function NEVER places secret material in the returned structure; only the
// non-sensitive reference locator and the env var name appear, exactly as the operator
// declared them. Returns nil when there are no bindings.
func nomadEnvTemplate(refs []SecretBinding) *nomadTemplate {
	if len(refs) == 0 {
		return nil
	}
	var sb strings.Builder
	for _, r := range refs {
		name := strings.TrimSpace(r.Name)
		ref := strings.TrimSpace(r.SecretRef)
		if name == "" || ref == "" {
			continue
		}
		// Emit a reference directive only. The locator is a non-secret pointer
		// ("<scheme>:<locator>"); the actual value is fetched by Nomad at runtime via
		// its native secret backend. No cleartext, ever.
		fmt.Fprintf(&sb, "%s={{ with nomadVar %q }}{{ .value }}{{ end }}\n", name, ref)
	}
	if sb.Len() == 0 {
		return nil
	}
	return &nomadTemplate{EmbeddedTmpl: sb.String(), DestPath: "secrets/env", Envvars: true}
}

// nomadQueryEscape escapes a query value minimally (the values are non-secret routing
// metadata). We avoid net/url to keep the helper trivial; only a handful of characters
// need escaping for region/namespace names.
func nomadQueryEscape(s string) string {
	replacer := strings.NewReplacer(" ", "%20", "&", "%26", "?", "%3F", "#", "%23", "=", "%3D")
	return replacer.Replace(strings.TrimSpace(s))
}

// nomadDestroyHandle encodes the region + job id into the destroy plan handle so Apply
// can target the right region/namespace. Format: "<region>\n<id>". Non-secret.
func nomadDestroyHandle(d Desired, id string) string {
	return nomadRegion(d.Target) + "\n" + id
}

// nomadParseDestroyHandle splits a destroy handle into region + id.
func nomadParseDestroyHandle(h string) (region, id string) {
	if i := strings.IndexByte(h, '\n'); i >= 0 {
		return h[:i], h[i+1:]
	}
	return "", h
}

// constants
const (
	nomadKind        = "nomad"
	nomadJobKind     = "nomad.job"
	nomadTokenHeader = "X-Nomad-Token"
)
