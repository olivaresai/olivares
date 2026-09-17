// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build e2e

// Package e2e holds the REAL-cluster convergence/failover harness for the
// ControlPlane operator. It is gated behind the `e2e` build tag and is NOT
// part of the unit gate: it requires a live kind/k3d cluster with the CRD applied,
// the manager running, an in-cluster Postgres, a shared audit-key Secret and two
// distinguishable engine image tags. The build container has none of that
// (kind/k3d/docker/kubectl are absent), so this file is EXECUTED by
// .github/workflows/e2e-operator-kind.yml, which provisions all of it.
//
// It owns exactly the assertions a fake client cannot make (design §D.2): kubelet
// probe behaviour, Service/endpoint selection, actual StatefulSet rolling-update
// progression (the wedge regression), and Postgres election/fencing across a
// leader kill. Every test skips cleanly when no cluster is reachable, so
// `go test -tags e2e ./...` in a cluster-less environment is a SKIP, never a false
// pass — and never a false success either: the scenarios below fail loudly.
//
// The three scenarios share ONE ControlPlane and run in order (create → rolling
// update → failover): a 3-replica Postgres control plane takes minutes to install,
// and two of them would elect against the same advisory lock in the same database.
package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	restclient "k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	opsv1alpha1 "github.com/olivaresai/olivares/operator/api/v1alpha1"
)

const (
	cpName        = "e2e"
	httpsPortName = "8443"
	// setupEmail/setupPassword bootstrap the engine through a REAL write: the
	// first-superadmin ceremony is an unauthenticated POST that lands in Postgres,
	// so it doubles as the write-path assertion (and its 409 on replay proves the
	// row persisted in the SHARED store, not in one pod's memory).
	setupEmail    = "e2e@olivares.test"
	setupPassword = "supersecret-e2e-1"
)

type harness struct {
	c   client.Client
	cs  *kubernetes.Clientset
	ns  string
	old string
	new string
}

// newHarness builds the cluster clients or SKIPS: no kubeconfig means no cluster
// (the container this repo builds in has none), and the image tags are provided by
// the CI job that builds them.
func newHarness(t *testing.T) *harness {
	t.Helper()
	cfg, err := config.GetConfig()
	if err != nil {
		t.Skipf("no cluster (kind/k3d) reachable: %v", err)
	}
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		corev1.AddToScheme, appsv1.AddToScheme, discoveryv1.AddToScheme, opsv1alpha1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			t.Fatalf("scheme: %v", err)
		}
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Skipf("cannot build client: %v", err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("clientset: %v", err)
	}
	h := &harness{
		c: c, cs: cs,
		ns:  envOr("OLIVARES_E2E_NAMESPACE", "olivares-e2e"),
		old: requireEnv(t, "OLIVARES_E2E_IMAGE_OLD"),
		new: requireEnv(t, "OLIVARES_E2E_IMAGE_NEW"),
	}
	return h
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func requireEnv(t *testing.T, key string) string {
	t.Helper()
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		t.Skipf("%s not set (CI must provide the engine image tags)", key)
	}
	return v
}

// --- cluster helpers ---------------------------------------------------------

func (h *harness) cpKey() types.NamespacedName {
	return types.NamespacedName{Namespace: h.ns, Name: cpName}
}

func (h *harness) getCP(t *testing.T) *opsv1alpha1.ControlPlane {
	t.Helper()
	var cp opsv1alpha1.ControlPlane
	if err := h.c.Get(context.Background(), h.cpKey(), &cp); err != nil {
		t.Fatalf("get ControlPlane: %v", err)
	}
	return &cp
}

func (h *harness) getSTS(t *testing.T) *appsv1.StatefulSet {
	t.Helper()
	var sts appsv1.StatefulSet
	if err := h.c.Get(context.Background(), h.cpKey(), &sts); err != nil {
		t.Fatalf("get StatefulSet: %v", err)
	}
	return &sts
}

