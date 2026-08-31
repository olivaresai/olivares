// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/core/license"
	"github.com/olivaresai/olivares/core/release"
	"github.com/olivaresai/olivares/core/secure"
)

// newLicenseCmd manages commercial licenses offline (Ed25519). License
// verification is informational only — it gates no feature (LICENSING.md).
func newLicenseCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "license",
		Short: "Manage commercial licenses (install/uninstall/status + keygen/sign/verify; offline Ed25519, never a feature gate)",
		Long: "license covers both sides of the commercial license: the operator side (install\n" +
			"one, replace it, remove it, report what is installed) and the issuer side (mint a\n" +
			"keypair, sign a license, verify one against a public key).\n\n" +
			"Verification is offline Ed25519 — no call-home, and no network is needed to\n" +
			"establish entitlement. A license is an ATTESTATION of entitlement, never a\n" +
			"feature gate: an expired or absent license does not switch functionality off.",
		Example: "  olivares license status --data-dir /var/lib/olivares\n" +
			"  olivares license install ./customer.license --data-dir /var/lib/olivares\n" +
			"  olivares license uninstall --data-dir /var/lib/olivares --yes\n" +
			"  olivares license verify \"$LICENSE_BLOB\" --pubkey \"$LICENSE_PUBLIC_KEY\"",
	}
	root.AddCommand(licenseKeygenCmd(), licenseSignCmd(), licenseVerifyCmd(), licenseInstallCmd(), licenseUninstallCmd(), licenseStatusCmd())
	return root
}

// licenseInstallCmd persists a license into the data dir (the customer-side install). It is the offline half of the in-place edition system: it verifies the blob
// against this build's embedded key and writes <data-dir>/license.key (0600) — the
// canonical at-rest license the engine reads by default. A running engine applies it
// WITHOUT a restart (SIGHUP / POST /v1/console/runtime/reload / the console); it also
// loads on the next start. It gates nothing (LICENSING.md).
func licenseInstallCmd() *cobra.Command {
	var dataDir, pubB64 string
	var force bool
	cmd := &cobra.Command{
		Use:   "install <file|->",
		Short: "Install a license into the data dir (verify + persist; apply live with SIGHUP / runtime reload)",
		Long: "install verifies a signed license against this build's embedded key and persists it to\n" +
			"<data-dir>/" + licenseFileName + " (mode 0600) — the canonical at-rest license the engine reads by\n" +
			"default. Pass a file path, or - to read the blob from stdin. It gates NO feature (docs/07 §9): in\n" +
			"the community build it stores the attestation ready for an in-place swap to the enterprise binary;\n" +
			"in the enterprise build it entitles the commercial add-ons. It never caps user accounts: self-\n" +
			"hosted users are unlimited in every tier. Apply to a RUNNING engine with no\n" +
			"restart via `kill -HUP <pid>`, POST /v1/console/runtime/reload, or the console.\n\n" +
			"Installing over an existing license REPLACES it, atomically, and says which one it replaced.\n" +
			"It is REFUSED while a --license/OLIVARES_LICENSE* override outranks the data-dir file (--force\n" +
			"stages it anyway), and `license uninstall` removes it again.",
		Example:      "  olivares license install ./customer.license --data-dir /var/lib/olivares",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			raw, err := readLicenseArg(cmd, args[0])
			if err != nil {
				return fmt.Errorf("read license: %w", err)
			}
			blob := strings.TrimSpace(string(raw))
			if blob == "" {
				return fmt.Errorf("the license is empty")
			}
			pub := license.DefaultPublicKey()
			if pubB64 != "" {
				p, perr := license.DecodePublicKey(pubB64)
				if perr != nil {
					return perr
				}
				pub = p
			} else if len(pub) == 0 {
				return fmt.Errorf("this build embeds no verification key (license-key=%s); pass --pubkey <base64-public-key> to verify before installing", license.KeyOrigin())
			}
			// Verify BEFORE persisting so a paste/format error is caught here, not at boot.
			// A valid-but-expired blob installs fine (it just lifts nothing until renewed).
			lic, verr := license.VerifyEnvelope(blob, pub)
			if verr != nil {
				return fmt.Errorf("refusing to install: the license does not verify against this build's key (%s): %w", license.KeyOrigin(), verr)
			}
			dir := dataDir
			if dir == "" {
				resolved, derr := defaultDataDir()
				if derr != nil {
					return derr
				}
				dir = resolved
			}
			// REFUSE under a boot override unless forced. This used to persist
			// the file and print a WARNING, which is the worst of the three options:
			// the operator installs a license, sees exit 0, and the engine goes on
			// reading a different one. The API half already refuses
			// (licenseService.InstallLicense, api.ErrLicenseManagedExternally) and the
			// upgrade runbook already documents the CLI as refusing too, so this makes
			// one surface stop disagreeing with the other two rather than inventing a
			// rule. --force keeps the legitimate case: staging the data-dir file now
			// for an override that is going away later.
			if kind, detail, present := licenseOverridePresent("", osGetenv); present && !force {
				return fmt.Errorf("refusing to install: a %s license override (%s) is active and OUTRANKS %s at boot, "+
					"so this install would change nothing the engine reads — edit that source instead, or pass --force "+
					"to stage the data-dir file anyway", kind, detail, licenseDataDirPath(dir))
			}
			// EnsureDataDir: this directory ends up holding the license next to the
			// signing keys, so it carries its own VCS exclusion.
			if err := secure.EnsureDataDir(dir); err != nil {
				return err
			}
			path := licenseDataDirPath(dir)
			// What is being REPLACED, read before it is gone. An install over an
			// existing license is a transition the operator cannot otherwise see, and
			// "installed" printed over a license that was already there reads as a
			// first install.
			replaced := describeInstalledLicense(path, pub)
			// writeKeyFile, not os.WriteFile: this command's own help promises mode
			// 0600, and os.WriteFile applies its perm ONLY WHEN IT CREATES THE FILE —
			// measured 2026-08-09 against the built binary, a target that already
			// existed 0644 still read 0644 after this write, with the license in it.
			// It is the trap this file diagnoses for keygen 200 lines below and never
			// applied here. The force path also makes the replacement ATOMIC (temp
			// file beside the target, fsync, rename), so a crash mid-install can no
			// longer leave a truncated license where a valid one used to be — the
			// failure mode that matters precisely because a renewal destroys the
			// previous license to make room for the new one.
			if err := writeKeyFile(path, []byte(blob+"\n"), 0o600, true, "the installed license"); err != nil {
				return fmt.Errorf("write license: %w", err)
			}
			now := time.Now().UTC()
			status := lic.Status(now)
			// E2 (sol-max contrast): `license install` reported through a
			// hand-built JSON document, so -o did not reach it either.
			reportKey, reportBody := licenseReport(lic, now)
			report := map[string]any{
				"installed": path,
				"status":    status,
				reportKey:   reportBody,
			}
			if replaced != "" {
				report["replaced"] = replaced
			}
			if rerr := renderReportOut(cmd, report); rerr != nil {
				return rerr
			}
			// Honest stderr guidance (keeps a piped JSON clean).
			if replaced != "" {
				fmt.Fprintf(cmd.ErrOrStderr(), "this REPLACED an installed license: %s\n", replaced)
			}
			if kind, detail, present := licenseOverridePresent("", osGetenv); present {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: --force was used and a %s license override (%s) is set: it OUTRANKS this file at boot, so nothing the engine reads has changed yet\n", kind, detail)
			}
			if status == license.StatusExpired {
				fmt.Fprintln(cmd.ErrOrStderr(), "note: this license is EXPIRED — it installs but lifts nothing until renewed.")
			}
			fmt.Fprintln(cmd.ErrOrStderr(), "Apply to a running engine WITHOUT a restart: kill -HUP <pid>, or POST /v1/console/runtime/reload, or the console. It also loads on next start.")
			return nil
		},
	}
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "data directory to install into (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares)")
	cmd.Flags().StringVar(&pubB64, "pubkey", "", "base64 Ed25519 public key to verify against (default: embedded key)")
	cmd.Flags().BoolVar(&force, "force", false,
		"install even though a --license/OLIVARES_LICENSE* override OUTRANKS the data-dir file. Without it the install is REFUSED, "+
			"because it would change nothing the engine reads; with it the file is staged and the warning says so")
	return cmd
}

