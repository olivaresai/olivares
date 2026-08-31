// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/core/auth"
)

// The `olivares secrets` command: offline CRUD over the runtime secret store
// (the sealed secrets an operator references from connector configs as
// `store:<name>`). It opens the same store and engine-held sealer the running
// engine uses, so a secret stored here resolves at the next Open. On SQLite the
// engine is single-writer, so run these against a STOPPED engine (or manage
// secrets live through the console/API while it runs); on Postgres they are safe
// alongside a running engine.
func newSecretsCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "secrets",
		Short: "Manage the runtime secret store (sealed; referenced from configs as store:<name>)",
		Example: "  olivares secrets ls --data-dir /var/lib/olivares\n" +
			"  olivares secrets put --name vault/token --value-file /run/secrets/vault-token\n" +
			"  olivares secrets rotate --name vault/token --value-file /run/secrets/vault-token-next",
		Long: "Store credentials sealed at rest instead of by value in a config file. A connector,\n" +
			"notify destination, identity provider or MCP/agent config then carries the reference\n" +
			"store:<name> (or db:<name>), which the engine resolves to the live value at Open.",
	}
	addTextJSONFormatFlag(root)
	root.AddCommand(secretsListCmd(), secretsPutCmd(), secretsRotateCmd(), secretsRemoveCmd())
	return root
}

func secretsListCmd() *cobra.Command {
	var dataDir, engine, dsn string
	cmd := &cobra.Command{
		Use:     "ls",
		Short:   "List stored secrets (names and non-secret hints; never the value)",
		Long:    "ls lists stored secret names, non-secret hints, descriptions and update times without revealing any secret value.",
		Example: "  olivares secrets ls --data-dir /var/lib/olivares",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			eng, err := auditBootRO(cmd, dataDir, engine, dsn)
			if err != nil {
				return err
			}
			defer func() { _ = eng.Close() }()
			views, err := eng.secretStore.List(cmd.Context(), auth.GlobalSecretScope)
			if err != nil {
				return err
			}
			items := make([]secretListItem, 0, len(views))
			for _, v := range views {
				updated := ""
				if !v.UpdatedAt.IsZero() {
					updated = v.UpdatedAt.String()
				}
				items = append(items, secretListItem{
					Name: v.Name, Hint: v.Hint, Description: v.Description, UpdatedAt: updated,
				})
			}
			return renderOut(cmd, func(out io.Writer) error {
				if len(views) == 0 {
					_, err := fmt.Fprintln(out, "no secrets stored")
					return err
				}
				tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
				fmt.Fprintln(tw, "NAME\tHINT\tDESCRIPTION\tUPDATED")
				for _, item := range items {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", item.Name, item.Hint, item.Description, item.UpdatedAt)
				}
				return tw.Flush()
			}, items)
		},
	}
	addStoreFlags(cmd, &dataDir, &engine, &dsn)
	return cmd
}

type secretListItem struct {
	Name        string `json:"name"`
	Hint        string `json:"hint"`
	Description string `json:"description"`
	UpdatedAt   string `json:"updated_at"`
}

// secretMutationResult is what `put` and `rotate` report: the two facts their text
// has always printed, and no third one. The HINT is the store's own published
// fingerprint (a truncated SHA-256 of the value, core/auth/secret_store.go:193) —
// already non-secret by product decision, already in `secrets ls`, and the only
// thing here derived from the value at all. The VALUE never enters this struct:
// both commands hold it as a live local variable when they build the report, which
// is precisely why its absence is pinned by a witness and not by good intentions.
//
// put and rotate share one struct and therefore render identically. That is the
// house form, not laziness: the majority form adds no `status`, no `action` and no
// root wrapper of the CLI's own invention, so which verb ran stays where it already
// is — in the command the operator invoked — instead of being restated inside the
// payload where it would be a second, drift-prone source of truth.
type secretMutationResult struct {
	Name string `json:"name"`
	Hint string `json:"hint"`
}

