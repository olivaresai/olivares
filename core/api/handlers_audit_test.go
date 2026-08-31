// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestAuditFilteredListMatchesAllFilterClasses(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "audit-filters")
	targets := []model.ID{model.NewID(), model.NewID(), model.NewID()}
	events := appendAuditDrafts(t, h, tenant,
		model.AuditDraft{
			Actor: "svc-alpha", ActorKind: model.ActorAgent, Action: "range.create",
			TargetKind: "core.agent", TargetID: targets[0],
		},
		model.AuditDraft{
			Actor: "BOB-Special", ActorKind: model.ActorUser, Action: "range.update.profile",
			TargetKind: "core.agent", TargetID: targets[1],
		},
		model.AuditDraft{
			Actor: "svc-charlie", ActorKind: model.ActorAgent, Action: "range.delete",
			TargetKind: "core.policy", TargetID: targets[2],
		},
	)
	from := strconv.FormatInt(events[0].Seq, 10)

	tests := []struct {
		name   string
		query  url.Values
		wanted []int64
	}{
		{
			name: "since inclusive",
			query: url.Values{
				"from":   {from},
				"since":  {events[0].OccurredAt.String()},
				"action": {"range."},
			},
			wanted: []int64{events[0].Seq, events[1].Seq, events[2].Seq},
		},
		{
			name: "since excludes older events",
			query: url.Values{
				"from": {from},
				"since": {
					events[2].OccurredAt.Time().Add(time.Hour).Format(time.RFC3339Nano),
				},
				"action": {"range."},
			},
			wanted: []int64{},
		},
		{
			name: "until inclusive",
			query: url.Values{
				"from":   {from},
				"until":  {events[2].OccurredAt.String()},
				"action": {"range."},
			},
			wanted: []int64{events[0].Seq, events[1].Seq, events[2].Seq},
		},
		{
			name: "until excludes newer events",
			query: url.Values{
				"from": {from},
				"until": {
					events[0].OccurredAt.Time().Add(-time.Hour).Format(time.RFC3339Nano),
				},
				"action": {"range."},
			},
			wanted: []int64{},
		},
		{
			name: "actor exact",
			query: url.Values{
				"from":  {from},
				"actor": {"BOB-Special"},
			},
			wanted: []int64{events[1].Seq},
		},
		{
			name: "action prefix",
			query: url.Values{
				"from":   {from},
				"action": {"range.update"},
			},
			wanted: []int64{events[1].Seq},
		},
		{
			name: "target kind exact",
			query: url.Values{
				"from":        {from},
				"target_kind": {"core.policy"},
			},
			wanted: []int64{events[2].Seq},
		},
		{
			name: "target id exact",
			query: url.Values{
				"from":      {from},
				"target_id": {targets[0].String()},
			},
			wanted: []int64{events[0].Seq},
		},
		{
			name: "q action case insensitive",
			query: url.Values{
				"from": {from},
				"q":    {"UPDATE.PRO"},
			},
			wanted: []int64{events[1].Seq},
		},
		{
			name: "q actor case insensitive",
			query: url.Values{
				"from": {from},
				"q":    {"bob-special"},
			},
			wanted: []int64{events[1].Seq},
		},
		{
			name: "q target kind case insensitive",
			query: url.Values{
				"from": {from},
				"q":    {"CORE.POL"},
			},
			wanted: []int64{events[2].Seq},
		},
		{
			name: "q target id case insensitive",
			query: url.Values{
				"from": {from},
				"q":    {strings.ToUpper(targets[0].String()[8:24])},
			},
			wanted: []int64{events[0].Seq},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := h.do("GET", "/v1/audit?"+tt.query.Encode(), admin, nil, tenantHdr(tenant))
			if r.code != http.StatusOK {
				t.Fatalf("filtered audit = %d %s", r.code, r.raw)
			}
			if got := auditResponseSeqs(t, r); !slices.Equal(got, tt.wanted) {
				t.Fatalf("sequences = %v, want %v", got, tt.wanted)
			}
			if complete, ok := r.body["scan_complete"].(bool); !ok || !complete {
				t.Fatalf("scan_complete = %#v, want true", r.body["scan_complete"])
			}
		})
	}
}

