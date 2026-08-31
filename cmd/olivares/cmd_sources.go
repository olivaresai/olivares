// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/auth"
)

// The `olivares sources` command: offline CRUD over the DURABLE SOURCE ROSTER
// (the store-backed successor to the file's `sources[]`). It opens the same store
// the running engine reconciles from, so a source added/edited here takes effect
// on a running server at its next reload — POST /v1/console/runtime/reload or
// `kill -HUP <pid>` — or at the next boot. A row carries connector settings and
// secret REFERENCES (e.g. token=store:vault/token), never a literal secret value;
// store the value with `olivares secrets`. On SQLite the engine is single-writer,
// so run these against a STOPPED engine (or author live through the console/API
// while it runs); on Postgres they are safe alongside a running engine.
func newSourcesCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "sources",
		Short: "Manage the durable source roster (connectors the engine ingests from)",
		Example: "  olivares sources ls --data-dir /var/lib/olivares\n" +
			"  olivares sources plan --name vault-prod --tenant t_abc123\n" +
			"  olivares sources set --name vault-prod --kind vault --tenant t_abc123\n" +
			"  olivares sources rm --name vault-prod",
		Long: "Author the observation-source connectors the engine ingests from, persisted so they\n" +
			"survive a restart and reconcile into a running engine WITHOUT one (reload via the API\n" +
			"or SIGHUP). Config carries secret REFERENCES (store:<name>), never values.\n\n" +
			"Changing where a tenant's data comes from is a change of PROVENANCE, so the group is\n" +
			"split by what each verb costs: `plan` says what would change and writes nothing,\n" +
			"`validate` says the configuration is coherent by itself without touching the network,\n" +
			"`test` opens the source for real to prove it answers, and `set` applies.",
	}
	root.AddCommand(sourcesListCmd(), sourcesGetCmd(), sourcesPlanCmd(), sourcesValidateCmd(), sourcesTestCmd(), sourcesSetCmd(), sourcesRemoveCmd())
	return root
}

// sourceListItem is the roster row as `sources ls -o json` renders it. The text
// table and the JSON come from this one value, so the two can never disagree.
type sourceListItem struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Tenant      string `json:"tenant"`
	Mode        string `json:"mode"`
	PollSeconds int    `json:"poll_seconds"`
	Enabled     bool   `json:"enabled"`
}

func sourcesListCmd() *cobra.Command {
	var dataDir, engine, dsn string
	cmd := &cobra.Command{
		Use:     "ls",
		Short:   "List the source roster (name, kind, tenant, mode, poll, enabled)",
		Long:    "ls lists every durable source definition, including its connector kind, tenant, mode, polling interval and enabled state.",
		Example: "  olivares sources ls --data-dir /var/lib/olivares",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			eng, err := auditBootRO(cmd, dataDir, engine, dsn)
			if err != nil {
				return err
			}
			defer func() { _ = eng.Close() }()
			rows, err := eng.sourceStore.List(cmd.Context(), auth.GlobalSourceScope)
			if err != nil {
				return err
			}
			// E2: through renderOut, so the global -o/--output the root help
			// advertises is honored here. It was not: this listing printed its
			// table whatever -o said, and `-o json` produced the same columns.
			items := make([]sourceListItem, 0, len(rows))
			for _, r := range rows {
				kind := r.Kind
				if r.Plugin != nil {
					kind = "plugin:" + r.Plugin.Path
				}
				items = append(items, sourceListItem{
					Name: r.Name, Kind: kind, Tenant: r.Tenant,
					Mode: sourceModeFromConfig(r.Config), PollSeconds: r.PollSeconds, Enabled: r.Enabled,
				})
			}
			return renderOut(cmd, func(out io.Writer) error {
				if len(items) == 0 {
					_, err := fmt.Fprintln(out, "no sources in the roster")
					return err
				}
				tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
				fmt.Fprintln(tw, "NAME\tKIND\tTENANT\tMODE\tPOLL\tENABLED")
				for _, it := range items {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%ds\t%t\n",
						it.Name, it.Kind, it.Tenant, it.Mode, it.PollSeconds, it.Enabled)
				}
				return tw.Flush()
			}, items)
		},
	}
	addStoreFlags(cmd, &dataDir, &engine, &dsn)
	return cmd
}

