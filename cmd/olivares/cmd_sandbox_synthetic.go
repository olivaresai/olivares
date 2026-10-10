// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

func newSandboxGenerateCmd(flags *authClientFlags) *cobra.Command {
	var count int
	var subjectKind, seedFile string
	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Generate reproducible scenario inputs from a local template",
		Long: "generate asks the engine for bounded local fixtures without contacting a model.\n" +
			"The server replaces {{index}} and {{subject_kind}}; other text stays literal.\n" +
			"Use -o json to produce the step array accepted by scenarios create --steps-file.\n" +
			"Generation requires scenario-write permission and does not save the inputs.",
		Example: "  olivares sandbox generate --count 2 --seed-file seed.txt -o json > steps.json\n" +
			"  olivares sandbox scenarios create --name generated --steps-file steps.json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			body := map[string]any{"count": count, "subject_kind": subjectKind}
			if seedFile != "" {
				seed, err := readAgentExecDocument(cmd, seedFile)
				if err != nil {
					return err
				}
				body["seed"] = string(seed)
			}
			res, err := (agentExecCall{
				flags: flags, module: sandboxModule,
				method: http.MethodPost, path: "/synthetic-data", body: body,
			}).do(cmd)
			if err != nil {
				return err
			}
			var response struct {
				Samples []json.RawMessage `json:"samples"`
			}
			if err := res.decode(&response); err != nil {
				return exitcode.New(exitcode.Server, err)
			}
			return renderOut(cmd, func(out io.Writer) error {
				return writeAgentExecTable(out, flags, response.Samples, []string{"key", "input"})
			}, response.Samples)
		},
	}
	cmd.Flags().IntVar(&count, "count", 10, "number of samples (1–100; zero uses the default)")
	cmd.Flags().StringVar(&subjectKind, "subject-kind", "agent", "subject substituted into the template (up to 200 bytes)")
	cmd.Flags().StringVar(&seedFile, "seed-file", "", "literal template file (up to 8192 bytes), '-' for stdin; omitted uses the server default")
	return cmd
}
