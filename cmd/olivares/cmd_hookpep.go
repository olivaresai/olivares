// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const hookPEPPDPPath = "/v1/m/governance/pdp/"

var (
	errHookPEPValidationFailed = errors.New("policy validation failed")
	errHookPEPDenied           = errors.New("policy decision denied")
)

// hookPEPClientConfig is intentionally transport-only: policy compilation,
// evaluation and activation remain server-side in modules/governance.
type hookPEPClientConfig struct {
	baseURL string
	// resolveServer applies the --server/--url precedence (E7). Nil when
	// the config was built without addFlags, as some constructor tests do.
	resolveServer func() string
	token         string
	caCert        string
	pins          []string
	insecure      bool
	timeout       time.Duration
}

func (c *hookPEPClientConfig) addFlags(cmd *cobra.Command) {
	flags := cmd.PersistentFlags()
	flags.StringVar(&c.baseURL, "url", "", "control-plane base URL (default $OLIVARES_HOOK_PEP_URL); --server is the canonical spelling")
	// E7: --server reaches this group too, without removing --url.
	c.resolveServer = addServerAliasFlag(cmd, &c.baseURL, "url", "OLIVARES_HOOK_PEP_URL", true)
	flags.StringVar(&c.token, "token", "", "API bearer token (default $OLIVARES_HOOK_PEP_TOKEN)")
	// ⛔ ESTAS DOS FALTABAN, y el comentario de `do` afirmaba que existían. Medido el 2026-08-19
	// contra un plano vivo: `hookpep versions --server https://…` contra un certificado
	// autofirmado —el que genera NUESTRO PROPIO `quickstart`, que además imprime su pin— fallaba
	// con «x509: certificate signed by unknown authority» y la única salida que el mandato
	// ofrecía era `--insecure`, es decir apagar la verificación entera. El transporte compartido
	// SIEMPRE supo pinear; lo que no había era dónde escribir el pin. Una promesa en un
	// comentario no es un control.
	flags.StringVar(&c.caCert, "ca-cert", "", "PEM CA bundle used to verify the control plane")
	flags.StringArrayVar(&c.pins, "pin-sha256", nil, "pinned leaf SPKI SHA-256, base64 or hex (repeatable) — the engine prints it as pin_sha256 on the line reporting its certificate")
	flags.BoolVar(&c.insecure, "insecure", false, "skip TLS certificate verification (self-signed development planes only)")
	flags.DurationVar(&c.timeout, "timeout", 30*time.Second, "request timeout")
}

func (c *hookPEPClientConfig) resolve() error {
	if c.resolveServer != nil {
		c.baseURL = c.resolveServer()
	} else {
		c.baseURL = strings.TrimRight(firstNonEmptyEnv(c.baseURL, "OLIVARES_HOOK_PEP_URL"), "/")
	}
	c.token = strings.TrimSpace(firstNonEmptyEnv(c.token, "OLIVARES_HOOK_PEP_TOKEN"))
	switch {
	case c.baseURL == "":
		return missingServerError("url", "OLIVARES_HOOK_PEP_URL")
	case c.token == "":
		return fmt.Errorf("no token: set --token or OLIVARES_HOOK_PEP_TOKEN")
	}
	return nil
}

func (c *hookPEPClientConfig) post(ctx context.Context, action string, body any) (int, []byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return 0, nil, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, action, nil, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, err
	}
	return c.do(req)
}

func (c *hookPEPClientConfig) get(ctx context.Context, action string, query url.Values) (int, []byte, error) {
	req, err := c.newRequest(ctx, http.MethodGet, action, query, nil)
	if err != nil {
		return 0, nil, err
	}
	return c.do(req)
}

