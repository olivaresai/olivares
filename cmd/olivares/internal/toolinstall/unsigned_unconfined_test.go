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
	"github.com/olivaresai/olivares/core/driverfacts"
	"github.com/olivaresai/olivares/modules/sessions/confine"
)

// stubNoConfinement makes the engine read this host as one that cannot confine
// a child, as a node without Landlock does (#3312).
func stubNoConfinement(t *testing.T) {
	t.Helper()
	previous := hostConfinement
	hostConfinement = func() confine.State {
		return confine.State{Mode: confine.ModeNone, Reason: "landlock unavailable in this test"}
	}
	t.Cleanup(func() { hostConfinement = previous })
}

func stubLandlock(t *testing.T) {
	t.Helper()
	previous := hostConfinement
	hostConfinement = func() confine.State { return confine.State{Mode: confine.ModeLandlock, ABI: 1} }
	t.Cleanup(func() { hostConfinement = previous })
}

// The first run of an unsigned install (none-origin-only, origin-tarball) is
// its --version probe. On a node that cannot confine a child, the engine
// refuses that probe instead of running unverified vendor bytes with the
// engine user's rights; a signed install probes as before.
func TestUnsignedInstallProbeRefusedWhileUnconfined(t *testing.T) {
	t.Run("grok (none-origin-only) refused, nothing executed", func(t *testing.T) {
		s := newTLSMap(t)
		marker := filepath.Join(toolinstalltest.ExecCapableDir(t), "ran.marker")
		s.put("/latest", []byte(grokFxVersion+"\n"))
		s.put("/grok-"+grokFxVersion+"-linux-x86_64", toolinstalltest.VersionReporter("Grok", grokFxVersion, marker))
		eng, _, root := grokEngine(t, s)
		children := recordChildren(eng)
		stubNoConfinement(t)
		req := RequestV2{Driver: DriverGrok, Version: ChannelLatest, Platform: PlatformV2{OS: "linux", Arch: "amd64"}, DestRoot: root, Source: s.url()}
		_, _, err := eng.InstallV2(context.Background(), req, nil, nil)
		requireKind(t, err, KindVerificationUnavailable)
		for _, want := range []string{"grok", "publisher signature", "Landlock", "olivares doctor"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("refusal %q lacks %q", err, want)
			}
		}
		if _, rerr := os.Stat(marker); !os.IsNotExist(rerr) {
			t.Errorf("the staged grok bytes executed: marker stat = %v", rerr)
		}
		if len(*children) != 0 {
			t.Errorf("a child was built for the refused probe: %+v", *children)
		}
		if inv, lerr := eng.List(context.Background(), root); lerr != nil || len(inv.Installed) != 0 {
			t.Errorf("a refused install left releases behind: %+v %v", inv.Installed, lerr)
		}
	})
	t.Run("cursor (origin-tarball) refused, nothing executed", func(t *testing.T) {
		s, _ := cursorFxServe(t, cursorFxMembers(cursorFxVersion))
		eng, root := cursorFxEngine(t, s)
		children := recordChildren(eng)
		stubNoConfinement(t)
		_, _, err := eng.InstallV2(context.Background(), cursorFxRequest(root), nil, nil)
		requireKind(t, err, KindVerificationUnavailable)
		if !strings.Contains(err.Error(), "Landlock") {
			t.Errorf("refusal %q does not name Landlock", err)
		}
		if len(*children) != 0 {
			t.Errorf("a child was built for the refused probe: %+v", *children)
		}
		if inv, lerr := eng.List(context.Background(), root); lerr != nil || len(inv.Installed) != 0 {
			t.Errorf("a refused install left releases behind: %+v %v", inv.Installed, lerr)
		}
	})
	t.Run("claude (openpgp) unchanged", func(t *testing.T) {
		f := newFixture(t)
		f.publish(fxVersion)
		stubNoConfinement(t)
		if _, _, err := f.install(fxVersion); err != nil {
			t.Fatalf("a signed install must probe as before: %v", err)
		}
		if f.ran() != 1 {
			t.Errorf("the signed probe ran %d times, want 1", f.ran())
		}
	})
	t.Run("ollama (github-release-sha256) unchanged", func(t *testing.T) {
		engine, req, _ := archiveEngine(t, DriverOllama, archiveFixture(t, DriverOllama, nil), false)
		stubNoConfinement(t)
		plan, err := engine.PlanV2(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := engine.InstallV2(context.Background(), req, plan, io.Discard); err != nil {
			t.Fatalf("a digest-verified release archive must probe as before: %v", err)
		}
	})
	t.Run("a registered grok release is not probed by detection either", func(t *testing.T) {
		s := newTLSMap(t)
		s.put("/latest", []byte(grokFxVersion+"\n"))
		s.put("/grok-"+grokFxVersion+"-linux-x86_64", toolinstalltest.VersionReporter("Grok", grokFxVersion, ""))
		eng, _, root := grokEngine(t, s)
		req := RequestV2{Driver: DriverGrok, Version: ChannelLatest, Platform: PlatformV2{OS: "linux", Arch: "amd64"}, DestRoot: root, Source: s.url()}
		if _, _, err := eng.InstallV2(context.Background(), req, nil, nil); err != nil {
			t.Fatal(err)
		}
		stubNoConfinement(t)
		cands, err := eng.Detect(context.Background(), DetectOptions{Driver: DriverGrok, Root: root, Home: t.TempDir(), Probe: true})
		if err == nil || !strings.Contains(err.Error(), "refused") {
			t.Fatalf("detect = %v, want the requested probe refused", err)
		}
		if len(cands) != 1 || cands[0].Match != MatchRegistered || cands[0].Probe != nil {
			t.Fatalf("detect = %+v, want one registered candidate that was not probed", cands)
		}
		if !strings.Contains(cands[0].ProbeSkipped, "Landlock") {
			t.Errorf("probe skipped %q does not name Landlock", cands[0].ProbeSkipped)
		}
	})
}

