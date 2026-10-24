// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
)

func activationWith(entries ...ActivationEntry) *ActivationManifest {
	return &ActivationManifest{Version: activationManifestVersion, Entries: entries}
}

var (
	reportingActive  = ActivationEntry{Addon: "reporting", Env: "OLIVARES_REPORTING_CONFIG", Value: "/x/reporting.json", State: ActivationActive}
	rtbfActive       = ActivationEntry{Addon: "rtbf-depth", Env: "OLIVARES_RTBF_CONFIG", Value: "/x/rtbf.json", State: ActivationActive}
	rtbfNeedsSecret  = ActivationEntry{Addon: "rtbf-depth", Env: "OLIVARES_RTBF_CONFIG", Value: "/x/rtbf.json", State: ActivationPending, NeedsSecret: true}
	productSettingsT = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
)

// bootForSettings starts a real engine on SQLite in a fresh data directory.
func bootForSettings(t *testing.T) (*engine, *productSettings) {
	t.Helper()
	prepareCompositionTestBoot(t)
	eng, err := boot(context.Background(), bootConfig{DataDir: t.TempDir(), Engine: "sqlite", Version: version, Logger: slog.Default()})
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	p := newProductSettings(eng.store, eng.dataDir)
	p.now = func() time.Time { return productSettingsT }
	return eng, p
}

func saveFile(t *testing.T, dir string, m *ActivationManifest) {
	t.Helper()
	if err := SaveActivationManifest(dir, m, productSettingsT); err != nil {
		t.Fatal(err)
	}
}