func (c *hookPEPClientConfig) newRequest(ctx context.Context, method, action string, query url.Values, body io.Reader) (*http.Request, error) {
	endpoint := c.baseURL + hookPEPPDPPath + action
	if encoded := query.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func (c *hookPEPClientConfig) do(req *http.Request) (int, []byte, error) {
	// E4: the shared transport, so --ca-cert, --pin-sha256 and the
	// --insecure warning exist here too, and a dead plane exits 6 not 1.
	client, _, err := cliTransport(cliTransportOptions{
		Resolved: cliResolvedConfig{Server: c.baseURL, Token: c.token, CACert: c.caCert, PinSHA256: c.pins},
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
	response, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return resp.StatusCode, response, err
}

func newHookPEPCmd() *cobra.Command {
	var cfg hookPEPClientConfig
	cmd := &cobra.Command{
		Use:   "hookpep",
		Short: "Author and inspect PDP policy through the control plane",
		Long: "hookpep is the authoring loop for the policy the PDP enforces: validate a\n" +
			"candidate, dry-run a concrete request against it, ask why that request would be\n" +
			"decided the way it is, then publish it as a new immutable revision — and roll\n" +
			"back to a prior revision when a change turns out wrong.\n\n" +
			"Nothing before publish changes what is in force, so a policy can be reasoned\n" +
			"about against real requests before it can deny one.",
		Example: "  olivares hookpep validate --engine cedar --file policy.cedar -o json\n" +
			"  olivares hookpep dry-run --engine cedar --file policy.cedar --request-file request.json\n" +
			"  olivares hookpep publish --engine cedar --file policy.cedar --note \"approved change\"",
	}
	cfg.addFlags(cmd)
	addDeprecatedFormatFlag(cmd, true)
	cmd.AddCommand(
		newHookPEPSourceCmd("validate", &cfg),
		newHookPEPSourceCmd("dry-run", &cfg),
		newHookPEPSourceCmd("explain", &cfg),
		newHookPEPPublishCmd(&cfg),
		newHookPEPVersionsCmd(&cfg),
		newHookPEPTestsCmd(&cfg),
		newHookPEPRollbackCmd(&cfg),
	)
	return cmd
}

func newHookPEPSourceCmd(action string, cfg *hookPEPClientConfig) *cobra.Command {
	var engine, source, sourceFile, requestJSON, requestFile string
	short := map[string]string{
		"validate": "Compile and validate a candidate policy without publishing it",
		"dry-run":  "Evaluate a request against a candidate policy without publishing it",
		"explain":  "Explain a request decision against a candidate policy without publishing it",
	}[action]
	cmd := &cobra.Command{
		Use:     action,
		Short:   short,
		Long:    short + ". The command is a thin authenticated HTTP client; all policy semantics and decisions are produced by the control plane.",
		Example: hookPEPSourceExample(action),
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			policy, err := readHookPEPValue(cmd.InOrStdin(), source, sourceFile, "policy source")
			if err != nil {
				return err
			}
			body := struct {
				Engine  string          `json:"engine"`
				Source  string          `json:"source"`
				Request json.RawMessage `json:"request,omitempty"`
			}{Engine: engine, Source: policy}
			if action != "validate" {
				rawRequest, readErr := readHookPEPValue(cmd.InOrStdin(), requestJSON, requestFile, "example request")
				if readErr != nil {
					return readErr
				}
				if !json.Valid([]byte(rawRequest)) {
					return fmt.Errorf("example request is not valid JSON")
				}
				body.Request = json.RawMessage(rawRequest)
			}
			status, response, err := cfg.post(cmd.Context(), action, body)
			if err != nil {
				return err
			}
			if status < http.StatusOK || status >= http.StatusMultipleChoices {
				return httpErr(status, response)
			}
			failed, err := printHookPEPResponse(cmd, action, response)
			if err != nil {
				return err
			}
			if failed {
				if action == "validate" {
					return errHookPEPValidationFailed
				}
				return errHookPEPDenied
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&engine, "engine", "cedar", "policy engine: cedar or opa")
	cmd.Flags().StringVar(&source, "source", "", "inline policy source")
	cmd.Flags().StringVar(&sourceFile, "file", "", "policy source file ('-' reads stdin)")
	_ = cmd.RegisterFlagCompletionFunc("engine", completeHookPEPEngine)
	if action != "validate" {
		cmd.Flags().StringVar(&requestJSON, "request", "", "inline example-request JSON")
		cmd.Flags().StringVar(&requestFile, "request-file", "", "example-request JSON file ('-' reads stdin)")
	}
	return cmd
}

func hookPEPSourceExample(action string) string {
	if action == "validate" {
		return "  olivares hookpep validate --engine cedar --file policy.cedar -o json"
	}
	return fmt.Sprintf("  olivares hookpep %s --engine cedar --file policy.cedar --request-file request.json", action)
}

func newHookPEPPublishCmd(cfg *hookPEPClientConfig) *cobra.Command {
	var engine, source, sourceFile, note string
	cmd := &cobra.Command{
		Use:     "publish",
		Short:   "Compile, publish, and activate an authored policy revision",
		Long:    "Publish an authored policy through the control plane. The server owns the deny-closed compile-and-activation workflow (Cedar activates on the live engine; OPA remains sidecar-owned); the client holds no policy logic.",
		Example: "  olivares hookpep publish --engine cedar --file policy.cedar --note \"approved change\" -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			policy, err := readHookPEPValue(cmd.InOrStdin(), source, sourceFile, "policy source")
			if err != nil {
				return err
			}
			body := struct {
				Engine string `json:"engine"`
				Source string `json:"source"`
				Note   string `json:"note,omitempty"`
			}{Engine: engine, Source: policy, Note: note}
			status, response, err := cfg.post(cmd.Context(), "publish", body)
			if err != nil {
				return err
			}
			if status < http.StatusOK || status >= http.StatusMultipleChoices {
				return httpErr(status, response)
			}
			return printHookPEPPublishResponse(cmd, response)
		},
	}
	cmd.Flags().StringVar(&engine, "engine", "cedar", "policy engine: cedar or opa")
	cmd.Flags().StringVar(&source, "source", "", "inline policy source")
	cmd.Flags().StringVar(&sourceFile, "file", "", "policy source file ('-' reads stdin)")
	cmd.Flags().StringVar(&note, "note", "", "optional publication note")
	_ = cmd.RegisterFlagCompletionFunc("engine", completeHookPEPEngine)
	return cmd
}

func newHookPEPVersionsCmd(cfg *hookPEPClientConfig) *cobra.Command {
	return &cobra.Command{
		Use:     "versions",
		Short:   "List immutable authored policy revisions",
		Long:    "List immutable Cedar and OPA policy revisions stored by the control plane. The client only renders server-owned revision metadata and contains no policy logic.",
		Example: "  olivares hookpep versions -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			status, response, err := cfg.get(cmd.Context(), "versions", nil)
			if err != nil {
				return err
			}
			if status < http.StatusOK || status >= http.StatusMultipleChoices {
				return httpErr(status, response)
			}
			return printHookPEPVersionsResponse(cmd, response)
		},
	}
}

func newHookPEPTestsCmd(cfg *hookPEPClientConfig) *cobra.Command {
	var engine string
	var revision int64
	cmd := &cobra.Command{
		Use:     "tests",
		Short:   "Show the stored compile-validation artifact for a policy revision",
		Long:    "Show the compile-validation artifact stored by the control plane for an immutable policy revision. The server selects the newest revision when --revision is omitted; the client performs no policy tests.",
		Example: "  olivares hookpep tests --engine cedar --revision 7 -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			if cmd.Flags().Changed("revision") && revision <= 0 {
				return fmt.Errorf("--revision must be a positive integer")
			}
			query := url.Values{"engine": []string{engine}}
			if cmd.Flags().Changed("revision") {
				query.Set("revision", strconv.FormatInt(revision, 10))
			}
			status, response, err := cfg.get(cmd.Context(), "tests", query)
			if err != nil {
				return err
			}
			if status < http.StatusOK || status >= http.StatusMultipleChoices {
				return httpErr(status, response)
			}
			return printHookPEPTestsResponse(cmd, response)
		},
	}
	cmd.Flags().StringVar(&engine, "engine", "", "policy engine: cedar or opa")
	cmd.Flags().Int64Var(&revision, "revision", 0, "immutable policy revision (default newest)")
	_ = cmd.MarkFlagRequired("engine")
	_ = cmd.RegisterFlagCompletionFunc("engine", completeHookPEPEngine)
	return cmd
}

