// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// SignatureReport is what a successful detached-signature verification proved.
type SignatureReport struct {
	// PrimaryFingerprint is the primary key of the certificate that signed; it
	// equals the pinned fingerprint or verification did not succeed.
	PrimaryFingerprint string
	// SigningFingerprint is the key that made the signature: the primary key or
	// one of its signing subkeys.
	SigningFingerprint string
	Created            time.Time
	PubkeyAlgo         string
	HashAlgo           string
	Verifier           string
}

// GPGVerifier verifies detached OpenPGP signatures with the local gpg executable
// in a throwaway keyring that holds exactly one key: the one pinned in this
// binary. It reads no user configuration, no user keyring, starts no agent and
// never contacts a keyserver.
type GPGVerifier struct {
	gpg     string
	version string
	timeout time.Duration
}

// NewGPGVerifier locates gpg with lookPath (exec.LookPath in production). A
// missing verifier is a verification_unavailable refusal with the way out named.
func NewGPGVerifier(ctx context.Context, lookPath func(string) (string, error)) (*GPGVerifier, error) {
	path, err := lookPath("gpg")
	if err != nil {
		return nil, refuse(KindVerificationUnavailable,
			"gpg is not installed or not on PATH; the release manifest cannot be verified and nothing is downloaded or executed without that verification (install GnuPG 2.x and re-run)")
	}
	if !filepath.IsAbs(path) {
		if path, err = filepath.Abs(path); err != nil {
			return nil, refuse(KindVerificationUnavailable, "resolve gpg path: %v", err)
		}
	}
	v := &GPGVerifier{gpg: path, timeout: 60 * time.Second}
	out, _, err := v.run(ctx, "", nil, "--version")
	if err != nil {
		return nil, refuse(KindVerificationUnavailable, "%s --version failed: %v", path, err)
	}
	first, _, _ := strings.Cut(string(out), "\n")
	v.version = strings.TrimSpace(first)
	if !strings.HasPrefix(v.version, "gpg (GnuPG") {
		return nil, refuse(KindVerificationUnavailable, "%s does not identify itself as GnuPG (got %q)", path, v.version)
	}
	return v, nil
}

// Describe names the verifier for receipts.
func (v *GPGVerifier) Describe() string { return "openpgp/" + v.version }

// run executes gpg with a minimal environment. homedir may be empty for
// invocations that need no keyring (--version, --show-keys).
func (v *GPGVerifier) run(ctx context.Context, homedir string, stdin []byte, args ...string) (stdout, stderr []byte, err error) {
	ctx, cancel := context.WithTimeout(ctx, v.timeout)
	defer cancel()
	base := []string{"--batch", "--no-tty", "--no-options", "--no-autostart", "--no-auto-key-retrieve", "--no-auto-key-locate", "--lock-never", "--exit-on-status-write-error"}
	if homedir != "" {
		base = append(base, "--homedir", homedir)
	}
	cmd := exec.CommandContext(ctx, v.gpg, append(base, args...)...) // #nosec G204 -- v.gpg is an absolute path resolved once from PATH; every argument is a fixed literal or a temporary file this package created
	env := []string{"LC_ALL=C", "PATH=" + filepath.Dir(v.gpg) + ":/usr/bin:/bin"}
	if homedir != "" {
		env = append(env, "GNUPGHOME="+homedir, "HOME="+homedir, "TMPDIR="+homedir)
	}
	cmd.Env = env
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
	return out.Bytes(), errb.Bytes(), err
}

