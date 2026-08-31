// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package eventing

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// catalog_docs_test.go turns a comment into an invariant.
//
// catalog.go has always CLAIMED that the hand-maintained AsyncAPI document and
// the rendered events reference "mirror this table; if either changes, both
// must move together". Nothing enforced it, and by the time this test was
// written the docs were behind by four types (approval.resolved, metric.sampled,
// audit.recorded, workflow.signal) — an integrator reading the published
// contract could not discover events the platform had been publishing for
// several releases.
//
// A missing entry is a PUBLISHED-CONTRACT gap, not a cosmetic one: the catalog
// is the deny-closed allowlist, so a type absent from the docs is subscribable
// but undiscoverable. Failing here is cheaper than a support ticket.

const (
	asyncAPIPath                 = "../../docs-site/public/asyncapi/asyncapi.yaml"
	eventsDocPath                = "../../docs-site/src/content/docs/reference/events.mdx"
	directNoticeProducerTestPath = "../sessions/communication_service_test.go"
	sessionsSourcePath           = "../sessions"
)

func readDoc(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("read %s: %v (the events docs are part of the published contract; if the file moved, move this test with it)", path, err)
	}
	return string(b)
}

// Every cataloged type must be a declared CHANNEL of the AsyncAPI document —
// checked on the channel address, not on the string appearing anywhere, so a
// passing mention in prose cannot satisfy the contract.
func TestCatalogTypesAppearInAsyncAPI(t *testing.T) {
	doc := readDoc(t, asyncAPIPath)
	for _, e := range Catalog() {
		if !strings.Contains(doc, "address: "+string(e.Type)) {
			t.Errorf("event type %q is cataloged but has no channel in %s — the published channel document must declare every subscribable type", e.Type, asyncAPIPath)
		}
	}
}

// The AsyncAPI channel set must not exceed the catalog either: a channel the
// engine does not catalog is a published promise it would deny at delivery.
func TestAsyncAPIChannelsAreAllCataloged(t *testing.T) {
	doc := readDoc(t, asyncAPIPath)
	cataloged := map[string]bool{}
	for _, e := range Catalog() {
		cataloged[string(e.Type)] = true
	}
	for _, line := range strings.Split(doc, "\n") {
		line = strings.TrimSpace(line)
		addr, ok := strings.CutPrefix(line, "address: ")
		if !ok {
			continue
		}
		if addr = strings.TrimSpace(addr); addr != "" && !cataloged[addr] {
			t.Errorf("%s declares channel %q, which is not in the catalog — the engine would deny every delivery of it", asyncAPIPath, addr)
		}
	}
}

// Every cataloged type must appear in the rendered events reference, together
// with the permission that gates receiving it — the per-type RBAC table is what
// an integrator uses to size the role their subscriber needs.
func TestCatalogTypesAppearInEventsReference(t *testing.T) {
	doc := readDoc(t, eventsDocPath)
	for _, e := range Catalog() {
		if !strings.Contains(doc, string(e.Type)) {
			t.Errorf("event type %q is cataloged but absent from %s", e.Type, eventsDocPath)
			continue
		}
		permissionRow := "| `" + string(e.Type) + "` | `" + string(e.Permission) + "`"
		if !strings.Contains(doc, permissionRow) {
			t.Errorf("event type %q is not paired with receive permission %q in the per-type table — a permission mentioned elsewhere cannot prove the mapping", e.Type, e.Permission)
		}
	}
}

// DirectNotice uses its own immutable payload rather than the WorkItem-shaped
// WorkEventFact. These anchors keep the channel, message and v1 schema tied
// together in the machine-readable contract.
func TestDirectNoticeAsyncAPIUsesItsVersionedPayload(t *testing.T) {
	doc := readDoc(t, asyncAPIPath)
	for _, fragment := range []string{
		"address: work.message.available",
		"#/components/messages/WorkMessageAvailable",
		"name: WorkMessageAvailable",
		"title: work.message.available",
		"#/components/schemas/DirectNoticeAvailableV1",
		"DirectNoticeAvailableV1:",
		"const: message.publish.direct",
		"const: sessions.message",
	} {
		if !strings.Contains(doc, fragment) {
			t.Errorf("%s lacks DirectNotice contract anchor %q", asyncAPIPath, fragment)
		}
	}
}

func TestProtocolBindingAsyncAPIUsesBoundedPayload(t *testing.T) {
	doc := readDoc(t, asyncAPIPath)
	for _, eventType := range []string{
		"work.binding.reserved",
		"work.binding.observed",
		"work.binding.ambiguous",
		"work.binding.cancel_requested",
	} {
		if !strings.Contains(doc, "address: "+eventType) {
			t.Errorf("%s lacks ProtocolBinding channel %q", asyncAPIPath, eventType)
		}
	}
	if got := strings.Count(doc, "$ref: '#/components/schemas/ProtocolBindingEvent'"); got != 4 {
		t.Errorf("ProtocolBinding message schema refs = %d, want 4", got)
	}

	start := strings.Index(doc, "    ProtocolBindingEvent:\n")
	end := strings.Index(doc, "    WorkEventFact:\n")
	if start < 0 || end <= start {
		t.Fatalf("%s does not define ProtocolBindingEvent before WorkEventFact", asyncAPIPath)
	}
	schema := doc[start:end]
	for _, fragment := range []string{
		"additionalProperties: false",
		"binding_id:",
		"binding_spec_id:",
		"binding_spec_generation:",
		"binding_generation:",
		"protocol:",
		"work_item_id:",
		"workspace_id:",
		"work_status:",
		"lease_fence:",
		"verdict:",
		"code:",
		"terminal:",
		"event_seq:",
		"external_id_hash:",
	} {
		if !strings.Contains(schema, fragment) {
			t.Errorf("ProtocolBindingEvent schema lacks %q", fragment)
		}
	}
	for _, forbidden := range []string{
		"remote_state:",
		"remote_resource_ref:",
		"tool_arguments:",
		"task_result:",
		"message_content:",
		"cancel_reason:",
	} {
		if strings.Contains(schema, forbidden) {
			t.Errorf("ProtocolBindingEvent schema exposes forbidden field %q", forbidden)
		}
	}
}

