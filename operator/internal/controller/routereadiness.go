// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// RouteReadiness is the CLOSED classification of ONE traffic-readiness
// observation of the pod the leader Service resolves to. It is an enum on purpose:
// the engine's response strings are an input to this decision, never a reason the
// operator republishes in status (a status reason is a contract an alert routes
// on, and a scalar copied out of a response body is neither fixed nor ours).
//
// The zero value is RouteUnknown, so every path that fails to produce a VERIFIED
// answer — no client wired, an unreadable pod, a transport failure, a schema this
// operator does not recognize — lands on "unverified" without anyone remembering
// to say so.
type RouteReadiness uint8

const (
	// RouteUnknown: nothing was verified. It is NOT evidence that the engine is
	// failing, and it never permits PhaseReady (SDD06 Q15: an unknown must not be
	// reported as a success, nor as a failure it did not demonstrate).
	RouteUnknown RouteReadiness = iota
	// RouteReady: the leader answered 200 with the engine's ok schema. That answers
	// "route client traffic here" for THIS request; it is not durable authorization
	// (application routes still refuse until POST /v1/setup completes) and it is
	// never remembered across reconciles.
	RouteReady
	// RouteSetupBlocked: the leader answered 503 with the one schema that means
	// first setup cannot complete until a human configures the cross-tenant
	// administrative pool. Verified and static: nothing in the cluster changes it.
	RouteSetupBlocked
	// RouteNotReady: the leader answered 503 with another schema this operator
	// recognizes (a standby that still carries the label, a store that went down
	// under a Ready pod, a setup-capability observation that itself failed). Real,
	// and normally transient.
	RouteNotReady
	// RouteForbidden: the API server refused the observation itself (403). That is
	// the OPERATOR's RBAC, not the engine's health — see the pods/proxy marker.
	RouteForbidden
)

// String is a FIXED token for diagnostics. It never carries response text.
func (rr RouteReadiness) String() string {
	switch rr {
	case RouteReady:
		return "RouteReady"
	case RouteSetupBlocked:
		return reasonSetupBlocked
	case RouteNotReady:
		return reasonRouteNotReady
	case RouteForbidden:
		return reasonRouteProbeForbidden
	default:
		return reasonRouteProbeUnknown
	}
}

// reason is the Progressing/Degraded reason a non-ready route implies. Ready has
// none: the classifier reports RolloutComplete instead.
func (rr RouteReadiness) reason() string {
	if rr == RouteReady {
		return ""
	}
	return rr.String()
}

// RouteReadinessProber observes traffic readiness on ONE named pod. It is the
// operator's only HTTP seam, injected so unit tests state the observation instead
// of simulating an API server — and so a manager that cannot build the real client
// fails at startup rather than degrading to the leader LABEL, which is discovery,
// not readiness.
type RouteReadinessProber interface {
	// ProbeRouteReadiness returns a classification, never an error and never a
	// payload: everything unverified is RouteUnknown. The caller owns the decision
	// about WHICH pod may be asked (ownership, UID, liveness); this owns the call.
	ProbeRouteReadiness(ctx context.Context, namespace, pod string) RouteReadiness
}

const (
	// routeObservationBudget bounds the ENTIRE observation, not merely its HTTP
	// leg: the ownership read before the call, the call and its body, and the
	// ownership read after it. All three speak to the same API server and any of
	// them can hang, so a budget that covered only the middle one bounded nothing —
	// two uncached reads still ran under the reconcile's own context.
	//
	// The engine's own /readyz budget is 2s (core/api/metrics.go), so 3s is one
	// margin above it and still far below the 30s progress re-queue: a hung API
	// server can never sit on the reconcile loop.
	routeObservationBudget = 3 * time.Second
	// routeProbeTimeout bounds the HTTP leg on its own. It is the same 3s: the
	// prober is injected and must be safe when called with an unbounded context,
	// while inside an observation the smaller of the two — always the observation's
	// remaining budget — is what actually applies.
	routeProbeTimeout = routeObservationBudget
	// routeProbeBodyLimit bounds the response read. The engine's health documents
	// are a few hundred bytes; anything larger is not one, and reading it would let
	// a compromised (or merely wrong) upstream spend the operator's memory. One
	// sentinel byte beyond the limit is read so "exactly at the limit" and
	// "truncated" are distinguishable.
	routeProbeBodyLimit = 4096
	// routeProbePort / routeProbePath are FIXED. The operator renders the container
	// port itself (https/8443) and there is no listen-port field on ControlPlane, so
	// nothing about this target comes from the custom resource. /readyz is the
	// traffic-readiness surface; /pod-readyz is pod health and would re-encode the
	// very defect this observation exists to close.
	routeProbePort = "8443"
	routeProbePath = "readyz"
)

