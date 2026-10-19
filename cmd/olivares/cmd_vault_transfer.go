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
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/auth"
)

// `vault import` and `vault export` move the owner's own items in and out of the vault. They
// add no engine route: import writes through the Put `vault put` uses, export reads through
// reveal, so every item exported is one audit entry and no value is ever listed or logged.

// vaultRateTries bounds how often one request is repeated against a rate limit that does not
// clear: at the default wait, a minute.
const vaultRateTries = 20

// maxVaultImportFailuresShown keeps a text report short; -o json lists every row.
const maxVaultImportFailuresShown = 10

// vaultPacer repeats a request the engine rate limited, after vaultRateWait.
//
// ponytail: the wait is fixed and Retry-After is ignored. The engine's own budget refills one item
// per vaultRateWait, so only a proxy with a longer limit costs extra tries (at most
// vaultRateTries, then an error); read Retry-After if such a deployment shows up.
type vaultPacer struct{ told bool }

func (p *vaultPacer) call(cmd *cobra.Command, request func() error) error {
	for try := 1; ; try++ {
		err := request()
		var refusal *apiRefusal
		if !errors.As(err, &refusal) || refusal.status != http.StatusTooManyRequests {
			return err
		}
		if try == vaultRateTries {
			return sentence(exitcode.Err, "The engine is still rate limiting vault access. Try again in a few minutes.")
		}
		if !p.told {
			p.told = true
			_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "The engine rate limits vault access; waiting and carrying on.")
		}
		select {
		case <-cmd.Context().Done():
			return cmd.Context().Err()
		case <-time.After(vaultRateWait):
		}
	}
}

// --- import -----------------------------------------------------------------------

func newVaultImportCmd(flags *authClientFlags) *cobra.Command {
	var (
		from            string
		dryRun, replace bool
	)
	cmd := &cobra.Command{
		Annotations: map[string]string{"changes_data": "write"},
		Use:         "import <file>",
		Short:       "Add the items of another password manager's export to your vault",
		Long: "import reads the CSV a password manager exported (or an `olivares vault export` file) and stores each\n" +
			"login, authenticator key and note as an item of your vault, the way `vault put` does. --from names the\n" +
			"program: bitwarden, keepass (KeePassXC), 1password, chrome (also Edge and Brave), proton (Proton Pass)\n" +
			"or olivares. The file is given as a path, or - for stdin.\n" +
			"An item's label is its title with anything but letters, digits, . _ and - turned into -; a title the\n" +
			"label changes is kept as the description. An item that is already in the vault is left as it is\n" +
			"unless you pass --replace. Cards, identities, aliases and other kinds the vault has no item for, and\n" +
			"data an item has no field for (folders, tags, custom fields), are counted and not stored.\n" +
			"Every row is checked before anything is sent: a row that cannot be an item is reported by its row\n" +
			"number and the field at fault, the others are stored, and the command exits 1. An import that\n" +
			"would not fit in the vault is refused whole. If the engine fails half way, every item stored is\n" +
			"complete and running the same command again stores the rest. With --replace an item keeps its\n" +
			"description unless the title gives a new one. --dry-run does all of this except the writes and\n" +
			"prints counts only. Neither mode prints or logs a value. The export file itself holds your\n" +
			"passwords in the clear: delete it once it is imported.\n" +
			"Like put it needs a signed-in session with the sign-in step-up this deployment asks for.\n" + vaultExitCodes,
		Example: "  olivares vault import ./bitwarden.csv --from bitwarden --dry-run\n" +
			"  olivares vault import ./bitwarden.csv --from bitwarden\n" +
			"  olivares vault import ./vault.json --from olivares --replace -o json",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if from == "" {
				return sentence(exitcode.Usage, "Say which program wrote the file: --from %s.", vaultImportSources)
			}
			data, err := readVaultImportFile(cmd, args[0])
			if err != nil {
				return err
			}
			parsed, err := parseVaultImport(from, data)
			if err != nil {
				return err
			}
			cfg, err := mcpClient(cmd, flags)
			if err != nil {
				return err
			}
			prefix, err := vaultPrefix(cmd, cfg, flags)
			if err != nil {
				return err
			}
			entries, err := vaultEntries(cmd, cfg)
			if err != nil {
				return err
			}
			held := map[string]string{} // the caller's entries and the line their list shows
			for _, e := range entries {
				if strings.HasPrefix(e.Name, prefix) {
					held[e.Name] = e.Description
				}
			}
			res := vaultImportResult{
				Source: from, DryRun: dryRun, Rows: parsed.Rows, Items: len(parsed.Items), Types: map[string]int{},
				Unsupported: parsed.Unsupported, NotKept: parsed.Dropped, Failed: parsed.Failed,
			}
			var todo []vaultImportItem
			fresh := 0
			for _, it := range parsed.Items {
				res.Types[it.Item.Type]++
				line, have := held[prefix+it.Label]
				switch {
				case !have:
					fresh++
					todo = append(todo, it)
				case replace:
					if it.Description == "" {
						it.Description = line // like put, a replacement keeps the line the list shows
					}
					todo = append(todo, it)
				default:
					res.SkippedExisting++
				}
			}
			if len(held)+fresh > auth.MaxVaultItemsPerOwner {
				return sentence(exitcode.Err, "Your vault holds %d items and this file adds %d new ones, but a vault holds at most %d. Nothing was imported.",
					len(held), fresh, auth.MaxVaultItemsPerOwner)
			}

			var stopped error
			if !dryRun {
				var pacer vaultPacer
				for _, it := range todo {
					name := prefix + it.Label
					if stopped = pacer.call(cmd, func() error { return vaultStore(cmd, cfg, name, it.Item, it.Description) }); stopped != nil {
						break
					}
					res.Written++
				}
			}
			if err := renderOut(cmd, func(w io.Writer) error { return writeVaultImportText(w, res) }, res); err != nil {
				return err
			}
			if stopped != nil {
				return exitcode.New(exitcode.From(stopped), fmt.Errorf("stopped after storing %d of %d items; run the same command again to store the rest (without --replace, items already stored are skipped): %w",
					res.Written, len(todo), stopped))
			}
			if n := len(res.Failed); n > 0 {
				return sentence(exitcode.Err, "Rows with no valid item: %d (listed above).", n)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "the program that wrote the file: "+vaultImportSources)
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "read and check the file and print counts only; store nothing")
	cmd.Flags().BoolVar(&replace, "replace", false, "overwrite items that are already in the vault")
	return cmd
}

