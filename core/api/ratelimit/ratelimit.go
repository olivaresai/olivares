// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package ratelimit is the engine's inbound request rate-limiter: a per-tenant,
// per-endpoint-class token bucket with per-tier quotas (OPS-5). It
// is the control that stops one tenant's burst from triple-ing another tenant's
// p99 in a shared multi-tenant deployment (the classic noisy-neighbor / silent
// SLA-breach vector). It is ORTHOGONAL to FinOps spend enforcement: this
// meters request RATE (in-memory token buckets, no external dependency)
// meters SPEND (µUSD budgets, store-backed). They share no state and can deny
// independently — a request can be rate-OK but budget-exhausted, or vice versa.
//
// Posture (the inverse of fail-OPEN). Fails open because a FinOps store
// outage must not take down already-approved inference; its dependency CAN be
// down. This limiter has NO external dependency to fail — it is pure in-memory
// arithmetic — so "fail" here means a bug, and allowing-without-metering would
// defeat the whole point. It is therefore fail-CLOSED by construction: every
// metered request takes a token from SOME bucket; a missing/degenerate tier never
// degrades to "unlimited", it degrades to the hard floor. The cheapness of that
// guarantee is exactly because the limiter depends on nothing external.
//
// This package is pure (it imports only core/model and core/metrics): the HTTP /
// gRPC glue — deriving the metering identity from the authenticated principal,
// writing the 429 + Retry-After, exempting the operational probes — lives in the
// api package, never here. The package is therefore unit-testable against a fake
// clock with no server, store, or socket.
package ratelimit

import (
	"context"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/olivaresai/olivares/core/metrics"
	"github.com/olivaresai/olivares/core/model"
)

// EndpointClass is the cost-class of a request. Writes hit the store and the
// audit hash-chain (costlier) so they carry a tighter quota than reads. The set
// is small and fixed by construction so the decisions metric stays low-cardinality.
type EndpointClass string

const (
	// ClassRead is a safe, read-only request (GET/HEAD/OPTIONS, read RPCs).
	ClassRead EndpointClass = "read"
	// ClassWrite is a mutating request (POST/PUT/PATCH/DELETE, write RPCs).
	ClassWrite EndpointClass = "write"
)

// classes is the fixed, ordered set used to pre-create metric series so an SLO
// query sees a real zero baseline before the first denial (not "no data").
var classes = []EndpointClass{ClassRead, ClassWrite}

// decision label values for the decisions counter.
const (
	decisionAllowed    = "allowed"     // admitted (a token was available)
	decisionLimited    = "limited"     // denied with 429/ResourceExhausted (Enforce mode)
	decisionReportOnly = "report_only" // WOULD have been denied, but allowed (ReportOnly mode)
)

var decisionLabels = []string{decisionAllowed, decisionLimited, decisionReportOnly}

// Mode selects whether the limiter enforces, shadow-reports, or is disabled.
type Mode string

const (
	// ModeEnforce denies over-limit requests (429). The secure default.
	ModeEnforce Mode = "enforce"
	// ModeReportOnly never denies but counts would-be denials (decision=report_only),
	// so an operator can size quotas against real traffic before turning on
	// enforcement (the "observe then enforce" rollout, AWS tiered-throttling). It is
	// an explicit operator opt-in, NOT a failure mode.
	ModeReportOnly Mode = "report_only"
	// ModeOff disables the limiter entirely (single-trusted-tenant deployments). An
	// explicit operator choice, logged at the composition root.
	ModeOff Mode = "off"
)

// Limit is one token bucket's parameters: a sustained refill Rate (tokens/sec)
// and a Burst capacity (the most a bucket can hold, i.e. the largest instantaneous
// spike admitted). A degenerate Limit (Rate<=0 or Burst<1) is never honored as-is;
// it is clamped to the hard floor at the point of use (see Limiter.normalize).
type Limit struct {
	Rate  float64 `json:"rate"`
	Burst float64 `json:"burst"`
}

func (l Limit) valid() bool { return l.Rate > 0 && l.Burst >= 1 }

// hardFloor is the conservative limit substituted for any missing or degenerate
// (Rate<=0 / Burst<1) configuration. It is deliberately low — a misconfiguration
// degrades to a tight-but-usable quota, NEVER to "unlimited" (fail-closed).
var hardFloor = Limit{Rate: 10, Burst: 20}

