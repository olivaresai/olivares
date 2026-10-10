// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// The git publication family: scriptable verbs over the thirteen
// /v1/m/gitpublish routes (modules/gitpublish/routes.go). `olivares gitpublish`
// and /v1/m/gitpublish are the same word, as with every module namespace.
//
// It is the datalane client shape (cmd_datalane.go) — the shared flag
// resolution, transport, redaction and bounded response reader — with ONE
// property that is this family's own: an effect request has THREE honest
// endings, and none of them may be flattened into exit 0.
//
//   - 200/201: the effect settled (applied, adopted or a proven no-dispatch).
//   - 202: the intent is recorded and dispatched but the HOST outcome is not
//     confirmed. It may have landed. Exit 0 would tell a script it published,
//     so the intent still renders first (in both output modes) and the exit is
//     Degraded — the same verdict `sourcescope` gives its own "accepted but not
//     in effect" answer.
//   - 409: EITHER a refusal (an error envelope; the shared httpErr mapping
//     answers it) OR the rejected intent itself (the host refused the effect
//     and the engine recorded why). The second shape still fails with Conflict,
//     but the intent renders first, because the recorded reason —
//     protected_branch_refused, workflow_change_refused — is the sentence the
//     operator actually needs, and the plain error path would swallow it.
//
// A transport failure on an EFFECT verb is Indeterminate, not Server: the
// request died after it was sent, so the host outcome is unknown, and the
// module's own recovery rule applies — re-send the SAME operation id (it
// returns the recorded intent and never dispatches twice) or reconcile. A read
// keeps the shared Server.
//
// WHAT IT DELIBERATELY DOES NOT DO.
//
//   - No authorization decision lives here. The credential and the tenant
//     header reach the engine, and the engine admits the exact caller at the
//     AAL floor each route seals (merge and abandon at AAL3).
//   - No value-shape validation. A required flag missing is a usage error
//     (exit 2, zero requests); a VALUE the operator typed — a sha, a branch
//     name, a merge method — is the module's to judge, and a copy of its
//     regexes here would be a second source of truth that drifts.
//   - No target administration. create/update/delete stay in the console
//     (AAL3 target administration); this surface is the publication flow the
//     issue names. The merge flags carry no expected_result_tree or
//     expected_base either: the module answers unsupported_requirement for
//     both, so a flag for them could only ever fail.
//
// The request timeout default is raised for the whole family because one
// effect spends the engine's FULL budget before it answers — admission
// (30s), the 2m dispatch deadline, settlement (30s), uncertainty observation
// (30s), release (15s), and the HTTP reply (15s): 4m. The shared 10s default would cut a healthy
// dispatch off mid-flight, which is exactly the ambiguous case above.

func newGitpublishCmd() *cobra.Command {
	flags := &authClientFlags{}
	cmd := &cobra.Command{
		Use:   "gitpublish",
		Short: "Publish through governed git targets: push, pull request, merge",
		Long: "Push commits, open pull requests and merge through approved publication targets,\n" +
			"exactly as the console does, over the same /v1/m/gitpublish routes. Every request is\n" +
			"checked against the target's approved bindings and your authority at the moment you\n" +
			"send it. Use gitpublish intents to follow a request and reconcile an uncertain one.",
		Example: "  olivares gitpublish targets ls\n" +
			"  olivares gitpublish push gpt_1 --operation-id run-7 --ref refs/heads/agent/run-7 \\\n" +
			"    --commit <sha> --tree <sha>\n" +
			"  olivares gitpublish intents ls --target gpt_1",
		Args: cobra.NoArgs,
	}
	flags.addPersistent(cmd)
	if t := cmd.PersistentFlags().Lookup("timeout"); t != nil {
		// The engine's full budget for one effect is admission (30s) + the 2m
		// dispatch deadline + settlement and uncertainty observation (30s each),
		// release (15s) and the reply (15s) = 4m;
		// the default covers it so a healthy publish is never cut mid-flight.
		t.Usage, t.DefValue, flags.timeout =
			"request timeout (covers the engine's full publication budget: admission, the 2m dispatch deadline, settle and release)",
			"4m15s", 255*time.Second
	}
	client := datalaneClient{flags: flags, base: "/v1/m/gitpublish", what: "gitpublish"}
	cmd.AddCommand(
		newGitpublishTargetsCmd(client),
		newGitpublishPushCmd(client),
		newGitpublishPullRequestCmd(client),
		newGitpublishMergeCmd(client),
		newGitpublishIntentsCmd(client),
	)
	return cmd
}

