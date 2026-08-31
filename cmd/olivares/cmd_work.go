// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/model"
)

const (
	workAPIBase            = "/v1/m/sessions"
	maxWorkCommandFileSize = 1 << 20
	maxWorkResponseSize    = 8 << 20
	workPlanFormat         = "olivares.work-plan/v1"
)

type workCommandRoute struct {
	method          string
	path            func(map[string]any) (string, error)
	requiresVersion bool
}

var workCommandRoutes = map[string]workCommandRoute{
	"item.create":       {method: http.MethodPost, path: workStaticPath("/work-items")},
	"item.update":       workItemRoute(http.MethodPatch, ""),
	"item.ready":        workItemRoute(http.MethodPost, "/transitions"),
	"item.block":        workItemRoute(http.MethodPost, "/transitions"),
	"item.unblock":      workItemRoute(http.MethodPost, "/transitions"),
	"item.submit":       workItemRoute(http.MethodPost, "/transitions"),
	"item.complete":     workItemRoute(http.MethodPost, "/transitions"),
	"item.fail":         workItemRoute(http.MethodPost, "/transitions"),
	"item.cancel":       workItemRoute(http.MethodPost, "/transitions"),
	"item.archive":      workItemRoute(http.MethodPost, "/transitions"),
	"item.assign":       workItemRoute(http.MethodPost, "/assignments"),
	"dependency.add":    workItemRoute(http.MethodPost, "/dependencies"),
	"dependency.remove": {method: http.MethodDelete, path: workDependencyRemovePath, requiresVersion: true},
	"acceptance.add":    workItemRoute(http.MethodPost, "/acceptance"),
	"acceptance.update": {
		method:          http.MethodPatch,
		path:            workAcceptanceEvaluatePath,
		requiresVersion: true,
	},
	"acceptance.evaluate": {
		method:          http.MethodPatch,
		path:            workAcceptanceEvaluatePath,
		requiresVersion: true,
	},
	"decision.set": {
		method:          http.MethodPost,
		path:            workStaticPath("/decisions"),
		requiresVersion: true,
	},
	"decision.supersede": {
		method:          http.MethodPost,
		path:            workStaticPath("/decisions"),
		requiresVersion: true,
	},
	"decision.revoke": {
		method:          http.MethodPost,
		path:            workDecisionRevokePath,
		requiresVersion: true,
	},
	"lease.acquire":      workItemRoute(http.MethodPost, "/lease/acquire"),
	"lease.renew":        workItemRoute(http.MethodPost, "/lease/renew"),
	"lease.release":      workItemRoute(http.MethodPost, "/lease/release"),
	"lease.takeover":     workItemRoute(http.MethodPost, "/lease/takeover"),
	"lease.revoke":       workItemRoute(http.MethodPost, "/lease/revoke"),
	"lease.clock_rebase": workItemRoute(http.MethodPost, "/lease/clock-rebase"),
}

var workDocumentStringFlags = map[string]string{
	"workspace-id":       "workspace_id",
	"work-item-id":       "work_item_id",
	"target-id":          "target_id",
	"dependency-id":      "dependency_id",
	"depends-on-id":      "depends_on_id",
	"criterion-id":       "criterion_id",
	"criterion-key":      "criterion_key",
	"decision-id":        "decision_id",
	"decision-key":       "decision_key",
	"work-kind":          "work_kind",
	"title":              "title",
	"brief":              "brief_md",
	"priority":           "priority",
	"owner-kind":         "owner_kind",
	"owner-ref":          "owner_ref",
	"provenance-kind":    "provenance_kind",
	"provenance-ref":     "provenance_ref",
	"provenance-hash":    "provenance_hash",
	"parent-id":          "parent_id",
	"supersedes-id":      "supersedes_id",
	"transition":         "transition",
	"code":               "code",
	"reason":             "reason",
	"blocked-code":       "blocked_code",
	"blocked-reason":     "blocked_reason",
	"terminal-code":      "terminal_code",
	"terminal-reason":    "terminal_reason",
	"due-at":             "due_at",
	"statement":          "statement",
	"statement-md":       "statement_md",
	"state":              "state",
	"evidence-ref":       "evidence_ref",
	"evidence-hash":      "evidence_hash",
	"waiver-decision-id": "waiver_decision_id",
	"subject-kind":       "subject_kind",
	"subject-ref":        "subject_ref",
	"rationale":          "rationale_md",
	"authority-ref":      "authority_ref",
	"holder-sid":         "holder_sid",
	"holder-run-ref":     "holder_run_ref",
	"holder-agent-ref":   "holder_agent_ref",
}

var workDocumentInt64Flags = map[string]string{
	"ttl-seconds": "ttl_seconds",
	"fence":       "fence",
}

var workDocumentBoolFlags = map[string]string{
	"force":             "force",
	"unblock":           "unblock",
	"changes-requested": "changes_requested",
}

type workMutationOptions struct {
	file           string
	planFile       string
	out            string
	idempotencyKey string
	planHash       string
	version        uint64
	fields         []string
}

type workResponse struct {
	status int
	header http.Header
	body   []byte
}

type workPlanArtifact struct {
	Format       string          `json:"format"`
	WorkCommand  map[string]any  `json:"work_command"`
	Plan         json.RawMessage `json:"plan"`
	PlanHash     string          `json:"plan_hash,omitempty"`
	ExpectedETag string          `json:"expected_etag,omitempty"`
}

func newWorkCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "work",
		Short: "Manage durable cross-session work, leases, decisions, and acceptance",
		Long: "work is the durable operator surface for backlog, ownership, leases, dependencies,\n" +
			"acceptance criteria, and decisions. Mutations always cross the control-plane\n" +
			"REST API through validate, plan, or apply; this CLI holds no local work state.",
		Example: "  olivares work list items --status ready\n" +
			"  olivares work get lease 01989d7d-32ac-7bb0-878d-3254f349a102\n" +
			"  olivares work validate item.create -f command.yaml\n" +
			"  olivares work watch --cursor 01989d7d-32ac-7bb0-878d-3254f349a102",
	}
	cmd.AddCommand(
		newWorkMutationCmd("validate"),
		newWorkMutationCmd("plan"),
		newWorkMutationCmd("apply"),
		newProtocolBindingCmd(),
		newWorkGetCmd(),
		newWorkListCmd(),
		newWorkReplayCmd(),
		newWorkWatchCmd(),
	)
	return cmd
}

func newWorkReplayCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "replay",
		Short: "Replay a dead-lettered durable work event",
		Long: "replay exposes governed recovery for a durable work event whose outbox delivery reached " +
			"dead-letter. Recovery keeps the original event ID and uses the same validate, plan, and " +
			"apply envelope as other work mutations.",
		Example: "  olivares work replay event 01989d7d-32ac-7bb0-878d-3254f349a102 --mode plan",
	}
	var (
		cfg                  agentClientConfig
		mode, idempotencyKey string
		planHash             string
		version              uint64
	)
	event := &cobra.Command{
		Use:   "event <event-id>",
		Short: "Requeue one dead-lettered WorkEvent under its stable event ID",
		Long: "event validates, plans, or applies one tenant-admin outbox recovery. Apply requires the " +
			"plan hash and row version returned by plan, plus an idempotency key for exact retries.",
		Example: "  olivares work replay event 01989d7d-32ac-7bb0-878d-3254f349a102 --mode plan\n" +
			"  olivares work replay event 01989d7d-32ac-7bb0-878d-3254f349a102 --mode apply " +
			"--plan-hash <sha256> --version 3",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			eventID, err := model.ParseID(strings.TrimSpace(args[0]))
			if err != nil || eventID.IsZero() {
				return workUsagef("event-id must be a valid UUID")
			}
			if err := cfg.resolve(); err != nil {
				return err
			}
			if mode != "validate" && mode != "plan" && mode != "apply" {
				return workUsagef("--mode must be validate, plan, or apply")
			}
			headers := make(http.Header)
			if strings.TrimSpace(planHash) != "" {
				decoded, err := hex.DecodeString(planHash)
				if err != nil || len(decoded) != 32 || len(planHash) != 64 {
					return workUsagef("--plan-hash must be a 64-character SHA-256 hex digest")
				}
				headers.Set("If-Plan-Hash", hex.EncodeToString(decoded))
			}
			if mode == "apply" {
				if headers.Get("If-Plan-Hash") == "" {
					return workUsagef("replay apply requires --plan-hash from replay plan")
				}
				if version == 0 {
					return workUsagef("replay apply requires --version from replay plan ETag")
				}
				key := strings.TrimSpace(idempotencyKey)
				if key == "" {
					key = model.NewID().String()
					fmt.Fprintf(cmd.ErrOrStderr(), "idempotency key: %s (reuse this key for an ambiguous retry)\n", key)
				}
				parsedKey, err := model.ParseID(key)
				if err != nil || parsedKey.IsZero() {
					return workUsagef("--idempotency-key must be a valid UUID")
				}
				headers.Set("Idempotency-Key", key)
				headers.Set("If-Match", fmt.Sprintf("\"v%d\"", version))
			}
			query := url.Values{"mode": []string{mode}}
			resp, err := workDo(
				cmd.Context(), &cfg, http.MethodPost,
				workAPIBase+"/work-events/"+url.PathEscape(eventID.String())+"/replay?"+query.Encode(),
				nil, headers, false,
			)
			if err != nil {
				return err
			}
			if resp.status < 200 || resp.status >= 300 {
				return workHTTPError(resp.status, resp.body)
			}
			return renderWorkResponse(cmd, resp.body)
		},
	}
	cfg.addFlags(event)
	event.Flags().StringVar(&mode, "mode", "apply", "command phase: validate, plan, or apply")
	event.Flags().StringVar(&planHash, "plan-hash", "", "required replay plan hash for apply")
	event.Flags().StringVar(&idempotencyKey, "idempotency-key", "", "UUID reused for an exact apply retry")
	event.Flags().Uint64Var(&version, "version", 0, "outbox row version from replay plan ETag (required for apply)")
	addDeprecatedJSONFlag(event)
	cmd.AddCommand(event)
	return cmd
}