func operator(t *testing.T) auth.Principal {
	t.Helper()
	p, err := auth.NewSystemOperator("test:product-settings", "exercise the deployment settings")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// An install from before the record exists keeps its activation: the node's
// file is imported once, and nothing restarts.
func TestReconcileActivationImportsTheFileOnce(t *testing.T) {
	_, p := bootForSettings(t)
	saveFile(t, p.dataDir, activationWith(reportingActive))
	if err := reconcileActivation(context.Background(), p, slog.Default()); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	got, err := p.Activation(context.Background())
	if err != nil || !sameActivation(got, activationWith(reportingActive)) {
		t.Fatalf("record after import = %+v, %v", got, err)
	}
	if err := reconcileActivation(context.Background(), p, slog.Default()); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
}

// A record whose active modules differ from what this start built rewrites the
// node's file and asks for one restart before serving.
func TestReconcileActivationRestartsWhenActiveModulesChange(t *testing.T) {
	_, p := bootForSettings(t)
	saveFile(t, p.dataDir, activationWith(reportingActive))
	if err := p.SaveActivation(context.Background(), operator(t), activationWith(reportingActive, rtbfActive), nil); err != nil {
		t.Fatal(err)
	}
	saveFile(t, p.dataDir, activationWith(reportingActive)) // this node still holds the old copy
	err := reconcileActivation(context.Background(), p, slog.Default())
	var restart *selfRestartError
	if !errors.As(err, &restart) || !errors.Is(err, errSelfRestart) {
		t.Fatalf("reconcile = %v, want a restart", err)
	}
	if _, err := os.Stat(filepath.Join(p.dataDir, settingsRestartMarker)); err != nil {
		t.Fatalf("the restart for the settings left no marker: %v", err)
	}
	file, _ := LoadActivationManifest(p.dataDir)
	if !sameActivation(file, activationWith(reportingActive, rtbfActive)) {
		t.Fatalf("node file = %+v, want the record", file)
	}
	// The restarted start is in step and serves.
	if err := reconcileActivation(context.Background(), p, slog.Default()); err != nil {
		t.Fatalf("after restart: %v", err)
	}
}

// A change that leaves the active modules alone (an entry still waiting for a
// secret) refreshes the node's file without a restart.
func TestReconcileActivationRefreshesWithoutRestartWhenModulesAreUnchanged(t *testing.T) {
	_, p := bootForSettings(t)
	if err := p.SaveActivation(context.Background(), operator(t), activationWith(reportingActive, rtbfNeedsSecret), nil); err != nil {
		t.Fatal(err)
	}
	saveFile(t, p.dataDir, activationWith(reportingActive))
	if err := reconcileActivation(context.Background(), p, slog.Default()); err != nil {
		t.Fatalf("reconcile = %v, want no restart", err)
	}
	file, _ := LoadActivationManifest(p.dataDir)
	if !sameActivation(file, activationWith(reportingActive, rtbfNeedsSecret)) {
		t.Fatalf("node file = %+v", file)
	}
}

// A node that still differs after its one restart serves instead of looping.
func TestReconcileActivationRestartsOnlyOnce(t *testing.T) {
	_, p := bootForSettings(t)
	if err := p.SaveActivation(context.Background(), operator(t), activationWith(reportingActive), nil); err != nil {
		t.Fatal(err)
	}
	saveFile(t, p.dataDir, activationWith(rtbfActive))
	if err := os.WriteFile(filepath.Join(p.dataDir, settingsRestartMarker), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := reconcileActivation(context.Background(), p, slog.Default()); err != nil {
		t.Fatalf("reconcile after a restart = %v, want serve", err)
	}
}

// SaveActivation refuses a module outside the purchased set and writes nothing.
func TestSaveActivationRefusesWhatWasNotPurchased(t *testing.T) {
	_, p := bootForSettings(t)
	err := p.SaveActivation(context.Background(), operator(t), activationWith(rtbfActive), []string{"reporting"})
	if !errors.Is(err, ErrModuleNotPurchased) {
		t.Fatalf("SaveActivation = %v", err)
	}
	if _, found, _ := p.load(context.Background()); found {
		t.Fatal("a refused activation was recorded")
	}
}

func TestSelfRestartRequestDrainsOnceAndNamesTheFirstReason(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &selfRestart{cancel: cancel}
	if err := r.Request("license activation"); err != nil {
		t.Fatal(err)
	}
	_ = r.Request("second")
	if ctx.Err() == nil || r.requested() != "license activation" {
		t.Fatalf("ctx=%v reason=%q", ctx.Err(), r.requested())
	}
	if (*selfRestart)(nil).Request("x") == nil || restartRequester(nil) != nil {
		t.Fatal("a CLI boot must offer no restart")
	}
}

// A file changed on this node after the record (an offline `olivares enterprise
// enable`) is recorded, not overwritten, and does not restart: this start was
// already built from it.
func TestReconcileActivationRecordsANewerNodeFile(t *testing.T) {
	_, p := bootForSettings(t)
	if err := p.SaveActivation(context.Background(), operator(t), activationWith(reportingActive), nil); err != nil {
		t.Fatal(err)
	}
	if err := SaveActivationManifest(p.dataDir, activationWith(reportingActive, rtbfActive), productSettingsT.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := reconcileActivation(context.Background(), p, slog.Default()); err != nil {
		t.Fatalf("reconcile = %v, want no restart", err)
	}
	got, _ := p.Activation(context.Background())
	if !sameActivation(got, activationWith(reportingActive, rtbfActive)) {
		t.Fatalf("record = %+v, want the newer node file", got)
	}
}

// fakeActivation writes the node file the way the edition's service does.
type fakeActivation struct {
	api.ActivationService
	dir string
	m   *ActivationManifest
	// live says the edition applied the change itself and needs no restart.
	live bool
}

func (f fakeActivation) ActivationApply(context.Context, api.ActivationApplyRequest) (api.ActivationStatusDTO, error) {
	return api.ActivationStatusDTO{Edition: "business", RestartRequired: !f.live}, SaveActivationManifest(f.dir, f.m, productSettingsT)
}

// A console activation is recorded for the deployment and restarts the engine.
func TestConsoleActivationIsRecordedAndRestartsTheEngine(t *testing.T) {
	_, p := bootForSettings(t)
	var reason string
	svc := recordingActivation(fakeActivation{dir: p.dataDir, m: activationWith(reportingActive, rtbfActive)}, p,
		func(r string) error { reason = r; return nil }, nil, slog.Default())
	dto, err := svc.ActivationApply(context.Background(), api.ActivationApplyRequest{Action: "enable", Preset: "full"})
	if err != nil || !dto.Restarting || reason != "license activation: enable" {
		t.Fatalf("apply = %+v %v, restart reason %q", dto, err, reason)
	}
	got, _ := p.Activation(context.Background())
	if !sameActivation(got, activationWith(reportingActive, rtbfActive)) {
		t.Fatalf("record = %+v", got)
	}
	if recordingActivation(nil, p, nil, nil, slog.Default()) != nil {
		t.Fatal("the community build must keep a nil activation service")
	}
}

// A restart that cannot happen is a failure the API reports, not a success.
func TestConsoleActivationReportsARestartItCannotDo(t *testing.T) {
	_, p := bootForSettings(t)
	svc := recordingActivation(fakeActivation{dir: p.dataDir, m: activationWith(reportingActive)}, p,
		func(string) error { return errors.New("not serving") }, nil, slog.Default())
	if _, err := svc.ActivationApply(context.Background(), api.ActivationApplyRequest{Action: "enable"}); !errors.Is(err, api.ErrActivationRestartUnavailable) {
		t.Fatalf("apply = %v, want ErrActivationRestartUnavailable", err)
	}
	svc = recordingActivation(fakeActivation{dir: p.dataDir, m: activationWith(reportingActive)}, p, nil, nil, slog.Default())
	if _, err := svc.ActivationApply(context.Background(), api.ActivationApplyRequest{Action: "enable"}); !errors.Is(err, api.ErrActivationRestartUnavailable) {
		t.Fatalf("apply without a restarter = %v", err)
	}
}

// A change the edition applied itself needs no restart: the record is written and
// the running engine follows it, so nothing stops and no session is cut.
func TestConsoleActivationThatNeedsNoRestartIsAppliedLive(t *testing.T) {
	_, p := bootForSettings(t)
	var restarted, applied bool
	svc := recordingActivation(fakeActivation{dir: p.dataDir, m: activationWith(reportingActive), live: true}, p,
		func(string) error { restarted = true; return nil },
		func(context.Context) error { applied = true; return nil }, slog.Default())
	dto, err := svc.ActivationApply(context.Background(), api.ActivationApplyRequest{Action: "enable", Preset: "business"})
	if err != nil || restarted || dto.Restarting || dto.RestartRequired || !applied {
		t.Fatalf("apply = %+v %v, restarted=%v applied=%v: want applied live, no restart", dto, err, restarted, applied)
	}
	got, _ := p.Activation(context.Background())
	if !sameActivation(got, activationWith(reportingActive)) {
		t.Fatalf("record = %+v", got)
	}
}

// A live application that fails falls back to the restart, which rebuilds the
// engine from the record: the console is never told "applied" for a change the
// engine does not follow.
func TestConsoleActivationFallsBackToTheRestartWhenLiveApplyFails(t *testing.T) {
	_, p := bootForSettings(t)
	var reason string
	svc := recordingActivation(fakeActivation{dir: p.dataDir, m: activationWith(reportingActive), live: true}, p,
		func(r string) error { reason = r; return nil },
		func(context.Context) error { return errors.New("module did not start") }, slog.Default())
	dto, err := svc.ActivationApply(context.Background(), api.ActivationApplyRequest{Action: "enable"})
	if err != nil || !dto.Restarting || reason != "license activation: enable" {
		t.Fatalf("apply = %+v %v, restart reason %q: want the restart fallback", dto, err, reason)
	}
}

// An edition that says the change needs a restart gets one, even when a live path exists:
// the live path is only for changes the edition applied itself.
func TestConsoleActivationThatNeedsARestartIsNotAppliedLive(t *testing.T) {
	_, p := bootForSettings(t)
	var reason string
	appliedLive := false
	svc := recordingActivation(fakeActivation{dir: p.dataDir, m: activationWith(reportingActive)}, p,
		func(r string) error { reason = r; return nil },
		func(context.Context) error { appliedLive = true; return nil }, slog.Default())
	dto, err := svc.ActivationApply(context.Background(), api.ActivationApplyRequest{Action: "enable"})
	if err != nil || !dto.Restarting || appliedLive || reason == "" {
		t.Fatalf("apply = %+v %v, live=%v, restart reason %q: want the restart and no live apply", dto, err, appliedLive, reason)
	}
}

// When the live path fails and the engine cannot restart itself, the console is told both
// facts: the change is recorded, and why it is not running.
func TestConsoleActivationReportsALiveFailureThatCannotRestart(t *testing.T) {
	_, p := bootForSettings(t)
	svc := recordingActivation(fakeActivation{dir: p.dataDir, m: activationWith(reportingActive), live: true}, p, nil,
		func(context.Context) error { return errors.New("compliance did not start") }, slog.Default())
	_, err := svc.ActivationApply(context.Background(), api.ActivationApplyRequest{Action: "enable"})
	if !errors.Is(err, api.ErrActivationRestartUnavailable) || !strings.Contains(err.Error(), "compliance did not start") {
		t.Fatalf("apply = %v, want ErrActivationRestartUnavailable naming the live failure", err)
	}
}

// A manifest that something other than the running engine wrote is not what the engine
// reads: nothing applied it.
func TestActivationOverlayMatchesOnlyWhatTheEngineWasGiven(t *testing.T) {
	t.Cleanup(func() { setActivationOverlayForTest(nil) })
	m := activationWith(reportingActive)
	setActivationOverlayForTest(nil)
	if activationOverlayMatches(m) {
		t.Fatal("an overlay that is empty matched a manifest with an active add-on")
	}
	publishActivationOverlay(m)
	if !activationOverlayMatches(m) {
		t.Fatal("the overlay the engine published did not match its own manifest")
	}
	if activationOverlayMatches(&ActivationManifest{}) {
		t.Fatal("an empty manifest matched an overlay with an add-on")
	}
	publishActivationOverlay(&ActivationManifest{})
	if !activationOverlayMatches(&ActivationManifest{}) {
		t.Fatal("an empty overlay did not match an empty manifest")
	}
}