// gitpublishRequest issues one request with this family's error semantics. It
// is the skills request shape (cmd_skills.go): the shared resolution and
// transport, but a non-GET transport failure is Indeterminate, because for an
// effect the failure is mid-flight and the outcome unknown.
func gitpublishRequest(cmd *cobra.Command, c datalaneClient, method, path string, body any, recovery string) ([]byte, int, error) {
	opts, err := c.flags.resolutionOptions(cmd)
	if err != nil {
		return nil, 0, err
	}
	resolved, err := resolveCLIConfig(opts)
	if err != nil {
		return nil, 0, redactCoded(err, c.flags.token)
	}
	client, headers, err := cliTransport(cliTransportOptions{
		Resolved:       resolved,
		Insecure:       c.flags.insecure,
		AllowCleartext: c.flags.allowCleartext,
		Timeout:        c.flags.timeout,
		Stderr:         cmd.ErrOrStderr(),
	})
	if err != nil {
		return nil, 0, exitcode.Or(exitcode.Server, redactCoded(err, resolved.Token))
	}
	var payload io.Reader
	if body != nil {
		encoded, merr := json.Marshal(body)
		if merr != nil {
			return nil, 0, exitcode.New(exitcode.Usage, fmt.Errorf("encode request body: %w", merr))
		}
		payload = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(cmd.Context(), method, resolved.Server+c.base+path, payload)
	if err != nil {
		return nil, 0, exitcode.Or(exitcode.Server, redactCoded(err, resolved.Token))
	}
	req.Header = headers.Clone()
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := cliDo(client, req)
	if err != nil {
		if method == http.MethodGet {
			return nil, 0, exitcode.Or(exitcode.Server, redactCoded(err, resolved.Token))
		}
		// Mid-flight: the engine may have recorded and dispatched the effect.
		// The note goes to the command's stderr, where guidance lives in this
		// CLI; the recovery rides INSIDE the returned error too, so the JSON
		// error envelope a script parses carries the safe retry, not only the
		// classification.
		note := "the request failed before the engine answered: the publication may have been recorded — do not blind-retry."
		if recovery != "" {
			note += " " + recovery
		}
		fmt.Fprintln(cmd.ErrOrStderr(), note)
		failed := redactCoded(err, resolved.Token)
		if recovery != "" {
			failed = fmt.Errorf("%w; %s", failed, recovery)
		}
		return nil, 0, exitcode.New(exitcode.Indeterminate, failed)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, rerr := readCLIHTTPResponse(resp, req, maxDatalaneResponseSize+1, datalaneOK(resp.StatusCode), func(status int, body []byte) error {
		// A 409 from the effect routes may carry the REJECTED INTENT, not a
		// refusal (WriteReceipt, modules/gitpublish/routes.go): the host
		// refused the effect and the engine recorded it. That body is a
		// receipt gitpublishReceipt renders, so it is not a refusal here.
		if status == http.StatusConflict && gitpublishBodyIsIntent(body) {
			return nil
		}
		return gitpublishRefusal(status, body)
	})
	if rerr != nil {
		// A truncated read on a SUCCESS answer to an effect is the same
		// ambiguity as a transport failure: the engine accepted the request
		// and the answer was lost. A refusal (status >= 300) is not ambiguous
		// — the engine said no — so only the success side carries the note.
		if resp.StatusCode < http.StatusMultipleChoices && method != http.MethodGet {
			fmt.Fprintln(cmd.ErrOrStderr(),
				"the engine accepted the request but its answer was lost: the publication may have been recorded — do not blind-retry. "+recovery)
			return nil, resp.StatusCode, exitcode.New(exitcode.Indeterminate, rerr)
		}
		return raw, resp.StatusCode, wrapCLIResponseReadError(rerr, "read gitpublish response")
	}
	if len(raw) > maxDatalaneResponseSize {
		return nil, resp.StatusCode, exitcode.New(exitcode.Server, fmt.Errorf("gitpublish response exceeds %d bytes", maxDatalaneResponseSize))
	}
	return raw, resp.StatusCode, nil
}

// gitpublishReceipt renders the answer to an effect request (push, pull
// request, merge, reconcile, abandon) and carries the three-honest-endings
// contract. The body renders BEFORE any exit so nothing a caller parses is
// lost; the guidance goes to stderr so a `$(…)` capture never swallows the
// state it describes.
func gitpublishReceipt(cmd *cobra.Command, raw []byte, status int, recovery string) error {
	// An empty body becomes null, exactly as observeJSON normalizes: an empty
	// RawMessage is not valid JSON and would fail the marshal with an error
	// about the CLI rather than an honest report of what arrived.
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = []byte("null")
	}
	switch {
	case status == http.StatusAccepted:
		// The note goes to stderr BEFORE the render and whatever happens to
		// the render, the classification keeps naming the family's ending: a
		// render failure must not turn an unconfirmed publication into a
		// generic exit 1 that says nothing about the recorded state.
		note := "accepted and dispatched, but the outcome on the host is NOT confirmed — do not send it again blind; gitpublish intents reconcile reads the host."
		if recovery != "" {
			note += " " + recovery
		}
		fmt.Fprintln(cmd.ErrOrStderr(), note)
		if err := renderStatusOut(cmd, json.RawMessage(raw)); err != nil {
			return exitcode.Or(exitcode.Degraded, err)
		}
		return exitcode.New(exitcode.Degraded, errors.New("publication recorded with an unconfirmed outcome on the host"))
	case status == http.StatusConflict && gitpublishBodyIsIntent(raw):
		fmt.Fprintln(cmd.ErrOrStderr(), "the host refused the effect; the intent above records the refusal")
		if err := renderStatusOut(cmd, json.RawMessage(raw)); err != nil {
			return exitcode.Or(exitcode.Conflict, err)
		}
		return exitcode.New(exitcode.Conflict, errors.New("the host refused the effect (the intent records the refusal)"))
	case status >= http.StatusMultipleChoices:
		return gitpublishRefusal(status, raw)
	default:
		return datalaneResult(cmd, raw, status, "")
	}
}

// gitpublishBodyIsIntent reports whether an error-status body is the rejected
// INTENT rather than a refusal envelope. The intent DTO always carries state;
// the engine's error bodies are {"error":{...}} and never do.
func gitpublishBodyIsIntent(raw []byte) bool {
	var probe map[string]json.RawMessage
	return json.Unmarshal(raw, &probe) == nil && len(probe["state"]) > 0
}

// gitpublishRefusal classifies a refusal body through the shared mapping, and
// keeps the one field this family's refusals carry that the shared reader does
// not model: the engine attaches a top-level intent_id to every refusal that
// names a recorded intent (writeErr, modules/gitpublish/routes.go) precisely so
// the operator can reach it. Dropping it sends them listing every intent of
// the target to guess which row holds the scope; keeping it names the handle.
func gitpublishRefusal(status int, body []byte) error {
	refusal := datalaneHTTPError("gitpublish", status, body)
	var envelope struct {
		IntentID string `json:"intent_id"`
	}
	if json.Unmarshal(body, &envelope) != nil || strings.TrimSpace(envelope.IntentID) == "" {
		return refusal
	}
	id := strings.TrimSpace(envelope.IntentID)
	return exitcode.Or(exitcode.From(refusal), fmt.Errorf(
		"%w; the recorded intent is %s — follow it with olivares gitpublish intents get %s", refusal, id, id))
}

// gitpublishRequireNamed refuses, before a connection is opened, an effect the
// caller did not fully name. It checks that a flag was passed at all, never
// what value it carries: the value is the module's to judge.
func gitpublishRequireNamed(cmd *cobra.Command, what string, flags ...string) error {
	var missing []string
	for _, name := range flags {
		if !cmd.Flags().Changed(name) {
			missing = append(missing, "--"+name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	verb := "are"
	if len(missing) == 1 {
		verb = "is"
	}
	return exitcode.New(exitcode.Usage, fmt.Errorf(
		"%s names an exact effect and %s %s required: pass %s", what, strings.Join(missing, ", "), verb, strings.Join(missing, ", ")))
}

// ── targets ──────────────────────────────────────────────────────────────────────

func newGitpublishTargetsCmd(client datalaneClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "targets",
		Short:   "Read publication targets",
		Long:    "Read the publication targets you may publish through. Creating and changing targets is target administration (AAL3) in the console.",
		Example: "  olivares gitpublish targets ls\n  olivares gitpublish targets get gpt_1",
		Args:    cobra.NoArgs,
	}
	cmd.AddCommand(newGitpublishTargetsListCmd(client), newGitpublishTargetsGetCmd(client))
	return cmd
}

func newGitpublishTargetsListCmd(client datalaneClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List publication targets",
		Long:    "List the publication targets the caller can read, with the branch prefix pushes must stay under and the merge bases pull requests may merge into.",
		Example: "  olivares gitpublish targets ls",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			raw, status, err := gitpublishRequest(cmd, client, http.MethodGet, "/targets", nil, "")
			if err != nil {
				return err
			}
			if !datalaneOK(status) {
				return gitpublishRefusal(status, raw)
			}
			return datalaneRenderList(cmd, "gitpublish targets", raw, "no publication targets",
				datalaneCols("ID", "id", "WORKSPACE", "workspace_id", "PUSH PREFIX", "push_prefix", "MERGE BASES", "merge_bases"))
		},
	}
	return cmd
}

func newGitpublishTargetsGetCmd(client datalaneClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "get <target-id>",
		Short:   "Show one publication target",
		Long:    "Show one target's push prefix, merge bases and version; its credential and repository binding ids appear to a caller who holds target administration on it.",
		Example: "  olivares gitpublish targets get gpt_1",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := datalanePath("targets", args[0])
			if err != nil {
				return err
			}
			raw, status, err := gitpublishRequest(cmd, client, http.MethodGet, path, nil, "")
			if err != nil {
				return err
			}
			if !datalaneOK(status) {
				return gitpublishRefusal(status, raw)
			}
			return datalaneResult(cmd, raw, status, "")
		},
	}
	return cmd
}

