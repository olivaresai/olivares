// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The two literal proofs the archive-placement decision owes: the blind travels
// INSIDE the v2 line, so (1) an offline verifier re-derives a new-rule hash from
// the segment alone, and (2) a crash-retry export pinned to the same in-flight
// boundary re-puts byte-identical segment bytes.
package audit_test

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/audit"
)

func TestBlindPlacementProof(t *testing.T) {
	ctx := context.Background()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatal(err)
	}
	st := signedStore(t, signer)
	tenant := provisionTenant(t, st)
	appendMetaEvents(t, st, tenant, 6, "blindproof")

	dir := t.TempDir()
	sink, err := audit.NewDirSink(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := audit.ExportSegments(ctx, st, tenant, sink,
		audit.ExportOptions{SegmentEvents: 100}, nil); err != nil {
		t.Fatalf("export: %v", err)
	}

	// PROOF 1 — the line carries the blind, and an offline verifier re-derives the
	// new-rule hash from the SEGMENT ALONE (no store, no side channel).
	var segPath string
	_ = filepath.Walk(dir, func(p string, fi os.FileInfo, _ error) error {
		if fi != nil && !fi.IsDir() && strings.HasSuffix(p, ".jsonl") {
			segPath = p
		}
		return nil
	})
	if segPath == "" {
		t.Fatal("no segment written")
	}
	raw, err := os.ReadFile(segPath)
	if err != nil {
		t.Fatal(err)
	}
	first := strings.SplitN(strings.TrimSpace(string(raw)), "\n", 2)[0]
	var line map[string]any
	if err := json.Unmarshal([]byte(first), &line); err != nil {
		t.Fatal(err)
	}
	blind, ok := line["meta_blind"].(string)
	if !ok || len(blind) != 64 {
		t.Fatalf("PROOF 1 FAILED: the v2 line carries no 32-byte meta_blind: %q", first)
	}
	t.Logf("PROOF 1a — the blind IS inside the line: meta_blind=%s (%d hex chars)", blind, len(blind))

	rep, err := audit.VerifyArchiveDir(ctx, dir, audit.ArchiveVerifyOptions{})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	t.Logf("PROOF 1b — offline re-derivation from the segment alone: OK=%v events=%d reason=%q breakAt=%d",
		rep.OK, rep.Events, rep.Reason, rep.BreakAt)
	if !rep.OK {
		t.Fatalf("PROOF 1 FAILED: %+v", rep)
	}

	// PROOF 2 — crash-retry determinism: the same in-flight boundary re-exports
	// BYTE-IDENTICAL segment bytes, which is what a WORM re-put depends on.
	dirA, dirB := t.TempDir(), t.TempDir()
	sinkA, err := audit.NewDirSink(dirA)
	if err != nil {
		t.Fatal(err)
	}
	sinkB, err := audit.NewDirSink(dirB)
	if err != nil {
		t.Fatal(err)
	}
	opts := audit.ExportOptions{SegmentEvents: 3, PendingToSeq: 3}
	if _, err := audit.ExportSegments(ctx, st, tenant, sinkA, opts, nil); err != nil {
		t.Fatalf("export A: %v", err)
	}
	if _, err := audit.ExportSegments(ctx, st, tenant, sinkB, opts, nil); err != nil {
		t.Fatalf("export B: %v", err)
	}
	readAll := func(root string) map[string]string {
		out := map[string]string{}
		_ = filepath.Walk(root, func(p string, fi os.FileInfo, _ error) error {
			if fi == nil || fi.IsDir() {
				return nil
			}
			b, rerr := os.ReadFile(p)
			if rerr != nil {
				return rerr
			}
			rel, _ := filepath.Rel(root, p)
			out[rel] = string(b)
			return nil
		})
		return out
	}
	a, b := readAll(dirA), readAll(dirB)
	if len(a) == 0 {
		t.Fatal("PROOF 2 FAILED: the pinned export wrote nothing")
	}
	for k, va := range a {
		vb, present := b[k]
		if !present {
			t.Fatalf("PROOF 2 FAILED: %q missing from the retry", k)
		}
		if va != vb {
			t.Fatalf("PROOF 2 FAILED: %q differs between the two runs", k)
		}
	}
	t.Logf("PROOF 2 — crash-retry with PendingToSeq=%d re-put %d object(s) BYTE-IDENTICAL", opts.PendingToSeq, len(a))
}
