// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/olivaresai/olivares/core/store"
)

// The HA leader-routing layout (stage-2, design §B.1 "Patroni-style split").
//
// In active-passive HA every replica is a healthy pod, but exactly one — the node
// holding the Postgres advisory lock — is the active writer. Kubernetes
// therefore needs TWO different signals, and the engine publishes both:
//
//   - POD HEALTH: GET /pod-readyz, leader-agnostic, wired as the container
//     readinessProbe. Every healthy replica is Ready, so a rolling update can
//     progress past a standby instead of wedging at it.
//   - LEADER ROUTING: this pod label. The operator creates a
//     `Service/<name>-leader` whose selector is `ops.olivares.ai/role=leader`, so
//     client traffic resolves to the single active writer through the ordinary
//     endpoint controllers.
//
// The label is DISCOVERY, never authority: the Postgres lock remains the sole
// write authority and every application route re-checks it (the API's leaderGate),
// so a stale label can only cost a retryable 503 — never a second writer.
const (
	// haRoleLabelKey is the pod label the leader Service selects on. It is a
	// CONTRACT shared with the operator (operator/internal/controller — the same
	// constant); changing it on one side alone silently empties the leader Service.
	haRoleLabelKey = "ops.olivares.ai/role"
	// haRoleLeader marks the pod holding the election lock (the routed writer).
	haRoleLeader = "leader"
	// haRoleStandby marks a healthy hot standby. It is published EXPLICITLY (rather
	// than removing the label) so an operator can tell "followed correctly" from
	// "never published", and so a pod whose container restarted cannot keep a stale
	// leader label from a previous incarnation.
	haRoleStandby = "standby"

	// haLabelGateEnv arms the API/gRPC leader gate on its own (a non-Kubernetes HA
	// deployment whose load balancer routes on /readyz).
	haLabelGateEnv = "OLIVARES_HA_LEADER_GATE"
	// haLabelPublishEnv arms the Kubernetes pod-label publisher. It IMPLIES the
	// gate: once standbys are Ready they are reachable, so serving application
	// traffic from them must be refused.
	haLabelPublishEnv = "OLIVARES_HA_LEADER_LABEL"

	// The projected ServiceAccount credentials the operator mounts into HA pods.
	haTokenFile = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	haCAFile    = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"

	// haLabelPoll is the resync interval of the publisher loop. It is the upper
	// bound on how long a demoted leader keeps its stale label when the elector's
	// own step-down callback path is not exercised (a lost lock session is detected
	// on the elector's 2s tick), and it self-heals a label edited out of band.
	haLabelPoll = time.Second
	// haLabelTimeout bounds one API-server call so a wedged apiserver can never
	// block the resync loop (or boot).
	haLabelTimeout = 5 * time.Second
	// haBackoffMax caps the retry interval while publication keeps failing, and
	// haLogEvery throttles the repeat warnings — a revoked RoleBinding must stay
	// VISIBLE without every replica hammering the apiserver (or the log volume)
	// once a second forever.
	haBackoffMax = 30 * time.Second
	haLogEvery   = 20
	// haRepublishEvery forces a re-PATCH every N resync ticks even when the role is
	// unchanged, so a label edited OUT OF BAND is repaired (~30s at the default
	// poll) instead of persisting until the next leadership change.
	haRepublishEvery = 30
)

// haLeaderConfig is the parsed HA leader-routing environment.
type haLeaderConfig struct {
	// Gate arms the API/gRPC leader gate (application routes answer 503 not_leader
	// on a standby).
	Gate bool
	// PublishLabel arms the Kubernetes pod-label publisher.
	PublishLabel bool
	// PodName/PodNamespace are the downward-API identity of THIS pod.
	PodName, PodNamespace string
	// APIServer is the in-cluster apiserver base URL.
	APIServer string
}