// ── the three effects ────────────────────────────────────────────────────────────

// gitpublishRecovery is the sentence an effect verb appends to its uncertain
// and indeterminate answers, naming the exact retry the module's rules permit.
func gitpublishRecovery(operationID string) string {
	return "re-send the SAME request (operation id " + operationID + ") to read the recorded intent — it is not dispatched twice."
}

// gitpublishFollow is the follow-up sentence the verbs that carry an intent id
// (reconcile, abandon) name, since they have no operation id of their own.
func gitpublishFollow(intentID string) string {
	return "Follow the intent with gitpublish intents get " + intentID + "."
}

func newGitpublishPushCmd(client datalaneClient) *cobra.Command {
	var operationID, ref, commit, tree, expectedOld, acknowledge string
	cmd := &cobra.Command{
		Use:   "push <target-id>",
		Short: "Push one exact commit to a branch under the target's prefix",
		Long: "Push one exact commit to a leased branch under the target's push prefix, as the\n" +
			"target's approved credential. The default branch and protected branches are refused\n" +
			"by name; a CI-file change is refused. An empty --expected-old leases an absent branch.",
		Example: "  olivares gitpublish push gpt_1 --operation-id run-7 --ref refs/heads/agent/run-7 \\\n" +
			"    --commit 0123abcd... --tree 4567ef89...",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := gitpublishRequireNamed(cmd, "push", "operation-id", "ref", "commit", "tree"); err != nil {
				return err
			}
			path, err := datalanePath("targets", args[0], "pushes")
			if err != nil {
				return err
			}
			raw, status, err := gitpublishRequest(cmd, client, http.MethodPost, path, struct {
				OperationID       string `json:"operation_id"`
				Ref               string `json:"ref"`
				ExpectedOld       string `json:"expected_old,omitempty"`
				Commit            string `json:"commit"`
				Tree              string `json:"tree"`
				AcknowledgeIntent string `json:"acknowledge_intent,omitempty"`
			}{OperationID: operationID, Ref: ref, ExpectedOld: expectedOld, Commit: commit, Tree: tree, AcknowledgeIntent: acknowledge},
				gitpublishRecovery(operationID))
			if err != nil {
				return err
			}
			return gitpublishReceipt(cmd, raw, status, gitpublishRecovery(operationID))
		},
	}
	cmd.Flags().StringVar(&operationID, "operation-id", "", "caller-chosen id of this publication request; the same id re-sent returns the recorded intent")
	cmd.Flags().StringVar(&ref, "ref", "", "full ref to push, refs/heads/<branch> under the target's push prefix")
	cmd.Flags().StringVar(&commit, "commit", "", "exact commit sha to publish")
	cmd.Flags().StringVar(&tree, "tree", "", "the commit's tree sha, checked in the server repository")
	cmd.Flags().StringVar(&expectedOld, "expected-old", "", "the branch's expected current sha; empty leases an absent branch")
	cmd.Flags().StringVar(&acknowledge, "acknowledge-intent", "", "id of an abandoned intent that holds this scope (target administration at AAL3)")
	return cmd
}