func newWorkMutationCmd(mode string) *cobra.Command {
	var (
		cfg  agentClientConfig
		opts workMutationOptions
	)
	cmd := &cobra.Command{
		Use:     mode + " <command>",
		Short:   workMutationShort(mode),
		Long:    fmt.Sprintf("%s executes the %s phase for one durable work command. It sends the same command document used by the other phases and never performs work locally.", mode, mode),
		Example: workMutationExample(mode),
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 1 {
				return workUsagef("%s requires exactly one work command", mode)
			}
			if _, ok := workCommandRoutes[args[0]]; !ok {
				return workUsagef("unsupported work command %q", args[0])
			}
			return nil
		},
		ValidArgsFunction: completeWorkCommands,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkMutation(cmd, &cfg, mode, args[0], opts)
		},
	}
	cfg.addFlags(cmd)
	addWorkDocumentFlags(cmd, &opts)
	cmd.Flags().Uint64Var(&opts.version, "version", 0, "expected resource version N (sent as strong If-Match \"vN\")")
	cmd.Flags().StringVar(&opts.planHash, "plan-hash", "", "bind the request to this plan hash")
	switch mode {
	case "plan":
		cmd.Flags().StringVar(&opts.out, "out", "", "atomically write a reusable 0600 work-plan artifact")
	case "apply":
		cmd.Flags().StringVar(&opts.planFile, "plan", "", "replay a work-plan artifact instead of -f or inline fields")
		cmd.Flags().StringVar(&opts.idempotencyKey, "idempotency-key", "", "UUID reused for an unambiguous retry (generated and printed when omitted)")
	}
	addDeprecatedJSONFlag(cmd)
	return cmd
}

func workMutationShort(mode string) string {
	switch mode {
	case "validate":
		return "Validate one work command without writing"
	case "plan":
		return "Plan one work command and its expected durable effects without writing"
	default:
		return "Apply one validated work command idempotently"
	}
}

func workMutationExample(mode string) string {
	switch mode {
	case "validate":
		return "  olivares work validate item.create -f command.yaml\n" +
			"  olivares work validate item.ready --work-item-id 01989d7d-32ac-7bb0-878d-3254f349a102"
	case "plan":
		return "  olivares work plan item.update -f command.yaml --version 3 --out plan.json"
	default:
		return "  olivares work apply item.create -f command.yaml\n" +
			"  olivares work apply item.update --plan plan.json\n" +
			"  olivares work apply lease.acquire --work-item-id <id> --holder-sid <sid> --version 2"
	}
}

func addWorkDocumentFlags(cmd *cobra.Command, opts *workMutationOptions) {
	cmd.Flags().StringVarP(&opts.file, "file", "f", "", "YAML or JSON WorkCommand file ('-' reads stdin; exactly one document)")
	for flag := range workDocumentStringFlags {
		cmd.Flags().String(flag, "", "WorkCommand "+workDocumentStringFlags[flag])
	}
	for flag, field := range workDocumentInt64Flags {
		cmd.Flags().Int64(flag, 0, "WorkCommand "+field)
	}
	for flag, field := range workDocumentBoolFlags {
		cmd.Flags().Bool(flag, false, "WorkCommand "+field)
	}
	cmd.Flags().Int("ordinal", 0, "acceptance criterion display order")
	cmd.Flags().Bool("required", false, "make an acceptance criterion required")
	cmd.Flags().StringArrayVar(&opts.fields, "field", nil, "additional WorkCommand field as key=JSON (repeatable)")
}

func completeWorkCommands(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	items := make([]string, 0, len(workCommandRoutes))
	for name := range workCommandRoutes {
		if strings.HasPrefix(name, toComplete) {
			items = append(items, name)
		}
	}
	sort.Strings(items)
	return items, cobra.ShellCompDirectiveNoFileComp
}

func runWorkMutation(cmd *cobra.Command, cfg *agentClientConfig, mode, command string, opts workMutationOptions) error {
	doc, expectedETag, err := workMutationDocument(cmd, mode, command, opts)
	if err != nil {
		return err
	}
	route := workCommandRoutes[command]
	path, err := route.path(doc)
	if err != nil {
		return err
	}

	if cmd.Flags().Changed("version") {
		if opts.version == 0 {
			return workUsagef("--version must be greater than zero")
		}
		flagETag := fmt.Sprintf("\"v%d\"", opts.version)
		if expectedETag != "" && expectedETag != flagETag {
			return workUsagef("--version does not match the version captured by --plan")
		}
		expectedETag = flagETag
	}
	if expectedETag != "" && !validStrongWorkETag(expectedETag) {
		return workUsagef("plan carries invalid expected_etag %q", expectedETag)
	}
	if mode == "apply" && route.requiresVersion && expectedETag == "" {
		return workUsagef("%s apply requires --version or a --plan containing expected_etag", command)
	}

	planHash := strings.TrimSpace(opts.planHash)
	if fromDoc, _ := doc["plan_hash"].(string); fromDoc != "" {
		if planHash != "" && planHash != fromDoc {
			return workUsagef("--plan-hash does not match the command document")
		}
		planHash = fromDoc
	}
	if planHash != "" {
		doc["plan_hash"] = planHash
	}

	if err := cfg.resolve(); err != nil {
		return err
	}
	headers := make(http.Header)
	if expectedETag != "" {
		headers.Set("If-Match", expectedETag)
	}
	if mode == "apply" {
		key := strings.TrimSpace(opts.idempotencyKey)
		if key == "" {
			key = model.NewID().String()
			fmt.Fprintf(cmd.ErrOrStderr(), "idempotency key: %s (reuse this key for an ambiguous retry)\n", key)
		}
		parsedKey, parseErr := model.ParseID(key)
		if parseErr != nil || parsedKey.IsZero() {
			return workUsagef("--idempotency-key must be a valid UUID")
		}
		headers.Set("Idempotency-Key", key)
	}

	query := url.Values{"mode": []string{mode}}
	resp, err := workDo(cmd.Context(), cfg, route.method, workAPIBase+path+"?"+query.Encode(), doc, headers, false)
	if err != nil {
		return err
	}
	if resp.status < 200 || resp.status >= 300 {
		return workHTTPError(resp.status, resp.body)
	}
	if mode == "plan" && strings.TrimSpace(opts.out) != "" {
		if err := writeWorkPlan(opts.out, doc, resp, expectedETag); err != nil {
			return err
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "work plan written to %s\n", opts.out)
	}
	return renderWorkResponse(cmd, resp.body)
}

