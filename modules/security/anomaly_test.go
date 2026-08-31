// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

import (
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// TestAntiEvasionCorrelation verifies the anti-evasion join (docs/SECURITY-HARDENING.md): when
// BOTH the kernel-side (subject identity) and cooperative-side (subject session)
// anti_evasion marks arrive, the module raises a single correlated, prioritized
// anomaly that surfaces in the anomaly queue.
func TestAntiEvasionCorrelation(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	// Kernel side (eBPF backstop marks the workload identity).
	h.publishFinding(tenant, antiEvasionKind, sdkmodel.SeverityLow, "identity", "container:abc/node",
		"agent workload active at the kernel without observed cooperative telemetry")
	// Cooperative side (watchdog marks the silent session).
	h.publishFinding(tenant, antiEvasionKind, sdkmodel.SeverityHigh, "session", "sess-42",
		"Claude Code OTEL telemetry went silent while hooks remained active")

	if !h.waitForFinding(busEvasionCorrelated) {
		t.Fatalf("expected a correlated anti-evasion finding on the bus")
	}

	// The correlated anomaly is in the prioritized queue.
	r := h.do("GET", "/v1/m/security/anomalies", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("anomalies = %d %s", r.code, r.raw)
	}
	items, _ := r.body["items"].([]any)
	found := false
	for _, it := range items {
		m := it.(map[string]any)
		if m["kind"] == sourceEvasionCorrelated {
			found = true
			if p, _ := m["priority"].(float64); p < 70 {
				t.Fatalf("correlated anomaly priority too low: %v", p)
			}
		}
	}
	if !found {
		t.Fatalf("correlated anti-evasion anomaly not in the queue; items=%v", items)
	}
}

// TestManagedAgentHITLQueuePersists (ANT2-14) verifies the managed-agents HITL
// carve-out: a LOW-severity governance finding on subject anthropic.managed_agent — the
// connector's "tool call awaiting human confirmation" — is persisted with its ORIGINAL
// kind so the console queue (GET /findings?kind=governance&subject_kind=
// anthropic.managed_agent) lists it, and an at-least-once redelivery does not duplicate
// the queue row. Without the carve-out the HIGH+ rule would keep the queue empty.
func TestManagedAgentHITLQueuePersists(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	listQueue := func() []any {
		r := h.do("GET", "/v1/m/security/findings?kind=governance&subject_kind=anthropic.managed_agent",
			admin, nil, tenantHdr(tenant))
		if r.code != http.StatusOK {
			t.Fatalf("findings = %d %s", r.code, r.raw)
		}
		items, _ := r.body["items"].([]any)
		return items
	}

	emit := func(subjectRef string) {
		h.publishFinding(tenant, "governance", sdkmodel.SeverityLow, "anthropic.managed_agent", subjectRef,
			"CMA tool call awaiting human confirmation (always_ask / HITL)")
	}
	emit("sesn_42")
	var items []any
	for i := 0; i < 200; i++ {
		if items = listQueue(); len(items) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(items) != 1 {
		t.Fatalf("HITL queue = %d rows, want 1 (the carve-out must persist Low governance findings)", len(items))
	}
	row := items[0].(map[string]any)
	if row["kind"] != "governance" || row["subject_kind"] != "anthropic.managed_agent" {
		t.Fatalf("queue row shape wrong: %v", row)
	}

	// At-least-once redelivery of the SAME pending fact must not multiply the queue.
	// Bus delivery is async but per-subscriber FIFO, so a DISTINCT sentinel published
	// AFTER the duplicate proves the duplicate was fully processed once the sentinel
	// row appears — a fixed sleep would pass vacuously if the redelivery were merely
	// slow rather than deduplicated.
	emit("sesn_42")
	emit("sesn_43") // sentinel: a different pending fact, same queue family
	for i := 0; i < 200; i++ {
		if items = listQueue(); len(items) >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(items) != 2 {
		t.Fatalf("HITL queue after redelivery+sentinel = %d rows, want 2 (dedup on the deterministic detail hash)", len(items))
	}
}

// TestDreamAdmissionFindingReachesHITLQueue verifies the SECOND queue family of
// the carve-out: a Dreams output store awaiting HITL admission — kind=governance with
// subject anthropic.memory_store at Medium — must persist so the admission queue can
// list it BEFORE the gate fails (without this, the only persisted dream signal would be
// the HIGH unadmitted-attach drift, i.e. after the fact).
func TestDreamAdmissionFindingReachesHITLQueue(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	listQueue := func() []any {
		r := h.do("GET", "/v1/m/security/findings?kind=governance&subject_kind=anthropic.memory_store",
			admin, nil, tenantHdr(tenant))
		if r.code != http.StatusOK {
			t.Fatalf("findings = %d %s", r.code, r.raw)
		}
		items, _ := r.body["items"].([]any)
		return items
	}

	emit := func() {
		h.publishFinding(tenant, "governance", sdkmodel.SeverityMedium, "anthropic.memory_store", "memstore_out",
			"CMA dream produced an output memory store awaiting HITL admission — do not attach to productive sessions")
	}
	emit()
	var items []any
	for i := 0; i < 200; i++ {
		if items = listQueue(); len(items) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(items) != 1 {
		t.Fatalf("dream admission queue = %d rows, want 1", len(items))
	}
	row := items[0].(map[string]any)
	if row["subject_kind"] != "anthropic.memory_store" || row["kind"] != "governance" {
		t.Fatalf("queue row shape wrong: %v", row)
	}

	// Redelivery dedups within the memory_store partition (sentinel pattern as above).
	emit()
	h.publishFinding(tenant, "governance", sdkmodel.SeverityMedium, "anthropic.memory_store", "memstore_other",
		"CMA dream produced an output memory store awaiting HITL admission — do not attach to productive sessions")
	for i := 0; i < 200; i++ {
		if items = listQueue(); len(items) >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(items) != 2 {
		t.Fatalf("dream admission queue after redelivery+sentinel = %d rows, want 2", len(items))
	}
}

// TestAnomaliesRequireReadTier verifies the anomaly queue is gated and self-audited
// (a privileged read). A viewer CAN read it (read tier); an unauthenticated request
// cannot.
func TestAnomaliesRequireReadTier(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "v@x.io", "viewer")
	if r := h.do("GET", "/v1/m/security/anomalies", viewer, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("viewer anomalies = %d, want 200", r.code)
	}
	if r := h.do("GET", "/v1/m/security/anomalies", "", nil, tenantHdr(tenant)); r.code == http.StatusOK {
		t.Fatalf("unauthenticated anomalies = 200, want denied")
	}
}

func TestIsExternalEgressIPv6HostParsing(t *testing.T) {
	tests := []struct {
		name string
		uri  string
		want bool
	}{
		{name: "private bare compressed", uri: "tcp://fd00::1", want: false},
		{name: "private bracketed port", uri: "tcp://[fd00::1]:443", want: false},
		{name: "public bracketed port", uri: "tcp://[2001:db8::1]:443", want: true},
		{name: "link local zone", uri: "udp://fe80::1%eth0", want: false},
		{name: "v4 mapped public", uri: "tcp://::ffff:192.0.2.1", want: true},
		{name: "public ipv6", uri: "tcp://2001:4860:4860::8888", want: true},
		{name: "private ipv4 port unchanged", uri: "tcp://10.1.2.3:443", want: false},
		{name: "public hostname unchanged", uri: "tcp://example.com:443", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isExternalEgress(model.Resource{Kind: "net", URI: tt.uri})
			if got != tt.want {
				t.Fatalf("isExternalEgress(%q) = %v, want %v", tt.uri, got, tt.want)
			}
		})
	}
}