func newGitpublishPullRequestCmd(client datalaneClient) *cobra.Command {
	var operationID, headRef, base, commit, title, body, acknowledge string
	var draft bool
	cmd := &cobra.Command{
		Use:   "pull-request <target-id>",
		Short: "Open a pull request from a prefixed branch into a merge base",
		Long: "Open a pull request (or adopt the matching open one) from a branch under the\n" +
			"target's push prefix into one of its allowed merge bases, as the target's approved\n" +
			"credential. A closed pull request never blocks and is never adopted.",
		Example: "  olivares gitpublish pull-request gpt_1 --operation-id run-7 --head-ref agent/run-7 \\\n" +
			"    --base main --commit 0123abcd... --title \"Publish run 7\"",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := gitpublishRequireNamed(cmd, "pull-request", "operation-id", "head-ref", "base", "commit", "title"); err != nil {
				return err
			}
			path, err := datalanePath("targets", args[0], "pull-requests")
			if err != nil {
				return err
			}
			raw, status, err := gitpublishRequest(cmd, client, http.MethodPost, path, struct {
				OperationID       string `json:"operation_id"`
				HeadRef           string `json:"head_ref"`
				Base              string `json:"base"`
				Commit            string `json:"commit"`
				Title             string `json:"title"`
				Body              string `json:"body,omitempty"`
				Draft             bool   `json:"draft,omitempty"`
				AcknowledgeIntent string `json:"acknowledge_intent,omitempty"`
			}{OperationID: operationID, HeadRef: headRef, Base: base, Commit: commit, Title: title, Body: body, Draft: draft, AcknowledgeIntent: acknowledge},
				gitpublishRecovery(operationID))
			if err != nil {
				return err
			}
			return gitpublishReceipt(cmd, raw, status, gitpublishRecovery(operationID))
		},
	}
	cmd.Flags().StringVar(&operationID, "operation-id", "", "caller-chosen id of this publication request; the same id re-sent returns the recorded intent")
	cmd.Flags().StringVar(&headRef, "head-ref", "", "branch under the target's push prefix to publish from")
	cmd.Flags().StringVar(&base, "base", "", "merge base to publish into (one of the target's merge bases)")
	cmd.Flags().StringVar(&commit, "commit", "", "the head commit sha the request is framed on")
	cmd.Flags().StringVar(&title, "title", "", "pull request title")
	cmd.Flags().StringVar(&body, "body", "", "pull request description")
	cmd.Flags().BoolVar(&draft, "draft", false, "open as a draft")
	cmd.Flags().StringVar(&acknowledge, "acknowledge-intent", "", "id of an abandoned intent that holds this scope (target administration at AAL3)")
	return cmd
}