// describeInstalledLicense names the license currently at path, for the
// transition line an install prints. It never fails the install: a file that
// cannot be read or verified is described as exactly that, because "there was
// something here and I could not read it" is information the operator needs
// BEFORE it is overwritten, and refusing over it would block a renewal on a
// corrupt predecessor — the one moment a renewal matters most.
func describeInstalledLicense(path string, pub []byte) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "" // nothing installed: this is a first install, not a replacement
	}
	blob := strings.TrimSpace(string(raw))
	if blob == "" {
		return "an empty file"
	}
	if len(pub) == 0 {
		return "a license this build has no key to identify"
	}
	lic, verr := license.VerifyEnvelope(blob, pub)
	if verr != nil {
		return "a license that does not verify against this build's key"
	}
	// A v3 credential describes itself by its purchased LINES, not by a profile label, and the
	// operator about to overwrite it deserves the count: "(3 lines, base paid through …)" says
	// what is being replaced, where "(plan online, expiring …)" would name one line's date as if
	// it were the whole purchase.
	if lic.IsCredentialV3() {
		// "runs through", NOT "paid through": Term() is the base line's EFFECTIVE boundary, and in
		// a provisional phase that is the money-back lease, which can end in 72h with the signed
		// paid_through a month out. Printing a lease under a paid-through label states a fact the
		// credential does not carry. (2026-08-11 Codex contrast, F-7.)
		if term := lic.Term(); !term.IsZero() {
			return fmt.Sprintf("%s (%d purchased line(s), base runs through %s)",
				lic.Licensee(), len(lic.Grants()), term.UTC().Format(time.RFC3339))
		}
		return fmt.Sprintf("%s (%d purchased line(s))", lic.Licensee(), len(lic.Grants()))
	}
	if lic.Term().IsZero() {
		// NOT "no expiry". A blob with no attested term is not perpetual — the v8 package
		// removed that right entirely and Claims.Status reports it as EXPIRED (license.go) —
		// so printing "no expiry" told an operator they held the one thing that no longer
		// exists, about a license the engine treats as lapsed.
		return fmt.Sprintf("%s (plan %s, no attested term — treated as expired)", lic.Licensee(), lic.Profile())
	}
	return fmt.Sprintf("%s (plan %s, expiring %s)", lic.Licensee(), lic.Profile(), lic.Term().UTC().Format(time.RFC3339))
}