func completeHookPEPEngine(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	return []string{"cedar", "opa"}, cobra.ShellCompDirectiveNoFileComp
}

func newHookPEPRollbackCmd(cfg *hookPEPClientConfig) *cobra.Command {
	var engine string
	var revision int64
	cmd := &cobra.Command{
		Use:     "rollback",
		Short:   "Re-activate a prior immutable policy revision",
		Long:    "Re-activate a prior immutable policy revision through the control plane. The server re-runs the compile/validation gate and atomically audits the activation; the client contains no policy logic.",
		Example: "  olivares hookpep rollback --engine cedar --revision 7 -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			if revision <= 0 {
				return fmt.Errorf("--revision must be a positive integer")
			}
			status, response, err := cfg.post(cmd.Context(), "rollback", map[string]any{
				"engine": engine, "revision": revision,
			})
			if err != nil {
				return err
			}
			if status < http.StatusOK || status >= http.StatusMultipleChoices {
				return httpErr(status, response)
			}
			_, err = printHookPEPResponse(cmd, "rollback", response)
			return err
		},
	}
	cmd.Flags().StringVar(&engine, "engine", "cedar", "policy engine: cedar or opa")
	cmd.Flags().Int64Var(&revision, "revision", 0, "immutable policy revision to re-activate")
	_ = cmd.RegisterFlagCompletionFunc("engine", completeHookPEPEngine)
	return cmd
}

