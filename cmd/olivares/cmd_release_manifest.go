// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/core/release"
)

// cmd_release_manifest.go is the PRODUCER side of the OTA framework:
// `olivares release manifest` builds — and optionally Ed25519-signs — a per-channel
// update manifest from a directory of release archives. It is the single source of
// the on-wire manifest format (it marshals core/release.Manifest), so the generator
// can never drift from the verifier the client runs. It is used by the release
// pipeline / signing ceremony and by scripts/export-update-bundle.sh (air-gap).
//
// Signing is OPTIONAL: with --sign-key it emits manifest.json + manifest.json.sig
// (the local air-gap path); without it, it emits the unsigned manifest for the
// offline release ceremony.
// `release sign-manifest` signs those exact bytes off-box after CI generation. The
// matching PUBLIC OTA key is embedded in core/release.artifactVerifyKeyB64 and is
// deliberately independent from the online license-signing key.

// knownGOOS bounds artifact filename parsing so a stray file cannot masquerade as
// a platform archive.
var knownGOOS = map[string]bool{"linux": true, "darwin": true, "windows": true, "freebsd": true}

func newReleaseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "release",
		Short: "Release/OTA tooling (manifest generation) — ops use",
		Long: "release builds the per-channel OTA manifest that `olivares upgrade` verifies\n" +
			"before it swaps a binary, signs that manifest during the off-box release\n" +
			"ceremony, and cross-checks a finished manifest against the cosign-verified\n" +
			"checksums.txt.\n\n" +
			"Hidden because it is release-engineering tooling, not an operator surface: the\n" +
			"signing half runs on the ceremony host, never on a serving node.",
		Example: "  olivares release manifest --version 26.8.0 --dir ./dist\n" +
			"  olivares release verify-manifest --manifest stable-manifest.json\n" +
			"  olivares release verify-channel-advance --candidate stable-manifest.json\n" +
			"  olivares release sign-manifest --manifest stable-manifest.json --sign-key @prod-ota.key",
		Hidden: true,
	}
	cmd.AddCommand(newReleaseManifestCmd(), newReleaseSignManifestCmd(), newReleaseVerifyManifestCmd(),
		newReleaseVerifyChannelAdvanceCmd(), newReleaseExportMirrorCmd())
	return cmd
}

type manifestGenOptions struct {
	channel         string
	version         string
	dir             string
	minVersion      string
	advisories      []string
	security        bool
	rollout         int
	startAt         string
	eolAt           string
	expiresIn       string
	noExpiry        bool
	notes           string
	out             string
	signKey         string
	revokeSerials   []string
	revokeHolders   []string
	licenseKeyEpoch string
}

// defaultExpiresIn is the freshness window every generated manifest carries unless
// the operator overrides it. It matches the documented production default the release
// workflow passes (2160h = 90 days; docs/RELEASE-GO-LIVE-RUNBOOK.md §7.2).
//
// It is a DEFAULT rather than an opt-in because the air-gap path
// (scripts/export-update-bundle.sh) forwards --expires-in only when the operator set
// it: an unset flag silently produced a signed manifest with `expires: null`, i.e.
// Manifest.Stale() permanently false and one bundle a mirror can replay forever.
// Anti-freeze must be what you get by forgetting, not what you get by remembering.
const defaultExpiresIn = "2160h"