func TestAuditFilteredListContinuationUsesLastExaminedSequence(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "audit-continuation")
	events := appendAuditDrafts(t, h, tenant,
		model.AuditDraft{Actor: "continuation", Action: "wanted.one"},
		model.AuditDraft{Actor: "continuation", Action: "noise.one"},
		model.AuditDraft{Actor: "continuation", Action: "wanted.two"},
		model.AuditDraft{Actor: "continuation", Action: "noise.two"},
		model.AuditDraft{Actor: "continuation", Action: "wanted.three"},
	)

	firstQuery := url.Values{
		"from":   {strconv.FormatInt(events[0].Seq, 10)},
		"limit":  {"2"},
		"action": {"wanted."},
	}
	first := h.do("GET", "/v1/audit?"+firstQuery.Encode(), admin, nil, tenantHdr(tenant))
	if first.code != http.StatusOK {
		t.Fatalf("first page = %d %s", first.code, first.raw)
	}
	if got, want := auditResponseSeqs(t, first), []int64{events[0].Seq, events[2].Seq}; !slices.Equal(got, want) {
		t.Fatalf("first page sequences = %v, want %v", got, want)
	}
	next := auditResponseInt64(t, first, "next_from")
	if want := events[2].Seq + 1; next != want {
		t.Fatalf("next_from = %d, want last examined + 1 = %d", next, want)
	}
	if first.body["has_more"] != true || first.body["scan_complete"] != false {
		t.Fatalf("first page honesty fields = %#v", first.body)
	}

	secondQuery := url.Values{
		"from":   {strconv.FormatInt(next, 10)},
		"limit":  {"2"},
		"action": {"wanted."},
	}
	second := h.do("GET", "/v1/audit?"+secondQuery.Encode(), admin, nil, tenantHdr(tenant))
	if second.code != http.StatusOK {
		t.Fatalf("second page = %d %s", second.code, second.raw)
	}
	if got, want := auditResponseSeqs(t, second), []int64{events[4].Seq}; !slices.Equal(got, want) {
		t.Fatalf("second page sequences = %v, want %v", got, want)
	}
	if second.body["has_more"] != false || second.body["scan_complete"] != true {
		t.Fatalf("second page honesty fields = %#v", second.body)
	}
}

// TestAuditEmptyFilterValuesStayOnLegacyPath pins the cleared-filter
// semantics: an EMPTY filter value ("?actor=" from a cleared form field) means
// "no filter" — it must not flip the request onto the bounded-scan envelope
// (which would scan up to 20k events to match nothing the operator asked for).
func TestAuditEmptyFilterValuesStayOnLegacyPath(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "audit-empty-filters")

	r := h.do("GET", "/v1/audit?actor=&action=&q=&limit=1", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("empty-filter audit = %d %s", r.code, r.raw)
	}
	if _, ok := r.body["scan_complete"]; ok {
		t.Fatalf("empty filter values flipped onto the scan envelope: %s", r.raw)
	}
}

func TestAuditUnfilteredListKeepsLegacyEnvelope(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "audit-unfiltered")

	r := h.do("GET", "/v1/audit?limit=1", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("unfiltered audit = %d %s", r.code, r.raw)
	}
	if _, ok := r.body["next_from"]; ok {
		t.Fatalf("unfiltered response unexpectedly gained next_from: %s", r.raw)
	}
	if _, ok := r.body["scan_complete"]; ok {
		t.Fatalf("unfiltered response unexpectedly gained scan_complete: %s", r.raw)
	}
}

func TestAuditRejectsInvalidFilterAndExportBounds(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "audit-validation")

	tests := []struct {
		name string
		path string
	}{
		{name: "bad since", path: "/v1/audit?since=not-a-time"},
		{
			name: "until before since",
			path: "/v1/audit?since=2026-07-24T00%3A00%3A00Z" +
				"&until=2026-07-23T23%3A59%3A59Z",
		},
		{name: "export to before from", path: "/v1/audit/export?from=10&to=9"},
		{name: "export malformed to", path: "/v1/audit/export?to=not-a-sequence"},
		{name: "export bad until", path: "/v1/audit/export?until=not-a-time"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := h.do("GET", tt.path, admin, nil, tenantHdr(tenant))
			if r.code != http.StatusBadRequest {
				t.Fatalf("%s = %d %s, want 400", tt.path, r.code, r.raw)
			}
		})
	}
}