// TierLimits is the quota for one tier: a per-class limit plus an aggregate Total
// that caps the tenant's whole footprint across every class. Per-class buckets
// alone leave the sum unbounded (read 50/s AND write 20/s simultaneously, and more
// as classes grow); the Total is the ceiling that actually bounds a tenant's share
// of shared CPU/store/audit capacity. A request must satisfy BOTH its class bucket
// and the Total bucket.
type TierLimits struct {
	PerClass map[EndpointClass]Limit `json:"per_class"`
	Total    Limit                   `json:"total"`
}

// TierResolver maps a tenant to its tier name. The default (nil) resolver maps
// every tenant to the configured default tier; a StaticTierResolver maps explicit
// tenants from operator config. The seam is deliberately store-free so a future
// store-backed (Org.Settings) resolver can drop in without touching the limiter.
type TierResolver interface {
	// Tier returns the tier name for tenant, or "" to fall through to the default.
	Tier(tenant model.TenantID) string
}

// StaticTierResolver resolves tiers from a fixed map (operator config). A tenant
// not in the map falls through to the default tier.
type StaticTierResolver map[model.TenantID]string

// Tier implements TierResolver.
func (m StaticTierResolver) Tier(tenant model.TenantID) string { return m[tenant] }

// Config is the limiter's policy. It is provided by the composition root; api.New
// builds the Limiter from it (binding the shared metrics registry). The zero
// Config is unusable — New fills empty fields with built-in defaults so a caller
// that supplies only a Mode still gets a working, secure limiter.
type Config struct {
	// Tiers is the tier table (tier name -> quota). Empty -> DefaultTiers.
	Tiers map[string]TierLimits
	// DefaultTier is the tier for tenants the resolver does not place. Empty ->
	// TierDefault. It MUST exist in Tiers (New repairs it if not).
	DefaultTier string
	// Mode selects enforce / report-only / off. Empty -> ModeEnforce.
	Mode Mode
	// Resolver maps tenants to tiers. nil -> every tenant gets DefaultTier.
	Resolver TierResolver
	// Now is the limiter's OWN clock seam, independent of the server clock (which
	// also drives access-log durations and SSO TTLs — freezing it for a limiter
	// test must not freeze those). nil -> time.Now. Tests inject a fake clock here.
	Now func() time.Time
	// Shards is the number of bucket stripes (concurrency). 0 -> defaultShards. A
	// tenant's aggregate and per-class buckets always share ONE shard (keyed by the
	// identity) so a request's buckets are taken atomically under a single lock.
	Shards int
	// Store, when set, makes buckets GLOBAL across nodes (HA): every take
	// goes to the shared backend; the in-proc shards remain as the bounded
	// per-node FALLBACK behind a circuit breaker (see store.go). nil = in-proc
	// only (single-node, the pre behavior, still the default).
	Store Store
	// StoreTimeout bounds one shared-store take. 0 -> defaultStoreTimeout (250ms).
	StoreTimeout time.Duration
	// LogWarn receives the (throttled) store-degradation warning. nil = silent
	// (the fallback counter and store_up gauge still tell the story). A closure,
	// not a logger type, so the package keeps its zero-dependency purity.
	LogWarn func(msg string, err error)
}

// Built-in tier names.
const (
	TierDefault    = "default"
	TierPro        = "pro"
	TierEnterprise = "enterprise"
	// TierSystem is the tier for the cross-tenant superadmin (operator) identity.
	// It is high — operators run bulk control-plane work — but bounded, and keyed
	// per-credential so one leaked superadmin token cannot starve another. Rate
	// limiting bounds the blast radius of a compromised superadmin token; its
	// primary control remains revocation (the credential id is the revoke handle).
	TierSystem = "system"
)

