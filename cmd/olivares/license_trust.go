// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/ed25519"
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
	"github.com/olivaresai/olivares/core/secure"
)

// license_trust.go is the ONE license trust resolver of this binary (connect-v1 contract §6).
//
// Boot, reload, `license install` (CLI and console), `license status`, the enterprise upgrade and
// `--bundle` license gates, doctor, the CRL description and the connected client all obtain their
// keyring here, so the same document answers the same way on every path. The keyring is the
// build's embedded license key plus the optional administrative document
// <data-dir>/license-trust.json. That document holds public keys only; it can add keys, retire
// them to verify-only, revoke them and set the key_epoch fence. Nothing here fetches a key.

const licenseTrustFileName = "license-trust.json"

func licenseTrustPath(dataDir string) string { return filepath.Join(dataDir, licenseTrustFileName) }

// errLicenseTrustUnsafe reports a trust document that is not an owner-controlled regular file.
var errLicenseTrustUnsafe = errors.New("the license trust document is not an owner-only regular file")

// embeddedLicenseTrust is the build anchor as keyring entries (none when no key is compiled in).
func embeddedLicenseTrust() []license.TrustedKey {
	pub := license.DefaultPublicKey()
	if len(pub) != ed25519.PublicKeySize {
		return nil
	}
	return []license.TrustedKey{{PublicKey: pub, State: license.KeyStateCurrent, Source: "embedded"}}
}

// readLicenseTrustDocument reads the optional administrative document. An absent document is not
// an error. A present one must be a regular file that neither group nor others can write, and it
// must decode strictly.
func readLicenseTrustDocument(dataDir string) (license.TrustDocument, bool, error) {
	if strings.TrimSpace(dataDir) == "" {
		return license.TrustDocument{}, false, nil
	}
	path := licenseTrustPath(dataDir)
	before, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return license.TrustDocument{}, false, nil
	}
	if err != nil {
		return license.TrustDocument{}, true, fmt.Errorf("read license trust %s: %w", path, err)
	}
	if !before.Mode().IsRegular() {
		return license.TrustDocument{}, true, fmt.Errorf("%w: %s is not a regular file", errLicenseTrustUnsafe, path)
	}
	if before.Mode().Perm()&0o022 != 0 {
		return license.TrustDocument{}, true, fmt.Errorf("%w: %s has mode %04o and is writable by group or others", errLicenseTrustUnsafe, path, before.Mode().Perm())
	}
	f, err := os.Open(path)
	if err != nil {
		return license.TrustDocument{}, true, fmt.Errorf("read license trust %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return license.TrustDocument{}, true, fmt.Errorf("%w: %s changed while it was opened", errLicenseTrustUnsafe, path)
	}
	data, err := io.ReadAll(io.LimitReader(f, license.MaxTrustDocumentBytes+1))
	if err != nil {
		return license.TrustDocument{}, true, fmt.Errorf("read license trust %s: %w", path, err)
	}
	doc, err := license.DecodeTrustDocument(data)
	if err != nil {
		return license.TrustDocument{}, true, fmt.Errorf("%s: %w", path, err)
	}
	return doc, true, nil
}

// licenseKeyringForDataDir resolves the keyring every product consumer verifies with.
func licenseKeyringForDataDir(dataDir string) (license.Keyring, error) {
	base := embeddedLicenseTrust()
	doc, present, err := readLicenseTrustDocument(dataDir)
	if err != nil {
		return license.Keyring{}, err
	}
	if !present {
		return license.NewKeyring(base, 0)
	}
	return doc.Apply(base)
}

// resolveLicenseKeyring is licenseKeyringForDataDir unless an explicit --pubkey names a single
// key, which then gets the same KID and epoch rules as any keyring entry.
func resolveLicenseKeyring(pubB64, dataDir string) (license.Keyring, error) {
	if strings.TrimSpace(pubB64) != "" {
		pub, err := license.DecodePublicKey(pubB64)
		if err != nil {
			return license.Keyring{}, err
		}
		return license.SingleKeyKeyring(pub, "--pubkey")
	}
	return licenseKeyringForDataDir(dataDir)
}

