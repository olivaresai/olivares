// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package meshobs is the shared L7 service-mesh / API-gateway observation builder
// for the Olivares AI network connectors. It turns a
// transport-neutral L7 record — an Envoy ALS entry, an ext_authz/ext_proc check, a
// Cilium Hubble flow, an egress-proxy verdict, a gateway access log — into the
// sealed SDK observations (sdk/model), so every network connector classifies R/RW,
// confidence and verdict the SAME way.
//
// The mapping onto the sealed sum type (ARCHITECTURE.md, modules III and IX):
//   - an ALLOWED / forwarded access becomes an EdgeObservation — the OBSERVED side
//     of the permitted-vs-observed diff (module III);
//   - a DENIED access becomes a FindingReport — a policy denial / anti-evasion
//     signal (module IX): an agent that tried to reach a destination its egress
//     policy forbids (docs/SECURITY-HARDENING.md, §6). A denied access did NOT happen, so it is
//     NOT emitted as an edge.
//
// It is the L7 complement to the eBPF backstop. The kernel sees a 5-tuple+SNI
// it can only attribute to a process/cgroup (ConfidenceApproximate, no identity);
// the mesh sees the SAME egress with a CRYPTOGRAPHICALLY VERIFIED service identity
// (mTLS/SPIFFE → ConfidenceAttributed), an explicit FQDN and an allow/deny verdict.
// So an L7 edge CONFIRMS OR DENIES a kernel L4 edge and a declared permitted-path on
// the same resource key. OriginKind is "identity" — a workload/service identity,
// NEVER a resolved "agent": the identity→agent upgrade is the access-map module's
// job, exactly as the eBPF connector is careful not to overstate (ARCHITECTURE.md).
//
// Minimal data (docs/SECURITY-HARDENING.md): the FQDN, method, port and service identity are
// structural metadata, never payloads; a free-text deny reason is scrubbed for
// secrets (connectors/internal/redact) before it travels, and a finding's detail is
// reduced to a SHA-256 — the raw value never leaves. It imports only the SDK, the
// Apache internal helpers (redact, tracecontext) and the standard library, never
// the engine (LICENSING.md).
package meshobs

