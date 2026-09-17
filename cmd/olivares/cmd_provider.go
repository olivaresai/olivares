// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// cmd_provider.go is the CLI half of D19: registering the credential a session
// launches with, testing it, binding it to a provider profile and withdrawing it.
//
// Every verb is a thin HTTP client against /v1/m/sessions/providers*. The CLI
// stores nothing, seals nothing and never prints a credential.
//
// ⛔ THE CREDENTIAL IS NEVER A FLAG VALUE, and that is the one design decision in
// this file. `--key sk-ant-...` would land the credential in the shell history, in
// the process table where every other user on the host can read it, and in any
// command log the operator keeps. No amount of documentation undoes those three.
// It is read from STDIN by default, or from a named environment variable.

const providersPath = "/v1/m/sessions/providers"

// providerKindChoices is the closed set, with what each one injects, so the
// completion and the help answer "which one do I pick" without a docs round trip.
var providerKindChoices = []string{
	"anthropic\tClaude Code; injects ANTHROPIC_API_KEY",
	"openai\tCodex; injects OPENAI_API_KEY",
	"xai\tGrok; injects XAI_API_KEY",
	"openai_compatible\tany OpenAI-shaped endpoint; injects OPENAI_API_KEY and OPENAI_BASE_URL (--base-url required)",
}

func newProviderCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "provider",
		Short: "Register, test and withdraw the provider credentials sessions launch with",
		Long: "provider is where an API key becomes something the product owns: the engine seals it,\n" +
			"reports a four-character hint, can test the connection, and hands it to a session at\n" +
			"launch. Nothing here needs a variable in the server's shell.\n\n" +
			"A provider is a CREDENTIAL (anthropic, openai, xai, or any OpenAI-shaped endpoint). A\n" +
			"provider PROFILE is which home directory an official CLI runs under. `provider bind`\n" +
			"joins the two, and a session launched under that profile uses that credential.\n\n" +
			"The key is never a flag value: it is read from stdin, or from the environment variable\n" +
			"named by --key-env. Nothing in this command prints a credential.",
		Example: "  olivares provider add --kind anthropic --name \"Anthropic (prod)\" < key.txt\n" +
			"  olivares provider ls\n" +
			"  olivares provider test prv_01J8...\n" +
			"  olivares provider bind prv_01J8... --profile ppf_01J8...\n" +
			"  olivares provider rm prv_01J8... --yes",
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(
		newProviderAddCmd(), newProviderListCmd(), newProviderGetCmd(),
		newProviderTestCmd(), newProviderRotateCmd(), newProviderBindCmd(), newProviderRemoveCmd(),
	)
	return cmd
}

// readProviderKey resolves the credential from stdin or from a named environment
// variable, and refuses every other source.
//
// A TERMINAL is refused rather than prompted: a prompt on a TTY is a fine feature
// and it is not this one, and printing "waiting for input" while a script hangs is
// the failure mode of guessing. The refusal names both accepted forms.
func readProviderKey(cmd *cobra.Command, keyEnv string) (string, error) {
	if name := strings.TrimSpace(keyEnv); name != "" {
		value := strings.TrimSpace(os.Getenv(name))
		if value == "" {
			return "", exitcode.New(exitcode.Usage,
				fmt.Errorf("--key-env names %s, and that variable is empty or unset in this shell", name))
		}
		return value, nil
	}
	in := cmd.InOrStdin()
	// The TTY check is on the command's OWN input and not on the process's: this
	// command is driven through exactly this seam from a test and from a script, and
	// consulting os.Stdin here would refuse both while accepting neither.
	if f, ok := in.(*os.File); ok {
		if stat, serr := f.Stat(); serr == nil && stat.Mode()&os.ModeCharDevice != 0 {
			return "", exitcode.New(exitcode.Usage, fmt.Errorf(
				"the credential is read from stdin or from --key-env NAME, never from a flag "+
					"(a flag value lands in the shell history and in the process table): "+
					"pipe it in, redirect a file, or name an environment variable"))
		}
	}
	raw, err := io.ReadAll(io.LimitReader(in, 64<<10))
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(raw))
	if value == "" {
		return "", exitcode.New(exitcode.Usage, fmt.Errorf("no credential arrived on stdin"))
	}
	return value, nil
}

func addProviderKeyFlags(cmd *cobra.Command, keyEnv *string) {
	cmd.Flags().StringVar(keyEnv, "key-env", "",
		"read the credential from this environment variable instead of stdin (never pass the key as a flag value)")
}

