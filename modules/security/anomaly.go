// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// antiEvasionKind is the finding Kind the eBPF backstop (and the cooperative
// watchdog) raise when an agent workload is active without observed telemetry
// (docs/SECURITY-HARDENING.md). The module joins the two sides into a correlated anomaly.
const antiEvasionKind = "anti_evasion"

// sourceAntiEvasion / sourceEvasionCorrelated mark the persisted security findings
// that mirror the mark and the module's correlation of its two sides.
const (
	sourceAntiEvasion       = "anti_evasion"
	sourceEvasionCorrelated = "anti_evasion_correlated"
)

// correlationWindow bounds how far apart the kernel-side and cooperative-side
// anti_evasion marks may be and still be treated as the same evasion event.
const correlationWindow = 30 * time.Minute

// onEvent is the anomaly reactor. It persists the estate's security-relevant
// findings into the tenant's security view and joins the two sides of the
// anti_evasion mark. It returns the handler error so a transient store failure is
// visible (the bus redelivers; the writes are idempotent enough that a retry is
// safe). It NEVER re-ingests the module's own emissions (guards on e.Source) so a
// guardrail finding the module published does not loop back as a new anomaly.
func (m *Module) onEvent(ctx context.Context, e event.Event) error {
	if m.data == nil || e.Source == Name {
		return nil
	}
	f, ok := event.FindingOf(e)
	if !ok {
		return nil
	}
	tenant := model.TenantID(e.Tenant)
	if tenant.IsZero() || tenant.IsSystem() {
		return nil // anomalies are tenant-scoped; never the system partition
	}
	switch {
	case f.Kind == antiEvasionKind:
		return m.ingestAntiEvasion(ctx, tenant, f)
	case f.Kind == hitlQueueKind && (f.SubjectKind == hitlQueueSubjectKind || f.SubjectKind == hitlQueueSubjectKindMemStore):
		return m.ingestManagedAgentHITL(ctx, tenant, e.Source, f)
	case f.SubjectKind == localResidencySubjectKind:
		// BEFORE the HIGH+ arm on purpose: that arm rewrites kind to `anomaly` and
		// source to the finding's own kind, which would erase both the connector's
		// classification and its origin. A residency row must reach the view as what
		// the connector said it was.
		return m.ingestLocalResidency(ctx, tenant, e.Source, f)
	case f.Kind == findingKindSafetyPosture:
		// Provider AI-safety posture (OpenAI Moderation, Bedrock Guardrails, Azure RAI —
		//). Persist at ANY severity (a posture finding is usually Info/Low/Medium,
		// so the HIGH+ rule below would drop it), with the same bounded dedup as the HITL
		// queue: connectors re-emit the posture each pass, and a state-deterministic
		// DetailHash means an unchanged posture must not multiply in the view.
		return m.ingestSafetyPosture(ctx, tenant, e.Source, f)
	case f.Severity.AtLeast(sdkmodel.SeverityHigh):
		// Persist other modules' HIGH+ findings into the security view so the
		// console and forensics see cross-module high signals in one place. (The
		// module is the first/only persister of findings —.)
		return m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
			_, err := m.persistFinding(ctx, sc, finding{
				kind: findingKindAnomaly, severity: f.Severity, source: f.Kind,
				subjectKind: f.SubjectKind, subjectRef: f.SubjectRef, title: f.Title,
				detail: f.Kind + "|" + f.SubjectRef + "|" + f.DetailHash,
				meta:   map[string]any{"origin": e.Source, "source_detail_hash": f.DetailHash},
			})
			return err
		})
	default:
		return nil
	}
}

// ANT2-14: the managed-agents HITL queue. The connectors translate a session
// paused on an always_ask permission policy (stop_reason requires_action) — subject
// anthropic.managed_agent — and a Dreams output store awaiting/recording its HITL
// admission — subject anthropic.memory_store — into governance findings; the HITL
// console queue lists them via GET /findings?kind=governance&subject_kind=... (web
// claude-policy api.ts). They ride Low/Medium/Info severity, so without this carve-out
// the HIGH+ persistence rule above would keep the queue permanently empty. The values
// agree with the Apache connectors by VALUE (license boundary; no shared import).
const (
	hitlQueueKind                = "governance"
	hitlQueueSubjectKind         = "anthropic.managed_agent"
	hitlQueueSubjectKindMemStore = "anthropic.memory_store"
)

