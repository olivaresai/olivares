// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/core/license"
	"github.com/olivaresai/olivares/core/release"
)

// cmd_upgrade.go is the OTA in-place upgrade for `olivares upgrade`: it
// GENERALISES the enterprise-only path into ONE framework that upgrades a
// community OR an enterprise binary to a newer signed release, verified OFFLINE and
// swapped atomically with automatic rollback. It reuses the primitives
// (atomicSwap / execProbe / downloadGated / resolveReleaseKey / requireValidLicense)
// and adds the layers a professional updater needs: a signed per-CHANNEL manifest
// (core/release, TUF-lite), ANTI-ROLLBACK (never silently downgrade — an explicit
// audited --force-rollback), staged ROLLOUT cohorts, AIR-GAP bundles (--bundle),
// and an opt-in systemd TIMER (--install-timer).
//
// Trust boundary (unchanged): the OFFLINE signature, not the transport.
// The per-channel manifest.json is verified against the embedded (or --pubkey)
// dedicated Ed25519 OTA key BEFORE any decision; the downloaded artifact is bound to the
// manifest's signed SHA-256 before it is ever executed; the candidate exec-probes
// OK before the swap and auto-rolls-back if the installed binary does not run. A
// build with no key and no --pubkey fails closed — there is no unverified upgrade.
//
// HONESTY: a Go binary is not live-patched. "OTA" here is a verified binary swap
// plus a zero-downtime service handover (SO_REUSEPORT drain, or rolling HA) — never
// "hot patching" of the running process (docs/UPGRADE-AND-ROLLBACK.md).

// maxArtifactBytes bounds a download so a hostile endpoint cannot exhaust disk.
const maxArtifactBytes = 512 << 20

// Default manifest/artifact hosts. Community pulls from the PUBLIC REPOSITORY'S GITHUB
// RELEASES; enterprise from the licensed download worker (gate contract).
//
// ⛔ THE COMMUNITY DEFAULT MOVED ON 2026-08-27, AND IT MOVED BECAUSE THE OLD ONE WAS A
// 404 THAT NOBODY WAS GOING TO SERVE. It was `https://olivares.ai/updates`, and measured
// on that date `https://olivares.ai/updates`, `/updates/stable/manifest.json` and its
// `.sig` all answered 404 — no workflow in .github/ produced anything under that path and
// no server served it, which is why scripts/check-emitted-urls.sh carried the URL with the
// owner `release-blocker-no-producer-no-server`. Every community binary shipped with that
// pin would have had `olivares upgrade` fail on its first run.
//
// The destination is not a preference: FIRMA B (2026-08-21,
// an internal design note (not shipped):314-326) put the community update channel on
// the GitHub Releases of the public repository, with an independent R2 kept only as a
// fallback that is not built by default. That signature SUPERSEDES the 2026-08-15 one
// that put it on the web repo — which is the plan an internal design note (not shipped)
// CFG-06 still described, and which is corrected there in the same change as this line.
//
// TWO-PHASE IS RESPECTED, NOT BYPASSED: `olivaresai/olivares` does not carry a release
// yet, so this pin resolves to a 404 until publishes the first one — the same
// honest state the README already documents ("not published yet; build from source"),
// but pointed at the place the release WILL be instead of a path nobody owns. Until
// then, and for the T3 rehearsal, `--endpoint` selects any other repository or a static
// mirror; the two layouts it accepts are resolved by release.ResolveChannel.
const (
	defaultCommunityEndpoint  = "https://github.com/olivaresai/olivares"
	defaultEnterpriseEndpoint = "https://licenses.olivares.ai"
	// binaryName is the executable inside a community .tar.gz archive.
	binaryName = "olivares"
)

type upgradeOptions struct {
	enterprise     bool
	token          string
	endpoint       string
	channel        string
	pubkey         string
	dataDir        string
	license        string
	goos           string
	goarch         string
	target         string
	currentVersion string
	bundle         string
	check          bool
	assumeYes      bool
	forceRollback  bool
	ifEligible     bool
	installTimer   bool
	timerDir       string
	timerSchedule  string
	timeout        time.Duration
}

func newUpgradeCmd() *cobra.Command {
	o := &upgradeOptions{}
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade this binary in place to a newer signed release (verified, atomic, reversible)",
		Long: "upgrade replaces THIS installed binary with a newer signed release of the SAME edition,\n" +
			"verified OFFLINE and swapped atomically with a kept backup for rollback. It follows a\n" +
			"release CHANNEL (stable | security): it downloads that channel's signed manifest,\n" +
			"verifies it against the embedded OTA key, refuses a downgrade unless you pass\n" +
			"--force-rollback (audited), binds the artifact to the manifest's signed SHA-256, and only\n" +
			"then swaps — the running process is untouched until you restart it.\n\n" +
			"  olivares upgrade                     # community: upgrade to the latest stable release\n" +
			"  olivares upgrade --channel security  # take only security releases\n" +
			"  olivares upgrade --check             # show the plan (current -> available, CVEs) — no swap\n" +
			"  olivares upgrade --bundle f.tar.gz   # air-gap: local bundle, no network (install needs a license)\n" +
			"  olivares upgrade --enterprise        # licensed enterprise superset (needs a live license)\n" +
			"  olivares upgrade --install-timer     # print an opt-in systemd auto-check timer\n\n" +
			"A build with no embedded OTA key requires --pubkey (air-gapped / self-signed mirror).\n" +
			"NOTE: a Go binary is not hot-patched; zero downtime is a drain + handover, see\n" +
			"docs/UPGRADE-AND-ROLLBACK.md.",
		Example: `  # Preview the latest stable upgrade without swapping
  olivares upgrade --check

  # Install a verified local air-gap bundle (the installed license is checked offline first)
  olivares upgrade --bundle ./olivares-release.tar.gz --yes`,
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE:         func(cmd *cobra.Command, _ []string) error { return runUpgrade(cmd, o) },
	}
	f := cmd.Flags()
	f.BoolVar(&o.enterprise, "enterprise", false, "upgrade the licensed enterprise edition (gated download; needs a live license)")
	f.StringVar(&o.token, "token", "", "enterprise download token from your license/fulfillment email")
	f.StringVar(&o.endpoint, "endpoint", "", "update channel source: a GitHub repository (https://github.com/<owner>/<repo>), one of its releases (…/releases/tag/<tag>), or a static mirror base (<base>/<channel>/manifest.json). Default: the public repository's releases; the license worker with --enterprise")
	// `lts` is still a value release.ValidChannel accepts, so hiding it would document a
	// narrower validator than the one that runs. What it must NOT do is read as an offer:
	// no lts line is produced (PRICING-CANON.md:665-670 — general_backports: false), so a
	// build that follows it would ask for a manifest nobody publishes.
	f.StringVar(&o.channel, "channel", release.ChannelStable, "release channel: stable | security (lts is accepted by the validator, but no lts line is published)")
	f.StringVar(&o.pubkey, "pubkey", "", "base64 or @file Ed25519 OTA key to verify against (default: the key embedded in this build)")
	f.StringVar(&o.dataDir, "data-dir", "", "data directory (license + install-id) (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares)")
	f.StringVar(&o.license, "license", "", "explicit license file path (enterprise; highest precedence)")
	f.StringVar(&o.goos, "os", runtime.GOOS, "target OS to download for")
	f.StringVar(&o.goarch, "arch", runtime.GOARCH, "target architecture to download for")
	f.StringVar(&o.target, "target", "", "binary path to replace (default: the running executable)")
	f.StringVar(&o.currentVersion, "current-version", "", "declare the version installed at --target when it cannot be probed (cross-arch staging, a noexec mount, or a build from source); keeps anti-rollback and min_version armed instead of guessing")
	f.StringVar(&o.bundle, "bundle", "", "install from a local air-gap bundle directory or .tar.gz (no network at all; installing needs a live installed license, verified offline; --check does not)")
	f.BoolVar(&o.check, "check", false, "show the upgrade plan (current -> available, channel, CVEs) without swapping")
	f.BoolVarP(&o.assumeYes, "yes", "y", false, "do not prompt for confirmation before swapping")
	f.BoolVar(&o.forceRollback, "force-rollback", false, "allow installing an OLDER version than the running one (records an audit entry)")
	f.BoolVar(&o.ifEligible, "if-eligible", false, "only proceed if this node is in the manifest's staged-rollout cohort (used by the timer)")
	f.BoolVar(&o.installTimer, "install-timer", false, "emit an opt-in systemd timer+service that runs `upgrade --if-eligible` in a maintenance window")
	f.StringVar(&o.timerDir, "timer-dir", "", "write the systemd units to this directory instead of printing them")
	f.StringVar(&o.timerSchedule, "timer-schedule", "Sun *-*-* 03:00:00", "systemd OnCalendar expression for the auto-check timer")
	f.DurationVar(&o.timeout, "timeout", 5*time.Minute, "overall network timeout")
	return cmd
}

