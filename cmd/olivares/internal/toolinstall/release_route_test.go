// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall/toolinstalltest"
)

// CONTRACT on the builder, 09570b6b: Grok installed as "stable" and then asked for its
// exact version 1.0.46 was refused as damaged ("receipt plan digest ... does not match the
// current plan"). A release a person selects another way (its exact version, latest or
// stable) is the release already installed; only its route differs.

func installAs(t *testing.T, e *Engine, req RequestV2, version string) *ReceiptV2 {
	t.Helper()
	req.Version = version
	rec, _, err := e.InstallV2(context.Background(), req, nil, io.Discard)
	if err != nil {
		t.Fatalf("install as %q: %v", version, err)
	}
	return rec
}

func TestAReleaseReachedThroughAnotherRouteIsAlreadyInstalled(t *testing.T) {
	for _, driver := range []string{DriverCodex, DriverOpenCode} {
		for _, order := range [][]string{{"latest", "1.2.3", "stable"}, {"1.2.3", "latest"}} {
			t.Run(driver+"/"+strings.Join(order, "-then-"), func(t *testing.T) {
				engine, req, _ := archiveEngine(t, driver, archiveFixture(t, driver, nil), false)
				first := installAs(t, engine, req, order[0])
				for _, version := range order[1:] {
					if rec := installAs(t, engine, req, version); rec.InstalledAt != first.InstalledAt || rec.PlanDigest != first.PlanDigest {
						t.Fatalf("as %q: a second installation instead of the release in place", version)
					}
				}
			})
		}
	}
	t.Run("grok/stable-then-exact-then-latest", func(t *testing.T) {
		s := newTLSMap(t)
		s.put("/stable", []byte(grokFxVersion+"\n"))
		s.put("/latest", []byte(grokFxVersion+"\n"))
		s.put("/grok-"+grokFxVersion+"-linux-x86_64", toolinstalltest.VersionReporter("Grok", grokFxVersion, ""))
		eng, _, root := grokEngine(t, s)
		req := RequestV2{Driver: DriverGrok, Platform: PlatformV2{OS: "linux", Arch: "amd64"}, DestRoot: root, Source: s.url()}
		first := installAs(t, eng, req, ChannelStable)
		for _, version := range []string{grokFxVersion, ChannelLatest} {
			if rec := installAs(t, eng, req, version); rec.PlanDigest != first.PlanDigest {
				t.Fatalf("as %q: not the release in place", version)
			}
		}
	})
}

// The route is the only thing forgiven: a receipt from any other plan, or a changed
// executable, is still refused when the release is reached another way.
func TestAnotherRouteStillRefusesAForeignReceiptOrAChangedRelease(t *testing.T) {
	engine, req, _ := archiveEngine(t, DriverOpenCode, archiveFixture(t, DriverOpenCode, nil), false)
	rec := installAs(t, engine, req, ChannelLatest)
	receipt := filepath.Join(rec.Destination.ReleaseDir, ReceiptFile)
	b, err := os.ReadFile(receipt)
	if err != nil {
		t.Fatal(err)
	}
	foreign := strings.Replace(string(b), rec.PlanDigest, strings.Repeat("0", 64), 1)
	replaceFile(t, receipt, []byte(foreign))
	req.Version = "1.2.3"
	if _, _, err := engine.InstallV2(context.Background(), req, nil, io.Discard); KindOf(err) != KindDamaged || !strings.Contains(err.Error(), "does not match the current plan") {
		t.Fatalf("a foreign receipt reached by exact version: %v", err)
	}
	replaceFile(t, receipt, b)
	replaceFile(t, rec.Destination.Executable, []byte("#!/bin/sh\necho changed\n"))
	if _, _, err := engine.InstallV2(context.Background(), req, nil, io.Discard); KindOf(err) != KindDamaged {
		t.Fatalf("a changed executable reached by exact version: %v", err)
	}
}

// replaceFile writes b to path, lifting a read-only mode for the write.
func replaceFile(t *testing.T, path string, b []byte) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, fi.Mode().Perm()|0o200); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, fi.Mode().Perm()|0o200); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, fi.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
}