// DefaultTiers is the built-in tier table. The limits are generous by design —
// the job is to absorb abusive bursts, never to throttle normal operation — but
// every tier has a finite ceiling. Writes are tighter than reads (audit-chain
// cost). System write is deliberately far below read: an operator token has no
// legitimate need to drive thousands of audited writes/sec, and capping it blunts
// a compromised-credential write flood into the costliest path.
func DefaultTiers() map[string]TierLimits {
	return map[string]TierLimits{
		TierDefault: {
			PerClass: map[EndpointClass]Limit{
				ClassRead:  {Rate: 50, Burst: 100},
				ClassWrite: {Rate: 20, Burst: 40},
			},
			Total: Limit{Rate: 60, Burst: 120},
		},
		TierPro: {
			PerClass: map[EndpointClass]Limit{
				ClassRead:  {Rate: 200, Burst: 400},
				ClassWrite: {Rate: 80, Burst: 160},
			},
			Total: Limit{Rate: 250, Burst: 500},
		},
		TierEnterprise: {
			PerClass: map[EndpointClass]Limit{
				ClassRead:  {Rate: 1000, Burst: 2000},
				ClassWrite: {Rate: 400, Burst: 800},
			},
			Total: Limit{Rate: 1200, Burst: 2400},
		},
		TierSystem: {
			PerClass: map[EndpointClass]Limit{
				ClassRead:  {Rate: 2000, Burst: 4000},
				ClassWrite: {Rate: 500, Burst: 1000},
			},
			Total: Limit{Rate: 2000, Burst: 4000},
		},
	}
}

// DefaultConfig returns a secure, working config (enforce mode, built-in tiers,
// every tenant on the default tier). The composition root overlays operator config.
func DefaultConfig() Config {
	return Config{Tiers: DefaultTiers(), DefaultTier: TierDefault, Mode: ModeEnforce}
}

const defaultShards = 16

// Decision is the outcome of an admission check. The header/response fields carry
// the BINDING bucket's numbers (the bucket that denied, or — when allowed — the
// tightest one), so the advertised limit/remaining/retry reflect the real constraint.
type Decision struct {
	// OK is whether the request is admitted. In ReportOnly it is always true.
	OK bool
	// Limited is whether the limiter WOULD have denied (true even in ReportOnly).
	Limited bool
	// Limit is the binding bucket's burst capacity (RateLimit-Limit).
	Limit int
	// Remaining is the binding bucket's whole tokens left (RateLimit-Remaining).
	Remaining int
	// RetryAfter is whole seconds until one token is available (>=1), meaningful
	// when Limited. It is a safe LOWER BOUND (ceil, min 1s): a client that obeys it
	// never gets an immediate second denial. RFC 9110 permits a lower bound, so for
	// fast rates it can over-state the true sub-second wait — a deliberate, honest
	// trade (never under-state) rather than an accident.
	RetryAfter int
	// Reset is whole seconds until the binding bucket refills to full capacity
	// (RateLimit-Reset, delta-seconds).
	Reset int
}

// bucket is one token bucket. tokens is the current fill; last is the instant of
// the last Take (advanced on EVERY take, allowed or denied, so "idle" in the sweep
// means genuinely no traffic — a continuously-denied bucket is never reaped and
// reset mid-attack).
type bucket struct {
	tokens float64
	last   time.Time
}

type shard struct {
	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
}

// Limiter is the rate limiter. Build it with New; check admission with Allow.
// Safe for concurrent use.
type Limiter struct {
	tiers       map[string]TierLimits
	defaultTier string
	mode        Mode
	resolver    TierResolver
	now         func() time.Time
	shards      []*shard
	idleTTL     time.Duration // bucket idle longer than this is swept (>= max burst/rate)
	active      int64         // live bucket count (atomic), exposed as a scrape-time gauge

	// Shared store + its circuit breaker; see store.go.
	store          Store
	storeTimeout   time.Duration
	storeFails     atomic.Int64
	storeOpenUntil atomic.Int64 // unixnano; takes skip the store until then
	logWarn        func(msg string, err error)
	warnStore      logRate

	mDecisions     *metrics.Counter
	mStoreFallback *metrics.Counter
}