func readHookPEPValue(stdin io.Reader, inline, path, label string) (string, error) {
	if inline != "" && path != "" {
		return "", fmt.Errorf("set only one of inline %s or its file flag", label)
	}
	if inline != "" {
		return inline, nil
	}
	if path == "" {
		return "", fmt.Errorf("%s is required", label)
	}
	var (
		value []byte
		err   error
	)
	if path == "-" {
		value, err = io.ReadAll(io.LimitReader(stdin, 1<<20))
	} else {
		value, err = os.ReadFile(path)
	}
	if err != nil {
		return "", fmt.Errorf("read %s: %w", label, err)
	}
	return string(value), nil
}

func printHookPEPResponse(cmd *cobra.Command, action string, response []byte) (bool, error) {
	switch action {
	case "validate":
		var result struct {
			OK          *bool `json:"ok"`
			Diagnostics []struct {
				Message  string `json:"message"`
				Severity string `json:"severity"`
			} `json:"diagnostics"`
		}
		if err := json.Unmarshal(response, &result); err != nil || result.OK == nil {
			return false, fmt.Errorf("unparseable validate response")
		}
		err := renderOut(cmd, func(out io.Writer) error {
			verdict := "pass"
			if !*result.OK {
				verdict = "fail"
			}
			if _, err := fmt.Fprintf(out, "validation: %s\n", verdict); err != nil {
				return err
			}
			for _, diagnostic := range result.Diagnostics {
				if _, err := fmt.Fprintf(out, "%s: %s\n", diagnostic.Severity, diagnostic.Message); err != nil {
					return err
				}
			}
			return nil
		}, json.RawMessage(response))
		return !*result.OK, err
	case "dry-run", "explain":
		var result struct {
			Allow  *bool  `json:"allow"`
			Reason string `json:"reason"`
			Engine string `json:"engine"`
		}
		if err := json.Unmarshal(response, &result); err != nil || result.Allow == nil {
			return false, fmt.Errorf("unparseable %s response", action)
		}
		err := renderOut(cmd, func(out io.Writer) error {
			verdict := "allow"
			if !*result.Allow {
				verdict = "deny"
			}
			_, err := fmt.Fprintf(out, "decision: %s engine=%s\nreason: %s\n", verdict, result.Engine, result.Reason)
			return err
		}, json.RawMessage(response))
		return !*result.Allow, err
	case "rollback":
		var result struct {
			Engine       string `json:"engine"`
			FromRevision int64  `json:"from_revision"`
			ToRevision   int64  `json:"to_revision"`
			Active       bool   `json:"active"`
			Note         string `json:"note"`
		}
		if err := json.Unmarshal(response, &result); err != nil {
			return false, fmt.Errorf("unparseable rollback response")
		}
		err := renderOut(cmd, func(out io.Writer) error {
			if _, err := fmt.Fprintf(out, "rollback: engine=%s from=%d to=%d active=%t\n", result.Engine, result.FromRevision, result.ToRevision, result.Active); err != nil {
				return err
			}
			if result.Note != "" {
				_, err := fmt.Fprintln(out, result.Note)
				return err
			}
			return nil
		}, json.RawMessage(response))
		return false, err
	default:
		return false, fmt.Errorf("unknown hookpep action %q", action)
	}
}