func newProviderAddCmd() *cobra.Command {
	var (
		cfg                   agentClientConfig
		kind, name, baseURL   string
		keyEnv                string
		bindProfile           string
		errNotAProviderRecord = "the control plane did not return a provider record"
	)
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Register a provider credential with the control plane",
		Long: "add seals one provider credential in the engine and returns its reference and a\n" +
			"four-character hint. The value is never returned, logged or printed again.\n\n" +
			"With --profile the new provider is bound to that provider profile in the same run, so\n" +
			"the next session launched under it uses this credential. Without it, bind later with\n" +
			"`olivares provider bind`.",
		Example: "  olivares provider add --kind anthropic --name \"Anthropic (prod)\" < key.txt\n" +
			"  OPENAI_KEY=sk-... olivares provider add --kind openai --name Codex --key-env OPENAI_KEY\n" +
			"  olivares provider add --kind openai_compatible --name Local --base-url https://llm.example.com < key.txt",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			key, err := readProviderKey(cmd, keyEnv)
			if err != nil {
				return err
			}
			status, b, err := cfg.do(cmd.Context(), "POST", providersPath, map[string]any{
				"kind": kind, "display_name": name, "base_url": baseURL, "api_key": key,
			})
			if err != nil {
				return err
			}
			if status != 201 {
				return httpErr(status, b)
			}
			var rec map[string]any
			if uerr := json.Unmarshal(b, &rec); uerr != nil {
				return fmt.Errorf("%s: %w", errNotAProviderRecord, uerr)
			}
			ref := str(rec, "provider_ref")
			if bindProfile != "" && ref != "" {
				if berr := bindProviderToProfile(cmd, &cfg, ref, bindProfile); berr != nil {
					return berr
				}
			}
			return renderOut(cmd, func(w io.Writer) error {
				return printProviderRecord(w, rec, bindProfile)
			}, rec)
		},
	}
	cfg.addFlags(cmd)
	cmd.Flags().StringVar(&kind, "kind", "", "anthropic | openai | xai | openai_compatible")
	cmd.Flags().StringVar(&name, "name", "", "your own name for this credential; it is what a picker shows")
	cmd.Flags().StringVar(&baseURL, "base-url", "", "https endpoint override (required for openai_compatible)")
	cmd.Flags().StringVar(&bindProfile, "profile", "", "provider profile reference to bind this credential to in the same run")
	addProviderKeyFlags(cmd, &keyEnv)
	_ = cmd.MarkFlagRequired("kind")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.RegisterFlagCompletionFunc("kind", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return providerKindChoices, cobra.ShellCompDirectiveNoFileComp
	})
	return cmd
}

func newProviderListCmd() *cobra.Command {
	var (
		cfg         agentClientConfig
		state, kind string
	)
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List the registered provider credentials",
		Long: "ls shows each registered provider, its kind, the four-character hint, and what its\n" +
			"last connection test found. A provider nobody has tested says so; it never reads as\n" +
			"working because it was registered.",
		Example: "  olivares provider ls\n  olivares provider ls --kind anthropic -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			path := providersPath
			if q := providerListQuery(state, kind); q != "" {
				path += "?" + q
			}
			status, b, err := cfg.do(cmd.Context(), "GET", path, nil)
			if err != nil {
				return err
			}
			if status != 200 {
				return httpErr(status, b)
			}
			var page struct {
				Items []map[string]any `json:"items"`
			}
			if uerr := json.Unmarshal(b, &page); uerr != nil {
				return uerr
			}
			return renderOut(cmd, func(w io.Writer) error {
				return printProviderTable(w, page.Items)
			}, page.Items)
		},
	}
	cfg.addFlags(cmd)
	cmd.Flags().StringVar(&state, "state", "", "only providers in this state (active or revoked)")
	cmd.Flags().StringVar(&kind, "kind", "", "only providers of this kind")
	addDeprecatedJSONFlag(cmd)
	return cmd
}

func providerListQuery(state, kind string) string {
	parts := make([]string, 0, 2)
	if s := strings.TrimSpace(state); s != "" {
		parts = append(parts, "state="+s)
	}
	if k := strings.TrimSpace(kind); k != "" {
		parts = append(parts, "kind="+k)
	}
	return strings.Join(parts, "&")
}

func newProviderGetCmd() *cobra.Command {
	var cfg agentClientConfig
	cmd := &cobra.Command{
		Use:     "get <provider-ref>",
		Short:   "Show one registered provider",
		Long:    "get prints one provider's kind, name, endpoint, hint and last test result. Never its value.",
		Example: "  olivares provider get prv_01J8ABCDEF -o json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			return providerPointCall(cmd, &cfg, "GET", "/"+args[0], nil, 200)
		},
	}
	cfg.addFlags(cmd)
	addDeprecatedJSONFlag(cmd)
	return cmd
}

