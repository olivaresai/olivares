// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/connectors/managedsettings"
)

// cmd_hooks.go is the operator CLI for the hooks-hardening add-on (legs 2 + 3): attest a
// fleet's deployed managed-settings as "deployed-verified", and produce a conformance certificate
// against the real claude binary. The verb LOGIC lives in the commercial enterprise/hookhardening
// module reached through the build-neutral hookHardeningEngine seam (hookhardeninggate.go). The
// command is always registered (main.go); in the default AGPL build the seam is nil, so each verb
// fails HONESTLY. A signing key is read from a file and NEVER logged.

func newHooksCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hooks",
		Short: "Hooks-hardening add-on: fleet deployed-verified attestation + conformance cert (enterprise)",
		Example: "  olivares hooks conform --behavioral --signing-key-file attest.key --signature-out hooks.conformance\n" +
			"  olivares hooks attest --version fleet-2026-07 --nodes node-reports.json \\\n" +
			"    --signing-key-file attest.key --signature-out fleet.attestation",
		Long: "hooks manages the hooks-hardening add-on (enterprise): attest that a fleet runs the exact,\n" +
			"PEP-hook-bearing managed-settings (deployed-verified), and certify conformance against the real\n" +
			"claude binary. The DLP-in-hook firewall half is enabled separately via OLIVARES_HOOK_FIREWALL_CONFIG.\n" +
			"Requires an enterprise build; the default AGPL build reports these verbs are unavailable.",
	}
	cmd.AddCommand(hooksAttestCmd(), hooksConformCmd())
	return cmd
}

// hookPolicyFlags are the shared flags that build the managed-settings Policy to attest/certify.
type hookPolicyFlags struct {
	pepCommand  string
	matcher     string
	timeoutSecs int
	redact      bool
	policyFile  string
}

func (f *hookPolicyFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.pepCommand, "pep-command", "olivares claude-hook", "the managed PreToolUse PEP-client command")
	cmd.Flags().StringVar(&f.matcher, "matcher", "", "tool-name matcher for the PEP hook (\"\" = all tools)")
	cmd.Flags().IntVar(&f.timeoutSecs, "timeout", 5, "PEP hook timeout in seconds")
	cmd.Flags().BoolVar(&f.redact, "redact", true, "also install the paired PostToolUse output-redaction hook")
	cmd.Flags().StringVar(&f.policyFile, "policy-file", "", "load a full managed-settings Policy JSON instead of building one from the flags above")
}

// resolve builds the Policy: a full Policy JSON from --policy-file when given, else the standard
// PEP-hook policy (the same one `olivares agent managed-settings` renders) from the flags.
func (f *hookPolicyFlags) resolve() (managedsettings.Policy, error) {
	if strings.TrimSpace(f.policyFile) != "" {
		b, err := os.ReadFile(f.policyFile)
		if err != nil {
			return managedsettings.Policy{}, fmt.Errorf("read policy file: %w", err)
		}
		var p managedsettings.Policy
		if err := json.Unmarshal(b, &p); err != nil {
			return managedsettings.Policy{}, fmt.Errorf("parse policy file: %w", err)
		}
		return p, nil
	}
	hooks, err := managedsettings.PEPHook(managedsettings.PEPHookConfig{
		Command: f.pepCommand, Matcher: f.matcher, TimeoutSecs: f.timeoutSecs, Redact: f.redact,
	})
	if err != nil {
		return managedsettings.Policy{}, err
	}
	return managedsettings.Policy{AllowManagedHooksOnly: true, Hooks: hooks}, nil
}

