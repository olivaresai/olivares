// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/core/content/richdoc"
	"github.com/olivaresai/olivares/core/runtime/plugjail"
)

// Rich-document extraction is a two-part mechanism that keeps hostile Office bytes
// away from the engine's secrets:
//
//   - the `__extract` HIDDEN subcommand is the extractor itself: it reads a document
//     from stdin, extracts text with the stdlib-only richdoc parser, and writes the
//     result to stdout. It touches no engine state and no config;
//   - sandboxedRichDocExtractor is the in-engine implementation of
//     contentsource.RichDocExtractor: it re-execs THIS binary as `__extract` under
//     plugin confinement (plugjail — env-scoped so the child inherits none of the
//     engine's connector secrets / KMS keys, plus the cgroup ceilings and per-launch
//     uid the platform allows) and streams the bytes in / text out over pipes.
//
// A content connector (fscontent) receives the extractor by injection and never
// builds one — the connectors module (Apache) must not import the engine (AGPL); the
// sandbox lives here, on the engine side of the license boundary.

const (
	// extractSkipExitCode is the exit code the `__extract` child uses to signal a
	// CLASSIFIED not-extractable input (not real OOXML, malformed, or over-limit),
	// which the parent maps to contentsource.ErrNotExtractable (an honest skip). Any
	// other non-zero exit is an unexpected operational failure.
	extractSkipExitCode = 3
	// extractTimeout bounds the whole out-of-process extraction (wall clock).
	extractTimeout = 30 * time.Second
	// extractFrameOverhead is the parent's read headroom for the child's "subtype\n"
	// framing prefix, so a text extraction exactly at MaxOutputBytes is not misread as
	// over-cap. The subtype is a short fixed enum ("docx"/"pptx"/"xlsx"); 16 is slack.
	extractFrameOverhead = 16
)