// Actions `upgrade` reports — what the run DID, as distinct from what it FOUND.
// A script needs both: three of these four exit 0 having installed nothing, and
// `status` alone cannot tell "in the cohort and installed" from "in the cohort,
// asked to check only" from "eligible but out of cohort". Same reason `egress
// actuate` carries an `action` beside its mode.
const (
	upgradeActionInstalled  = "installed"
	upgradeActionUpToDate   = "up-to-date"
	upgradeActionChecked    = "checked"
	upgradeActionNotInCohor = "not-in-cohort"
)

// Ordering verdicts, i.e. the `status:` line of the plan, as stable tokens. The
// prose spells them for a human ("DOWNGRADE (blocked unless --force-rollback)");
// these are what a consumer branches on.
//
// upgradeStatusUnknown is NOT reachable in an emitted document today, and saying
// so is more useful than leaving a consumer to write a branch for it: an unknown
// installed version is REFUSED at runUpgrade's `if !current.Known` arm, which
// returns an error before any renderer runs (the ordering guards are
// claims ABOUT the installed version, so an unknown one is refused rather than
// compared). The arm exists anyway because it mirrors printPlan's, and the two
// must not drift: if that refusal is ever relaxed or moved, the document gains
// the state at the same moment the prose does, instead of silently reporting
// "upgrade-available" for a version nobody could read.
const (
	upgradeStatusUpToDate  = "up-to-date"
	upgradeStatusAvailable = "upgrade-available"
	upgradeStatusDowngrade = "downgrade"
	upgradeStatusUnknown   = "unknown"
)

// upgradeResult is the -o json pane of `olivares upgrade`: ONE document for every
// desenlace that reaches a verdict about an upgrade, whether or not it installed.
//
// Its fields are the lines the text pane prints, one for one — the OTA key and
// source header, every field of printPlan, the CRL notes, and the terminal
// sentence — because the point of the pane is to make those lines readable
// without a parser, not to publish a second, richer view that then drifts.
//
// `installed` and `backup` are "" on the three desenlaces that install nothing.
// They are PRESENT and empty rather than absent so a consumer reads one shape:
// `action` says what happened, and the swap fields are how it happened.
//
// TWO THINGS THIS DOCUMENT DELIBERATELY DOES NOT COVER:
//
//   - A FAILED upgrade. Every refusal in this command is an error with its own
//     exit code and its own sentence on stderr — an expired manifest, a
//     wrong-channel answer, an unknown installed version, min_version, the
//     anti-rollback gate. Handing any of those back as a well-formed document is
//     how a pipeline comes to treat a refusal as a result, and the refusal
//     messages here are the ones that carry the way out.
//   - `--install-timer`. It returns at the top of runUpgrade, before a key, a
//     source or a manifest exists, so it shares no field with this document; it
//     has its own (upgradeTimerResult) for the same reason `superadmin status`
//     and `superadmin enable` do not share one.
type upgradeResult struct {
	Action string `json:"action"`
	Status string `json:"status"`
	// OTAKey is resolveReleaseKey's label for the anchor the manifest was verified
	// against, with its fingerprint: the difference between a run that trusted this
	// build's embedded key and one that trusted a key the caller supplied.
	OTAKey            string `json:"ota_key"`
	OTAKeyFingerprint string `json:"ota_key_fingerprint"`
	// Source is the transport label (public channel, licensed worker, air-gap
	// bundle) — never a token; describe() is the same value the prose prints.
	Source  string `json:"source"`
	Channel string `json:"channel"`
	// Current is the version installed AT TARGET, and CurrentDeclared says whether
	// it was measured or ASSERTED with --current-version. Collapsing those two into
	// one string is the substitution removed from the text pane; a document
	// that could not tell a reading of the box from an operator's claim would
	// re-open it on the machine side.
	Current         string   `json:"current"`
	CurrentDeclared bool     `json:"current_declared"`
	Available       string   `json:"available"`
	ReleasedAt      string   `json:"released_at"`
	Security        bool     `json:"security"`
	Advisories      []string `json:"advisories"`
	MinVersion      string   `json:"min_version"`
	// Eligible is the staged-rollout cohort answer. The text pane prints a line
	// only when it is FALSE; the document always carries it, because "absent"
	// reading as "eligible" is the kind of default a rollout must not have.
	Eligible bool   `json:"eligible"`
	EOLAt    string `json:"eol_at"`
	Notes    string `json:"notes"`
	// CRLRecorded is the path the channel's license CRL was written to, "" when the
	// manifest carried no revocations. Warnings collects every WARNING: line the
	// run printed, in order, so a document consumer sees what a reader saw.
	//
	// "EVERY" IS LOAD-BEARING, and it cost a line to keep true: the last WARNING a
	// run can print — "rollback done but audit record failed" — is emitted AFTER
	// this document is built, so it has to be appended at that point rather than
	// collected here. Leaving it out would have made `warnings == []` the answer
	// for the one run where a forced downgrade went through with NO audit record,
	// which is precisely the run a fleet consumer must not read as clean. It stays
	// on stderr on both panes as well; the document repeats it, it does not move it.
	CRLRecorded string   `json:"crl_recorded"`
	Warnings    []string `json:"warnings"`
	Target      string   `json:"target"`
	Installed   string   `json:"installed"`
	Backup      string   `json:"backup"`
}