// licenseUninstallCmd removes the installed license.
//
// The verb was missing from the CLI ONLY. The console has had it since —
// licenseService.UninstallLicense, served at DELETE /v1/console/license — and the
// upgrade runbook lists license "removal" as a supported hot-applied change
// without naming a command for it. So an operator who wanted to remove a license
// offline had one option: delete a file by hand, with nothing to tell them
// whether an override was still supplying one anyway. This closes the parity gap;
// it does not invent a policy, and it keeps the API's two refusals (a boot
// override is edited at its source, and removal is destructive so it asks).
func licenseUninstallCmd() *cobra.Command {
	var dataDir string
	var yes bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the installed license from the data dir (the offline half of DELETE /v1/console/license)",
		Long: "uninstall deletes <data-dir>/" + licenseFileName + ", the canonical at-rest license, and reports what the\n" +
			"engine would resolve afterwards. The deployment reverts to the community edition on the next\n" +
			"reload or start — it costs NO user accounts (self-hosted users are unlimited in every tier) and\n" +
			"touches no data.\n\n" +
			"It REFUSES while a --license/OLIVARES_LICENSE* override is active, exactly as the console does:\n" +
			"removing the file would leave that source still supplying a license, so the command would look\n" +
			"like it worked and change nothing.\n\n" +
			"Removing a license that is not there is not an error — it reports that there was none.",
		Example:      "  olivares license uninstall --data-dir /var/lib/olivares --yes",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir := dataDir
			if dir == "" {
				// defaultDataDir now RETURNS AN ERROR instead of guessing: a relative
				// literal made the first command seed private keys wherever it was run.
				resolved, derr := defaultDataDir()
				if derr != nil {
					return derr
				}
				dir = resolved
			}
			path := licenseDataDirPath(dir)
			if kind, detail, present := licenseOverridePresent("", osGetenv); present {
				return fmt.Errorf("refusing to uninstall: a %s license override (%s) is active and OUTRANKS %s, "+
					"so removing this file would leave that source still supplying a license — remove it there instead",
					kind, detail, path)
			}
			describing := describeInstalledLicense(path, license.DefaultPublicKey())
			if _, serr := os.Stat(path); errors.Is(serr, fs.ErrNotExist) {
				return renderReportOut(cmd, map[string]any{"removed": false, "path": path, "note": "no license is installed here"})
			}
			// The absence of a license is quiet by nature, so make the decision loud —
			// the same rule `sources rm` follows.
			what := "remove the installed license"
			if describing != "" {
				what = "remove the installed license for " + describing
			}
			if err := confirmDestructive(cmd, yes, what+" (the deployment reverts to the community edition on the next reload)"); err != nil {
				return err
			}
			if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("remove license %q: %w", path, err)
			}
			report := map[string]any{"removed": true, "path": path}
			if describing != "" {
				report["was"] = describing
			}
			// What the engine resolves NOW. Reporting "removed" without it would be the
			// same half-answer the override refusal above exists to prevent.
			if src, rerr := resolveLicense("", dir, osGetenv); rerr == nil {
				report["now_resolves"] = src.Kind
			}
			if rerr := renderReportOut(cmd, report); rerr != nil {
				return rerr
			}
			fmt.Fprintln(cmd.ErrOrStderr(), "Apply to a running engine WITHOUT a restart: kill -HUP <pid>, or POST /v1/console/runtime/reload, or the console.")
			return nil
		},
	}
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "data directory holding the license (default $OLIVARES_DATA_DIR or ./olivares-data)")
	addYesFlag(cmd, &yes)
	return cmd
}

// licenseStatusCmd shows the at-rest license and its status OFFLINE, resolving it by
// the same precedence the engine uses. The LIVE status (with active-user usage) is GET
// /v1/console/license or the console — this is the no-database, on-disk view.
func licenseStatusCmd() *cobra.Command {
	var dataDir, licPath, pubB64, manifestPath, manifestSig, otaPub string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the installed license and its status (offline; resolves --license > env > data-dir)",
		Long: "status resolves the at-rest license by the same precedence the engine uses (explicit --license >\n" +
			"OLIVARES_LICENSE_PATH > OLIVARES_LICENSE > <data-dir>/" + licenseFileName + "), verifies it offline\n" +
			"against this build's key and prints its source, profile, grace and status as JSON. With\n" +
			"--manifest it also evaluates the license CRL from an OTA-signed channel manifest;\n" +
			"without one the CRL is honestly reported as unavailable. It reads no database — the LIVE\n" +
			"status (with live active-user usage) is GET /v1/console/license or the console.",
		Example:      "  olivares license status --data-dir /var/lib/olivares --manifest ./stable-manifest.json",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir := dataDir
			if dir == "" {
				resolved, derr := defaultDataDir()
				if derr != nil {
					return derr
				}
				dir = resolved
			}
			src, err := resolveLicense(licPath, dir, osGetenv)
			if err != nil {
				return err
			}
			pub := license.DefaultPublicKey()
			if pubB64 != "" {
				p, perr := license.DecodePublicKey(pubB64)
				if perr != nil {
					return perr
				}
				pub = p
			}
			result := map[string]any{"source": src.Kind}
			if src.Path != "" {
				result["source_path"] = src.Path
			}
			switch {
			case src.Blob == "":
				result["status"] = "none"
			case len(pub) == 0:
				result["status"] = "unknown"
				result["note"] = fmt.Sprintf("this build embeds no verification key (license-key=%s); pass --pubkey to verify", license.KeyOrigin())
			default:
				lic, verr := license.VerifyEnvelope(src.Blob, pub)
				if verr != nil {
					result["status"] = "invalid"
					// WHY, not just THAT. "invalid" with no reason cannot tell a paste error
					// from a wrong key from a container this binary is too old to read, and
					// the operator's next step is different in all three.
					result["reason"] = verr.Error()
				} else {
					rev, crlDesc, crlOK, lerr := loadRevocation(manifestPath, manifestSig, otaPub)
					if lerr != nil {
						return lerr
					}
					now := time.Now().UTC()
					for k, v := range licenseLifecycle(lic, rev, crlDesc, crlOK, now) {
						result[k] = v
					}
					reportKey, reportBody := licenseReport(lic, now)
					result[reportKey] = reportBody
				}
			}
			// E2: honor -o instead of always printing JSON.
			return renderReportOut(cmd, result)
		},
	}
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "data directory holding the license (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares)")
	cmd.Flags().StringVar(&licPath, "license", "", "explicit license file path (highest precedence, like serve --license)")
	cmd.Flags().StringVar(&pubB64, "pubkey", "", "base64 Ed25519 public key to verify against (default: embedded key)")
	cmd.Flags().StringVar(&manifestPath, "manifest", "", "OTA channel manifest to read the license CRL from (its signature must verify)")
	cmd.Flags().StringVar(&manifestSig, "manifest-sig", "", "detached manifest signature (default <manifest>.sig)")
	cmd.Flags().StringVar(&otaPub, "ota-pubkey", "", "base64 or @file Ed25519 OTA key for the manifest (default: the key embedded in this build)")
	return cmd
}