// New builds a Limiter from cfg, registering its metrics into reg (nil reg => no
// metrics). Empty fields are filled with secure built-in defaults. The idle TTL is
// DERIVED from the tier table so bucket eviction is provably safe: a bucket idle
// for >= burst/rate has refilled to full, so evicting it equals handing out a fresh
// full bucket — no penalty is ever reset early. idleTTL is set to comfortably
// dominate the largest burst/rate ratio across all configured (tier,class,total)
// limits, so even an operator's high-ratio tier cannot make eviction unsafe (it
// only makes buckets live longer).
func New(cfg Config, reg *metrics.Registry) *Limiter {
	tiers := cfg.Tiers
	if len(tiers) == 0 {
		tiers = DefaultTiers()
	}
	def := cfg.DefaultTier
	if _, ok := tiers[def]; !ok {
		// An unknown/empty default tier would silently route every unplaced tenant to
		// the hard floor; repair it to a real tier so the default quota is honest.
		if _, ok := tiers[TierDefault]; ok {
			def = TierDefault
		} else {
			def = anyTierName(tiers)
		}
	}
	mode := cfg.Mode
	if mode == "" {
		mode = ModeEnforce
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	n := cfg.Shards
	if n <= 0 {
		n = defaultShards
	}
	st := cfg.StoreTimeout
	if st <= 0 {
		st = defaultStoreTimeout
	}
	l := &Limiter{
		tiers: tiers, defaultTier: def, mode: mode, resolver: cfg.Resolver, now: now,
		shards:       make([]*shard, n),
		idleTTL:      deriveIdleTTL(tiers),
		store:        cfg.Store,
		storeTimeout: st,
		logWarn:      cfg.LogWarn,
	}
	for i := range l.shards {
		l.shards[i] = &shard{buckets: map[string]*bucket{}}
	}
	if reg != nil {
		l.mDecisions = reg.CounterVec("olivares_http_ratelimit_decisions_total",
			"Inbound rate-limit admission decisions, by endpoint class and decision.", "class", "decision")
		// Pre-create every class×decision series so an SLO/error-budget query sees a
		// real zero baseline from t=0, not "no data" until the first denial.
		for _, c := range classes {
			for _, d := range decisionLabels {
				l.mDecisions.Add(0, string(c), d)
			}
		}
		reg.RegisterFunc("olivares_http_ratelimit_active_buckets", l.writeActiveBuckets)
		if l.store != nil {
			// The degraded-mode SLIs exist only when a shared store is wired (a
			// single-node limiter has no store to fall back FROM).
			l.mStoreFallback = reg.Counter("olivares_http_ratelimit_store_fallback_total",
				"Rate-limit takes served by the per-node fallback because the shared store failed or timed out. Enforcement is per-node (bounded, never unlimited) while this rises.")
			reg.RegisterFunc("olivares_http_ratelimit_store_up", l.writeStoreUp)
		}
	}
	return l
}

// IdleTTLFor returns the sweep-safe idle TTL for a tier table (empty = the
// built-in tiers) — the same derivation the limiter applies to its own shards.
// The composition root hands it to the shared store's sweeper so the
// two eviction policies cannot diverge: a bucket idle this long has provably
// refilled to full on EITHER backend.
func IdleTTLFor(tiers map[string]TierLimits) time.Duration {
	if len(tiers) == 0 {
		tiers = DefaultTiers()
	}
	return deriveIdleTTL(tiers)
}

// deriveIdleTTL returns an idle TTL that dominates the largest burst/rate ratio in
// the table (so a fully-depleted bucket of any tier has refilled to full before it
// is eligible for eviction), with a generous floor.
func deriveIdleTTL(tiers map[string]TierLimits) time.Duration {
	maxRatio := 0.0
	consider := func(l Limit) {
		if l.Rate > 0 {
			if r := l.Burst / l.Rate; r > maxRatio {
				maxRatio = r
			}
		}
	}
	for _, t := range tiers {
		for _, l := range t.PerClass {
			consider(l)
		}
		consider(t.Total)
	}
	consider(hardFloor)
	// 2x headroom over the worst ratio, with a 60s floor.
	ttl := time.Duration(maxRatio*2*float64(time.Second)) + time.Second
	if ttl < 60*time.Second {
		ttl = 60 * time.Second
	}
	return ttl
}

func anyTierName(tiers map[string]TierLimits) string {
	names := make([]string, 0, len(tiers))
	for k := range tiers {
		names = append(names, k)
	}
	sort.Strings(names) // deterministic
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

// TierFor resolves a tenant to a configured tier name, falling back to the default
// tier when the resolver returns "" or names a tier the table does not define.
func (l *Limiter) TierFor(tenant model.TenantID) string {
	if l.resolver != nil {
		if t := l.resolver.Tier(tenant); t != "" {
			if _, ok := l.tiers[t]; ok {
				return t
			}
		}
	}
	return l.defaultTier
}

// DefaultTier is the tier applied to an identity with no resolvable single tenant.
func (l *Limiter) DefaultTier() string { return l.defaultTier }

// normalize returns lim if valid, else the hard floor — so a missing or degenerate
// (Rate<=0 / Burst<1) tier limit is never honored as "unlimited" or as a
// permanent-deny brick; it degrades to a tight-but-usable quota.
func (l *Limiter) normalize(lim Limit) Limit {
	if lim.valid() {
		return lim
	}
	return hardFloor
}

// limitsFor returns the (per-class, aggregate) limits for a tier+class, normalized.
func (l *Limiter) limitsFor(tier string, class EndpointClass) (cls, total Limit) {
	t, ok := l.tiers[tier]
	if !ok {
		t = l.tiers[l.defaultTier]
	}
	return l.normalize(t.PerClass[class]), l.normalize(t.Total)
}

// Allow checks whether one request from identity (the metering key, e.g.
// "tn:<tenant>" or "su:<cred>") in the given tier and class is admitted. It takes a
// token from BOTH the per-class bucket and the tenant-aggregate bucket atomically,
// all-or-nothing — under one shard lock in-proc, or in one shared-store round trip
// when a Store is wired (global buckets in HA; ctx bounds that round trip,
// and a store failure falls back to the per-node shards behind a circuit breaker,
// see store.go) — and returns the binding decision. In ModeReportOnly it always
// admits but flags Limited and counts decision=report_only; in ModeOff it admits
// without metering.
func (l *Limiter) Allow(ctx context.Context, identity, tier string, class EndpointClass) Decision {
	if l.mode == ModeOff {
		return Decision{OK: true}
	}
	clsLim, totLim := l.limitsFor(tier, class)
	now := l.now()
	reqs := []req{
		{key: identity + "|" + string(class), lim: clsLim},
		{key: identity + "|*", lim: totLim},
	}
	var (
		wouldAllow bool
		dec        Decision
		viaStore   bool
	)
	if l.store != nil {
		if proceed, observed := l.storeGate(now); proceed {
			wouldAllow, dec, viaStore = l.storeTake(ctx, now, reqs, observed)
		}
	}
	if !viaStore {
		sh := l.shards[l.shardIndex(identity)]
		wouldAllow, dec = l.take(sh, now, reqs)
	}

	switch l.mode {
	case ModeReportOnly:
		dec.OK = true
		dec.Limited = !wouldAllow
		if wouldAllow {
			l.count(class, decisionAllowed)
		} else {
			l.count(class, decisionReportOnly)
		}
	default: // ModeEnforce
		dec.OK = wouldAllow
		dec.Limited = !wouldAllow
		if wouldAllow {
			l.count(class, decisionAllowed)
		} else {
			l.count(class, decisionLimited)
		}
	}
	return dec
}

func (l *Limiter) count(class EndpointClass, decision string) {
	if l.mDecisions != nil {
		l.mDecisions.Inc(string(class), decision)
	}
}

// req is one bucket requirement of a request (its key and the limit governing it).
type req struct {
	key string
	lim Limit
}

// bstate pairs a bucket with the limit governing it for one request.
type bstate struct {
	b   *bucket
	lim Limit
}

// take refills and (if all admit) decrements every requested bucket under the
// shard lock, all-or-nothing. It returns whether the request would be admitted and
// the binding Decision. Every bucket's last is advanced to now regardless of the
// outcome (so the sweep never reaps an actively-denied bucket). On denial NO bucket
// is decremented (no spurious penalty on the other buckets).
func (l *Limiter) take(sh *shard, now time.Time, reqs []req) (bool, Decision) {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	l.sweepLocked(sh, now)

	states := make([]bstate, len(reqs))
	wouldAllow := true
	for i, rq := range reqs {
		b := sh.buckets[rq.key]
		if b == nil {
			b = &bucket{tokens: rq.lim.Burst, last: now}
			sh.buckets[rq.key] = b
			atomic.AddInt64(&l.active, 1)
		} else {
			// Refill only on forward time. model.Clock strips the monotonic reading
			// (Timestamp normalizes to UTC), so a wall-clock step backward (NTP) yields
			// elapsed < 0; skipping it keeps tokens from going negative and 429-ing a
			// blameless tenant. (elapsed == 0 adds nothing, so > 0 and >= 0 are equivalent.)
			elapsed := now.Sub(b.last).Seconds()
			if elapsed > 0 {
				b.tokens += elapsed * rq.lim.Rate
				if b.tokens > rq.lim.Burst {
					b.tokens = rq.lim.Burst
				}
			}
		}
		b.last = now // advance on every take, allowed or denied
		states[i] = bstate{b: b, lim: rq.lim}
		if b.tokens < 1 {
			wouldAllow = false
		}
	}
	if wouldAllow {
		for _, s := range states {
			s.b.tokens -= 1
		}
	}
	return wouldAllow, bindingDecision(states, wouldAllow)
}

// bindingDecision picks the constraint to advertise: the bucket with the fewest
// tokens left — on denial that is the most-restrictive (longest wait), on
// admission the tightest remaining headroom.
func bindingDecision(states []bstate, allowed bool) Decision {
	tokens := make([]float64, len(states))
	lims := make([]Limit, len(states))
	for i := range states {
		tokens[i] = states[i].b.tokens
		lims[i] = states[i].lim
	}
	return bindingFrom(tokens, lims, allowed)
}

// bindingFrom is the storage-agnostic binding math, shared by the in-proc and
// shared-store paths so the advertised headers cannot drift between them.
func bindingFrom(tokens []float64, lims []Limit, allowed bool) Decision {
	binding := -1
	for i := range tokens {
		if binding < 0 || tokens[i] < tokens[binding] {
			binding = i
		}
	}
	if binding < 0 {
		return Decision{OK: allowed}
	}
	t, rate := tokens[binding], lims[binding].Rate
	d := Decision{
		OK:        allowed,
		Limit:     int(lims[binding].Burst),
		Remaining: int(math.Floor(math.Max(0, t))),
		Reset:     secondsUntil(lims[binding].Burst-t, rate),
	}
	if !allowed {
		d.RetryAfter = secondsUntil(1-t, rate)
		if d.RetryAfter < 1 {
			d.RetryAfter = 1 // Retry-After is whole seconds; 0 is meaningless
		}
	}
	return d
}

// secondsUntil returns ceil(deficit/rate) in whole seconds, clamped to >= 0. rate
// is always > 0 here (limits are normalized before use), so there is no division by
// zero; the guard is defensive.
func secondsUntil(deficit, rate float64) int {
	if deficit <= 0 {
		return 0
	}
	if rate <= 0 {
		return 1 // defensive (unreachable: limits are normalized to Rate>0 before use)
	}
	return int(math.Ceil(deficit / rate))
}

func (l *Limiter) shardIndex(identity string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(identity))
	return int(h.Sum32() % uint32(len(l.shards)))
}

// sweepLocked evicts buckets idle longer than idleTTL, at most once per idleTTL per
// shard (so take stays O(1) amortized). Eviction is safe: an idle-for-idleTTL bucket
// has refilled to full (idleTTL >= burst/rate by construction), so dropping it is
// equivalent to recreating a fresh full bucket — no penalty is reset early. Caller
// holds sh.mu.
func (l *Limiter) sweepLocked(sh *shard, now time.Time) {
	if now.Sub(sh.lastSweep) < l.idleTTL {
		return
	}
	sh.lastSweep = now
	for k, b := range sh.buckets {
		if now.Sub(b.last) > l.idleTTL {
			delete(sh.buckets, k)
			atomic.AddInt64(&l.active, -1)
		}
	}
}

// ActiveBuckets returns the current live bucket count (test/observability helper).
func (l *Limiter) ActiveBuckets() int64 { return atomic.LoadInt64(&l.active) }

// writeActiveBuckets emits the active-bucket gauge at scrape time as a complete,
// well-formed Prometheus family (the RegisterFunc contract). It reads an atomic
// counter (no shard lock), so a scrape never contends with the hot take path.
func (l *Limiter) writeActiveBuckets(w io.Writer) {
	const name = "olivares_http_ratelimit_active_buckets"
	fmt.Fprintf(w, "# HELP %s Live rate-limit token buckets currently held in memory.\n# TYPE %s gauge\n%s %d\n",
		name, name, name, atomic.LoadInt64(&l.active))
}