// upgradeStatusToken maps the plan to the token `status` carries, from the SAME
// three predicates printPlan branches on — so the pane and the prose cannot
// disagree about whether an unknown current version yields an ordering claim
// (it does not: that is the fail-open, and it is one arm here too).
func upgradeStatusToken(plan release.UpgradePlan) string {
	switch {
	case !plan.CurrentKnown:
		return upgradeStatusUnknown
	case plan.IsUpToDate():
		return upgradeStatusUpToDate
	case plan.IsRollback():
		return upgradeStatusDowngrade
	default:
		return upgradeStatusAvailable
	}
}

func runUpgrade(cmd *cobra.Command, o *upgradeOptions) error {
	// --install-timer is a local, network-free generator: emit the opt-in units and stop.
	// It runs BEFORE the progress stream is chosen because it shares none of the
	// narration below — it prints unit files, which are an artifact, not commentary.
	if o.installTimer {
		return generateUpgradeTimer(cmd, o)
	}

	// Everything from the OTA key line to the artifact digest is PROGRESS: printed
	// as the run advances, ahead of steps (a download, an exec-probe, a swap) that
	// can still refuse, and over a window that is minutes long on a real release.
	// Under -o json it moves to stderr so stdout is one document; without -o json
	// this is cmd.OutOrStdout() and not a byte moves. See progressstream.go.
	out := progressStream(cmd)
	warnings := []string{}
	crlRecorded := ""
	if !release.ValidChannel(o.channel) {
		return fmt.Errorf("unknown --channel %q (want one of %s)", o.channel, strings.Join(release.Channels, " | "))
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), o.timeout)
	defer cancel()

	// Resolve the data dir ONCE, the same way serve does (defaultDataDir reads
	// OLIVARES_DATA_DIR), so the install-id, license lookup and the CRL
	// observation store all land where the engine reads them. An unresolved ""
	// would silently make the CRL store a no-op on the default and timer paths (H1).
	if strings.TrimSpace(o.dataDir) == "" {
		resolved, derr := defaultDataDir()
		if derr != nil {
			return derr
		}
		o.dataDir = resolved
	}

	// 1) Offline verification key (explicit --pubkey else embedded). Fail-closed.
	pub, keySrc, err := resolveReleaseKey(o.pubkey)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "OTA key: %s (fingerprint %s)\n", keySrc, release.Fingerprint(pub))

	// 2) Resolve the target so we can (a) report/anti-rollback against what is
	//    ACTUALLY installed there and (b) swap it later.
	//    Trim ONCE, here, and hand the same value to both readers. They used to disagree
	//    on a whitespace-only --target: resolveTargetBinary tested the raw string (so
	//    "  " was an explicit path) while targetIsSelf trimmed it (so the same "  " was
	//    the running executable). That gap re-opened the exact substitution this session
	//    removed — main.version reported as the version of a file nobody read.
	//    A flag that was TYPED but is blank is an error, not an absence: silently
	//    re-reading `--target "  "` as "no target given" would answer about this
	//    executable a question the operator asked about somewhere else.
	if o.target != "" && strings.TrimSpace(o.target) == "" {
		return fmt.Errorf("--target was given but is blank; omit it to upgrade the running executable, or pass a path")
	}
	o.target = strings.TrimSpace(o.target)
	targetIsSelf := o.target == ""
	target, err := resolveTargetBinary(o.target)
	if err != nil {
		return err
	}
	current := currentInstalledVersion(ctx, target, o.currentVersion, targetIsSelf)
	// Fingerprint the bytes the guards are about to be decided against, HERE and not at
	// lock time, so the window the fingerprint covers is exactly the window the verdict
	// covers. Re-checked immediately before the swap (C03-23).
	before := fingerprintTarget(target)

	// 3) Build the update source (air-gap bundle | licensed worker | public channel).
	src, cleanup, err := buildUpdateSource(o)
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}
	fmt.Fprintf(out, "source: %s\n", src.describe())

	// 4) Fetch + VERIFY the signed manifest before any decision touches it.
	mb, sig, err := src.fetchManifest(ctx)
	if err != nil {
		return err
	}
	m, err := release.VerifyManifest(mb, sig, pub)
	if err != nil {
		return fmt.Errorf("REFUSING to upgrade: %w", err)
	}
	want := strings.TrimSpace(o.channel)
	if want == "" {
		want = release.ChannelStable
	}
	// THE CHANNEL THE SERVER SERVED MUST BE THE CHANNEL WE ASKED FOR.
	//
	// The signature proves the manifest is OURS. It does not prove it is the one we
	// requested: `stable`, `security` and `lts` are all signed by the same key, so a
	// perfectly authentic stable manifest satisfies `--channel security` — and the only
	// thing this command did with m.Channel was PRINT it. An operator asking for the
	// security line would be told "already up to date" by a genuine stable manifest that
	// simply does not carry the fix, and nothing in the output would look wrong.
	//
	// It is not hypothetical mischief: the gate that selects the channel is one query
	// parameter (commercial/license-worker/src/download/gate.ts), a mirror is one
	// redirect, and an air-gapped bundle is one file copied into the wrong directory.
	// Three ordinary mistakes and one hostile one all land here.
	//
	// Deny-closed, and it does NOT fall back to stable: "the security manifest is not
	// published yet" and "here is stable instead" are different answers, and collapsing
	// them is how a missing security release reads as a healthy one. The external
	// contrast on the channel plane raised exactly this as H-01.
	if got := strings.TrimSpace(m.Channel); got != want {
		return fmt.Errorf("REFUSING to upgrade: asked for channel %q and the server served a manifest signed for channel %q — the signature is valid, so this is a wrong-channel answer, not a forgery: a stale mirror, a misrouted gate, or the wrong air-gap bundle. Re-run against the %s endpoint, or use --channel %s deliberately",
			want, got, want, got)
	}
	// Freshness (anti-freeze): a still-signed but EXPIRED manifest may be a stale or
	// hostile mirror hiding a newer (security) release. Refuse to act on it — and do
	// this BEFORE recording the license CRL, so a replayed stale manifest can never
	// touch the observation store either.
	if now := time.Now().UTC(); m.Stale(now) {
		return fmt.Errorf("REFUSING to upgrade: the %s manifest expired at %s — it may be stale or a mirror serving an old (freeze) manifest; retry against a fresh endpoint/bundle",
			m.Channel, m.Expires.Format(time.RFC3339))
	}
	// License CRL observation: the fresh, verified manifest is the pull
	// channel the license CRL rides on. Record it independently of whether THIS
	// upgrade proceeds (up-to-date / out-of-cohort still observe). Resolve the data
	// dir the SAME way the rest of the command and the engine do, so the store lands
	// where the enterprise seat policy reads it (crlViewFromDataDir(cfg.DataDir)).
	// Recording failures warn and never block an upgrade.
	crlNow := time.Now().UTC()
	if rerr := recordCRLObservations(o.dataDir, m, crlNow); rerr != nil {
		warnings = append(warnings, fmt.Sprintf("could not record the channel's license CRL: %v", rerr))
		fmt.Fprintf(out, "WARNING: could not record the channel's license CRL: %v\n", rerr)
	} else if !m.Revoked.Empty() {
		crlRecorded = crlFilePath(o.dataDir)
		fmt.Fprintf(out, "recorded the channel license CRL in %s\n", crlRecorded)
	}
	for _, w := range describeCRLForLicense(o.dataDir, m, crlNow) {
		warnings = append(warnings, w)
		fmt.Fprintf(out, "WARNING: %s\n", w)
	}

	// 5) Plan the move against the running version.
	installID := resolveInstallID(o.dataDir)
	plan, err := m.PlanUpgrade(current.Version.Raw, o.goos, o.goarch, installID, time.Now().UTC())
	if err != nil {
		return err
	}
	printPlan(out, o, m, plan, current)

	// ONE base document for every terminal desenlace below, built HERE from the same
	// manifest, plan and installed version printPlan just rendered. Building it once
	// is what stops the two panes from reporting different plans for one run: there
	// is no second place where a field could be recomputed differently.
	res := upgradeResult{
		Status:            upgradeStatusToken(plan),
		OTAKey:            keySrc,
		OTAKeyFingerprint: release.Fingerprint(pub),
		Source:            src.describe(),
		Channel:           m.Channel,
		Current:           current.Version.Raw,
		CurrentDeclared:   current.Declared,
		Available:         m.Version,
		ReleasedAt:        m.ReleasedAt.UTC().Format(time.RFC3339),
		Security:          m.Security,
		Advisories:        []string{},
		MinVersion:        strings.TrimSpace(m.MinVersion),
		Eligible:          plan.Eligible,
		Notes:             m.Notes,
		CRLRecorded:       crlRecorded,
		Warnings:          warnings,
		Target:            target,
	}
	res.Advisories = append(res.Advisories, plan.Advisories...)
	if m.EOLAt != nil {
		res.EOLAt = m.EOLAt.UTC().Format(time.RFC3339)
	}

	// 6) Decide. The ordering guards come first, and they cannot run at all without a
	// current version: anti-rollback and min_version are both claims ABOUT the
	// installed version, so an unknown one is refused here rather than compared. This
	// is the fail-closed. Before it, a failed probe silently became main.version
	// ("dev" -> the zero version), which made every older signed release look like a
	// forward step: a downgrade to a vulnerable release installed with exit 0 and an
	// EMPTY audit log, because IsRollback() was false and --force-rollback was never
	// required. One refusal serves both ways of not knowing (see installedVersion).
	if !current.Known {
		// Worded so it is TRUE on every path that reaches it, --check included: --check
		// installs nothing, so it may not be told that an install was about to happen. It
		// still fails, and should — a check that cannot evaluate the gates has not
		// checked anything, and reporting that is the whole job.
		return fmt.Errorf("cannot establish the version installed at %s: %s\n"+
			"REFUSING: anti-rollback and the minimum-version gate are both claims ABOUT the installed version, so neither can be evaluated here. This upgrade is unverifiable, not merely unattempted.\n"+
			"Way out: re-run with --current-version <version> to declare what is installed there (both guards stay armed, and the audit record says the value was declared), or make %s answer `%s version`",
			target, current.Reason, target, target)
	}
	if plan.IsUpToDate() {
		res.Action = upgradeActionUpToDate
		return renderOut(cmd, func(w io.Writer) error {
			_, werr := fmt.Fprintf(w, "\nalready on %s (channel %s) — nothing to do.\n", m.Version, m.Channel)
			return werr
		}, res)
	}
	if !plan.HasArtifact {
		return fmt.Errorf("release %s has no artifact for %s/%s (platforms: %s)", m.Version, o.goos, o.goarch, strings.Join(m.Platforms(), ", "))
	}
	if plan.MinTooOld {
		return fmt.Errorf("cannot jump directly to %s: it requires a minimum current version of %s (you are on %s) — upgrade to an intermediate release first",
			m.Version, m.MinVersion, current.Version.Raw)
	}
	if plan.IsRollback() && !o.forceRollback {
		return fmt.Errorf("REFUSING to downgrade %s -> %s (anti-rollback): re-run with --force-rollback to override (it will be audited)",
			current.Version.Raw, m.Version)
	}
	if o.ifEligible && !plan.Eligible {
		res.Action = upgradeActionNotInCohor
		return renderOut(cmd, func(w io.Writer) error {
			_, werr := fmt.Fprintf(w, "\nnot in the staged-rollout cohort for %s yet — skipping (this is expected during a partial rollout).\n", m.Version)
			return werr
		}, res)
	}

	if o.check {
		res.Action = upgradeActionChecked
		return renderOut(cmd, func(w io.Writer) error {
			_, werr := fmt.Fprintln(w, "\n--check OK: manifest verifies and an upgrade is available. Re-run without --check to install.")
			return werr
		}, res)
	}

	// 7) Prepare the swap. From here to the swap there is exactly ONE agent per target:
	// the lock is taken BEFORE the download precisely because the download is the long
	// part, and it is taken AFTER --check returns so a read-only plan never blocks an
	// install (C03-23). --install-timer returned at the top and never reaches here.
	if err := checkWritable(target); err != nil {
		return err
	}
	unlock, err := lockUpgradeTarget(target)
	if err != nil {
		return err
	}
	defer unlock()
	if !o.assumeYes {
		verb := "Upgrade"
		if plan.IsRollback() {
			verb = "DOWNGRADE"
		}
		if !confirm(cmd, fmt.Sprintf("%s %s (%s -> %s)?", verb, target, current.Version.Raw, m.Version)) {
			return fmt.Errorf("aborted by operator")
		}
	}

	// 8) Download, bind to the signed digest, extract, swap.
	fmt.Fprintf(out, "downloading %s %s/%s ...\n", m.Version, o.goos, o.goarch)
	data, err := src.fetchArtifact(ctx, m, plan.Artifact)
	if err != nil {
		return fmt.Errorf("download artifact: %w", err)
	}
	if err := release.VerifyArtifactSHA256(data, plan.Artifact.SHA256); err != nil {
		return fmt.Errorf("REFUSING to upgrade: %w", err)
	}
	sum := sha256.Sum256(data)
	fmt.Fprintf(out, "artifact: %s verified (sha256 %s…)\n", plan.Artifact.Filename, hex.EncodeToString(sum[:])[:12])

	bin, err := extractBinary(data, binaryName)
	if err != nil {
		return fmt.Errorf("extract binary: %w", err)
	}
	// RE-READ THE TARGET IMMEDIATELY BEFORE THE SWAP (C03-23).
	//
	// Every ordering guard above — up-to-date, min_version, anti-rollback — was decided
	// against `current`, read at step 2, before a manifest fetch and an artifact download
	// that can take minutes. The lock stops another olivares upgrade from moving the binary
	// inside that window, but it cannot stop a package manager, an image rollout or an
	// operator with `cp`, and none of those take our lock. If the target changed, the
	// verdict "this is a forward step" was computed about a file that is no longer there.
	if err := refuseIfTargetMoved(target, before); err != nil {
		return err
	}
	backup, newVer, err := atomicSwap(target, bin)
	if err != nil {
		return err
	}
	// Record a forced downgrade in the audit log ONLY after the swap actually
	// happened — an audit line for a rollback that was aborted or failed would be a lie.
	// `from` is marked when it was DECLARED rather than measured: this record exists to
	// prove a downgrade was deliberate, and a record that cannot tell an operator's claim
	// from a reading of the box is not evidence of anything.
	if plan.IsRollback() && o.forceRollback {
		from := current.Version.Raw
		if current.Declared {
			from += "(declared)"
		}
		if err := auditRollback(o, from, m.Version, target); err != nil {
			// Appended, not printed twice: the stderr line below is untouched on both
			// panes, and the document carries the same sentence so a machine consumer
			// of `warnings` learns that this downgrade left no audit record. See the
			// note on upgradeResult.Warnings for why it cannot be collected earlier.
			res.Warnings = append(res.Warnings, fmt.Sprintf("rollback done but audit record failed: %v", err))
			fmt.Fprintf(cmd.ErrOrStderr(), "WARNING: rollback done but audit record failed: %v\n", err)
		} else {
			fmt.Fprintf(cmd.ErrOrStderr(), "AUDIT: forced rollback %s -> %s recorded.\n", from, m.Version)
		}
	}
	res.Action = upgradeActionInstalled
	res.Installed = newVer
	res.Backup = backup
	return renderOut(cmd, func(w io.Writer) error {
		fmt.Fprintf(w, "\ninstalled: %s is now %s\n", target, newVer)
		fmt.Fprintf(w, "rollback: the previous binary is backed up at %s (restore it to revert)\n", backup)
		fmt.Fprintln(w, "next: restart the service to run the new binary — for zero downtime use a drain +")
		if _, werr := fmt.Fprintln(w, "      handover (single node) or a rolling restart (HA); see docs/UPGRADE-AND-ROLLBACK.md."); werr != nil {
			return werr
		}
		if o.enterprise {
			_, werr := fmt.Fprintln(w, "      then `olivares enterprise enable <preset>` to activate the add-ons.")
			return werr
		}
		return nil
	}, res)
}

