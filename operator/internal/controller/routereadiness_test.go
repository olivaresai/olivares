// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package controller

import (
	"bytes"
	"context"
	"encoding/pem"
	"io"
	stdlog "log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

// The engine's real readiness documents (core/api/metrics.go handleReadyz). They
// are quoted verbatim so this table fails if the engine's schema moves under us —
// which is the only way a classifier over a foreign schema can stay honest.
const (
	engineOKComplete    = `{"status":"ok","store":"up","leader":true,"setup_required":false}`
	engineOKFirstBoot   = `{"status":"ok","store":"up","leader":true,"setup_required":true}`
	engineStandby       = `{"status":"standby","store":"up","leader":false}`
	engineStoreDown     = `{"status":"unavailable","store":"down"}`
	engineSetupState    = `{"status":"setup_unavailable","store":"up","leader":true,"code":"setup_state_unavailable"}`
	engineSetupProbe    = `{"status":"setup_unavailable","store":"up","leader":true,"setup_required":true,"code":"setup_probe_unavailable"}`
	engineSetupBlockedB = `{"status":"setup_blocked","store":"up","leader":true,"setup_required":true,` +
		`"code":"cross_tenant_admin_pool_not_configured","remedy":"configure the cross-tenant administrative pool"}`
	// The API server's OWN failure envelope for an unreachable pod: a 503 that says
	// nothing whatsoever about the engine.
	apiserverUnreachable = `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure",` +
		`"message":"error trying to reach service: dial tcp 10.1.2.3:8443: connect: connection refused",` +
		`"reason":"ServiceUnavailable","code":503}`
)

// TestClassifyRouteResponse is the closed vocabulary. Everything the engine
// actually answers is recognized; everything else is UNVERIFIED, because a
// classification this operator cannot defend is worth less than admitting it did
// not learn anything.
func TestClassifyRouteResponse(t *testing.T) {
	tests := []struct {
		name string
		code int
		body string
		want RouteReadiness
	}{
		{"200 ok on a configured install", 200, engineOKComplete, RouteReady},
		{
			// The engine can serve the setup ceremony: that IS traffic readiness for
			// this request. Demanding a completed setup here would make a first
			// create unable to reach Ready until a human ran the ceremony through a
			// route the operator had already declared not ready.
			"200 ok while setup is still required", 200, engineOKFirstBoot, RouteReady,
		},
		{"503 setup_blocked with the admin-pool code", 503, engineSetupBlockedB, RouteSetupBlocked},
		{"503 standby: the label lags the election", 503, engineStandby, RouteNotReady},
		{"503 store down under a Ready pod", 503, engineStoreDown, RouteNotReady},
		{"503 setup state unobservable", 503, engineSetupState, RouteNotReady},
		{"503 setup capability probe failed", 503, engineSetupProbe, RouteNotReady},
		{"403: the OPERATOR is unauthorized, the engine said nothing", 403, "", RouteForbidden},
		{
			// The hazard this whole guard exists for: a Kubernetes 503 is a transport
			// failure. Reading it as an engine verdict would report "the control
			// plane refuses traffic" every time a pod was briefly unroutable.
			"apiserver 503 is transport, not an engine answer", 503, apiserverUnreachable, RouteUnknown,
		},
		{
			// A decoy wearing the API server's envelope while carrying the engine's
			// most consequential words. It must not become the static SetupBlocked
			// verdict that stops the progress clock.
			"decoy: Status envelope carrying engine vocabulary", 503,
			`{"kind":"Status","apiVersion":"v1","status":"setup_blocked","code":"cross_tenant_admin_pool_not_configured"}`,
			RouteUnknown,
		},
		{"decoy: a 200 whose body is an apiserver Status", 200, `{"kind":"Status","apiVersion":"v1","status":"ok"}`, RouteUnknown},
		{"malformed body", 200, `{"status":`, RouteUnknown},
		{"empty 200 body", 200, "", RouteUnknown},
		{"200 with an unrecognized status", 200, `{"status":"degraded"}`, RouteUnknown},
		{"200 carrying a code the ok schema never has", 200, `{"status":"ok","code":"whatever"}`, RouteUnknown},
		{"503 setup_blocked with an unknown code is not the static verdict", 503, `{"status":"setup_blocked","code":"something_else"}`, RouteUnknown},
		{"503 standby carrying an unknown code", 503, `{"status":"standby","code":"x"}`, RouteUnknown},
		{"503 with an unrecognized status", 503, `{"status":"on_fire"}`, RouteUnknown},
		{"404: the path is wrong, the engine is not the subject", 404, `not found`, RouteUnknown},
		{"302: an unfollowed redirect is not an answer", 302, "", RouteUnknown},
		{"500 from the proxy", 500, `{"status":"ok"}`, RouteUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyRouteResponse(tc.code, []byte(tc.body)); got != tc.want {
				t.Errorf("classify(%d, %q) = %s, want %s", tc.code, tc.body, got, tc.want)
			}
		})
	}
}

