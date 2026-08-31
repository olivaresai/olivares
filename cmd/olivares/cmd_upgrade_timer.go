// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// cmd_upgrade_timer.go generates an OPT-IN systemd timer + service for `olivares
// upgrade --install-timer`. Auto-update is never on by default — a control
// plane does not change under an operator without a maintenance window — so this
// only EMITS the units (to stdout, or to --timer-dir); enabling them is a deliberate
// `systemctl enable` the operator runs. The generated service runs
// `upgrade --if-eligible` so a partial (staged) rollout is honored: a node upgrades
// only when it is in the manifest's cohort, in the window, never mid-day by surprise.

// Actions `upgrade --install-timer` reports: the same two units either WRITTEN to
// --timer-dir or printed for the operator to save. Both exit 0 having installed
// nothing, so this is the only field that tells a script which happened.
const (
	upgradeTimerActionWrote   = "wrote-timer-units"
	upgradeTimerActionPrinted = "printed-timer-units"
)

// upgradeTimerResult is the -o json pane of `upgrade --install-timer`.
//
// This leaf has its OWN document rather than sharing upgradeResult, because it
// shares no fact with it: it returns at the top of runUpgrade, before an OTA key,
// a source or a manifest exists, so there is no channel state, no plan and no
// installed version to report. Forcing one shape over both would mean a document
// that is two thirds empty on whichever call the operator made.
//
// IT CARRIES THE UNIT TEXT, not only the paths, and that is the whole point on the
// print path. Those two blocks are the command's PRODUCT — the reason
// progressstream.go leaves them on stdout — and prose framed by `# ---- …` headers
// is exactly what a config-management tool cannot consume. As `service_unit` /
// `timer_unit` the same bytes are installable without a screen-scrape, and they are
// present on BOTH paths so the document has one shape: with --timer-dir the two
// paths fill in as well, and without it they are "".
type upgradeTimerResult struct {
	Action      string `json:"action"`
	Channel     string `json:"channel"`
	Schedule    string `json:"schedule"`
	ExecStart   string `json:"exec_start"`
	ServicePath string `json:"service_path"`
	TimerPath   string `json:"timer_path"`
	ServiceUnit string `json:"service_unit"`
	TimerUnit   string `json:"timer_unit"`
}