func TestAuditExportHonorsRangeFiltersAndTerminator(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "audit-export")
	events := appendAuditDrafts(t, h, tenant,
		model.AuditDraft{Actor: "export-actor", Action: "export.keep.one"},
		model.AuditDraft{Actor: "export-other", Action: "export.drop.two"},
		model.AuditDraft{Actor: "export-actor", Action: "export.keep.three"},
	)
	query := url.Values{
		"format": {"cef"},
		"from":   {strconv.FormatInt(events[0].Seq, 10)},
		"to":     {strconv.FormatInt(events[1].Seq, 10)},
		"actor":  {"export-actor"},
	}
	r := h.do("GET", "/v1/audit/export?"+query.Encode(), admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("export = %d %s", r.code, r.raw)
	}
	if !strings.Contains(r.raw, events[0].Action) {
		t.Fatalf("export omitted matching in-range event: %s", r.raw)
	}
	if strings.Contains(r.raw, events[1].Action) {
		t.Fatalf("export included non-matching actor: %s", r.raw)
	}
	if strings.Contains(r.raw, events[2].Action) {
		t.Fatalf("export crossed inclusive to bound: %s", r.raw)
	}
	terminator := "# olivares-audit-export-complete count=1 last_seq=" +
		strconv.FormatInt(events[0].Seq, 10) + "\n"
	if !strings.HasSuffix(r.raw, terminator) {
		t.Fatalf("export terminator = %q, want suffix %q", r.raw, terminator)
	}

	selfAudits := canonicalAuditEventsFrom(t, h, tenant, events[2].Seq+1)
	if len(selfAudits) != 1 || selfAudits[0].event.Action != "audit.export" {
		t.Fatalf("export self-audits = %#v, want one audit.export", selfAudits)
	}
	meta := selfAudits[0].meta
	if meta["format"] != "cef" {
		t.Fatalf("export audit format meta = %#v", meta["format"])
	}
	if got := jsonNumberInt64(t, meta["from"]); got != events[0].Seq {
		t.Fatalf("export audit from meta = %d, want %d", got, events[0].Seq)
	}
	if got := jsonNumberInt64(t, meta["to"]); got != events[1].Seq {
		t.Fatalf("export audit to meta = %d, want %d", got, events[1].Seq)
	}
	filters, ok := meta["filters"].(map[string]any)
	if !ok || filters["actor"] != "export-actor" {
		t.Fatalf("export audit filter meta = %#v", meta["filters"])
	}
}

func TestAuditFilteredListSelfAuditsOnceWithFilters(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "audit-self-read")
	events := appendAuditDrafts(t, h, tenant,
		model.AuditDraft{Actor: "self-audit-filter", Action: "sample.one"},
		model.AuditDraft{Actor: "other", Action: "sample.two"},
	)
	query := url.Values{
		"from":  {strconv.FormatInt(events[0].Seq, 10)},
		"limit": {"7"},
		"actor": {"self-audit-filter"},
	}
	r := h.do("GET", "/v1/audit?"+query.Encode(), admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("filtered list = %d %s", r.code, r.raw)
	}

	selfAudits := canonicalAuditEventsFrom(t, h, tenant, events[1].Seq+1)
	if len(selfAudits) != 1 || selfAudits[0].event.Action != "audit.read" {
		t.Fatalf("list self-audits = %#v, want one audit.read", selfAudits)
	}
	filters, ok := selfAudits[0].meta["filters"].(map[string]any)
	if !ok || filters["actor"] != "self-audit-filter" {
		t.Fatalf("list audit filter meta = %#v", selfAudits[0].meta["filters"])
	}
}