// fakeAPIServer is a local stand-in for the Kubernetes API server: TLS with a CA
// the client must actually verify, and a bearer token it must actually present. It
// records what it was asked so the request itself can be asserted.
type fakeAPIServer struct {
	srv  *httptest.Server
	cfg  *rest.Config
	mu   sync.Mutex
	reqs []*http.Request
}

const fakeAPIToken = "fake-apiserver-token-do-not-log" //nolint:gosec // a test fixture, not a credential

func newFakeAPIServer(t *testing.T, handler http.HandlerFunc) *fakeAPIServer {
	t.Helper()
	f := &fakeAPIServer{}
	f.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.reqs = append(f.reqs, r.Clone(r.Context()))
		f.mu.Unlock()
		if got := r.Header.Get("Authorization"); got != "Bearer "+fakeAPIToken {
			// Authentication is not decoration here: an operator that loses its
			// credential must fail the observation, not read someone else's pod.
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		handler(w, r)
	}))
	// A rejected TLS handshake is an EXPECTED outcome of one of these tests; keep it
	// out of the process log so it cannot be mistaken for a failure, and so the
	// log-capture assertions see only what the code under test wrote.
	f.srv.Config.ErrorLog = stdlog.New(io.Discard, "", 0)
	f.srv.StartTLS()
	t.Cleanup(f.srv.Close)
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.srv.Certificate().Raw})
	f.cfg = &rest.Config{
		Host:            f.srv.URL,
		BearerToken:     fakeAPIToken,
		TLSClientConfig: rest.TLSClientConfig{CAData: caPEM},
	}
	return f
}

func (f *fakeAPIServer) requests() []*http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*http.Request, len(f.reqs))
	copy(out, f.reqs)
	return out
}

func (f *fakeAPIServer) prober(t *testing.T) *podProxyRouteProber {
	t.Helper()
	p, err := NewPodProxyRouteProber(f.cfg)
	if err != nil {
		t.Fatalf("build prober: %v", err)
	}
	impl, ok := p.(*podProxyRouteProber)
	if !ok {
		t.Fatalf("prober = %T, want the pod-proxy implementation", p)
	}
	return impl
}

// TestPodProxyProber_RequestShape pins the request the production transport makes:
// the fixed pod-proxy path, a GET, no body, the credential presented, and nothing
// derived from a custom resource. This is the part a fake client cannot assert and
// a real cluster asserts too late.
func TestPodProxyProber_RequestShape(t *testing.T) {
	f := newFakeAPIServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(engineOKFirstBoot))
	})
	if got := f.prober(t).ProbeRouteReadiness(context.Background(), "olivares-e2e", "cp-0"); got != RouteReady {
		t.Fatalf("probe = %s, want %s", got, RouteReady)
	}
	reqs := f.requests()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d, want exactly one", len(reqs))
	}
	req := reqs[0]
	if req.Method != http.MethodGet {
		t.Errorf("method = %s, want GET: this observation must never act on a pod", req.Method)
	}
	const wantPath = "/api/v1/namespaces/olivares-e2e/pods/https:cp-0:8443/proxy/readyz"
	if req.URL.Path != wantPath {
		t.Errorf("path = %q, want %q", req.URL.Path, wantPath)
	}
	if req.URL.RawQuery != "" {
		t.Errorf("query = %q, want none", req.URL.RawQuery)
	}
	if req.ContentLength > 0 {
		t.Errorf("content-length = %d, want no body", req.ContentLength)
	}
}