// loadHALeaderConfig parses the HA leader-routing env contract, FAIL-CLOSED: an
// unparseable boolean or a label publisher without a pod identity is an error, not
// a silently disabled feature — a silently unpublished label would leave the leader
// Service permanently empty (no client traffic at all) with no explanation.
func loadHALeaderConfig(getenv func(string) string) (haLeaderConfig, error) {
	gate, err := haBoolEnv(getenv, haLabelGateEnv)
	if err != nil {
		return haLeaderConfig{}, err
	}
	publish, err := haBoolEnv(getenv, haLabelPublishEnv)
	if err != nil {
		return haLeaderConfig{}, err
	}
	cfg := haLeaderConfig{Gate: gate || publish, PublishLabel: publish}
	if !publish {
		return cfg, nil
	}
	cfg.PodName = strings.TrimSpace(getenv("POD_NAME"))
	cfg.PodNamespace = strings.TrimSpace(getenv("POD_NAMESPACE"))
	if cfg.PodName == "" || cfg.PodNamespace == "" {
		return haLeaderConfig{}, fmt.Errorf("%s=1 needs POD_NAME and POD_NAMESPACE (downward API); without them this pod cannot publish the leader label and the leader Service would stay empty", haLabelPublishEnv)
	}
	host := strings.TrimSpace(getenv("KUBERNETES_SERVICE_HOST"))
	port := strings.TrimSpace(getenv("KUBERNETES_SERVICE_PORT"))
	if host == "" {
		return haLeaderConfig{}, fmt.Errorf("%s=1 but KUBERNETES_SERVICE_HOST is unset (not running in a Kubernetes pod)", haLabelPublishEnv)
	}
	if port == "" {
		port = "443"
	}
	cfg.APIServer = "https://" + net.JoinHostPort(host, port)
	return cfg, nil
}

func haBoolEnv(getenv func(string) string, key string) (bool, error) {
	raw := strings.TrimSpace(getenv(key))
	if raw == "" {
		return false, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s=%q is not a boolean (use 1/0, true/false)", key, raw)
	}
	return v, nil
}

// haLeaderPublisher keeps THIS pod's role label converged onto the node's live
// leadership state. It talks to the apiserver directly over the projected
// ServiceAccount credentials — no client-go dependency in the engine binary, and
// nothing Kubernetes-shaped inside core/store (leadership stays a plain Postgres
// advisory lock that also works under systemd/Docker and air-gapped).
type haLeaderPublisher struct {
	base      string // apiserver base URL
	pod       string
	namespace string
	tokenFile string
	client    *http.Client
	log       *slog.Logger
	poll      time.Duration

	// publishMu serializes PATCHes so two overlapping publications cannot land out
	// of order; mu guards the small state below.
	publishMu sync.Mutex
	mu        sync.Mutex
	last      string // last role successfully published ("" = none yet)
	fails     int    // consecutive publication failures (throttles logs + backs off)
	// done is closed when the resync loop exits, so shutdown can JOIN it before
	// issuing the final demotion (otherwise an in-flight promotion PATCH could land
	// after it and leave the pod labeled leader as it goes away).
	done chan struct{}
}

// newHALeaderPublisher builds the publisher from the in-cluster credentials. It
// fails if the CA bundle or the token is unreadable: an HA pod that cannot publish
// its role would be Ready but never routable, so it must say so loudly at boot.
func newHALeaderPublisher(cfg haLeaderConfig, log *slog.Logger) (*haLeaderPublisher, error) {
	ca, err := os.ReadFile(haCAFile)
	if err != nil {
		return nil, fmt.Errorf("read in-cluster CA %s: %w", haCAFile, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, fmt.Errorf("in-cluster CA %s holds no usable certificate", haCAFile)
	}
	if _, err := os.ReadFile(haTokenFile); err != nil {
		return nil, fmt.Errorf("read projected ServiceAccount token %s: %w", haTokenFile, err)
	}
	return &haLeaderPublisher{
		base:      cfg.APIServer,
		pod:       cfg.PodName,
		namespace: cfg.PodNamespace,
		tokenFile: haTokenFile,
		// cli-transport-exempt: ENGINE→Kubernetes API server, not the control
		// plane. Its trust anchor is the cluster CA pool read from the pod's
		// projected ServiceAccount, which is exactly what cliTransport must NOT
		// substitute an operator --ca-cert for.
		client: &http.Client{
			Timeout:   haLabelTimeout,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}},
		},
		log:  log,
		poll: haLabelPoll,
	}, nil
}

