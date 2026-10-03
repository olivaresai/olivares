// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"context"
	"testing"
)

// HU-R13 (2026-10-01, real use on refresh-06): the OpenCode install picked the
// musl build on a glibc host and the probe failed "fork/exec .../bin/opencode:
// no such file or directory" — the musl package is NOT static, it needs the
// musl loader. The rule: on Linux choose the glibc build unless the host has NO
// glibc loader AND has a musl loader, decided from the actual loader files.

func pinLoaders(t *testing.T, glibc, musl bool) {
	t.Helper()
	loaderFilesExist = func() (bool, bool) { return glibc, musl }
	t.Cleanup(func() { loaderFilesExist = hostLoaderPresence })
}

func TestHostLibcChoosesGlibcUnlessOnlyMuslLoaderExists(t *testing.T) {
	for _, tc := range []struct {
		name        string
		glibc, musl bool
		want        string
	}{
		{"a plain glibc host", true, false, "glibc"},
		{"a glibc host with a musl loader beside it", true, true, "glibc"},
		{"alpine: no glibc loader, a musl loader", false, true, "musl"},
		{"no loader of either kind answers glibc (the default, and the honest choice to try)", false, false, "glibc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pinLoaders(t, tc.glibc, tc.musl)
			if got := hostLibc(); got != tc.want {
				t.Fatalf("hostLibc(glibc=%v, musl=%v) = %q, want %q", tc.glibc, tc.musl, got, tc.want)
			}
		})
	}
}

func TestOpenCodePlatformPicksTheBuildTheHostCanExec(t *testing.T) {
	for _, tc := range []struct {
		arch        string
		glibc, musl bool
		wantName    string
		wantLibc    string
	}{
		{"amd64", true, false, "linux-x64-baseline", "glibc"},
		{"amd64", true, true, "linux-x64-baseline", "glibc"},
		{"amd64", false, true, "linux-x64-baseline-musl", "musl"},
		{"arm64", true, false, "linux-arm64", "glibc"},
		{"arm64", true, true, "linux-arm64", "glibc"},
		{"arm64", false, true, "linux-arm64-musl", "musl"},
	} {
		t.Run(tc.arch+"/"+tc.wantLibc, func(t *testing.T) {
			pinLoaders(t, tc.glibc, tc.musl)
			p, name, err := PlatformV2For(DriverOpenCode, Platform{OS: "linux", Arch: tc.arch})
			if err != nil {
				t.Fatalf("PlatformV2For: %v", err)
			}
			if name != tc.wantName {
				t.Fatalf("artifact = %q, want %q", name, tc.wantName)
			}
			if p.Libc != tc.wantLibc {
				t.Fatalf("Libc = %q, want %q", p.Libc, tc.wantLibc)
			}
		})
	}
}

// HU 042 (09b real use): Console › AI tools › OpenCode › Review install answered
// 400 "codex platform libc must be musl, got "glibc"" on every glibc host:
// PlatformV2For picks OpenCode's glibc build (HU-R13) but the selection's
// platform check still grouped OpenCode with Codex. This plans the way POST
// /v1/m/agenttools/plans does (PlatformV2For on the host, then PlanV2): a plan,
// not a refusal, on a glibc host and on a musl host.
func TestOpenCodePlansOnGlibcAndMuslHosts(t *testing.T) {
	for _, tc := range []struct {
		name        string
		glibc, musl bool
		wantLibc    string
		wantVendor  string
	}{
		{"glibc host", true, false, "glibc", "linux-x64-baseline"},
		{"musl host", false, true, "musl", "linux-x64-baseline-musl"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pinLoaders(t, tc.glibc, tc.musl)
			engine, req, _ := archiveEngine(t, DriverOpenCode, archiveFixture(t, DriverOpenCode, nil), false)
			platform, vendor, err := PlatformV2For(DriverOpenCode, Platform{OS: "linux", Arch: "amd64"})
			if err != nil || vendor != tc.wantVendor {
				t.Fatalf("PlatformV2For = %+v %q %v, want the %s build", platform, vendor, err, tc.wantVendor)
			}
			req.Platform = platform
			plan, err := engine.PlanV2(context.Background(), req)
			if err != nil {
				t.Fatalf("plan OpenCode on a %s: %v", tc.name, err)
			}
			if plan.Selection.Platform.Libc != tc.wantLibc || plan.Selection.VendorPlatform != tc.wantVendor {
				t.Fatalf("selection platform %+v vendor %q, want libc %s vendor %s", plan.Selection.Platform, plan.Selection.VendorPlatform, tc.wantLibc, tc.wantVendor)
			}
		})
	}
}

// Each driver's libc rule for a selection: Codex is the static musl package
// everywhere, OpenCode the build the host's loader runs, Grok and Ollama none.
func TestPlatformV2LibcRulePerDriver(t *testing.T) {
	for _, tc := range []struct {
		driver, libc string
		ok           bool
	}{
		{DriverCodex, "musl", true},
		{DriverCodex, "glibc", false},
		{DriverCodex, "", false},
		{DriverOpenCode, "glibc", true},
		{DriverOpenCode, "musl", true},
		{DriverOpenCode, "", false},
		{DriverOpenCode, "uclibc", false},
		{DriverGrok, "", true},
		{DriverGrok, "musl", false},
		{DriverOllama, "", true},
		{DriverOllama, "glibc", false},
	} {
		err := PlatformV2{OS: "linux", Arch: "amd64", Libc: tc.libc}.validate(tc.driver)
		if (err == nil) != tc.ok {
			t.Errorf("%s libc %q: err = %v, want ok=%v", tc.driver, tc.libc, err, tc.ok)
		}
	}
}