// sourceGetItem is what `sources get` renders: the roster row `ls` already shows
// PLUS the config it cannot. The text table and the JSON come from this one value,
// so the two can never disagree — the same rule sourceListItem states.
type sourceGetItem struct {
	Name        string            `json:"name"`
	Kind        string            `json:"kind"`
	Tenant      string            `json:"tenant"`
	Mode        string            `json:"mode"`
	PollSeconds int               `json:"poll_seconds"`
	Enabled     bool              `json:"enabled"`
	Config      map[string]string `json:"config"`
}

// sourcesGetCmd closes the gap docs/CLI-VERB-PARITY.md records as a REAL GAP: a
// source's configuration is writable and not readable. `ls` renders six columns and
// not the config (:49-56), and `plan` does not fill the hole — on an unchanged row it
// prints NO-OP with an empty change set, because it reports a DIFF, not a state.
//
// Why it matters beyond ergonomics: the roster's contract is that config carries
// REFERENCES, never values (:18-26). An operator who cannot see WHICH reference a
// source resolves at Open cannot audit that contract.
//
// ⛔ AND IT MASKS THROUGH `planValue`, WHICH IS NOT A COURTESY. The parity note states
// the constraint: a row written before the inline-secret guard existed can still hold a
// literal, so printing config verbatim would publish to stdout — and to `-o json` — a
// credential that `set` would refuse today. Reusing the plan's rule rather than a fresh
// one is deliberate: `planValue` asks the connector's OWN secret declaration first, and
// the sol-max contrast caught an earlier version that skipped exactly that and printed a
// `ghp_...` in full. One masking rule, one place to fix it.
//
// ⚠ Y EL LÍMITE DE ESA REGLA SE DICE AQUÍ, porque este verbo invita a confiar en ella más
// que `plan`: para una fuente de PLUGIN, `secretConfigKeys` devuelve el conjunto VACÍO
// (cmd_sources_plan.go:289-291 — el descriptor del conector vive en un binario externo y no
// se puede consultar en proceso). Ahí el enmascarado se apoya sólo en las dos heurísticas,
// así que un literal en una clave que ninguna reconozca se imprimiría. Es la conducta que
// `plan` ya tiene y no la cambio por mi cuenta; queda escrita para que quien lea `get` no
// suponga que la máscara es completa.
func sourcesGetCmd() *cobra.Command {
	var dataDir, engine, dsn string
	cmd := &cobra.Command{
		Use:   "get <name>",
		Short: "Show one source's definition, including the config `ls` cannot render",
		Long: "get prints a single source definition by name: the roster columns plus its connector\n" +
			"configuration, with any value the write path would refuse masked as it is in `plan`.\n" +
			"Use it to audit which secret REFERENCE a source resolves — `ls` does not render config,\n" +
			"and `plan` reports a diff, so an unchanged source shows NO-OP and an empty change set.",
		Example: "  olivares sources get github-main --data-dir /var/lib/olivares",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := auditBootRO(cmd, dataDir, engine, dsn)
			if err != nil {
				return err
			}
			defer func() { _ = eng.Close() }()
			def, found, err := eng.sourceStore.Get(cmd.Context(), auth.GlobalSourceScope, args[0])
			if err != nil {
				return err
			}
			// NotFound(4) and not the generic 1: a script that asks for a source which is
			// not in the roster must be able to tell that from a store it could not read.
			if !found {
				return exitcode.New(exitcode.NotFound,
					fmt.Errorf("no source named %q in the roster", args[0]))
			}
			kind := def.Kind
			if def.Plugin != nil {
				kind = "plugin:" + def.Plugin.Path
			}
			secretKeys := secretConfigKeys(def)
			cfg := make(map[string]string, len(def.Config))
			for k, v := range def.Config {
				cfg[k] = planValue("config."+k, v, secretKeys)
			}
			item := sourceGetItem{
				Name: def.Name, Kind: kind, Tenant: def.Tenant,
				Mode: sourceModeFromConfig(def.Config), PollSeconds: def.PollSeconds,
				Enabled: def.Enabled, Config: cfg,
			}
			return renderOut(cmd, func(out io.Writer) error {
				tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
				fmt.Fprintf(tw, "NAME\t%s\n", item.Name)
				fmt.Fprintf(tw, "KIND\t%s\n", item.Kind)
				fmt.Fprintf(tw, "TENANT\t%s\n", item.Tenant)
				fmt.Fprintf(tw, "MODE\t%s\n", item.Mode)
				fmt.Fprintf(tw, "POLL\t%ds\n", item.PollSeconds)
				fmt.Fprintf(tw, "ENABLED\t%t\n", item.Enabled)
				if err := tw.Flush(); err != nil {
					return err
				}
				if len(item.Config) == 0 {
					_, err := fmt.Fprintln(out, "\nno config keys")
					return err
				}
				if _, err := fmt.Fprintln(out, "\nCONFIG"); err != nil {
					return err
				}
				tc := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
				// Sorted so two runs of the same row print the same bytes: a Go map
				// iterates at random, and an operator diffing two `get` outputs would
				// otherwise see churn that is not a change.
				keys := make([]string, 0, len(item.Config))
				for k := range item.Config {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					fmt.Fprintf(tc, "%s\t%s\n", k, item.Config[k])
				}
				return tc.Flush()
			}, item)
		},
	}
	addStoreFlags(cmd, &dataDir, &engine, &dsn)
	return cmd
}