// TestPodProxyProber_EngineAnswersEndToEnd runs the real transport against each
// answer the engine actually gives, so the classification is exercised through the
// same path production uses rather than only as a pure function.
func TestPodProxyProber_EngineAnswersEndToEnd(t *testing.T) {
	tests := []struct {
		name string
		code int
		body string
		want RouteReadiness
	}{
		{"first boot without the administrative pool", 503, engineSetupBlockedB, RouteSetupBlocked},
		{"a leader that will serve", 200, engineOKComplete, RouteReady},
		{"a standby still carrying the label", 503, engineStandby, RouteNotReady},
		{"the manager may not read the pod proxy", 403, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403}`, RouteForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeAPIServer(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte(tc.body))
			})
			if got := f.prober(t).ProbeRouteReadiness(context.Background(), "ns", "cp-0"); got != tc.want {
				t.Errorf("probe = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestPodProxyProber_BoundsTheResponseRead: the body limit is a real bound, not a
// comment. A document one byte over it is unverified rather than parsed.
func TestPodProxyProber_BoundsTheResponseRead(t *testing.T) {
	// JSON tolerates trailing whitespace, so padding keeps the document VALID: the
	// only thing that changes between these two cases is its size.
	pad := func(total int) string {
		doc := `{"status":"ok","store":"up","leader":true}`
		return doc + strings.Repeat(" ", total-len(doc))
	}
	tests := []struct {
		name string
		body string
		want RouteReadiness
	}{
		{"exactly at the limit", pad(routeProbeBodyLimit), RouteReady},
		{"one byte over the limit", pad(routeProbeBodyLimit + 1), RouteUnknown},
		{"far over the limit", pad(64 * 1024), RouteUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeAPIServer(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			})
			if got := f.prober(t).ProbeRouteReadiness(context.Background(), "ns", "cp-0"); got != tc.want {
				t.Errorf("probe over %d bytes = %s, want %s", len(tc.body), got, tc.want)
			}
		})
	}
}

// TestPodProxyProber_RefusesRedirects: a 3xx must not carry the operator's
// credential to another address. The redirect target is never requested and the
// unfollowed response is unverified.
func TestPodProxyProber_RefusesRedirects(t *testing.T) {
	var elsewhere int
	f := newFakeAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/elsewhere") {
			elsewhere++
			_, _ = w.Write([]byte(engineOKComplete))
			return
		}
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	})
	if got := f.prober(t).ProbeRouteReadiness(context.Background(), "ns", "cp-0"); got != RouteUnknown {
		t.Errorf("probe = %s, want %s for an unfollowed redirect", got, RouteUnknown)
	}
	if elsewhere != 0 {
		t.Errorf("the redirect target was requested %d times; the credential must not follow a redirect", elsewhere)
	}
}

// TestPodProxyProber_VerifiesServerIdentity: TLS is the API server's, unmodified.
// A server this client cannot verify yields no observation — nothing here relaxes
// verification to "get an answer".
func TestPodProxyProber_VerifiesServerIdentity(t *testing.T) {
	f := newFakeAPIServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(engineOKComplete))
	})
	untrusted := *f.cfg
	untrusted.TLSClientConfig = rest.TLSClientConfig{} // no CA: the certificate is unverifiable
	p, err := NewPodProxyRouteProber(&untrusted)
	if err != nil {
		t.Fatalf("build prober: %v", err)
	}
	if got := p.ProbeRouteReadiness(context.Background(), "ns", "cp-0"); got != RouteUnknown {
		t.Errorf("probe against an unverifiable server = %s, want %s", got, RouteUnknown)
	}
}

// TestPodProxyProber_MissingCredentialIsNotAVerdict: a manager whose token is gone
// is refused by the API server, and that refusal is about the OPERATOR. It must
// never read as "the engine is ready".
func TestPodProxyProber_MissingCredentialIsNotAVerdict(t *testing.T) {
	f := newFakeAPIServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(engineOKComplete))
	})
	anonymous := *f.cfg
	anonymous.BearerToken = ""
	p, err := NewPodProxyRouteProber(&anonymous)
	if err != nil {
		t.Fatalf("build prober: %v", err)
	}
	if got := p.ProbeRouteReadiness(context.Background(), "ns", "cp-0"); got != RouteUnknown {
		t.Errorf("unauthenticated probe = %s, want %s", got, RouteUnknown)
	}
}

// TestPodProxyProber_BoundsTheCall pins the production deadline and proves the
// bound is applied: a server that never answers costs one timeout, not a wedged
// reconcile.
func TestPodProxyProber_BoundsTheCall(t *testing.T) {
	if routeProbeTimeout > 3*time.Second {
		t.Fatalf("routeProbeTimeout = %s, want at most 3s (the engine bounds /readyz at 2s)", routeProbeTimeout)
	}
	blocked := make(chan struct{})
	f := newFakeAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-blocked:
		}
		_, _ = w.Write([]byte(engineOKComplete))
	})
	t.Cleanup(func() { close(blocked) })

	p := f.prober(t)
	if p.timeout != routeProbeTimeout {
		t.Errorf("constructed timeout = %s, want the production %s", p.timeout, routeProbeTimeout)
	}
	// Shortened so the assertion costs milliseconds; the production value is pinned
	// above. What is being proved is that the bound EXISTS and ends the call.
	p.timeout = 50 * time.Millisecond
	start := time.Now()
	if got := p.ProbeRouteReadiness(context.Background(), "ns", "cp-0"); got != RouteUnknown {
		t.Errorf("probe of a hung server = %s, want %s", got, RouteUnknown)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("a hung server held the reconcile for %s; the probe must bound itself", elapsed)
	}
}

// TestPodProxyProber_Cancellation: a canceled reconcile ends the probe at once and
// can never yield Ready. A success that arrives while the caller is being torn down
// is stale, and stale is not readiness.
func TestPodProxyProber_Cancellation(t *testing.T) {
	t.Run("canceled before the call: no request at all", func(t *testing.T) {
		f := newFakeAPIServer(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(engineOKComplete))
		})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if got := f.prober(t).ProbeRouteReadiness(ctx, "ns", "cp-0"); got != RouteUnknown {
			t.Errorf("probe = %s, want %s", got, RouteUnknown)
		}
		if n := len(f.requests()); n != 0 {
			t.Errorf("requests = %d, want none: a canceled reconcile must not speak to the cluster", n)
		}
	})

	t.Run("canceled during the call: not Ready, and prompt", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		f := newFakeAPIServer(t, func(w http.ResponseWriter, _ *http.Request) {
			close(entered)
			<-release
			// The answer the engine WOULD have given, written after cancellation.
			_, _ = w.Write([]byte(engineOKComplete))
		})
		t.Cleanup(func() { close(release) })

		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			<-entered
			cancel()
		}()
		defer cancel()
		start := time.Now()
		if got := f.prober(t).ProbeRouteReadiness(ctx, "ns", "cp-0"); got != RouteUnknown {
			t.Errorf("probe = %s, want %s: a success that arrives after cancellation is stale", got, RouteUnknown)
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Errorf("cancellation took %s to end the probe; it must end promptly", elapsed)
		}
	})
}

// cancelingRoundTripper delivers a COMPLETE successful response and cancels the
// caller's context while doing it — the one interleaving a real client cannot be
// asked to produce on demand: the answer arrived, and by the time it is classified
// the reconcile it belongs to is gone.
type cancelingRoundTripper struct {
	cancel context.CancelFunc
	body   string
}

func (c cancelingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	c.cancel()
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(c.body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Request:    req,
	}, nil
}

// TestPodProxyProber_StaleSuccessIsNotReady: a 200 that completes under a canceled
// context is STALE, and stale is not readiness. Without this the manager could
// report Ready from an observation belonging to a reconcile that no longer exists —
// which is the same error as caching a success, arrived at by timing.
func TestPodProxyProber_StaleSuccessIsNotReady(t *testing.T) {
	f := newFakeAPIServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(engineOKComplete))
	})
	p := f.prober(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.client = &http.Client{Transport: cancelingRoundTripper{cancel: cancel, body: engineOKComplete}}

	if got := p.ProbeRouteReadiness(ctx, "ns", "cp-0"); got != RouteUnknown {
		t.Errorf("probe = %s, want %s: a success delivered under a canceled context is stale", got, RouteUnknown)
	}
	// Control: the same transport, WITHOUT the cancellation, is a Ready — so the
	// assertion above measures the cancellation and not a broken fixture.
	live, liveCancel := context.WithCancel(context.Background())
	defer liveCancel()
	p.client = &http.Client{Transport: cancelingRoundTripper{cancel: func() {}, body: engineOKComplete}}
	if got := p.ProbeRouteReadiness(live, "ns", "cp-0"); got != RouteReady {
		t.Errorf("control probe = %s, want %s", got, RouteReady)
	}
}

// stubRoundTripper answers with a canned response WITHOUT consulting the request
// context, and optionally after a delay. A transport that ignores its deadline is
// not hypothetical — an intermediary, a hook, or a future client change can all
// produce one — and it is the only way to see whether this prober checks the
// budget it promised rather than merely setting it.
type stubRoundTripper struct {
	delay  time.Duration
	body   string
	closed *int
}

func (s stubRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       &countingBody{Reader: strings.NewReader(s.body), closed: s.closed},
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Request:    req,
	}, nil
}

// countingBody records that the body was closed, which is a resource contract and
// not a style rule: a body left open on a keep-alive connection leaks it.
type countingBody struct {
	*strings.Reader
	closed *int
}

func (c *countingBody) Close() error {
	if c.closed != nil {
		*c.closed++
	}
	return nil
}

// TestPodProxyProber_NestedDeadlineExpiryIsNotAccepted: the prober's own budget is
// checked, not only the caller's. A transport that ignores the context can deliver
// a complete, entirely valid 200 after the deadline this prober promised to honor;
// accepting it would report readiness measured outside the window it was allowed.
func TestPodProxyProber_NestedDeadlineExpiryIsNotAccepted(t *testing.T) {
	f := newFakeAPIServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(engineOKComplete))
	})
	p := f.prober(t)
	p.timeout = 20 * time.Millisecond
	p.client = &http.Client{Transport: stubRoundTripper{delay: 80 * time.Millisecond, body: engineOKComplete}}

	// The PARENT is healthy and unbounded: only the nested budget expired.
	parent := context.Background()
	if got := p.ProbeRouteReadiness(parent, "ns", "cp-0"); got != RouteUnknown {
		t.Errorf("probe = %s, want %s: the answer arrived after this call's own deadline", got, RouteUnknown)
	}
	if parent.Err() != nil {
		t.Fatal("the parent context was disturbed; this case must isolate the NESTED budget")
	}

	// Control: the same transport inside its budget IS a verdict, so the assertion
	// above measures the deadline and not a broken stub.
	p.timeout = time.Second
	if got := p.ProbeRouteReadiness(parent, "ns", "cp-0"); got != RouteReady {
		t.Errorf("control probe = %s, want %s", got, RouteReady)
	}
}

// TestPodProxyProber_ClosesEveryResponseBody: every path closes the body — the one
// that classifies an answer and the one that refuses an oversized document.
func TestPodProxyProber_ClosesEveryResponseBody(t *testing.T) {
	oversized := `{"status":"ok"}` + strings.Repeat(" ", routeProbeBodyLimit)
	for _, tc := range []struct {
		name string
		body string
		want RouteReadiness
	}{
		{"a classified answer", engineOKComplete, RouteReady},
		{"a refused oversized document", oversized, RouteUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeAPIServer(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(engineOKComplete))
			})
			closed := 0
			p := f.prober(t)
			p.client = &http.Client{Transport: stubRoundTripper{body: tc.body, closed: &closed}}
			if got := p.ProbeRouteReadiness(context.Background(), "ns", "cp-0"); got != tc.want {
				t.Fatalf("probe = %s, want %s", got, tc.want)
			}
			if closed != 1 {
				t.Errorf("response body closed %d times, want exactly once", closed)
			}
		})
	}
}

// TestPodProxyProber_LogsNoSecretsOrPayload: the observation returns one enum, and
// nothing it saw — the bearer token, the response document, a header — is written
// anywhere. A status or a log line is where a credential outlives the process that
// held it.
func TestPodProxyProber_LogsNoSecretsOrPayload(t *testing.T) {
	var captured bytes.Buffer
	restore := stdlog.Writer()
	stdlog.SetOutput(&captured)
	t.Cleanup(func() { stdlog.SetOutput(restore) })

	f := newFakeAPIServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Sensitive-Header", "header-value-do-not-log")
		w.WriteHeader(503)
		_, _ = w.Write([]byte(engineSetupBlockedB))
	})
	// Both sinks a Kubernetes controller writes through: the process logger and the
	// one carried on the reconcile context, at debug verbosity so nothing is hidden
	// by a level.
	ctx := log.IntoContext(context.Background(), zap.New(zap.WriteTo(&captured), zap.UseDevMode(true)))

	if got := f.prober(t).ProbeRouteReadiness(ctx, "ns", "cp-0"); got != RouteSetupBlocked {
		t.Fatalf("probe = %s, want %s", got, RouteSetupBlocked)
	}
	for _, secret := range []string{
		fakeAPIToken, "header-value-do-not-log", "cross_tenant_admin_pool_not_configured",
		"setup_blocked", "remedy",
	} {
		if strings.Contains(captured.String(), secret) {
			t.Errorf("the probe logged %q:\n%s", secret, captured.String())
		}
	}
}

// TestPodProxyProber_RefusesAnUnusableTarget: the request is built with the
// client's own path facilities, which reject a segment that would address
// something else. A rejected build makes no request rather than a request to the
// wrong object.
func TestPodProxyProber_RefusesAnUnusableTarget(t *testing.T) {
	f := newFakeAPIServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(engineOKComplete))
	})
	p := f.prober(t)
	for _, tc := range []struct{ name, ns, pod string }{
		{"empty namespace", "", "cp-0"},
		{"empty pod", "ns", ""},
		{"a pod name that would escape its path segment", "ns", "cp-0/../../secrets"},
		{"a namespace that would escape its path segment", "ns/../..", "cp-0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := p.ProbeRouteReadiness(context.Background(), tc.ns, tc.pod); got != RouteUnknown {
				t.Errorf("probe(%q, %q) = %s, want %s", tc.ns, tc.pod, got, RouteUnknown)
			}
		})
	}
	if n := len(f.requests()); n != 0 {
		t.Errorf("requests = %d, want none: an unbuildable target is not asked", n)
	}
}

// TestNewPodProxyRouteProber_FailsClosed: the manager must not start with a
// half-built observer. A nil configuration is an error, never a prober that
// silently answers "unknown" forever.
func TestNewPodProxyRouteProber_FailsClosed(t *testing.T) {
	if _, err := NewPodProxyRouteProber(nil); err == nil {
		t.Fatal("a nil REST config produced a prober; the manager must fail to start instead")
	}
}