// buildUpdateSource selects the transport and (for --bundle) returns a cleanup for
// any temp extraction. TWO of the three routes enforce the license gate up front, here,
// before a single byte is read: --enterprise, and --bundle when it is about to INSTALL.
// Only the public community channel is ungated.
//
// --bundle IS ONE OF THEM, AND THAT HALF WAS MISSING (C02-20). This comment used to read
// "the enterprise path enforces the license gate up front", which was true of the worker
// route and silent about the other way to obtain the same bytes: the bundle branch
// RETURNED its source before requireValidLicense ran at all, so whoever held a tarball
// installed from it with no credential, no token and no network — and --help advertised
// exactly that ("100% offline"). The gate was not weak on that route; it was not on it.
// A comment that is true of one of two branches reads as true of the function, and this
// one sat directly above the branch it did not describe.
//
// WHY EVERY BUNDLE AND NOT ONLY AN ENTERPRISE ONE: nothing authenticated inside a bundle
// says which edition its artifact is. The signed manifest has no edition/set field
// (core/release/manifest.go, Manifest), and the artifact's SHAPE is a heuristic, not a
// signed claim — so this route cannot tell a community bundle from an enterprise one.
// Not being able to look does not authorize running less, so it fails CLOSED for every
// bundle. When Manifest carries the set, the gate can narrow to what the bundle declares;
// it cannot narrow on a guess. What that costs today — an operator with no license has no
// offline route for a community INSTALL — is stated in the flag's own help and in
// docs/UPGRADE-AND-ROLLBACK.md §10 rather than left for one to discover at the air gap.
//
// WHAT IS REQUIRED IS THE LICENSE AT REST, NOT AN ENTITLEMENT LOOKUP: requireValidLicense
// resolves the installed license file and verifies it OFFLINE against this build's
// embedded license key. No registry, no worker, no network — asking the registry here
// would contradict the air gap this route exists to serve, and then "air-gapped" and
// "gated" could not both be true of the same command.
//
// --check IS NOT GATED, AND THAT IS A DECISION, NOT AN OVERSIGHT. What this closes is an
// unlicensed INSTALL; --check installs nothing (runUpgrade returns at its `if o.check`
// arm, before checkWritable, the target lock, the artifact read and atomicSwap), and it
// hands the holder of a leaked tarball nothing they do not already have — they hold the
// bytes. Gating it would cost three real things and buy none: the release ceremony's
// updater smoke test runs `--bundle … --check` on the SHIPPED community binary
// (.github/workflows/release.yml), the key-domain battery mainline-ci runs drives this
// route three times with --check and one of those is a POSITIVE control
// (scripts/test-key-domain-separation.sh), and docs/UPGRADE-AND-ROLLBACK.md §10 teaches
// operators to check a bundle before installing it. A gate that made --check fail would
// teach them to skip straight to --yes, which is a worse habit than the one it protects.
// The invariant it leans on — --check never installs — is not left to trust: the witness
// asserts the binary is untouched after an ungated `--bundle --check`.
func buildUpdateSource(o *upgradeOptions) (updateSource, func(), error) {
	if o.bundle != "" {
		// Refuse BEFORE openBundle: an unauthorized caller must not get this process to
		// extract an untrusted tarball into a temp dir on its way to being refused.
		if !o.check {
			if _, err := requireValidLicense(o.license, o.dataDir); err != nil {
				return nil, nil, fmt.Errorf("REFUSING to install from --bundle: installing from a local bundle is gated on a live license, checked OFFLINE against this build's embedded license key (no network, no registry). `--bundle --check` is not gated and verifies this bundle without installing it: %w", err)
			}
		}
		dir, cleanup, err := openBundle(o.bundle)
		if err != nil {
			return nil, nil, err
		}
		return bundleSource{dir: dir}, cleanup, nil
	}
	// cli-transport-exempt: an OTA download, whose trust anchor is the Ed25519
	// manifest signature and the cosign-verified checksums verified AFTER the
	// bytes arrive (docs/RELEASE-VERIFICATION.md) — not TLS. It reaches a public
	// release endpoint, not the operator's control plane, so the client context
	// and its pins do not apply and must not be attached to it.
	client := &http.Client{Timeout: o.timeout}
	if o.enterprise {
		// The gated download is the self-serve counterpart to a live license — refuse
		// locally with the next step rather than fetch and be rejected by the worker.
		claims, err := requireValidLicense(o.license, o.dataDir)
		if err != nil {
			return nil, nil, err
		}
		if strings.TrimSpace(o.token) == "" {
			return nil, nil, fmt.Errorf("--token is required for --enterprise (it is in your license/fulfillment email)")
		}
		if o.endpoint == "" {
			o.endpoint = defaultEnterpriseEndpoint
		}
		_ = claims // licensee is informational; the worker re-checks entitlement
		return gatedSource{o: o, client: client}, nil, nil
	}
	base := o.endpoint
	if base == "" {
		base = defaultCommunityEndpoint
	}
	// The LAYOUT is decided from the endpoint's shape, and a github.com endpoint that is
	// neither a repository nor a releases base is REFUSED rather than treated as a static
	// host (release.ResolveChannel). A wrong endpoint must read as a wrong endpoint, not
	// as an unreachable channel.
	src, err := buildCommunitySource(base, o.channel, client)
	if err != nil {
		return nil, nil, err
	}
	return src, nil, nil
}