// hitlQueueScanLimit bounds the dedup scan over the most recent queue rows.
const hitlQueueScanLimit = 256

// safetyPostureScanLimit bounds the dedup scan for provider safety-posture findings
//. Like the HITL queue, it scans the NEWEST rows of the incoming subject kind;
// a tenant with more than this many distinct safety subjects could re-insert a stale
// duplicate beyond the window (the same documented bound as recentAnomalies).
const safetyPostureScanLimit = 256

// localResidencySubjectKind is the subject the local-inference connector emits one
// finding per, for each model the local server is holding in memory RIGHT NOW — a
// different assertion from the catalog's "this model is installed". The connector sets
// the severity FROM THE PLACEMENT: fully in VRAM is Info, CPU-only or split GPU/CPU is
// Medium, because a split model is latency the operator pays without being told.
//
// WHY IT NEEDS A CARVE-OUT, and this is the whole reason the row was invisible: the
// switch above persists `safety_posture` at any severity and everything else only at
// HIGH+. A residency row is Info or Medium and its kind is `posture`, not
// `safety_posture` — so it fell through to `default: return nil` and was DROPPED
// before any surface could show it. Painting it in the console was necessary
// and, on its own, inert: the console can only render rows that were persisted.
//
// DELIBERATELY NARROW. 85 sites across the connector tree emit `Kind: "posture"` and
// 56 of them are below HIGH, so admitting the whole family here would change what
// GET /findings contains for every existing deployment — a volume and retention
// decision that belongs to this module's owner, not to a console session. This case
// admits ONE subject kind, keeps the connector's own kind and severity, and dedups
// per MODEL so a re-observed unchanged placement does not multiply.
const localResidencySubjectKind = "local.residency"

// localResidencyScanLimit bounds the dedup scan over the NEWEST rows of this subject
// kind, exactly like the safety-posture and HITL bounds, and carries the same
// documented caveat: a tenant whose residency history exceeds this window could
// re-insert a duplicate of a placement that scrolled out of it. A host holds a handful
// of models resident at once, so the window covers many polls of a real estate.
const localResidencyScanLimit = 256

// ingestManagedAgentHITL persists a managed-agent HITL-queue finding with its ORIGINAL
// kind (the queue's list filter keys on it), deduplicating on the deterministic detail
// hash: the bus is at-least-once and a paused session is re-observed per delivery, so
// an identical pending row must not multiply in the queue. The scan is bounded to the
// NEWEST rows (occurred_at desc — a default id-ordered page would return the OLDEST
// rows and the dedup would silently die once a tenant outgrows one page; same trap
// recentAnomalies documents) and filtered to the incoming subject kind so each queue
// family dedups within its own partition.
func (m *Module) ingestManagedAgentHITL(ctx context.Context, tenant model.TenantID, origin string, f sdkmodel.FindingReport) error {
	detail := f.Kind + "|" + f.SubjectRef + "|" + f.DetailHash
	want := hashBytes(detail)
	return m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		existing, _, err := sc.Findings().List(ctx, model.Query{
			Filters: []model.Filter{
				{Column: "kind", Op: model.OpEq, Value: hitlQueueKind},
				{Column: "subject_kind", Op: model.OpEq, Value: f.SubjectKind},
			},
			Sort:  []model.Sort{{Column: "occurred_at", Desc: true}},
			Limit: hitlQueueScanLimit,
		})
		if err != nil {
			return err
		}
		for _, row := range existing {
			if bytes.Equal(row.DetailHash, want) {
				return nil // the same pending fact is already queued
			}
		}
		_, err = m.persistFinding(ctx, sc, finding{
			kind: f.Kind, severity: f.Severity, source: origin,
			subjectKind: f.SubjectKind, subjectRef: f.SubjectRef, title: f.Title,
			detail: detail,
			meta:   map[string]any{"origin": origin, "source_detail_hash": f.DetailHash},
		})
		return err
	})
}