func newGitpublishMergeCmd(client datalaneClient) *cobra.Command {
	var operationID, expectedHead, method, acknowledge string
	var number int
	cmd := &cobra.Command{
		Use:   "merge <target-id>",
		Short: "Merge a pull request whose head is still the reviewed sha",
		Long: "Merge one pull request only while its head is still the --expected-head that was\n" +
			"reviewed, with the sha guard the host enforces. Requires an admin session at AAL3;\n" +
			"the launcher of a publication must not also be its merger.",
		Example: "  olivares gitpublish merge gpt_1 --operation-id run-7 --number 12 \\\n" +
			"    --expected-head 0123abcd... --method squash",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := gitpublishRequireNamed(cmd, "merge", "operation-id", "number", "expected-head", "method"); err != nil {
				return err
			}
			path, err := datalanePath("targets", args[0], "merges")
			if err != nil {
				return err
			}
			raw, status, err := gitpublishRequest(cmd, client, http.MethodPost, path, struct {
				OperationID       string `json:"operation_id"`
				Number            int    `json:"number"`
				ExpectedHead      string `json:"expected_head"`
				Method            string `json:"method"`
				AcknowledgeIntent string `json:"acknowledge_intent,omitempty"`
			}{OperationID: operationID, Number: number, ExpectedHead: expectedHead, Method: method, AcknowledgeIntent: acknowledge},
				gitpublishRecovery(operationID))
			if err != nil {
				return err
			}
			return gitpublishReceipt(cmd, raw, status, gitpublishRecovery(operationID))
		},
	}
	cmd.Flags().StringVar(&operationID, "operation-id", "", "caller-chosen id of this publication request; the same id re-sent returns the recorded intent")
	cmd.Flags().IntVar(&number, "number", 0, "the pull request number on the host")
	cmd.Flags().StringVar(&expectedHead, "expected-head", "", "the reviewed head sha; a moved head refuses the merge")
	cmd.Flags().StringVar(&method, "method", "", "merge method: merge, squash or rebase (rebase is refused on GitLab)")
	cmd.Flags().StringVar(&acknowledge, "acknowledge-intent", "", "id of an abandoned intent that holds this scope (target administration at AAL3)")
	return cmd
}