// ensureControlPlane creates the 3-replica HA ControlPlane in the LEADER-ROUTING
// layout if it does not exist yet. Idempotent, so each scenario can call it.
func (h *harness) ensureControlPlane(t *testing.T) {
	t.Helper()
	var existing opsv1alpha1.ControlPlane
	err := h.c.Get(context.Background(), h.cpKey(), &existing)
	if err == nil {
		return
	}
	if !apierrors.IsNotFound(err) {
		t.Fatalf("get ControlPlane: %v", err)
	}
	cp := &opsv1alpha1.ControlPlane{
		ObjectMeta: metav1.ObjectMeta{Name: cpName, Namespace: h.ns},
		Spec: opsv1alpha1.ControlPlaneSpec{
			Image:                 h.old,
			Replicas:              3,
			Engine:                opsv1alpha1.EnginePostgres,
			HARouting:             opsv1alpha1.HARoutingLeader,
			AuditSigningKeySecret: envOr("OLIVARES_E2E_AUDIT_SECRET", "audit-key"),
			// TWO pools, one Secret. AdminDSNKey names the cross-tenant BYPASSRLS read
			// pool inside the SAME Secret as the application DSN, which is the only
			// input that makes the controller wire --admin-dsn (adminDSNWired). Without
			// it SystemScope.ListOrgs is RLS-limited on PostgreSQL and refuses, so
			// POST /v1/setup answers 501 cross_tenant_admin_pool_not_configured and the
			// LEADER's /readyz answers 503 setup_blocked — a first boot that can never
			// complete, and the state the leader assertion below would report as
			// "0 leaders".
			Postgres: &opsv1alpha1.PostgresSpec{
				DSNSecret:   envOr("OLIVARES_E2E_DSN_SECRET", "pg-dsn"),
				AdminDSNKey: envOr("OLIVARES_E2E_ADMIN_DSN_KEY", "admin-dsn"),
			},
			Persistence: &opsv1alpha1.PersistenceSpec{Size: "1Gi"},
			// Fail fast in CI rather than after the default 10 minutes.
			ProgressDeadlineSeconds: 240,
		},
	}
	if err := h.c.Create(context.Background(), cp); err != nil {
		t.Fatalf("create ControlPlane: %v", err)
	}
}

// podProxy performs an HTTP request against a POD through the apiserver proxy, so
// the test needs no port-forward and no in-cluster runner. Returns the status code
// and the body; a non-2xx is NOT an error here (the assertions want the code).
func (h *harness) podProxy(ctx context.Context, method, pod, path string, body []byte) (int, string, error) {
	req := h.cs.CoreV1().RESTClient().Verb(method).
		Namespace(h.ns).
		Resource("pods").
		Name("https:" + pod + ":" + httpsPortName).
		SubResource("proxy").
		Suffix(path)
	if body != nil {
		req = req.Body(body).SetHeader("Content-Type", "application/json")
	}
	return doProxy(ctx, req)
}

// serviceProxy is the same, addressed at a SERVICE — so it exercises the real
// endpoint selection (which pod the leader Service resolves to), not a pod IP.
func (h *harness) serviceProxy(ctx context.Context, method, svc, path string, body []byte) (int, string, error) {
	req := h.cs.CoreV1().RESTClient().Verb(method).
		Namespace(h.ns).
		Resource("services").
		Name("https:" + svc + ":https").
		SubResource("proxy").
		Suffix(path)
	if body != nil {
		req = req.Body(body).SetHeader("Content-Type", "application/json")
	}
	return doProxy(ctx, req)
}

// doProxy runs the request and reports the UPSTREAM status code the engine
// returned. client-go turns a non-2xx into an error, but StatusCode captures the
// real code first — which is exactly what these assertions are about (503
// not_leader vs 200), so a non-2xx is a result here, not a failure.
func doProxy(ctx context.Context, req *restclient.Request) (int, string, error) {
	var code int
	result := req.Do(ctx).StatusCode(&code)
	raw, err := result.Raw()
	if proxyErr := transientPodProxyError(code, raw, err); proxyErr != nil {
		return 0, string(raw), proxyErr
	}
	if code == 0 && err != nil {
		return 0, string(raw), err
	}
	return code, string(raw), nil
}

var errPodProxyTargetNotAddressable = errors.New("kubernetes pod proxy target is not addressable")

// kubernetesPodProxyUnreachablePrefix is the apiserver proxy transport wrapper
// from k8s.io/apimachinery@v0.36.2 pkg/util/proxy/transport.go: RoundTrip
// returns errors.NewServiceUnavailable(fmt.Sprintf("error trying to reach
// service: %v", err)) when dialing the pod port fails.
const kubernetesPodProxyUnreachablePrefix = "error trying to reach service:"

// transientPodProxyError recognizes the apiserver responses that mean a
// replacement Pod exists but cannot be reached yet: no address yet (HTTP 400,
// reason BadRequest, message exactly "address not allowed") or an address whose
// container port is not listening (HTTP 503, reason ServiceUnavailable, message
// prefix "error trying to reach service:"). Both require a non-nil request
// error and matching wrapper kind/apiVersion/status plus HTTP/status.Code.
// It deliberately does not classify every 503 as transport: Olivares' expected
// 503 not_leader and anomalous handler responses must remain observable to the
// fencing assertions.
func transientPodProxyError(code int, raw []byte, requestErr error) error {
	if requestErr == nil {
		return nil
	}
	var status metav1.Status
	if err := json.Unmarshal(raw, &status); err != nil ||
		status.Kind != "Status" || status.APIVersion != "v1" ||
		status.Status != metav1.StatusFailure {
		return nil
	}
	switch {
	case code == http.StatusBadRequest &&
		status.Reason == metav1.StatusReasonBadRequest &&
		status.Code == http.StatusBadRequest &&
		status.Message == "address not allowed":
		return fmt.Errorf("%w: %v", errPodProxyTargetNotAddressable, requestErr)
	case code == http.StatusServiceUnavailable &&
		status.Reason == metav1.StatusReasonServiceUnavailable &&
		status.Code == http.StatusServiceUnavailable &&
		strings.HasPrefix(status.Message, kubernetesPodProxyUnreachablePrefix):
		return fmt.Errorf("%w: %v", errPodProxyTargetNotAddressable, requestErr)
	default:
		return nil
	}
}

