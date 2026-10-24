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
	// licensed is the edition the live license entitles ("" for none); installKind is how this
	// engine was installed (nil reads the host). Together they say what a Community build can
	// move to and how.
	licensed    func() string
	installKind func() string
	settings    *productSettings
	log         *slog.Logger
}

// upgradeRunLimit bounds one `olivares upgrade` run: the command's own network timeout
// is 5 minutes, and an install adds the download, the probes and the swap.
const upgradeRunLimit = 10 * time.Minute

// checkTimeout bounds the network part of a status check (`upgrade --timeout`).
const checkTimeout = "30s"

const updateApplyAction = "deployment.update.apply"

func newUpdateService(st *productSettings, edition, version, dataDir string, licensed func() string, restart func(string) error, sessions func() int, log *slog.Logger) updateService {
	return updateService{
		run: runSelfUpgrade, version: version, edition: edition, dataDir: dataDir,
		getenv: envconfig.Get, restart: restart, sessions: sessions, licensed: licensed, installKind: installKind,
		settings: st, log: log,
	}
}

// switchTo is the edition a Community build with a live commercial license moves to, or "".
func (s updateService) switchTo() string {
	if s.edition != "community" || s.licensed == nil {
		return ""
	}
	return s.licensed()
}

// endpointTokenReason is why the licensed download refuses an update endpoint: the variable names
// the public update channel, and the download sends its token to the endpoint it is given.
const endpointTokenReason = "OLIVARES_UPDATE_ENDPOINT names the public update channel, and the licensed download would send its token there: unset it, or connect this installation once with `olivares license connect start`"

// endpointBlocksToken says the licensed download cannot run here: this engine names an update
// endpoint and is not connected. Only a connected installation may name one, because its connect
// state binds the origin and refuses another; without it the token would go to a public mirror.
func (s updateService) endpointBlocksToken() bool {
	return strings.TrimSpace(s.getenv("OLIVARES_UPDATE_ENDPOINT")) != "" && !isConnected(s.dataDir)
}

// licensedRoute says this engine's update is the licensed download: always on a Business build,
// and on a Community build only where its license entitles a build the console can take now (a
// token or a connection, no unconnected mirror, an install the engine can swap). Everywhere else a
// Community build keeps the public channel it always had, so a security release never waits on a
// switch nobody can take; the switch step is shown beside it. One predicate for args(), the
// step's button and the apply, so they never disagree.
func (s updateService) licensedRoute() bool {
	if s.edition != "community" {
		return true
	}
	sw := s.editionSwitch()
	return sw != nil && sw.ConsoleAction
}

// args is `olivares upgrade` for this instance: the channel and endpoint the update
// indicator already reads (OLIVARES_UPDATE_CHANNEL / OLIVARES_UPDATE_ENDPOINT), and the
// licensed route when licensedRoute says so. `upgrade` takes that route's download token from
// this installation's connected identity when it has one, else from OLIVARES_UPGRADE_TOKEN
// in this engine's environment, which the child inherits; with neither it says so. A
// Community build with a live Business license takes the licensed route too (--edition
// business): the update it offers is the Business build.
func (s updateService) args(apply bool) ([]string, error) {
	a := []string{"upgrade", "-o", "json", "--data-dir", s.dataDir}
	licensed := s.licensedRoute()
	// A connected installation refreshes its own credential, and the connect state binds the
	// endpoint (a different one is refused), as a Business build's console update always did.
	connected := licensed && isConnected(s.dataDir)
	if ch := strings.TrimSpace(s.getenv("OLIVARES_UPDATE_CHANNEL")); ch != "" {
		a = append(a, "--channel", ch)
	}
	if ep := strings.TrimSpace(s.getenv("OLIVARES_UPDATE_ENDPOINT")); ep != "" {
		// The endpoint travels in the child's argv, which any local user can read.
		u, err := url.Parse(ep)
		if err != nil || u.User != nil {
			return nil, errors.New("OLIVARES_UPDATE_ENDPOINT carries credentials or is not a URL; the update would expose them in the process list")
		}
		// The connect state is read once (above): a binding that turns active between two reads must
		// not skip this refusal and still leave --connect off, which would send the token to the mirror.
		if licensed && !connected {
			return nil, errors.New(endpointTokenReason)
		}
		a = append(a, "--endpoint", ep)
	}
	if s.edition != "community" {
		a = append(a, "--enterprise")
	} else if licensed {
		a = append(a, "--edition", "business")
	}
	if connected {
		a = append(a, "--connect")
	}
	if apply {
		return append(a, "--yes"), nil
	}
	// A status read must answer in the time an administrator waits for a page.
	return append(a, "--check", "--timeout", checkTimeout), nil
}