// ── intents ──────────────────────────────────────────────────────────────────────

func newGitpublishIntentsCmd(client datalaneClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "intents",
		Short:   "Follow, reconcile and close publication requests",
		Long:    "Read the publication intents of one target, follow a single intent and its observations, reconcile an uncertain one against the host, and take responsibility for one that cannot resolve.",
		Example: "  olivares gitpublish intents ls --target gpt_1\n  olivares gitpublish intents get gpi_1",
		Args:    cobra.NoArgs,
	}
	cmd.AddCommand(
		newGitpublishIntentsListCmd(client),
		newGitpublishIntentsGetCmd(client),
		newGitpublishIntentsObservationsCmd(client),
		newGitpublishIntentsReconcileCmd(client),
		newGitpublishIntentsAbandonCmd(client),
	)
	return cmd
}

func newGitpublishIntentsListCmd(client datalaneClient) *cobra.Command {
	var target string
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List the publication intents of one target",
		Long:    "List one target's publication intents: what each requested, the state the engine last settled, and the receipt. The route reads one target at a time.",
		Example: "  olivares gitpublish intents ls --target gpt_1",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !cmd.Flags().Changed("target") {
				return exitcode.New(exitcode.Usage, fmt.Errorf("intents ls reads one target at a time: --target is required"))
			}
			q := url.Values{"target_id": []string{target}}
			raw, status, err := gitpublishRequest(cmd, client, http.MethodGet, "/intents?"+q.Encode(), nil, "")
			if err != nil {
				return err
			}
			if !datalaneOK(status) {
				return gitpublishRefusal(status, raw)
			}
			return datalaneRenderList(cmd, "gitpublish intents", raw, "no publication intents are recorded for this target",
				datalaneCols("ID", "id", "EFFECT", "effect", "STATE", "state", "OPERATION", "operation_id", "ATTEMPT", "attempt", "RECEIPT", "receipt"))
		},
	}
	cmd.Flags().StringVar(&target, "target", "", "the target whose intents to list")
	return cmd
}