// engineHealth is the ONLY response schema this operator recognizes: the engine's
// own readiness document (core/api/metrics.go). Kind/APIVersion are decoded so the
// API server's own Status envelope — which it returns for ITS failures, and which
// can carry any words at all in its message — can be rejected explicitly instead of
// being mistaken for a health document that happens to parse.
type engineHealth struct {
	Kind       string `json:"kind"`
	APIVersion string `json:"apiVersion"`
	Status     string `json:"status"`
	Code       string `json:"code"`
}

// Engine health vocabulary (core/api/metrics.go handleReadyz). Recognized
// EXACTLY: an unlisted status, or a listed one carrying a code this operator does
// not know, is RouteUnknown — a classification this operator cannot defend is
// worth less than an honest "unverified".
const (
	engineStatusOK               = "ok"
	engineStatusStandby          = "standby"
	engineStatusStoreUnavailable = "unavailable"
	engineStatusSetupBlocked     = "setup_blocked"
	engineStatusSetupUnavailable = "setup_unavailable"

	engineCodeAdminPoolNotConfigured = "cross_tenant_admin_pool_not_configured"
	engineCodeSetupStateUnavailable  = "setup_state_unavailable"
	engineCodeSetupProbeUnavailable  = "setup_probe_unavailable"
)

// classifyRouteResponse maps one HTTP answer onto the closed enum. It is pure, so
// the whole vocabulary is testable without a server.
func classifyRouteResponse(code int, body []byte) RouteReadiness {
	if code == http.StatusForbidden {
		// The API server refused the observation. Reading the body would tell us
		// nothing the operator may act on, and the remedy is the manager's RBAC.
		return RouteForbidden
	}
	if code != http.StatusOK && code != http.StatusServiceUnavailable {
		// Anything else (a redirect left unfollowed, 404 from a wrong path, a 500
		// from the proxy, a 401) is unverified, not a verdict about the engine.
		return RouteUnknown
	}
	var h engineHealth
	if err := json.Unmarshal(body, &h); err != nil {
		return RouteUnknown
	}
	if h.Kind != "" || h.APIVersion != "" {
		// A Kubernetes Status envelope (or anything else wearing one). The API
		// server's 503 means "I could not reach that pod"; equating it with the
		// engine's 503 would report a transport failure as an engine verdict.
		return RouteUnknown
	}
	if code == http.StatusOK {
		if h.Status == engineStatusOK && h.Code == "" {
			// 200 is traffic readiness for THIS request. It stays ready when the
			// document also reports setup_required: the engine is telling us it can
			// serve the setup ceremony, which is exactly what a first boot needs.
			return RouteReady
		}
		return RouteUnknown
	}
	switch h.Status {
	case engineStatusSetupBlocked:
		if h.Code == engineCodeAdminPoolNotConfigured {
			return RouteSetupBlocked
		}
		return RouteUnknown
	case engineStatusStandby, engineStatusStoreUnavailable:
		if h.Code == "" {
			return RouteNotReady
		}
		return RouteUnknown
	case engineStatusSetupUnavailable:
		if h.Code == engineCodeSetupStateUnavailable || h.Code == engineCodeSetupProbeUnavailable {
			return RouteNotReady
		}
		return RouteUnknown
	default:
		return RouteUnknown
	}
}