// readLicenseArg reads a license blob from a file path, or from stdin when arg is "-".
func readLicenseArg(cmd *cobra.Command, arg string) ([]byte, error) {
	if arg == "-" {
		return io.ReadAll(cmd.InOrStdin())
	}
	return os.ReadFile(arg)
}

// crlUnavailableNote is the honest CRL limit (LICENSING.md): the CRL rides the
// signed channel manifest, so it only reaches deployments that pull updates or
// import offline bundles — there is no phone-home, and status stays display-only.
const crlUnavailableNote = "unavailable — no channel manifest supplied; the CRL rides the OTA manifest and only reaches deployments that pull updates or import offline bundles (docs/07)"

// loadRevocation loads the license CRL from a channel manifest for the license
// commands. Fail-closed on purpose: a manifest is only accepted as a CRL source
// with a VERIFYING OTA signature (embedded anchor or --ota-pubkey) — an unsigned
// or tampered manifest must not influence even a display-only status. Returns
// ok=false with no error when no manifest was supplied (the honest
// "CRL unavailable" path).
func loadRevocation(manifestPath, sigPath, otaPubFlag string) (rev license.Revocation, desc string, ok bool, err error) {
	manifestPath = strings.TrimSpace(manifestPath)
	if manifestPath == "" {
		return license.Revocation{}, "", false, nil
	}
	mb, err := os.ReadFile(manifestPath)
	if err != nil {
		return license.Revocation{}, "", false, fmt.Errorf("read --manifest: %w", err)
	}
	if strings.TrimSpace(sigPath) == "" {
		sigPath = manifestPath + ".sig"
	}
	rawSig, err := os.ReadFile(sigPath)
	if err != nil {
		return license.Revocation{}, "", false, fmt.Errorf("read manifest signature (%s): %w — the CRL is only trusted from an OTA-signed manifest", sigPath, err)
	}
	sig, err := decodeDetachedSig(rawSig)
	if err != nil {
		return license.Revocation{}, "", false, err
	}
	pub, keySrc, err := resolveReleaseKey(otaPubFlag)
	if err != nil {
		return license.Revocation{}, "", false, err
	}
	// resolveReleaseKey is shared with verify-manifest, where the flag IS --pubkey;
	// here the operator passed --ota-pubkey, so relabel to avoid misleading forensics.
	if keySrc == "--pubkey" {
		keySrc = "--ota-pubkey"
	}
	m, err := release.VerifyManifest(mb, sig, pub)
	if err != nil {
		return license.Revocation{}, "", false, fmt.Errorf("refusing the CRL source: %w", err)
	}
	desc = fmt.Sprintf("%s (channel %s, version %s, OTA key %s)", manifestPath, m.Channel, m.Version, keySrc)
	if m.Revoked == nil {
		return license.Revocation{}, desc, true, nil
	}
	return license.Revocation{
		Serials:         m.Revoked.Serials,
		HolderIDs:       m.Revoked.HolderIDs,
		LicenseKeyEpoch: m.Revoked.LicenseKeyEpoch,
	}, desc, true, nil
}

// licenseLifecycle renders the shared status/CRL block the verify and status
// commands print for a set of verified claims.
func licenseLifecycle(v license.Verified, rev license.Revocation, crlDesc string, crlOK bool, now time.Time) map[string]any {
	out := map[string]any{}
	// A v3 credential carries no issuance profile — its envelope describes the deployment
	// binding and the clock policy instead — so the key is omitted rather than defaulted to
	// "online", which would print a fact nobody signed.
	if p := v.Profile(); p != "" {
		out["profile"] = p
	}
	// The attested grace window, defined identically in both containers as "how far past the
	// paid term the right still runs": ExpiresAt→ExpiresAt+GracePeriod for a flat license, and
	// paid_through→the base line's effective boundary for a credential (non-zero only in the
	// renewal_grace phase). Never inferred in either.
	grace := time.Duration(0)
	if t, r := v.Term(), v.RightEnds(); !t.IsZero() && r.After(t) {
		grace = r.Sub(t)
	}
	out["grace_period"] = grace.String()
	var status license.Status
	if crlOK {
		status = v.StatusWithRevocation(now, rev)
		out["crl"] = map[string]any{"source": crlDesc, "revoked": status == license.StatusRevoked}
	} else {
		status = v.Status(now)
		out["crl"] = crlUnavailableNote
	}
	out["status"] = string(status)
	if status == license.StatusGrace {
		out["grace_remaining"] = v.RightEnds().Sub(now).Round(time.Minute).String()
	}
	return out
}