func workMutationDocument(cmd *cobra.Command, mode, command string, opts workMutationOptions) (map[string]any, string, error) {
	if mode == "apply" && strings.TrimSpace(opts.planFile) != "" {
		if strings.TrimSpace(opts.file) != "" || workInlineDocumentChanged(cmd) {
			return nil, "", workUsagef("--plan cannot be combined with -f or inline WorkCommand fields")
		}
		doc, planHash, etag, err := readWorkPlan(cmd, opts.planFile)
		if err != nil {
			return nil, "", err
		}
		if opts.planHash != "" && planHash != "" && opts.planHash != planHash {
			return nil, "", workUsagef("--plan-hash does not match --plan")
		}
		if opts.planHash == "" {
			opts.planHash = planHash
		}
		if opts.planHash != "" {
			doc["plan_hash"] = opts.planHash
		}
		if err := setWorkCommand(doc, command); err != nil {
			return nil, "", err
		}
		return doc, etag, nil
	}
	doc, err := buildWorkDocument(cmd, opts.file, opts.fields, command)
	return doc, "", err
}

func buildWorkDocument(cmd *cobra.Command, path string, extraFields []string, command string) (map[string]any, error) {
	var doc map[string]any
	if strings.TrimSpace(path) != "" {
		if workInlineDocumentChanged(cmd) {
			return nil, workUsagef("-f cannot be combined with inline WorkCommand fields")
		}
		var err error
		doc, err = readWorkDocument(cmd, path)
		if err != nil {
			return nil, err
		}
	} else {
		doc = make(map[string]any)
		for flag, field := range workDocumentStringFlags {
			if !cmd.Flags().Changed(flag) {
				continue
			}
			value, err := cmd.Flags().GetString(flag)
			if err != nil {
				return nil, err
			}
			doc[field] = value
		}
		for flag, field := range workDocumentInt64Flags {
			if !cmd.Flags().Changed(flag) {
				continue
			}
			value, err := cmd.Flags().GetInt64(flag)
			if err != nil {
				return nil, err
			}
			doc[field] = value
		}
		for flag, field := range workDocumentBoolFlags {
			if !cmd.Flags().Changed(flag) {
				continue
			}
			value, err := cmd.Flags().GetBool(flag)
			if err != nil {
				return nil, err
			}
			doc[field] = value
		}
		if cmd.Flags().Changed("ordinal") {
			value, err := cmd.Flags().GetInt("ordinal")
			if err != nil {
				return nil, err
			}
			doc["ordinal"] = value
		}
		if cmd.Flags().Changed("required") {
			value, err := cmd.Flags().GetBool("required")
			if err != nil {
				return nil, err
			}
			doc["required"] = value
		}
		for _, field := range extraFields {
			key, value, err := parseWorkField(field)
			if err != nil {
				return nil, err
			}
			if _, exists := doc[key]; exists {
				return nil, workUsagef("WorkCommand field %q was provided more than once", key)
			}
			doc[key] = value
		}
	}
	if err := setWorkCommand(doc, command); err != nil {
		return nil, err
	}
	return doc, nil
}

func workInlineDocumentChanged(cmd *cobra.Command) bool {
	if cmd.Flags().Changed("field") || cmd.Flags().Changed("ordinal") || cmd.Flags().Changed("required") {
		return true
	}
	for flag := range workDocumentStringFlags {
		if cmd.Flags().Changed(flag) {
			return true
		}
	}
	for flag := range workDocumentInt64Flags {
		if cmd.Flags().Changed(flag) {
			return true
		}
	}
	for flag := range workDocumentBoolFlags {
		if cmd.Flags().Changed(flag) {
			return true
		}
	}
	return false
}

func setWorkCommand(doc map[string]any, command string) error {
	if doc == nil {
		return workUsagef("WorkCommand must be a YAML or JSON object")
	}
	if got, exists := doc["command"]; exists {
		name, ok := got.(string)
		if !ok || name != command {
			return workUsagef("document command does not match positional command %q", command)
		}
	}
	doc["command"] = command
	return nil
}

func parseWorkField(raw string) (string, any, error) {
	key, value, ok := strings.Cut(raw, "=")
	key = strings.TrimSpace(key)
	if !ok || key == "" || key == "command" {
		return "", nil, workUsagef("--field must be key=JSON and cannot set command")
	}
	value = strings.TrimSpace(value)
	var decoded any
	if err := json.Unmarshal([]byte(value), &decoded); err == nil {
		return key, decoded, nil
	}
	return key, value, nil
}

func readWorkDocument(cmd *cobra.Command, path string) (map[string]any, error) {
	raw, err := readWorkInput(cmd, path)
	if err != nil {
		return nil, err
	}
	return decodeWorkDocument(raw, path)
}

func decodeWorkDocument(raw []byte, label string) (map[string]any, error) {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return nil, workUsagef("parse WorkCommand %s: %v", label, err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, workUsagef("parse WorkCommand %s: multiple YAML documents are not allowed", label)
		}
		return nil, workUsagef("parse WorkCommand %s: %v", label, err)
	}
	if doc == nil {
		return nil, workUsagef("WorkCommand %s must contain one YAML or JSON object", label)
	}
	return doc, nil
}

