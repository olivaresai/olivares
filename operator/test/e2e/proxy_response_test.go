// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	restclient "k8s.io/client-go/rest"
)

// observedCIPodProxyStatus503Prefix is the first 200 bytes of the Status body
// printed by TestE2E_LeaderKillFailover (`%.200s`) on GitHub run 34086627367
// job 101631800422. The log ended at `"cod`.
const observedCIPodProxyStatus503Prefix = `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"error trying to reach service: dial tcp 10.244.0.15:8443: connect: connection refused","reason":"ServiceUnavailable","cod`

// reconstructedCIPodProxyStatus503 completes that truncated CI body. The
// remainder `e":503}` is labeled reconstructed: it is not in the 200-character
// log. It matches k8s.io/apimachinery@v0.36.2 errors.NewServiceUnavailable
// (Code=503, Reason=ServiceUnavailable, Status=Failure) plus TypeMeta
// kind=Status apiVersion=v1, which encoding/json emits including metadata:{}.
const reconstructedCIPodProxyStatus503 = `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"error trying to reach service: dial tcp 10.244.0.15:8443: connect: connection refused","reason":"ServiceUnavailable","code":503}`

func TestTransientPodProxyErrorDistinguishesAPIServerFromEngine(t *testing.T) {
	t.Parallel()
	requestErr := errors.New("request returned a non-success status")
	tests := []struct {
		name string
		code int
		body string
		want bool
	}{
		{
			name: "pod_without_an_address_is_transient",
			code: http.StatusBadRequest,
			body: `{"kind":"Status","apiVersion":"v1","status":"Failure",` +
				`"message":"address not allowed","reason":"BadRequest","code":400}`,
			want: true,
		},
		{
			name: "original_ci_pod_proxy_503_is_transient",
			code: http.StatusServiceUnavailable,
			body: reconstructedCIPodProxyStatus503,
			want: true,
		},
		{
			name: "engine_not_leader_remains_an_upstream_response",
			code: http.StatusServiceUnavailable,
			body: `{"error":{"code":"not_leader","message":"retry against the leader"}}`,
		},
		{
			name: "other_engine_503_remains_an_upstream_response",
			code: http.StatusServiceUnavailable,
			body: `{"error":{"code":"temporarily_unavailable","message":"retry later"}}`,
		},
		{
			name: "arbitrary_engine_bad_request_remains_an_anomaly",
			code: http.StatusBadRequest,
			body: `{"error":{"code":"invalid_request","message":"bad request"}}`,
		},
		{
			name: "generic_kubernetes_503_is_not_silently_retryable",
			code: http.StatusServiceUnavailable,
			body: `{"kind":"Status","apiVersion":"v1","status":"Failure",` +
				`"message":"service unavailable","reason":"ServiceUnavailable","code":503}`,
		},
		{
			name: "other_kubernetes_failures_are_not_silently_retryable",
			code: http.StatusForbidden,
			body: `{"kind":"Status","apiVersion":"v1","status":"Failure",` +
				`"message":"pods is forbidden","reason":"Forbidden","code":403}`,
		},
		{
			name: "other_kubernetes_bad_requests_are_not_silently_retryable",
			code: http.StatusBadRequest,
			body: `{"kind":"Status","apiVersion":"v1","status":"Failure",` +
				`"message":"invalid proxy path","reason":"BadRequest","code":400}`,
		},
		{
			name: "status_body_code_must_match",
			code: http.StatusBadRequest,
			body: `{"kind":"Status","apiVersion":"v1","status":"Failure",` +
				`"message":"address not allowed","reason":"BadRequest","code":503}`,
		},
		{
			name: "http_code_must_match",
			code: http.StatusServiceUnavailable,
			body: `{"kind":"Status","apiVersion":"v1","status":"Failure",` +
				`"message":"address not allowed","reason":"BadRequest","code":400}`,
		},
		{
			name: "wrong_http_code_for_reach_service_503",
			code: http.StatusBadRequest,
			body: reconstructedCIPodProxyStatus503,
		},
		{
			name: "wrong_status_code_for_reach_service_503",
			code: http.StatusServiceUnavailable,
			body: `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure",` +
				`"message":"error trying to reach service: dial tcp 10.244.0.15:8443: connect: connection refused",` +
				`"reason":"ServiceUnavailable","code":400}`,
		},
		{
			name: "wrong_reason_for_reach_service_503",
			code: http.StatusServiceUnavailable,
			body: `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure",` +
				`"message":"error trying to reach service: dial tcp 10.244.0.15:8443: connect: connection refused",` +
				`"reason":"InternalError","code":503}`,
		},
		{
			name: "wrong_status_field_for_reach_service_503",
			code: http.StatusServiceUnavailable,
			body: `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Success",` +
				`"message":"error trying to reach service: dial tcp 10.244.0.15:8443: connect: connection refused",` +
				`"reason":"ServiceUnavailable","code":503}`,
		},
		{
			name: "wrong_wrapper_kind_for_reach_service_503",
			code: http.StatusServiceUnavailable,
			body: `{"kind":"Pod","apiVersion":"v1","metadata":{},"status":"Failure",` +
				`"message":"error trying to reach service: dial tcp 10.244.0.15:8443: connect: connection refused",` +
				`"reason":"ServiceUnavailable","code":503}`,
		},
		{
			name: "wrong_wrapper_api_version_for_reach_service_503",
			code: http.StatusServiceUnavailable,
			body: `{"kind":"Status","apiVersion":"v2","metadata":{},"status":"Failure",` +
				`"message":"error trying to reach service: dial tcp 10.244.0.15:8443: connect: connection refused",` +
				`"reason":"ServiceUnavailable","code":503}`,
		},
		{
			name: "malformed_json_is_not_transport",
			code: http.StatusServiceUnavailable,
			body: `{`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := transientPodProxyError(tc.code, []byte(tc.body), requestErr)
			if tc.want {
				if !errors.Is(err, errPodProxyTargetNotAddressable) {
					t.Fatalf("transient target error = false, want true (err %v)", err)
				}
			} else if err != nil {
				t.Fatalf("non-target response returned error: %v", err)
			}
		})
	}
}

