// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// cmd_evals.go is the CI-facing CLI of the eval-methodology surface
// (docs/EVAL-METHODOLOGY.md):
//
//   - `evals gate`  — run (or re-check) a regression gate against a running control
//     plane and map the EFFECTIVE verdict onto the exit code: pass→0, warn→0 with a
//     loud warning (the decision-3 honest degradation), fail→1 (the merge blocks).
//     After a governed override, CI re-checks with --check-id and sees the pass.
//   - `evals label` — the guided human-labeling loop that builds the judge↔human
//     calibration set: it walks a JSONL of candidate items, asks for the human
//     verdict per item, and posts each label IMMEDIATELY (a long session never
//     loses work). Already-labeled case keys are skipped on resume.
//
// Both are thin HTTP clients (Bearer token + tenant header) — all semantics live
// server-side in modules/evals.

// evalsClientConfig is the shared server/credential flag set.
type evalsClientConfig struct {
	server   string
	token    string
	tenant   string
	caCert   string
	pins     []string
	insecure bool
	timeout  time.Duration
}

func (c *evalsClientConfig) addFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&c.server, "server", "", "control-plane base URL (default $OLIVARES_SERVER_URL)")
	cmd.Flags().StringVar(&c.token, "token", "", "API bearer token (default $OLIVARES_TOKEN)")
	cmd.Flags().StringVar(&c.tenant, "tenant", "", "tenant id (default $OLIVARES_TENANT)")
	// ⛔ EL MISMO DEFECTO QUE cmd_hookpep.go, con el MISMO comentario copiado abajo: la llamada al
	// transporte compartido se duplicó y las banderas no se ataron en ninguno de los dos. Medido el
	// 2026-08-19 recorriendo el CLI contra un plano vivo: contra un certificado autofirmado —el que
	// genera nuestro propio quickstart, que imprime su pin— la única salida era `--insecure`.
	cmd.Flags().StringVar(&c.caCert, "ca-cert", "", "PEM CA bundle used to verify the control plane")
	cmd.Flags().StringArrayVar(&c.pins, "pin-sha256", nil, "pinned leaf SPKI SHA-256, base64 or hex (repeatable) — the engine prints it as pin_sha256 on the line reporting its certificate")
	cmd.Flags().BoolVar(&c.insecure, "insecure", false, "skip TLS certificate verification (self-signed dev planes only)")
	cmd.Flags().DurationVar(&c.timeout, "timeout", 10*time.Minute, "request timeout (a judged gate can take a while)")
}

func (c *evalsClientConfig) resolve() error {
	c.server = strings.TrimRight(firstNonEmptyEnv(c.server, "OLIVARES_SERVER_URL"), "/")
	c.token = firstNonEmptyEnv(c.token, "OLIVARES_TOKEN")
	c.tenant = firstNonEmptyEnv(c.tenant, "OLIVARES_TENANT")
	if c.server == "" {
		return fmt.Errorf("no server: set --server or OLIVARES_SERVER_URL")
	}
	if c.token == "" {
		return fmt.Errorf("no token: set --token or OLIVARES_TOKEN")
	}
	if c.tenant == "" {
		return fmt.Errorf("no tenant: set --tenant or OLIVARES_TENANT")
	}
	return nil
}

// do performs one JSON request against the plane.
func (c *evalsClientConfig) do(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.server+path, rdr)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-Olivares-Tenant", c.tenant)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// E4: the shared transport, so --ca-cert, --pin-sha256 and the
	// --insecure warning exist here too, and a dead plane exits 6 not 1.
	client, _, err := cliTransport(cliTransportOptions{
		Resolved: cliResolvedConfig{Server: c.server, Token: c.token, Tenant: c.tenant, CACert: c.caCert, PinSHA256: c.pins},
		Insecure: c.insecure, Timeout: c.timeout,
	})
	if err != nil {
		return 0, nil, err
	}
	resp, err := cliDo(client, req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return resp.StatusCode, b, err
}

func newEvalsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "evals",
		Short: "Eval methodology tools: the CI regression gate and the judge-calibration labeler",
		Long: "evals holds the two halves of the eval methodology that a CI pipeline needs: the\n" +
			"regression gate that decides whether a candidate's outputs may ship, and the\n" +
			"guided labeling session that keeps an automated judge calibrated against human\n" +
			"judgement.\n\n" +
			"gate is the machine-facing verb — it exits non-zero on a fail so a pipeline can\n" +
			"branch on it; label is the human-facing one.",
		Example: "  olivares evals gate --suite suite-123 --subject agent-v2 --outputs outputs.json\n" +
			"  olivares evals label --set safety-v1 --in calibration-candidates.jsonl",
	}
	cmd.AddCommand(newEvalsGateCmd(), newEvalsLabelCmd())
	return cmd
}