// installedVersion is what the updater KNOWS about the binary at --target. Known is
// the whole point: anti-rollback and min_version are ordering guards, and an ordering
// guard fed a guess is not a guard.
type installedVersion struct {
	Version release.Version
	Known   bool
	// Reason names WHY it is unknown, in the operator's terms. There are exactly two
	// ways not to know and they share this one type, one resolution and one refusal
	// — an unstamped build asked about itself, and a target whose exec-probe
	// could not run. They are the same missing fact (no position in the ordering) with
	// the same remedy (--current-version), so they must not become two guards that
	// drift apart; only the sentence shown to the operator differs.
	Reason string
	// Declared records that this version was ASSERTED by the operator via
	// --current-version rather than measured, so the audit trail can say which it was.
	// A record that cannot tell a measurement from a claim is not evidence.
	Declared bool
}

// currentInstalledVersion resolves the version installed AT TARGET.
//
// THE MEASUREMENT WINS. The target's own exec-probe (or, for the default target, this
// build's stamp — the running executable IS this build) is the fact; --current-version
// is a FALLBACK for when nothing can be measured, never an override of what was. The
// first cut of this had it backwards: the declaration was consulted first, so
// `--current-version 26.0.0` beat a working probe reading 26.8.0 and installed a
// 26.7.0 DOWNGRADE with exit 0 and no audit line — an unaudited bypass of
// anti-rollback, and of min_version too, which not even --force-rollback can override.
// The escape hatch was strictly more powerful than the audited door beside it, and the
// refusal message advertised it. A declaration that CONTRADICTS a successful probe is
// now refused outright: the operator's belief about the box is demonstrably wrong, and
// the useful answer is to say so rather than to pick a side.
//
// It NEVER substitutes this process's main.version for a --target it was asked about —
// that is a different binary, and reporting it as `current:` was both a false statement
// to the operator and a false input to anti-rollback.
func currentInstalledVersion(ctx context.Context, target, declared string, targetIsSelf bool) installedVersion {
	// Validate the declaration before anything else, so a malformed one is an error even
	// when the probe could have answered — a flag that is silently ignored is a trap.
	var declaredV release.Version
	haveDeclared := false
	if declared != "" {
		d := strings.TrimSpace(declared)
		switch {
		case d == "":
			return installedVersion{Reason: "--current-version was given but is blank"}
		case release.IsUnstamped(d):
			return installedVersion{Reason: fmt.Sprintf("--current-version %q is not a version, it is the absence of one", d)}
		}
		v, err := release.ParseVersion(d)
		if err != nil {
			return installedVersion{Reason: fmt.Sprintf("--current-version %q is not a valid version: %v", d, err)}
		}
		declaredV, haveDeclared = v, true
	}

	// 1) Measure.
	measured, ok := release.Version{}, false
	probeErr := "no target to probe"
	if target != "" {
		line, err := execProbe(ctx, target)
		switch {
		case err != nil:
			probeErr = fmt.Sprintf("%s could not be run to ask its version: %v", target, err)
		case release.IsUnstamped(versionToken(line)):
			probeErr = fmt.Sprintf("%s reports %q — it is an unstamped build and carries no version", target, versionToken(line))
		default:
			v, perr := release.ParseVersion(versionToken(line))
			if perr != nil {
				probeErr = fmt.Sprintf("%s reported an unparseable version %q: %v", target, versionToken(line), perr)
			} else {
				measured, ok = v, true
			}
		}
	}
	// The default target is THIS running executable, so main.version is authoritative
	// for it — when it is stamped at all.
	if !ok && targetIsSelf {
		if release.IsUnstamped(version) {
			probeErr = "this binary was built from source without a version stamp (main.version=" + version + ")"
		} else if v, err := release.ParseVersion(version); err == nil {
			measured, ok = v, true
		}
	}

	// 2) A measurement settles it — and a contradicting declaration is a refusal, not a
	//    tie the operator gets to win.
	if ok {
		if haveDeclared && release.Compare(measured, declaredV) != 0 {
			return installedVersion{Reason: fmt.Sprintf(
				"--current-version says %s but %s reports %s — refusing to act on a declaration the target contradicts (drop the flag to use what is actually installed)",
				declaredV.Raw, target, measured.Raw)}
		}
		return installedVersion{Version: measured, Known: true}
	}

	// 3) Nothing measurable: the declaration is the only thing left, and it is marked as
	//    a claim so the audit record can say so.
	if haveDeclared {
		return installedVersion{Version: declaredV, Known: true, Declared: true}
	}
	return installedVersion{Reason: probeErr}
}