// editionReplaced says a Community engine's file now holds the licensed build: a switch whose
// restart has not happened. A Community build moving to the licensed one keeps its version, so the
// version alone cannot show it. Only that direction counts: a package upgrade that puts Community
// back under a Business engine is not an install, and its refusal must stay the answer.
func (s updateService) editionReplaced(doc upgradeResult) bool {
	ed := doc.EditionCurrent
	return s.edition == release.EditionCommunity && ed != "" && ed != release.EditionUnknown && ed != release.EditionCommunity
}

// replaced says the binary on disk is not the build this engine runs: an install whose restart
// has not happened.
func (s updateService) replaced(doc upgradeResult) bool {
	if s.editionReplaced(doc) {
		return true
	}
	running, err := release.ParseVersion(s.version)
	if err != nil {
		return false
	}
	installed, err := release.ParseVersion(doc.Current)
	return err == nil && release.Compare(running, installed) != 0
}

// editionSwitch is the step to the licensed build for this installation, nil when the license
// entitles nothing this build lacks. It is part of the answer even when the channel cannot be
// checked: a missing download token is exactly when the owner needs the step. Its button
// (ConsoleAction) is true only where the apply would run it, so an unconnected mirror takes the
// button away and names itself, as a missing token does.
func (s updateService) editionSwitch() *api.EditionSwitchDTO {
	to := s.switchTo()
	if to == "" {
		return nil
	}
	kind := installKind
	if s.installKind != nil {
		kind = s.installKind
	}
	step := switchStepFor(kind(), s.dataDir)
	if step.ConsoleAction && s.endpointBlocksToken() {
		step.ConsoleAction = false
		step.Then += " " + endpointTokenReason + "."
	}
	return withEdition(step, to)
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
	dto := api.UpdateStatusDTO{Edition: s.edition, Version: s.version, RunningSessions: s.runningSessions(), EditionSwitch: s.editionSwitch()}
	doc, err := s.upgrade(ctx, false)
	if err == nil && doc.Status == "" {
		err = errors.New("the upgrade check returned no status")
	}
	if err != nil {
		s.log.Warn("update: the channel could not be checked", "err", err)
		dto.CheckError = err.Error()
		return dto, nil
	}
	dto.RestartRequired = s.replaced(doc)
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
// on the installed binary. Nothing newer means nothing installed and no restart, except that
// a licensed build another run already put on disk is restarted onto. Which download it runs is
// licensedRoute's: the switch where the console offers its button, else the public channel.
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
		if cerr != nil || !s.replaced(check) {
			return api.UpdateStatusDTO{}, fmt.Errorf("%w: %v", api.ErrUpdateRefused, err)
		}
		s.log.Warn("update: the upgrade run failed after installing; restarting on the installed build", "err", err, "to", check.Current)
		doc = check
		doc.Action = upgradeActionInstalled
	}
	dto.Channel, dto.Available = doc.Channel, doc.Available
	installed := doc.Action == upgradeActionInstalled
	if !installed && !s.editionReplaced(doc) {
		return dto, nil
	}
	if !installed {
		// Another run (a terminal's `olivares upgrade`) installed it and nobody restarted the engine:
		// the ledger names what is on disk, not the channel's newest.
		s.log.Info("update: the installed build is not the one running; restarting on it", "actor", actor.Actor(), "to", doc.Current)
		doc.Available, doc.EditionAvailable = doc.Current, doc.EditionCurrent
	}
	if err := s.record(ctx, actor, doc); err != nil {
		// The binary is already replaced: restart on it rather than leave the engine on
		// a version its ledger does not mention, and say why the record is missing.
		s.log.Error("update: restarting on the installed build, but the audit event was not recorded", "err", err, "actor", actor.Actor(), "to", doc.Available)
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
			Meta: map[string]any{"from": s.version, "to": doc.Available, "channel": doc.Channel, "installed": doc.Installed,
				"edition_from": s.edition, "edition_to": doc.EditionAvailable},
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