func TestUnsignedInstallRefusedWithoutConfinedBuilder(t *testing.T) {
	s := newTLSMap(t)
	marker := filepath.Join(toolinstalltest.ExecCapableDir(t), "ran.marker")
	s.put("/latest", []byte(grokFxVersion+"\n"))
	s.put("/grok-"+grokFxVersion+"-linux-x86_64", toolinstalltest.VersionReporter("Grok", grokFxVersion, marker))
	eng, _, root := grokEngine(t, s)
	eng.SetChildCommand(nil)
	stubLandlock(t)
	req := RequestV2{Driver: DriverGrok, Version: ChannelLatest, Platform: PlatformV2{OS: "linux", Arch: "amd64"}, DestRoot: root, Source: s.url()}
	_, _, err := eng.InstallV2(context.Background(), req, nil, nil)
	requireKind(t, err, KindVerificationUnavailable)
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("the unconfined grok bytes executed: marker stat=%v", statErr)
	}
}

func TestUnsignedInstallSuccessFixturesAreHermetic(t *testing.T) {
	s := newTLSMap(t)
	for _, eng := range []*Engine{func() *Engine { e, _, _ := grokEngine(t, s); return e }(), func() *Engine { e, _ := cursorFxEngine(t, s); return e }()} {
		if eng.childCommand.Load() == nil {
			t.Error("unsigned success fixture has no confined child builder")
		}
	}
	if got := hostConfinement().Mode; got != confine.ModeLandlock {
		t.Errorf("unsigned success fixture host mode = %s, want an explicit Landlock stub", got)
	}
}

// The node-wide rule reads two fixed tables: the engine adapters' verification
// levels (RefuseUnconfinedUnsigned, used by the probes, logins and status
// reads) and core/driverfacts installers (the sessions model-catalog gate).
// They must name the same unsigned tools; a driver that drifts between the
// tables, an adapter without VerificationLevel, or a catalog driver missing
// from driverfacts would silently lose one of the two refusals.
func TestUnsignedKindsAgreeWithDriverFacts(t *testing.T) {
	e := NewEngineWithCapabilities(NewOfficialCatalog(nil, UnavailableVerifier{}), EngineOptions{})
	levels := e.VerificationLevels()
	if len(levels) == 0 {
		t.Fatal("the official catalog has no drivers")
	}
	for driver, level := range levels {
		facts, ok := driverfacts.Lookup(driver)
		if !ok {
			t.Fatalf("%s is in the catalog but missing from driverfacts", driver)
		}
		unsignedLevel := level == VerificationNoneOriginOnly || level == VerificationOriginTarball
		unsignedInstaller := facts.Installer == driverfacts.InstallPackage || facts.Installer == driverfacts.InstallOriginTarball
		if unsignedLevel != unsignedInstaller {
			t.Fatalf("%s: verification level %q and installer %q disagree on which rule refuses it", driver, level, facts.Installer)
		}
	}
}
