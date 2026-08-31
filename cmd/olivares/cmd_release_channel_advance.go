// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/release"
)

// cmd_release_channel_advance.go is the CFG-06 MONOTONICITY FENCE: the publisher-side
// half of "a channel never goes backwards".
//
// WHY IT HAS TO EXIST NOW, AND DID NOT BEFORE. Until FIRMA B (2026-08-21) the
// community channel was to be a static path the publisher OVERWRITES, where going
// backwards takes a deliberate bad write. On GitHub Releases it takes no mistake at all:
// `/releases/latest/download/` follows GitHub's "latest release" pointer, and GitHub
// picks that by PUBLICATION ORDER, not by version. Publish a patch for an older line
// after a newer minor — 26.8.1 after 26.9.0, exactly what a security backport looks
// like — and the community channel silently starts offering the OLDER release to
// everybody. Nothing in the release workflow notices, because nothing in it reads the
// live channel.
//
// The client half already exists and is not enough on its own: anti-rollback refuses to
// downgrade a binary without an audited --force-rollback, so nobody is moved backwards —
// but every up-to-date node then sits on a channel that offers it nothing, and a node
// that installs fresh takes the older release. "Nobody is harmed" is not "the channel is
// correct".
//
// WHAT IT COMPARES, AND WHY IT IS THIS BINARY THAT COMPARES IT. The ordering is
// release.Compare — the SAME precedence the updater applies when it decides whether an
// upgrade is a step forward. Reimplementing semver precedence in the ceremony's shell
// (or in a python helper) would give the publisher a second opinion about ordering, and
// two orderings that can disagree are worse than one: the fence would bless exactly the
// publications the client later refuses. The URL is resolved through
// release.ResolveChannel for the same reason — the fence must read the URL the CLIENT
// will read, not a second spelling of it.
//
// ANSWERS, on this binary's documented exit contract (root Long):
//
//	0                            the publication ADVANCES the channel (or first publication)
//	1  (exitcode.Err)            it would FREEZE or REGRESS it — refuse to publish
//	2  (exitcode.Usage)          the invocation or its inputs are wrong (bad --candidate, bad
//	                             --channel, an endpoint this command cannot use)
//	8  (exitcode.Indeterminate)  the live channel could not be READ, AUTHENTICATED or trusted
//	                             — NOT clean, and never to be read as a pass
//
// 8 rather than 2 for the third: 2 is this binary's USAGE code, and a caller told "you typed
// it wrong" when the truth is "I could not look" has been told the wrong thing. The reverse
// mattered too — until the contrast pointed it out, an unreadable candidate exited 1, which
// this table reserves for "I compared and found a regression".
//
// ⛔ AND THE LIVE VERSION IS ONLY BELIEVED WHEN ITS SIGNATURE VERIFIES. The first cut made
// --pubkey optional and, with no key, compared against whatever bytes came back — with a
// comment claiming the failure direction was safe because "a forged NEWER live version can
// only make this REFUSE". That is one direction of two, and the other one is the dangerous
// one: a forged or REPLAYED OLDER live version makes this answer ADVANCES while the real head
// is newer. An unsafe acceptance is the only failure a refusal control must never have, and
// the reasoning that missed it was mine. The key is now required — the embedded OTA anchor by
// default, --pubkey to override — and a build with neither cannot answer, so it exits 8.

type channelAdvanceOptions struct {
	endpoint  string
	channel   string
	candidate string
	pubkey    string
	timeout   time.Duration
}