func newProviderTestCmd() *cobra.Command {
	var cfg agentClientConfig
	cmd := &cobra.Command{
		Use:   "test <provider-ref>",
		Short: "Ask the provider which models it serves, with the registered credential",
		Long: "test performs one model-list call against the provider and records what came back.\n" +
			"It sends NO completion and spends nothing.\n\n" +
			"Three outcomes, and they are not the same question: `ok` means the provider answered\n" +
			"and listed its models; `refused` means the provider answered and rejected this\n" +
			"credential; `unreachable` means no answer was obtained, which is not a verdict about\n" +
			"the credential at all.",
		Example: "  olivares provider test prv_01J8ABCDEF",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			return providerPointCall(cmd, &cfg, "POST", "/"+args[0]+"/test", nil, 200)
		},
	}
	cfg.addFlags(cmd)
	addDeprecatedJSONFlag(cmd)
	return cmd
}

func newProviderRotateCmd() *cobra.Command {
	var (
		cfg    agentClientConfig
		keyEnv string
	)
	cmd := &cobra.Command{
		Use:   "rotate <provider-ref>",
		Short: "Replace a provider's credential in place",
		Long: "rotate reseals a new value under the same provider reference, so every profile bound\n" +
			"to it keeps working and the NEXT session launched uses the new credential. A session\n" +
			"already running keeps the credential it started with.\n\n" +
			"The previous connection test is cleared: a verdict measured on a credential that no\n" +
			"longer exists is not evidence about the one that replaced it.",
		Example: "  olivares provider rotate prv_01J8ABCDEF < new-key.txt",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			key, err := readProviderKey(cmd, keyEnv)
			if err != nil {
				return err
			}
			return providerPointCall(cmd, &cfg, "PATCH", "/"+args[0], map[string]any{"api_key": key}, 200)
		},
	}
	cfg.addFlags(cmd)
	addProviderKeyFlags(cmd, &keyEnv)
	addDeprecatedJSONFlag(cmd)
	return cmd
}

func newProviderBindCmd() *cobra.Command {
	var (
		cfg     agentClientConfig
		profile string
		unbind  bool
	)
	cmd := &cobra.Command{
		Use:   "bind <provider-ref>",
		Short: "Make a provider profile launch with this credential",
		Long: "bind joins a registered provider to a provider profile. From then on a session\n" +
			"launched under that profile resolves its credential from this provider, and the host's\n" +
			"own credential variables are not consulted for it.\n\n" +
			"The engine refuses a credential the profile's driver cannot read — an OpenAI key on a\n" +
			"Claude profile is a 422 that names both, not a launch that fails later.\n\n" +
			"--unbind removes the binding and returns the profile to the host-wide credential.",
		Example: "  olivares provider bind prv_01J8ABCDEF --profile ppf_01J8ZZZZZZ\n" +
			"  olivares provider bind --unbind --profile ppf_01J8ZZZZZZ",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			ref := ""
			if len(args) == 1 {
				ref = args[0]
			}
			switch {
			case unbind && ref != "":
				return sessionCLIUsage("--unbind takes no provider reference: it clears whatever the profile names")
			case !unbind && ref == "":
				return sessionCLIUsage("name the provider to bind, or pass --unbind to clear the profile's binding")
			}
			return bindProviderToProfile(cmd, &cfg, ref, profile)
		},
	}
	cfg.addFlags(cmd)
	cmd.Flags().StringVar(&profile, "profile", "", "provider profile reference")
	cmd.Flags().BoolVar(&unbind, "unbind", false, "clear the profile's binding instead of setting one")
	_ = cmd.MarkFlagRequired("profile")
	addDeprecatedJSONFlag(cmd)
	return cmd
}

// bindProviderToProfile patches the profile's provider_record_ref. An empty ref
// unbinds, which is why the caller and not this function decides what empty means.
func bindProviderToProfile(cmd *cobra.Command, cfg *agentClientConfig, providerRef, profileRef string) error {
	if strings.TrimSpace(profileRef) == "" {
		return sessionCLIUsage("--profile is required to bind a provider")
	}
	status, b, err := cfg.do(cmd.Context(), "PATCH",
		"/v1/m/sessions/provider-profiles/"+profileRef,
		map[string]any{"provider_record_ref": providerRef})
	if err != nil {
		return err
	}
	if status != 200 {
		return httpErr(status, b)
	}
	return nil
}