import (
	"context"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/connectors/internal/tracecontext"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// OriginKindIdentity is the OriginKind every mesh/gateway edge carries: the mesh
// attributes an access to a workload/service IDENTITY, never to a resolved agent
// (the identity→agent upgrade is job). It mirrors the eBPF connector,
// which is likewise careful not to claim "agent" from a non-human runtime identity.
const OriginKindIdentity = "identity"

// Resource kinds an L7 record maps to. An HTTP request is an "http.api" access
// keyed by its FQDN (the permitted-path unit); a non-HTTP L4 flow degrades to
// "net.endpoint" with the eBPF connector's "tcp://host:port" scheme so the access
// map can JOIN an L7 edge onto a kernel L4 edge on the same resource ref.
const (
	ResourceKindHTTPAPI     = "http.api"
	ResourceKindNetEndpoint = "net.endpoint"
)

// DefaultDenyKind is the FindingReport.Kind for an egress/policy denial.
const DefaultDenyKind = "egress_denied"

// Verdict is the mesh/gateway's allow/deny decision for an L7 access. It is what
// turns a record into either an observed edge (allowed) or a denial finding
// (denied). An empty verdict is treated as allowed/observed: a connector that only
// sees forwarded traffic (a plain access log) has nothing to deny.
type Verdict string

const (
	// VerdictUnknown is the zero value; a record with no explicit verdict is an
	// OBSERVED access (the connector saw it happen) and yields an edge.
	VerdictUnknown Verdict = ""
	// VerdictAllowed is an access the mesh/gateway forwarded — an observed edge.
	VerdictAllowed Verdict = "allowed"
	// VerdictDenied is an access the mesh/gateway blocked by policy — a denial
	// finding (anti-evasion / permitted-path violation), never an edge.
	VerdictDenied Verdict = "denied"
)

// Record is the transport-neutral L7 fact a network connector observed. A
// connector fills the fields it has; meshobs derives the SDK observations,
// classification, confidence and trace correlation from them. It carries only
// identifiers and structural metadata — never a request/response payload.
type Record struct {
	// OriginRef is the source workload/service identity: a SPIFFE ID
	// ("spiffe://…/sa/payments"), an Istio canonical service ("payments.default"),
	// or a verified mTLS peer principal. Empty becomes "unknown".
	OriginRef string
	// OriginVerified is true when OriginRef came from a CRYPTOGRAPHICALLY VERIFIED
	// peer identity (a completed mTLS handshake / a SPIFFE SVID the mesh validated),
	// which upgrades Confidence to Attributed. An identity taken from a spoofable
	// header, or absent, leaves it false → Approximate.
	OriginVerified bool

	// FQDN is the destination authority/host — the egress target and the
	// permitted-path unit (e.g. "api.anthropic.com"). It is lowercased on use. An
	// empty FQDN yields no observations (nothing to attribute the access to).
	FQDN string
	// Port is the destination port, or 0 if unknown.
	Port int

	// Method is the HTTP method, which drives the R/RW classification. Empty means a
	// non-HTTP L4 flow (classified ModeReadWrite, like a bidirectional socket).
	Method string

	// Verdict is the mesh/gateway's allow/deny decision (see Verdict).
	Verdict Verdict
	// DenyReason is a short, non-sensitive policy reason for a deny (e.g. "fqdn not
	// in egress allowlist"). It is scrubbed for secrets before use.
	DenyReason string
	// DenySeverity grades a deny finding; invalid/empty defaults to SeverityMedium.
	DenySeverity model.Severity
	// OWASPLLM/OWASPASI/ATLAS are optional taxonomy references for a deny finding
	//. A connector sets them only when it has a justified mapping; left nil
	// they are byte-identical to a pre-taxonomy finding (no fabrication).
	OWASPLLM, OWASPASI, ATLAS []string

	// Source is the calling connector's own provenance value (a package-local
	// model.SignalSource such as "hubble" or "envoy_als"), shown to the operator so
	// an L7 mesh edge and a kernel eBPF edge are never silently collapsed.
	Source model.SignalSource
	// Tool names the observing surface (e.g. "envoy.als", "hubble.flow",
	// "egress_proxy.verdict"); it rides ToolRef on an edge.
	Tool string

	// Trace is any W3C Trace Context the hop propagated; when present it
	// is handed to Correlator and never embedded in an observation (no DTO field for
	// it, and correlation is job).
	Trace tracecontext.TraceContext
	// Correlator receives the trace context for cross-hop correlation; nil uses the
	// deny-closed no-op default (declared, not yet correlated).
	Correlator tracecontext.Correlator

	// ObservedAt is when the access happened, in the connector's clock — the
	// natural-key timestamp consumers de-dupe re-emitted edges on.
	ObservedAt time.Time
}

// MethodToMode maps an HTTP method to the R/RW classification the access map uses
// (ARCHITECTURE.md). The classification is the L7 value-add over the eBPF backstop,
// which can only call a socket ModeReadWrite:
//   - a safe/idempotent read method (GET/HEAD/OPTIONS/TRACE) is ModeRead;
//   - a body-bearing method (POST/PUT/PATCH) sends data AND reads a response, so it
//     is ModeReadWrite;
//   - DELETE is ModeWrite; CONNECT tunnels bidirectionally, so ModeReadWrite;
//   - an empty method (a non-HTTP L4 flow) is ModeReadWrite (a socket is
//     bidirectional, matching the eBPF connector);
//   - any unrecognized verb is ModeUnknown — never guessed (ARCHITECTURE.md).
func MethodToMode(method string) model.AccessMode {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case "":
		return model.ModeReadWrite
	case "GET", "HEAD", "OPTIONS", "TRACE":
		return model.ModeRead
	case "POST", "PUT", "PATCH":
		return model.ModeReadWrite
	case "DELETE":
		return model.ModeWrite
	case "CONNECT":
		return model.ModeReadWrite
	default:
		return model.ModeUnknown
	}
}

// host returns the lowercased, trimmed FQDN.
func (r Record) host() string { return strings.ToLower(strings.TrimSpace(r.FQDN)) }

// origin returns the origin identity, or "unknown" when the mesh attributed none.
func (r Record) origin() string {
	if o := strings.TrimSpace(r.OriginRef); o != "" {
		return o
	}
	return "unknown"
}