// readVaultImportFile reads the export named by path, or stdin for -, up to a bound.
func readVaultImportFile(cmd *cobra.Command, path string) ([]byte, error) {
	var in io.Reader = cmd.InOrStdin()
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("read the file: %w", err)
		}
		defer func() { _ = f.Close() }()
		in = f
	}
	data, err := io.ReadAll(io.LimitReader(in, maxVaultImportBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read the file: %w", err)
	}
	if len(data) > maxVaultImportBytes {
		return nil, fmt.Errorf("the file is larger than %d MiB: that is not a password export", maxVaultImportBytes>>20)
	}
	return data, nil
}

// writeVaultImportText is the report: counts and the field at fault, never a title or a value.
func writeVaultImportText(w io.Writer, res vaultImportResult) error {
	var b strings.Builder
	if res.DryRun {
		fmt.Fprintf(&b, "Dry run, nothing stored. %d rows read, %d items found (%s).\n", res.Rows, res.Items, vaultCounts(res.Types))
	} else {
		fmt.Fprintf(&b, "Stored %d of %d items from %d rows (%s).\n", res.Written, res.Items, res.Rows, vaultCounts(res.Types))
	}
	if res.SkippedExisting > 0 {
		fmt.Fprintf(&b, "%d already in your vault were left as they are; --replace overwrites them.\n", res.SkippedExisting)
	}
	if len(res.Unsupported) > 0 {
		fmt.Fprintf(&b, "Not supported, left out: %s.\n", vaultCounts(res.Unsupported))
	}
	if len(res.NotKept) > 0 {
		fmt.Fprintf(&b, "Data with no place in the vault, not stored: %s.\n", vaultCounts(res.NotKept))
	}
	for i, f := range res.Failed {
		if i == maxVaultImportFailuresShown {
			fmt.Fprintf(&b, "...and %d more rows (-o json lists them all).\n", len(res.Failed)-i)
			break
		}
		fmt.Fprintf(&b, "Row %d: %s.\n", f.Row, f.Reason)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// --- export -----------------------------------------------------------------------

type vaultExportResult struct {
	Items   int      `json:"items"`
	File    string   `json:"file"`
	Skipped []string `json:"skipped"`
}

func newVaultExportCmd(flags *authClientFlags) *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Annotations: map[string]string{"changes_data": "read"},
		Use:         "export",
		Short:       "Write every item of your vault to a file, to move it or keep a copy",
		Long: "export opens every item of your vault, one reveal each, and writes them to --file as one JSON document\n" +
			"that `olivares vault import --from olivares` reads back item for item. Each reveal is recorded in the\n" +
			"audit ledger without the value, and the engine's per-person read limit applies: a large vault is\n" +
			"read at about 20 items a minute, so the command waits and carries on.\n" +
			"THE FILE HOLDS EVERY PASSWORD, NOTE AND AUTHENTICATOR KEY IN THE CLEAR. It is created for you alone\n" +
			"(mode 0600), never over an existing file, and not at all if the export fails. Keep it private and delete it\n" +
			"when you are done; --file - writes it to stdout instead, for a pipe you trust.\n" +
			"Entries stored before items had a shape are not items: they are left out and named in the result.\n" +
			"It needs a signed-in session with the sign-in step-up this deployment asks for; an API token is refused.\n" +
			"-o json prints the result: the count, the file and the entries left out (with --file - only the\n" +
			"document goes to stdout, and the summary goes to stderr as text).\n" + vaultExitCodes,
		Example: "  olivares vault export --file ./vault.json\n  olivares vault export --file - | gpg --symmetric > vault.json.gpg",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) (err error) {
			if file == "" {
				return sentence(exitcode.Usage, "Say where to write the export: --file <path>, or --file - for stdout.")
			}
			// The file is claimed before the first reveal: a path that cannot be written must not
			// cost hundreds of audited reads. Anything but a whole export removes it again.
			var out *os.File
			if file != "-" {
				if out, err = createVaultExportFile(file); err != nil {
					return err
				}
				defer func() {
					if cerr := out.Close(); err == nil {
						err = cerr
					}
					if err != nil {
						if rerr := os.Remove(file); rerr != nil {
							err = errors.Join(err, fmt.Errorf("%s could not be removed: %w", file, rerr))
						}
					}
				}()
			}
			cfg, err := mcpClient(cmd, flags)
			if err != nil {
				return err
			}
			prefix, err := vaultPrefix(cmd, cfg, flags)
			if err != nil {
				return err
			}
			entries, err := vaultEntries(cmd, cfg)
			if err != nil {
				return err
			}
			sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })

			doc := vaultExportDoc{Format: vaultExportFormat, Items: []vaultExportItem{}}
			res := vaultExportResult{File: file, Skipped: []string{}}
			var pacer vaultPacer
			for _, e := range entries {
				if !strings.HasPrefix(e.Name, prefix) {
					continue
				}
				label := vaultLabel(e.Name)
				// A hint that is not an item's kind is a value stored before items had a shape: nothing to open.
				if kind, _ := vaultKindOf(e.Hint); kind == "other" {
					res.Skipped = append(res.Skipped, label)
					continue
				}
				var item *auth.VaultItem
				err := pacer.call(cmd, func() (err error) {
					item, _, err = vaultReveal(cmd, cfg, e.Name)
					return err
				})
				if err != nil {
					return exitcode.New(exitcode.From(err), fmt.Errorf("the export stopped at %s and wrote nothing: %w", termSafe(label), err))
				}
				if item == nil {
					res.Skipped = append(res.Skipped, label)
					continue
				}
				doc.Items = append(doc.Items, vaultExportItem{Label: label, Description: e.Description, VaultItem: *item})
			}
			res.Items = len(doc.Items)

			var buf bytes.Buffer
			enc := json.NewEncoder(&buf)
			enc.SetEscapeHTML(false)
			enc.SetIndent("", "  ")
			if err := enc.Encode(doc); err != nil {
				return err
			}
			if out == nil {
				if _, err := cmd.OutOrStdout().Write(buf.Bytes()); err != nil {
					return err
				}
				return writeVaultExportText(cmd.ErrOrStderr(), res)
			}
			if _, err := out.Write(buf.Bytes()); err != nil {
				return fmt.Errorf("write the export file: %w", err)
			}
			return renderOut(cmd, func(w io.Writer) error { return writeVaultExportText(w, res) }, res)
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "where to write the export, or - for stdout (required; never overwrites a file)")
	return cmd
}

// createVaultExportFile creates the export file for its owner alone, and never over a file.
func createVaultExportFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	switch {
	case errors.Is(err, os.ErrExist):
		return nil, sentence(exitcode.Usage, "%s already exists. An export never overwrites a file; name a new one.", path)
	case err != nil:
		return nil, fmt.Errorf("create the export file: %w", err)
	}
	return f, nil
}

func writeVaultExportText(w io.Writer, res vaultExportResult) error {
	where := res.File
	if where == "-" {
		where = "stdout"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Exported %d items to %s.\n", res.Items, termSafe(where))
	b.WriteString("It holds every secret in the clear: keep it private and delete it when you are done.\n")
	if len(res.Skipped) > 0 {
		safe := make([]string, len(res.Skipped))
		for i, label := range res.Skipped {
			safe[i] = termSafe(label)
		}
		fmt.Fprintf(&b, "Left out, because they are not items: %s.\n", strings.Join(safe, ", "))
	}
	_, err := io.WriteString(w, b.String())
	return err
}