func TestAuditFilteredRoutesKeepRBACGuards(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "audit-rbac")
	issued := h.do("POST", "/v1/tokens", admin, map[string]any{
		"name": "audit-viewer", "tenant": tenant.String(), "role": auth.RoleViewer,
	}, nil)
	if issued.code != http.StatusCreated {
		t.Fatalf("issue viewer token = %d %s", issued.code, issued.raw)
	}
	viewer := issued.body["token"].(string)

	if r := h.do("GET", "/v1/audit?action=agent.", viewer, nil, nil); r.code != http.StatusOK {
		t.Fatalf("viewer filtered tenant audit = %d %s", r.code, r.raw)
	}
	if r := h.do("GET", "/v1/audit/export?action=agent.", viewer, nil, nil); r.code != http.StatusOK {
		t.Fatalf("viewer filtered tenant export = %d %s", r.code, r.raw)
	}
	if r := h.do("GET", "/v1/audit?action=agent.", "", nil, tenantHdr(tenant)); r.code != http.StatusUnauthorized {
		t.Fatalf("anonymous filtered tenant audit = %d, want 401", r.code)
	}
	if r := h.do("GET", "/v1/audit/system?action=auth.", viewer, nil, nil); r.code != http.StatusForbidden {
		t.Fatalf("tenant viewer filtered system audit = %d, want 403", r.code)
	}
	if r := h.do("GET", "/v1/audit/system?action=auth.", admin, nil, nil); r.code != http.StatusOK {
		t.Fatalf("superadmin filtered system audit = %d %s", r.code, r.raw)
	}
}

func appendAuditDrafts(
	t *testing.T,
	h *harness,
	tenant model.TenantID,
	drafts ...model.AuditDraft,
) []model.AuditEvent {
	t.Helper()
	events := make([]model.AuditEvent, 0, len(drafts))
	err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		for _, draft := range drafts {
			event, err := sc.Audit().Append(context.Background(), draft)
			if err != nil {
				return err
			}
			events = append(events, event)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("append audit drafts: %v", err)
	}
	return events
}

func auditResponseSeqs(t *testing.T, r resp) []int64 {
	t.Helper()
	items, ok := r.body["items"].([]any)
	if !ok {
		t.Fatalf("audit items = %#v", r.body["items"])
	}
	seqs := make([]int64, 0, len(items))
	for _, item := range items {
		event, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("audit item = %#v", item)
		}
		seq, ok := event["seq"].(float64)
		if !ok {
			t.Fatalf("audit seq = %#v", event["seq"])
		}
		seqs = append(seqs, int64(seq))
	}
	return seqs
}

func auditResponseInt64(t *testing.T, r resp, key string) int64 {
	t.Helper()
	value, ok := r.body[key].(float64)
	if !ok {
		t.Fatalf("%s = %#v", key, r.body[key])
	}
	return int64(value)
}

type canonicalAuditEvent struct {
	event model.AuditEvent
	meta  map[string]any
}

func canonicalAuditEventsFrom(
	t *testing.T,
	h *harness,
	tenant model.TenantID,
	from int64,
) []canonicalAuditEvent {
	t.Helper()
	var events []canonicalAuditEvent
	err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		walker, ok := sc.Audit().(store.CanonicalWalker)
		if !ok {
			t.Fatal("audit log does not expose canonical walk")
		}
		return walker.WalkCanonical(context.Background(), from, func(event model.AuditEvent, raw string, _ []byte) error {
			decoder := json.NewDecoder(strings.NewReader(raw))
			decoder.UseNumber()
			var meta map[string]any
			if err := decoder.Decode(&meta); err != nil {
				return err
			}
			events = append(events, canonicalAuditEvent{event: event, meta: meta})
			return nil
		})
	})
	if err != nil {
		t.Fatalf("walk canonical audit events: %v", err)
	}
	return events
}

func jsonNumberInt64(t *testing.T, value any) int64 {
	t.Helper()
	number, ok := value.(json.Number)
	if !ok {
		t.Fatalf("meta number = %#v", value)
	}
	got, err := number.Int64()
	if err != nil {
		t.Fatalf("parse meta number: %v", err)
	}
	return got
}