// versionToken pulls the semver token out of a `version` line
// ("olivares 26.7.0 (commit …)" -> "26.7.0").
func versionToken(line string) string {
	fields := strings.Fields(line)
	if len(fields) >= 2 && strings.EqualFold(fields[0], "olivares") {
		return fields[1]
	}
	if len(fields) >= 1 {
		return fields[0]
	}
	return ""
}

// printPlan renders the human-readable upgrade plan (also the body of --check).
func printPlan(out io.Writer, o *upgradeOptions, m release.Manifest, plan release.UpgradePlan, current installedVersion) {
	fmt.Fprintf(out, "channel:   %s\n", m.Channel)
	// An unknown version is printed as unknown, WITH its cause. Printing this build's
	// own main.version here was the visible half of the defect: `current: dev`
	// read as a fact about the target, and it was never asked.
	switch {
	case !plan.CurrentKnown:
		fmt.Fprintf(out, "current:   UNKNOWN — %s\n", current.Reason)
	case current.Declared:
		fmt.Fprintf(out, "current:   %s (declared with --current-version, not measured)\n", plan.Current.Raw)
	default:
		fmt.Fprintf(out, "current:   %s\n", plan.Current.Raw)
	}
	fmt.Fprintf(out, "available: %s (released %s)\n", m.Version, m.ReleasedAt.Format("2006-01-02"))
	switch {
	// An unknown current version yields NO ordering claim. IsUpToDate and IsRollback are
	// both false when CurrentKnown is false, so without this arm every unknown fell into
	// the default and printed "upgrade available" — the exact sentence this session cites
	// as the fail-open's signature, two lines under the word UNKNOWN.
	case !plan.CurrentKnown:
		fmt.Fprintln(out, "status:    UNKNOWN — no upgrade, downgrade or up-to-date claim can be made")
	case plan.IsUpToDate():
		fmt.Fprintln(out, "status:    up to date")
	case plan.IsRollback():
		fmt.Fprintln(out, "status:    DOWNGRADE (blocked unless --force-rollback)")
	default:
		fmt.Fprintln(out, "status:    upgrade available")
	}
	if m.Security {
		adv := "security release"
		if len(plan.Advisories) > 0 {
			adv += " — fixes " + strings.Join(plan.Advisories, ", ")
		}
		fmt.Fprintf(out, "security:  %s\n", adv)
	}
	if m.MinVersion != "" {
		note := cond(plan.MinTooOld, " (YOU ARE TOO OLD — step through an intermediate)", "")
		if !plan.CurrentKnown {
			note = " (NOT CHECKABLE — the installed version is unknown)"
		}
		fmt.Fprintf(out, "min_ver:   %s%s\n", m.MinVersion, note)
	}
	if !plan.Eligible {
		fmt.Fprintln(out, "rollout:   this node is NOT yet in the staged-rollout cohort")
	}
	if m.EOLAt != nil {
		fmt.Fprintf(out, "eol:       channel %s reaches end-of-life %s\n", m.Channel, m.EOLAt.Format("2006-01-02"))
	}
	if strings.TrimSpace(m.Notes) != "" {
		fmt.Fprintf(out, "notes:     %s\n", m.Notes)
	}
}

