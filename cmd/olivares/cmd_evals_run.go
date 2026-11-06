// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/olivaresai/olivares/modules/evals"
	"github.com/spf13/cobra"
)

func newEvalsRunCmd() *cobra.Command {
	var cfg evalsClientConfig
	var suite, template string
	var trials int
	cmd := &cobra.Command{
		Use:   "run [folder] --suite S --template T --trials k",
		Short: "Run repeated governed sessions and gate on every trial passing",
		Long: "Run each golden case in a fresh git worktree using a saved session template.\n" +
			"The folder (default: current folder) must be a repository on the engine host.\n" +
			"Normal provider binding, approvals and audit apply. Each completed session is stopped.\n" +
			"Worktrees remain available for inspection with session show and removal with session rm.\n\n" +
			"Each trial batch uses the existing regression gate. The final verdict also requires\n" +
			"pass^k (the fraction of cases passing all k trials) to meet the suite threshold.\n" +
			"Missing, skipped or errored scores fail closed. --timeout bounds the whole run.",
		Example: "  olivares evals run --suite suite-123 --template tpl-456 --trials 3\n" +
			"  olivares evals run ./my-repo --suite suite-123 --template tpl-456 --trials 3",
		Args:        cobra.MaximumNArgs(1),
		Annotations: map[string]string{"changes_data": "write"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(suite) == "" || strings.TrimSpace(template) == "" || trials < 1 || trials > 100 || cfg.timeout <= 0 {
				return fmt.Errorf("--suite and --template are required; --trials must be 1–100 and --timeout positive")
			}
			if err := cfg.resolve(); err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), cfg.timeout)
			defer cancel()
			folder := "."
			if len(args) > 0 {
				folder = args[0]
			}
			path := "/v1/m/evals/suites/" + url.PathEscape(suite)
			_, raw, err := cfg.do(ctx, http.MethodGet, path, nil)
			if err != nil {
				return err
			}
			var spec struct {
				PassThreshold float64 `json:"pass_threshold"`
				Version       int64   `json:"suite_version"`
			}
			if err = json.Unmarshal(raw, &spec); err != nil {
				return err
			}
			if spec.Version < 1 || spec.PassThreshold < 0 || spec.PassThreshold > 1 {
				return fmt.Errorf("invalid eval suite")
			}
			_, raw, err = cfg.do(ctx, http.MethodGet, path+"/cases", nil)
			if err != nil {
				return err
			}
			var cases struct {
				Items []struct {
					Key     string `json:"case_key"`
					Input   string `json:"input"`
					Version int64  `json:"suite_version"`
				} `json:"items"`
			}
			if err = json.Unmarshal(raw, &cases); err != nil {
				return err
			}
			if len(cases.Items) == 0 {
				return fmt.Errorf("the suite has no cases")
			}
			keys := map[string]int{}
			for i, c := range cases.Items {
				if c.Key == "" || strings.TrimSpace(c.Input) == "" || c.Version != spec.Version {
					return fmt.Errorf("suite contains an invalid case")
				}
				if _, exists := keys[c.Key]; exists {
					return fmt.Errorf("suite contains duplicate case keys")
				}
				keys[c.Key] = i
			}
			result := struct {
				evals.TrialReliability
				Verdict  string   `json:"verdict"`
				Trials   int      `json:"trials"`
				Cases    int      `json:"cases"`
				Sessions []string `json:"sessions"`
				Gates    []string `json:"gates"`
			}{Verdict: "pass", Trials: trials, Cases: len(cases.Items), Sessions: []string{}, Gates: []string{}}
			passed := make([][]bool, trials)
			base, modelRef := "", ""
			incomplete := false
			for trial := 0; trial < trials; trial++ {
				outputs := map[string]string{}
				for _, c := range cases.Items {
					ref, output, err := cfg.runEvalSession(ctx, cmd, folder, template, c.Input, &base, &modelRef)
					if err != nil {
						return fmt.Errorf("trial %d case %q: %w", trial+1, c.Key, err)
					}
					result.Sessions = append(result.Sessions, ref)
					outputs[c.Key] = output
				}
				_, raw, err = cfg.do(ctx, http.MethodPost, "/v1/m/evals/gate", map[string]any{
					"suite_ref": suite, "subject_kind": "session", "subject_ref": template, "model_ref": modelRef, "outputs": outputs,
				}, http.StatusCreated, http.StatusOK)
				if err != nil {
					return err
				}
				var gate struct {
					ID        string `json:"id"`
					Effective string `json:"effective_verdict"`
					Run       *struct {
						Version int64             `json:"suite_version"`
						Cases   []evals.CaseScore `json:"cases"`
					} `json:"run"`
				}
				if err = json.Unmarshal(raw, &gate); err != nil {
					return err
				}
				if gate.ID == "" || gate.Run == nil || gate.Run.Version != spec.Version {
					return fmt.Errorf("gate returned no compatible scored run")
				}
				result.Gates = append(result.Gates, gate.ID)
				switch gate.Effective {
				case "fail":
					result.Verdict = "fail"
				case "warn":
					if result.Verdict == "pass" {
						result.Verdict = "warn"
					}
				case "pass":
				default:
					return fmt.Errorf("gate returned an invalid verdict")
				}
				passed[trial] = make([]bool, len(cases.Items))
				seen := map[string]bool{}
				for _, score := range gate.Run.Cases {
					index, ok := keys[score.CaseKey]
					if !ok || seen[score.CaseKey] {
						return fmt.Errorf("gate returned unexpected or duplicate cases")
					}
					seen[score.CaseKey] = true
					if score.Outcome != "pass" && score.Outcome != "fail" {
						incomplete = true
					}
					passed[trial][index] = score.Passed && score.Outcome == "pass"
				}
				if len(seen) != len(keys) {
					incomplete = true
				}
			}
			result.TrialReliability, err = evals.SummarizeTrials(passed)
			if err != nil {
				return err
			}
			if incomplete || result.PassPowK < spec.PassThreshold {
				result.Verdict = "fail"
			}
			if err = renderOut(cmd, func(w io.Writer) error {
				_, err := fmt.Fprintf(w, "%s: pass@1=%.4f pass@%d=%.4f pass^%d=%.4f 95%% CI=[%.4f,%.4f] sessions=%d cases=%d\n",
					result.Verdict, result.PassAt1, trials, result.PassAtK, trials, result.PassPowK, result.PassPowKCI.Lo, result.PassPowKCI.Hi, len(result.Sessions), result.Cases)
				return err
			}, result); err != nil {
				return err
			}
			if result.Verdict == "fail" {
				return errGateFailed
			}
			if result.Verdict == "warn" {
				_, err = fmt.Fprintln(cmd.ErrOrStderr(), "WARNING: an underlying eval gate reported degradation")
				return err
			}
			return nil
		},
	}
	cfg.addFlags(cmd)
	cmd.Flags().StringVar(&suite, "suite", "", "golden suite ID")
	cmd.Flags().StringVar(&template, "template", "", "saved session template ID")
	cmd.Flags().IntVar(&trials, "trials", 1, "independent sessions per case (1–100)")
	addDeprecatedJSONFlag(cmd)
	return cmd
}