// TestAuditListPublishesTheLedgerHead is the end-to-end half of the head_seq
// contract: the field is on every audit list response, it names the real
// chain tip, and — paired with ?from — it is what lets a client read the NEWEST
// events of a ledger that only pages forwards.
//
// The bug it forecloses is not "the number is wrong": it is that without this
// field the newest events had no address at all, so the notification bell asked
// for `limit=10` with no `from`, got the ten OLDEST, and labelled the first of
// them "newest".
func TestAuditListPublishesTheLedgerHead(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "audit-head-seq")

	// The chain already holds org.create at seq 1 from provisioning, so the tip
	// after these is the LAST one appended, not their count.
	events := appendAuditDrafts(t, h, tenant,
		model.AuditDraft{Actor: "head", Action: "head.one"},
		model.AuditDraft{Actor: "head", Action: "head.two"},
		model.AuditDraft{Actor: "head", Action: "head.three"},
		model.AuditDraft{Actor: "head", Action: "head.four"},
	)
	tip := events[len(events)-1].Seq

	probe := h.do("GET", "/v1/audit?limit=1", admin, nil, tenantHdr(tenant))
	if probe.code != http.StatusOK {
		t.Fatalf("probe = %d %s", probe.code, probe.raw)
	}
	if got := auditResponseInt64(t, probe, "head_seq"); got != tip {
		t.Fatalf("head_seq = %d, want the chain tip %d", got, tip)
	}
	// The head is read BEFORE this request's own audit.read joins the chain. Were
	// it read after, head_seq would be tip+1 and would name an event that is not
	// in any page the caller just received.
	if got, want := auditResponseSeqs(t, probe), []int64{1}; !slices.Equal(got, want) {
		t.Fatalf("probe page = %v, want %v: ?from still pages FORWARDS from the genesis event", got, want)
	}

	// The whole point of the field: address the tail with it.
	const recent = 3
	tailFrom := tip - recent + 1
	tail := h.do(
		"GET",
		"/v1/audit?from="+strconv.FormatInt(tailFrom, 10)+"&limit="+strconv.Itoa(recent),
		admin, nil, tenantHdr(tenant),
	)
	if tail.code != http.StatusOK {
		t.Fatalf("tail = %d %s", tail.code, tail.raw)
	}
	wantTail := []int64{tip - 2, tip - 1, tip}
	if got := auditResponseSeqs(t, tail); !slices.Equal(got, wantTail) {
		t.Fatalf("tail page = %v, want the last %d events %v", got, recent, wantTail)
	}
	// Each request self-audits, so the head has moved on by now; what must hold is
	// that it is AT LEAST the tip the page ends on — never behind it, which is the
	// state that would send a client backwards forever.
	if got := auditResponseInt64(t, tail, "head_seq"); got < tip {
		t.Fatalf("head_seq = %d on a page ending at %d: the head cannot trail its own page", got, tip)
	}

	// Additive: the legacy unfiltered envelope is unchanged apart from the new field.
	if _, ok := tail.body["next_from"]; ok {
		t.Fatalf("unfiltered response gained next_from: %s", tail.raw)
	}
	if _, ok := tail.body["scan_complete"]; ok {
		t.Fatalf("unfiltered response gained scan_complete: %s", tail.raw)
	}

	// And the filtered path carries it too — head_seq is a property of the ledger,
	// not of one of the two read paths.
	filtered := h.do("GET", "/v1/audit?action=head.&limit=100", admin, nil, tenantHdr(tenant))
	if filtered.code != http.StatusOK {
		t.Fatalf("filtered = %d %s", filtered.code, filtered.raw)
	}
	if got := auditResponseInt64(t, filtered, "head_seq"); got < tip {
		t.Fatalf("filtered head_seq = %d, want at least the tip %d", got, tip)
	}

	// The SYSTEM chain publishes its own head, and it is asserted here because the
	// route is a second entry into the same handler: a change that answered head_seq
	// only for the resolved business tenant would leave every test above green while
	// the superadmin's evidence ledger lost its tail address. Its chain is never
	// empty — EnsureSystemTenant seals system.genesis at seq 1 — so a positive head
	// is the whole claim.
	system := h.do("GET", "/v1/audit/system?limit=1", admin, nil, nil)
	if system.code != http.StatusOK {
		t.Fatalf("system audit = %d %s", system.code, system.raw)
	}
	if got := auditResponseInt64(t, system, "head_seq"); got < 1 {
		t.Fatalf("system head_seq = %d, want the system chain's own tip (>= 1)", got)
	}
}

