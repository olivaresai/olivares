// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/olivaresai/olivares/core/metrics"
)

// httpDurationBuckets are the latency histogram bounds (seconds) for the API. They
// span sub-millisecond reads to multi-second module calls — the range an operator
// alerts on — without the cardinality of a per-route histogram.
var httpDurationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// ingestDurationBuckets are the latency bounds (seconds) for one accepted
// observation on the collector→core ingest path: from the moment the engine
// receives a pushed envelope to the moment it is durably lifted onto the bus.
// They start finer than the HTTP buckets (a healthy enqueue is sub-millisecond)
// and top out at 5s — long enough to capture the backpressure tail, because the
// in-process bus applies blocking backpressure (a full subscriber queue blocks
// the publish inside Ingest) and a saturated SQLite writer blocks up to its 5s
// busy_timeout. This is the SLI the ingest-p99 SLO is measured against and the
// primary backpressure signal (it spikes when a downstream subscriber stalls).
var ingestDurationBuckets = []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5}

// initMetrics builds (or adopts) the Prometheus registry and the metric handles the
// hot paths increment. It registers a live store-reachability collector read at
// scrape time. Cardinality is bounded by construction: the only labels are the HTTP
// method, the numeric status code, and the observation kind — all small, fixed sets.
func (s *Server) initMetrics(reg *metrics.Registry) {
	if reg == nil {
		reg = metrics.New(s.version, s.clock.Now().Time())
	}
	s.metrics = reg
	s.mReqTotal = reg.CounterVec("olivares_http_requests_total",
		"Total HTTP requests handled by the API, by method and response status code.", "method", "code")
	s.mReqDur = reg.HistogramVec("olivares_http_request_duration_seconds",
		"HTTP request duration in seconds, by method.", httpDurationBuckets, "method")
	s.mInflight = reg.Gauge("olivares_http_requests_in_flight",
		"HTTP requests currently being served.")
	s.mIngestObs = reg.CounterVec("olivares_ingest_observations_total",
		"Observations accepted by the collector->core ingest service, by kind.", "kind")
	s.mIngestDur = reg.HistogramVec("olivares_ingest_duration_seconds",
		"Time to lift one accepted observation onto the bus (collector->core ingest). p99 is the ingest-latency SLI; it rises under bus backpressure.", ingestDurationBuckets)
	s.mIngestRej = reg.Counter("olivares_ingest_rejected_total",
		"Observations rejected by the collector->core ingest service after authorization (decode or publish/backpressure error). With accepted, the denominator of the ingest success-ratio SLI.")
	// gRPC SLIs (the docs/17 §5 per-RPC count/latency/error gap). method is
	// the full RPC name — a small fixed set (ControlPlane + IngestService + the
	// health service), bounded like the HTTP method label. Duration is observed
	// for UNARY RPCs only: the one streaming RPC (IngestService.Push) is a
	// long-lived collector channel whose "duration" is its lifetime, which would
	// land every observation in +Inf and tick only at stream close (docs/17 §5).
	s.mGRPCTotal = reg.CounterVec("olivares_grpc_requests_total",
		"Total gRPC requests handled, by full method and status code (streams counted at completion).", "method", "code")
	s.mGRPCDur = reg.HistogramVec("olivares_grpc_request_duration_seconds",
		"Unary gRPC request duration in seconds, by full method.", httpDurationBuckets, "method")
	// Login/abuse SLI (docs/17 §5 — previously audit-only). Pre-created so
	// an abuse query sees a zero baseline, not "no data" until the first failure.
	// Scope: the password login endpoint; SSO logins mint sessions elsewhere.
	s.mLogin = reg.CounterVec("olivares_auth_login_attempts_total",
		"Password login attempts by outcome (success, failed = bad credentials, locked_out = throttle lockout). Store/setup errors are not attempts and are not counted.", "outcome")
	for _, o := range []string{loginOutcomeSuccess, loginOutcomeFailed, loginOutcomeLockedOut} {
		s.mLogin.Add(0, o)
	}

	// Store reachability, read live at scrape time with a short timeout so a wedged
	// backend yields store_up 0 (and a 503 from /readyz) rather than hanging the
	// scrape. It is the single dependency a control-plane operator alerts on first.
	reg.RegisterFunc("olivares_store_up", func(w io.Writer) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		up := 0
		if s.st.Ping(ctx) == nil {
			up = 1
		}
		fmt.Fprintf(w, "# HELP olivares_store_up Store reachability (1 = the store answered a ping, 0 = it did not).\n# TYPE olivares_store_up gauge\nolivares_store_up %d\n", up)
	})
}