// Verify checks that signature is a good detached signature over data by the key
// material key, whose primary fingerprint must equal wantFingerprint. Any
// expired, revoked, unknown or bad signature status is a signature_invalid
// refusal; a verifier failure is verification_unavailable.
func (v *GPGVerifier) Verify(ctx context.Context, key []byte, wantFingerprint string, signature, data []byte) (SignatureReport, error) {
	want := normalizeFingerprint(wantFingerprint)
	if len(want) != 40 {
		return SignatureReport{}, refuse(KindVerificationUnavailable, "pinned fingerprint %q is not a 40-hex-digit OpenPGP v4 fingerprint", wantFingerprint)
	}
	home, err := os.MkdirTemp("", "olivares-gpg-")
	if err != nil {
		return SignatureReport{}, refuse(KindVerificationUnavailable, "create isolated keyring: %v", err)
	}
	defer func() { _ = os.RemoveAll(home) }()
	if err := os.Chmod(home, 0o700); err != nil {
		return SignatureReport{}, refuse(KindVerificationUnavailable, "restrict isolated keyring: %v", err)
	}
	keyPath := filepath.Join(home, "pinned-key.asc")
	dataPath := filepath.Join(home, "data")
	sigPath := filepath.Join(home, "data.sig")
	for _, f := range []struct {
		path string
		b    []byte
	}{{keyPath, key}, {dataPath, data}, {sigPath, signature}} {
		if err := os.WriteFile(f.path, f.b, 0o600); err != nil {
			return SignatureReport{}, refuse(KindVerificationUnavailable, "stage %s: %v", filepath.Base(f.path), err)
		}
	}
	// The key material is checked against the pin BEFORE it enters the keyring:
	// a keyring with the wrong key would make every later status line meaningless.
	fprs, err := v.showFingerprints(ctx, home, keyPath)
	if err != nil {
		return SignatureReport{}, err
	}
	if len(fprs) != 1 || fprs[0] != want {
		return SignatureReport{}, refuse(KindVerificationUnavailable,
			"pinned key material carries primary fingerprint(s) %v, not the pinned %s; the key embedded in this binary and its pin disagree, so no signature can be trusted", fprs, want)
	}
	if _, stderr, err := v.run(ctx, home, nil, "--import", keyPath); err != nil {
		return SignatureReport{}, refuse(KindVerificationUnavailable, "import pinned key into isolated keyring: %v: %s", err, strings.TrimSpace(string(stderr)))
	}
	stdout, stderr, runErr := v.run(ctx, home, nil, "--status-fd", "1", "--verify", sigPath, dataPath)
	rep, err := parseVerifyStatus(stdout, want)
	if err != nil {
		return SignatureReport{}, fmt.Errorf("%w (gpg: %s)", err, oneLine(stderr))
	}
	if runErr != nil {
		// gpg exits non-zero for a bad signature; the status parse above already
		// classified that. Reaching here means a good status with a failed exit,
		// which is a verifier fault rather than a bad signature.
		return SignatureReport{}, refuse(KindVerificationUnavailable, "gpg reported a valid signature but exited with %v: %s", runErr, oneLine(stderr))
	}
	rep.Verifier = v.Describe()
	return rep, nil
}

func (v *GPGVerifier) showFingerprints(ctx context.Context, home, keyPath string) ([]string, error) {
	out, stderr, err := v.run(ctx, home, nil, "--with-colons", "--import-options", "show-only", "--import", keyPath)
	if err != nil {
		return nil, refuse(KindVerificationUnavailable, "inspect pinned key: %v: %s", err, oneLine(stderr))
	}
	var fprs []string
	afterPub := false
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Split(line, ":")
		switch fields[0] {
		case "pub":
			afterPub = true
		case "sub", "uid":
			if fields[0] == "sub" {
				afterPub = false
			}
		case "fpr":
			if afterPub && len(fields) > 9 {
				fprs = append(fprs, normalizeFingerprint(fields[9]))
				afterPub = false
			}
		}
	}
	if len(fprs) == 0 {
		return nil, refuse(KindVerificationUnavailable, "pinned key material contains no OpenPGP public key")
	}
	return fprs, nil
}