func TestTransientPodProxyErrorRequiresARequestFailure(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		code int
		body string
	}{
		{
			name: "address_not_allowed_400",
			code: http.StatusBadRequest,
			body: `{"kind":"Status","apiVersion":"v1","status":"Failure",` +
				`"message":"address not allowed","reason":"BadRequest","code":400}`,
		},
		{
			name: "original_ci_pod_proxy_503",
			code: http.StatusServiceUnavailable,
			body: reconstructedCIPodProxyStatus503,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := transientPodProxyError(tc.code, []byte(tc.body), nil); err != nil {
				t.Fatalf("successful request classified as transient: %v", err)
			}
		})
	}
}

func TestNotLeaderResponseDistinguishesEngineFromAPIServer(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		code int
		body string
		want bool
	}{
		{
			name: "engine_not_leader",
			code: http.StatusServiceUnavailable,
			body: `{"error":{"code":"not_leader","message":"retry against the leader"}}`,
			want: true,
		},
		{
			name: "kubernetes_service_unavailable",
			code: http.StatusServiceUnavailable,
			body: `{"kind":"Status","apiVersion":"v1","status":"Failure",` +
				`"message":"service unavailable","reason":"ServiceUnavailable","code":503}`,
		},
		{
			name: "original_ci_pod_proxy_503_is_not_leader",
			code: http.StatusServiceUnavailable,
			body: reconstructedCIPodProxyStatus503,
		},
		{
			name: "engine_service_unavailable_with_another_code",
			code: http.StatusServiceUnavailable,
			body: `{"error":{"code":"temporarily_unavailable","message":"retry later"}}`,
		},
		{
			name: "not_leader_with_the_wrong_http_status",
			code: http.StatusBadRequest,
			body: `{"error":{"code":"not_leader","message":"retry against the leader"}}`,
		},
		{
			name: "malformed_body",
			code: http.StatusServiceUnavailable,
			body: `{`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isNotLeaderResponse(tc.code, tc.body); got != tc.want {
				t.Fatalf("isNotLeaderResponse() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestReconstructedCIStatus503MatchesPinnedEncoder(t *testing.T) {
	t.Parallel()
	if len(observedCIPodProxyStatus503Prefix) != 200 {
		t.Fatalf("CI truncation prefix length = %d, want 200", len(observedCIPodProxyStatus503Prefix))
	}
	if !strings.HasPrefix(reconstructedCIPodProxyStatus503, observedCIPodProxyStatus503Prefix) {
		t.Fatalf("reconstructed body does not start with the 200-byte CI truncation")
	}
	st := apierrors.NewServiceUnavailable(
		"error trying to reach service: dial tcp 10.244.0.15:8443: connect: connection refused",
	).Status()
	if st.Status != metav1.StatusFailure ||
		st.Reason != metav1.StatusReasonServiceUnavailable ||
		st.Code != http.StatusServiceUnavailable {
		t.Fatalf("NewServiceUnavailable fields = status=%q reason=%q code=%d", st.Status, st.Reason, st.Code)
	}
	st.Kind = "Status"
	st.APIVersion = "v1"
	encoded, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("marshal pinned Status: %v", err)
	}
	if string(encoded) != reconstructedCIPodProxyStatus503 {
		t.Fatalf("pinned encoding/json = %s\nreconstructed fixture = %s", encoded, reconstructedCIPodProxyStatus503)
	}
}

func TestDoProxyPropagatesKubernetesStatus503Transport(t *testing.T) {
	t.Parallel()
	cs, path := localProxyClientset(t, http.StatusServiceUnavailable, reconstructedCIPodProxyStatus503)

	req := localPodProxyRequest(cs)
	var code int
	raw, reqErr := req.Do(context.Background()).StatusCode(&code).Raw()
	if reqErr == nil {
		t.Fatalf("client-go request error = nil, want non-nil for HTTP 503")
	}
	if code != http.StatusServiceUnavailable {
		t.Fatalf("status code = %d, want 503", code)
	}
	if string(raw) != reconstructedCIPodProxyStatus503 {
		t.Fatalf("raw body = %q, want reconstructed CI Status JSON", raw)
	}
	if err := transientPodProxyError(code, raw, reqErr); !errors.Is(err, errPodProxyTargetNotAddressable) {
		t.Fatalf("classifier from client-go plumbing = %v, want transport", err)
	}

	got, body, err := doProxy(context.Background(), localPodProxyRequest(cs))
	if !errors.Is(err, errPodProxyTargetNotAddressable) {
		t.Fatalf("doProxy err = %v, want transport", err)
	}
	if got != 0 {
		t.Fatalf("doProxy code = %d, want 0 when transport is classified", got)
	}
	if body != reconstructedCIPodProxyStatus503 {
		t.Fatalf("doProxy body = %q, want reconstructed CI Status JSON", body)
	}
	if !strings.Contains(path(), "/api/v1/namespaces/olivares-e2e/pods/") ||
		!strings.Contains(path(), "/proxy/") {
		t.Fatalf("client-go request path = %q, want a pods proxy URL", path())
	}
}

func TestDoProxyPreservesEngineAndGeneric503(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		body       string
		wantLeader bool
	}{
		{
			name:       "engine_not_leader",
			body:       `{"error":{"code":"not_leader","message":"retry against the leader"}}`,
			wantLeader: true,
		},
		{
			name: "other_engine_503",
			body: `{"error":{"code":"temporarily_unavailable","message":"retry later"}}`,
		},
		{
			name: "generic_kubernetes_503",
			body: `{"kind":"Status","apiVersion":"v1","status":"Failure",` +
				`"message":"service unavailable","reason":"ServiceUnavailable","code":503}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cs, _ := localProxyClientset(t, http.StatusServiceUnavailable, tc.body)
			code, body, err := doProxy(context.Background(), localPodProxyRequest(cs))
			if err != nil {
				t.Fatalf("doProxy classified a non-transport 503 as unreachable: %v", err)
			}
			if code != http.StatusServiceUnavailable {
				t.Fatalf("doProxy code = %d, want 503", code)
			}
			if body != tc.body {
				t.Fatalf("doProxy body = %q, want %q", body, tc.body)
			}
			if got := isNotLeaderResponse(code, body); got != tc.wantLeader {
				t.Fatalf("isNotLeaderResponse() = %t, want %t", got, tc.wantLeader)
			}
		})
	}
}

func TestDoProxyPreservesAddressNotAllowedTransport(t *testing.T) {
	t.Parallel()
	body := `{"kind":"Status","apiVersion":"v1","status":"Failure",` +
		`"message":"address not allowed","reason":"BadRequest","code":400}`
	cs, _ := localProxyClientset(t, http.StatusBadRequest, body)
	code, got, err := doProxy(context.Background(), localPodProxyRequest(cs))
	if !errors.Is(err, errPodProxyTargetNotAddressable) {
		t.Fatalf("doProxy err = %v, want address-not-allowed transport", err)
	}
	if code != 0 {
		t.Fatalf("doProxy code = %d, want 0 when transport is classified", code)
	}
	if got != body {
		t.Fatalf("doProxy body = %q, want %q", got, body)
	}
}

func localProxyClientset(t *testing.T, statusCode int, body string) (*kubernetes.Clientset, func() string) {
	t.Helper()
	var lastPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	cs, err := kubernetes.NewForConfig(&restclient.Config{Host: srv.URL, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("clientset: %v", err)
	}
	return cs, func() string { return lastPath }
}

func localPodProxyRequest(cs *kubernetes.Clientset) *restclient.Request {
	return cs.CoreV1().RESTClient().Get().
		Namespace("olivares-e2e").
		Resource("pods").
		Name("https:e2e-2:" + httpsPortName).
		SubResource("proxy").
		Suffix("v1/server-info")
}

// --- the /readyz probe record (D3) -------------------------------------------

// TestReadyzProbeNamesTheRefusalWithoutQuotingTheBody is the D3 subject: the
// first-create assertion used to discard the /readyz body, so a deterministic
// configuration refusal and an election flake produced the identical message
// ("0 leaders"). Each case is one real engine answer, and the assertion is on the
// line an operator reads in CI.
func TestReadyzProbeNamesTheRefusalWithoutQuotingTheBody(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		body string
		want string
	}{
		{
			// The refusal that made run 34605533735 report 0 leaders. The `remedy`
			// sentence is in the body and must NOT be echoed.
			name: "leader whose first-boot setup read is refused",
			code: http.StatusServiceUnavailable,
			body: `{"status":"setup_blocked","store":"up","leader":true,"setup_required":true,` +
				`"code":"cross_tenant_admin_pool_not_configured","remedy":"provision a NOSUPERUSER BYPASSRLS role and pass --admin-dsn"}`,
			want: "e2e-0: HTTP 503 status=setup_blocked code=cross_tenant_admin_pool_not_configured",
		},
		{
			name: "healthy standby draining writer routing",
			code: http.StatusServiceUnavailable,
			body: `{"status":"standby","store":"up","leader":false}`,
			want: "e2e-1: HTTP 503 status=standby code=<absent>",
		},
		{
			name: "leader that is ready before setup",
			code: http.StatusOK,
			body: `{"status":"ok","store":"up","leader":true,"setup_required":true}`,
			want: "e2e-2: HTTP 200 status=ok code=<absent>",
		},
		{
			name: "store down",
			code: http.StatusServiceUnavailable,
			body: `{"status":"unavailable","store":"down"}`,
			want: "e2e-0: HTTP 503 status=unavailable code=<absent>",
		},
		{
			name: "setup state that could not be observed",
			code: http.StatusServiceUnavailable,
			body: `{"status":"setup_unavailable","store":"up","leader":true,"code":"setup_state_unavailable"}`,
			want: "e2e-1: HTTP 503 status=setup_unavailable code=setup_state_unavailable",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pod := tc.want[:strings.Index(tc.want, ":")]
			got := observeReadyz(pod, tc.code, tc.body, nil).String()
			if got != tc.want {
				t.Errorf("observeReadyz(...).String() = %q, want %q", got, tc.want)
			}
			// Nothing outside the two named scalars may reach the line.
			for _, leaked := range []string{"remedy", "provision", "--admin-dsn", "setup_required"} {
				if strings.Contains(tc.body, leaked) && strings.Contains(got, leaked) &&
					!strings.Contains(tc.want, leaked) {
					t.Errorf("the diagnostic line leaked %q from the body: %q", leaked, got)
				}
			}
		})
	}
}

// TestReadyzProbeIdentifiesAnUnexpectedPayloadWithoutCopyingIt covers the answers the
// schema does not define. Each must be IDENTIFIED — class and length — and none may be
// quoted, because an unexpected payload is exactly the one whose contents are unknown.
func TestReadyzProbeIdentifiesAnUnexpectedPayloadWithoutCopyingIt(t *testing.T) {
	secret := "Bearer eyJ-not-a-real-token-but-it-must-not-be-logged"
	for _, tc := range []struct {
		name   string
		body   string
		want   string
		banned string
	}{
		{
			name: "empty body",
			body: "",
			want: "e2e-0: HTTP 503, empty body",
		},
		{
			name:   "an HTML error page from something in front of the engine",
			body:   "<html><body>" + secret + "</body></html>",
			want:   "e2e-0: HTTP 503, %d-byte body is not JSON (not quoted)",
			banned: secret,
		},
		{
			name:   "JSON that is not this schema",
			body:   `{"status":{"nested":"` + secret + `"}}`,
			want:   "e2e-0: HTTP 503, %d-byte JSON body is not the health-response schema (not quoted)",
			banned: secret,
		},
		{
			name:   "a JSON array",
			body:   `["` + secret + `"]`,
			want:   "e2e-0: HTTP 503, %d-byte JSON body is not the health-response schema (not quoted)",
			banned: secret,
		},
		{
			name: "whitespace only",
			body: "   \n\t ",
			want: "e2e-0: HTTP 503, empty body",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := tc.want
			if strings.Contains(want, "%d") {
				want = fmt.Sprintf(want, len(tc.body))
			}
			got := observeReadyz("e2e-0", http.StatusServiceUnavailable, tc.body, nil).String()
			if got != want {
				t.Errorf("observeReadyz(...).String() = %q, want %q", got, want)
			}
			if tc.banned != "" && strings.Contains(got, tc.banned) {
				t.Errorf("the diagnostic line copied the unexpected payload: %q", got)
			}
		})
	}
}

// TestReadyzProbeBoundsAServerControlledScalar keeps a schema-named field from becoming
// an unbounded write into a CI log. Length and control characters are no longer the
// test: an allowlist is, so an oversized or non-printable value is simply one more
// value the engine cannot emit.
func TestReadyzProbeBoundsAServerControlledScalar(t *testing.T) {
	long := strings.Repeat("a", 65)
	got := observeReadyz("e2e-0", http.StatusServiceUnavailable,
		`{"status":"`+long+`","code":"ok\tbad"}`, nil).String()
	want := "e2e-0: HTTP 503 status=<unrecognized 65-byte value> code=<unrecognized 6-byte value>"
	if got != want {
		t.Errorf("observeReadyz(...).String() = %q, want %q", got, want)
	}
	if strings.Contains(got, long) {
		t.Errorf("the oversized scalar was copied verbatim: %q", got)
	}
	// The longest code the engine actually emits must still print in full — the
	// negative control, because a guard that suppressed everything would also pass.
	atLimit := observeReadyz("e2e-0", http.StatusServiceUnavailable,
		`{"status":"setup_blocked","code":"cross_tenant_admin_pool_not_configured"}`, nil).String()
	if !strings.Contains(atLimit, "cross_tenant_admin_pool_not_configured") {
		t.Errorf("a real engine code was suppressed: %q", atLimit)
	}
}

// TestReadyzProbeKeepsTransportFailureDistinctFromRefusal is the distinction the
// contract names: the apiserver proxy failing to reach a pod says NOTHING about
// readiness, and must never render like an engine answer.
func TestReadyzProbeKeepsTransportFailureDistinctFromRefusal(t *testing.T) {
	transport := observeReadyz("e2e-2", 0, `{"kind":"Status"}`, errPodProxyTargetNotAddressable)
	if !transport.transport {
		t.Fatal("a probe with a request error must be recorded as a transport failure")
	}
	got := transport.String()
	if !strings.Contains(got, "never reached the engine") {
		t.Errorf("transport line = %q, want it to say the engine was never reached", got)
	}
	for _, refusalShape := range []string{"HTTP ", "status=", "code="} {
		if strings.Contains(got, refusalShape) {
			t.Errorf("transport line %q reads like an engine refusal (%q); the two must stay distinct", got, refusalShape)
		}
	}
	// A 503 the ENGINE chose is the other side of that distinction.
	refusal := observeReadyz("e2e-2", http.StatusServiceUnavailable, `{"status":"standby"}`, nil)
	if refusal.transport {
		t.Error("an HTTP refusal must not be recorded as a transport failure")
	}
}

// TestFormatReadyzProbesReportsEveryPod proves the failure message carries one line per
// pod — the whole point of retaining the results rather than a single count.
func TestFormatReadyzProbesReportsEveryPod(t *testing.T) {
	out := formatReadyzProbes([]readyzProbe{
		observeReadyz("e2e-0", http.StatusServiceUnavailable,
			`{"status":"setup_blocked","code":"cross_tenant_admin_pool_not_configured"}`, nil),
		observeReadyz("e2e-1", http.StatusServiceUnavailable, `{"status":"standby"}`, nil),
		observeReadyz("e2e-2", 0, "", errPodProxyTargetNotAddressable),
	})
	for _, want := range []string{
		"e2e-0: HTTP 503 status=setup_blocked code=cross_tenant_admin_pool_not_configured",
		"e2e-1: HTTP 503 status=standby code=<absent>",
		"e2e-2: transport failure",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("formatReadyzProbes() = %q, want it to contain %q", out, want)
		}
	}
	if lines := strings.Count(out, "\n"); lines != 3 {
		t.Errorf("formatReadyzProbes() produced %d newlines, want 3 (a header and one line per pod)", lines)
	}
	if empty := formatReadyzProbes(nil); !strings.Contains(empty, "none was recorded") {
		t.Errorf("an empty probe set must say so: %q", empty)
	}
}

// --- correction 1: only the values the engine actually emits ---------------

// TestReadyzScalarNeverCopiesAnUnrecognizedValue is the first gap Root found: the
// scalar renderer bounded LENGTH and refused control characters, but copied any short
// printable string verbatim. A field named by the schema is still server-controlled,
// so an unexpected payload could put arbitrary text into the diagnostic line — which
// is the one thing the bounded schema promised it would not do.
func TestReadyzScalarNeverCopiesAnUnrecognizedValue(t *testing.T) {
	// Self-describing decoys. Short, printable, and exactly what the old renderer
	// would have copied straight into a CI log.
	const statusDecoy = "tok-DECOY-not-a-real-credential"
	const codeDecoy = "pw-DECOY-also-not-a-real-credential"

	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{
			name: "an unrecognized status is reported by length, not copied",
			body: `{"status":"` + statusDecoy + `"}`,
			want: "e2e-0: HTTP 503 status=<unrecognized 31-byte value> code=<absent>",
		},
		{
			name: "an unrecognized code is reported by length, not copied",
			body: `{"status":"setup_blocked","code":"` + codeDecoy + `"}`,
			want: "e2e-0: HTTP 503 status=setup_blocked code=<unrecognized 35-byte value>",
		},
		{
			name: "both unrecognized at once",
			body: `{"status":"` + statusDecoy + `","code":"` + codeDecoy + `"}`,
			want: "e2e-0: HTTP 503 status=<unrecognized 31-byte value> code=<unrecognized 35-byte value>",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := observeReadyz("e2e-0", http.StatusServiceUnavailable, tc.body, nil).String()
			if got != tc.want {
				t.Errorf("observeReadyz(...).String() = %q, want %q", got, tc.want)
			}
			for _, decoy := range []string{statusDecoy, codeDecoy} {
				if strings.Contains(got, decoy) {
					t.Errorf("the diagnostic line copied an unrecognized scalar verbatim: %q", got)
				}
			}
		})
	}
}

// TestReadyzScalarStillPrintsEveryValueTheEngineEmits is the negative control for the
// allowlist above: a guard that suppressed everything would pass that test and destroy
// the diagnostic. Every value core/api/metrics.go can emit must still print in full.
func TestReadyzScalarStillPrintsEveryValueTheEngineEmits(t *testing.T) {
	// handleReadyz and handlePodReadyz, verified at this commit.
	for _, status := range []string{"ok", "unavailable", "standby", "setup_unavailable", "setup_blocked"} {
		got := observeReadyz("e2e-0", http.StatusServiceUnavailable, `{"status":"`+status+`"}`, nil).String()
		want := "e2e-0: HTTP 503 status=" + status + " code=<absent>"
		if got != want {
			t.Errorf("status %q rendered as %q, want %q", status, got, want)
		}
	}
	for _, code := range []string{"cross_tenant_admin_pool_not_configured", "setup_probe_unavailable", "setup_state_unavailable"} {
		got := observeReadyz("e2e-0", http.StatusServiceUnavailable, `{"status":"setup_unavailable","code":"`+code+`"}`, nil).String()
		want := "e2e-0: HTTP 503 status=setup_unavailable code=" + code
		if got != want {
			t.Errorf("code %q rendered as %q, want %q", code, got, want)
		}
	}
}

// TestReadyzProbeIdentifiesAMissingStatus is the second gap: `null`, `{}` and a null
// status all decode into a zero value WITHOUT an error, so they used to render as
// "status=<absent>" — indistinguishable from a legitimate answer that merely lacks the
// optional code. status is REQUIRED by the schema; without it the body is not a health
// response, whatever else it holds.
func TestReadyzProbeIdentifiesAMissingStatus(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "a bare JSON null", body: `null`},
		{name: "an empty object", body: `{}`},
		{name: "an explicit null status", body: `{"status":null}`},
		{name: "only the optional code", body: `{"code":"setup_probe_unavailable"}`},
		{name: "an unrelated object that happens to be JSON", body: `{"kind":"Status","reason":"NotFound"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := observeReadyz("e2e-0", http.StatusServiceUnavailable, tc.body, nil).String()
			want := fmt.Sprintf("e2e-0: HTTP 503, %d-byte JSON body carries no health status (not quoted)", len(tc.body))
			if got != want {
				t.Errorf("observeReadyz(...).String() = %q, want %q", got, want)
			}
			// The HTTP status stays visible and this is never read as transport.
			if !strings.Contains(got, "HTTP 503") {
				t.Errorf("the HTTP status was lost: %q", got)
			}
			if observeReadyz("e2e-0", http.StatusServiceUnavailable, tc.body, nil).transport {
				t.Error("a malformed body must not be recorded as a transport failure")
			}
		})
	}
}

// TestReadyzProbeAcceptsAnAbsentOptionalCode keeps the required/optional distinction
// honest in the other direction: a valid status with no code is the engine's ordinary
// answer on four of its six paths and must stay a normal, fully-rendered line.
func TestReadyzProbeAcceptsAnAbsentOptionalCode(t *testing.T) {
	for _, body := range []string{
		`{"status":"ok","store":"up","leader":true,"setup_required":false}`,
		`{"status":"standby","store":"up","leader":false}`,
		`{"status":"unavailable","store":"down"}`,
		`{"status":"ok","code":null}`,
	} {
		got := observeReadyz("e2e-1", http.StatusOK, body, nil).String()
		if !strings.Contains(got, "code=<absent>") {
			t.Errorf("an absent optional code must render as <absent>, got %q", got)
		}
		if strings.Contains(got, "carries no health status") || strings.Contains(got, "unrecognized") {
			t.Errorf("a legitimate answer was reported as unexpected: %q", got)
		}
	}
}