// generateUpgradeTimer renders the .service and .timer units and either writes them
// to --timer-dir or prints them with install instructions. It performs no network.
func generateUpgradeTimer(cmd *cobra.Command, o *upgradeOptions) error {
	// The binary the timer invokes: an explicit --target, else the running exe.
	bin, err := resolveTargetBinary(o.target)
	if err != nil {
		return err
	}

	// Build the upgrade invocation. --if-eligible + --yes make it unattended and
	// rollout-aware; --channel selects the stream. The enterprise token is NEVER
	// inlined — it is read from an EnvironmentFile so the unit file carries no secret.
	args := []string{"upgrade", "--channel", o.channel, "--if-eligible", "--yes"}
	if o.endpoint != "" {
		args = append(args, "--endpoint", o.endpoint)
	}
	// Pin the data dir into the unattended invocation so the license CRL the run
	// observes is recorded where the engine's seat policy reads it. Without
	// it the timer would default the dir per-run and the observation store could
	// land somewhere the engine never looks. Resolve it the same way the engine does.
	crlDir := o.dataDir
	if crlDir == "" {
		resolved, err := defaultDataDir()
		if err != nil {
			return err
		}
		crlDir = resolved
	}
	args = append(args, "--data-dir", crlDir)
	envFileHint := ""
	if o.enterprise {
		args = append(args, "--enterprise", "--token", "${OLIVARES_UPGRADE_TOKEN}")
		envFileHint = "EnvironmentFile=-/etc/olivares/upgrade.env\n"
	}
	execStart := shellQuote(bin) + " " + strings.Join(args, " ")

	service := fmt.Sprintf(`# olivares-upgrade.service — opt-in automatic update check.
# Runs a rollout-aware, verified in-place upgrade in a maintenance window. It is
# SAFE to interrupt: the swap is atomic with automatic rollback, and --if-eligible
# skips nodes outside the staged-rollout cohort. The service needs write access to
# the installed binary (%s) and its directory.
#
# ONE AGENT PER BINARY, NOT ONE PER CHANNEL. Install ONE timer. Do not copy these
# units under a second name to follow a second channel: `+"`upgrade`"+` takes an
# exclusive lock on the target for the whole prepare-download-swap sequence, so the
# second run exits 5 (Conflict) without installing anything. That refusal is the
# safe outcome, not the intended workflow — before the lock existed, two runs
# landing in the same second overwrote each other's rollback backup, and the loser's
# automatic rollback restored the winner's binary and reported success. Change
# channel by changing --channel on this one unit.
[Unit]
Description=Olivares AI — verified in-place upgrade (channel %s)
Documentation=https://olivares.ai/docs (docs/UPGRADE-AND-ROLLBACK.md)
Wants=network-online.target
After=network-online.target

[Service]
Type=oneshot
%sExecStart=%s
# Do not fail the timer if there is simply nothing to do / not yet in the cohort.
SuccessExitStatus=0
Nice=10
`, bin, o.channel, envFileHint, execStart)

	timer := fmt.Sprintf(`# olivares-upgrade.timer — opt-in schedule for the verified upgrade.
# Enable with:  systemctl enable --now olivares-upgrade.timer
[Unit]
Description=Olivares AI — scheduled verified upgrade check (channel %s)

[Timer]
OnCalendar=%s
# Spread the fleet across the window so a channel host is never thundered.
RandomizedDelaySec=30m
# Catch up a missed run (host was down) at next boot, but not immediately.
Persistent=true

[Install]
WantedBy=timers.target
`, o.channel, o.timerSchedule)

	res := upgradeTimerResult{
		Channel:     o.channel,
		Schedule:    o.timerSchedule,
		ExecStart:   execStart,
		ServiceUnit: service,
		TimerUnit:   timer,
	}

	if o.timerDir != "" {
		if err := os.MkdirAll(o.timerDir, 0o755); err != nil {
			return err
		}
		sp := filepath.Join(o.timerDir, "olivares-upgrade.service")
		tp := filepath.Join(o.timerDir, "olivares-upgrade.timer")
		if err := os.WriteFile(sp, []byte(service), 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(tp, []byte(timer), 0o644); err != nil {
			return err
		}
		res.Action = upgradeTimerActionWrote
		res.ServicePath = sp
		res.TimerPath = tp
		return renderOut(cmd, func(w io.Writer) error {
			fmt.Fprintf(w, "wrote %s\nwrote %s\n\n", sp, tp)
			if _, werr := fmt.Fprintf(w, "enable with:\n  sudo cp %s %s /etc/systemd/system/\n  sudo systemctl daemon-reload\n  sudo systemctl enable --now olivares-upgrade.timer\n", sp, tp); werr != nil {
				return werr
			}
			if o.enterprise {
				_, werr := fmt.Fprintln(w, "\nenterprise: put your download token in /etc/olivares/upgrade.env (0600):\n  OLIVARES_UPGRADE_TOKEN=<token-from-your-license-email>")
				return werr
			}
			return nil
		}, res)
	}

	res.Action = upgradeTimerActionPrinted
	return renderOut(cmd, func(w io.Writer) error {
		fmt.Fprintln(w, "# ---- /etc/systemd/system/olivares-upgrade.service ----")
		fmt.Fprint(w, service)
		fmt.Fprintln(w, "\n# ---- /etc/systemd/system/olivares-upgrade.timer ----")
		fmt.Fprint(w, timer)
		fmt.Fprintln(w, "\n# Install: save the two blocks above, then:")
		fmt.Fprintln(w, "#   sudo systemctl daemon-reload && sudo systemctl enable --now olivares-upgrade.timer")
		if o.enterprise {
			fmt.Fprintln(w, "# Enterprise: OLIVARES_UPGRADE_TOKEN=<token> in /etc/olivares/upgrade.env (chmod 0600).")
		}
		_, werr := fmt.Fprintln(w, "# (Or write the files directly with:  olivares upgrade --install-timer --timer-dir <dir>)")
		return werr
	}, res)
}

// shellQuote wraps a path in single quotes for a systemd ExecStart when it contains
// spaces or shell-special characters, so an install path with a space still works.
func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\"'\\$") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
