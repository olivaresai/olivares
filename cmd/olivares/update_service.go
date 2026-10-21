// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/envconfig"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/release"
	"github.com/olivaresai/olivares/core/store"
)

// updateService is the console's Updates page (/v1/console/updates). It adds no update
// mechanism: it runs this same binary as `olivares upgrade` and reads its -o json
// document, so the console checks, verifies, swaps atomically and refuses exactly as the
// CLI does. After an install it asks the engine to restart on the new binary
// (selfRestart re-executes the binary path, which the swap just replaced).
type updateService struct {
	// run executes `olivares <args>` and returns its stdout; a non-zero exit is an
	// error carrying the command's own reason.
	run     func(ctx context.Context, args []string) ([]byte, error)
	version string
	edition string
	dataDir string
	getenv  func(string) string
	// restart asks the serving engine to restart; nil when none can.
	restart func(reason string) error
	// sessions counts the sessions running now, which a restart stops.
	sessions func() int
	settings *productSettings
	log      *slog.Logger
}

// upgradeRunLimit bounds one `olivares upgrade` run: the command's own network timeout
// is 5 minutes, and an install adds the download, the probes and the swap.
const upgradeRunLimit = 10 * time.Minute

// checkTimeout bounds the network part of a status check (`upgrade --timeout`).
const checkTimeout = "30s"

const updateApplyAction = "deployment.update.apply"

func newUpdateService(st *productSettings, edition, version, dataDir string, restart func(string) error, sessions func() int, log *slog.Logger) updateService {
	return updateService{
		run: runSelfUpgrade, version: version, edition: edition, dataDir: dataDir,
		getenv: envconfig.Get, restart: restart, sessions: sessions, settings: st, log: log,
	}
}

// args is `olivares upgrade` for this instance: the channel and endpoint the update
// indicator already reads (OLIVARES_UPDATE_CHANNEL / OLIVARES_UPDATE_ENDPOINT), and the
// licensed route for a Business build, which `upgrade` requires and cannot default
// without a download token.
func (s updateService) args(apply bool) ([]string, error) {
	a := []string{"upgrade", "-o", "json", "--data-dir", s.dataDir}
	if ch := strings.TrimSpace(s.getenv("OLIVARES_UPDATE_CHANNEL")); ch != "" {
		a = append(a, "--channel", ch)
	}
	if ep := strings.TrimSpace(s.getenv("OLIVARES_UPDATE_ENDPOINT")); ep != "" {
		// The endpoint travels in the child's argv, which any local user can read.
		if u, err := url.Parse(ep); err != nil || u.User != nil {
			return nil, errors.New("OLIVARES_UPDATE_ENDPOINT carries credentials or is not a URL; the update would expose them in the process list")
		}
		a = append(a, "--endpoint", ep)
	}
	if s.edition != "community" {
		a = append(a, "--enterprise", "--connect")
	}
	if apply {
		return append(a, "--yes"), nil
	}
	// A status read must answer in the time an administrator waits for a page.
	return append(a, "--check", "--timeout", checkTimeout), nil
}

// replaced says the binary on disk is not the version this engine runs: an install
// whose restart has not happened.
func (s updateService) replaced(onDisk string) bool {
	running, err := release.ParseVersion(s.version)
	if err != nil {
		return false
	}
	installed, err := release.ParseVersion(onDisk)
	return err == nil && release.Compare(running, installed) != 0
}

func (s updateService) runningSessions() int {
	if s.sessions == nil {
		return 0
	}
	return s.sessions()
}

// UpdateStatus runs `olivares upgrade --check`. A check that fails (no network, no
// release yet, an unverifiable manifest) still answers with the installed version and
// upgrade's own reason, because that is what the page shows the administrator.
func (s updateService) UpdateStatus(ctx context.Context) (api.UpdateStatusDTO, error) {
	dto := api.UpdateStatusDTO{Edition: s.edition, Version: s.version, RunningSessions: s.runningSessions()}
	doc, err := s.upgrade(ctx, false)
	if err == nil && doc.Status == "" {
		err = errors.New("the upgrade check returned no status")
	}
	if err != nil {
		s.log.Warn("update: the channel could not be checked", "err", err)
		dto.CheckError = err.Error()
		return dto, nil
	}
	dto.RestartRequired = s.replaced(doc.Current)
	dto.Channel = doc.Channel
	dto.Available = doc.Available
	dto.UpdateAvailable = doc.Status == upgradeStatusAvailable
	dto.ReleasedAt = doc.ReleasedAt
	dto.Notes = doc.Notes
	dto.Security = doc.Security
	dto.Advisories = doc.Advisories
	return dto, nil
}

