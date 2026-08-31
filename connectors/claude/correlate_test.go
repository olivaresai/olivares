// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

func otelSig(session, tool, id string, at time.Time, kind, ref string, mode model.AccessMode) toolSignal {
	return toolSignal{sessionID: session, toolName: tool, toolUseID: id, at: at, fromHook: false, resKind: kind, resRef: ref, mode: mode, hasResource: kind != resTool}
}

func hookSig(session, tool, id string, at time.Time, kind, ref string, mode model.AccessMode) toolSignal {
	return toolSignal{sessionID: session, toolName: tool, toolUseID: id, at: at, fromHook: true, resKind: kind, resRef: ref, mode: mode, hasResource: kind != resTool}
}

func TestCorrelatePairByToolUseID(t *testing.T) {
	c := &collect{}
	cr := newCorrelator(5*time.Second, c.emit)

	// OTEL has no resource detail; the hook carries the real file path. The pair
	// must complete into exactly one edge using the hook's resource.
	cr.offer(otelSig("s", "Read", "tu_1", testTime, resTool, "Read", model.ModeRead), testTime)
	if c.count() != 0 {
		t.Fatal("a single side must not emit yet")
	}
	cr.offer(hookSig("s", "Read", "tu_1", testTime, resFile, "/etc/hosts", model.ModeRead), testTime)

	edges := c.edges()
	if len(edges) != 1 {
		t.Fatalf("want exactly one edge, got %d", len(edges))
	}
	if edges[0].ResourceRef != "/etc/hosts" || edges[0].ResourceKind != resFile {
		t.Errorf("hook resource should win: %+v", edges[0])
	}
}

func TestCorrelateOtelOnlySweepDegraded(t *testing.T) {
	c := &collect{}
	cr := newCorrelator(5*time.Second, c.emit)
	cr.offer(otelSig("s", "Read", "tu_1", testTime, resTool, "Read", model.ModeRead), testTime)

	// Before the window: still buffered.
	cr.sweep(testTime.Add(time.Second))
	if c.count() != 0 {
		t.Fatal("swept too early")
	}
	// After the window: flush a degraded usage edge.
	cr.sweep(testTime.Add(6 * time.Second))
	edges := c.edges()
	if len(edges) != 1 || edges[0].ResourceKind != resTool {
		t.Fatalf("want one degraded edge, got %+v", edges)
	}
}

func TestCorrelateHookOnlySweepResource(t *testing.T) {
	c := &collect{}
	cr := newCorrelator(time.Second, c.emit)
	cr.offer(hookSig("s", "Write", "tu_9", testTime, resFile, "/a/b", model.ModeWrite), testTime)
	cr.sweep(testTime.Add(2 * time.Second))
	edges := c.edges()
	if len(edges) != 1 || edges[0].ResourceRef != "/a/b" || edges[0].Mode != model.ModeWrite {
		t.Fatalf("hook-only edge = %+v", edges)
	}
}

func TestCorrelateFallbackBySessionTool(t *testing.T) {
	c := &collect{}
	cr := newCorrelator(5*time.Second, c.emit)
	// No tool_use_id on either side → fall back to session+tool join.
	cr.offer(otelSig("s", "Edit", "", testTime, resTool, "Edit", model.ModeWrite), testTime)
	cr.offer(hookSig("s", "Edit", "", testTime, resFile, "/x", model.ModeWrite), testTime)
	if len(c.edges()) != 1 {
		t.Fatalf("fallback join failed: %d edges", len(c.edges()))
	}
}

func TestCorrelateSameSideNeverMerges(t *testing.T) {
	c := &collect{}
	cr := newCorrelator(5*time.Second, c.emit)
	// Two OTEL tool_results for the same session+tool with no id are the SAME side, so
	// neither is a counterpart for the other: both stay buffered (no false pairing) and
	// both flush as separate degraded edges on drain.
	cr.offer(otelSig("s", "Bash", "", testTime, resShell, "ls", model.ModeUnknown), testTime)
	cr.offer(otelSig("s", "Bash", "", testTime, resShell, "cat", model.ModeUnknown), testTime.Add(time.Millisecond))
	if c.count() != 0 {
		t.Fatalf("two same-side signals must not pair, got %d edges", c.count())
	}
	cr.drain()
	if c.count() != 2 {
		t.Fatalf("drain should flush both calls, total %d", c.count())
	}
}

func TestCorrelateInterleavedIDsPairCorrectly(t *testing.T) {
	c := &collect{}
	cr := newCorrelator(5*time.Second, c.emit)
	// The fidelity fix: TWO calls of the same tool INTERLEAVE, both carrying ids on both
	// sides (the verified current norm). Each OTEL must pair with the hook of the SAME
	// id — not whichever arrives — yielding exactly two correctly-resolved edges. The old
	// single-slot session+tool join left all four signals unpaired here.
	cr.offer(otelSig("s", "Read", "tu_1", testTime, resTool, "Read", model.ModeRead), testTime)
	cr.offer(otelSig("s", "Read", "tu_2", testTime, resTool, "Read", model.ModeRead), testTime)
	cr.offer(hookSig("s", "Read", "tu_2", testTime, resFile, "/two", model.ModeRead), testTime)
	cr.offer(hookSig("s", "Read", "tu_1", testTime, resFile, "/one", model.ModeRead), testTime)
	edges := c.edges()
	if len(edges) != 2 {
		t.Fatalf("interleaved id'd calls must pair into 2 edges, got %d", len(edges))
	}
	refs := map[string]bool{}
	for _, e := range edges {
		refs[e.ResourceRef] = true
	}
	if !refs["/one"] || !refs["/two"] {
		t.Errorf("each call must pair with its OWN hook resource, got %v", refs)
	}
}