func hooksAttestCmd() *cobra.Command {
	var (
		pf           hookPolicyFlags
		version      string
		nodesFile    string
		signingKey   string
		signatureOut string
	)
	cmd := &cobra.Command{
		Use:   "attest",
		Short: "Attest a fleet's deployed managed-settings against the canonical PEP-hook bundle (deployed-verified)",
		Long: "attest renders the canonical, content-addressed managed-settings bundle (the PEP hook +\n" +
			"allowManagedHooksOnly) and classifies each fleet node report as deployed_verified | governed_no_pep |\n" +
			"drifted | ungoverned by a BYTE-EXACT hash comparison. With a signing key it emits a tamper-evident\n" +
			"signed roll-up. Node reports are a JSON array of {node_id, present, sha256, claude_version}.",
		Example: "  olivares hooks attest --version fleet-2026-07 --nodes node-reports.json --signing-key-file attest.key --signature-out fleet.attestation",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			eng, err := resolveHookHardening()
			if err != nil {
				return err
			}
			policy, err := pf.resolve()
			if err != nil {
				return err
			}
			var nodes []byte
			if strings.TrimSpace(nodesFile) != "" {
				if nodes, err = os.ReadFile(nodesFile); err != nil {
					return fmt.Errorf("read node reports: %w", err)
				}
			}
			key, err := readSigningKeyFile(signingKey)
			if err != nil {
				return err
			}
			blob, summary, err := eng.FleetAttest(policy, version, nodes, key, time.Now().UTC())
			if err != nil {
				return err
			}
			_, _ = cmd.OutOrStdout().Write(append(summary, '\n'))
			return emitSignature(cmd, blob, signatureOut)
		},
	}
	pf.bind(cmd)
	cmd.Flags().StringVar(&version, "version", "", "a label for the canonical bundle version")
	cmd.Flags().StringVar(&nodesFile, "nodes", "", "JSON file: an array of node reports to attest")
	cmd.Flags().StringVar(&signingKey, "signing-key-file", "", "file holding the base64 ed25519 private key to sign the attestation (optional)")
	cmd.Flags().StringVar(&signatureOut, "signature-out", "", "write the signed blob to this file (default: print to stderr when signed)")
	return cmd
}

func hooksConformCmd() *cobra.Command {
	var (
		pf           hookPolicyFlags
		signingKey   string
		signatureOut string
		behavioral   bool
	)
	cmd := &cobra.Command{
		Use:   "conform",
		Short: "Certify conformance of the managed-settings + PEP hook against the real claude binary",
		Long: "conform drives the real `claude` binary (version pin + settings-load) and validates the rendered\n" +
			"managed-settings carries the PreToolUse PEP hook, emitting a certificate. With --behavioral it also\n" +
			"drives the real binary against an in-process mock model and asserts the PreToolUse PEP BLOCKS a\n" +
			"tool-call in flight (no creds/network needed). It is HONEST when the binary is absent (a not_run\n" +
			"cert, never a fabricated pass). With a signing key it emits a signed cert blob.",
		Example: "  olivares hooks conform --behavioral --signing-key-file attest.key --signature-out hooks.conformance",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			eng, err := resolveHookHardening()
			if err != nil {
				return err
			}
			policy, err := pf.resolve()
			if err != nil {
				return err
			}
			key, err := readSigningKeyFile(signingKey)
			if err != nil {
				return err
			}
			certJSON, blob, err := eng.Conform(cmd.Context(), policy, behavioral, key, time.Now().UTC())
			if err != nil {
				return err
			}
			_, _ = cmd.OutOrStdout().Write(append(certJSON, '\n'))
			return emitSignature(cmd, blob, signatureOut)
		},
	}
	pf.bind(cmd)
	cmd.Flags().BoolVar(&behavioral, "behavioral", false, "also run the behavioral hook-deny e2e against a mock model (drives the real binary twice; no creds needed)")
	cmd.Flags().StringVar(&signingKey, "signing-key-file", "", "file holding the base64 ed25519 private key to sign the certificate (optional)")
	cmd.Flags().StringVar(&signatureOut, "signature-out", "", "write the signed cert blob to this file (default: print to stderr when signed)")
	return cmd
}

// readSigningKeyFile reads the base64 ed25519 private key from a file (trimmed). An empty path
// yields an empty key (unsigned output). The key is never logged.
func readSigningKeyFile(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read signing key: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

// emitSignature writes the signed blob to a file (when --signature-out is set) or to stderr.
func emitSignature(cmd *cobra.Command, blob, out string) error {
	if blob == "" {
		return nil
	}
	if strings.TrimSpace(out) != "" {
		return os.WriteFile(out, []byte(blob+"\n"), 0o600)
	}
	fmt.Fprintln(cmd.ErrOrStderr(), "signed-blob: "+blob)
	return nil
}
