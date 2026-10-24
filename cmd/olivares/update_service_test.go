// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The service runs `olivares upgrade` and reads its -o json document, so these tests feed
// it the real upgradeResult type: a renamed field there breaks this mapping here.

type fakeUpgrade struct {
	calls   [][]string
	ctxErrs []error // the context error each run saw
	doc     upgradeResult
	err     error
	// applyErr fails only the run that installs (--yes); the check after it succeeds.
	applyErr error
}

func (f *fakeUpgrade) run(ctx context.Context, args []string) ([]byte, error) {
	f.calls = append(f.calls, slices.Clone(args))
	f.ctxErrs = append(f.ctxErrs, ctx.Err())
	if f.err != nil {
		return nil, f.err
	}
	if f.applyErr != nil && slices.Contains(args, "--yes") {
		return nil, f.applyErr
	}
	return json.Marshal(f.doc)
}

func newTestUpdateService(t *testing.T, f *fakeUpgrade, restart func(string) error) updateService {
	t.Helper()
	return updateService{
		run: f.run, version: "0.2", edition: "community", dataDir: "/data",
		getenv: func(string) string { return "" }, restart: restart,
		sessions: func() int { return 2 }, log: slog.Default(),
	}
}

func availableDoc() upgradeResult {
	return upgradeResult{
		Action: upgradeActionChecked, Status: upgradeStatusAvailable, Channel: "stable",
		Current: "0.2", Available: "0.3", ReleasedAt: "2026-10-16T00:00:00Z",
		Notes: "Adds the Updates page.", Security: true, Advisories: []string{"CVE-2026-0001"},
	}
}

func TestUpdateStatusReportsTheNewestReleaseFromUpgradeCheck(t *testing.T) {
	f := &fakeUpgrade{doc: availableDoc()}
	got, err := newTestUpdateService(t, f, nil).UpdateStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := api.UpdateStatusDTO{
		Edition: "community", Version: "0.2", Channel: "stable", Available: "0.3", UpdateAvailable: true,
		ReleasedAt: "2026-10-16T00:00:00Z", Notes: "Adds the Updates page.", Security: true,
		Advisories: []string{"CVE-2026-0001"}, RunningSessions: 2,
	}
	if !equalUpdateDTO(got, want) {
		t.Fatalf("status = %+v, want %+v", got, want)
	}
	if len(f.calls) != 1 || !slices.Contains(f.calls[0], "--check") || slices.Contains(f.calls[0], "--yes") {
		t.Fatalf("status ran %v, want one `upgrade --check` and no install", f.calls)
	}
	for _, flag := range []string{"upgrade", "-o", "json", "--data-dir", "/data"} {
		if !slices.Contains(f.calls[0], flag) {
			t.Errorf("args %v lack %q", f.calls[0], flag)
		}
	}
}