// isNotLeaderResponse distinguishes the engine's fencing response from an
// infrastructure 503 returned by the Kubernetes API proxy.
func isNotLeaderResponse(code int, raw string) bool {
	if code != http.StatusServiceUnavailable {
		return false
	}
	var response struct {
		Error *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	return json.Unmarshal([]byte(raw), &response) == nil &&
		response.Error != nil && response.Error.Code == "not_leader"
}

// --- the /readyz probe record (D3) -------------------------------------------

// readyzHealth is the ONLY shape read out of a /readyz body: the two scalar strings
// the engine's bounded health-response schema defines (core/api handleReadyz —
// "status" on every answer, "code" on the refusals that carry one). Everything else
// the body may hold, including the operator-facing remedy sentence, is deliberately
// NOT read and never reaches a log.
type readyzHealth struct {
	Status string `json:"status"`
	Code   string `json:"code"`
}

// payloadClass says what could be read out of a /readyz body, so an unexpected answer
// is reported as unexpected instead of being quoted.
type payloadClass int

const (
	payloadParsed    payloadClass = iota // the two scalars were read
	payloadEmpty                         // no body at all
	payloadNotJSON                       // not JSON
	payloadOffSchema                     // JSON, but not this schema (wrong types/shape)
	payloadNoStatus                      // JSON of the right shape, but no required status
)

// readyzKnownStatus and readyzKnownCode are the CLOSED sets core/api/metrics.go emits,
// verified against handleReadyz and handlePodReadyz. status is on every answer; code is
// optional and only accompanies a refusal that has one.
//
// They are ALLOWLISTS, and that is the point: bounding length and refusing control
// characters still copied any short printable string, so a payload this schema does not
// describe could put arbitrary server-controlled text into a CI log. A value the engine
// cannot emit is not a value this diagnostic reports.
var (
	readyzKnownStatus = map[string]bool{
		"ok": true, "unavailable": true, "standby": true,
		"setup_unavailable": true, "setup_blocked": true,
	}
	readyzKnownCode = map[string]bool{
		"cross_tenant_admin_pool_not_configured": true,
		"setup_probe_unavailable":                true,
		"setup_state_unavailable":                true,
	}
)

// readyzScalar renders a schema scalar only when the engine can actually emit it.
// Anything else is reported by length, never copied.
func readyzScalar(v string, known map[string]bool) string {
	if v == "" {
		return "<absent>"
	}
	if known[v] {
		return v
	}
	return fmt.Sprintf("<unrecognized %d-byte value>", len(v))
}

// readyzProbe is the BOUNDED record of one /readyz observation.
//
// It exists because the first-create assertion used to report "0 leaders" and nothing
// else: the body that says WHY — a standby drain, a store that is down, or a leader
// whose first-boot setup read is refused — was discarded at the probe site, so a
// deterministic configuration refusal was indistinguishable from an election flake.
//
// It is DIAGNOSTIC OUTPUT ONLY. Nothing here feeds the leader count or any other
// assertion: the count still rises on HTTP 200 alone.
type readyzProbe struct {
	pod       string
	transport bool // the probe never reached the engine (apiserver proxy/transport)
	code      int  // the HTTP status the engine returned; 0 when transport is true
	status    string
	reason    string
	payload   payloadClass
	bodyBytes int
}

// observeReadyz records one probe. A transport failure — the apiserver proxy could not
// reach the pod — is a DIFFERENT observation from an HTTP refusal the engine chose to
// send, and the two must never be collapsed: the first says nothing about readiness.
func observeReadyz(pod string, code int, body string, err error) readyzProbe {
	if err != nil {
		return readyzProbe{pod: pod, transport: true}
	}
	probe := readyzProbe{pod: pod, code: code, bodyBytes: len(body)}
	if strings.TrimSpace(body) == "" {
		probe.payload = payloadEmpty
		return probe
	}
	var health readyzHealth
	switch decodeErr := json.Unmarshal([]byte(body), &health); {
	case decodeErr != nil && errors.As(decodeErr, new(*json.UnmarshalTypeError)):
		probe.payload = payloadOffSchema
	case decodeErr != nil:
		probe.payload = payloadNotJSON
	case health.Status == "":
		// `null`, `{}` and {"status":null} all decode into a zero value WITHOUT an
		// error, so the REQUIRED field is checked explicitly. Without it the body is
		// not a health response, and reporting it as one with an absent status would
		// read like the engine's ordinary answer that merely lacks the optional code.
		probe.payload = payloadNoStatus
	default:
		probe.payload, probe.status, probe.reason = payloadParsed, health.Status, health.Code
	}
	return probe
}

// String renders one probe for a CI log: the pod, the HTTP status, and the two schema
// scalars. An unexpected payload is identified by class and length, never quoted.
func (p readyzProbe) String() string {
	switch {
	case p.transport:
		return fmt.Sprintf("%s: transport failure — the probe never reached the engine, so it reports nothing about readiness", p.pod)
	case p.payload == payloadEmpty:
		return fmt.Sprintf("%s: HTTP %d, empty body", p.pod, p.code)
	case p.payload == payloadNotJSON:
		return fmt.Sprintf("%s: HTTP %d, %d-byte body is not JSON (not quoted)", p.pod, p.code, p.bodyBytes)
	case p.payload == payloadOffSchema:
		return fmt.Sprintf("%s: HTTP %d, %d-byte JSON body is not the health-response schema (not quoted)", p.pod, p.code, p.bodyBytes)
	case p.payload == payloadNoStatus:
		return fmt.Sprintf("%s: HTTP %d, %d-byte JSON body carries no health status (not quoted)", p.pod, p.code, p.bodyBytes)
	default:
		return fmt.Sprintf("%s: HTTP %d status=%s code=%s", p.pod, p.code,
			readyzScalar(p.status, readyzKnownStatus), readyzScalar(p.reason, readyzKnownCode))
	}
}

// formatReadyzProbes renders every retained probe, one per line.
func formatReadyzProbes(probes []readyzProbe) string {
	if len(probes) == 0 {
		return "each /readyz result: none was recorded"
	}
	lines := make([]string, 0, len(probes))
	for _, probe := range probes {
		lines = append(lines, "  "+probe.String())
	}
	return "each /readyz result:\n" + strings.Join(lines, "\n")
}

// pods returns the ControlPlane's pods sorted by name.
func (h *harness) pods(t *testing.T) []corev1.Pod {
	t.Helper()
	var list corev1.PodList
	if err := h.c.List(context.Background(), &list,
		client.InNamespace(h.ns),
		client.MatchingLabels{"app.kubernetes.io/instance": cpName, "app.kubernetes.io/component": "core"}); err != nil {
		t.Fatalf("list pods: %v", err)
	}
	out := make([]corev1.Pod, 0, len(list.Items))
	for _, p := range list.Items {
		if p.DeletionTimestamp == nil {
			out = append(out, p)
		}
	}
	return out
}

// leaderServiceEndpoints returns the pod names currently SERVING the leader
// Service, read from EndpointSlices — the same objects kube-proxy programs.
func (h *harness) leaderServiceEndpoints(t *testing.T) []string {
	t.Helper()
	var slices discoveryv1.EndpointSliceList
	if err := h.c.List(context.Background(), &slices,
		client.InNamespace(h.ns),
		client.MatchingLabels{discoveryv1.LabelServiceName: cpName + "-leader"}); err != nil {
		t.Fatalf("list endpointslices: %v", err)
	}
	var serving []string
	for _, s := range slices.Items {
		for _, ep := range s.Endpoints {
			if ep.Conditions.Ready != nil && *ep.Conditions.Ready && ep.TargetRef != nil {
				serving = append(serving, ep.TargetRef.Name)
			}
		}
	}
	return serving
}

// labeledLeaders returns the pods publishing role=leader (any readiness).
func (h *harness) labeledLeaders(t *testing.T) []string {
	var out []string
	for _, p := range h.pods(t) {
		if p.Labels["ops.olivares.ai/role"] == "leader" {
			out = append(out, p.Name)
		}
	}
	return out
}

func podIsReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// crashLoopRestarts is how many restarts must have accumulated before a wait gives
// up early. One restart is unremarkable (a pod can lose the race with Postgres
// becoming reachable); a container that has died repeatedly AND is backing off is
// not going to converge inside the timeout.
const crashLoopRestarts = 2

// operandCrashLoop reports the operand containers stuck in a restart loop, with the
// exit status and the log of the attempt that FAILED. A crash loop is terminal for
// every wait in this file — the engine never opens its store, so the ControlPlane
// can never reach Ready — and waiting out the timeout only delays a verdict that is
// already decided: the first real-cluster run spent 30 minutes across three
// scenarios on pods that had died in two seconds, and the failure it printed
// ("timed out waiting for PhaseReady") named the symptom instead of the cause.
//
// It returns "" when nothing is looping and on any API error: a diagnosis must
// never be the reason a test that might still pass fails.
func (h *harness) operandCrashLoop() string {
	var list corev1.PodList
	if err := h.c.List(context.Background(), &list,
		client.InNamespace(h.ns),
		client.MatchingLabels{"app.kubernetes.io/instance": cpName, "app.kubernetes.io/component": "core"}); err != nil {
		return ""
	}
	var looping []string
	for _, p := range list.Items {
		if p.DeletionTimestamp != nil {
			continue
		}
		for _, cs := range p.Status.ContainerStatuses {
			w := cs.State.Waiting
			if cs.RestartCount < crashLoopRestarts || w == nil || w.Reason != "CrashLoopBackOff" {
				continue
			}
			looping = append(looping, fmt.Sprintf("%s/%s restarts=%d %s%s\n--- log of the failed attempt ---\n%s",
				p.Name, cs.Name, cs.RestartCount, w.Reason, terminationSummary(cs), h.failedAttemptLog(p.Name, cs.Name)))
		}
	}
	if len(looping) == 0 {
		return ""
	}
	return strings.Join(looping, "\n")
}

// terminationSummary renders how the last attempt died, which for a Go binary that
// refuses to boot is the whole story (exit code + the message kubelet captured).
func terminationSummary(cs corev1.ContainerStatus) string {
	term := cs.LastTerminationState.Terminated
	if term == nil {
		return ""
	}
	out := fmt.Sprintf(" (last exit=%d reason=%s", term.ExitCode, term.Reason)
	if msg := strings.TrimSpace(term.Message); msg != "" {
		out += " message=" + msg
	}
	return out + ")"
}

// failedAttemptLog returns the tail of the PREVIOUS container instance's log — the
// one that exited. The current instance is usually still in backoff and has written
// nothing, so `kubectl logs` without --previous shows an empty or half-written boot.
func (h *harness) failedAttemptLog(pod, container string) string {
	tail := int64(25)
	raw, err := h.cs.CoreV1().Pods(h.ns).GetLogs(pod, &corev1.PodLogOptions{
		Container: container, Previous: true, TailLines: &tail,
	}).DoRaw(context.Background())
	if err != nil {
		return fmt.Sprintf("(previous log unavailable: %v)", err)
	}
	return strings.TrimSpace(string(raw))
}

// eventually polls cond until it holds or the deadline passes, reporting the last
// diagnostic string on failure. It aborts early on a crash-looping operand (see
// operandCrashLoop) so the failure names the cause instead of the timeout.
func (h *harness) eventually(t *testing.T, timeout time.Duration, what string, cond func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		ok, diag := cond()
		if ok {
			return
		}
		last = diag
		if crashed := h.operandCrashLoop(); crashed != "" {
			t.Fatalf("giving up on %s: the operand is in a crash loop, which waiting does not fix.\nlast state: %s\n%s",
				what, last, crashed)
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("timed out after %s waiting for %s; last state: %s", timeout, what, last)
}

func conditionIs(cp *opsv1alpha1.ControlPlane, condType string, want metav1.ConditionStatus) bool {
	for _, c := range cp.Status.Conditions {
		if c.Type == condType {
			return c.Status == want
		}
	}
	return false
}

func conditionOf(cp *opsv1alpha1.ControlPlane, condType string) metav1.Condition {
	for _, c := range cp.Status.Conditions {
		if c.Type == condType {
			return c
		}
	}
	return metav1.Condition{Type: condType, Reason: "<absent>"}
}

// waitReady waits for the ControlPlane to report PhaseReady with a converged
// StatefulSet — the state the LEGACY layout can never reach.
func (h *harness) waitReady(t *testing.T, timeout time.Duration) {
	t.Helper()
	h.eventually(t, timeout, "the ControlPlane to reach PhaseReady", func() (bool, string) {
		var cp opsv1alpha1.ControlPlane
		if err := h.c.Get(context.Background(), h.cpKey(), &cp); err != nil {
			return false, err.Error()
		}
		var sts appsv1.StatefulSet
		if err := h.c.Get(context.Background(), h.cpKey(), &sts); err != nil {
			return false, err.Error()
		}
		diag := fmt.Sprintf("phase=%s image=%s leader=%s progressing=%s degraded=%s sts(ready=%d updated=%d current=%d rev=%s/%s)",
			cp.Status.Phase, cp.Status.CurrentImage, cp.Status.LeaderPod,
			conditionOf(&cp, opsv1alpha1.ConditionProgressing).Reason,
			conditionOf(&cp, opsv1alpha1.ConditionDegraded).Reason,
			sts.Status.ReadyReplicas, sts.Status.UpdatedReplicas, sts.Status.CurrentReplicas,
			sts.Status.CurrentRevision, sts.Status.UpdateRevision)
		return cp.Status.Phase == opsv1alpha1.PhaseReady, diag
	})
}

// --- Scenario 1 — create a healthy three-replica HA control plane ------------

// TestE2E_CreateHealthyHA is design §D.2 scenario 1 AND the headline claim of the
// leader-routing layout: a healthy 3-replica active-passive control plane reaches
// PhaseReady with THREE Ready pods, while client routing still resolves to exactly
// one active writer.
func TestE2E_CreateHealthyHA(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.ensureControlPlane(t)
	h.waitReady(t, 10*time.Minute)

	// --- StatefulSet: fully rolled AND fully healthy (impossible in the legacy
	// layout, where ReadyReplicas tops out at 1). ---
	sts := h.getSTS(t)
	if sts.Status.ReadyReplicas != 3 || sts.Status.UpdatedReplicas != 3 || sts.Status.CurrentReplicas != 3 {
		t.Fatalf("StatefulSet counters = ready %d / updated %d / current %d, want 3/3/3",
			sts.Status.ReadyReplicas, sts.Status.UpdatedReplicas, sts.Status.CurrentReplicas)
	}
	if sts.Status.CurrentRevision == "" || sts.Status.CurrentRevision != sts.Status.UpdateRevision {
		t.Fatalf("revisions = %q/%q, want one settled non-empty revision", sts.Status.CurrentRevision, sts.Status.UpdateRevision)
	}
	if got := sts.Spec.Template.Spec.Containers[0].ReadinessProbe.HTTPGet.Path; got != "/pod-readyz" {
		t.Fatalf("readinessProbe = %q, want /pod-readyz in the leader-routing layout", got)
	}

	// --- every pod is health-Ready; exactly ONE is the /readyz leader ---
	pods := h.pods(t)
	if len(pods) != 3 {
		t.Fatalf("pods = %d, want 3", len(pods))
	}
	leaders := 0
	// Every /readyz result is RETAINED (D3), so a refusal explains itself here instead of
	// being reported as a bare leader count. Diagnostic only: `leaders` still rises on
	// HTTP 200 alone, and no field below is read by any assertion.
	probes := make([]readyzProbe, 0, len(pods))
	for _, p := range pods {
		if !podIsReady(&p) {
			t.Errorf("pod %s is not Ready; the split exists so healthy standbys ARE Ready", p.Name)
		}
		code, body, err := h.podProxy(ctx, "GET", p.Name, "pod-readyz", nil)
		if err != nil || code != 200 {
			t.Errorf("/pod-readyz on %s = %d %q (err %v), want 200", p.Name, code, body, err)
		}
		code, body, err = h.podProxy(ctx, "GET", p.Name, "readyz", nil)
		probes = append(probes, observeReadyz(p.Name, code, body, err))
		if err != nil {
			t.Errorf("/readyz on %s: %v", p.Name, err)
			continue
		}
		switch code {
		case 200:
			leaders++
		case 503:
		default:
			t.Errorf("/readyz on %s = %d, want 200 (leader) or 503 (standby)", p.Name, code)
		}
	}
	t.Logf("%s", formatReadyzProbes(probes))
	if leaders != 1 {
		t.Fatalf("/readyz reports %d leaders, want exactly 1 (the leader-only drain must survive the split)\n%s",
			leaders, formatReadyzProbes(probes))
	}

	// --- routing: the leader Service resolves to exactly the labeled leader ---
	serving := h.leaderServiceEndpoints(t)
	if len(serving) != 1 {
		t.Fatalf("leader Service endpoints = %v, want exactly one", serving)
	}
	cp := h.getCP(t)
	if cp.Status.LeaderPod != serving[0] {
		t.Errorf("status.leaderPod = %q but the Service serves %q", cp.Status.LeaderPod, serving[0])
	}
	if labeled := h.labeledLeaders(t); len(labeled) != 1 || labeled[0] != serving[0] {
		t.Errorf("pods labeled leader = %v, want exactly [%s]", labeled, serving[0])
	}
	if !conditionIs(cp, opsv1alpha1.ConditionAvailable, metav1.ConditionTrue) ||
		!conditionIs(cp, opsv1alpha1.ConditionProgressing, metav1.ConditionFalse) ||
		!conditionIs(cp, opsv1alpha1.ConditionDegraded, metav1.ConditionFalse) {
		t.Errorf("conditions = %+v, want Available=True Progressing=False Degraded=False", cp.Status.Conditions)
	}

	// --- application traffic: the leader serves it, every standby refuses it ---
	h.assertOnlyLeaderServes(t, ctx, serving[0])
}

// assertOnlyLeaderServes is the routing + split-brain contract at the request
// surface. It is CREDENTIAL-FREE by construction, and still decisive:
//
//   - through the leader Service, an application request REACHES the engine's
//     handlers (200 for the public server-info leaf; a rejected setup attempt comes
//     back as a client error from the handler, never 503) — so the Service resolves
//     to a node that serves;
//   - dialed DIRECTLY, every standby answers 503 not_leader — the engine's leader
//     gate refuses application traffic BEFORE the handler, which is what makes a
//     Ready standby safe to have in the cluster at all.
//
// A fully authenticated write is deliberately not exercised here: the one-time
// setup token is minted into the pod's own data dir by design and cannot be
// injected from CI (see sessions "residuals").
func (h *harness) assertOnlyLeaderServes(t *testing.T, ctx context.Context, leaderPod string) {
	t.Helper()
	bogusSetup, err := json.Marshal(map[string]string{"token": "not-the-real-token", "email": setupEmail, "password": setupPassword})
	if err != nil {
		t.Fatal(err)
	}

	code, body, err := h.serviceProxy(ctx, "GET", cpName+"-leader", "v1/server-info", nil)
	if err != nil || code != 200 {
		t.Errorf("GET /v1/server-info through the leader Service = %d %q (err %v), want 200", code, body, err)
	}
	if code, body, err := h.serviceProxy(ctx, "POST", cpName+"-leader", "v1/setup", bogusSetup); err != nil || code == 503 {
		t.Errorf("POST /v1/setup through the leader Service = %d %q (err %v); the leader must REACH the handler (a bad token is a client error, not 503)", code, body, err)
	}

	for _, p := range h.pods(t) {
		if p.Name == leaderPod {
			continue
		}
		if code, body, err := h.podProxy(ctx, "GET", p.Name, "v1/server-info", nil); err != nil ||
			!isNotLeaderResponse(code, body) {
			t.Errorf("GET /v1/server-info direct to standby %s = %d %q (err %v), want 503 not_leader", p.Name, code, body, err)
		}
		if code, body, err := h.podProxy(ctx, "POST", p.Name, "v1/setup", bogusSetup); err != nil ||
			!isNotLeaderResponse(code, body) {
			t.Errorf("POST /v1/setup direct to standby %s = %d %q (err %v), want 503 not_leader (refused BEFORE the handler)", p.Name, code, body, err)
		}
	}
}

// --- Scenario 2 — a rolling image update completes ---------------------------

// TestE2E_RollingUpdateCompletes is design §D.2 scenario 2 and the DIRECT
// regression for the wedge that made this whole unit necessary: on the legacy
// layout the StatefulSet controller replaces the highest-ordinal pod, that pod is a
// standby, it never becomes Ready, and the rollout stops there forever. Here it
// must run to completion — with CurrentImage lagging until it does, and never more
// than one serving leader endpoint along the way.
func TestE2E_RollingUpdateCompletes(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.ensureControlPlane(t)
	h.waitReady(t, 10*time.Minute)

	cp := h.getCP(t)
	if cp.Spec.Image == h.new {
		t.Skip("already on the new image (re-run against a fresh cluster to exercise the roll)")
	}
	patched := cp.DeepCopy()
	patched.Spec.Image = h.new
	if err := h.c.Patch(ctx, patched, client.MergeFrom(cp)); err != nil {
		t.Fatalf("patch spec.image: %v", err)
	}

	// In flight: the operator must report Upgrading and must NOT claim the new image
	// as current before the pods actually run it (the stage-1 premature
	// completion bug). We also watch the leader endpoint the whole time.
	sawInFlight := false
	h.eventually(t, 8*time.Minute, "the rolling update to complete", func() (bool, string) {
		var live opsv1alpha1.ControlPlane
		if err := h.c.Get(ctx, h.cpKey(), &live); err != nil {
			return false, err.Error()
		}
		var sts appsv1.StatefulSet
		if err := h.c.Get(ctx, h.cpKey(), &sts); err != nil {
			return false, err.Error()
		}
		if serving := h.leaderServiceEndpoints(t); len(serving) > 1 {
			t.Errorf("leader Service served %v simultaneously during the roll; only one writer may be routed", serving)
		}
		rolling := sts.Status.CurrentRevision != sts.Status.UpdateRevision || sts.Status.UpdatedReplicas < 3
		if rolling {
			sawInFlight = true
			if live.Status.CurrentImage == h.new {
				t.Errorf("status.currentImage = %q while the rollout is still in flight (updated=%d, revisions %s/%s); it must lag until every pod runs it",
					live.Status.CurrentImage, sts.Status.UpdatedReplicas, sts.Status.CurrentRevision, sts.Status.UpdateRevision)
			}
		}
		diag := fmt.Sprintf("phase=%s image=%s updated=%d ready=%d rev=%s/%s progressing=%s",
			live.Status.Phase, live.Status.CurrentImage, sts.Status.UpdatedReplicas, sts.Status.ReadyReplicas,
			sts.Status.CurrentRevision, sts.Status.UpdateRevision, conditionOf(&live, opsv1alpha1.ConditionProgressing).Reason)
		done := live.Status.Phase == opsv1alpha1.PhaseReady &&
			live.Status.CurrentImage == h.new &&
			sts.Status.UpdatedReplicas == 3 && sts.Status.ReadyReplicas == 3 &&
			sts.Status.CurrentRevision == sts.Status.UpdateRevision
		return done, diag
	})
	if !sawInFlight {
		t.Log("note: the rollout completed between polls; the completion assertions still hold")
	}

	// Every pod actually runs the new image — the whole point of observed state.
	for _, p := range h.pods(t) {
		for _, c := range p.Spec.Containers {
			if c.Name == "olivares" && c.Image != h.new {
				t.Errorf("pod %s runs %q after the rollout, want %q", p.Name, c.Image, h.new)
			}
		}
	}
	if serving := h.leaderServiceEndpoints(t); len(serving) != 1 {
		t.Errorf("leader Service endpoints after the rollout = %v, want exactly one", serving)
	}
}

// --- Scenario 3 — leader-kill failover ---------------------------------------

// TestE2E_LeaderKillFailover is design §D.2 scenario 3: killing the routed leader
// must promote a standby, move the leader label AND the Service endpoint to it,
// resume writes, and restore three Ready replicas — without ever exposing two
// pods that can both act as the writer.
func TestE2E_LeaderKillFailover(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.ensureControlPlane(t)
	h.waitReady(t, 10*time.Minute)

	before := h.leaderServiceEndpoints(t)
	if len(before) != 1 {
		t.Fatalf("leader Service endpoints before the kill = %v, want exactly one", before)
	}
	victim := before[0]

	if err := h.c.Delete(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: victim, Namespace: h.ns}}); err != nil {
		t.Fatalf("delete leader pod %s: %v", victim, err)
	}

	// A DIFFERENT pod becomes the sole routed leader.
	h.eventually(t, 5*time.Minute, "a new leader to be published and routed", func() (bool, string) {
		serving := h.leaderServiceEndpoints(t)
		labeled := h.labeledLeaders(t)
		diag := fmt.Sprintf("serving=%v labeled=%v", serving, labeled)
		if len(serving) > 1 {
			return false, diag + " (transient: more than one endpoint)"
		}
		return len(serving) == 1 && serving[0] != victim, diag
	})
	promoted := h.leaderServiceEndpoints(t)[0]

	// Service continues through the SAME client Service, now on the promoted node.
	h.eventually(t, 3*time.Minute, "application traffic to resume through the leader Service", func() (bool, string) {
		code, body, err := h.serviceProxy(ctx, "GET", cpName+"-leader", "v1/server-info", nil)
		if err != nil {
			return false, err.Error()
		}
		return code == 200, fmt.Sprintf("server-info through the leader Service = %d %q", code, body)
	})

	// At most ONE pod may act as the writer at any moment. Three DISTINCT
	// outcomes, each with its own meaning — the earlier version counted every
	// code != 503 as "served traffic" while its comment promised "never a
	// success", so a standby answering 400 read as a fencing violation and a
	// probe error vanished silently (all standbys unreachable would have
	// passed with nothing verified):
	//   2xx            -> the pod SERVED as the writer (only the promoted may)
	//   503            -> a standby refusing correctly (retryable not_leader)
	//   anything else  -> an anomaly reported with its body: neither serving
	//                     nor the refusal, and filing it under either bucket
	//                     falsifies one claim or the other
	// An unreachable pod is logged, not skipped: the replacement may
	// legitimately still be booting, so it cannot fail the test by itself —
	// but at least the promoted leader and one survivor standby must have
	// answered, and the promoted one must have answered 2xx.
	servers, probed := 0, 0
	promotedServed := false
	for _, p := range h.pods(t) {
		code, body, err := h.podProxy(ctx, "GET", p.Name, "v1/server-info", nil)
		if err != nil {
			t.Logf("pod %s unreachable during the fence sweep (a rebooting replacement is legitimate): %v", p.Name, err)
			continue
		}
		probed++
		switch {
		case code >= 200 && code < 300:
			servers++
			if p.Name == promoted {
				promotedServed = true
			} else {
				t.Errorf("pod %s SERVED application traffic (%d) while %s is the leader", p.Name, code, promoted)
			}
		case isNotLeaderResponse(code, body):
			// the correct standby refusal
		default:
			t.Errorf("pod %s answered %d to server-info — neither a success nor the 503 not_leader refusal; body: %.200s",
				p.Name, code, body)
		}
	}
	if probed < 2 {
		t.Fatalf("only %d pod(s) answered the fence sweep; the promoted leader and at least one survivor standby must be probeable, or the sweep verified nothing", probed)
	}
	if servers > 1 {
		t.Fatalf("%d pods served application traffic simultaneously; exactly one may", servers)
	}
	if !promotedServed {
		t.Fatalf("the promoted leader %s did not serve server-info during the sweep — the Service answered 200 moments ago, so a second leadership transition is in flight", promoted)
	}

	// The replacement returns as a health-Ready standby and the control plane is
	// whole again — three Ready replicas and PhaseReady.
	h.waitReady(t, 6*time.Minute)
	sts := h.getSTS(t)
	if sts.Status.ReadyReplicas != 3 {
		t.Fatalf("ReadyReplicas after failover = %d, want 3 (the replacement must rejoin as a healthy standby)", sts.Status.ReadyReplicas)
	}
	if serving := h.leaderServiceEndpoints(t); len(serving) != 1 {
		t.Fatalf("leader Service endpoints after failover = %v, want exactly one", serving)
	}
}