// errGateFailed maps a failing gate onto exit code 1 without cobra noise.
var errGateFailed = fmt.Errorf("gate failed")

func newEvalsGateCmd() *cobra.Command {
	var (
		cfg         evalsClientConfig
		suite       string
		subject     string
		subjectKind string
		baseline    string
		outputsPath string
		seed        string
		sampleSize  int
		checkID     string
	)
	cmd := &cobra.Command{
		Use:   "gate",
		Short: "Run the CI regression gate (exit 0 pass/warn, 1 fail) or re-check one after a governed override",
		Long: "evals gate scores candidate outputs against a golden suite on the control plane and\n" +
			"maps the gate's EFFECTIVE verdict onto the exit code a CI pipeline blocks on:\n" +
			"  pass → 0    warn → 0 (printed loudly: declared degradation, e.g. no judge credential)\n" +
			"  fail → 1    (regression vs baseline, pass-rate below threshold, uncalibrated judge, budget block)\n\n" +
			"A failed gate is overridable ONLY via the governed override (admin + written reason, audited):\n" +
			"  POST /v1/m/evals/gate/{id}/override — then re-run this command with --check-id {id}.\n\n" +
			"--outputs is a JSON object file mapping case_key → candidate output ('-' reads stdin).",
		Example: `  # Run a regression gate from a candidate-output map
  olivares evals gate --suite suite-123 --subject agent-v2 --outputs outputs.json

  # Re-check a gate after a governed override
  olivares evals gate --check-id gate-123`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			ctx := cmd.Context()
			var status int
			var body []byte
			var err error
			if checkID != "" {
				status, body, err = cfg.do(ctx, "GET", "/v1/m/evals/gate/"+checkID, nil)
			} else {
				if suite == "" {
					return fmt.Errorf("--suite is required (or --check-id to re-check an existing gate)")
				}
				outputs, oerr := readOutputsFile(cmd.InOrStdin(), outputsPath)
				if oerr != nil {
					return oerr
				}
				reqBody := map[string]any{
					"suite_ref": suite, "subject_kind": subjectKind, "subject_ref": subject,
					"baseline_ref": baseline, "outputs": outputs, "seed": seed, "sample_size": sampleSize,
				}
				status, body, err = cfg.do(ctx, "POST", "/v1/m/evals/gate", reqBody)
			}
			if err != nil {
				return err
			}
			if status != http.StatusOK && status != http.StatusCreated {
				return fmt.Errorf("gate request failed: HTTP %d: %s", status, strings.TrimSpace(string(body)))
			}
			var gate struct {
				ID               string   `json:"id"`
				Verdict          string   `json:"verdict"`
				EffectiveVerdict string   `json:"effective_verdict"`
				Reasons          []string `json:"reasons"`
				Overridden       bool     `json:"overridden"`
				Sampled          int64    `json:"sampled"`
				TotalCases       int64    `json:"total_cases"`
				CacheHits        int      `json:"cache_hits"`
			}
			if err := json.Unmarshal(body, &gate); err != nil {
				return fmt.Errorf("unparseable gate response: %w", err)
			}
			if err := renderOut(cmd, func(out io.Writer) error {
				if _, err := fmt.Fprintf(out, "gate %s: verdict=%s effective=%s sampled=%d/%d cache_hits=%d reasons=%s\n",
					gate.ID, gate.Verdict, gate.EffectiveVerdict, gate.Sampled, gate.TotalCases, gate.CacheHits,
					strings.Join(gate.Reasons, ",")); err != nil {
					return err
				}
				switch gate.EffectiveVerdict {
				case "pass":
					if gate.Overridden {
						_, err := fmt.Fprintln(out, "NOTE: this gate FAILED and was overridden by a governed decision (audited).")
						return err
					}
				case "warn":
					_, err := fmt.Fprintln(out, "WARNING: gate degraded (see reasons) — not blocking, but NOT a judged pass.")
					return err
				default:
					_, err := fmt.Fprintln(out, "gate FAILED — merge blocked. A governed override (admin + reason) can unblock: evals gate --check-id "+gate.ID)
					return err
				}
				return nil
			}, json.RawMessage(body)); err != nil {
				return err
			}
			switch gate.EffectiveVerdict {
			case "pass":
				return nil
			case "warn":
				return nil
			default:
				return errGateFailed
			}
		},
	}
	cfg.addFlags(cmd)
	cmd.Flags().StringVar(&suite, "suite", "", "suite id to gate against")
	cmd.Flags().StringVar(&subject, "subject", "", "subject ref (e.g. the agent/model under test)")
	cmd.Flags().StringVar(&subjectKind, "subject-kind", "", "subject kind (defaults to the suite's)")
	cmd.Flags().StringVar(&baseline, "baseline", "", "explicit baseline run id (default: pinned baseline or latest prior run)")
	cmd.Flags().StringVar(&outputsPath, "outputs", "", "JSON file mapping case_key → candidate output ('-' = stdin)")
	cmd.Flags().StringVar(&seed, "seed", "", "deterministic sample seed (default: derived from the suite version)")
	cmd.Flags().IntVar(&sampleSize, "sample-size", 0, "judge at most N cases (deterministic subset; 0 = all)")
	cmd.Flags().StringVar(&checkID, "check-id", "", "re-check an existing gate id (after a governed override)")
	addDeprecatedJSONFlag(cmd)
	return cmd
}