// TestAuditExcludeActionFiltersTheViewAndNeverTheLedger is the whole point of
// exclude_action stated as a test: it changes what a caller is SHOWN and nothing
// else.
//
// The second half is the half that matters. A change that stopped APPENDING the
// excluded events would pass "the excluded ones do not come back" perfectly, and
// would have quietly traded an evidence property for a quiet notification bell —
// the one remedy that must never be the cheap one. So the excluded events are read
// back through a second, unfiltered request, and head_seq is asserted to count the
// WHOLE chain: a head that counted only the filtered view would shift the window
// the bell asks for and put it right back to showing the wrong events.
func TestAuditExcludeActionFiltersTheViewAndNeverTheLedger(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "audit-exclude-action")

	appendAuditDrafts(t, h, tenant,
		model.AuditDraft{Actor: "excl", Action: "agent.create"},
		model.AuditDraft{Actor: "excl", Action: "noise.one"},
		model.AuditDraft{Actor: "excl", Action: "agent.update"},
		model.AuditDraft{Actor: "excl", Action: "noise.two"},
	)

	// Reading the ledger appends audit.read, so by the time this asks, the chain
	// holds the very family the bell needs excluded — no fixture required.
	seed := h.do("GET", "/v1/audit?limit=100", admin, nil, tenantHdr(tenant))
	if seed.code != http.StatusOK {
		t.Fatalf("seed read = %d %s", seed.code, seed.raw)
	}

	filtered := h.do("GET", "/v1/audit?limit=100&exclude_action=noise.&exclude_action=audit.read", admin, nil, tenantHdr(tenant))
	if filtered.code != http.StatusOK {
		t.Fatalf("excluded list = %d %s", filtered.code, filtered.raw)
	}
	actions := auditResponseActions(t, filtered)
	if len(actions) == 0 {
		t.Fatalf("excluded list came back empty; it must still return what was NOT excluded: %s", filtered.raw)
	}
	for _, action := range actions {
		if strings.HasPrefix(action, "noise.") || strings.HasPrefix(action, "audit.read") {
			t.Fatalf("excluded action %q came back: %v", action, actions)
		}
	}
	// Repeatable means every occurrence counts. One prefix honoured and the other
	// dropped would still pass a test that only checked one of them.
	if !slices.Contains(actions, "agent.create") || !slices.Contains(actions, "agent.update") {
		t.Fatalf("exclusion removed more than it was asked to: %v", actions)
	}

	// FILTERED, NOT DELETED. The same events, through a request that does not exclude.
	// Taken AFTER the filtered one on purpose: every read appends its own audit.read,
	// so the chain only grows, and the head measured earlier can be compared to a
	// count measured later in exactly one direction. Written the other way round this
	// assertion failed on its own arithmetic — a head photographed at one instant
	// against a count photographed at the next.
	plain := h.do("GET", "/v1/audit?limit=100", admin, nil, tenantHdr(tenant))
	if plain.code != http.StatusOK {
		t.Fatalf("plain list = %d %s", plain.code, plain.raw)
	}
	plainActions := auditResponseActions(t, plain)
	if !slices.Contains(plainActions, "noise.one") || !slices.Contains(plainActions, "noise.two") {
		t.Fatalf("events excluded from one VIEW went missing from the ledger: %v", plainActions)
	}
	if !slices.Contains(plainActions, "audit.read") {
		t.Fatalf("audit.read stopped being recorded: excluding a family from a view must not stop sealing it: %v", plainActions)
	}

	// head_seq counts the chain, not the page. The bell derives its window from it,
	// so a head that shrank with the filter would move the window and undo.
	excludedHead := auditResponseInt64(t, filtered, "head_seq")
	plainHead := auditResponseInt64(t, plain, "head_seq")
	if excludedHead <= int64(len(actions)) {
		t.Fatalf("head_seq = %d with %d items in the filtered page: a head that counted the VIEW would be no larger than it", excludedHead, len(actions))
	}
	// THE ONE THAT SEPARATES FILTERING FROM DELETING, and it has to be this exact
	// claim. Reading back "audit.read is still in the ledger" is NOT enough: the
	// earlier unfiltered reads put some there, so a change that stopped sealing only
	// the reads it was asked to hide passes that check untouched. It survived here
	// until this assertion replaced it.
	//
	// Each read measures the head BEFORE appending its own, so between the filtered
	// read's measurement and the next one's, exactly ONE event was sealed: the
	// filtered read's own. Anything else means a request served a page and left no
	// trace of having done so.
	if plainHead != excludedHead+1 {
		t.Fatalf("head_seq %d -> %d across one read: a filtered request must still seal its OWN audit.read, exactly once", excludedHead, plainHead)
	}
	reads := 0
	for _, action := range plainActions {
		if action == "audit.read" {
			reads++
		}
	}
	if reads < 2 {
		t.Fatalf("only %d audit.read event(s) in the chain after two unfiltered reads and one filtered one: the filtered read did not record itself", reads)
	}
}