// licenseTrustAction is the operator's next step for a trust refusal, or "" for other errors.
func licenseTrustAction(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, license.ErrKeyUnknown):
		return "the license is signed by a key this deployment does not trust: add the issuer's public key with `olivares license trust set`, or upgrade to a binary that embeds it"
	case errors.Is(err, license.ErrKeyRevoked):
		return "its signing key is revoked in this deployment's license trust: obtain a credential signed by a current key"
	case errors.Is(err, license.ErrKeyRetired):
		return "its signing key is retired in this deployment's license trust: obtain a credential reissued under the current key"
	case errors.Is(err, license.ErrKeyIDMismatch), errors.Is(err, license.ErrKeyEpochFenced):
		return "the credential does not match this deployment's license trust: obtain a reissued credential, or correct the key epoch with `olivares license trust set`"
	case errors.Is(err, license.ErrNoTrustedKeys):
		return "this build embeds no license key: configure one with `olivares license trust set`"
	case errors.Is(err, errLicenseTrustUnsafe), errors.Is(err, license.ErrKeyringInvalid):
		return "repair " + licenseTrustFileName + " in the data directory (owner-only, strict " + license.TrustDocumentSchema + "), or remove it to trust only the embedded key"
	}
	return ""
}

// withLicenseTrustAction appends licenseTrustAction to err when it applies.
func withLicenseTrustAction(err error) error {
	if a := licenseTrustAction(err); a != "" {
		return fmt.Errorf("%w — %s", err, a)
	}
	return err
}

// writeLicenseTrustDocument validates doc against the embedded anchor and replaces the document
// atomically with mode 0600.
func writeLicenseTrustDocument(dataDir string, doc license.TrustDocument) (string, error) {
	if _, err := doc.Apply(embeddedLicenseTrust()); err != nil {
		return "", err
	}
	data, err := license.EncodeTrustDocument(doc)
	if err != nil {
		return "", err
	}
	if err := secure.EnsureDataDir(dataDir); err != nil {
		return "", err
	}
	path := licenseTrustPath(dataDir)
	if err := writeKeyFile(path, data, 0o600, true, "the license trust document"); err != nil {
		return "", fmt.Errorf("write license trust: %w", err)
	}
	return path, nil
}

// licenseTrustKeyReport renders a keyring entry for the CLI. Public data only.
func licenseTrustKeyReport(k license.TrustedKey) map[string]any {
	out := map[string]any{
		"kid":        k.KID(),
		"public_key": base64.StdEncoding.EncodeToString(k.PublicKey),
		"state":      string(k.State),
		"epoch":      k.Epoch,
		"source":     k.Source,
	}
	if !k.RetiredAt.IsZero() {
		out["retired_at"] = k.RetiredAt.UTC().Format(time.RFC3339)
	}
	if !k.VerifyUntil.IsZero() {
		out["verify_until"] = k.VerifyUntil.UTC().Format(time.RFC3339)
	}
	return out
}

// licenseTrustReport describes the keyring entry that verified a license.
func licenseTrustReport(v license.Verified) map[string]any {
	if v.Trust.KID == "" {
		return nil
	}
	return map[string]any{"kid": v.Trust.KID, "state": string(v.Trust.State), "epoch": v.Trust.Epoch, "source": v.Trust.Source}
}

func resolveCommandDataDir(dataDir string) (string, error) {
	if strings.TrimSpace(dataDir) != "" {
		return dataDir, nil
	}
	return defaultDataDir()
}

func licenseTrustCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "trust",
		Short: "Show or change which license signing keys this deployment trusts (current, verify-only, revoked, epoch fence)",
		Long: "trust manages the license trust keyring of a data directory: the key embedded in this build plus\n" +
			"the optional administrative document <data-dir>/" + licenseTrustFileName + ". Boot, reload, license\n" +
			"install, the enterprise upgrade and bundle gates and the connected client all verify licenses with\n" +
			"this keyring. It holds PUBLIC keys only and nothing here contacts a network.",
		Example: "  olivares license trust status --data-dir /var/lib/olivares\n" +
			"  olivares license trust fence --data-dir /var/lib/olivares --min-key-epoch 2",
	}
	root.AddCommand(licenseTrustStatusCmd(), licenseTrustSetCmd(), licenseTrustFenceCmd())
	return root
}