// readOutputsFile loads the case_key→output JSON object ('-' = stdin).
func readOutputsFile(stdin io.Reader, path string) (map[string]string, error) {
	if path == "" {
		return nil, fmt.Errorf("--outputs is required (a JSON object mapping case_key → output)")
	}
	var b []byte
	var err error
	if path == "-" {
		b, err = io.ReadAll(io.LimitReader(stdin, 8<<20))
	} else {
		b, err = os.ReadFile(path) //nolint:gosec // operator-provided outputs path
	}
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("outputs file is not a JSON object of case_key → output: %w", err)
	}
	return out, nil
}

// labelCandidate is one JSONL line of the labeling input.
type labelCandidate struct {
	CaseKey   string `json:"case_key"`
	Input     string `json:"input,omitempty"`
	Output    string `json:"output"`
	Expected  string `json:"expected,omitempty"`
	Criterion string `json:"criterion,omitempty"`
	Notes     string `json:"notes,omitempty"`
}

func newEvalsLabelCmd() *cobra.Command {
	var (
		cfg       evalsClientConfig
		set       string
		inPath    string
		criterion string
	)
	cmd := &cobra.Command{
		Use:   "label",
		Short: "Guided human-labeling session for the judge↔human calibration set",
		Long: "evals label walks a JSONL file of candidate items (one JSON object per line:\n" +
			"  {\"case_key\":..., \"input\":..., \"output\":..., \"expected\":..., \"criterion\":..., \"notes\":...})\n" +
			"and asks for the HUMAN verdict per item: [p]ass, [f]ail, [s]kip, [q]uit. Each label is\n" +
			"posted immediately (a long session never loses work) and case keys already labeled in the\n" +
			"set are skipped, so the session is resumable. The calibration set should contain BOTH pass\n" +
			"and fail labels — an all-pass set cannot measure chance-corrected agreement (kappa) and\n" +
			"will never certify the judge. Aim for ~100-150 items (docs/EVAL-METHODOLOGY.md §2).",
		Example: "  olivares evals label --set safety-v1 --in calibration-candidates.jsonl",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			if inPath == "" {
				return fmt.Errorf("--in is required (a JSONL file of candidate items)")
			}
			f, err := os.Open(inPath) //nolint:gosec // operator-provided candidates path
			if err != nil {
				return err
			}
			defer func() { _ = f.Close() }()
			return runLabelSession(cmd.Context(), &cfg, cmd.InOrStdin(), cmd.OutOrStdout(), f, set, criterion)
		},
	}
	cfg.addFlags(cmd)
	cmd.Flags().StringVar(&set, "set", "default", "calibration set name")
	cmd.Flags().StringVar(&inPath, "in", "", "JSONL file of candidate items to label")
	cmd.Flags().StringVar(&criterion, "criterion", "", "default criterion for items that carry none")
	return cmd
}