// ApplyUpdate runs `olivares upgrade --yes`, records who asked, and restarts the engine
// on the installed binary. Nothing newer means nothing installed and no restart.
func (s updateService) ApplyUpdate(ctx context.Context, actor auth.Principal) (api.UpdateStatusDTO, error) {
	dto := api.UpdateStatusDTO{Edition: s.edition, Version: s.version, RunningSessions: s.runningSessions()}
	// Once begun, the install finishes and the engine restarts on it whether or not the
	// administrator's connection survives the minutes it can take.
	ctx = context.WithoutCancel(ctx)
	doc, err := s.upgrade(ctx, true)
	if err != nil {
		// A run can fail after the swap (killed at the time limit, unreadable output).
		// If the binary on disk is no longer the running one, the install happened.
		check, cerr := s.upgrade(ctx, false)
		if cerr != nil || !s.replaced(check.Current) {
			return api.UpdateStatusDTO{}, fmt.Errorf("%w: %v", api.ErrUpdateRefused, err)
		}
		s.log.Warn("update: the upgrade run failed after installing; restarting on the installed binary", "err", err, "to", check.Current)
		doc = check
		doc.Action = upgradeActionInstalled
	}
	dto.Channel, dto.Available = doc.Channel, doc.Available
	if doc.Action != upgradeActionInstalled {
		return dto, nil
	}
	if err := s.record(ctx, actor, doc); err != nil {
		// The binary is already replaced: restart on it rather than leave the engine on
		// a version its ledger does not mention, and say why the record is missing.
		s.log.Error("update: installed, but the audit event was not recorded", "err", err, "actor", actor.Actor(), "to", doc.Available)
	}
	if s.restart == nil {
		return api.UpdateStatusDTO{}, fmt.Errorf("%w: this engine is not running as a server", api.ErrUpdateRestartUnavailable)
	}
	if err := s.restart("update to " + doc.Available); err != nil {
		s.log.Error("update: installed, but the engine could not restart", "err", err, "to", doc.Available)
		return api.UpdateStatusDTO{}, fmt.Errorf("%w: %v", api.ErrUpdateRestartUnavailable, err)
	}
	dto.Restarting = true
	return dto, nil
}

// upgrade runs one `olivares upgrade` and reads its document.
func (s updateService) upgrade(ctx context.Context, apply bool) (upgradeResult, error) {
	args, err := s.args(apply)
	if err != nil {
		return upgradeResult{}, err
	}
	out, err := s.run(ctx, args)
	if err != nil {
		return upgradeResult{}, err
	}
	var doc upgradeResult
	if err := json.Unmarshal(out, &doc); err != nil {
		return upgradeResult{}, fmt.Errorf("read the upgrade result: %w", err)
	}
	return doc, nil
}

// record appends the install to the signed audit ledger.
func (s updateService) record(ctx context.Context, actor auth.Principal, doc upgradeResult) error {
	return s.settings.st.AuthMutate(ctx, func(as store.AuthScope) error {
		_, err := as.Audit().Append(ctx, model.AuditDraft{
			Actor: actor.Actor(), ActorKind: actor.ActorKind(),
			Action: updateApplyAction, TargetKind: "core.engine_binary",
			Meta: map[string]any{"from": s.version, "to": doc.Available, "channel": doc.Channel, "installed": doc.Installed},
		})
		return err
	})
}

// runSelfUpgrade executes this binary with args. A binary replaced while it runs shows
// as "<path> (deleted)" in /proc; the path itself is the new one.
func runSelfUpgrade(ctx context.Context, args []string) ([]byte, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	exe = strings.TrimSuffix(exe, " (deleted)")
	ctx, cancel := context.WithTimeout(ctx, upgradeRunLimit)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...) // #nosec G204 -- the engine's own binary and a fixed argv
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, errors.New(upgradeFailureReason(stderr.String(), err))
	}
	return stdout.Bytes(), nil
}

// maxUpgradeReason bounds the sentence shown in the console.
const maxUpgradeReason = 600

// upgradeFailureReason is the command's own reason: the message of the JSON error it
// prints last under -o json, else its last line of output, else why it did not run.
func upgradeFailureReason(stderr string, runErr error) string {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(line), &e) == nil && e.Error.Message != "" {
			line = e.Error.Message
		}
		if len(line) > maxUpgradeReason {
			line = strings.ToValidUTF8(line[:maxUpgradeReason], "") + "…"
		}
		return line
	}
	return runErr.Error()
}