func newProviderRemoveCmd() *cobra.Command {
	var (
		cfg agentClientConfig
		yes bool
	)
	cmd := &cobra.Command{
		Use:     "rm <provider-ref>",
		Aliases: []string{"revoke"},
		Short:   "Withdraw a provider credential for good",
		Long: "rm destroys the sealed credential and marks the provider revoked. It is irreversible\n" +
			"for that reference.\n\n" +
			"The record itself is KEPT, and so is every profile binding that names it: a session\n" +
			"launched under such a profile is then refused by name, which an operator can act on.\n" +
			"A binding that silently vanished would present as a profile nobody had configured.\n\n" +
			"The credential is not revoked at the provider. Do that in the provider's own console.",
		Example: "  olivares provider rm prv_01J8ABCDEF --yes",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				return sessionCLIUsage("this destroys the sealed credential and cannot be undone: pass --yes")
			}
			if err := cfg.resolve(); err != nil {
				return err
			}
			return providerPointCall(cmd, &cfg, "POST", "/"+args[0]+"/revoke", nil, 200)
		},
	}
	cfg.addFlags(cmd)
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm the irreversible withdrawal")
	addDeprecatedJSONFlag(cmd)
	return cmd
}

// providerPointCall performs one request against a single provider and renders the
// record it answered with.
func providerPointCall(cmd *cobra.Command, cfg *agentClientConfig, method, suffix string, body any, want int) error {
	status, b, err := cfg.do(cmd.Context(), method, providersPath+suffix, body)
	if err != nil {
		return err
	}
	if status != want {
		return httpErr(status, b)
	}
	var rec map[string]any
	if uerr := json.Unmarshal(b, &rec); uerr != nil {
		return uerr
	}
	return renderOut(cmd, func(w io.Writer) error {
		return printProviderRecord(w, rec, "")
	}, rec)
}

// probeSentence turns the stored probe columns into one line an operator can act
// on. "never tested" is its own answer and never reads as a failure.
func probeSentence(rec map[string]any) string {
	switch str(rec, "probe_state") {
	case "ok":
		return "tested: the provider answered and accepted this credential"
	case "refused":
		return "tested: the provider REFUSED this credential — replace it (the endpoint was reachable)"
	case "unreachable":
		return "tested: the endpoint could not be reached — this says nothing about the credential"
	default:
		return "not tested yet: run `olivares provider test <provider-ref>`"
	}
}

func printProviderRecord(w io.Writer, rec map[string]any, boundProfile string) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "PROVIDER\t%s\n", str(rec, "provider_ref"))
	fmt.Fprintf(tw, "NAME\t%s\n", str(rec, "display_name"))
	fmt.Fprintf(tw, "KIND\t%s\n", str(rec, "kind"))
	if base := str(rec, "base_url"); base != "" {
		fmt.Fprintf(tw, "ENDPOINT\t%s\n", base)
	}
	fmt.Fprintf(tw, "KEY\t%s\n", str(rec, "key_hint"))
	fmt.Fprintf(tw, "STATE\t%s\n", str(rec, "state"))
	fmt.Fprintf(tw, "CONNECTION\t%s\n", probeSentence(rec))
	if detail := str(rec, "probe_detail"); detail != "" {
		fmt.Fprintf(tw, "DETAIL\t%s\n", detail)
	}
	if models, ok := rec["models"].([]any); ok && len(models) > 0 {
		names := make([]string, 0, len(models))
		for _, m := range models {
			if s, ok := m.(string); ok {
				names = append(names, s)
			}
		}
		fmt.Fprintf(tw, "MODELS\t%d (%s)\n", len(names), strings.Join(firstN(names, 5), ", "))
	}
	if boundProfile != "" {
		fmt.Fprintf(tw, "BOUND TO\t%s\n", boundProfile)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	// The next action, named. A screen that ends here is the screen this lane
	// exists to remove.
	if str(rec, "state") == "active" && str(rec, "probe_state") == "" {
		fmt.Fprintf(w, "\nNext: olivares provider test %s\n", str(rec, "provider_ref"))
	} else if boundProfile == "" && str(rec, "state") == "active" {
		fmt.Fprintf(w, "\nNext: olivares provider bind %s --profile <provider-profile-ref>\n", str(rec, "provider_ref"))
	}
	return nil
}

func printProviderTable(w io.Writer, items []map[string]any) error {
	if len(items) == 0 {
		fmt.Fprintln(w, "No providers registered.")
		fmt.Fprintln(w, "Next: olivares provider add --kind anthropic --name \"Anthropic\" < key.txt")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PROVIDER\tNAME\tKIND\tKEY\tSTATE\tCONNECTION")
	for _, rec := range items {
		connection := str(rec, "probe_state")
		if connection == "" {
			connection = "not tested"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			str(rec, "provider_ref"), str(rec, "display_name"), str(rec, "kind"),
			str(rec, "key_hint"), str(rec, "state"), connection)
	}
	return tw.Flush()
}

func firstN(in []string, n int) []string {
	if len(in) <= n {
		return in
	}
	return append(append([]string(nil), in[:n]...), "…")
}