// publish merge-patches this pod's role label. The RBAC the operator provisions is
// exactly `get,patch` on the pods of this StatefulSet, so a merge patch of one
// label is the smallest possible mutation (no read-modify-write, no conflict
// retries, no ownership of any other field).
func (p *haLeaderPublisher) publish(ctx context.Context, role string) error {
	token, err := os.ReadFile(p.tokenFile)
	if err != nil {
		return fmt.Errorf("read ServiceAccount token: %w", err)
	}
	body := []byte(fmt.Sprintf(`{"metadata":{"labels":{%q:%q}}}`, haRoleLabelKey, role))
	url := fmt.Sprintf("%s/api/v1/namespaces/%s/pods/%s", p.base, p.namespace, p.pod)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/merge-patch+json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("patch pod label: %s: %s", resp.Status, strings.TrimSpace(string(snippet)))
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	p.mu.Lock()
	p.last = role
	p.mu.Unlock()
	return nil
}

// reconcile publishes role when it differs from the last SUCCESSFULLY published
// value (so steady state costs nothing and a failed publish is retried on the next
// tick). A failure is logged and returned; it never changes leadership — the
// Postgres lock is the sole authority and the request gate the sole safety net.
//
// A PERSISTENT failure (a revoked RoleBinding, an apiserver outage) would otherwise
// log once per second forever, so repeats are throttled: the first failure and then
// every haLogEvery-th, plus one line when it recovers. The condition stays visible
// without filling the log volume.
func (p *haLeaderPublisher) reconcile(ctx context.Context, role string, force bool) error {
	// One publication at a time. Without this, two overlapping PATCHes (a transition
	// racing the resync tick, or a shutdown demotion racing an in-flight promotion)
	// could complete out of order and leave `last` — and the pod — labeled with the
	// value that lost the race.
	p.publishMu.Lock()
	defer p.publishMu.Unlock()

	p.mu.Lock()
	unchanged := p.last == role
	p.mu.Unlock()
	if unchanged && !force {
		return nil
	}
	if err := p.publish(ctx, role); err != nil {
		p.mu.Lock()
		p.fails++
		n := p.fails
		p.mu.Unlock()
		if n == 1 || n%haLogEvery == 0 {
			p.log.Warn("ha: could not publish the pod role label; the leader Service may route stale until this succeeds (the write fence and the request gate keep it SAFE, not available)",
				"role", role, "pod", p.pod, "consecutive_failures", n, "err", err)
		}
		return err
	}
	p.mu.Lock()
	recovered := p.fails > 0
	p.fails = 0
	p.mu.Unlock()
	if recovered {
		p.log.Info("ha: pod role label publication recovered", "role", role, "pod", p.pod)
	}
	p.log.Info("ha: published the pod role label", "role", role, "pod", p.pod, "label", haRoleLabelKey)
	return nil
}

// backoff is how long to wait before the next resync attempt: the normal poll while
// healthy, capped exponential backoff while the apiserver keeps refusing — so a
// namespace-wide outage is not amplified by every replica retrying every second.
func (p *haLeaderPublisher) backoff() time.Duration {
	p.mu.Lock()
	fails := p.fails
	p.mu.Unlock()
	if fails == 0 {
		return p.poll
	}
	wait := p.poll
	for i := 0; i < fails && wait < haBackoffMax; i++ {
		wait *= 2
	}
	if wait > haBackoffMax {
		wait = haBackoffMax
	}
	return wait
}