// licenseReport renders a verified license for the CLI's JSON documents, in whichever container
// it arrived in, and returns the key it belongs under.
//
// A flat license keeps emitting exactly what it always did, under "claims". A v3 credential gets
// its own key and its FULL grant list: the per-line detail is what the container exists to carry,
// and a report that printed one aggregate term would be the flattening this whole change refuses
// — an operator holding a base plus two add-ons would see one product and one date.
func licenseReport(v license.Verified, now time.Time) (string, any) {
	if !v.IsCredentialV3() {
		return "claims", v.Claims
	}
	c := v.Credential
	// "active" is derived from the credential-level effective set, NEVER from a line's own upper
	// bound: an add-on whose base has lapsed is inside its own window and confers nothing
	// (PRICING-CANON.md:925), and before not_before no line confers anything at all. Reading
	// g.Active(now) here printed `status:"expired"` beside `active:true` for the same document.
	// (2026-08-11 Codex contrast, F-3.)
	effective := make(map[string]bool, len(c.Grants))
	for _, g := range c.ActiveGrants(now) {
		effective[g.GrantID] = true
	}
	grants := make([]map[string]any, 0, len(c.Grants))
	for _, g := range c.Grants {
		line := map[string]any{
			"grant_id":           g.GrantID,
			"order_line_id":      g.OrderLineID,
			"product_id":         g.ProductID,
			"kind":               string(g.Kind),
			"cadence":            g.Cadence,
			"issuance_phase":     string(g.Phase),
			"paid_through":       g.PaidThrough.UTC().Format(time.RFC3339),
			"expires_at":         g.ExpiresAt.UTC().Format(time.RFC3339),
			"effective_boundary": g.EffectiveBoundary().UTC().Format(time.RFC3339),
			"active":             effective[g.GrantID],
		}
		for k, t := range map[string]time.Time{
			"guarantee_deadline":      g.GuaranteeDeadline,
			"promotion_hold_deadline": g.PromotionHoldDeadline,
			"lease_until":             g.LeaseUntil,
			"grace_ends_at":           g.GraceEndsAt,
		} {
			if !t.IsZero() {
				line[k] = t.UTC().Format(time.RFC3339)
			}
		}
		if g.GraceReason != "" {
			line["grace_reason"] = g.GraceReason
		}
		if g.PriceVintage != "" {
			line["price_vintage"] = g.PriceVintage
		}
		grants = append(grants, line)
	}
	out := map[string]any{
		"schema":        c.Schema,
		"serial":        c.Serial,
		"issue_seq":     c.IssueSeq,
		"key_id":        c.KeyID,
		"key_epoch":     c.KeyEpoch,
		"issued_at":     c.IssuedAt.UTC().Format(time.RFC3339),
		"not_before":    c.NotBefore.UTC().Format(time.RFC3339),
		"entity_id":     c.EntityID,
		"deployment_id": c.Deployment,
		"purpose":       c.Purpose,
		"licensee":      c.Licensee,
		"grants":        grants,
	}
	for k, s := range map[string]string{
		"supersedes_serial": c.SupersedesSerial,
		"support_profile":   c.SupportProfile,
		"clock_policy":      c.ClockPolicy,
		"clock_key_id":      c.ClockKeyID,
		"clock_anchor_id":   c.ClockAnchorID,
	} {
		if s != "" {
			out[k] = s
		}
	}
	return "credential", out
}

// decodeDetachedSig parses a detached signature file (base64, as release
// manifest .sig files are written) tolerating surrounding whitespace.
func decodeDetachedSig(raw []byte) ([]byte, error) {
	s := strings.TrimSpace(string(raw))
	sig, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("manifest signature is not valid base64: %w", err)
	}
	return sig, nil
}