func sourcesSetCmd() *cobra.Command {
	var (
		actorFlag, reasonFlag string
		dataDir, engine, dsn  string
		e                     sourceEdit
	)
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Create or update a source (only the flags you pass are changed on an existing source)",
		Long: "Create a source, or update an existing one by --name. On an EXISTING source only the\n" +
			"flags you actually pass are changed (so `set --name x --enabled=false` just disables it).\n" +
			"Pass connector settings as repeated --config key=value; use secret REFERENCES for\n" +
			"credentials (e.g. --config token=store:vault/token).\n\n" +
			"set APPLIES. To see the same change first, run `olivares sources plan` with these very\n" +
			"flags: it computes the result through the same code this command applies, and writes\n" +
			"nothing. A plan is not required — it is offered.",
		Example: `  # Add a Vault connector source
  olivares sources set --name vault-prod --kind vault --tenant t_abc123 \
    --config addr=https://vault.internal:8200 --config token=store:vault/token

  # Disable an existing source without changing anything else
  olivares sources set --name vault-prod --enabled=false`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// EVERY refusal that can be decided from the ARGUMENTS ALONE is decided
			// before the store opens, because opening it is itself a large side
			// effect: with an explicit --data-dir it creates the directory, a
			// database, three signing keys and three sealer keys, bootstraps the
			// system tenant, and starts the engine. A request that was never going to
			// be accepted used to mint all of that on its way to saying no, and leave
			// the operator holding an installation they did not ask for.
			//
			// The sol-max contrast measured this and it was worse than the version
			// that only moved three checks: a `set` missing --actor exited 1 for
			// attribution AFTER leaving six key files and a 6.4 MB olivares.db behind.
			// So attribution moved here too. It needs no store, no network and no row
			// (localactor.go: two strings and a Principal), and `sources set` has no
			// consent gate for it to compete with — the ordering rationale that keeps
			// it late in `secrets rm` and `sources rm` (consent first, then store,
			// then identity) simply does not apply to a command with no --yes.
			//
			// What still cannot be decided here is anything that depends on the ROW
			// being edited: a kind XOR a plugin, and the descriptor-aware credential
			// guard. Those need the store, and they run where they always did.
			if msg := auth.ValidateSourceName(e.name); msg != "" {
				return fmt.Errorf("--name: %s", msg)
			}
			if e.pollSeconds < 0 {
				return fmt.Errorf("--poll-seconds cannot be negative")
			}
			if _, perr := parseConfigKV(e.configKV, nil); perr != nil {
				return perr
			}
			// An EXPLICITLY empty --tenant is a decided refusal too (the store rejects
			// it in validateDef). Only when the flag was passed: leaving it out on an
			// existing row keeps that row's tenant, which is the whole point of a
			// partial edit.
			if cmd.Flags().Changed("tenant") && strings.TrimSpace(e.tenant) == "" {
				return fmt.Errorf("--tenant: a source must name the business tenant its observations belong to")
			}
			op, err := requireLocalActor(viaCLISources, actorFlag, reasonFlag)
			if err != nil {
				return err
			}
			eng, err := auditBoot(cmd, dataDir, engine, dsn)
			if err != nil {
				return err
			}
			defer func() { _ = eng.Close() }()

			existing, found, gerr := eng.sourceStore.Get(cmd.Context(), auth.GlobalSourceScope, e.name)
			if gerr != nil {
				return gerr
			}
			// The SAME function `sources plan` previews with. set does not merge
			// the flags itself, so a plan cannot describe an apply this command would
			// have performed differently — which is the only way a preview can be worse
			// than no preview at all.
			def, derr := desiredSourceDef(cmd.Flags(), &e, existing, found)
			if derr != nil {
				return derr
			}
			changes := diffSourceDefs(existing, found, def)

			if err := checkInlineSecrets(def); err != nil {
				return err
			}
			// A `set` that would move NOTHING does not write, and this is what makes
			// plan's NO-OP true rather than nearly true.
			//
			// The sol-max contrast caught the earlier version: plan said "the roster
			// already says exactly this" while set called Put unconditionally, and Put
			// runs an Update that bumps version and updated_at and appends a source.put
			// audit event even when every field it writes is identical. So the preview
			// said "nothing happens" about an operation that moved the row's version and
			// wrote evidence of a change that did not occur — and none of the verbs here
			// could show it. Not writing is the honest half of that pair: the audit
			// ledger stops recording changes that changed nothing, and the plan's word
			// holds.
			if found && len(changes) == 0 {
				return renderOut(cmd, func(out io.Writer) error {
					_, werr := fmt.Fprintf(out, "source %q is already exactly this — nothing was written\n", existing.Name)
					return werr
				}, sourceApplyReport{
					Name: existing.Name, Action: "no-op", Kind: existing.Kind,
					Tenant: existing.Tenant, Enabled: existing.Enabled,
					Changes: changes, Persisted: false,
				})
			}
			saved, err := eng.sourceStore.Put(cmd.Context(), op, def)
			if err != nil {
				return err
			}
			verb := "created"
			action := "create"
			if found {
				verb = "updated"
				action = "update"
			}
			// The report is built from `saved` — what the store actually wrote back —
			// not from `def`, so the JSON cannot claim a value the roster refused to
			// take. The text pane below reads the same fields off the same value.
			return renderOut(cmd, func(out io.Writer) error {
				if _, werr := fmt.Fprintf(out, "%s source %q (kind %q, tenant %q, enabled %t)\n", verb, saved.Name, saved.Kind, saved.Tenant, saved.Enabled); werr != nil {
					return werr
				}
				// What actually moved, in the plan's own words and from the plan's own
				// diff. An operator who ran `plan` first can compare them line for line;
				// one who did not still gets to see what they just did.
				writeSourceApplied(out, changes)
				_, werr := fmt.Fprintln(out, "→ reload a running engine to apply: POST /v1/console/runtime/reload, or `kill -HUP <pid>` (it also applies at next boot)")
				return werr
			}, sourceApplyReport{
				Name: saved.Name, Action: action, Kind: saved.Kind,
				Tenant: saved.Tenant, Enabled: saved.Enabled,
				Changes: changes, Persisted: true,
			})
		},
	}
	addStoreFlags(cmd, &dataDir, &engine, &dsn)
	addLocalActorFlags(cmd, &actorFlag, &reasonFlag)
	addSourceEditFlags(cmd, &e)
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