func TestProtocolMessageAsyncAPIUsesWorkflowCarrierPayload(t *testing.T) {
	doc := readDoc(t, asyncAPIPath)
	for _, eventType := range []string{
		"work.protocol.reply.available",
		"work.protocol.message.received",
	} {
		if !strings.Contains(doc, "address: "+eventType) {
			t.Errorf("%s lacks protocol Message channel %q", asyncAPIPath, eventType)
		}
	}
	for _, fragment := range []string{
		"name: WorkProtocolReplyAvailable",
		"name: WorkProtocolMessageReceived",
		"title: work.protocol.reply.available",
		"title: work.protocol.message.received",
		"#/components/schemas/WorkflowMessageCarrierV1",
		"WorkflowMessageCarrierV1:",
		"workflow.message.protocol-reply",
		"workflow.message.protocol-inbound",
		"enum: [request, handoff_offer, notice]",
		"additionalProperties: false",
	} {
		if !strings.Contains(doc, fragment) {
			t.Errorf("%s lacks protocol Message contract anchor %q", asyncAPIPath, fragment)
		}
	}
}

// The sessions producer freezes its v1 encoder against this same byte string.
// Linking the two golden fixtures makes the durable-intake test exercise the
// bytes the producer proves it emits, rather than an independently invented
// payload that merely resembles the schema.
func TestDirectNoticeDurableFixtureMatchesProducerGolden(t *testing.T) {
	producerTest := readDoc(t, directNoticeProducerTestPath)
	declaration := "const golden = `" + directNoticeEventPayloadV1Golden + "`"
	if !strings.Contains(producerTest, declaration) {
		t.Fatalf("%s does not freeze the DirectNotice v1 bytes exercised by Eventing", directNoticeProducerTestPath)
	}
}

// The reverse direction: a type documented as subscribable but NOT cataloged
// would be a promise the deny-closed engine refuses to keep (every delivery
// denied). Scan the reference's channel table for type-shaped identifiers and
// require each to be cataloged.
func TestDocumentedTypesAreAllCataloged(t *testing.T) {
	doc := readDoc(t, eventsDocPath)
	cataloged := map[string]bool{}
	for _, e := range Catalog() {
		cataloged[string(e.Type)] = true
	}
	// The channel table rows start with a backticked type identifier.
	for _, line := range strings.Split(doc, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "| `") {
			continue
		}
		rest := line[len("| `"):]
		end := strings.Index(rest, "`")
		if end <= 0 {
			continue
		}
		name := rest[:end]
		// Only consider "<domain>.<verb>" identifiers — the table also carries
		// field names and permissions in other columns/tables.
		if strings.Count(name, ".") != 1 || strings.ContainsAny(name, " :/") {
			continue
		}
		if !cataloged[name] {
			t.Errorf("%s documents %q as an event type, but it is not in the catalog — the engine would deny every delivery of it", eventsDocPath, name)
		}
	}
}

// The sessions module owns its WorkEvent wire strings and cannot import
// Eventing. Inventorying production string literals here keeps the two module
// catalogs exact without creating an import cycle or accepting a design-only
// alias that no source outbox can emit.
func TestSessionsProductionWorkEventVocabularyIsCatalogued(t *testing.T) {
	discovered := map[string]string{}
	entries, err := os.ReadDir(sessionsSourcePath)
	if err != nil {
		t.Fatalf("read sessions production sources: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") ||
			strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(sessionsSourcePath, entry.Name())
		file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", path, parseErr)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			value, unquoteErr := strconv.Unquote(literal.Value)
			if unquoteErr != nil || !closedWorkEventType(value) {
				return true
			}
			discovered[value] = entry.Name()
			return true
		})
	}

	catalogued := map[string]EventTypeInfo{}
	for _, info := range Catalog() {
		if strings.HasPrefix(string(info.Type), "work.") {
			catalogued[string(info.Type)] = info
		}
	}
	for typ, source := range discovered {
		info, ok := catalogued[typ]
		if !ok {
			t.Errorf("sessions production source %s emits %q, but Eventing does not catalog it", source, typ)
			continue
		}
		if !info.Internal {
			t.Errorf("sessions durable WorkEvent %q is not marked Internal", typ)
		}
	}
	for typ := range catalogued {
		if _, ok := discovered[typ]; !ok {
			t.Errorf("Eventing catalogs work type %q, but no sessions production source owns that wire string", typ)
		}
	}
}

func closedWorkEventType(value string) bool {
	if !strings.HasPrefix(value, "work.") || strings.HasSuffix(value, ".") ||
		strings.Count(value, ".") < 2 {
		return false
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '.' && r != '-' && r != '_' {
			return false
		}
	}
	return true
}
