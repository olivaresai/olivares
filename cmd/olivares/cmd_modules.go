// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"io"
	"net/http"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/api"
	"github.com/spf13/cobra"
)

const modulesSelectionPath = "/v1/console/modules"

func newModulesCmd() *cobra.Command {
	flags := &authClientFlags{}
	root := &cobra.Command{
		Use: "modules", Short: "List, turn on and turn off optional modules",
		Long: "Manage the same module selection as Edition & modules in the console. Sign in with\n" +
			"olivares login, then see the current selection with olivares modules ls.",
		Example: "  olivares modules ls\n  olivares modules on adoption\n  olivares modules off adoption",
	}
	flags.addPersistent(root)
	client := bootstrapClient{flags: flags, surface: "modules"}
	root.AddCommand(&cobra.Command{
		Use: "ls", Short: "List selected and running modules",
		Long: "List each module's selected and running state. Kernel modules and dependencies may\n" +
			"keep running without being selected. To select a module, use olivares modules on <name>.",
		Example: "  olivares modules ls", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			state, err := modulesSelectionCall(cmd, client, http.MethodGet, nil)
			if err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				if _, err := fmt.Fprintln(out, "MODULE\tSELECTED\tRUNNING"); err != nil {
					return err
				}
				for _, module := range state.Modules {
					selected, running := "off", "off"
					if module.Selected {
						selected = "on"
					}
					if module.Running {
						running = "on"
					}
					if _, err := fmt.Fprintf(out, "%s\t%s\t%s\n", termSafe(module.Name), selected, running); err != nil {
						return err
					}
				}
				return nil
			}, state)
		},
	})
	for _, verb := range []string{"on", "off"} {
		root.AddCommand(&cobra.Command{
			Use: verb + " <name>", Short: "Turn " + verb + " a module in the saved selection",
			Long: "Keep the other selected modules and turn " + verb + " the named module. The engine restarts\n" +
				"once if its running modules change, and the restart stops the running sessions (each\n" +
				"can be resumed). Kernel modules and required dependencies stay on.\n" +
				"Data is kept when a module is off. Check the result with olivares modules ls.",
			Example: "  olivares modules " + verb + " adoption", Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				state, err := modulesSelectionCall(cmd, client, http.MethodGet, nil)
				if err != nil {
					return err
				}
				selected := []string{}
				found := false
				for _, module := range state.Modules {
					keep := module.Selected
					if module.Name == args[0] {
						found, keep = true, verb == "on"
					}
					if keep {
						selected = append(selected, module.Name)
					}
				}
				if !found {
					return sentence(exitcode.Usage, "Unknown module %s. See olivares modules ls.", termSafe(args[0]))
				}
				state, err = modulesSelectionCall(cmd, client, http.MethodPut, map[string]any{"selected": selected})
				if err != nil {
					return err
				}
				return renderOut(cmd, func(out io.Writer) error {
					message := "Module selection saved."
					if state.Restarting {
						message += " The engine is restarting once to apply it; then run olivares modules ls."
						if n := state.RunningSessions; n == 1 {
							message += " 1 running session stops; resume it to continue."
						} else if n > 1 {
							message += fmt.Sprintf(" %d running sessions stop; resume each one to continue.", n)
						}
					}
					_, err := fmt.Fprintln(out, message)
					return err
				}, state)
			},
		})
	}
	return root
}

func modulesSelectionCall(cmd *cobra.Command, client bootstrapClient, method string, body any) (api.ModuleSelectionDTO, error) {
	var state api.ModuleSelectionDTO
	raw, err := client.expect(cmd, method, modulesSelectionPath, body, http.StatusOK)
	if err != nil {
		return state, err
	}
	err = decodeBootstrapJSON("modules", raw, &state)
	return state, err
}