func cond(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}

// resolveInstallID returns a stable per-install identity for staged-rollout
// bucketing: a random id persisted in the data dir on first use, else the hostname.
// It is NOT a secret and never leaves the node except as an anonymous rollout bucket.
func resolveInstallID(dataDir string) string {
	dir := dataDir
	if dir == "" {
		resolved, err := defaultDataDir()
		if err != nil {
			// This function already degrades to the hostname when it cannot read a
			// persisted id, and an unresolvable data dir is that same case: an
			// anonymous rollout bucket is never worth guessing a filesystem path for.
			return installIDFallback()
		}
		dir = resolved
	}
	p := filepath.Join(dir, "install-id")
	if b, err := os.ReadFile(p); err == nil {
		if id := strings.TrimSpace(string(b)); id != "" {
			return id
		}
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err == nil {
		id := hex.EncodeToString(buf)
		if err := os.MkdirAll(dir, 0o700); err == nil {
			_ = os.WriteFile(p, []byte(id+"\n"), 0o600)
		}
		return id
	}
	return installIDFallback()
}

// installIDFallback is the non-filesystem identity: the hostname, else a constant.
// Factored out because resolveInstallID reaches it from two places — no persisted
// id, and no resolvable data directory to persist one in.
func installIDFallback() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "unknown-install"
}

// auditRollback appends a tamper-evident-intent record of a forced downgrade to the
// data dir. A standalone CLI has no engine ledger to sign into, so this is an honest
// append-only local record (who/when/from/to/target) plus the loud stderr AUDIT line
// and the kept .bak — the operator's evidence that a downgrade was deliberate.
func auditRollback(o *upgradeOptions, from, to, target string) error {
	dir := o.dataDir
	if dir == "" {
		resolved, derr := defaultDataDir()
		if derr != nil {
			return derr
		}
		dir = resolved
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	who := "unknown"
	if u, err := user.Current(); err == nil {
		who = u.Username
	}
	rec := fmt.Sprintf("%s\tforce-rollback\tfrom=%s\tto=%s\ttarget=%s\tby=%s\n",
		time.Now().UTC().Format(time.RFC3339), from, to, target, who)
	f, err := os.OpenFile(filepath.Join(dir, "upgrade-audit.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = f.WriteString(rec)
	return err
}

// requireValidLicense resolves the at-rest license and requires it to verify and be
// live (valid or perpetual). It returns a clear next step otherwise. (reused.)
//
// TWO routes call it now — the gated enterprise download and --bundle (C02-20) — so its
// refusals name no flag: they used to end in "re-run `upgrade --enterprise`", which is the
// wrong next step for an operator who typed --bundle. The route each caller is about is
// added by the caller, which is the only place that knows it.
func requireValidLicense(explicitPath, dataDir string) (license.Verified, error) {
	dir := dataDir
	if dir == "" {
		resolved, derr := defaultDataDir()
		if derr != nil {
			return license.Verified{}, derr
		}
		dir = resolved
	}
	src, err := resolveLicense(explicitPath, dir, osGetenv)
	if err != nil {
		return license.Verified{}, err
	}
	if src.Blob == "" {
		return license.Verified{}, fmt.Errorf("no license installed: run `olivares license install <file>` first " +
			"(the enterprise download and --bundle are both gated on a live license)")
	}
	pub := license.DefaultPublicKey()
	if len(pub) == 0 {
		return license.Verified{}, fmt.Errorf("this build embeds no license key (license-key=%s); cannot verify the installed license", license.KeyOrigin())
	}
	lic, verr := license.VerifyEnvelope(src.Blob, pub)
	if verr != nil {
		return license.Verified{}, fmt.Errorf("the installed license does not verify against this build's key: %w", verr)
	}
	// StatusPerpetual is unreachable since the v8 package made every offer term-only,
	// but the arm stays: the constant is still exported for compatibility, and a build
	// that silently changed its answer if it ever came back would be worse than a dead
	// case (core/license, StatusPerpetual).
	switch st := lic.Status(time.Now().UTC()); st {
	case license.StatusExpired:
		return license.Verified{}, fmt.Errorf("the installed license is EXPIRED: renew it, install the reissued license, then re-run the upgrade")
	case license.StatusValid, license.StatusPerpetual:
		return lic, nil
	case license.StatusGrace:
		// A license inside its attested grace window is in a BILLING failure, not a
		// software one, and it says so. The old text was "the installed license is not
		// live (status grace)", which named a state and no action — an operator whose
		// renewal charge bounced could not tell what to do from it. Refusing is still
		// right: fetching a new enterprise binary is not the way out of a lapsed term.
		return license.Verified{}, fmt.Errorf(
			"the installed license is inside its GRACE window (its term ended %s): settle the renewal, install the reissued license, then re-run the upgrade — the engine keeps running meanwhile",
			lic.Term().UTC().Format(time.RFC3339))
	default:
		return license.Verified{}, fmt.Errorf("the installed license is not live (status %s)", st)
	}
}

// resolveReleaseKey returns the verification key: an explicit --pubkey (base64 or
// @file) when given, else the key embedded in this build. Absent both fails closed.
func resolveReleaseKey(flag string) (ed25519.PublicKey, string, error) {
	flag = strings.TrimSpace(flag)
	if flag == "" {
		pub := release.EmbeddedKey()
		if pub == nil {
			return nil, "", fmt.Errorf("%w", release.ErrNoKey)
		}
		return pub, "embedded", nil
	}
	raw := flag
	if strings.HasPrefix(flag, "@") {
		b, err := os.ReadFile(flag[1:])
		if err != nil {
			return nil, "", fmt.Errorf("read --pubkey file: %w", err)
		}
		raw = strings.TrimSpace(string(b))
	}
	pub, err := release.DecodePublicKey(raw)
	if err != nil {
		return nil, "", err
	}
	return pub, "--pubkey", nil
}

// downloadGated fetches one gated object from the licensed worker (gate
// contract): kind "" is the binary; "manifest"/"manifest.sig" the signed channel
// manifest and its detached signature (extends the gate with these two kinds).
func downloadGated(ctx context.Context, client *http.Client, o *upgradeOptions, kind string) ([]byte, error) {
	base, err := url.Parse(strings.TrimRight(o.endpoint, "/") + "/download")
	if err != nil {
		return nil, fmt.Errorf("bad --endpoint: %w", err)
	}
	q := base.Query()
	q.Set("token", o.token)
	q.Set("os", o.goos)
	q.Set("arch", o.goarch)
	if o.channel != "" {
		q.Set("channel", o.channel)
	}
	if kind != "" {
		q.Set("kind", kind)
	}
	base.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "olivares-upgrade")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxArtifactBytes))
}