func licenseTrustStatusCmd() *cobra.Command {
	var dataDir string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the effective license trust keyring of a data directory",
		Long: "status prints every trusted license key with its derived key id, state, pinned epoch, retirement\n" +
			"window and source (embedded or data-dir), and the key_epoch fence. An unusable trust document is an\n" +
			"error that names the file.",
		Example:      "  olivares license trust status --data-dir /var/lib/olivares",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := resolveCommandDataDir(dataDir)
			if err != nil {
				return err
			}
			_, present, derr := readLicenseTrustDocument(dir)
			kr, err := licenseKeyringForDataDir(dir)
			if err == nil {
				err = derr
			}
			if err != nil {
				return withLicenseTrustAction(err)
			}
			keys := []map[string]any{}
			for _, k := range kr.Keys() {
				keys = append(keys, licenseTrustKeyReport(k))
			}
			return renderReportOut(cmd, map[string]any{
				"path":          licenseTrustPath(dir),
				"document":      present,
				"min_key_epoch": kr.MinKeyEpoch(),
				"keys":          keys,
			})
		},
	}
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "data directory whose license trust to show (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares)")
	return cmd
}

func licenseTrustSetCmd() *cobra.Command {
	var dataDir, publicKey, kid, state, retiredAt, verifyUntil string
	var epoch int
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Add a license public key or change a trusted key's state, epoch or retirement window",
		Long: "set writes one key into <data-dir>/" + licenseTrustFileName + " (mode 0600, replaced atomically).\n" +
			"Name the key by --public-key (base64, or @file) to add it, or by --kid to change a key that is already\n" +
			"trusted, including the embedded one. --state is current, verify_only or revoked. A verify_only key\n" +
			"needs --retired-at: licenses it signed at or after that instant are refused, and --verify-until ends\n" +
			"its verification window. --epoch pins the key_epoch its credentials must carry (0 = not pinned).\n" +
			"Apply to a running engine without a restart: kill -HUP <pid>, or POST /v1/console/runtime/reload.",
		Example: "  olivares license trust set --data-dir /var/lib/olivares --public-key @issuer-2026-10.pub --state current --epoch 2\n" +
			"  olivares license trust set --data-dir /var/lib/olivares --kid sha256:<hex> --state verify_only --epoch 1 --retired-at 2026-10-01T00:00:00Z",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := resolveCommandDataDir(dataDir)
			if err != nil {
				return err
			}
			doc, _, err := readLicenseTrustDocument(dir)
			if err != nil {
				return withLicenseTrustAction(err)
			}
			pub, err := licenseTrustSelectKey(dir, doc, publicKey, kid)
			if err != nil {
				return err
			}
			st, err := license.ParseKeyState(state)
			if err != nil {
				return err
			}
			entry := license.TrustDocumentKey{PublicKey: pub, State: st, Epoch: epoch}
			if entry.RetiredAt, err = parseTrustInstant("--retired-at", retiredAt); err != nil {
				return err
			}
			if entry.VerifyUntil, err = parseTrustInstant("--verify-until", verifyUntil); err != nil {
				return err
			}
			replaced := false
			for i := range doc.Keys {
				if doc.Keys[i].KID() == entry.KID() {
					doc.Keys[i], replaced = entry, true
				}
			}
			if !replaced {
				doc.Keys = append(doc.Keys, entry)
			}
			path, err := writeLicenseTrustDocument(dir, doc)
			if err != nil {
				return withLicenseTrustAction(err)
			}
			kr, err := licenseKeyringForDataDir(dir)
			if err != nil {
				return withLicenseTrustAction(err)
			}
			var written map[string]any
			for _, k := range kr.Keys() {
				if k.KID() == entry.KID() {
					written = licenseTrustKeyReport(k)
				}
			}
			if rerr := renderReportOut(cmd, map[string]any{"path": path, "key": written}); rerr != nil {
				return rerr
			}
			fmt.Fprintln(cmd.ErrOrStderr(), "Apply to a running engine WITHOUT a restart: kill -HUP <pid>, or POST /v1/console/runtime/reload.")
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&dataDir, "data-dir", "", "data directory whose license trust to change (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares)")
	f.StringVar(&publicKey, "public-key", "", "base64 Ed25519 license public key, or @file (public material only)")
	f.StringVar(&kid, "kid", "", "key id (sha256:<hex>) of a key that is already trusted")
	f.StringVar(&state, "state", "", "current | verify_only | revoked")
	f.IntVar(&epoch, "epoch", 0, "key_epoch the key's credentials must carry (0 = not pinned)")
	f.StringVar(&retiredAt, "retired-at", "", "verify_only: RFC3339 UTC instant from which this key's new licenses are refused")
	f.StringVar(&verifyUntil, "verify-until", "", "verify_only: RFC3339 UTC instant that ends this key's verification window")
	_ = cmd.MarkFlagRequired("state")
	return cmd
}