func TestCorrelateIdlessPrefersIdlessBuffered(t *testing.T) {
	c := &collect{}
	cr := newCorrelator(5*time.Second, c.emit)
	// A precisely-id'd OTEL span and a degraded id-less OTEL span share the bucket. An
	// incoming id-less hook must pair with the ID-LESS span, leaving the id'd span free
	// for its own exact hook — no resource theft in mixed-fidelity traffic.
	cr.offer(otelSig("s", "Read", "tu_1", testTime, resTool, "Read", model.ModeRead), testTime)
	cr.offer(otelSig("s", "Read", "", testTime, resTool, "Read", model.ModeRead), testTime)
	cr.offer(hookSig("s", "Read", "", testTime, resFile, "/degraded", model.ModeRead), testTime)
	if c.count() != 1 {
		t.Fatalf("id-less hook should pair with the id-less span, got %d edges", c.count())
	}
	cr.offer(hookSig("s", "Read", "tu_1", testTime, resFile, "/precise", model.ModeRead), testTime)
	edges := c.edges()
	if len(edges) != 2 {
		t.Fatalf("want 2 edges, got %d", len(edges))
	}
	refs := map[string]bool{}
	for _, e := range edges {
		refs[e.ResourceRef] = true
	}
	if !refs["/degraded"] || !refs["/precise"] {
		t.Errorf("resources mis-paired (id-less hook stole the id'd span): %v", refs)
	}
}

func TestCorrelatePromptIDKeepsCallsApart(t *testing.T) {
	c := &collect{}
	cr := newCorrelator(5*time.Second, c.emit)
	// When tool_use_id is absent but prompt.id differs AND both sides carry it, the calls
	// are provably different and must not merge (forward-safe guard). Here both sides are
	// OTEL-shaped with prompt.id to exercise the discriminator directly.
	a := otelSig("s", "Glob", "", testTime, resTool, "Glob", model.ModeRead)
	a.promptID = "p1"
	b := hookSig("s", "Glob", "", testTime, resFile, "/g", model.ModeRead)
	b.promptID = "p2"
	cr.offer(a, testTime)
	cr.offer(b, testTime)
	if c.count() != 0 {
		t.Fatalf("different prompt.ids must not merge, got %d", c.count())
	}
	cr.drain()
	if c.count() != 2 {
		t.Fatalf("drain should flush both, got %d", c.count())
	}
}

func TestCorrelateAsymmetricToolUseID(t *testing.T) {
	c := &collect{}
	cr := newCorrelator(5*time.Second, c.emit)
	// The dominant CLI reality: OTEL tool_result carries a tool_use_id, the hook
	// does not. They MUST still pair into exactly one edge using the hook resource.
	cr.offer(otelSig("s", "Read", "tu_1", testTime, resTool, "Read", model.ModeRead), testTime)
	cr.offer(hookSig("s", "Read", "", testTime, resFile, "/etc/hosts", model.ModeRead), testTime)
	edges := c.edges()
	if len(edges) != 1 {
		t.Fatalf("asymmetric tool_use_id must pair: got %d edges", len(edges))
	}
	if edges[0].ResourceKind != resFile || edges[0].ResourceRef != "/etc/hosts" {
		t.Errorf("hook resource should win: %+v", edges[0])
	}
}

func TestCorrelateDistinctIDsDoNotMerge(t *testing.T) {
	c := &collect{}
	cr := newCorrelator(5*time.Second, c.emit)
	// Two different calls of the same tool, both carrying ids → differentCall keeps
	// them apart even though they share the session+tool bucket: no premature pairing,
	// and on drain each flushes as its own degraded edge (never one merged edge).
	cr.offer(otelSig("s", "Read", "tu_1", testTime, resTool, "Read", model.ModeRead), testTime)
	cr.offer(hookSig("s", "Read", "tu_2", testTime, resFile, "/b", model.ModeRead), testTime)
	if c.count() != 0 {
		t.Fatalf("conflicting ids must not pair: %d edges", c.count())
	}
	cr.drain()
	if c.count() != 2 {
		t.Fatalf("after drain: %d edges", c.count())
	}
}

func TestCorrelateDrain(t *testing.T) {
	c := &collect{}
	cr := newCorrelator(time.Hour, c.emit)
	cr.offer(hookSig("s", "Read", "tu_1", testTime, resFile, "/a", model.ModeRead), testTime)
	cr.offer(hookSig("s", "Read", "tu_2", testTime, resFile, "/b", model.ModeRead), testTime)
	cr.drain()
	if len(c.edges()) != 2 {
		t.Fatalf("drain flushed %d, want 2", len(c.edges()))
	}
}