// sourceApplyReport is what `sources set` reports, and it speaks `sources plan`'s
// vocabulary ON PURPOSE.
//
// `name`, `action`, `changes` and `persisted` are sourcePlanReport's own keys with
// sourcePlanReport's own values (cmd_sources_plan.go:515), because plan and set are
// designed to be read against each other — the file already says so of the text
// pane: "an operator who ran `plan` first can compare them line for line". A set
// that invented its own words would leave a script needing two parsers for one
// change, and the pair's whole value is that they describe the same change.
//
// `persisted` is the one field that separates them, and it is the reason plan
// carries a field it never sets to true: plan reports `false` because it wrote
// nothing, set reports `true` because it did. A NO-OP set reports `false` too, and
// that is not a technicality — the roster deliberately does not Put an unchanged
// row (it would bump the version and append an audit event for a change that did
// not happen), so "nothing was written" is literally true and the JSON says so in
// the same field plan uses to say it.
//
// It does NOT carry plan's `check` or `live_effect`: set does not compute either,
// and a zero-valued `check` in a document an operator archives as evidence would
// read as "checked, and clean".
type sourceApplyReport struct {
	Name      string         `json:"name"`
	Action    string         `json:"action"`
	Kind      string         `json:"kind"`
	Tenant    string         `json:"tenant"`
	Enabled   bool           `json:"enabled"`
	Changes   []sourceChange `json:"changes"`
	Persisted bool           `json:"persisted"`
}