func licenseTrustFenceCmd() *cobra.Command {
	var dataDir string
	var minEpoch int
	cmd := &cobra.Command{
		Use:   "fence",
		Short: "Set the minimum key_epoch this deployment accepts in license credentials",
		Long: "fence writes min_key_epoch into <data-dir>/" + licenseTrustFileName + ". Credentials whose signed\n" +
			"key_epoch is below it are refused, and a flat legacy license is accepted only through a key pinned at\n" +
			"or above it. 0 removes the fence. A current key pinned below the new fence is refused as a\n" +
			"configuration error rather than silently disabled.",
		Example:      "  olivares license trust fence --data-dir /var/lib/olivares --min-key-epoch 2",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := resolveCommandDataDir(dataDir)
			if err != nil {
				return err
			}
			doc, _, err := readLicenseTrustDocument(dir)
			if err != nil {
				return withLicenseTrustAction(err)
			}
			doc.MinKeyEpoch = minEpoch
			path, err := writeLicenseTrustDocument(dir, doc)
			if err != nil {
				return withLicenseTrustAction(err)
			}
			return renderReportOut(cmd, map[string]any{"path": path, "min_key_epoch": minEpoch})
		},
	}
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "data directory whose license trust to change (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares)")
	cmd.Flags().IntVar(&minEpoch, "min-key-epoch", 0, "minimum accepted key_epoch (0 = no fence)")
	_ = cmd.MarkFlagRequired("min-key-epoch")
	return cmd
}

// licenseTrustSelectKey resolves the key `trust set` changes: exactly one of --public-key or --kid.
func licenseTrustSelectKey(dataDir string, doc license.TrustDocument, publicKey, kid string) (ed25519.PublicKey, error) {
	publicKey, kid = strings.TrimSpace(publicKey), strings.TrimSpace(kid)
	switch {
	case publicKey != "" && kid != "":
		return nil, fmt.Errorf("pass --public-key or --kid, not both")
	case publicKey != "":
		raw := publicKey
		if strings.HasPrefix(publicKey, "@") {
			b, err := os.ReadFile(publicKey[1:])
			if err != nil {
				return nil, fmt.Errorf("read --public-key file: %w", err)
			}
			raw = strings.TrimSpace(string(b))
		}
		return license.DecodePublicKey(raw)
	case kid != "":
		for _, k := range doc.Keys {
			if k.KID() == kid {
				return k.PublicKey, nil
			}
		}
		for _, k := range embeddedLicenseTrust() {
			if derived, err := license.KeyID(k.PublicKey); err == nil && derived == kid {
				return k.PublicKey, nil
			}
		}
		return nil, fmt.Errorf("no trusted license key in %s has kid %q; add it with --public-key", dataDir, kid)
	}
	return nil, fmt.Errorf("name the key with --public-key or --kid")
}

func parseTrustInstant(flag, value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil || !strings.HasSuffix(value, "Z") {
		return time.Time{}, fmt.Errorf("%s must be an RFC3339 UTC instant ending in Z", flag)
	}
	return t.UTC(), nil
}