func newReleaseManifestCmd() *cobra.Command {
	o := &manifestGenOptions{rollout: -1}
	cmd := &cobra.Command{
		Use:   "manifest",
		Short: "Build (and optionally sign) a per-channel OTA update manifest from a release directory",
		Long: "Scans --dir for `olivares_<version>_<os>_<arch>.tar.gz` archives, records each digest\n" +
			"and size, and writes a signed TUF-lite manifest for `olivares upgrade`. With --sign-key\n" +
			"it also writes <out>.sig (Ed25519 over the exact manifest bytes); without it the manifest\n" +
			"is left for the offline signing ceremony.",
		Example: `  olivares release manifest --version 26.8.0 --dir ./dist \
    --channel stable --out ./dist/manifest.json --sign-key @release-private.key`,
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE:         func(cmd *cobra.Command, _ []string) error { return runReleaseManifest(cmd, o) },
	}
	f := cmd.Flags()
	f.StringVar(&o.channel, "channel", release.ChannelStable, "channel: stable | security (lts is accepted by the validator, but no lts line is produced)")
	f.StringVar(&o.version, "version", "", "release version (semver), e.g. 26.8.0 (required)")
	f.StringVar(&o.dir, "dir", ".", "directory holding the release archives")
	f.StringVar(&o.minVersion, "min-version", "", "minimum current version allowed to jump directly to this release")
	f.StringArrayVar(&o.advisories, "advisory", nil, "advisory/CVE id fixed by this release (repeatable)")
	f.BoolVar(&o.security, "security", false, "mark this as a security release")
	f.IntVar(&o.rollout, "rollout", -1, "staged rollout percentage 0..100 (-1 = full rollout / omit)")
	f.StringVar(&o.startAt, "start-at", "", "rollout start time (RFC3339); before it no node upgrades")
	f.StringVar(&o.eolAt, "eol-at", "", "channel/line end-of-life date (RFC3339): recorded and printed, never enforced — a past date only warns, it never refuses (core/release/manifest.go:638-640)")
	f.StringVar(&o.expiresIn, "expires-in", defaultExpiresIn, "freshness window as a duration (e.g. 168h): clients REFUSE the manifest after released_at+this (anti-freeze; re-sign periodically)")
	f.BoolVar(&o.noExpiry, "no-expiry", false, "UNSAFE: emit a manifest with NO freshness bound — a mirror can then serve it forever. Only for a throwaway/test manifest")
	f.StringVar(&o.notes, "notes", "", "short human note or URL")
	f.StringVar(&o.out, "out", "manifest.json", "output manifest path (a .sig is written beside it when --sign-key is set)")
	f.StringVar(&o.signKey, "sign-key", "", "base64 (or @file) Ed25519 PRIVATE key to sign the manifest")
	f.StringArrayVar(&o.revokeSerials, "revoke-serial", nil, "license serial to revoke via this channel's CRL (repeatable)")
	f.StringArrayVar(&o.revokeHolders, "revoke-holder", nil, "holder_id whose EVERY license is revoked via this channel's CRL (repeatable)")
	f.StringVar(&o.licenseKeyEpoch, "license-key-epoch", "", "key-compromise fence (RFC3339, the PAST compromise time): licenses issued before it are invalid; set only during an O03 rotation")
	return cmd
}

func runReleaseManifest(cmd *cobra.Command, o *manifestGenOptions) error {
	out := cmd.OutOrStdout()
	if strings.TrimSpace(o.version) == "" {
		return fmt.Errorf("--version is required")
	}
	if !release.ValidChannel(o.channel) {
		return fmt.Errorf("unknown --channel %q (want %s)", o.channel, strings.Join(release.Channels, " | "))
	}
	if _, err := release.ParseVersion(o.version); err != nil {
		return fmt.Errorf("--version: %w", err)
	}
	// Git tags carry the conventional leading "v" while GoReleaser's .Version and
	// archive names do not. Canonicalise once so a tag `v26.6.0` always produces
	// manifest.version `26.6.0` and scans `olivares_26.6.0_...` (commerce contract).
	version := strings.TrimPrefix(strings.TrimSpace(o.version), "v")

	arts, err := scanArtifacts(o.dir, version)
	if err != nil {
		return err
	}
	if len(arts) == 0 {
		return fmt.Errorf("no `olivares_%s_<os>_<arch>.tar.gz` archives found in %s", version, o.dir)
	}

	m := release.Manifest{
		SchemaVersion: release.ManifestSchemaVersion,
		Channel:       o.channel,
		Version:       version,
		MinVersion:    strings.TrimSpace(o.minVersion),
		ReleasedAt:    time.Now().UTC(),
		Security:      o.security || o.channel == release.ChannelSecurity,
		Advisories:    o.advisories,
		Notes:         o.notes,
		Artifacts:     arts,
	}
	if o.rollout >= 0 {
		if o.rollout > 100 {
			return fmt.Errorf("--rollout must be 0..100")
		}
		pct := o.rollout
		m.Rollout.Percentage = &pct
	}
	if s := strings.TrimSpace(o.startAt); s != "" {
		ts, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return fmt.Errorf("--start-at: %w", err)
		}
		m.Rollout.StartAt = ts
	}
	if s := strings.TrimSpace(o.eolAt); s != "" {
		ts, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return fmt.Errorf("--eol-at: %w", err)
		}
		m.EOLAt = &ts
	}
	if len(o.revokeSerials) > 0 || len(o.revokeHolders) > 0 || strings.TrimSpace(o.licenseKeyEpoch) != "" {
		rs := &release.RevokedSet{Serials: o.revokeSerials, HolderIDs: o.revokeHolders}
		if s := strings.TrimSpace(o.licenseKeyEpoch); s != "" {
			ts, err := time.Parse(time.RFC3339, s)
			if err != nil {
				return fmt.Errorf("--license-key-epoch: %w (want RFC3339, the past compromise time)", err)
			}
			rs.LicenseKeyEpoch = ts.Unix()
		}
		m.Revoked = rs
		fmt.Fprintf(out, "CRL: this manifest revokes %d serial(s), %d holder(s)%s — the ceremony reviews these before signing\n",
			len(rs.Serials), len(rs.HolderIDs), cond(rs.LicenseKeyEpoch > 0, ", and fences licenses issued before "+time.Unix(rs.LicenseKeyEpoch, 0).UTC().Format(time.RFC3339), ""))
	}
	switch s := strings.TrimSpace(o.expiresIn); {
	case o.noExpiry:
		fmt.Fprintln(out, "WARNING: --no-expiry: this manifest carries NO freshness bound; a mirror can serve it forever (anti-freeze DISABLED). Never publish it.")
	case s == "":
		return fmt.Errorf("--expires-in is empty: pass a duration (default %s) or --no-expiry to deliberately emit an unbounded manifest", defaultExpiresIn)
	default:
		d, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("--expires-in: %w", err)
		}
		if d <= 0 {
			return fmt.Errorf("--expires-in must be positive (got %s): a manifest that expires at or before released_at is stale the moment it is published", s)
		}
		exp := m.ReleasedAt.Add(d)
		m.Expires = &exp
	}

	// render-exempt: these bytes are the OTA MANIFEST ARTIFACT — they get
	// signed and published, and `upgrade` verifies that exact serialization.
	// Reformatting them for a human would invalidate the signature.
	mb, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	mb = append(mb, '\n')
	// Self-check: the bytes we emit must parse under the SAME validator the client uses.
	if _, err := release.ParseManifest(mb); err != nil {
		return fmt.Errorf("generated manifest failed self-validation: %w", err)
	}
	if err := os.WriteFile(o.out, mb, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(out, "wrote %s (%d artifact(s): %s)\n", o.out, len(arts), strings.Join(m.Platforms(), ", "))

	if strings.TrimSpace(o.signKey) != "" {
		priv, err := loadEd25519Private(o.signKey)
		if err != nil {
			return err
		}
		sig := release.SignManifest(mb, priv)
		sigPath := o.out + ".sig"
		if err := os.WriteFile(sigPath, []byte(base64.StdEncoding.EncodeToString(sig)+"\n"), 0o644); err != nil {
			return err
		}
		// Prove the signature verifies against the derived public key before we exit.
		if _, err := release.VerifyManifest(mb, sig, priv.Public().(ed25519.PublicKey)); err != nil {
			return fmt.Errorf("self-verify of the signed manifest failed: %w", err)
		}
		fmt.Fprintf(out, "wrote %s (Ed25519; embed pubkey %s to verify)\n", sigPath,
			base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey)))
	} else {
		fmt.Fprintln(out, "unsigned: sign these exact bytes off-box with `olivares release sign-manifest` during the release ceremony")
	}
	return nil
}