// sourceRemovedReport is what `sources rm` reports. It carries `name`, `action`
// and `persisted` — the three keys of sourceApplyReport that a delete can answer
// honestly — and NOT kind/tenant/enabled: rm deletes by name without reading the
// row, so it has no value for those and emitting empty ones would describe the
// deleted source as an unnamed, disabled, tenantless connector.
type sourceRemovedReport struct {
	Name      string `json:"name"`
	Action    string `json:"action"`
	Persisted bool   `json:"persisted"`
}

// writeSourceApplied prints the fields a set moved. It says so explicitly when a
// set moved nothing: "updated source" over an unchanged row reads as a change
// that happened, and an operator chasing a stale value would believe it.
func writeSourceApplied(out io.Writer, changes []sourceChange) {
	if len(changes) == 0 {
		fmt.Fprintln(out, "no field changed — the roster already said exactly this")
		return
	}
	for _, ch := range changes {
		fmt.Fprintf(out, "  %s: %s → %s\n", ch.Field, orDash(ch.From), orDash(ch.To))
	}
}

func sourcesRemoveCmd() *cobra.Command {
	var dataDir, engine, dsn, name string
	var actorFlag, reasonFlag string
	var yes bool
	cmd := &cobra.Command{
		Use:     "rm",
		Short:   "Delete a source from the roster",
		Long:    "rm removes one named connector from the durable source roster; reload a running engine to stop it immediately.",
		Example: "  olivares sources rm --name vault-prod",
		Args:    cobra.NoArgs,
		Aliases: []string{"remove", "delete"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Removing a source stops the engine ingesting from it — an absence
			// that is quiet by nature, so make the decision loud.
			if err := confirmDestructive(cmd, yes, fmt.Sprintf(
				"delete source %q from the roster (the engine stops ingesting from it)", name)); err != nil {
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
			op, err := requireLocalActor(viaCLISources, actorFlag, reasonFlag)
			if err != nil {
				return err
			}
			if err := eng.sourceStore.Delete(cmd.Context(), op, auth.GlobalSourceScope, name); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				if _, werr := fmt.Fprintf(out, "deleted source %q\n", name); werr != nil {
					return werr
				}
				_, werr := fmt.Fprintln(out, "→ reload a running engine to stop it live: POST /v1/console/runtime/reload, or `kill -HUP <pid>`")
				return werr
			}, sourceRemovedReport{Name: name, Action: "delete", Persisted: true})
		},
	}
	addStoreFlags(cmd, &dataDir, &engine, &dsn)
	addLocalActorFlags(cmd, &actorFlag, &reasonFlag)
	cmd.Flags().StringVar(&name, "name", "", "source name")
	addYesFlag(cmd, &yes)
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

// parseConfigKV merges key=value pairs onto base. An empty value (key=) removes
// the key, so `--config token=` clears a setting. A pair without '=' is an error.
func parseConfigKV(pairs []string, base map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range base {
		out[k] = v
	}
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok {
			return nil, fmt.Errorf("--config %q must be key=value", p)
		}
		k = strings.TrimSpace(k)
		if k == "" {
			return nil, fmt.Errorf("--config %q has an empty key", p)
		}
		if v == "" {
			delete(out, k)
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil, nil
	}
	// The codec marshals the map to JSON with sorted keys, so a re-set with the same
	// pairs persists identical bytes (no spurious rotate on the next reconcile).
	return out, nil
}