// recordRequest updates the request counter and latency histogram for a completed
// request. It is called by the access-log middleware, which already wraps every
// request and measured the duration, so there is no second wrapper.
func (s *Server) recordRequest(method string, status int, dur time.Duration) {
	s.mReqTotal.Inc(method, strconv.Itoa(status))
	s.mReqDur.Observe(dur.Seconds(), method)
}

// handleMetrics serves the Prometheus text exposition (format version 0.0.4). It is
// unauthenticated and setup-exempt like /healthz (a Prometheus scraper presents no
// bearer). It exposes only structural engine counters/gauges — request rates,
// in-flight, ingest throughput, Go runtime, store reachability — never tenant data,
// tokens, or any value useful for recon (OBS-06; docs/SECURITY-HARDENING.md,§3). In production bind
// it to a trusted scrape network (packaging documents the scrape config).
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if !s.allowMetrics(w, r) {
		return
	}
	w.Header().Set("Content-Type", metrics.ContentType)
	w.WriteHeader(http.StatusOK)
	s.metrics.WritePrometheus(w)
}

// handleLivez is the liveness probe: if the process can answer, it is alive. It runs
// NO dependency check on purpose — a failing dependency must not trigger a liveness
// restart loop; that is what readiness is for.
func (s *Server) handleLivez(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handlePodReadyz is the POD-health probe (stage-2, design §B.1 — the
// Patroni-style split): the pod is healthy when the process serves and its store
// is reachable. It deliberately does NOT consult Leader().Active(), so a hot
// standby answers 200 and the kubelet marks it Ready.
//
// That is the whole point of the split. /readyz means "route client traffic here"
// (leader-only); with it wired as the container readinessProbe an
// active-passive StatefulSet can never reach ReadyReplicas==desired and its
// rolling update wedges at the first replaced standby, because a never-Ready pod
// never satisfies the update barrier. Pod health and leader eligibility are two
// different questions, so they get two different endpoints: the kubelet asks this
// one, the leader-selecting Service resolves routing from the leader label the
// engine publishes, and every application route still re-checks leadership
// (leaderGate) so a stale label can never make a standby serve.
//
// The store ping is the same 2s-bounded check /readyz runs first: a pod whose
// store is unreachable is NOT healthy — it leaves the endpoints (that is what
// readiness is for) but is not restarted (that is /livez's job).
func (s *Server) handlePodReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.st.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "unavailable", "store": "down"})
		return
	}
	// `leader` reports ESTABLISHED leadership (the same predicate the leader gate and
	// the routing label use), so a pod that is mid-promotion reads as not-leader here
	// even though the store lets its own bootstrap write.
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "store": "up", "leader": s.st.Leader().IsLeader(),
		"setup_required": !s.setupCompleteNow(r),
	})
}

// handleReadyz is the readiness probe: the engine is ready to serve when its store
// is reachable AND this node is the active writer. It pings with a short timeout so
// a wedged backend yields 503 (the load balancer drains this instance) instead of
// hanging the probe. setup_required is reported for observability but does NOT fail
// readiness — a freshly booted engine is ready to BE set up.
//
// HA failover: in an active-passive cluster a STANDBY reports 503 here so
// Kubernetes removes it from the Service endpoints — WITHOUT restarting it (that is
// /livez's job; restarting a healthy hot standby would be wrong). When the leader
// dies a standby acquires leadership and this flips to 200, so traffic follows the
// new leader automatically. The store ping runs FIRST so a standby still surfaces a
// real store outage; the body distinguishes the two states for an operator's logs.
// On a single-node store the elector is always active, so this is unchanged.
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.st.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "unavailable", "store": "down"})
		return
	}
	if !s.st.Leader().IsLeader() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "standby", "store": "up", "leader": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "store": "up", "leader": true, "setup_required": !s.setupCompleteNow(r),
	})
}