func TestUpdateStatusOnlyOffersAnUpdateWhenTheReleaseIsNewer(t *testing.T) {
	for _, status := range []string{upgradeStatusUpToDate, upgradeStatusDowngrade, upgradeStatusUnknown} {
		t.Run(status, func(t *testing.T) {
			doc := availableDoc()
			doc.Status = status
			got, err := newTestUpdateService(t, &fakeUpgrade{doc: doc}, nil).UpdateStatus(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got.UpdateAvailable {
				t.Fatalf("status %q offered an update; only upgrade-available may", status)
			}
		})
	}
}

func TestUpdateStatusKeepsTheInstalledVersionWhenTheChannelCannotBeChecked(t *testing.T) {
	f := &fakeUpgrade{err: errors.New("fetch manifest: 404 Not Found")}
	got, err := newTestUpdateService(t, f, nil).UpdateStatus(context.Background())
	if err != nil {
		t.Fatalf("an unreachable channel must not fail the page: %v", err)
	}
	if got.Version != "0.2" || got.Edition != "community" || got.UpdateAvailable {
		t.Fatalf("status = %+v, want the installed version and no update", got)
	}
	if !strings.Contains(got.CheckError, "404 Not Found") {
		t.Fatalf("check_error = %q, want upgrade's own reason", got.CheckError)
	}
}

func TestUpdateRunsUpgradeAgainstTheConfiguredChannelAndEdition(t *testing.T) {
	env := map[string]string{"OLIVARES_UPDATE_ENDPOINT": "https://mirror.example/olivares", "OLIVARES_UPDATE_CHANNEL": "security"}
	for _, tc := range []struct {
		name, edition string
		connected     bool
		noEndpoint    bool // the public-channel endpoint is not in the environment
		want          []string
		absent        []string
	}{
		{"community", "community", false, false, []string{"--endpoint", "https://mirror.example/olivares", "--channel", "security"}, []string{"--enterprise", "--connect"}},
		// A connected Business build names its endpoint (the connect state binds the origin) and connects.
		{"business, connected", "enterprise", true, false, []string{"--endpoint", "https://mirror.example/olivares", "--channel", "security", "--enterprise", "--connect"}, nil},
		// Not connected: upgrade takes OLIVARES_UPGRADE_TOKEN from the engine's environment, so a token
		// alone also updates, and it is sent to the license service, never to a public-channel endpoint.
		{"business, by token alone", "enterprise", false, true, []string{"--channel", "security", "--enterprise"}, []string{"--connect", "--endpoint"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeUpgrade{doc: availableDoc()}
			svc := newTestUpdateService(t, f, nil)
			svc.edition = tc.edition
			if tc.connected {
				svc.dataDir = connectedDir(t)
			}
			svc.getenv = func(k string) string {
				if tc.noEndpoint && k == "OLIVARES_UPDATE_ENDPOINT" {
					return ""
				}
				return env[k]
			}
			if _, err := svc.UpdateStatus(context.Background()); err != nil {
				t.Fatal(err)
			}
			args := strings.Join(f.calls[0], " ")
			for _, w := range tc.want {
				if !strings.Contains(args, w) {
					t.Errorf("args %q lack %q", args, w)
				}
			}
			for _, a := range tc.absent {
				if strings.Contains(args, a) {
					t.Errorf("args %q carry %q", args, a)
				}
			}
		})
	}
}

func TestApplyUpdateInstallsThenRestartsAndAudits(t *testing.T) {
	_, p := bootForSettings(t)
	doc := availableDoc()
	doc.Action, doc.Installed = upgradeActionInstalled, "olivares 0.3 (commit abc, built now)"
	f := &fakeUpgrade{doc: doc}
	var restartReason string
	svc := newTestUpdateService(t, f, func(r string) error { restartReason = r; return nil })
	svc.settings = p

	got, err := svc.ApplyUpdate(context.Background(), operator(t))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Restarting || got.Available != "0.3" || got.Version != "0.2" {
		t.Fatalf("apply = %+v, want restarting on 0.3 from 0.2", got)
	}
	if len(f.calls) != 1 || !slices.Contains(f.calls[0], "--yes") || slices.Contains(f.calls[0], "--check") {
		t.Fatalf("apply ran %v, want one `upgrade --yes`", f.calls)
	}
	if !strings.Contains(restartReason, "0.3") {
		t.Fatalf("restart reason = %q, want the version it restarts on", restartReason)
	}
	ev, ok := auditedUpdate(t, p.st)
	if !ok {
		t.Fatal("the installed update left no audit event")
	}
	// The ledger blinds event metadata, so the record is read by who asked and what was done.
	if want := operator(t); ev.Actor != want.Actor() || ev.TargetKind != "core.engine_binary" {
		t.Fatalf("audit event = actor %q target %q, want actor %q on core.engine_binary", ev.Actor, ev.TargetKind, want.Actor())
	}
}

func TestApplyUpdateWithNothingNewerInstallsAndRestartsNothing(t *testing.T) {
	doc := availableDoc()
	doc.Action, doc.Status = upgradeActionUpToDate, upgradeStatusUpToDate
	restarts := 0
	svc := newTestUpdateService(t, &fakeUpgrade{doc: doc}, func(string) error { restarts++; return nil })
	got, err := svc.ApplyUpdate(context.Background(), operator(t))
	if err != nil {
		t.Fatal(err)
	}
	if got.Restarting || restarts != 0 {
		t.Fatalf("restarting=%v restarts=%d for an up-to-date engine", got.Restarting, restarts)
	}
}

func TestApplyUpdateRefusedByUpgradeInstallsAndRestartsNothing(t *testing.T) {
	restarts := 0
	f := &fakeUpgrade{err: errors.New("/usr/bin is not writable")}
	svc := newTestUpdateService(t, f, func(string) error { restarts++; return nil })
	_, err := svc.ApplyUpdate(context.Background(), operator(t))
	if !errors.Is(err, api.ErrUpdateRefused) || !strings.Contains(err.Error(), "not writable") {
		t.Fatalf("err = %v, want ErrUpdateRefused carrying upgrade's reason", err)
	}
	if restarts != 0 {
		t.Fatal("a refused update restarted the engine")
	}
}

func TestApplyUpdateThatCannotRestartSaysTheBinaryIsInstalled(t *testing.T) {
	_, p := bootForSettings(t)
	doc := availableDoc()
	doc.Action, doc.Installed = upgradeActionInstalled, "olivares 0.3 (commit abc, built now)"
	for name, restart := range map[string]func(string) error{
		"no restarter":    nil,
		"restart refused": func(string) error { return errors.New("this platform cannot re-execute") },
	} {
		t.Run(name, func(t *testing.T) {
			svc := newTestUpdateService(t, &fakeUpgrade{doc: doc}, restart)
			svc.settings = p
			if _, err := svc.ApplyUpdate(context.Background(), operator(t)); !errors.Is(err, api.ErrUpdateRestartUnavailable) {
				t.Fatalf("err = %v, want ErrUpdateRestartUnavailable", err)
			}
		})
	}
}

func equalUpdateDTO(a, b api.UpdateStatusDTO) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func auditedEvents(t *testing.T, st store.Store) []model.AuditEvent {
	t.Helper()
	var events []model.AuditEvent
	err := st.AuthView(context.Background(), func(as store.AuthScope) error {
		return as.Audit().Walk(context.Background(), 0, func(ev model.AuditEvent) error {
			events = append(events, ev)
			return nil
		})
	})
	if err != nil {
		t.Fatalf("walk the audit ledger: %v", err)
	}
	return events
}

func auditedUpdate(t *testing.T, st store.Store) (model.AuditEvent, bool) {
	t.Helper()
	for _, ev := range auditedEvents(t, st) {
		if ev.Action == updateApplyAction {
			return ev, true
		}
	}
	return model.AuditEvent{}, false
}

func TestUpgradeFailureReasonIsTheCommandsOwnMessage(t *testing.T) {
	stderr := "OTA key: embedded (fingerprint ab12)\nsource: public channel\n" +
		`{"error":{"message":"REFUSING to upgrade: manifest signature does not verify"}}` + "\n"
	if got := upgradeFailureReason(stderr, errors.New("exit status 1")); got != "REFUSING to upgrade: manifest signature does not verify" {
		t.Fatalf("reason = %q", got)
	}
	if got := upgradeFailureReason("Error: plain text\n", errors.New("exit status 1")); got != "Error: plain text" {
		t.Fatalf("plain reason = %q", got)
	}
	if got := upgradeFailureReason("", errors.New("exit status 1")); got != "exit status 1" {
		t.Fatalf("empty stderr reason = %q", got)
	}
	if got := upgradeFailureReason(strings.Repeat("x", 5000), errors.New("e")); len(got) > maxUpgradeReason+len("…") {
		t.Fatalf("reason is %d bytes, want it bounded", len(got))
	}
}

func TestApplyUpdateFinishesWhenTheAdministratorsRequestIsCancelled(t *testing.T) {
	_, p := bootForSettings(t)
	doc := availableDoc()
	doc.Action, doc.Installed = upgradeActionInstalled, "olivares 0.3 (commit abc, built now)"
	f := &fakeUpgrade{doc: doc}
	restarted := false
	svc := newTestUpdateService(t, f, func(string) error { restarted = true; return nil })
	svc.settings = p
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := svc.ApplyUpdate(ctx, operator(t)); err != nil {
		t.Fatal(err)
	}
	if f.ctxErrs[0] != nil || !restarted {
		t.Fatalf("run saw ctx error %v, restarted=%v: a dropped connection must not abandon an install", f.ctxErrs[0], restarted)
	}
}

func TestUpdateStatusBoundsTheNetworkCheck(t *testing.T) {
	f := &fakeUpgrade{doc: availableDoc()}
	if _, err := newTestUpdateService(t, f, nil).UpdateStatus(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.calls[0], "--timeout") {
		t.Fatalf("status ran %v with no network timeout", f.calls[0])
	}
}

func TestUpdateStatusRefusesAnEmptyUpgradeDocument(t *testing.T) {
	got, err := newTestUpdateService(t, &fakeUpgrade{doc: upgradeResult{}}, nil).UpdateStatus(context.Background())
	if err != nil || got.CheckError == "" || got.UpdateAvailable {
		t.Fatalf("status = %+v, %v: a document with no status must read as a failed check, not as up to date", got, err)
	}
}

func TestUpdateStatusSaysAnInstalledBinaryAwaitsItsRestart(t *testing.T) {
	doc := availableDoc()
	doc.Status, doc.Current = upgradeStatusUpToDate, "0.3"
	got, err := newTestUpdateService(t, &fakeUpgrade{doc: doc}, nil).UpdateStatus(context.Background())
	if err != nil || !got.RestartRequired || got.UpdateAvailable {
		t.Fatalf("status = %+v, %v, want restart_required and no update on offer", got, err)
	}
	doc.Current = "0.2"
	got, _ = newTestUpdateService(t, &fakeUpgrade{doc: doc}, nil).UpdateStatus(context.Background())
	if got.RestartRequired {
		t.Fatal("restart_required while the disk and the engine agree")
	}
}

func TestUpdateRefusesAnEndpointThatCarriesCredentials(t *testing.T) {
	f := &fakeUpgrade{doc: availableDoc()}
	svc := newTestUpdateService(t, f, nil)
	svc.getenv = func(k string) string {
		if k == "OLIVARES_UPDATE_ENDPOINT" {
			return "https://user:secret@mirror.example/olivares"
		}
		return ""
	}
	got, err := svc.UpdateStatus(context.Background())
	if err != nil || !strings.Contains(got.CheckError, "credentials") || strings.Contains(got.CheckError, "secret") {
		t.Fatalf("status = %+v, %v", got, err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("upgrade ran with %v: the credentials would sit in the process list", f.calls)
	}
}

func TestApplyUpdateThatFailsAfterTheSwapStillRestartsOnTheInstalledBinary(t *testing.T) {
	_, p := bootForSettings(t)
	doc := availableDoc()
	doc.Status, doc.Current = upgradeStatusUpToDate, "0.3" // the check after the failed run reads the new binary
	f := &fakeUpgrade{doc: doc, applyErr: errors.New("signal: killed")}
	restarted := false
	svc := newTestUpdateService(t, f, func(string) error { restarted = true; return nil })
	svc.settings = p
	got, err := svc.ApplyUpdate(context.Background(), operator(t))
	if err != nil || !got.Restarting || !restarted {
		t.Fatalf("apply = %+v, %v, restarted=%v: an install that finished before the run failed must be restarted onto", got, err, restarted)
	}
	if _, ok := auditedUpdate(t, p.st); !ok {
		t.Fatal("the recovered install left no audit event")
	}
}

func TestApplyUpdateThatFailsBeforeTheSwapIsRefusedNotRestarted(t *testing.T) {
	restarted := false
	f := &fakeUpgrade{doc: availableDoc(), applyErr: errors.New("manifest signature does not verify")}
	svc := newTestUpdateService(t, f, func(string) error { restarted = true; return nil })
	if _, err := svc.ApplyUpdate(context.Background(), operator(t)); !errors.Is(err, api.ErrUpdateRefused) || restarted {
		t.Fatalf("err = %v, restarted=%v, want a refusal and no restart", err, restarted)
	}
}

func TestApplyUpdateRestartsEvenWhenTheAuditEventCannotBeWritten(t *testing.T) {
	eng, p := bootForSettings(t)
	doc := availableDoc()
	doc.Action, doc.Installed = upgradeActionInstalled, "olivares 0.3 (commit abc, built now)"
	restarted := false
	svc := newTestUpdateService(t, &fakeUpgrade{doc: doc}, func(string) error { restarted = true; return nil })
	svc.settings = p
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := svc.ApplyUpdate(context.Background(), operator(t))
	if err != nil || !got.Restarting || !restarted {
		t.Fatalf("apply = %+v, %v, restarted=%v: the binary is replaced, so the engine must restart even without a ledger entry", got, err, restarted)
	}
}