// runLabelSession drives the interactive loop. Split from the cobra wiring so the
// whole flow (resume, prompt, immediate post, summary) is testable with fake stdin.
func runLabelSession(ctx context.Context, cfg *evalsClientConfig, stdin io.Reader, out io.Writer, candidates io.Reader, set, defaultCriterion string) error {
	already, err := labeledCaseKeys(ctx, cfg, set)
	if err != nil {
		return err
	}
	answers := bufio.NewScanner(stdin)
	scan := bufio.NewScanner(candidates)
	scan.Buffer(make([]byte, 0, 64*1024), 1<<20)
	var line, labeled, skipped, resumed int
	for scan.Scan() {
		line++
		text := strings.TrimSpace(scan.Text())
		if text == "" {
			continue
		}
		var cand labelCandidate
		if err := json.Unmarshal([]byte(text), &cand); err != nil {
			fmt.Fprintf(out, "line %d: bad JSONL item, skipping (%v)\n", line, err)
			continue
		}
		if cand.CaseKey == "" || cand.Output == "" {
			fmt.Fprintf(out, "line %d: item needs case_key and output, skipping\n", line)
			continue
		}
		if already[cand.CaseKey] {
			resumed++
			continue
		}
		if cand.Criterion == "" {
			cand.Criterion = defaultCriterion
		}

		fmt.Fprintf(out, "\n[%s] case %s\n", set, cand.CaseKey)
		if cand.Criterion != "" {
			fmt.Fprintf(out, "  CRITERION: %s\n", cand.Criterion)
		}
		if cand.Input != "" {
			fmt.Fprintf(out, "  INPUT:     %s\n", cand.Input)
		}
		if cand.Expected != "" {
			fmt.Fprintf(out, "  EXPECTED:  %s\n", cand.Expected)
		}
		fmt.Fprintf(out, "  OUTPUT:    %s\n", cand.Output)

		verdict, quit := askVerdict(answers, out)
		if quit {
			break
		}
		if verdict == "" {
			skipped++
			continue
		}
		item := map[string]any{
			"case_key": cand.CaseKey, "input": cand.Input, "output": cand.Output,
			"expected": cand.Expected, "criterion": cand.Criterion, "notes": cand.Notes,
			"human_passed": verdict == "p",
		}
		status, body, err := cfg.do(ctx, "POST", "/v1/m/evals/calibration/items",
			map[string]any{"set_name": set, "items": []any{item}})
		if err != nil {
			return fmt.Errorf("posting label for %s: %w", cand.CaseKey, err)
		}
		if status != http.StatusCreated {
			return fmt.Errorf("posting label for %s: HTTP %d: %s", cand.CaseKey, status, strings.TrimSpace(string(body)))
		}
		labeled++
		fmt.Fprintf(out, "  saved (%d labeled so far)\n", labeled)
	}
	if err := scan.Err(); err != nil {
		return err
	}
	fmt.Fprintf(out, "\nsession done: %d labeled, %d skipped, %d already labeled (resumed)\n", labeled, skipped, resumed)
	fmt.Fprintf(out, "next: POST /v1/m/evals/calibration/run {\"set_name\":%q,\"judge_model\":\"<pin>\"} to measure the judge\n", set)
	return nil
}

// askVerdict prompts until it gets p/f/s/q. quit=true on q or exhausted input;
// verdict "" means skip.
func askVerdict(answers *bufio.Scanner, out io.Writer) (verdict string, quit bool) {
	for {
		fmt.Fprint(out, "  human verdict — [p]ass / [f]ail / [s]kip / [q]uit: ")
		if !answers.Scan() {
			return "", true
		}
		switch strings.ToLower(strings.TrimSpace(answers.Text())) {
		case "p", "pass":
			return "p", false
		case "f", "fail":
			return "f", false
		case "s", "skip":
			return "", false
		case "q", "quit":
			return "", true
		}
	}
}

// labeledCaseKeys returns the case keys already labeled in the set (resume support).
func labeledCaseKeys(ctx context.Context, cfg *evalsClientConfig, set string) (map[string]bool, error) {
	keys := map[string]bool{}
	cursor := ""
	for {
		path := "/v1/m/evals/calibration/items?set=" + set
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		status, body, err := cfg.do(ctx, "GET", path, nil)
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("listing labeled items: HTTP %d: %s", status, strings.TrimSpace(string(body)))
		}
		var page struct {
			Items []struct {
				CaseKey string `json:"case_key"`
			} `json:"items"`
			Cursor  string `json:"cursor"`
			HasMore bool   `json:"has_more"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, err
		}
		for _, it := range page.Items {
			keys[it.CaseKey] = true
		}
		if !page.HasMore || page.Cursor == "" {
			return keys, nil
		}
		cursor = page.Cursor
	}
}