// secretDeleteResult has no hint, exactly as `rm`'s text has none. A delete never
// reads the sealed value, so it has nothing to fingerprint — and a hint here would
// be a fingerprint of a secret this command is not entitled to open.
type secretDeleteResult struct {
	Name string `json:"name"`
}

func secretsPutCmd() *cobra.Command {
	var dataDir, engine, dsn, name, value, valueFile, description string
	var actorFlag, reasonFlag string
	cmd := &cobra.Command{
		Use:   "put",
		Short: "Create or update a secret (seals the value at rest)",
		Long: "Provide the value with --value, --value-file <path>, or --value-file - (stdin).\n" +
			"On an existing secret, an empty value keeps the stored value (edit --description only).",
		Example: `  # Store a secret from a file (keeps it out of shell history)
  olivares secrets put --name vault/token --value-file /run/secrets/vault-token

  # Store a secret from stdin
  echo -n "s3cr3t" | olivares secrets put --name db/password --value-file - --description "Postgres app password"`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			val, err := readSecretValue(cmd, value, valueFile)
			if err != nil {
				return err
			}
			eng, err := auditBoot(cmd, dataDir, engine, dsn)
			if err != nil {
				return err
			}
			defer func() { _ = eng.Close() }()
			// Attribution is checked here — after the consent gate and after the store
			// opens — because three refusals compete and the operator must hear them in
			// the order they apply: do you mean it, is there a store, and only then who
			// are you. Putting this first made `secrets rm` answer "--actor" to somebody
			// who had not stated intent, and made an uninitialised store report an actor
			// problem. It is no less fail-closed here: nothing has been mutated yet.
			op, err := requireLocalActor(viaCLISecrets, actorFlag, reasonFlag)
			if err != nil {
				return err
			}
			view, err := eng.secretStore.Put(cmd.Context(), op, auth.GlobalSecretScope, name, val, description)
			if err != nil {
				return err
			}
			res := secretMutationResult{Name: view.Name, Hint: view.Hint}
			return renderOut(cmd, func(w io.Writer) error {
				_, werr := fmt.Fprintf(w, "stored secret %q (hint %s)\n", res.Name, res.Hint)
				return werr
			}, res)
		},
	}
	addStoreFlags(cmd, &dataDir, &engine, &dsn)
	addLocalActorFlags(cmd, &actorFlag, &reasonFlag)
	cmd.Flags().StringVar(&name, "name", "", "secret name (referenced as store:<name>)")
	cmd.Flags().StringVar(&value, "value", "", "secret value (prefer --value-file to keep it out of shell history)")
	cmd.Flags().StringVar(&valueFile, "value-file", "", "read the value from a file, or - for stdin")
	cmd.Flags().StringVar(&description, "description", "", "optional non-secret note")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func secretsRotateCmd() *cobra.Command {
	var dataDir, engine, dsn, name, value, valueFile string
	var actorFlag, reasonFlag string
	cmd := &cobra.Command{
		Use:     "rotate",
		Short:   "Replace a secret's value (a new value is required)",
		Long:    "rotate replaces an existing sealed secret value while preserving its description; the new value is required.",
		Example: "  olivares secrets rotate --name vault/token --value-file /run/secrets/vault-token-next",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			val, err := readSecretValue(cmd, value, valueFile)
			if err != nil {
				return err
			}
			if val == "" {
				return fmt.Errorf("rotate needs a new value (--value or --value-file)")
			}
			eng, err := auditBoot(cmd, dataDir, engine, dsn)
			if err != nil {
				return err
			}
			defer func() { _ = eng.Close() }()
			// Attribution is checked here — after the consent gate and after the store
			// opens — because three refusals compete and the operator must hear them in
			// the order they apply: do you mean it, is there a store, and only then who
			// are you. Putting this first made `secrets rm` answer "--actor" to somebody
			// who had not stated intent, and made an uninitialised store report an actor
			// problem. It is no less fail-closed here: nothing has been mutated yet.
			op, err := requireLocalActor(viaCLISecrets, actorFlag, reasonFlag)
			if err != nil {
				return err
			}
			// Keep the existing description: read it, then put the new value.
			existing, _, gerr := eng.secretStore.Get(cmd.Context(), auth.GlobalSecretScope, name)
			if gerr != nil {
				return gerr
			}
			view, err := eng.secretStore.Put(cmd.Context(), op, auth.GlobalSecretScope, name, val, existing.Description)
			if err != nil {
				return err
			}
			res := secretMutationResult{Name: view.Name, Hint: view.Hint}
			return renderOut(cmd, func(w io.Writer) error {
				_, werr := fmt.Fprintf(w, "rotated secret %q (hint %s)\n", res.Name, res.Hint)
				return werr
			}, res)
		},
	}
	addStoreFlags(cmd, &dataDir, &engine, &dsn)
	addLocalActorFlags(cmd, &actorFlag, &reasonFlag)
	cmd.Flags().StringVar(&name, "name", "", "secret name")
	cmd.Flags().StringVar(&value, "value", "", "new secret value (prefer --value-file)")
	cmd.Flags().StringVar(&valueFile, "value-file", "", "read the new value from a file, or - for stdin")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func secretsRemoveCmd() *cobra.Command {
	var dataDir, engine, dsn, name string
	var actorFlag, reasonFlag string
	var yes bool
	cmd := &cobra.Command{
		Use:     "rm",
		Short:   "Delete a secret (a reference to it then fails closed)",
		Long:    "rm deletes one stored secret so subsequent store:<name> or db:<name> resolution fails closed.",
		Example: "  olivares secrets rm --name vault/retired-token",
		Args:    cobra.NoArgs,
		Aliases: []string{"remove", "delete"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Deleting a secret does not just remove a row: every store:<name>
			// reference to it starts failing closed, so a connector can stop
			// working somewhere the operator is not looking.
			if err := confirmDestructive(cmd, yes, fmt.Sprintf(
				"delete secret %q (every store:%s reference then fails closed)", name, name)); err != nil {
				return err
			}
			eng, err := auditBoot(cmd, dataDir, engine, dsn)
			if err != nil {
				return err
			}
			defer func() { _ = eng.Close() }()
			// Attribution is checked here — after the consent gate and after the store
			// opens — because three refusals compete and the operator must hear them in
			// the order they apply: do you mean it, is there a store, and only then who
			// are you. Putting this first made `secrets rm` answer "--actor" to somebody
			// who had not stated intent, and made an uninitialised store report an actor
			// problem. It is no less fail-closed here: nothing has been mutated yet.
			op, err := requireLocalActor(viaCLISecrets, actorFlag, reasonFlag)
			if err != nil {
				return err
			}
			if err := eng.secretStore.Delete(cmd.Context(), op, auth.GlobalSecretScope, name); err != nil {
				return err
			}
			// The confirmation prompt confirmDestructive writes goes to STDERR
			// (confirm.go:79), which is what leaves stdout parseable here: a prompt on
			// stdout would make `secrets rm -o json` emit a question before its object.
			res := secretDeleteResult{Name: name}
			return renderOut(cmd, func(w io.Writer) error {
				_, werr := fmt.Fprintf(w, "deleted secret %q\n", res.Name)
				return werr
			}, res)
		},
	}
	addStoreFlags(cmd, &dataDir, &engine, &dsn)
	addLocalActorFlags(cmd, &actorFlag, &reasonFlag)
	cmd.Flags().StringVar(&name, "name", "", "secret name")
	addYesFlag(cmd, &yes)
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

// readSecretValue resolves the value from --value-file (a path, or - for stdin)
// when set, else --value. A file's single trailing newline is trimmed. An empty
// result is allowed (put treats it as "keep existing"); the caller enforces any
// non-empty requirement.
func readSecretValue(cmd *cobra.Command, value, valueFile string) (string, error) {
	if valueFile == "" {
		return value, nil
	}
	var b []byte
	var err error
	if valueFile == "-" {
		b, err = io.ReadAll(cmd.InOrStdin())
	} else {
		b, err = os.ReadFile(valueFile)
	}
	if err != nil {
		return "", fmt.Errorf("read value: %w", err)
	}
	return strings.TrimRight(string(b), "\r\n"), nil
}