// writeKeyFile writes sensitive bytes to path and GUARANTEES the mode it
// promises. `what` names the artifact for the refusal messages — it guards key
// material for `license keygen` and the installed license for `license install`,
// and an operator reading "the wrong custody" deserves to know over what.
//
// os.WriteFile applies its perm argument ONLY WHEN IT CREATES THE FILE
// (O_WRONLY|O_CREATE|O_TRUNC): an existing path is truncated and keeps whatever mode it
// already had. Measured 2026-08-06 with a probe rather than read off the documentation —
// a target created 0644 still read 0644 after a 0600 write, with the private key material
// now in it. For the one artifact that mints licenses, that is the whole custody promise
// failing silently while the command exits 0.
//
// Two failures, one fix:
//   - WITHOUT force the path is created O_EXCL, so an existing key is never destroyed.
//     There was no --force and no exclusive create, so re-running a ceremony with the
//     wrong path overwrote the private anchor and said nothing.
//   - WITH force the write goes to a fresh temp file IN THE SAME DIRECTORY (so the rename
//     cannot cross filesystems), is chmod'ed and re-stat'ed BEFORE it carries the secret
//     into place, and only then replaces the target. The mode is then asserted on the
//     final path: a promise nobody checks is a promise nobody keeps.
func writeKeyFile(path string, data []byte, mode os.FileMode, force bool, what string) error {
	if !force {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			if errors.Is(err, fs.ErrExist) {
				return fmt.Errorf("%s already exists — refusing to overwrite %s. "+
					"Move it aside, or pass --force to replace it deliberately", path, what)
			}
			return err
		}
		if _, werr := f.Write(data); werr != nil {
			_ = f.Close()
			_ = os.Remove(path) // a half-written key is worse than none
			return werr
		}
		if cerr := f.Close(); cerr != nil {
			_ = os.Remove(path)
			return cerr
		}
		return assertMode(path, mode, what)
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".olivares-key-*")
	if err != nil {
		return fmt.Errorf("create a temporary file beside %s: %w", path, err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = tmp.Close(); _ = os.Remove(tmpName) }
	// chmod BEFORE the secret is written: CreateTemp makes 0600, but the mode is asserted
	// explicitly so this holds if that ever changes, and so a 0644 public key is right too.
	if err := tmp.Chmod(mode); err != nil {
		cleanup()
		return fmt.Errorf("set mode %04o on the temporary file: %w", mode, err)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil { // the key must survive a crash between write and rename
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := assertMode(tmpName, mode, what); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return assertMode(path, mode, what)
}

// assertMode refuses when the file on disk does not carry the mode that was promised.
// Without it every guarantee above is a comment.
func assertMode(path string, want os.FileMode, what string) error {
	st, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat %s to verify its mode: %w", path, err)
	}
	if got := st.Mode().Perm(); got != want.Perm() {
		return fmt.Errorf("%s has mode %04o, not the %04o this command promises — "+
			"refusing to report success over %s with the wrong custody", path, got, want.Perm(), what)
	}
	return nil
}

// licenseKeygenResult reports each half of a minted pair BY SINK, on the same rule
// as ddilKeygenResult: a half written to a FILE reports its PATH, a half printed to
// stdout reports its VALUE. All four flag combinations are therefore representable,
// and the one that matters most — `--out-private <file>` — cannot carry the private
// key into the object, because the whole purpose of that flag is that the key does
// not pass through stdout. A witness asserts the absence.
//
// This is also the leaf where -o json earns its place most concretely. The text
// form is `public_key:  <b64>` with TWO SPACES after the colon, so scripting it
// today means a sed that happens to tolerate the padding — and what the command's
// own Long text asks the operator to do with that value is paste it into a
// `-ldflags -X …releasePublicKeyB64=` invocation. `-o json | jq -r .public_key` is
// that, without the parser.
type licenseKeygenResult struct {
	PrivateKey     string `json:"private_key,omitempty"`
	PrivateKeyFile string `json:"private_key_file,omitempty"`
	PublicKey      string `json:"public_key,omitempty"`
	PublicKeyFile  string `json:"public_key_file,omitempty"`
}

func licenseKeygenCmd() *cobra.Command {
	var outPriv, outPub string
	var force bool
	cmd := &cobra.Command{
		Use:   "keygen",
		Short: "Generate one Ed25519 keypair for a license or OTA trust domain",
		Long: "keygen mints one fresh Ed25519 keypair. Assign each generated pair to exactly\n" +
			"one trust domain; invoke it twice for independent license and OTA pairs.\n\n" +
			"Inject a LICENSE public key into release builds with:\n" +
			"  go build -tags release -ldflags \\\n" +
			"    \"-X github.com/olivaresai/olivares/core/license.releasePublicKeyB64=<public_key>\"\n" +
			"or set OLIVARES_LICENSE_PUBKEY. Set OLIVARES_OTA_PUBKEY for the independently\n" +
			"generated OTA public key. License private custody is the scoped Worker; OTA private\n" +
			"custody is off-box/HSM. Full ceremony: docs/POLAR-COMMERCIAL-SETUP.md §2.",
		Example: "  olivares license keygen --out-private license-private.key --out-public license-public.key",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			pub, priv, err := license.GenerateKey()
			if err != nil {
				return err
			}
			pubB64, privB64 := license.EncodeKey(pub), license.EncodeKey(priv)
			out := cmd.OutOrStdout()
			// Prefer files when asked: the private key on stdout leaks into terminal
			// scrollback, tmux capture, shell history and CI logs — for the single
			// most sensitive artifact in the product, a 0600 file is the correct sink.
			//
			// The PRIVATE key goes first, deliberately. If the pair cannot be landed
			// whole, the half that must not be missing is the secret: a public key
			// without its private half is inert, while the reverse is a key nobody can
			// use and nobody knows they have. The residual window is named rather than
			// hidden — two files cannot be renamed atomically as one — and the failure
			// path below removes the private key it just wrote, so a failed run leaves
			// the ceremony where it started instead of half-done.
			// The private line is emitted HERE, at the point the ceremony reaches it,
			// and not from the renderer at the end. That looks like an inconsistency
			// with the rest of this lot and it is load-bearing: when the private half
			// goes to stdout and the PUBLIC write then fails, the pre-existing text
			// behavior is that the private key is already on the operator's terminal —
			// it is the only copy that ever existed, since nothing was persisted.
			// Deferring it to a renderer that the error path never reaches would delete
			// that line, which is a change to what this command prints without -o, in
			// the one command whose output mints licenses. So text keeps its ordering
			// exactly, byte for byte, on the success path AND on that failure path.
			//
			// JSON cannot have it both ways: a half-written object is not parseable, so
			// the json form fails closed there and says what to do about it. That mode
			// is new, so it breaks no contract — and nothing was persisted, so re-running
			// the ceremony costs nothing.
			format, ferr := selectedOutput(cmd)
			if ferr != nil {
				return ferr
			}
			asJSON := format == "json"
			var res licenseKeygenResult
			if outPriv != "" {
				if werr := writeKeyFile(outPriv, []byte(privB64+"\n"), 0o600, force, "key material"); werr != nil {
					return fmt.Errorf("write private key: %w", werr)
				}
				res.PrivateKeyFile = outPriv
			} else {
				res.PrivateKey = privB64
				// Read back from res, not from privB64, so this line and the JSON field
				// have ONE source. Measured with a mutant: dropping the value from the
				// struct then breaks the text too, instead of letting the two forms
				// silently disagree about what the command produced.
				if !asJSON {
					fmt.Fprintf(out, "private_key: %s\n", res.PrivateKey)
				}
			}
			if outPub != "" {
				if werr := writeKeyFile(outPub, []byte(pubB64+"\n"), 0o644, force, "key material"); werr != nil { //nolint:gosec // a public key is not secret
					if outPriv != "" {
						// Do not leave a private key whose public half never landed: the
						// operator would inject an anchor that matches nothing.
						_ = os.Remove(outPriv)
						return fmt.Errorf("write public key: %w (the private key just written to %s was removed, "+
							"so this ceremony left nothing half-done — re-run it)", werr, outPriv)
					}
					if asJSON {
						return fmt.Errorf("write public key: %w (no key material was emitted, because a partial "+
							"object is not parseable and nothing was persisted — re-run the ceremony)", werr)
					}
					return fmt.Errorf("write public key: %w", werr)
				}
				res.PublicKeyFile = outPub
			} else {
				res.PublicKey = pubB64
			}
			if rerr := renderOut(cmd, func(w io.Writer) error {
				if res.PublicKey == "" {
					return nil
				}
				_, werr := fmt.Fprintf(w, "public_key:  %s\n", res.PublicKey)
				return werr
			}, res); rerr != nil {
				return rerr
			}
			// Custody guidance to STDERR so it never pollutes a piped key value.
			fmt.Fprintln(cmd.ErrOrStderr(), "Assign this pair to exactly one domain. License private keys go only to the scoped Worker; "+
				"OTA private keys stay off-box/HSM. Never commit, build with, or reuse a private key. Public keys are safe to publish.")
			return nil
		},
	}
	cmd.Flags().StringVar(&outPriv, "out-private", "", "write the private key to this file (created 0600; refuses to overwrite without --force) instead of stdout")
	cmd.Flags().StringVar(&outPub, "out-public", "", "write the public key to this file (created 0644; refuses to overwrite without --force) instead of stdout")
	cmd.Flags().BoolVar(&force, "force", false,
		"replace existing key files. Without it an existing path is REFUSED, because re-running a ceremony "+
			"with the wrong path used to destroy the signing anchor in silence. With it the replacement is "+
			"written to a temporary file beside the target, chmod'ed and verified, and renamed into place")
	return cmd
}

