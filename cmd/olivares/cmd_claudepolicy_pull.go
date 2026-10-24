// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// cmd_claudepolicy_pull.go is the workstation half of the fleet mechanism: pull the signed
// artifact of a surface, verify it, write the file, and attest what is now on disk. It is
// the same for every surface (a Claude Code settings file, a Codex requirements.toml, a
// Grok, OpenCode or Gemini CLI file), because the plane signs the exact bytes of the file.
//
// WHAT "VERIFY" MEANS HERE. The key is pinned by the operator out of band (--pin-key-
// fingerprint), never taken from the answer: an answer that brings its own key proves
// nothing. Then, in this order, and before anything is written: the key in the answer is
// the pinned one, the content hashes to artifact_sha256, and the detached Ed25519
// signature over the domain-separated hash of tenant | surface | revision | sha verifies.
// The tenant is this CLI's own, so an artifact signed for another tenant, surface or
// revision does not verify. Any failure exits 7 (degraded) with the host file untouched
// and no attestation sent.

// policyArtifactPreimagePrefix pins a signature to the policy-artifact domain; it is the
// plane's artifactPreimagePrefix (modules-overlay/governance/claudepolicy_truth_enterprise.go).
const policyArtifactPreimagePrefix = "olivares.policy.artifact.v1"

type policyArtifact struct {
	Surface        string `json:"surface"`
	Revision       int64  `json:"revision"`
	Content        string `json:"content"`
	ArtifactSHA256 string `json:"artifact_sha256"`
	Signature      string `json:"signature"`
	PublicKey      string `json:"pubkey"`
	KeyFingerprint string `json:"key_fingerprint"`
	Algorithm      string `json:"algorithm"`
}

// verifyPolicyArtifact checks an artifact against the surface asked for, the revision asked
// for (0 means the newest), this CLI's tenant and the pinned key fingerprint. It returns nil
// only when every check holds.
func verifyPolicyArtifact(a policyArtifact, surface string, wantRevision int64, tenant, pin string) error {
	if a.Revision < 1 {
		return fmt.Errorf("the plane served no revision number")
	}
	if wantRevision > 0 && a.Revision != wantRevision {
		return fmt.Errorf("the plane served revision %d, not the %d asked for", a.Revision, wantRevision)
	}
	if a.Algorithm != "ed25519" {
		return fmt.Errorf("artifact algorithm %q is not ed25519", a.Algorithm)
	}
	if a.Surface != surface {
		return fmt.Errorf("the plane served surface %q, not %q", a.Surface, surface)
	}
	pub, err := base64.StdEncoding.DecodeString(a.PublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("artifact public key is not an Ed25519 key")
	}
	sig, err := base64.StdEncoding.DecodeString(a.Signature)
	if err != nil {
		return fmt.Errorf("artifact signature is not base64")
	}
	keySum := sha256.Sum256(pub)
	fingerprint := hex.EncodeToString(keySum[:])[:16]
	if subtle.ConstantTimeCompare([]byte(fingerprint), []byte(a.KeyFingerprint)) != 1 {
		return fmt.Errorf("artifact key_fingerprint %q is not the fingerprint of the public key it carries", a.KeyFingerprint)
	}
	if subtle.ConstantTimeCompare([]byte(fingerprint), []byte(pin)) != 1 {
		return fmt.Errorf("the artifact is signed by key %s, which does not match the pinned key fingerprint", fingerprint)
	}
	contentSum := sha256.Sum256([]byte(a.Content))
	if hex.EncodeToString(contentSum[:]) != a.ArtifactSHA256 {
		return fmt.Errorf("the artifact content does not match artifact_sha256")
	}
	digest := sha256.Sum256([]byte(policyArtifactPreimagePrefix + "|" + tenant + "|" + surface + "|" +
		strconv.FormatInt(a.Revision, 10) + "|" + a.ArtifactSHA256))
	if !ed25519.Verify(pub, digest[:], sig) {
		return fmt.Errorf("the artifact signature does not verify for tenant %s, surface %s, revision %d", tenant, surface, a.Revision)
	}
	return nil
}