// parseVerifyStatus turns gpg --status-fd output into a report. It requires
// exactly one GOODSIG and one VALIDSIG whose primary fingerprint is the pin, and
// refuses on any status that weakens the verdict.
func parseVerifyStatus(status []byte, want string) (SignatureReport, error) {
	var rep SignatureReport
	good, valid := 0, 0
	for _, line := range strings.Split(string(status), "\n") {
		if !strings.HasPrefix(line, "[GNUPG:] ") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "[GNUPG:] "))
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "BADSIG":
			return rep, refuse(KindSignatureInvalid, "the manifest signature does not verify (BADSIG): the manifest or its signature was altered")
		case "ERRSIG":
			reason := "the signature could not be checked"
			if len(fields) > 6 && fields[6] == "9" {
				reason = "the signature was made by a key that is not the pinned release key (NO_PUBKEY)"
			}
			return rep, refuse(KindSignatureInvalid, "%s (ERRSIG %s)", reason, strings.Join(fields[1:], " "))
		case "EXPSIG", "SIGEXPIRED":
			return rep, refuse(KindSignatureInvalid, "the manifest signature has expired (%s)", fields[0])
		case "EXPKEYSIG", "KEYEXPIRED":
			return rep, refuse(KindSignatureInvalid, "the signing key has expired (%s); an expired key is not accepted", fields[0])
		case "REVKEYSIG", "KEYREVOKED":
			return rep, refuse(KindSignatureInvalid, "the signing key is revoked (%s); a revoked key is not accepted", fields[0])
		case "NO_PUBKEY":
			return rep, refuse(KindSignatureInvalid, "the signature was made by key %s, which is not the pinned release key", strings.Join(fields[1:], " "))
		case "NODATA", "UNEXPECTED", "NO_DATA":
			return rep, refuse(KindSignatureInvalid, "the signature file is not an OpenPGP detached signature (%s)", fields[0])
		case "GOODSIG":
			good++
		case "VALIDSIG":
			valid++
			// VALIDSIG <fpr> <date> <ts> <expire-ts> <ver> <reserved> <pk-algo> <hash-algo> <class> <primary-fpr>
			if len(fields) < 11 {
				return rep, refuse(KindSignatureInvalid, "VALIDSIG status is truncated: %q", line)
			}
			rep.SigningFingerprint = normalizeFingerprint(fields[1])
			rep.PrimaryFingerprint = normalizeFingerprint(fields[10])
			if ts, err := strconv.ParseInt(fields[3], 10, 64); err == nil {
				rep.Created = time.Unix(ts, 0).UTC()
			}
			rep.PubkeyAlgo = "openpgp-pk-" + fields[7]
			rep.HashAlgo = "openpgp-hash-" + fields[8]
		}
	}
	if good != 1 || valid != 1 {
		return rep, refuse(KindSignatureInvalid, "expected exactly one good signature, gpg reported GOODSIG=%d VALIDSIG=%d", good, valid)
	}
	if rep.PrimaryFingerprint != want {
		return rep, refuse(KindSignatureInvalid, "signature verifies under primary key %s, not the pinned %s", rep.PrimaryFingerprint, want)
	}
	return rep, nil
}

func normalizeFingerprint(s string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(s), " ", ""))
}

func oneLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	s = strings.ReplaceAll(s, "\n", " | ")
	if len(s) > 400 {
		s = s[:400] + "…"
	}
	return s
}

// UnavailableVerifier is the verifier wired when no gpg could be located: every
// verification returns the refusal that explains what is missing, so list and
// detect still work while plan and install refuse rather than proceed unverified.
type UnavailableVerifier struct{ Err error }

func (u UnavailableVerifier) Verify(context.Context, []byte, string, []byte, []byte) (SignatureReport, error) {
	if u.Err == nil {
		return SignatureReport{}, refuse(KindVerificationUnavailable, "no signature verifier is available")
	}
	return SignatureReport{}, u.Err
}

func (u UnavailableVerifier) Describe() string { return "unavailable" }