// TestAuditWithoutExcludeActionIsUnchanged is the additive half: the parameter has
// to be invisible when nobody passes it, and "unfiltered" has to keep meaning the
// legacy envelope rather than sliding onto the bounded scanner.
func TestAuditWithoutExcludeActionIsUnchanged(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "audit-exclude-absent")

	r := h.do("GET", "/v1/audit?limit=100", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("plain list = %d %s", r.code, r.raw)
	}
	if _, ok := r.body["scan_complete"]; ok {
		t.Fatalf("a request with no exclusion took the FILTERED path: %s", r.raw)
	}
	// And an empty occurrence is a cleared field, not "exclude everything" — the same
	// rule the other filters follow, and the direction that would silently blank the
	// ledger view if it were read the other way.
	cleared := h.do("GET", "/v1/audit?limit=100&exclude_action=", admin, nil, tenantHdr(tenant))
	if cleared.code != http.StatusOK {
		t.Fatalf("cleared exclusion = %d %s", cleared.code, cleared.raw)
	}
	if _, ok := cleared.body["scan_complete"]; ok {
		t.Fatalf("an EMPTY exclude_action flipped the request onto the scan path: %s", cleared.raw)
	}
	if len(auditResponseActions(t, cleared)) == 0 {
		t.Fatalf("an empty exclude_action emptied the view: %s", cleared.raw)
	}
}

func auditResponseActions(t *testing.T, r resp) []string {
	t.Helper()
	items, ok := r.body["items"].([]any)
	if !ok {
		t.Fatalf("audit items = %#v", r.body["items"])
	}
	actions := make([]string, 0, len(items))
	for _, item := range items {
		event, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("audit item = %#v", item)
		}
		action, ok := event["action"].(string)
		if !ok {
			t.Fatalf("audit action = %#v", event["action"])
		}
		actions = append(actions, action)
	}
	return actions
}

// TestAuditExcludeActionIsCaseSensitiveLikeAction pins the parity that makes
// exclude_action safe to reason about: it matches with the SAME rule as the
// positive `action` filter, case included.
//
// Every action and prefix in the other tests is lower-case, so a change that
// lower-cased one side of the exclusion would leave the whole battery green while
// the two sibling filters quietly stopped agreeing — and a caller who read the rule
// off `action` would be wrong about `exclude_action` with no way to find out.
func TestAuditExcludeActionIsCaseSensitiveLikeAction(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "audit-exclude-case")
	appendAuditDrafts(t, h, tenant,
		model.AuditDraft{Actor: "case", Action: "Audit.Read.Lookalike"},
		model.AuditDraft{Actor: "case", Action: "audit.read.lookalike"},
	)

	r := h.do("GET", "/v1/audit?limit=100&exclude_action=audit.read", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("excluded list = %d %s", r.code, r.raw)
	}
	actions := auditResponseActions(t, r)
	if !slices.Contains(actions, "Audit.Read.Lookalike") {
		t.Fatalf("a differently-CASED action was excluded by prefix %q: exclusion must match like `action`, which is case-sensitive: %v", "audit.read", actions)
	}
	if slices.Contains(actions, "audit.read.lookalike") {
		t.Fatalf("the exactly-cased prefix did not exclude: %v", actions)
	}
}