func readWorkInput(cmd *cobra.Command, path string) ([]byte, error) {
	var (
		r   io.Reader
		f   *os.File
		err error
	)
	if path == "-" {
		r = cmd.InOrStdin()
	} else {
		f, err = os.Open(filepath.Clean(path)) //nolint:gosec // operator-selected command document.
		if err != nil {
			return nil, workUsagef("open WorkCommand %s: %v", path, err)
		}
		defer func() { _ = f.Close() }()
		r = f
	}
	raw, err := io.ReadAll(io.LimitReader(r, maxWorkCommandFileSize+1))
	if err != nil {
		return nil, workUsagef("read WorkCommand %s: %v", path, err)
	}
	if len(raw) > maxWorkCommandFileSize {
		return nil, workUsagef("WorkCommand %s exceeds %d bytes", path, maxWorkCommandFileSize)
	}
	return raw, nil
}

func readWorkPlan(cmd *cobra.Command, path string) (map[string]any, string, string, error) {
	raw, err := readWorkInput(cmd, path)
	if err != nil {
		return nil, "", "", err
	}
	var artifact workPlanArtifact
	if err := json.Unmarshal(raw, &artifact); err == nil && artifact.Format == workPlanFormat {
		if artifact.WorkCommand == nil {
			return nil, "", "", workUsagef("work plan %s has no work_command", path)
		}
		return artifact.WorkCommand, artifact.PlanHash, artifact.ExpectedETag, nil
	}
	doc, err := decodeWorkDocument(raw, path)
	if err != nil {
		return nil, "", "", err
	}
	planHash, _ := doc["plan_hash"].(string)
	expectedETag, _ := doc["expected_etag"].(string)
	return doc, planHash, expectedETag, nil
}

func writeWorkPlan(path string, doc map[string]any, resp workResponse, fallbackETag string) error {
	if path == "-" {
		return workUsagef("--out requires a file path; use -o json to write the REST envelope to stdout")
	}
	var body map[string]any
	if err := json.Unmarshal(resp.body, &body); err != nil {
		return fmt.Errorf("decode work plan response: %w", err)
	}
	planHash, _ := body["plan_hash"].(string)
	expectedETag := strings.TrimSpace(resp.header.Get("ETag"))
	if expectedETag == "" {
		expectedETag, _ = body["expected_etag"].(string)
	}
	if expectedETag == "" {
		expectedETag = fallbackETag
	}
	artifact := workPlanArtifact{
		Format:       workPlanFormat,
		WorkCommand:  cloneWorkDocument(doc),
		Plan:         append(json.RawMessage(nil), resp.body...),
		PlanHash:     planHash,
		ExpectedETag: expectedETag,
	}
	delete(artifact.WorkCommand, "plan_hash")
	// render-exempt: plan artifacts have a fixed JSON file format and are not CLI output.
	raw, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		return fmt.Errorf("encode work plan: %w", err)
	}
	raw = append(raw, '\n')
	return writeAtomicWorkFile(path, raw)
}

func cloneWorkDocument(doc map[string]any) map[string]any {
	clone := make(map[string]any, len(doc))
	for key, value := range doc {
		clone[key] = value
	}
	return clone
}

func writeAtomicWorkFile(path string, raw []byte) error {
	target := filepath.Clean(path)
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(target)+".tmp-")
	if err != nil {
		return fmt.Errorf("create temporary work plan: %w", err)
	}
	tmpPath := tmp.Name()
	keep := false
	defer func() {
		_ = tmp.Close()
		if !keep {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("secure temporary work plan: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		return fmt.Errorf("write temporary work plan: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temporary work plan: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary work plan: %w", err)
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return fmt.Errorf("replace work plan %s: %w", target, err)
	}
	keep = true
	if err := os.Chmod(target, 0o600); err != nil {
		return fmt.Errorf("secure work plan %s: %w", target, err)
	}
	return nil
}

func workStaticPath(path string) func(map[string]any) (string, error) {
	return func(map[string]any) (string, error) { return path, nil }
}

func workItemRoute(method, suffix string) workCommandRoute {
	return workCommandRoute{
		method: method,
		path: func(doc map[string]any) (string, error) {
			id, err := requiredWorkString(doc, "work_item_id")
			if err != nil {
				return "", err
			}
			return "/work-items/" + url.PathEscape(id) + suffix, nil
		},
		requiresVersion: true,
	}
}

func workDependencyRemovePath(doc map[string]any) (string, error) {
	workItemID, err := requiredWorkString(doc, "work_item_id")
	if err != nil {
		return "", err
	}
	dependencyID, err := requiredWorkStringAlias(doc, "target_id", "dependency_id", "dep_id")
	if err != nil {
		return "", err
	}
	return "/work-items/" + url.PathEscape(workItemID) + "/dependencies/" + url.PathEscape(dependencyID), nil
}

func workAcceptanceEvaluatePath(doc map[string]any) (string, error) {
	workItemID, err := requiredWorkString(doc, "work_item_id")
	if err != nil {
		return "", err
	}
	criterionID, err := requiredWorkString(doc, "criterion_id")
	if err != nil {
		return "", err
	}
	return "/work-items/" + url.PathEscape(workItemID) + "/acceptance/" + url.PathEscape(criterionID), nil
}

func workDecisionRevokePath(doc map[string]any) (string, error) {
	decisionID, err := requiredWorkString(doc, "decision_id")
	if err != nil {
		return "", err
	}
	return "/decisions/" + url.PathEscape(decisionID) + "/revoke", nil
}

func requiredWorkString(doc map[string]any, key string) (string, error) {
	value, ok := doc[key].(string)
	value = strings.TrimSpace(value)
	if !ok || value == "" {
		return "", workUsagef("WorkCommand %s is required", key)
	}
	return value, nil
}

func requiredWorkStringAlias(doc map[string]any, keys ...string) (string, error) {
	for _, key := range keys {
		if value, ok := doc[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value), nil
		}
	}
	return "", workUsagef("WorkCommand %s is required", strings.Join(keys, " or "))
}