// Compose the normal launch and pipeline wait; credentials stay in the client,
// never in subprocess arguments. Stop an owned launch even if attach or input fails.
func (c *evalsClientConfig) runEvalSession(ctx context.Context, parent *cobra.Command, folder, template, input string, base, modelRef *string) (ref, output string, err error) {
	childCfg := &agentClientConfig{}
	var run map[string]any
	start := newSessionStartCmdWithResult(childCfg, nil, func(created map[string]any) error { run = created; return nil })
	*childCfg = c.agentClientConfig
	childCfg.command = nil
	childCfg.credentialFlags.tokenFile = ""
	start.SetContext(ctx)
	start.SetErr(parent.ErrOrStderr())
	start.SetIn(parent.InOrStdin())
	flags := []string{"--template", template, "--worktree", "--name", "eval"}
	if *base != "" {
		flags = append(flags, "--worktree-from", *base)
	}
	if err = start.ParseFlags(flags); err != nil {
		return
	}
	if err = start.RunE(start, []string{folder}); err != nil {
		return
	}
	ref = str(run, "run_ref")
	if ref == "" {
		return "", "", fmt.Errorf("engine returned no session reference")
	}
	defer func() {
		if err == nil {
			return
		}
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_, stopErr := childCfg.stopPipelineProcess(stopCtx, sessionRunsPath+"/"+url.PathEscape(ref))
		if stopErr != nil {
			err = fmt.Errorf("session %s could not be stopped: %w (trial error: %v)", ref, stopErr, err)
		}
	}()
	if *base == "" {
		var raw []byte
		_, raw, err = childCfg.do(ctx, http.MethodGet, sessionRunsPath+"/"+url.PathEscape(ref)+"/diff", nil)
		if err != nil {
			return
		}
		var diff sessionBranchList
		if err = json.Unmarshal(raw, &diff); err != nil {
			return
		}
		*base = diff.Base
		if *base == "" {
			err = fmt.Errorf("engine returned no repository base commit")
			return
		}
	}
	// The waiter emits one JSON document; capture it in memory, never a file.
	var reply bytes.Buffer
	start.SetOut(&reply)
	start.Flags().String("output", "json", "output format")
	if err = childCfg.waitPipeline(start, run, input, 0); err != nil {
		return
	}
	var finished struct {
		State       string `json:"state"`
		LastMessage string `json:"last_message"`
	}
	if err = json.Unmarshal(reply.Bytes(), &finished); err != nil {
		return
	}
	if finished.State != "finished" {
		err = fmt.Errorf("session %s did not finish", ref)
		return
	}
	_, raw, readErr := childCfg.do(ctx, http.MethodGet, sessionRunsPath+"/"+url.PathEscape(ref), nil)
	if readErr != nil {
		err = readErr
		return
	}
	var measured map[string]any
	if err = json.Unmarshal(raw, &measured); err != nil {
		return
	}
	model := str(measured, "usage_model_ref")
	if model == "" {
		model = str(measured, "model_ref")
	}
	if model == "" {
		err = fmt.Errorf("session %s returned no observed or configured model", ref)
		return
	}
	if *modelRef != "" && *modelRef != model {
		err = fmt.Errorf("the model changed between trial sessions")
		return
	}
	*modelRef = model
	return ref, finished.LastMessage, nil
}