// ingestSafetyPosture persists a provider safety-posture finding into the
// security view with its ORIGINAL kind ("safety_posture", so the GET /findings and
// GET /safety-posture filters key on it), deduplicating on the deterministic detail
// hash. The connectors re-emit the posture on every Gather, and the DetailHash is a
// function of the config STATE (no timestamp), so an unchanged posture must not
// multiply; a real change (a guardrail loses its prompt-attack filter, an RAI policy
// is weakened, moderation usage starts/stops) carries a fresh hash and persists as a
// new row. The scan is bounded to the newest rows of the incoming subject kind and
// matches the incoming subject kind, so each provider surface dedups within its own
// partition (same trap recentAnomalies/ingestManagedAgentHITL document).
func (m *Module) ingestSafetyPosture(ctx context.Context, tenant model.TenantID, origin string, f sdkmodel.FindingReport) error {
	detail := f.Kind + "|" + f.SubjectKind + "|" + f.SubjectRef + "|" + f.DetailHash
	want := hashBytes(detail)
	return m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		existing, _, err := sc.Findings().List(ctx, model.Query{
			Filters: []model.Filter{
				{Column: "kind", Op: model.OpEq, Value: findingKindSafetyPosture},
				{Column: "subject_kind", Op: model.OpEq, Value: f.SubjectKind},
			},
			Sort:  []model.Sort{{Column: "occurred_at", Desc: true}},
			Limit: safetyPostureScanLimit,
		})
		if err != nil {
			return err
		}
		for _, row := range existing {
			if bytes.Equal(row.DetailHash, want) {
				return nil // the same posture state is already recorded
			}
		}
		_, err = m.persistFinding(ctx, sc, finding{
			kind: f.Kind, severity: f.Severity, source: origin,
			subjectKind: f.SubjectKind, subjectRef: f.SubjectRef, title: f.Title,
			detail: detail,
			meta:   map[string]any{"origin": origin, "source_detail_hash": f.DetailHash, "provider_surface": f.SubjectKind},
		})
		return err
	})
}

// ingestLocalResidency persists one local-inference residency posture per resident
// model, KEEPING the connector's own kind and severity — the severity IS the placement
// and re-deriving it here would be a second opinion about a fact only the connector
// measured.
//
// The dedup scans this model's newest rows and compares the connector's deterministic
// DetailHash, which commits the placement and the VRAM/total split: an unchanged model
// re-observed on the next poll matches and writes nothing, while the same model moving
// from gpu to split gpu/cpu hashes differently and lands as a new row. That is the
// behavior an operator needs — the transition is the event, not the steady state.
func (m *Module) ingestLocalResidency(ctx context.Context, tenant model.TenantID, origin string, f sdkmodel.FindingReport) error {
	detail := f.Kind + "|" + f.SubjectKind + "|" + f.SubjectRef + "|" + f.DetailHash
	want := hashBytes(detail)
	return m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		// Scanned by subject_kind ONLY. There is no subject_ref column — the ref is
		// carried in metadata (findings.go:65) — so a per-model filter is not something
		// the store can do, and asking for one silently fails the whole write. The model
		// identity is not lost, though: the DetailHash below commits SubjectRef, so two
		// different models never collide and the same model in the same placement always
		// matches.
		existing, _, err := sc.Findings().List(ctx, model.Query{
			Filters: []model.Filter{
				{Column: "subject_kind", Op: model.OpEq, Value: f.SubjectKind},
			},
			Sort:  []model.Sort{{Column: "occurred_at", Desc: true}},
			Limit: localResidencyScanLimit,
		})
		if err != nil {
			return err
		}
		for _, row := range existing {
			if bytes.Equal(row.DetailHash, want) {
				return nil // this model is already recorded in this placement
			}
		}
		_, err = m.persistFinding(ctx, sc, finding{
			kind: f.Kind, severity: f.Severity, source: origin,
			subjectKind: f.SubjectKind, subjectRef: f.SubjectRef, title: f.Title,
			detail: detail,
			meta:   map[string]any{"origin": origin, "source_detail_hash": f.DetailHash},
		})
		return err
	})
}

