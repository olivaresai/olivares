// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

const (
	securityDrillAdvisoryID      = "OLIVARES-DRILL-0001"
	securityDrillIntroduced      = "26.5.0"
	securityDrillFixed           = "26.7.1"
	securityDrillBelowIntroduced = "26.4.9"
	securityDrillKeyContext      = "olivares.ai/secadvisory drill fixture key v1 — TEST ONLY, never trust outside a drill"
	securityDrillWrongKeyContext = "olivares.ai/secadvisory drill fixture wrong key v1 — TEST ONLY, never trust outside a drill"
)

//go:embed fixtures/security-drill/draft-advisories.json
var securityDrillEmbeddedDraft []byte

// securityDrillCmd proves the PSIRT advisory path through the real CLI, end to end:
// producer, signed feed, offline verification, both range boundaries, and signature
// refusal after tampering or under an untrusted key. Every subprocess is this same
// executable, so the drill exercises the shipped command wiring rather than helpers.
func securityDrillCmd() *cobra.Command {
	var draftPath string
	var keepArtifacts bool
	cmd := &cobra.Command{
		Use:   "drill",
		Short: "Timed end-to-end PSIRT advisory-pipeline drill",
		Long: strings.TrimSpace(`
Prove the PSIRT advisory pipeline end to end in a disposable scratch directory: build
and sign the fixture feed, verify affected/patched/range-boundary versions offline, then
prove tampered content and an untrusted key are refused. The key is deterministic and
TEST-ONLY; the drill touches no real data and is safe for CI (docs/PSIRT-RUNBOOK.md §7).`),
		Example: "  olivares security drill",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSecurityDrill(cmd, draftPath, keepArtifacts)
		},
	}
	cmd.Flags().StringVar(&draftPath, "draft", "", "override the embedded advisory draft fixture")
	cmd.Flags().BoolVar(&keepArtifacts, "keep-artifacts", false, "keep the scratch dir instead of removing it (debugging)")
	return cmd
}

// securityDrillResult is the -o json pane of `security drill`: the PASS verdict
// plus the per-step wall clock, which is the fact the drill exists to produce.
// docs/PSIRT-RUNBOOK.md §7 asks for a rehearsed advisory path and a MEASURED
// end-to-end time; a CI job can gate on `duration_ms` and on the step names
// without parsing "ok tamper 41ms".
//
// It covers the PASSING desenlace only, and that is deliberate. A drill that
// fails hands back a subprocess transcript, and the operator needs those bytes,
// not a document: `securityDrillStepFailure` keeps printing them (to stderr when
// -o json is selected, so stdout stays one document or nothing) and the command
// still exits non-zero. Handing a failed drill back as a well-formed JSON object
// is how a `set -e` script comes to treat it as an answer.
type securityDrillResult struct {
	Passed bool                `json:"passed"`
	Steps  []securityDrillStep `json:"steps"`
	// DurationMS is the same measurement the last prose line reports, in
	// milliseconds — a number, because a consumer that compares drill times must
	// not have to parse Go's "1.234s" / "987ms" duration spelling.
	DurationMS int64 `json:"duration_ms"`
	// Artifacts is the kept scratch directory under --keep-artifacts, else "".
	// The text pane prints that path FIRST, before any step can fail, so the
	// document repeats it rather than replacing it.
	Artifacts string `json:"artifacts"`
}

// securityDrillStep is one drill step and its measured duration.
type securityDrillStep struct {
	Name       string `json:"name"`
	DurationMS int64  `json:"duration_ms"`
}