// confidence is Attributed only for a cryptographically verified identity; an
// inferred or header-borne identity is Approximate (ARCHITECTURE.md).
func (r Record) confidence() model.Confidence {
	if r.OriginVerified {
		return model.ConfidenceAttributed
	}
	return model.ConfidenceApproximate
}

// resource returns the (kind, ref) for the access: an http.api keyed by FQDN for an
// HTTP request, else a net.endpoint in the eBPF "tcp://host:port" scheme so the two
// layers de-dup/join on the same resource ref.
func (r Record) resource() (kind, ref string) {
	h := r.host()
	if strings.TrimSpace(r.Method) != "" {
		return ResourceKindHTTPAPI, h
	}
	ep := h
	if r.Port > 0 {
		// net.JoinHostPort matches the eBPF connector verbatim (it brackets an
		// IPv6 host), so an L7 net.endpoint edge and a kernel L4 edge JOIN on the same
		// resource ref even for IPv6 destinations.
		ep = net.JoinHostPort(h, strconv.Itoa(r.Port))
	}
	return ResourceKindNetEndpoint, "tcp://" + ep
}

// Edge builds the EdgeObservation for an allowed/observed L7 access — the OBSERVED
// side of the permitted-vs-observed diff (module III).
func (r Record) Edge() model.EdgeObservation {
	kind, ref := r.resource()
	return model.EdgeObservation{
		OriginKind:   OriginKindIdentity,
		OriginRef:    r.origin(),
		ResourceKind: kind,
		ResourceRef:  ref,
		Mode:         MethodToMode(r.Method),
		Source:       r.Source,
		Confidence:   r.confidence(),
		ToolRef:      r.Tool,
		ObservedAt:   r.ObservedAt,
	}
}

// DenyFinding builds the FindingReport for a denied L7 access — a policy denial /
// anti-evasion signal (module IX). The raw deny reason is scrubbed for secrets and
// the detail is reduced to a SHA-256; only a short, non-sensitive title travels.
func (r Record) DenyFinding() model.FindingReport {
	sev := r.DenySeverity
	if !sev.Valid() {
		sev = model.SeverityMedium
	}
	h := r.host()
	origin := r.origin()
	detail := origin + " -> " + h
	if reason := strings.TrimSpace(r.DenyReason); reason != "" {
		detail += " (" + redact.Clean(reason) + ")"
	}
	return model.FindingReport{
		Kind:        DefaultDenyKind,
		Severity:    sev,
		SubjectKind: "net.egress",
		SubjectRef:  h,
		Title:       "egress denied: " + origin + " -> " + h,
		DetailHash:  redact.Hash(detail),
		OccurredAt:  r.ObservedAt,
		OWASPLLM:    r.OWASPLLM,
		OWASPASI:    r.OWASPASI,
		ATLAS:       r.ATLAS,
	}
}

// Observations returns the observations the record implies — an EdgeObservation for
// an allowed/forwarded access, a FindingReport for a denied one — and fires the
// deny-closed trace correlator when trace context is present. A record
// with no FQDN yields nothing (there is no resource to attribute the access to).
func (r Record) Observations() []model.Observation {
	if r.host() == "" {
		return nil
	}
	// Hand a WELL-FORMED traceparent to the deny-closed correlator.
	// ValidTraceParent gates out a malformed/garbage value so the (future) correlator
	// never receives an unparseable wire id — the validator's documented purpose.
	if r.Trace.Present() && tracecontext.ValidTraceParent(r.Trace.TraceParent) {
		c := r.Correlator
		if c == nil {
			c = tracecontext.NopCorrelator{}
		}
		c.Correlate(r.origin()+"->"+r.host(), r.Trace)
	}
	if r.Verdict == VerdictDenied {
		return []model.Observation{r.DenyFinding()}
	}
	return []model.Observation{r.Edge()}
}

// Emit builds the record's observations and emits each to sink, stopping on the
// first sink error. It is the convenience a connector's Gather loop calls per
// observed L7 record.
func (r Record) Emit(ctx context.Context, sink sdk.Sink) error {
	for _, obs := range r.Observations() {
		if err := sink.Emit(ctx, obs); err != nil {
			return err
		}
	}
	return nil
}