// newExtractCmd builds the hidden `__extract` subcommand. It is Hidden so it never
// appears in help/completion — it is an internal re-exec target, not a user command,
// though running it by hand is harmless (it only parses the file on its stdin).
func newExtractCmd() *cobra.Command {
	var kind string
	cmd := &cobra.Command{
		Use:    "__extract",
		Hidden: true,
		Args:   cobra.NoArgs,
		Short:  "internal: extract text from a rich document on stdin (sandboxed re-exec target)",
		Long: "__extract is an internal, hidden re-exec target — NOT a user command. The engine\n" +
			"launches it under plugin confinement to extract plain text from an Office Open XML\n" +
			"document (DOCX/PPTX/XLSX) read from stdin, writing \"<subtype>\\n<text>\" to stdout. It\n" +
			"reads no config and touches no engine state, so running it by hand only parses the\n" +
			"file on its stdin; a classified non-extractable input exits with a skip code.",
		Example: "  # internal use only — the engine invokes this under confinement per document\n" +
			"  olivares __extract --kind ooxml < report.docx",
		RunE: func(cmd *cobra.Command, _ []string) error {
			code, err := extractOnce(cmd.InOrStdin(), cmd.OutOrStdout(), kind)
			if err != nil {
				return err
			}
			if code != 0 {
				os.Exit(code) // classified skip: leaf subprocess, no defers to run
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "rich-document kind (ooxml)")
	return cmd
}

// extractOnce is the child's pure core: read the document from in (bounded), extract,
// and write "subtype\n" + text to out. It returns (exitCode, err): a non-zero
// exitCode is a CLASSIFIED not-extractable input (unsupported kind, over the input
// cap, not real OOXML, malformed, or over-limit) that the caller turns into
// extractSkipExitCode; err is only an unexpected operational failure. Keeping the
// os.Exit out of here makes every classified path unit-testable in-process.
func extractOnce(in io.Reader, out io.Writer, kind string) (int, error) {
	lim := richdoc.DefaultLimits()
	if kind != string(contentsource.RichDocOOXML) {
		return extractSkipExitCode, nil // unsupported kind (e.g. pdf)
	}
	raw, err := io.ReadAll(io.LimitReader(in, lim.MaxInputBytes+1))
	if err != nil {
		return 0, fmt.Errorf("read stdin: %w", err)
	}
	if int64(len(raw)) > lim.MaxInputBytes {
		return extractSkipExitCode, nil // over the input cap — skip, never parse partial
	}
	res, err := richdoc.ExtractOOXML(raw, lim)
	if err != nil {
		if errors.Is(err, richdoc.ErrNotOOXML) || errors.Is(err, richdoc.ErrMalformed) || errors.Is(err, richdoc.ErrTooLarge) {
			return extractSkipExitCode, nil // classified: not extractable → skip
		}
		return 0, fmt.Errorf("extract: %w", err) // unexpected internal failure
	}
	if _, err := io.WriteString(out, string(res.Subtype)+"\n"); err != nil {
		return 0, err
	}
	if _, err := io.WriteString(out, res.Text); err != nil {
		return 0, err
	}
	return 0, nil
}

// sandboxedRichDocExtractor implements contentsource.RichDocExtractor by re-execing
// the engine binary as the confined `__extract` helper. It is stateless (one instance
// is shared across sources) — each Extract spawns and reaps a fresh subprocess.
type sandboxedRichDocExtractor struct {
	log *slog.Logger
	// exePath is the engine binary to re-exec; "" resolves os.Executable() at call
	// time (production). A test injects a freshly built `olivares` binary here because
	// plugjail env-scopes the child, so the test-binary trampoline cannot survive.
	exePath string
}

// newSandboxedRichDocExtractor returns the engine's rich-document extractor. log may
// be nil (the attestation debug line is then skipped).
func newSandboxedRichDocExtractor(log *slog.Logger) *sandboxedRichDocExtractor {
	return &sandboxedRichDocExtractor{log: log}
}

var _ contentsource.RichDocExtractor = (*sandboxedRichDocExtractor)(nil)

// Extract runs the sandboxed `__extract` subprocess over raw and returns the text and
// resolved subtype. A classified not-extractable input yields contentsource.
// ErrNotExtractable (a skip); an unexpected operational failure yields a wrapped
// error. raw is streamed to the child and not retained.
func (e *sandboxedRichDocExtractor) Extract(ctx context.Context, kind contentsource.RichDocKind, raw []byte) (string, string, error) {
	if kind != contentsource.RichDocOOXML {
		return "", "", contentsource.ErrNotExtractable // PDF/other not supported this release
	}
	exe := e.exePath
	if exe == "" {
		var err error
		exe, err = os.Executable()
		if err != nil {
			return "", "", fmt.Errorf("richdoc: resolve executable: %w", err)
		}
	}

	cctx, cancel := context.WithTimeout(ctx, extractTimeout)
	defer cancel()

	// #nosec G204 -- exe is os.Executable() (this same engine binary); the only args
	// are the fixed "__extract" subcommand and a constant kind flag. No user input
	// reaches argv (the document travels over stdin), and the child is confined below.
	cmd := exec.CommandContext(cctx, exe, "__extract", "--kind", string(kind))
	att, cleanup, jerr := plugjail.Apply(cmd, plugjail.Default("richdoc-extract"))
	if jerr != nil {
		return "", "", fmt.Errorf("richdoc: confine extractor: %w", jerr)
	}
	defer cleanup()
	if e.log != nil {
		// Log the degraded controls too, so a "minimal"/"partial" launch explains WHY
		// (e.g. non-root engine → the child shares the engine uid; see the threat model).
		e.log.Debug("richdoc extractor launched under confinement",
			"level", att.Level, "env_scoped", att.EnvScoped, "dedicated_uid", att.DedicatedUID,
			"cgroup", att.Cgroup, "platform", att.Platform, "degraded", att.Degraded)
	}

	cmd.Stdin = bytes.NewReader(raw)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", "", fmt.Errorf("richdoc: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		plugjail.CloseSpawnFD(cmd) // the spawn fd is dead weight once Start failed — don't leak it
		return "", "", fmt.Errorf("richdoc: start extractor: %w", err)
	}
	plugjail.CloseSpawnFD(cmd)

	// The child frames its output as "subtype\n" + text. Read with headroom for that
	// frame so a legitimate at-cap extraction (text == MaxOutputBytes) is NOT mistaken
	// for over-cap by the header bytes; the real over-cap guard is on the TEXT length
	// below (defense in depth on top of the child's own MaxOutputBytes cap).
	maxOut := richdoc.DefaultLimits().MaxOutputBytes
	readCap := int64(maxOut) + extractFrameOverhead + 1
	out, readErr := io.ReadAll(io.LimitReader(stdout, readCap))
	waitErr := cmd.Wait()
	if waitErr != nil {
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) && ee.ExitCode() == extractSkipExitCode {
			return "", "", contentsource.ErrNotExtractable // classified skip
		}
		return "", "", fmt.Errorf("richdoc: extractor failed: %w (stderr: %s)", waitErr, strings.TrimSpace(stderr.String()))
	}
	if readErr != nil {
		return "", "", fmt.Errorf("richdoc: read output: %w", readErr)
	}
	subtype, text := splitExtractOutput(out)
	if len(text) > maxOut {
		// The child exceeded its own text cap → treat as a skip, never truncate-and-ingest.
		return "", "", contentsource.ErrNotExtractable
	}
	return text, subtype, nil
}

// splitExtractOutput parses the child's "subtype\n<text>" framing. A missing newline
// (a truncated/empty child write) yields an empty result, handled as no text.
func splitExtractOutput(out []byte) (subtype, text string) {
	i := bytes.IndexByte(out, '\n')
	if i < 0 {
		return "", ""
	}
	return string(out[:i]), string(out[i+1:])
}