func validStrongWorkETag(etag string) bool {
	if len(etag) < 4 || !strings.HasPrefix(etag, "\"v") || !strings.HasSuffix(etag, "\"") {
		return false
	}
	n, err := strconv.ParseUint(etag[2:len(etag)-1], 10, 64)
	return err == nil && n > 0
}

func workDo(ctx context.Context, cfg *agentClientConfig, method, path string, body any, headers http.Header, stream bool) (workResponse, error) {
	req, err := cfg.newRequest(ctx, method, path, body)
	if err != nil {
		return workResponse{}, err
	}
	for key, values := range headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	var client *http.Client
	if stream {
		client, err = cfg.streamTransport()
	} else {
		client, err = cfg.transport(cfg.timeout)
	}
	if err != nil {
		return workResponse{}, err
	}
	resp, err := cliDo(client, req)
	if err != nil {
		return workResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxWorkResponseSize+1))
	if err != nil {
		return workResponse{}, err
	}
	if len(raw) > maxWorkResponseSize {
		return workResponse{}, fmt.Errorf("work response exceeds %d bytes", maxWorkResponseSize)
	}
	return workResponse{status: resp.StatusCode, header: resp.Header.Clone(), body: raw}, nil
}

func renderWorkResponse(cmd *cobra.Command, body []byte) error {
	var value any
	if len(bytes.TrimSpace(body)) == 0 {
		value = map[string]any{}
	} else if err := decodeWorkJSON(body, &value); err != nil {
		return fmt.Errorf("decode work response: %w", err)
	}
	if err := renderOut(cmd, func(out io.Writer) error {
		tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
		writeStatusLines(tw, "", value)
		return tw.Flush()
	}, value); err != nil {
		return err
	}
	verdict := workVerdict(value)
	switch verdict {
	case "", "LIMPIO":
		return nil
	case "ROTO":
		return exitcode.New(exitcode.Degraded, nil)
	case "NO_HE_PODIDO_MIRAR":
		return exitcode.New(exitcode.Indeterminate, nil)
	default:
		return fmt.Errorf("work response carries unknown verdict %q", verdict)
	}
}

func workVerdict(value any) string {
	object, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	verdict, _ := object["verdict"].(string)
	return verdict
}

func workHTTPError(status int, body []byte) error {
	base := fmt.Errorf("request failed: HTTP %d: %s", status, strings.TrimSpace(string(body)))
	var envelope struct {
		Verdict string `json:"verdict"`
		Error   struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &envelope)
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return exitcode.New(exitcode.Auth, base)
	case status == http.StatusNotFound:
		return exitcode.New(exitcode.NotFound, base)
	case status == http.StatusBadRequest:
		return exitcode.New(exitcode.Usage, base)
	case status == http.StatusConflict || status == http.StatusPreconditionFailed ||
		status == http.StatusPreconditionRequired || status == http.StatusUnprocessableEntity ||
		status == http.StatusLocked:
		return exitcode.New(exitcode.Conflict, base)
	case status == http.StatusBadGateway || envelope.Verdict == "ROTO":
		return exitcode.New(exitcode.Degraded, base)
	case status == http.StatusServiceUnavailable &&
		(envelope.Verdict == "NO_HE_PODIDO_MIRAR" || workEvidenceUnavailableCode(envelope.Error.Code)):
		return exitcode.New(exitcode.Indeterminate, base)
	case status >= http.StatusInternalServerError:
		return exitcode.New(exitcode.Server, base)
	default:
		return base
	}
}

func workEvidenceUnavailableCode(code string) bool {
	switch code {
	case "evidence_unavailable", "clock_unavailable", "clock_rollback", "policy_unavailable", "observation_unavailable":
		return true
	default:
		return false
	}
}

func workUsagef(format string, args ...any) error {
	return exitcode.New(exitcode.Usage, fmt.Errorf(format, args...))
}

func newWorkGetCmd() *cobra.Command {
	var cfg agentClientConfig
	cmd := &cobra.Command{
		Use:   "get item|decision|lease <id>",
		Short: "Get one durable work item, decision, or lease",
		Long:  "get retrieves one tenant-visible work item snapshot, append-only decision, or WorkItem lease from the control plane.",
		Example: "  olivares work get item 01989d7d-32ac-7bb0-878d-3254f349a102\n" +
			"  olivares work get decision 01989d7d-4221-7429-ac66-d118af429159 -o json\n" +
			"  olivares work get lease 01989d7d-32ac-7bb0-878d-3254f349a102",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 2 {
				return workUsagef("get requires item|decision|lease and one id")
			}
			if args[0] != "item" && args[0] != "decision" && args[0] != "lease" {
				return workUsagef("unsupported work resource %q", args[0])
			}
			return nil
		},
		ValidArgsFunction: completeWorkGetArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			var path string
			switch args[0] {
			case "item":
				path = "/work-items/" + url.PathEscape(args[1])
			case "decision":
				path = "/decisions/" + url.PathEscape(args[1])
			case "lease":
				path = "/work-items/" + url.PathEscape(args[1]) + "/lease"
			}
			resp, err := workDo(cmd.Context(), &cfg, http.MethodGet,
				workAPIBase+path, nil, nil, false)
			if err != nil {
				return err
			}
			if resp.status != http.StatusOK {
				return workHTTPError(resp.status, resp.body)
			}
			return renderWorkResponse(cmd, resp.body)
		},
	}
	cfg.addFlags(cmd)
	addDeprecatedJSONFlag(cmd)
	return cmd
}