func runSecurityDrill(cmd *cobra.Command, draftOverride string, keepArtifacts bool) error {
	// The step lines are PROGRESS: they are printed as each step passes, before a
	// later step can still fail. They keep going to stdout for the text pane and
	// move to stderr under -o json, so the document on stdout stays parseable
	// without the operator losing the running commentary or, on a failure, the
	// subprocess transcript that is the whole diagnostic.
	w := progressStream(cmd)
	totalStart := time.Now()
	steps := make([]securityDrillStep, 0, 6)
	runStep := func(name string, args []string, assert func(int, string) error) error {
		step, err := runSecurityDrillStep(w, name, args, assert)
		if err != nil {
			return err
		}
		steps = append(steps, step)
		return nil
	}
	root, err := os.MkdirTemp("", "olivares-security-drill-")
	if err != nil {
		return fmt.Errorf("security drill scratch dir: %w", err)
	}
	if keepArtifacts {
		fmt.Fprintf(w, "drill artifacts kept in %s\n", root)
	} else {
		defer func() { _ = os.RemoveAll(root) }()
	}

	draftBytes := securityDrillEmbeddedDraft
	draftPath := filepath.Join(root, "draft-advisories.json")
	if strings.TrimSpace(draftOverride) != "" {
		draftPath = draftOverride
		draftBytes, err = os.ReadFile(draftPath)
		if err != nil {
			return securityDrillGuardFailure(w, fmt.Errorf("read draft %s: %w", draftPath, err))
		}
	}
	if err := validateSecurityDrillDraft(draftBytes); err != nil {
		return securityDrillGuardFailure(w, err)
	}
	if strings.TrimSpace(draftOverride) == "" {
		if err := os.WriteFile(draftPath, draftBytes, 0o644); err != nil {
			return securityDrillGuardFailure(w, fmt.Errorf("materialize embedded draft: %w", err))
		}
	}

	seed := sha256.Sum256([]byte(securityDrillKeyContext))
	privateKey := ed25519.NewKeyFromSeed(seed[:])
	publicKey := privateKey.Public().(ed25519.PublicKey)
	wrongSeed := sha256.Sum256([]byte(securityDrillWrongKeyContext))
	wrongPrivateKey := ed25519.NewKeyFromSeed(wrongSeed[:])
	wrongPublicKey := wrongPrivateKey.Public().(ed25519.PublicKey)

	keyPath := filepath.Join(root, "drill-signing-seed.b64")
	keyFile := base64.StdEncoding.EncodeToString(seed[:]) + "\n"
	if err := os.WriteFile(keyPath, []byte(keyFile), 0o600); err != nil {
		return securityDrillGuardFailure(w, fmt.Errorf("write deterministic drill key: %w", err))
	}
	publicKeyB64 := base64.StdEncoding.EncodeToString(publicKey)
	wrongPublicKeyB64 := base64.StdEncoding.EncodeToString(wrongPublicKey)
	feedPath := filepath.Join(root, "advisories.json")
	sigPath := feedPath + ".sig"

	if err := runStep("produce", []string{
		// El ancla que el receptor fija: sin declararla, el productor no puede distinguir un
		// seed de una clave publica (los dos miden 32 bytes) y firmaria con un par derivado.
		"security", "advisories", "--in", draftPath, "--out", feedPath, "--sign-key", "@" + keyPath,
		"--expect-pubkey", publicKeyB64,
	}, func(code int, _ string) error {
		if code != 0 {
			return fmt.Errorf("exit code %d, want 0", code)
		}
		if _, err := os.Stat(feedPath); err != nil {
			return fmt.Errorf("produced feed missing: %w", err)
		}
		if _, err := os.Stat(sigPath); err != nil {
			return fmt.Errorf("detached signature missing: %w", err)
		}
		return nil
	}); err != nil {
		return err
	}

	if err := runStep("affected", securityDrillCheckArgs(feedPath, "", publicKeyB64, securityDrillIntroduced), func(code int, output string) error {
		// Exit contract: "affected" is a degraded condition (report on
		// stdout, exit 7), no longer the generic 1.
		if code != exitcode.Degraded {
			return fmt.Errorf("exit code %d, want %d (degraded)", code, exitcode.Degraded)
		}
		for _, want := range []string{"AFFECTED", securityDrillAdvisoryID, "fixed in " + securityDrillFixed} {
			if !strings.Contains(output, want) {
				return fmt.Errorf("output does not contain %q", want)
			}
		}
		return nil
	}); err != nil {
		return err
	}

	if err := runStep("patched", securityDrillCheckArgs(feedPath, "", publicKeyB64, securityDrillFixed), func(code int, output string) error {
		if code != 0 {
			return fmt.Errorf("exit code %d, want 0", code)
		}
		if !strings.Contains(output, "no known advisory") {
			return fmt.Errorf("output does not contain %q", "no known advisory")
		}
		return nil
	}); err != nil {
		return err
	}

	if err := runStep("below-introduced", securityDrillCheckArgs(feedPath, "", publicKeyB64, securityDrillBelowIntroduced), func(code int, _ string) error {
		if code != 0 {
			return fmt.Errorf("exit code %d, want 0", code)
		}
		return nil
	}); err != nil {
		return err
	}

	tamperedPath := filepath.Join(root, "advisories-tampered.json")
	feedBytes, err := os.ReadFile(feedPath)
	if err != nil {
		return securityDrillStepFailure(w, "tamper", nil, fmt.Errorf("read produced feed: %w", err))
	}
	if len(feedBytes) == 0 {
		return securityDrillStepFailure(w, "tamper", nil, errors.New("produced feed is empty"))
	}
	tamperedBytes := append([]byte(nil), feedBytes...)
	tamperedBytes[len(tamperedBytes)/2] ^= 0x01
	if err := os.WriteFile(tamperedPath, tamperedBytes, 0o644); err != nil {
		return securityDrillStepFailure(w, "tamper", nil, fmt.Errorf("write tampered feed: %w", err))
	}
	if err := runStep("tamper", securityDrillCheckArgs(tamperedPath, sigPath, publicKeyB64, securityDrillIntroduced), securityDrillRefusalAssertion); err != nil {
		return err
	}

	if err := runStep("wrong-key", securityDrillCheckArgs(feedPath, "", wrongPublicKeyB64, securityDrillIntroduced), securityDrillRefusalAssertion); err != nil {
		return err
	}

	// ONE measurement for both panes. Taking it here and formatting it twice is what
	// keeps the prose's "measured end-to-end time" and the document's `duration_ms`
	// from being two different readings of the same run.
	total := time.Since(totalStart)
	kept := ""
	if keepArtifacts {
		kept = root
	}
	return renderOut(cmd, func(out io.Writer) error {
		fmt.Fprintln(out, "security drill PASSED — advisory pipeline proven end to end")
		_, werr := fmt.Fprintf(out, "measured end-to-end time: %s\n", total.Round(time.Millisecond))
		return werr
	}, securityDrillResult{
		Passed:     true,
		Steps:      steps,
		DurationMS: total.Milliseconds(),
		Artifacts:  kept,
	})
}