// resolveTargetBinary resolves the binary path to replace: an explicit --target,
// else the running executable with symlinks resolved.
func resolveTargetBinary(explicit string) (string, error) {
	if explicit != "" {
		return filepath.Abs(explicit)
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cannot resolve the running executable (pass --target): %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return exe, nil
}

// checkWritable verifies the target file and its directory are writable.
func checkWritable(target string) error {
	dir := filepath.Dir(target)
	probe, err := os.CreateTemp(dir, ".olivares-upgrade-probe-*")
	if err != nil {
		return fmt.Errorf("the install directory %s is not writable (upgrade this deployment via its package/image instead): %w", dir, err)
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	if fi, err := os.Stat(target); err == nil && fi.Mode()&0o200 == 0 {
		return fmt.Errorf("the binary %s is read-only; make it writable or upgrade via the package/image", target)
	}
	return nil
}

// atomicSwap installs newBytes at target atomically, keeping a timestamped backup
// for rollback. It NEVER replaces the binary until the candidate exec-probes OK, and
// reverts to the backup if the post-swap probe fails. Returns the backup path and
// the new binary's `version` line. (reused verbatim.)
func atomicSwap(target string, newBytes []byte) (string, string, error) {
	// The exec-probes are LOCAL operations with their own short timeout; they must
	// NOT inherit the (possibly exhausted) network-download deadline, or a slow
	// download would spuriously fail the post-swap probe and roll back a valid
	// upgrade. Use a fresh background context for the probes.
	probeCtx := context.Background()
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".olivares-upgrade-new-*")
	if err != nil {
		return "", "", err
	}
	tmpName := tmp.Name()
	cleanupTmp := true
	defer func() {
		if cleanupTmp {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(newBytes); err != nil {
		_ = tmp.Close()
		return "", "", err
	}
	if err := tmp.Close(); err != nil {
		return "", "", err
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return "", "", err
	}

	// Exec-probe the CANDIDATE before touching the live binary — the strongest
	// rollback is the one you never needed.
	newVer, err := execProbe(probeCtx, tmpName)
	if err != nil {
		return "", "", fmt.Errorf("the downloaded binary failed its self-check (not swapped): %w", err)
	}

	// THE BACKUP PATH MUST BE UNIQUE, AND SECONDS ARE NOT UNIQUE (C03-23).
	//
	// This was `fmt.Sprintf("%s.bak-%d", target, time.Now().Unix())`. Two installs that
	// reached this line in the same second computed the SAME path, and the second
	// copyFilePreserve overwrote the first's backup — so run A's automatic rollback
	// restored run B's binary and its post-swap probe passed, reporting a successful
	// rollback to a version nobody asked for. The upgrade lock now makes two concurrent
	// olivares upgrades impossible, but a backup name that relies on nothing else running
	// is a guarantee borrowed from another mechanism; O_EXCL makes it the file system's.
	//
	// The timestamp stays in the name because operators read it to find the previous
	// binary, and the tests glob `<target>.bak-*`. os.CreateTemp appends its own random
	// suffix and creates with O_EXCL, so the name is unique even at the same second, and
	// the timestamp keeps its ordering role.
	stamp := fmt.Sprintf("%s.bak-%d-", target, time.Now().Unix())
	backup, err := uniqueBackupPath(stamp)
	if err != nil {
		return "", "", fmt.Errorf("reserve a backup path for the current binary: %w", err)
	}
	if err := copyFilePreserve(target, backup); err != nil {
		return "", "", fmt.Errorf("back up the current binary: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		return "", "", fmt.Errorf("swap in the new binary: %w", err)
	}
	cleanupTmp = false

	// Post-swap verification: the installed target must run. If not, roll back.
	if _, err := execProbe(probeCtx, target); err != nil {
		if rbErr := os.Rename(backup, target); rbErr != nil {
			return "", "", fmt.Errorf("the new binary does not run AND rollback failed (restore %s manually): probe=%v rollback=%v", backup, err, rbErr)
		}
		return "", "", fmt.Errorf("the new binary did not run after the swap; rolled back to the previous binary: %w", err)
	}
	return backup, newVer, nil
}

// execProbe runs `<bin> version` with a short timeout and returns its trimmed first
// line, requiring a zero exit and recognizable output.
func execProbe(ctx context.Context, bin string) (string, error) {
	pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(pctx, bin, "version")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%v (output: %s)", err, strings.TrimSpace(buf.String()))
	}
	line := strings.TrimSpace(buf.String())
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	if !strings.Contains(strings.ToLower(line), "olivares") {
		return "", fmt.Errorf("unexpected version output: %q", line)
	}
	return line, nil
}

// copyFilePreserve copies src to dst preserving mode; dst is written atomically.
func copyFilePreserve(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	// THE STAGING FILE MUST BE UNIQUE TOO (C03-23).
	//
	// This staged at the FIXED path `dst + ".tmp"` with O_TRUNC. Two callers copying to
	// the same dst interleaved their writes into one file and then each published it with
	// os.Rename, so the "atomic" copy could publish a torn mixture of two binaries. A
	// backup that exists and is corrupt is worse than one that is missing: the rollback
	// path finds a file, restores it, and only the exec-probe stands between that and a
	// dead install. CreateTemp is O_EXCL with a random suffix, so each caller stages its
	// own file and the rename remains the atomic publish.
	out, err := os.CreateTemp(filepath.Dir(dst), filepath.Base(dst)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := out.Name()
	// CreateTemp makes 0o600; the copy must preserve the source's mode, which for a
	// binary being backed up is what makes the restored file runnable.
	if err := out.Chmod(fi.Mode().Perm()); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// confirm prompts on stderr and reads a yes/no from stdin.
func confirm(cmd *cobra.Command, prompt string) bool {
	fmt.Fprintf(cmd.ErrOrStderr(), "%s [y/N]: ", prompt)
	r := bufio.NewReader(cmd.InOrStdin())
	line, _ := r.ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}