func completeWorkGetArgs(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return []string{"item", "decision", "lease"}, cobra.ShellCompDirectiveNoFileComp
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}

type workListOptions struct {
	limit   int
	cursor  string
	filters []string
}

var workListStringFilters = []string{
	"status", "priority", "work-kind", "owner-kind", "owner-ref", "provenance-kind",
	"provenance-ref", "parent-id", "due-before", "updated-after", "work-item-id",
	"decision-key", "subject-kind", "subject-ref", "actor-kind", "actor-ref",
	"holder-sid", "state", "expires-before",
}

var workListBoolFilters = []string{"archived", "effective", "revoked"}

var workItemFilterAllowlist = map[string]string{
	"status": "status", "priority": "priority", "work-kind": "work_kind",
	"owner-kind": "owner_kind", "owner-ref": "owner_ref", "provenance-kind": "provenance_kind",
	"provenance-ref": "provenance_ref", "parent-id": "parent_id", "archived": "archived",
	"due-before": "due_before", "updated-after": "updated_after",
}

var workDecisionFilterAllowlist = map[string]string{
	"work-item-id": "work_item_id", "decision-key": "decision_key", "subject-kind": "subject_kind",
	"subject-ref": "subject_ref", "actor-kind": "decided_by_kind", "actor-ref": "decided_by_ref",
	"effective": "effective", "revoked": "revoked",
}

var workLeaseFilterAllowlist = map[string]string{
	"work-item-id": "work_item_id", "holder-sid": "holder_sid", "state": "state",
	"expires-before": "expires_before",
}

func newWorkListCmd() *cobra.Command {
	var (
		cfg  agentClientConfig
		opts workListOptions
	)
	cmd := &cobra.Command{
		Use:     "list items|decisions|leases",
		Aliases: []string{"ls"},
		Short:   "List durable work items, decisions, or leases with keyset pagination",
		Long: "list returns one tenant-visible keyset page. Filters are an allowlist and are combined with AND by the control plane. " +
			"Decision lists are append-only history unless --effective or --revoked selects the current-head projection.",
		Example: "  olivares work list items --status ready --limit 50\n" +
			"  olivares work list decisions --work-item-id 01989d7d-32ac-7bb0-878d-3254f349a102 -o json\n" +
			"  olivares work list leases --state active --expires-before 2026-08-11T00:00:00Z",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 1 || (args[0] != "items" && args[0] != "decisions" && args[0] != "leases") {
				return workUsagef("list requires exactly one of items|decisions|leases")
			}
			return nil
		},
		ValidArgsFunction: func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
			return []string{"items", "decisions", "leases"}, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkList(cmd, &cfg, args[0], opts)
		},
	}
	cfg.addFlags(cmd)
	cmd.Flags().IntVar(&opts.limit, "limit", 100, "page size (1..200)")
	cmd.Flags().StringVar(&opts.cursor, "cursor", "", "opaque UUIDv7 keyset cursor")
	cmd.Flags().StringArrayVar(&opts.filters, "filter", nil, "additional allowlisted filter as key=value (repeatable)")
	for _, flag := range workListStringFilters {
		cmd.Flags().String(flag, "", "filter by "+strings.ReplaceAll(flag, "-", " "))
	}
	cmd.Flags().Bool("archived", false, "filter work items by archived state")
	cmd.Flags().Bool("effective", false, "filter decisions by effective head state")
	cmd.Flags().Bool("revoked", false, "filter decisions by revoked head state")
	addDeprecatedJSONFlag(cmd)
	return cmd
}

func runWorkList(cmd *cobra.Command, cfg *agentClientConfig, noun string, opts workListOptions) error {
	if opts.limit < 1 || opts.limit > 200 {
		return workUsagef("--limit must be between 1 and 200")
	}
	query := url.Values{"limit": []string{strconv.Itoa(opts.limit)}}
	if opts.cursor != "" {
		query.Set("cursor", opts.cursor)
	}
	allowlist := workItemFilterAllowlist
	path := "/work-items"
	switch noun {
	case "decisions":
		allowlist = workDecisionFilterAllowlist
		path = "/decisions"
	case "leases":
		allowlist = workLeaseFilterAllowlist
		path = "/leases"
	}
	for _, flag := range append(append([]string(nil), workListStringFilters...), workListBoolFilters...) {
		if cmd.Flags().Changed(flag) {
			if _, allowed := allowlist[flag]; !allowed {
				return workUsagef("--%s is not an allowed filter for %s", flag, noun)
			}
		}
	}
	for flag, key := range allowlist {
		if !cmd.Flags().Changed(flag) {
			continue
		}
		if flag == "archived" || flag == "effective" || flag == "revoked" {
			value, err := cmd.Flags().GetBool(flag)
			if err != nil {
				return err
			}
			query.Set(key, strconv.FormatBool(value))
			continue
		}
		value, err := cmd.Flags().GetString(flag)
		if err != nil {
			return err
		}
		query.Set(key, value)
	}
	for _, filter := range opts.filters {
		key, value, ok := strings.Cut(filter, "=")
		apiKey, allowed := allowlist[strings.TrimSpace(key)]
		if !ok || !allowed || strings.TrimSpace(value) == "" {
			return workUsagef("filter %q is not allowed for %s", filter, noun)
		}
		if query.Has(apiKey) {
			return workUsagef("filter %q was provided more than once", key)
		}
		query.Set(apiKey, strings.TrimSpace(value))
	}
	if err := cfg.resolve(); err != nil {
		return err
	}
	resp, err := workDo(cmd.Context(), cfg, http.MethodGet, workAPIBase+path+"?"+query.Encode(), nil, nil, false)
	if err != nil {
		return err
	}
	if resp.status != http.StatusOK {
		return workHTTPError(resp.status, resp.body)
	}
	var page struct {
		Items []map[string]any `json:"items"`
	}
	if err := decodeWorkJSON(resp.body, &page); err != nil {
		return fmt.Errorf("decode work list response: %w", err)
	}
	empty := "no work items"
	switch noun {
	case "decisions":
		empty = "no decisions"
	case "leases":
		empty = "no leases"
	}
	return renderListOut(cmd, page.Items, empty, func(out io.Writer, item map[string]any) error {
		if noun == "decisions" {
			_, err := fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", str(item, "id"), str(item, "operation"), str(item, "decision_key"), workDecisionListState(item))
			return err
		}
		if noun == "leases" {
			_, err := fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%s\n", str(item, "id"), str(item, "state"),
				str(item, "holder_sid"), workListScalar(item, "fence"), str(item, "expires_at"))
			return err
		}
		_, err := fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", str(item, "id"), str(item, "status"), str(item, "priority"), str(item, "title"))
		return err
	}, json.RawMessage(resp.body))
}