func printHookPEPPublishResponse(cmd *cobra.Command, response []byte) error {
	var result struct {
		Engine   string `json:"engine"`
		Revision int64  `json:"revision"`
		Active   bool   `json:"active"`
		Note     string `json:"note"`
	}
	if err := json.Unmarshal(response, &result); err != nil || result.Engine == "" || result.Revision <= 0 {
		return fmt.Errorf("unparseable publish response")
	}
	return renderOut(cmd, func(out io.Writer) error {
		if _, err := fmt.Fprintf(out, "publish: engine=%s revision=%d active=%t\n", result.Engine, result.Revision, result.Active); err != nil {
			return err
		}
		if result.Note != "" {
			_, err := fmt.Fprintln(out, result.Note)
			return err
		}
		return nil
	}, json.RawMessage(response))
}

func printHookPEPVersionsResponse(cmd *cobra.Command, response []byte) error {
	var result struct {
		Items []struct {
			Revision  int64  `json:"revision"`
			Surface   string `json:"surface"`
			Validated bool   `json:"validated"`
			Active    bool   `json:"active"`
		} `json:"items"`
	}
	if err := json.Unmarshal(response, &result); err != nil || result.Items == nil {
		return fmt.Errorf("unparseable versions response")
	}
	return renderOut(cmd, func(out io.Writer) error {
		if len(result.Items) == 0 {
			_, err := fmt.Fprintln(out, "versions: none")
			return err
		}
		for _, revision := range result.Items {
			if _, err := fmt.Fprintf(out, "revision: engine=%s revision=%d validated=%t active=%t\n",
				revision.Surface, revision.Revision, revision.Validated, revision.Active); err != nil {
				return err
			}
		}
		return nil
	}, json.RawMessage(response))
}

func printHookPEPTestsResponse(cmd *cobra.Command, response []byte) error {
	var result struct {
		Engine    string `json:"engine"`
		Revision  int64  `json:"revision"`
		Available *bool  `json:"available"`
		Passed    int    `json:"passed"`
		Failed    int    `json:"failed"`
		Total     int    `json:"total"`
	}
	if err := json.Unmarshal(response, &result); err != nil || result.Engine == "" || result.Available == nil {
		return fmt.Errorf("unparseable tests response")
	}
	compiled := *result.Available && result.Total > 0 && result.Passed > 0 && result.Failed == 0
	return renderOut(cmd, func(out io.Writer) error {
		_, err := fmt.Fprintf(out, "tests: engine=%s revision=%d available=%t compiled=%t\n",
			result.Engine, result.Revision, *result.Available, compiled)
		return err
	}, json.RawMessage(response))
}