// manifestPolicyField is one row of the POLICY block, for the -o json panes of
// `release sign-manifest` and `release verify-manifest`.
//
// It is a CLI-local DTO and not release.PolicyField because that type carries no
// json tags (core/release/manifest.go:684), so marshaling it would publish `Name`
// / `Value` / `Alert` as the wire names in a binary whose every other document is
// snake_case — and core/ is not this lot's to change.
//
// `alert` is the `!!` marker, and it is the reason the block is worth having as
// data at all: the prose puts two characters in front of a field the custodian
// must consciously confirm, and two characters in a column are exactly what a
// pipeline cannot assert on. `jq '[.policy[] | select(.alert)]'` can.
type manifestPolicyField struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Alert bool   `json:"alert"`
}

// manifestPolicyFields converts the engine's summary rows for the JSON panes.
func manifestPolicyFields(fields []release.PolicyField) []manifestPolicyField {
	out := make([]manifestPolicyField, 0, len(fields))
	for _, f := range fields {
		out = append(out, manifestPolicyField{Name: f.Name, Value: f.Value, Alert: f.Alert})
	}
	return out
}

// manifestSignResult is the -o json pane of `release sign-manifest`.
//
// THE POLICY BLOCK IS IN THE DOCUMENT, not dropped from it. That block exists so
// a human reads every field the signature will cover before the key touches the
// bytes, and the tempting shortcut — "-o json means a machine, so skip the human
// block" — would make `-o json` the quiet way to sign without reviewing. It is
// carried as data instead, where it is strictly easier to check than prose: the
// `!!` markers become `alert: true`, and a ceremony wrapper can refuse on them.
//
// What -o json does NOT do is turn a refusal into a document. Every REFUSING path
// above stays an error on stderr with a non-zero code, because handing back a
// well-formed object for a manifest this command declined to sign is how a
// pipeline comes to treat the refusal as a result.
//
// The unsafe path reports itself: with --unsafe-no-crosscheck, `cross_checked` is
// false and `policy`, `warnings`, `checksums` and `artifacts_matched` are empty,
// because in that mode nothing was cross-checked and no policy was reviewed. The
// key set does not change — the shape is the same document with the review half
// empty, so a consumer reads `cross_checked` rather than probing for keys.
type manifestSignResult struct {
	Manifest         string                `json:"manifest"`
	Signature        string                `json:"signature"`
	KeyFingerprint   string                `json:"key_fingerprint"`
	Channel          string                `json:"channel"`
	Version          string                `json:"version"`
	CrossChecked     bool                  `json:"cross_checked"`
	Checksums        string                `json:"checksums"`
	ArtifactsMatched int                   `json:"artifacts_matched"`
	Policy           []manifestPolicyField `json:"policy"`
	Warnings         []string              `json:"warnings"`
}