// TestAuditExcludeActionIsRecordedAsAListInTheSelfAudit keeps the evidence able to
// say what the reader asked for.
//
// A joined string cannot: nothing forbids a comma inside a prefix, so ["a,b","c"]
// and ["a","b,c"] are different filters that would record identically. And with no
// assertion here at all, simply deleting the metadata write survives every other
// test in this file — the engine would go on filtering and recording the read while
// the record of WHICH exclusion was asked for disappeared.
func TestAuditExcludeActionIsRecordedAsAListInTheSelfAudit(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "audit-exclude-meta")
	events := appendAuditDrafts(t, h, tenant,
		model.AuditDraft{Actor: "meta", Action: "sample.one"},
	)

	r := h.do("GET", "/v1/audit?limit=100&exclude_action=a%2Cb&exclude_action=c", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("excluded list = %d %s", r.code, r.raw)
	}

	selfAudits := canonicalAuditEventsFrom(t, h, tenant, events[0].Seq+1)
	if len(selfAudits) != 1 || selfAudits[0].event.Action != "audit.read" {
		t.Fatalf("self-audits = %#v, want one audit.read", selfAudits)
	}
	// Under "filters", where the rest of the read's filter record lives — the same
	// place TestAuditSelfAuditRecordsListFilters reads `actor` from.
	filters, ok := selfAudits[0].meta["filters"].(map[string]any)
	if !ok {
		t.Fatalf("self-audit filter meta = %#v", selfAudits[0].meta["filters"])
	}
	recorded, ok := filters["exclude_action"].([]any)
	if !ok {
		t.Fatalf("exclude_action meta = %#v, want a LIST: a joined string cannot distinguish [\"a,b\",\"c\"] from [\"a\",\"b,c\"]", filters["exclude_action"])
	}
	got := make([]string, 0, len(recorded))
	for _, value := range recorded {
		text, ok := value.(string)
		if !ok {
			t.Fatalf("exclude_action entry = %#v", value)
		}
		got = append(got, text)
	}
	if want := []string{"a,b", "c"}; !slices.Equal(got, want) {
		t.Fatalf("exclude_action meta = %v, want %v — the occurrence boundaries are the point", got, want)
	}
}

// TestAuditSystemRouteHonoursExcludeAction covers the system chain directly. The two
// routes converge on one handler today, so this is green by construction — which is
// exactly why it is worth writing down: the day the system route grows a path of its
// own, nothing else in this file would notice it losing the filter.
func TestAuditSystemRouteHonoursExcludeAction(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()

	// TWO reads before the assertion, and the reason is the ordering this whole
	// change rests on: a read measures the chain BEFORE appending its own event, so
	// the first system read cannot see its own audit.read. One read leaves nothing to
	// exclude, and the control below would then be passing on an empty premise.
	if seed := h.do("GET", "/v1/audit/system?limit=100", admin, nil, nil); seed.code != http.StatusOK {
		t.Fatalf("system seed read = %d %s", seed.code, seed.raw)
	}
	plain := h.do("GET", "/v1/audit/system?limit=100", admin, nil, nil)
	if plain.code != http.StatusOK {
		t.Fatalf("system list = %d %s", plain.code, plain.raw)
	}
	if !slices.Contains(auditResponseActions(t, plain), "audit.read") {
		t.Fatalf("the system chain holds no audit.read to exclude; the fixture proves nothing: %s", plain.raw)
	}

	filtered := h.do("GET", "/v1/audit/system?limit=100&exclude_action=audit.read", admin, nil, nil)
	if filtered.code != http.StatusOK {
		t.Fatalf("filtered system list = %d %s", filtered.code, filtered.raw)
	}
	for _, action := range auditResponseActions(t, filtered) {
		if strings.HasPrefix(action, "audit.read") {
			t.Fatalf("the system route ignored exclude_action: %q came back", action)
		}
	}
}