func workDecisionListState(item map[string]any) string {
	if state := str(item, "state"); state != "" {
		return state
	}
	return "history"
}

func workListScalar(item map[string]any, key string) string {
	value, ok := item[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case json.Number:
		return typed.String()
	default:
		return fmt.Sprint(typed)
	}
}

func decodeWorkJSON(body []byte, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

type workWatchOptions struct {
	cursor string
}

func newWorkWatchCmd() *cobra.Command {
	var (
		cfg  agentClientConfig
		opts workWatchOptions
	)
	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Watch the durable work-event stream from a resumable cursor",
		Long:  "watch opens the sessions work-stream SSE endpoint without a client deadline. Each persisted event is printed once by the server cursor; pass the last id back with --cursor to resume.",
		Example: "  olivares work watch\n" +
			"  olivares work watch --cursor 01989d7d-32ac-7bb0-878d-3254f349a102 -o json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runWorkWatch(cmd, &cfg, opts)
		},
	}
	cfg.addFlags(cmd)
	cmd.Flags().StringVar(&opts.cursor, "cursor", "", "resume after this persisted WorkEvent cursor")
	addDeprecatedJSONFlag(cmd)
	return cmd
}

func runWorkWatch(cmd *cobra.Command, cfg *agentClientConfig, opts workWatchOptions) error {
	query := url.Values{}
	if opts.cursor != "" {
		query.Set("cursor", opts.cursor)
	}
	if err := cfg.resolve(); err != nil {
		return err
	}
	path := workAPIBase + "/work-stream"
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	req, err := cfg.newRequest(cmd.Context(), http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	if opts.cursor != "" {
		req.Header.Set("Last-Event-ID", opts.cursor)
	}
	client, err := cfg.streamTransport()
	if err != nil {
		return err
	}
	resp, err := cliDo(client, req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxWorkCommandFileSize))
		return workHTTPError(resp.StatusCode, body)
	}
	format, err := selectedOutput(cmd)
	if err != nil {
		return err
	}
	return consumeWorkSSE(cmd.Context(), resp.Body, cmd.OutOrStdout(), format)
}

type workSSEFrame struct {
	id    string
	event string
	data  []string
}

func consumeWorkSSE(ctx context.Context, src io.Reader, out io.Writer, format string) error {
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	frame := workSSEFrame{}
	dispatch := func() error {
		if len(frame.data) == 0 {
			frame = workSSEFrame{}
			return nil
		}
		data := strings.Join(frame.data, "\n")
		if frame.event == "olivares.error" {
			var envelope struct {
				Verdict string `json:"verdict"`
				Code    string `json:"code"`
			}
			if err := json.Unmarshal([]byte(data), &envelope); err != nil {
				return exitcode.New(exitcode.Indeterminate,
					fmt.Errorf("work stream ended with unreadable failure evidence: %w", err))
			}
			return exitcode.New(exitcode.Indeterminate,
				fmt.Errorf("work stream observation failed: %s", envelope.Code))
		}
		if format == "json" {
			var compact bytes.Buffer
			if err := json.Compact(&compact, []byte(data)); err != nil {
				return fmt.Errorf("decode work-stream event %q: %w", frame.id, err)
			}
			if _, err := fmt.Fprintln(out, compact.String()); err != nil {
				return err
			}
		} else {
			event := frame.event
			if event == "" {
				event = "message"
			}
			if _, err := fmt.Fprintf(out, "%s\t%s\t%s\n", frame.id, event, data); err != nil {
				return err
			}
		}
		frame = workSSEFrame{}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, found := strings.Cut(line, ":")
		if !found {
			value = ""
		}
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "id":
			frame.id = value
		case "event":
			frame.event = value
		case "data":
			frame.data = append(frame.data, value)
		}
	}
	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	if err := dispatch(); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return nil
	}
	return exitcode.New(exitcode.Indeterminate,
		fmt.Errorf("work stream ended before the caller canceled it; resume from the last persisted event id"))
}