func newReleaseVerifyChannelAdvanceCmd() *cobra.Command {
	o := &channelAdvanceOptions{}
	cmd := &cobra.Command{
		Use:   "verify-channel-advance",
		Short: "Refuse a channel publication that would not move the LIVE channel forward (CFG-06 monotonicity fence)",
		Long: "Reads the manifest the update channel serves RIGHT NOW and compares its version to\n" +
			"the one you are about to publish (--candidate). The publication is accepted only if\n" +
			"it moves the channel strictly forward.\n\n" +
			"It exists because the community carrier is GitHub Releases, whose `latest` pointer is\n" +
			"chosen by publication order and NOT by version: publishing a backport tag after a\n" +
			"newer one silently points the whole channel at the older release. Run it in the\n" +
			"release ceremony BEFORE publishing the draft, and again AFTER publishing to confirm\n" +
			"the live channel is the release you just cut.\n\n" +
			"A channel that is not published yet (the manifest asset 404s) is a legitimate FIRST\n" +
			"publication and passes, saying so.\n\n" +
			"Exit codes: 0 advances · 1 would freeze or regress · 8 the live channel could not be\n" +
			"read (NOT a clean result — treat it as unanswered).",
		// ⛔ THE EXAMPLE IS A PLACEHOLDER, NOT A REAL REPOSITORY, and that is not style. This
		// string is `--help` output: it SHIPS, and a value ships verbatim where a comment is
		// scrubbed. Naming the maintainers' own disposable rehearsal repository here would put
		// a private org in front of every reader of the public binary. The export gate caught
		// exactly this line on 2026-08-27.
		Example: "  # before publishing the draft for v26.8.1\n" +
			"  olivares release verify-channel-advance --candidate dist/stable-manifest.json\n\n" +
			"  # against a disposable rehearsal repository, pinned to its tag\n" +
			"  olivares release verify-channel-advance --candidate dist/stable-manifest.json \\\n" +
			"    --endpoint https://github.com/<owner>/<repo>/releases/tag/<tag>",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE:         func(cmd *cobra.Command, _ []string) error { return runReleaseVerifyChannelAdvance(cmd, o) },
	}
	f := cmd.Flags()
	f.StringVar(&o.candidate, "candidate", "", "the manifest JSON about to be published (required)")
	f.StringVar(&o.endpoint, "endpoint", defaultCommunityEndpoint,
		"the channel to read: a GitHub repository, one of its releases, or a static mirror base")
	f.StringVar(&o.channel, "channel", release.ChannelStable, "channel to compare (stable | security | lts)")
	f.StringVar(&o.pubkey, "pubkey", "",
		"base64 or @file Ed25519 OTA key; when set the LIVE manifest's signature is verified before its version is believed")
	f.DurationVar(&o.timeout, "timeout", 60*time.Second, "network timeout for reading the live channel")
	return cmd
}