type manifestSignOptions struct {
	manifest           string
	out                string
	signKey            string
	checksums          string
	unsafeNoCrosscheck bool
}

// newReleaseSignManifestCmd is the off-box half of the two-phase release pipeline.
// CI produces an unsigned, digest-bound manifest; the ceremony signs that exact file without
// regenerating timestamps or touching the OTA private key in CI.
func newReleaseSignManifestCmd() *cobra.Command {
	o := &manifestSignOptions{}
	cmd := &cobra.Command{
		Use:   "sign-manifest",
		Short: "Sign an existing OTA manifest during the off-box release ceremony",
		Long: "Signs the exact bytes of an existing, validated OTA manifest with the dedicated\n" +
			"Ed25519 OTA private key. Run this only on the off-box/HSM ceremony host; CI\n" +
			"receives the resulting public detached signature, never the private key.",
		Example:      "  olivares release sign-manifest --manifest stable-manifest.json --sign-key @prod-ota.key",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runReleaseSignManifest(cmd, o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.manifest, "manifest", "", "existing manifest JSON to sign (required)")
	f.StringVar(&o.out, "out", "", "detached signature output (default <manifest>.sig)")
	f.StringVar(&o.signKey, "sign-key", "", "base64 (or @file) dedicated OTA Ed25519 PRIVATE key")
	f.StringVar(&o.checksums, "checksums", "", "the cosign-verified checksums.txt the manifest must agree with (REQUIRED: signing binds these digests)")
	f.BoolVar(&o.unsafeNoCrosscheck, "unsafe-no-crosscheck", false, "UNSAFE: sign without binding the manifest to checksums.txt or reviewing its policy")
	return cmd
}

func runReleaseSignManifest(cmd *cobra.Command, o *manifestSignOptions) error {
	if strings.TrimSpace(o.manifest) == "" {
		return fmt.Errorf("--manifest is required")
	}
	if strings.TrimSpace(o.signKey) == "" {
		return fmt.Errorf("--sign-key is required")
	}
	mb, err := os.ReadFile(o.manifest)
	if err != nil {
		return fmt.Errorf("read --manifest: %w", err)
	}
	m, err := release.ParseManifest(mb)
	if err != nil {
		return fmt.Errorf("refusing to sign invalid manifest: %w", err)
	}
	// The signature is the whole trust anchor: whatever this command signs, every
	// deployed binary will accept. Parsing proves only that the JSON is well
	// formed. Bind the digests to the cosign-verified checksums.txt and put the
	// policy in front of the human BEFORE the key touches the bytes — the air-gap
	// script already enforces exactly this, and the incident path (a substituted
	// draft asset signed under time pressure) runs through here.
	// The review block is PROGRESS in the progressStream sense: it is printed before
	// a policy check and a digest cross-check that can both still refuse, so it
	// cannot be deferred into the final renderer without vanishing from exactly the
	// runs it exists for. Under -o json it moves to stderr and the same rows travel
	// to the document as `policy`.
	policy := []manifestPolicyField{}
	warnings := []string{}
	if !o.unsafeNoCrosscheck {
		out := progressStream(cmd)
		if strings.TrimSpace(o.checksums) == "" {
			return fmt.Errorf("--checksums is required: signing binds the manifest digests to the release's cosign-verified checksums.txt " +
				"(pass --unsafe-no-crosscheck only when you have verified the manifest by other means and accept signing it blind)")
		}
		now := time.Now().UTC()
		fmt.Fprintln(out, "\n=== POLICY THE SIGNATURE WILL COVER — READ EVERY LINE BEFORE SIGNING ===")
		fmt.Fprintln(out, "    (these fields are NOT bound by checksums.txt; only your review binds them)")
		summary := m.PolicySummary(now)
		for _, fld := range summary {
			marker := "   "
			if fld.Alert {
				marker = "!! "
			}
			fmt.Fprintf(out, "%s%-15s %s\n", marker, fld.Name+":", fld.Value)
		}
		policy = manifestPolicyFields(summary)
		fmt.Fprintln(out, "=======================================================================")
		checked, perr := m.CheckPolicy(now, release.DefaultPolicyBounds())
		for _, w := range checked {
			fmt.Fprintf(out, "WARNING:   %s\n", w)
		}
		if len(checked) > 0 {
			warnings = checked
		}
		if perr != nil {
			return fmt.Errorf("REFUSING to sign: %w\n\nThese policy values are not ones this publisher would issue. A manifest whose digests are "+
				"honest but whose policy is hostile still blocks, delays or suppresses every upgrade in the fleet — treat it as a possible "+
				"substitution of the draft asset and investigate", perr)
		}
		cb, rerr := os.ReadFile(o.checksums)
		if rerr != nil {
			return fmt.Errorf("read --checksums: %w", rerr)
		}
		if cerr := m.CrossCheckChecksums(cb); cerr != nil {
			return fmt.Errorf("REFUSING to sign: %w\n\nThe manifest does not describe the artifacts the release pipeline signed. "+
				"Treat this as a possible substitution of the draft asset and investigate", cerr)
		}
		fmt.Fprintf(out, "digests:   all %d manifest artifact(s) match %s\n", len(m.Artifacts), o.checksums)
	}
	priv, err := loadEd25519Private(o.signKey)
	if err != nil {
		return err
	}
	sig := release.SignManifest(mb, priv)
	pub := priv.Public().(ed25519.PublicKey)
	if _, err := release.VerifyManifest(mb, sig, pub); err != nil {
		return fmt.Errorf("self-verify of the signed manifest failed: %w", err)
	}
	outPath := strings.TrimSpace(o.out)
	if outPath == "" {
		outPath = o.manifest + ".sig"
	}
	if err := os.WriteFile(outPath, []byte(base64.StdEncoding.EncodeToString(sig)+"\n"), 0o644); err != nil {
		return err
	}
	matched := 0
	if !o.unsafeNoCrosscheck {
		matched = len(m.Artifacts)
	}
	return renderOut(cmd, func(w io.Writer) error {
		_, werr := fmt.Fprintf(w, "wrote %s (dedicated OTA key fingerprint %s)\n", outPath, release.Fingerprint(pub))
		return werr
	}, manifestSignResult{
		Manifest:         o.manifest,
		Signature:        outPath,
		KeyFingerprint:   release.Fingerprint(pub),
		Channel:          m.Channel,
		Version:          m.Version,
		CrossChecked:     !o.unsafeNoCrosscheck,
		Checksums:        cond(o.unsafeNoCrosscheck, "", o.checksums),
		ArtifactsMatched: matched,
		Policy:           policy,
		Warnings:         warnings,
	})
}

// manifestVerifyResult is the -o json pane of `release verify-manifest`, and it is
// the document the two-phase OTA pipeline actually needs: the custodian's
// pre-ceremony cross-check is a GATE, and a gate whose verdict has to be grepped
// out of five prose sections is one a protected dispatch cannot enforce.
//
// Same rules as the sign half. The POLICY block travels as data, so the `!!` rows
// a human is asked to confirm become `alert: true` a wrapper can refuse on. Every
// REFUSING path stays an error with a non-zero code and never becomes a document.
// The key set is fixed across both call shapes: without --sig, `signature_checked`
// is false and the two key fields are ""; without --dir, `bytes_verified` is `[]`.
//
// There is NO `ok` field. Every way this command can fail returns an error before
// the render, so a boolean here could only ever be true — a control that cannot
// fail is not a control, and a consumer trusting it would be trusting nothing. The
// exit code is the verdict, and `signature_checked` / `bytes_verified` say HOW
// MUCH was verified, which is the question the `OK:` line's own footnote raises.
type manifestVerifyResult struct {
	Manifest         string `json:"manifest"`
	SignatureChecked bool   `json:"signature_checked"`
	// KeySource is resolveReleaseKey's own label ("--pubkey" / the embedded key),
	// so an operator can tell a run that proved the SHIPPED anchor accepts these
	// bytes from one that merely proved some key does. "" when no --sig was given.
	KeySource      string `json:"key_source"`
	KeyFingerprint string `json:"key_fingerprint"`
	Channel        string `json:"channel"`
	Version        string `json:"version"`
	// MaxFreshnessWindow is the bound the policy check was actually run under,
	// including an operator's --max-expires-in override. A gate that reported the
	// verdict without the bound it used would let a relaxed run read as a strict one.
	MaxFreshnessWindow string                  `json:"max_freshness_window"`
	Policy             []manifestPolicyField   `json:"policy"`
	Warnings           []string                `json:"warnings"`
	Checksums          string                  `json:"checksums"`
	ArtifactsMatched   int                     `json:"artifacts_matched"`
	BytesVerified      []manifestArtifactBytes `json:"bytes_verified"`
}

// manifestArtifactBytes is one published archive that was re-hashed and bound to
// its manifest digest under --dir — the step that makes the dispatch's proof
// non-vacuous, and therefore the one a script most needs to count.
type manifestArtifactBytes struct {
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
}

type manifestVerifyOptions struct {
	manifest           string
	sig                string
	pubkey             string
	checksums          string
	dir                string
	channel            string
	version            string
	allowNoExpiry      bool
	maxExpiresIn       string
	allowPausedRollout bool
}

// newReleaseVerifyManifestCmd is the CROSS-CHECK the two-phase OTA pipeline needs
// to stop being a blind signature. CI signs checksums.txt with cosign (keyless
// OIDC); the custodian signs the manifest off-box with the Ed25519 OTA key. Those
// are two independent signatures over two documents that nothing forced to agree —
// so anyone able to overwrite the draft asset between the build and the ceremony
// could have the custodian sign digests of their choosing.
//
// This command forces the agreement, offline, in both directions the release flow
// needs it:
//
//	BEFORE signing (custodian, no --sig): does the manifest CI produced actually
//	describe the artifacts cosign attested? Refuse to sign if not.
//	AFTER signing (protected dispatch, with --sig and the SHIPPED community binary):
//	does the signature verify under the binary's own embedded OTA anchor, AND do the
//	PUBLISHED bytes hash to the digests the manifest commits to?
//
// The caller authenticates checksums.txt itself (cosign verify-blob) — this binary
// deliberately stays offline and tool-free, so it takes the already-verified file.
func newReleaseVerifyManifestCmd() *cobra.Command {
	o := &manifestVerifyOptions{}
	cmd := &cobra.Command{
		Use:   "verify-manifest",
		Short: "Cross-check an OTA manifest against the cosign-verified checksums.txt (and, with --dir, the published bytes)",
		Long: "Binds an OTA update manifest to the release it claims to describe. Every digest in\n" +
			"the manifest must match the same filename's entry in --checksums (the checksums.txt\n" +
			"the caller has ALREADY verified with `cosign verify-blob`), and with --dir every\n" +
			"published archive is re-hashed and bound to that digest.\n\n" +
			"With --sig the manifest signature is verified first, against this build's embedded\n" +
			"OTA anchor unless --pubkey overrides it — so running this from the SHIPPED community\n" +
			"binary proves the real client-side anchor accepts the real published bytes.\n\n" +
			"Digests are only half of what the signature covers. The manifest's POLICY (expires,\n" +
			"min_version, rollout, security/advisories, notes) is bound by NOTHING in checksums.txt,\n" +
			"so this command also applies fail-closed plausibility bounds to it AND prints every\n" +
			"policy field for the custodian to read before signing.\n\n" +
			"Run it BEFORE the off-box ceremony (no --sig) and REFUSE to sign on any failure.",
		Example: "  # custodian, before signing (checksums.txt already cosign-verified)\n" +
			"  olivares release verify-manifest --manifest stable-manifest.json \\\n" +
			"    --checksums checksums.txt --dir . --expect-channel stable --expect-version 26.8.0",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE:         func(cmd *cobra.Command, _ []string) error { return runReleaseVerifyManifest(cmd, o) },
	}
	f := cmd.Flags()
	f.StringVar(&o.manifest, "manifest", "", "manifest JSON to cross-check (required)")
	f.StringVar(&o.sig, "sig", "", "detached manifest signature; when set the signature is verified BEFORE the cross-check")
	f.StringVar(&o.pubkey, "pubkey", "", "base64 or @file Ed25519 OTA key for --sig (default: the key embedded in this build)")
	f.StringVar(&o.checksums, "checksums", "", "the release's checksums.txt, ALREADY verified with cosign (required)")
	f.StringVar(&o.dir, "dir", "", "directory holding the published archives; every manifest artifact must be present and re-hash to its digest")
	f.StringVar(&o.channel, "expect-channel", "", "fail unless the manifest declares this channel")
	f.StringVar(&o.version, "expect-version", "", "fail unless the manifest declares this version (a leading v is ignored)")
	// A freshness bound is now REQUIRED by default. It used to be opt-in, so a
	// custodian who simply forgot the flag got `expires: none (anti-freeze DISABLED)`
	// followed by a reassuring `OK:` — the exact shape of a check that fails open.
	f.Bool("require-expiry", true, "DEPRECATED (now the default): a freshness bound is required unless --allow-no-expiry")
	_ = f.MarkDeprecated("require-expiry", "a freshness bound is required by default; use --allow-no-expiry to opt OUT")
	f.BoolVar(&o.allowNoExpiry, "allow-no-expiry", false, "UNSAFE: accept a manifest with no freshness bound (anti-freeze disabled)")
	f.StringVar(&o.maxExpiresIn, "max-expires-in", release.DefaultMaxFreshnessWindow.String(),
		"upper bound on the freshness window (expires-released_at and expires-now): beyond it the anti-freeze defense is effectively off")
	f.BoolVar(&o.allowPausedRollout, "allow-paused-rollout", false,
		"accept a SECURITY manifest whose rollout is paused (percentage 0 or a future start_at) — only when the pause is deliberate")
	return cmd
}

func runReleaseVerifyManifest(cmd *cobra.Command, o *manifestVerifyOptions) error {
	// Every line below the final verdict is PROGRESS: the signature note, the policy
	// block, the warnings and the per-artifact re-hash are printed as each stage
	// passes, ahead of stages that can still REFUSE. Deferring them into the final
	// renderer would delete them from precisely the runs a custodian must read, so
	// under -o json they move to stderr and travel to the document as fields.
	out := progressStream(cmd)
	res := manifestVerifyResult{
		Manifest:      strings.TrimSpace(o.manifest),
		Policy:        []manifestPolicyField{},
		Warnings:      []string{},
		BytesVerified: []manifestArtifactBytes{},
	}
	if strings.TrimSpace(o.manifest) == "" {
		return fmt.Errorf("--manifest is required")
	}
	if strings.TrimSpace(o.checksums) == "" {
		return fmt.Errorf("--checksums is required: without the cosign-verified checksums.txt there is nothing to cross-check the manifest digests against")
	}
	mb, err := os.ReadFile(o.manifest)
	if err != nil {
		return fmt.Errorf("read --manifest: %w", err)
	}

	// 1) Signature (optional — the custodian runs this on an unsigned manifest).
	var m release.Manifest
	if sigPath := strings.TrimSpace(o.sig); sigPath != "" {
		sig, rerr := os.ReadFile(sigPath)
		if rerr != nil {
			return fmt.Errorf("read --sig: %w", rerr)
		}
		pub, keySrc, kerr := resolveReleaseKey(o.pubkey)
		if kerr != nil {
			return kerr
		}
		if m, err = release.VerifyManifest(mb, sig, pub); err != nil {
			return fmt.Errorf("REFUSING: the manifest signature does not verify: %w", err)
		}
		res.SignatureChecked = true
		res.KeySource = keySrc
		res.KeyFingerprint = release.Fingerprint(pub)
		fmt.Fprintf(out, "signature: OK (OTA key %s, fingerprint %s)\n", keySrc, release.Fingerprint(pub))
	} else {
		if m, err = release.ParseManifest(mb); err != nil {
			return err
		}
		fmt.Fprintln(out, "signature: not checked (no --sig — pre-ceremony cross-check of an unsigned manifest)")
	}

	// 2) Identity: this must be the manifest for the release we think we are signing.
	if want := strings.TrimSpace(o.channel); want != "" && m.Channel != want {
		return fmt.Errorf("REFUSING: manifest channel is %q, expected %q", m.Channel, want)
	}
	if want := strings.TrimPrefix(strings.TrimSpace(o.version), "v"); want != "" && m.Version != want {
		return fmt.Errorf("REFUSING: manifest version is %q, expected %q", m.Version, want)
	}

	// 3) POLICY. The digest cross-check below binds the manifest to the artifacts CI
	//    signed; it binds NONE of the fields that decide who may upgrade, who
	//    receives it, and for how long this manifest stays alive. An attacker who
	//    leaves every digest honest still gets a fleet-wide kill switch out of
	//    min_version/rollout/expires — with a signature the custodian applied.
	//    Two layers: machine-checkable plausibility bounds (fail-closed), then the
	//    full field dump, because a field nobody prints is a field nobody reviews.
	now := time.Now().UTC()
	// `--require-expiry=false` used to mean "accept a manifest with no freshness
	// bound". Silently ignoring it now would turn a request to RELAX the check into a
	// no-op the caller never learns about — say so instead of guessing.
	if fl := cmd.Flags().Lookup("require-expiry"); fl != nil && fl.Changed && fl.Value.String() == "false" {
		return fmt.Errorf("--require-expiry=false is no longer accepted: a freshness bound is now required by default; " +
			"pass --allow-no-expiry if you really intend to accept a manifest a hostile mirror can serve forever")
	}
	bounds := release.DefaultPolicyBounds()
	bounds.AllowNoExpiry = o.allowNoExpiry
	bounds.AllowPausedRollout = o.allowPausedRollout
	if s := strings.TrimSpace(o.maxExpiresIn); s != "" {
		d, perr := time.ParseDuration(s)
		if perr != nil {
			return fmt.Errorf("--max-expires-in: %w", perr)
		}
		if d <= 0 {
			return fmt.Errorf("--max-expires-in must be positive (got %s)", s)
		}
		bounds.MaxFreshnessWindow = d
	}

	res.Channel = m.Channel
	res.Version = m.Version
	res.MaxFreshnessWindow = bounds.MaxFreshnessWindow.String()

	fmt.Fprintln(out, "\n=== POLICY THE SIGNATURE WILL COVER — READ EVERY LINE BEFORE SIGNING ===")
	fmt.Fprintln(out, "    (these fields are NOT bound by checksums.txt; only your review binds them)")
	summary := m.PolicySummary(now)
	for _, fld := range summary {
		marker := "   "
		if fld.Alert {
			marker = "!! "
		}
		fmt.Fprintf(out, "%s%-15s %s\n", marker, fld.Name+":", fld.Value)
	}
	res.Policy = manifestPolicyFields(summary)
	fmt.Fprintln(out, "=======================================================================")

	warnings, perr := m.CheckPolicy(now, bounds)
	for _, w := range warnings {
		fmt.Fprintf(out, "WARNING:   %s\n", w)
	}
	if len(warnings) > 0 {
		res.Warnings = warnings
	}
	if perr != nil {
		return fmt.Errorf("REFUSING: %w\n\nThese policy values are not ones this publisher would issue. Do NOT sign and do NOT publish: "+
			"a manifest whose digests are honest but whose policy is hostile still blocks, delays or suppresses every upgrade in the fleet — "+
			"treat it as a possible substitution of the draft asset and investigate", perr)
	}
	fmt.Fprintln(out, "policy:    within plausibility bounds (max freshness window "+bounds.MaxFreshnessWindow.String()+")")

	// 4) THE cross-check: manifest digests vs the cosign-verified checksums.txt.
	cb, err := os.ReadFile(o.checksums)
	if err != nil {
		return fmt.Errorf("read --checksums: %w", err)
	}
	if err := m.CrossCheckChecksums(cb); err != nil {
		return fmt.Errorf("REFUSING: %w\n\nThe manifest does not describe the artifacts the release pipeline signed. "+
			"Do NOT sign it and do NOT publish it: treat this as a possible substitution of the draft asset and investigate", err)
	}
	fmt.Fprintf(out, "digests:   all %d manifest artifact(s) match %s\n", len(m.Artifacts), o.checksums)
	res.Checksums = strings.TrimSpace(o.checksums)
	res.ArtifactsMatched = len(m.Artifacts)

	// 5) With --dir, bind the digests to the PUBLISHED BYTES, not just to another
	//    document. This is what makes the dispatch's proof non-vacuous.
	if dir := strings.TrimSpace(o.dir); dir != "" {
		for _, a := range m.Artifacts {
			data, rerr := os.ReadFile(filepath.Join(dir, a.Filename))
			if rerr != nil {
				return fmt.Errorf("REFUSING: cannot read the published artifact %s from --dir: %w", a.Filename, rerr)
			}
			if verr := release.VerifyArtifactSHA256(data, a.SHA256); verr != nil {
				return fmt.Errorf("REFUSING: published artifact %s does not match its manifest digest: %w", a.Filename, verr)
			}
			fmt.Fprintf(out, "bytes:     %s (%d B) re-hashed and bound\n", a.Filename, len(data))
			res.BytesVerified = append(res.BytesVerified, manifestArtifactBytes{Filename: a.Filename, Size: int64(len(data))})
		}
	}

	return renderOut(cmd, func(w io.Writer) error {
		fmt.Fprintf(w, "OK: %s manifest for %s is bound to the signed checksums%s, and its policy is within bounds.\n",
			m.Channel, m.Version, cond(strings.TrimSpace(o.dir) != "", " and to the published bytes", ""))
		fmt.Fprintln(w, "    STILL YOURS TO CONFIRM (no machine can): that the POLICY block above is the policy you intended —")
		_, werr := fmt.Fprintln(w, "    min_version, rollout, expires, security/advisories and notes. `OK:` means plausible, not intended.")
		return werr
	}, res)
}

// scanArtifacts finds the base release archives for version and records their
// digests. FIPS/other variants (any name with `_fips_`) are skipped — a FIPS
// deployment upgrades via its own image/package, never an auto-OTA to a non-FIPS binary.
func scanArtifacts(dir, version string) ([]release.Artifact, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	prefix := "olivares_" + version + "_"
	var arts []release.Artifact
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".tar.gz") || strings.Contains(name, "_fips_") {
			continue
		}
		mid := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".tar.gz") // "<os>_<arch>"
		goos, goarch, ok := strings.Cut(mid, "_")
		if !ok || !knownGOOS[goos] || goarch == "" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(b)
		arts = append(arts, release.Artifact{
			OS: goos, Arch: goarch, Filename: name,
			SHA256: hex.EncodeToString(sum[:]), Size: int64(len(b)),
		})
	}
	sort.Slice(arts, func(i, j int) bool {
		if arts[i].OS != arts[j].OS {
			return arts[i].OS < arts[j].OS
		}
		return arts[i].Arch < arts[j].Arch
	})
	return arts, nil
}

// loadEd25519Private decodes a base64 Ed25519 private key (64-byte key or 32-byte
// seed), from a literal or an @file.
func loadEd25519Private(flag string) (ed25519.PrivateKey, error) {
	raw := strings.TrimSpace(flag)
	if strings.HasPrefix(raw, "@") {
		b, err := os.ReadFile(raw[1:])
		if err != nil {
			return nil, fmt.Errorf("read --sign-key file: %w", err)
		}
		raw = strings.TrimSpace(string(b))
	}
	dec, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		if dec, err = base64.RawURLEncoding.DecodeString(raw); err != nil {
			return nil, fmt.Errorf("--sign-key is not valid base64")
		}
	}
	switch len(dec) {
	case ed25519.PrivateKeySize:
		return ed25519.PrivateKey(dec), nil
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(dec), nil
	default:
		return nil, fmt.Errorf("--sign-key is %d bytes, want %d (key) or %d (seed)", len(dec), ed25519.PrivateKeySize, ed25519.SeedSize)
	}
}