// publishRole is the immediate transition path (boot and shutdown), bounded so it
// can never block either.
func (p *haLeaderPublisher) publishRole(ctx context.Context, role string) error {
	ctx, cancel := context.WithTimeout(ctx, haLabelTimeout)
	defer cancel()
	return p.reconcile(ctx, role, false)
}

// run is the resync loop: it converges the label onto the live leadership state
// every tick. It is the self-healing backstop for a failed patch, a label edited
// out of band, and demotion (the elector steps down on its own tick when it loses
// the lock session — there is no demotion callback in the store seam, and adding
// Kubernetes-shaped callbacks to a plain Postgres elector would couple the
// cross-platform leadership contract to the orchestrator).
func (p *haLeaderPublisher) run(ctx context.Context, isLeader func() bool) {
	p.mu.Lock()
	if p.done == nil {
		p.done = make(chan struct{})
	}
	done := p.done
	p.mu.Unlock()
	defer close(done)

	t := time.NewTimer(p.poll)
	defer t.Stop()
	ticks := 0
	for {
		role := haRoleStandby
		if isLeader() {
			role = haRoleLeader
		}
		// Every haRepublishEvery ticks the label is re-PATCHed even when this process
		// believes it is already correct. That is what makes the loop actually
		// SELF-HEALING: the publisher's memory cannot detect a label edited out of
		// band (an administrator, or a peer sharing the ServiceAccount), and a leader
		// whose label was cleared elsewhere would otherwise leave the leader Service
		// empty until the next leadership change.
		ticks++
		force := ticks%haRepublishEvery == 0
		pubCtx, cancel := context.WithTimeout(ctx, haLabelTimeout)
		_ = p.reconcile(pubCtx, role, force)
		cancel()
		if !t.Stop() {
			select {
			case <-t.C:
			default:
			}
		}
		t.Reset(p.backoff())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// haOperationalPaths are the endpoints that must answer on EVERY replica, on every
// listener: the kubelet probes and the scrape target. They are the only paths the
// auxiliary-listener leader gate lets through on a standby.
var haOperationalPaths = map[string]bool{
	"/healthz": true, "/livez": true, "/readyz": true, "/pod-readyz": true, "/metrics": true,
}

// leaderOnlyHandler wraps an AUXILIARY application listener (the HITL receiver, the
// voice webhook, the agent-protocols gateway, the Claude hook PEP, the inference
// proxy) with the same leader gate the API server applies internally.
//
// Those listeners are separate http.Servers with their own handlers, so they never
// pass through core/api's middleware chain. In the HA leader-routing layout every
// replica is Ready — and therefore dialable — so without this wrapper a standby
// would happily run a governed hook decision, accept an A2A push, or proxy an
// inference call while another node is the writer. Each returns the same retryable
// 503 not_leader shape the API and the store's write fence produce.
func leaderOnlyHandler(next http.Handler, st store.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if haOperationalPaths[r.URL.Path] || st.Leader().IsLeader() {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"code":"not_leader","message":"this node is not the active writer; retry against the leader"}}`))
	})
}

// haShutdownLabel demotes the label at graceful shutdown so the leader Service
// drops this pod as soon as it starts draining, instead of waiting for the endpoint
// controller to notice the failing probe.
//
// It first JOINS the resync loop (bounded): issuing the demotion while a promotion
// PATCH is still in flight could let that promotion land last and leave a
// terminating pod labeled leader — the exact state the leader Service must never
// select. Errors after that are best effort: the pod is going away and its
// readiness removes it regardless.
func (p *haLeaderPublisher) haShutdownLabel() {
	p.mu.Lock()
	done := p.done
	p.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-time.After(haLabelTimeout):
			p.log.Debug("ha: resync loop did not stop in time; demoting the label anyway")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), haLabelTimeout)
	defer cancel()
	if err := p.publishRole(ctx, haRoleStandby); err != nil && !errors.Is(err, context.Canceled) {
		p.log.Debug("ha: could not clear the leader label at shutdown (readiness will drain this pod anyway)", "err", err)
	}
}