func licenseSignCmd() *cobra.Command {
	var licensee, plan, supportTier, holder, expires, keyB64, featuresCSV string
	var maxUsers int
	cmd := &cobra.Command{
		Use:   "sign",
		Short: "Sign a license (requires --key in a release build; uses the dev key only in dev/test builds)",
		Long: "sign creates an offline Ed25519-signed license blob from the supplied organization, plan,\n" +
			"support, holder, seat, feature tags and expiry claims. Production release builds require an explicit private key.\n" +
			"--features attests informational tags onto Claims.Features (never a gate; docs/07 §9).\n" +
			"--max-users is attested for display only (B10): self-hosted users are unlimited in every tier,\n" +
			"so leave it at 0 unless you are reproducing a historical blob.",
		Example: "  olivares license sign --licensee 'Acme Ltd' --plan business --expires 2027-07-14T00:00:00Z --key \"$LICENSE_PRIVATE_KEY\"",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			priv := license.DevPrivateKey()
			switch {
			case keyB64 != "":
				p, err := license.DecodePrivateKey(keyB64)
				if err != nil {
					return err
				}
				priv = p
			case !license.HasDevKey:
				// A release build ships no dev key; falling through would sign with a
				// nil key and emit the cryptic "bad private key size 0". Fail clearly.
				return fmt.Errorf("this is a release build: production signing requires --key <base64-private-key> (the dev key is not compiled in)")
			}
			feats, ferr := parseLicenseFeatures(featuresCSV)
			if ferr != nil {
				return ferr
			}
			c := license.Claims{Licensee: licensee, Plan: plan, SupportTier: supportTier, HolderID: holder, MaxUsers: maxUsers, Features: feats, IssuedAt: time.Now().UTC()}
			if expires != "" {
				t, err := time.Parse(time.RFC3339, expires)
				if err != nil {
					return fmt.Errorf("parse --expires (RFC3339): %w", err)
				}
				c.ExpiresAt = t
			}
			blob, err := license.Sign(c, priv)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), blob)
			if keyB64 != "" {
				// Provenance reminder to STDERR (keeps a piped blob clean): the holder
				// of the matching public key is what verifies this, not the dev key.
				fmt.Fprintln(cmd.ErrOrStderr(), "signed with --key; verify with the matching public key: olivares license verify <blob> --pubkey <public_key>")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&licensee, "licensee", "", "the organization the exception is granted to")
	cmd.Flags().StringVar(&plan, "plan", "commercial", "plan label")
	cmd.Flags().StringVar(&supportTier, "support-tier", "", "attested support relationship label for display only, e.g. standard|enterprise (empty = none; never gates — SUPPORT.md)")
	cmd.Flags().StringVar(&holder, "holder", "", "opaque holder id")
	cmd.Flags().StringVar(&expires, "expires", "", "expiry (RFC3339). Empty signs a blob with NO expiry, which the wire format still accepts but commercial entitlements are term-only — every real license gets a date")
	cmd.Flags().StringVar(&keyB64, "key", "", "base64 Ed25519 private key (default: dev key)")
	cmd.Flags().IntVar(&maxUsers, "max-users", 0, "attested seat figure, DISPLAY-ONLY since B10 — no build caps users on it; leave 0 (unlimited), which is what every self-hosted tier gets")
	cmd.Flags().StringVar(&featuresCSV, "features", "", "comma-separated add-on ids from the fused pricing canon (informational; never a gate)")
	return cmd
}