// ingestAntiEvasion persists an incoming anti_evasion mark and, if BOTH the
// kernel-side (subject kind "identity") and the cooperative-side (subject kind
// "session") marks are present within the correlation window, raises a single
// correlated, prioritized anomaly (docs/SECURITY-HARDENING.md — "Joins the two for forensics").
func (m *Module) ingestAntiEvasion(ctx context.Context, tenant model.TenantID, f sdkmodel.FindingReport) error {
	var emitCorrelated bool
	var corrSubjectKind, corrSubjectRef string
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		if _, err := m.persistFinding(ctx, sc, finding{
			kind: findingKindAnomaly, severity: f.Severity, source: sourceAntiEvasion,
			subjectKind: f.SubjectKind, subjectRef: f.SubjectRef, title: f.Title,
			detail: antiEvasionKind + "|" + f.SubjectKind + "|" + f.SubjectRef,
			meta:   map[string]any{"side": antiEvasionSide(f.SubjectKind), "source_detail_hash": f.DetailHash},
		}); err != nil {
			return err
		}
		// Look at the recent anti_evasion findings to decide whether both sides are
		// now present and we have not already raised a correlation.
		recent, err := recentAnomalies(ctx, sc, m.clock.Now())
		if err != nil {
			return err
		}
		kernel, coop, alreadyCorrelated := false, false, false
		for _, fr := range recent {
			switch fr.Source {
			case sourceAntiEvasion:
				switch antiEvasionSide(fr.SubjectKind) {
				case "kernel":
					kernel = true
				case "cooperative":
					coop = true
				}
			case sourceEvasionCorrelated:
				alreadyCorrelated = true
			}
		}
		if kernel && coop && !alreadyCorrelated {
			emitCorrelated = true
			corrSubjectKind, corrSubjectRef = "identity", f.SubjectRef
			_, err = m.persistFinding(ctx, sc, finding{
				kind: findingKindAnomaly, severity: sdkmodel.SeverityHigh, source: sourceEvasionCorrelated,
				subjectKind: corrSubjectKind, subjectRef: corrSubjectRef,
				title:  "Possible telemetry evasion: kernel activity without cooperative telemetry",
				detail: sourceEvasionCorrelated + "|" + f.SubjectRef,
				meta:   map[string]any{"kernel": kernel, "cooperative": coop},
			})
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	if emitCorrelated {
		m.emitFinding(ctx, tenant, busEvasionCorrelated, sdkmodel.SeverityHigh, corrSubjectKind, corrSubjectRef,
			"Possible telemetry evasion correlated across kernel + cooperative signals", sourceEvasionCorrelated+"|"+corrSubjectRef)
	}
	return nil
}

// antiEvasionSide classifies an anti_evasion mark by its subject kind: the eBPF
// backstop marks the workload IDENTITY (kernel side); the cooperative watchdog
// marks the SESSION whose telemetry went silent (cooperative side).
func antiEvasionSide(subjectKind string) string {
	switch subjectKind {
	case "identity":
		return "kernel"
	case "session":
		return "cooperative"
	default:
		return "unknown"
	}
}

// recentAnomalies returns the tenant's anomaly findings within the correlation
// window of now. The window is bounded at the STORE (occurred_at >= cutoff) and
// ordered newest-first, so a tenant with many historical anomaly findings cannot
// push the just-inserted marks past the page limit — a default id-ordered page
// returns the OLDEST rows, which would silently break correlation once a tenant
// exceeds one page of anomaly findings.
func recentAnomalies(ctx context.Context, sc store.Scope, now model.Timestamp) ([]model.Finding, error) {
	cutoff := model.NewTimestamp(now.Time().Add(-correlationWindow)).String()
	all, _, err := sc.Findings().List(ctx, model.Query{
		Filters: []model.Filter{eq("kind", findingKindAnomaly), {Column: "occurred_at", Op: model.OpGte, Value: cutoff}},
		Sort:    []model.Sort{{Column: "occurred_at", Desc: true}},
		Limit:   listCap,
	})
	if err != nil {
		return nil, err
	}
	return all, nil
}

// ---- on-demand anomaly view -----------------------------------------------------

// anomalyDTO is one prioritized anomaly for the security console (the contract).
type anomalyDTO struct {
	Kind        string         `json:"kind"`
	Severity    string         `json:"severity"`
	Priority    int            `json:"priority"` // 0..100, higher = more urgent
	SubjectKind string         `json:"subject_kind"`
	SubjectRef  string         `json:"subject_ref"`
	Title       string         `json:"title"`
	Confidence  string         `json:"confidence,omitempty"`
	Source      string         `json:"source"`
	OccurredAt  string         `json:"occurred_at,omitempty"`
	Evidence    map[string]any `json:"evidence,omitempty"`
}

// handleAnomalies computes the tenant's prioritized anomalies on demand: the
// permitted-vs-observed drift (consumed via the store, not
// recomputed), egress/exfil-relevant drift labeled from the resource, and the
// correlated/high security findings (incl. the joined anti_evasion mark). It is a
// PRIVILEGED, self-audited read (docs/SECURITY-HARDENING.md, §4): the audit append and the reads
// share one transaction.
func (m *Module) handleAnomalies(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	out := listResponse[anomalyDTO]{Items: []anomalyDTO{}}
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		if err := auditEvent(r.Context(), sc, mc, "security.anomalies.read", caseKind, "", nil); err != nil {
			return err
		}
		// 1) Drift: observed accesses that are not permitted. The store-level
		// Drift is the raw signal; the reconciled, agent↔identity view is access-map's
		// /drift — these are flagged confidence=approximate so a false drift is never
		// headlined as a firm violation (docs/SECURITY-HARDENING.md).
		drifts, err := sc.AccessEdges().Drift(r.Context(), model.Query{Limit: listCap})
		if err != nil {
			return err
		}
		for _, d := range drifts {
			if d.Kind != model.DriftViolation {
				continue
			}
			res, _ := resolveResource(r.Context(), sc, d.Edge.ResourceID)
			a := driftAnomaly(d.Edge, res)
			out.Items = append(out.Items, a)
		}
		// 2) Security findings: the correlated anti_evasion mark and any HIGH+
		// persisted anomaly/guardrail findings still open.
		finds, _, err := sc.Findings().List(r.Context(), model.Query{Limit: listCap})
		if err != nil {
			return err
		}
		for _, f := range finds {
			if f.Status != model.FindingOpen {
				continue
			}
			if f.Source == sourceEvasionCorrelated || coreAtLeast(f.Severity, model.SeverityHigh) {
				out.Items = append(out.Items, findingAnomaly(f))
			}
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	sortAnomalies(out.Items)
	writeJSON(w, http.StatusOK, out)
}

// resolveResource loads a resource, tolerating not-found (the edge may reference a
// resource not yet inventoried).
func resolveResource(ctx context.Context, sc store.Scope, id model.ID) (model.Resource, bool) {
	if id.IsZero() {
		return model.Resource{}, false
	}
	res, err := sc.Resources().Get(ctx, id)
	if err != nil {
		return model.Resource{}, false
	}
	return res, true
}

// driftAnomaly builds an anomaly from an unexpected-access drift edge, labeling
// egress/exfil when the resource is a network endpoint to an external destination.
func driftAnomaly(edge model.AccessEdge, res model.Resource) anomalyDTO {
	kind := "access_drift"
	title := "Unexpected access: observed but not permitted"
	sev := model.SeverityMedium
	ev := map[string]any{
		"origin_kind": edge.OriginKind, "origin_id": edge.OriginID.String(),
		"resource_id": edge.ResourceID.String(), "mode": string(edge.Mode),
		"signal_source": string(edge.SignalSource), "occurrence_count": edge.OccurrenceCount,
		"reconciled": false,
	}
	if res.Kind != "" {
		ev["resource_kind"] = res.Kind
		ev["resource"] = res.Name
		if isExternalEgress(res) {
			kind = "egress_exfil_suspected"
			title = "Unexpected egress to an external endpoint"
			sev = model.SeverityHigh
		}
		if res.Sensitivity != "" {
			ev["sensitivity"] = res.Sensitivity
			if strings.EqualFold(res.Sensitivity, "high") || strings.EqualFold(res.Sensitivity, "secret") {
				sev = model.SeverityHigh
			}
		}
	}
	return anomalyDTO{
		Kind: kind, Severity: string(sev), Priority: priorityFor(sev, kind, string(sdkmodel.ConfidenceApproximate)),
		SubjectKind: edge.OriginKind, SubjectRef: edge.OriginID.String(), Title: title,
		Confidence: string(sdkmodel.ConfidenceApproximate), Source: "access_map_drift",
		OccurredAt: edge.LastSeen.String(), Evidence: ev,
	}
}

// isExternalEgress reports whether a resource is a network endpoint to a
// non-private, non-loopback destination (a potential exfil channel). It parses the
// "tcp://host:port" URI the eBPF connector emits; a private/loopback host is not
// external.
func isExternalEgress(res model.Resource) bool {
	if !strings.HasPrefix(res.Kind, "net") && !strings.HasPrefix(res.URI, "tcp://") && !strings.HasPrefix(res.URI, "udp://") {
		return false
	}
	host := res.URI
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return !isPrivateHost(host)
}

// isPrivateHost reports whether host is loopback, RFC1918/unique-local/link-local,
// or an obviously internal name. A literal IP is classified with the standard
// library; a hostname (SNI) that is not obviously internal is treated as external
// (conservative: surface it for review).
func isPrivateHost(host string) bool {
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return true
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		addr = addr.Unmap()
		return addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsUnspecified()
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
	}
	return false // a non-internal hostname is treated as external
}

// findingAnomaly projects a persisted security finding into the anomaly view.
func findingAnomaly(f model.Finding) anomalyDTO {
	kind := f.Source
	if kind == "" {
		kind = f.Kind
	}
	subjectRef := ""
	if f.Metadata != nil {
		if v, ok := f.Metadata["subject_ref"].(string); ok {
			subjectRef = v
		}
	}
	return anomalyDTO{
		Kind: kind, Severity: string(f.Severity), Priority: priorityFor(f.Severity, kind, ""),
		SubjectKind: f.SubjectKind, SubjectRef: subjectRef, Title: f.Title,
		Source: f.Source, OccurredAt: f.OccurredAt.String(),
		Evidence: map[string]any{"finding_id": f.ID.String(), "status": string(f.Status)},
	}
}

// priorityFor scores an anomaly 0..100 from its severity, kind and confidence so
// the console can sort the queue. The correlated anti_evasion mark is boosted (it
// is a strong, joined signal); an approximate (unreconciled) drift is discounted.
func priorityFor(sev model.Severity, kind, confidence string) int {
	base := map[model.Severity]int{
		model.SeverityCritical: 90, model.SeverityHigh: 70, model.SeverityMedium: 45, model.SeverityLow: 20,
	}[sev]
	if base == 0 {
		base = 20
	}
	if kind == sourceEvasionCorrelated {
		base += 10
	}
	if confidence == string(sdkmodel.ConfidenceApproximate) {
		base -= 15
	}
	if base < 0 {
		base = 0
	}
	if base > 100 {
		base = 100
	}
	return base
}

// sortAnomalies orders the queue by descending priority, then by recency.
func sortAnomalies(items []anomalyDTO) {
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && lessAnomaly(items[j-1], items[j]); j-- {
			items[j-1], items[j] = items[j], items[j-1]
		}
	}
}

func lessAnomaly(a, b anomalyDTO) bool {
	if a.Priority != b.Priority {
		return a.Priority < b.Priority
	}
	return a.OccurredAt < b.OccurredAt
}
