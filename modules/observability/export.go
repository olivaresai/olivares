// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package observability

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
)

// OTLP/JSON export types (OpenTelemetry Protocol JSON encoding).
// These mirror the proto3 JSON mapping defined in
// opentelemetry-proto/opentelemetry/proto/trace/v1/trace.proto,
// scoped to the fields the ledger read-model can honestly populate.

type otlpExport struct {
	ResourceSpans []otlpResourceSpan `json:"resourceSpans"`
}

type otlpResourceSpan struct {
	Resource   otlpResource    `json:"resource"`
	ScopeSpans []otlpScopeSpan `json:"scopeSpans"`
}

type otlpResource struct {
	Attributes []otlpAttribute `json:"attributes"`
}

type otlpScopeSpan struct {
	Scope otlpScope  `json:"scope"`
	Spans []otlpSpan `json:"spans"`
}

type otlpScope struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

type otlpSpan struct {
	TraceID           string          `json:"traceId"`
	SpanID            string          `json:"spanId"`
	Name              string          `json:"name"`
	Kind              int             `json:"kind"`
	StartTimeUnixNano string          `json:"startTimeUnixNano"`
	EndTimeUnixNano   string          `json:"endTimeUnixNano"`
	Attributes        []otlpAttribute `json:"attributes,omitempty"`
	Status            otlpStatus      `json:"status"`
}

type otlpAttribute struct {
	Key   string       `json:"key"`
	Value otlpAnyValue `json:"value"`
}

type otlpAnyValue struct {
	StringValue string `json:"stringValue,omitempty"`
	IntValue    string `json:"intValue,omitempty"`
}

type otlpStatus struct {
	Code int `json:"code"`
}

// OTLP SpanKind constants (proto3 enum).
const (
	otlpSpanKindUnspecified = 0
	otlpSpanKindInternal    = 1
	otlpSpanKindServer      = 2
	otlpSpanKindClient      = 3
)

func stringAttr(key, val string) otlpAttribute {
	return otlpAttribute{Key: key, Value: otlpAnyValue{StringValue: val}}
}

func intAttr(key string, val int64) otlpAttribute {
	return otlpAttribute{Key: key, Value: otlpAnyValue{IntValue: strconv.FormatInt(val, 10)}}
}

// handleExportTrace exports one trace as OTLP-compatible JSON so the
// operator can import it into Jaeger, Grafana Tempo, Datadog, or any
// OTLP-aware tool. The export is HONEST: span kind is INTERNAL (the
// ledger has no real OTel kind), durations are ledger-event windows, and
// timestamps are derived from event OccurredAt — never fabricated. The
// Content-Disposition header suggests a filename for browser downloads.
func (m *Module) handleExportTrace(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := chi.URLParam(r, "id")
	if id == "" || len(id) > 64 || !isLowerHexLoose(id) {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid trace id"))
		return
	}

	var events []ledgerTraceEvent
	walked, err := m.walkTraceWindow(r, mc, func(traceID string, ev ledgerTraceEvent) {
		if traceID == id {
			events = append(events, ev)
		}
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !walked {
		writeJSON(w, http.StatusNotFound,
			errorBody("trace correlation unavailable: the ledger exposes no canonical meta"))
		return
	}
	if len(events) == 0 {
		writeJSON(w, http.StatusNotFound,
			errorBody(fmt.Sprintf("trace not found in the ledger window (last %d events)", traceWalkCap)))
		return
	}

	traceStart, _ := windowOf(events)
	spans := m.buildSpans(events)

	otlpSpans := make([]otlpSpan, 0, len(spans))
	for _, s := range spans {
		startNano := traceStart.Add(time.Duration(s.StartMS) * time.Millisecond)
		endNano := startNano.Add(time.Duration(s.DurationMS) * time.Millisecond)

		// Product attribute keys live under the reserved reverse-DNS namespace
		// ai.olivares.* (freeze — the bare olivares.* spelling, read as
		// reverse DNS, claims a TLD the product does not own).
		attrs := make([]otlpAttribute, 0, len(s.Attributes)+3)
		if s.Actor != "" {
			attrs = append(attrs, stringAttr("ai.olivares.actor", s.Actor))
		}
		if s.ActorKind != "" {
			attrs = append(attrs, stringAttr("ai.olivares.actor_kind", s.ActorKind))
		}
		if s.EntityRef != "" {
			attrs = append(attrs, stringAttr("ai.olivares.entity_ref", s.EntityRef))
		}
		// Sorted, because Go map iteration is randomized: two downloads of the
		// SAME trace must not differ byte-wise in attribute order.
		keys := make([]string, 0, len(s.Attributes))
		for k := range s.Attributes {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			attrs = append(attrs, stringAttr(k, s.Attributes[k]))
		}

		otlpSpans = append(otlpSpans, otlpSpan{
			TraceID:           id,
			SpanID:            s.SpanID,
			Name:              s.Name,
			Kind:              otlpSpanKindInternal,
			StartTimeUnixNano: strconv.FormatInt(startNano.UnixNano(), 10),
			EndTimeUnixNano:   strconv.FormatInt(endNano.UnixNano(), 10),
			Attributes:        attrs,
			Status:            otlpStatus{Code: otlpSpanKindUnspecified},
		})
	}

	export := otlpExport{
		ResourceSpans: []otlpResourceSpan{{
			Resource: otlpResource{
				Attributes: []otlpAttribute{
					stringAttr("service.name", traceServiceName),
					stringAttr("telemetry.sdk.name", "olivares"),
					stringAttr("ai.olivares.source", "ledger"),
				},
			},
			ScopeSpans: []otlpScopeSpan{{
				Scope: otlpScope{Name: Name},
				Spans: otlpSpans,
			}},
		}},
	}

	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="trace-%s.json"`, clampStr(id, 16)))
	writeJSON(w, http.StatusOK, export)
}