// podProxyRouteProber performs the observation through the AUTHENTICATED
// Kubernetes API server's pod-proxy subresource — the same seam the kind e2e
// harness uses — and never through a pod IP or any address derived from the custom
// resource. The API server authorizes `get pods/proxy`; the PATH is constrained
// here, in code, because Kubernetes RBAC cannot express "only /readyz".
type podProxyRouteProber struct {
	// rest builds the request URL with the client's own path facilities (which
	// validate and escape each segment); it is not used to execute the call.
	rest rest.Interface
	// client executes it with the API server's TLS and credentials, and refuses to
	// follow redirects.
	client *http.Client
	// timeout is routeProbeTimeout in production; tests may shorten it to assert the
	// bound without waiting for it.
	timeout time.Duration
}

// NewPodProxyRouteProber builds the production prober from the manager's REST
// configuration. It returns an error rather than a degraded prober: a manager that
// cannot observe traffic readiness must not start and quietly answer PhaseReady
// from the leader label again.
func NewPodProxyRouteProber(cfg *rest.Config) (RouteReadinessProber, error) {
	if cfg == nil {
		return nil, errors.New("route readiness: nil REST config")
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("route readiness client: %w", err)
	}
	// The transport carries the API server's CA, client certificate and/or bearer
	// token exactly as configured. Nothing here relaxes TLS.
	tr, err := rest.TransportFor(cfg)
	if err != nil {
		return nil, fmt.Errorf("route readiness transport: %w", err)
	}
	return &podProxyRouteProber{
		rest:    cs.CoreV1().RESTClient(),
		client:  &http.Client{Transport: tr, CheckRedirect: refuseRedirect},
		timeout: routeProbeTimeout,
	}, nil
}

// refuseRedirect stops the client from following a 3xx. A redirect would take the
// credential somewhere the operator did not authorize; the unfollowed response
// then classifies as unverified.
func refuseRedirect(_ *http.Request, _ []*http.Request) error {
	return http.ErrUseLastResponse
}

// ProbeRouteReadiness issues GET
// /api/v1/namespaces/{namespace}/pods/https:{pod}:8443/proxy/readyz and classifies
// the answer. Nothing about the request is derived from the custom resource beyond
// the namespace and pod name the caller already verified, and no response text,
// header, credential or transport error leaves this function: the return value is
// one enum.
func (p *podProxyRouteProber) ProbeRouteReadiness(ctx context.Context, namespace, pod string) RouteReadiness {
	if p == nil || p.rest == nil || p.client == nil || namespace == "" || pod == "" {
		return RouteUnknown
	}
	if ctx.Err() != nil {
		return RouteUnknown
	}
	req := p.rest.Get().
		Namespace(namespace).
		Resource("pods").
		Name("https:" + pod + ":" + routeProbePort).
		SubResource("proxy").
		Suffix(routeProbePath)
	if err := req.Error(); err != nil {
		// A segment the client's own validation rejects. Building the URL anyway
		// would address SOMETHING (the subresource of the wrong object), which is
		// worse than not asking.
		return RouteUnknown
	}

	callCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodGet, req.URL().String(), nil)
	if err != nil {
		return RouteUnknown
	}
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return RouteUnknown
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, routeProbeBodyLimit+1))
	if err != nil {
		return RouteUnknown
	}
	if len(body) > routeProbeBodyLimit {
		return RouteUnknown
	}
	// Cancellation or expiry between the request and the verdict: an answer that
	// arrived while the reconcile was being torn down — or after this call's own
	// budget ran out — is stale, and stale is never a verdict.
	//
	// BOTH contexts are checked. callCtx is derived from ctx, so it already reports
	// a canceled parent; ctx is checked as well so that a future change to how the
	// nested context is built cannot silently drop the parent's cancellation. The
	// nested one is the half that a transport ignoring its context would otherwise
	// slip past: it can return a complete, entirely valid 200 after the deadline
	// this prober promised to honor.
	if ctx.Err() != nil || callCtx.Err() != nil {
		return RouteUnknown
	}
	return classifyRouteResponse(resp.StatusCode, body)
}