func runReleaseVerifyChannelAdvance(cmd *cobra.Command, o *channelAdvanceOptions) error {
	out := cmd.OutOrStdout()
	indeterminate := func(format string, a ...any) error {
		return exitcode.New(exitcode.Indeterminate, fmt.Errorf("COULD NOT LOOK: "+format, a...))
	}
	usage := func(format string, a ...any) error {
		return exitcode.New(exitcode.Usage, fmt.Errorf(format, a...))
	}
	if strings.TrimSpace(o.candidate) == "" {
		return usage("--candidate is required: without the manifest you are about to publish there is nothing to compare")
	}
	if !release.ValidChannel(o.channel) {
		return usage("--channel %q is not one of %s", o.channel, strings.Join(release.Channels, ", "))
	}
	cb, err := os.ReadFile(o.candidate)
	if err != nil {
		return usage("read --candidate: %v", err)
	}
	// The candidate is OURS and unsigned at this point in the ceremony (phase 1 produces
	// it deliberately unsigned), so it is parsed, not verified. ParseManifest still
	// applies the schema and field checks, so a malformed candidate is refused here
	// rather than compared as if it were sound.
	cand, err := release.ParseManifest(cb)
	if err != nil {
		return usage("parse --candidate %s: %v", o.candidate, err)
	}
	if got := strings.TrimSpace(cand.Channel); got != o.channel {
		return usage("--candidate declares channel %q but --channel is %q: comparing a manifest against another channel's live version would bless a publication nobody checked", got, o.channel)
	}
	candV, err := release.ParseVersion(cand.Version)
	if err != nil {
		return usage("--candidate version %q: %v", cand.Version, err)
	}
	if release.IsUnstamped(cand.Version) {
		return usage("--candidate declares version %q, which has no position in the ordering: an unstamped candidate cannot be shown to advance anything", cand.Version)
	}

	// ⛔ THE KEY IS RESOLVED BEFORE ANYTHING IS FETCHED, and its absence is a refusal to answer
	// rather than a cheaper answer. See the header: an unauthenticated live version can be
	// forged in the direction that produces a FALSE ADVANCE.
	key, keyName, kerr := resolveReleaseKey(o.pubkey)
	if kerr != nil {
		return indeterminate("no OTA key to authenticate the live channel with (%v). This build embeds none, so pass --pubkey. Comparing against unauthenticated bytes is not a cheaper answer: a replayed OLDER live manifest would make this report an advance while the real head is newer", kerr)
	}

	layout, lerr := release.ResolveChannel(o.endpoint, o.channel)
	if lerr != nil {
		return usage("%v", lerr)
	}
	// ⛔ A PINNED ENDPOINT IS NOT THE CHANNEL HEAD, and this command's whole subject is the
	// head. Pointed at `…/releases/tag/<tag>` it would compare two authentic versions and
	// neither of them the live one — reporting an advance while the head is newer, which is
	// the same false green by another route. Refused by shape, with the form that works named.
	if layout.ReleaseAssets() && layout.Tag() != "" {
		return usage("--endpoint %q pins ONE release (%s), and this check is about the CHANNEL HEAD: comparing against a release you chose says nothing about what the channel serves. Point it at the repository instead (…/<owner>/<repo>), which resolves to whatever is currently latest", o.endpoint, layout.Tag())
	}
	// cli-transport-exempt: this reads a PUBLIC release channel, not the operator's control
	// plane. Its trust anchor is the Ed25519 signature verified above after the bytes arrive
	// (the key is resolved before anything is fetched), never TLS, so the client context and
	// its pins do not apply and must not be attached — exactly as the upgrade path documents
	// for the same endpoints.
	src, err := buildCommunitySource(o.endpoint, o.channel, &http.Client{Timeout: o.timeout})
	if err != nil {
		return usage("%v", err)
	}
	fmt.Fprintf(out, "candidate: %s %s (channel %s)\n", o.candidate, cand.Version, cand.Channel)
	fmt.Fprintf(out, "live:      %s\n", src.describe())
	fmt.Fprintf(out, "OTA key:   %s\n", keyName)

	ctx, cancel := context.WithTimeout(cmd.Context(), o.timeout)
	defer cancel()
	lb, lsig, err := src.fetchManifest(ctx)
	if err != nil {
		// ⛔ THE SHORTCUT BELONGS TO THE MANIFEST, NOT TO THE PAIR. A 404 on the MANIFEST
		// asset is the one failure that is a clean answer: the channel has never been
		// published, so this publication cannot regress it. A 404 on the SIGNATURE is the
		// opposite fact — a manifest IS live and its signature is missing — and the first
		// cut of this took one for the other, answering 0 with a live manifest in front of
		// it. The transport marks which fetch failed (errManifestSignature); the status is
		// matched as a NUMBER, never as the word "404" in a message.
		if errors.Is(err, errManifestSignature) {
			return indeterminate("the live %s channel serves a manifest but its detached signature could not be fetched (%v). That is a SPLIT PAIR, not an unpublished channel: a manifest whose signature is missing is one every conforming client refuses, and its version cannot be trusted enough to compare against", o.channel, err)
		}
		if isHTTPStatus(err, 404) {
			fmt.Fprintf(out, "\nOK: the %s channel publishes no manifest yet — this is its FIRST publication,\n"+
				"    so there is no live version it could regress.\n"+
				"    (What this shows is that the ASSET is absent. A repository that 404s for any\n"+
				"     other reason — wrong path, no access — answers the same way, so read it with\n"+
				"     the endpoint above in hand.)\n", o.channel)
			return nil
		}
		return indeterminate("the live %s channel could not be read: %v", o.channel, err)
	}
	live, err := release.VerifyManifest(lb, lsig, key)
	if err != nil {
		return indeterminate("the LIVE %s manifest does not verify under %s: %v", o.channel, keyName, err)
	}
	fmt.Fprintf(out, "live manifest signature: VERIFIED under %s\n", keyName)
	// ⛔ FRESHNESS, because VerifyManifest does not check it and both other readers of this
	// channel do (cmd_upgrade.go and core/updatecheck). An expired manifest is still validly
	// signed, so without this an old head replayed past its own freshness bound produces the
	// same false advance the signature check exists to prevent.
	if now := time.Now().UTC(); live.Stale(now) {
		return indeterminate("the live %s manifest EXPIRED at %s, so it is not a head to compare against: it may be a stale mirror or a replay, and every conforming client already refuses it", o.channel, live.Expires.Format(time.RFC3339))
	}
	if got := strings.TrimSpace(live.Channel); got != o.channel {
		return indeterminate("the live manifest declares channel %q, not %q: the endpoint serves a different channel than the one asked for, so nothing here compares what it claims to", got, o.channel)
	}
	liveV, err := release.ParseVersion(live.Version)
	if err != nil {
		return indeterminate("the live %s version %q does not parse: %v", o.channel, live.Version, err)
	}
	if release.IsUnstamped(live.Version) {
		return indeterminate("the live %s manifest declares version %q, which has no position in the ordering", o.channel, live.Version)
	}

	fmt.Fprintf(out, "live version:  %s\ncandidate:     %s\n", live.Version, cand.Version)
	switch c := release.Compare(candV, liveV); {
	case c > 0:
		fmt.Fprintf(out, "\nOK: publishing %s ADVANCES the %s channel from %s.\n", cand.Version, o.channel, live.Version)
		return nil
	case c == 0:
		return fmt.Errorf("REFUSING: the %s channel already serves %s. Re-publishing the same version does not advance it, and on a carrier whose `latest` moves by publication order it can only move the pointer sideways",
			o.channel, live.Version)
	default:
		return fmt.Errorf("REFUSING: the %s channel serves %s and this publication would take it BACK to %s.\n"+
			"On GitHub Releases `latest` follows publication order, not version, so publishing this as the latest release points every community client at the older one.\n"+
			"If this is a deliberate backport, publish it with make_latest=false so it never becomes the channel head (docs/RELEASE-GO-LIVE-RUNBOOK.md), then re-run this check",
			o.channel, live.Version, cand.Version)
	}
}