func newGitpublishIntentsGetCmd(client datalaneClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "get <intent-id>",
		Aliases: []string{"status"},
		Short:   "Show one publication intent",
		Long:    "Show one intent: what it requested, what the host was last observed to hold, whether the host acknowledged it, and its state.",
		Example: "  olivares gitpublish intents get gpi_1",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := datalanePath("intents", args[0])
			if err != nil {
				return err
			}
			raw, status, err := gitpublishRequest(cmd, client, http.MethodGet, path, nil, "")
			if err != nil {
				return err
			}
			if !datalaneOK(status) {
				return gitpublishRefusal(status, raw)
			}
			return datalaneResult(cmd, raw, status, "")
		},
	}
	return cmd
}

func newGitpublishIntentsObservationsCmd(client datalaneClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "observations <intent-id>",
		Short:   "List every observation of one publication intent",
		Long:    "List every observation recorded for one intent: each host read, dispatcher outcome and refusal, with its attempt, result and time.",
		Example: "  olivares gitpublish intents observations gpi_1",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := datalanePath("intents", args[0], "observations")
			if err != nil {
				return err
			}
			raw, status, err := gitpublishRequest(cmd, client, http.MethodGet, path, nil, "")
			if err != nil {
				return err
			}
			if !datalaneOK(status) {
				return gitpublishRefusal(status, raw)
			}
			return datalaneRenderList(cmd, "gitpublish observations", raw, "no observations are recorded for this intent",
				datalaneCols("ATTEMPT", "attempt", "SOURCE", "source", "RESULT", "result", "AT", "at"))
		},
	}
	return cmd
}

func newGitpublishIntentsReconcileCmd(client datalaneClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reconcile <intent-id>",
		Short: "Read the host again for one uncertain intent",
		Long: "Read the host again for one publication intent and record what it observed. It\n" +
			"never dispatches; only the requested effect, once observed, ends an uncertain intent.",
		Example: "  olivares gitpublish intents reconcile gpi_1",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := datalanePath("intents", args[0], "reconcile")
			if err != nil {
				return err
			}
			raw, status, err := gitpublishRequest(cmd, client, http.MethodPost, path, nil, gitpublishFollow(args[0]))
			if err != nil {
				return err
			}
			return gitpublishReceipt(cmd, raw, status, gitpublishFollow(args[0]))
		},
	}
	return cmd
}

func newGitpublishIntentsAbandonCmd(client datalaneClient) *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "abandon <intent-id>",
		Short: "Take responsibility for an intent that cannot resolve",
		Long: "Record that an administrator takes responsibility for an unresolved publication\n" +
			"intent. Abandoning proves nothing about the host, and the scope stays held until a\n" +
			"later request names this intent under --acknowledge-intent. Requires AAL3.",
		Example: "  olivares gitpublish intents abandon gpi_1 --reason \"investigated: pushed by hand\"",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := datalanePath("intents", args[0], "abandon")
			if err != nil {
				return err
			}
			raw, status, err := gitpublishRequest(cmd, client, http.MethodPost, path, struct {
				Reason string `json:"reason,omitempty"`
			}{Reason: reason}, gitpublishFollow(args[0]))
			if err != nil {
				return err
			}
			return gitpublishReceipt(cmd, raw, status, gitpublishFollow(args[0]))
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "bounded note (256 bytes) recorded with the abandonment")
	return cmd
}