// writeFileAtomic replaces path with data: a host never sees a half-written policy file.
func writeFileAtomic(path string, data []byte) error {
	// A fresh host has no /etc/opencode or /etc/codex yet.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // managed policy directories are world-readable by design
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".olivares-pull-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // no-op once renamed
	// Chmod on the descriptor, not the name: the name is in a directory the caller may share.
	if err := tmp.Chmod(0o644); err != nil { //nolint:gosec // managed policy files are world-readable by design
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	// Sync before the rename: a crash must leave the old file or the whole new one, never an
	// empty policy file, which is an ungoverned host.
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func newClaudePolicyPullCmd(flags *authClientFlags) *cobra.Command {
	var (
		out      string
		scope    string
		pin      string
		revision int64
	)
	cmd := &cobra.Command{
		Annotations: map[string]string{"changes_data": "write"},
		Use:         "pull <surface>",
		Short:       "Pull a surface's signed artifact, verify it, write the file and attest it (exit 7 when it does not verify)",
		Long: "pull is what a workstation runs. It fetches the newest signed artifact of a surface\n" +
			"(or --revision), verifies it against the key you pinned with --pin-key-fingerprint,\n" +
			"writes the document to --out and reports back through `checkin` with the hash it\n" +
			"verified and the bytes it finds on disk, so the plane can show drift.\n\n" +
			"The pin is the fingerprint `publish` printed, handed to the host out of band. An\n" +
			"artifact that is not signed by that key for this tenant, surface and revision, or\n" +
			"whose content does not match its hash, exits 7: the file on the host is left as it\n" +
			"was and nothing is attested.\n\n" +
			"Run it as the user that owns --out (root for /etc/...). Placement per tool:\n" +
			"  managed-settings          /etc/claude-code/managed-settings.json\n" +
			"  codex-requirements        /etc/codex/requirements.toml\n" +
			"  codex-managed-config      /etc/codex/managed_config.toml\n" +
			"  grok-requirements         /etc/grok/requirements.toml\n" +
			"  opencode-config           /etc/opencode/opencode.json\n" +
			"  gemini-cli-settings       /etc/gemini-cli/settings.json",
		Example: "  olivares claude-policy pull opencode-config --scope $(hostname) \\\n" +
			"    --pin-key-fingerprint 1a2b3c4d5e6f7a8b --out /etc/opencode/opencode.json",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			surface, err := claudePolicySurface(args[0])
			if err != nil {
				return err
			}
			if out == "" {
				return exitcode.New(exitcode.Usage, fmt.Errorf("--out must name the file to write"))
			}
			if revision < 0 {
				return exitcode.New(exitcode.Usage, fmt.Errorf("--revision must be positive, got %d", revision))
			}
			opts, err := flags.resolutionOptions(cmd)
			if err != nil {
				return redactCoded(err, flags.token)
			}
			resolved, err := resolveCLIConfig(opts)
			if err != nil {
				return redactCoded(err, flags.token)
			}
			q := url.Values{}
			if revision > 0 {
				q.Set("revision", strconv.FormatInt(revision, 10))
			}
			res, err := agentExecCall{
				flags: flags, module: claudePolicyModule,
				method: http.MethodGet, path: "/" + agentExecPathID(surface) + "/artifact", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			var artifact policyArtifact
			if err := res.decode(&artifact); err != nil {
				return exitcode.New(exitcode.Server, err)
			}
			if err := verifyPolicyArtifact(artifact, surface, revision, resolved.Tenant, pin); err != nil {
				return exitcode.New(exitcode.Degraded, fmt.Errorf("NOT APPLIED: %w; %s is unchanged", err, out))
			}
			if err := writeFileAtomic(out, []byte(artifact.Content)); err != nil {
				return fmt.Errorf("write %s: %w", out, err)
			}
			onDisk, err := os.ReadFile(out) //nolint:gosec // the path the operator named
			if err != nil {
				return fmt.Errorf("read back %s: %w", out, err)
			}
			if sum := sha256.Sum256(onDisk); hex.EncodeToString(sum[:]) != artifact.ArtifactSHA256 {
				return exitcode.New(exitcode.Degraded, fmt.Errorf(
					"NOT ATTESTED: %s does not hash to the artifact after the write (the disk changed it)", out))
			}
			attested, err := agentExecCall{
				flags: flags, module: claudePolicyModule,
				method: http.MethodPost, path: "/" + agentExecPathID(surface) + "/checkin",
				body: map[string]any{
					"scope": scope, "revision": artifact.Revision,
					"artifact_sha256": artifact.ArtifactSHA256, "key_fingerprint": artifact.KeyFingerprint,
					"observed_content": string(onDisk),
				},
			}.do(cmd)
			if err != nil {
				return fmt.Errorf("revision %d was written to %s but the check-in failed: %w", artifact.Revision, out, err)
			}
			if err := claudePolicyReportCheckin(cmd, flags, attested); err != nil {
				return err
			}
			// Verified says the host echoed what was signed. Two more answers decide whether the
			// pull left the host on the policy: a newer revision exists (an older signed artifact
			// is a rollback), and the plane found the file's keys drifting.
			var after struct {
				LatestRevision int64             `json:"latest_revision"`
				Drift          []json.RawMessage `json:"drift"`
			}
			_ = attested.decode(&after) // claudePolicyReportCheckin already refused an unreadable answer
			if after.LatestRevision > artifact.Revision {
				return exitcode.New(exitcode.Degraded, fmt.Errorf(
					"STALE: wrote revision %d to %s but the plane's latest is %d; pull again or publish an artifact for it",
					artifact.Revision, out, after.LatestRevision))
			}
			if len(after.Drift) > 0 {
				return exitcode.New(exitcode.Degraded, fmt.Errorf(
					"revision %d was written to %s but the plane reports %d drift finding(s) for it", artifact.Revision, out, len(after.Drift)))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "the file to write (required)")
	cmd.Flags().StringVar(&scope, "scope", "", "the host id / distribution name this workstation reports as (required)")
	cmd.Flags().StringVar(&pin, "pin-key-fingerprint", "", "the signing key fingerprint 'publish' printed, pinned out of band (required)")
	cmd.Flags().Int64Var(&revision, "revision", 0, "a specific revision (0 uses the newest); one older than the plane's latest is written and then exits 7, because the host now drifts from the latest")
	for _, name := range []string{"out", "scope", "pin-key-fingerprint"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}