func validateSecurityDrillDraft(raw []byte) error {
	var draft advisoryDraft
	if err := json.Unmarshal(raw, &draft); err != nil {
		return fmt.Errorf("parse draft: %w", err)
	}
	if len(draft.Advisories) != 1 {
		return fmt.Errorf("draft must contain exactly one advisory, got %d", len(draft.Advisories))
	}
	advisory := draft.Advisories[0]
	if advisory.ID != securityDrillAdvisoryID {
		return fmt.Errorf("draft advisory id is %q, want %q", advisory.ID, securityDrillAdvisoryID)
	}
	for _, affected := range advisory.Affected {
		if affected.Package.Ecosystem != "Go" || affected.Package.Name != productModule {
			continue
		}
		for _, advisoryRange := range affected.Ranges {
			if advisoryRange.Type != "SEMVER" || len(advisoryRange.Events) != 2 {
				continue
			}
			if advisoryRange.Events[0].Introduced == securityDrillIntroduced &&
				advisoryRange.Events[1].Fixed == securityDrillFixed {
				return nil
			}
		}
	}
	return fmt.Errorf("draft must target %s (Go/SEMVER) with introduced %s and fixed %s",
		productModule, securityDrillIntroduced, securityDrillFixed)
}

func securityDrillCheckArgs(feedPath, sigPath, publicKey, productVersion string) []string {
	args := []string{
		"security", "check", "--feed", feedPath, "--pubkey", publicKey,
		"--product-version", productVersion,
	}
	if sigPath != "" {
		args = append(args, "--sig", sigPath)
	}
	return args
}

func securityDrillRefusalAssertion(code int, output string) error {
	if code == 0 {
		return errors.New("exit code 0, want non-zero signature refusal")
	}
	if !strings.Contains(output, "did not verify") {
		return fmt.Errorf("output does not contain %q", "did not verify")
	}
	if strings.Contains(output, "AFFECTED") {
		return errors.New("unverified feed reached affected-version reporting")
	}
	return nil
}

// runSecurityDrillStep runs one step and returns its MEASUREMENT as well as its
// verdict. The duration was already being measured here and thrown away after
// printing; returning it is what lets the -o json pane report the same numbers
// the prose does, rather than a second timing taken somewhere else.
func runSecurityDrillStep(w io.Writer, name string, args []string, assert func(int, string) error) (securityDrillStep, error) {
	started := time.Now()
	output, code, err := runSecurityDrillSubprocess(args)
	if err != nil {
		return securityDrillStep{Name: name}, securityDrillStepFailure(w, name, output, err)
	}
	if err := assert(code, string(output)); err != nil {
		return securityDrillStep{Name: name}, securityDrillStepFailure(w, name, output, err)
	}
	elapsed := time.Since(started)
	fmt.Fprintf(w, "ok %s %s\n", name, elapsed.Round(time.Millisecond))
	return securityDrillStep{Name: name, DurationMS: elapsed.Milliseconds()}, nil
}

func runSecurityDrillSubprocess(args []string) ([]byte, int, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, -1, fmt.Errorf("resolve current executable: %w", err)
	}
	process := exec.Command(executable, args...) // #nosec G204 -- executable is os.Executable() (this same binary); args are fixed drill flags
	process.Env = os.Environ()
	output, err := process.CombinedOutput()
	if err == nil {
		return output, 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return output, exitErr.ExitCode(), nil
	}
	return output, -1, fmt.Errorf("execute %s: %w", strings.Join(args, " "), err)
}

func securityDrillGuardFailure(w io.Writer, cause error) error {
	fmt.Fprintf(w, "security drill FAILED at guard\n%s\n", cause)
	return fmt.Errorf("security drill guard failed: %w", cause)
}

func securityDrillStepFailure(w io.Writer, name string, output []byte, cause error) error {
	fmt.Fprintf(w, "security drill FAILED at %s\nsubprocess output:\n%s", name, output)
	if len(output) == 0 || output[len(output)-1] != '\n' {
		fmt.Fprintln(w)
	}
	return fmt.Errorf("security drill step %s failed: %w", name, cause)
}