// fusedCanonAddonIDs are the four self-hosted business add-ons as
// named by PRICING-CANON.md (`self_hosted.business.addons.<id>`).
// Measured on main (catalog-v8). C03-03: do not invent a fifth.
var fusedCanonAddonIDs = []string{
	"regulated",
	"ai-runtime-security",
	"compliance-packs",
	"identity-scale",
}

func isFusedCanonAddonID(id string) bool {
	for _, a := range fusedCanonAddonIDs {
		if a == id {
			return true
		}
	}
	return false
}

// parseLicenseFeatures splits --features. Empty input is no tags. A
// blank token is refused. A tag that is not one of the four fused
// canon add-on ids is refused (C03-03): minting an invented id would
// make issued licenses diverge from the signed catalog.
func parseLicenseFeatures(csv string) ([]string, error) {
	csv = strings.TrimSpace(csv)
	if csv == "" {
		return nil, nil
	}
	var out []string
	for _, part := range strings.Split(csv, ",") {
		p := strings.TrimSpace(part)
		if p == "" {
			return nil, fmt.Errorf("--features contains an empty tag")
		}
		if !isFusedCanonAddonID(p) {
			return nil, fmt.Errorf("--features %q is not a fused-canon add-on id", p)
		}
		out = append(out, p)
	}
	return out, nil
}

func licenseVerifyCmd() *cobra.Command {
	var pubB64, manifestPath, manifestSig, otaPub string
	cmd := &cobra.Command{
		Use:   "verify <license-blob>",
		Short: "Verify a license against a public key (default: embedded key), with profile/grace and optional CRL status",
		Long: "verify authenticates a signed license blob with the embedded or supplied Ed25519 public key and\n" +
			"prints its claims, profile, grace and current status. With --manifest it also evaluates the\n" +
			"license CRL an OTA-signed channel manifest carries: the manifest signature MUST verify\n" +
			"against the OTA anchor, because an unsigned CRL is exactly the spoof the OTA key domain exists\n" +
			"to stop. Without --manifest the CRL is honestly reported as unavailable — it only reaches\n" +
			"deployments that pull updates or import offline bundles. Everything here is display-only:\n" +
			"license status never gates the open binary (docs/07 §9).",
		Example:      "  olivares license verify \"$LICENSE_BLOB\" --pubkey \"$LICENSE_PUBLIC_KEY\" --manifest ./stable-manifest.json",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			pub := license.DefaultPublicKey()
			if pubB64 != "" {
				p, err := license.DecodePublicKey(pubB64)
				if err != nil {
					return err
				}
				pub = p
			} else if len(pub) == 0 {
				// A release build with no/invalid key injected has no embedded anchor.
				// Mirror the sign path's clear message instead of the cryptic
				// "license: bad public key size 0" from Verify.
				return fmt.Errorf("this build embeds no verification key (license-key=%s); pass --pubkey <base64-public-key> to verify", license.KeyOrigin())
			}
			lic, err := license.VerifyEnvelope(strings.TrimSpace(args[0]), pub)
			if err != nil {
				return err
			}
			rev, crlDesc, crlOK, err := loadRevocation(manifestPath, manifestSig, otaPub)
			if err != nil {
				return err
			}
			now := time.Now().UTC()
			result := licenseLifecycle(lic, rev, crlDesc, crlOK, now)
			reportKey, reportBody := licenseReport(lic, now)
			result[reportKey] = reportBody
			// E2: honor -o instead of always printing JSON.
			return renderReportOut(cmd, result)
		},
	}
	cmd.Flags().StringVar(&pubB64, "pubkey", "", "base64 Ed25519 public key (default: embedded key)")
	cmd.Flags().StringVar(&manifestPath, "manifest", "", "OTA channel manifest to read the license CRL from (its signature must verify)")
	cmd.Flags().StringVar(&manifestSig, "manifest-sig", "", "detached manifest signature (default <manifest>.sig)")
	cmd.Flags().StringVar(&otaPub, "ota-pubkey", "", "base64 or @file Ed25519 OTA key for the manifest (default: the key embedded in this build)")
	return cmd
}
