// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/cmd/olivares/internal/localinstall"
)

// newPlaceInstallManifestCmd builds the hidden `__place-install-manifest` subcommand, run only by the package's
// postinstall script when its staging directory and the data directory are on different filesystems (the appliance's
// data volume), where its own rename cannot be atomic. It is not a user command.
//
// Exit codes: 0 placed, and "INODE MTIME" printed for the script's upgrade marker; 1 the placement failed: before the
// rename nothing is left in the data directory but what was there, and when only the directory sync after the rename
// fails the new manifest is in place but the script refuses, so the marker still names the old one; 2 wrong arguments,
// not root, or no olivares group.
func newPlaceInstallManifestCmd() *cobra.Command {
	return &cobra.Command{
		Annotations: map[string]string{"changes_data": "write"},
		Use:         "__place-install-manifest STAGE TARGET",
		Hidden:      true,
		Args:        cobra.ExactArgs(2),
		Short:       "internal: place the install manifest inside the data directory (package script)",
		Long: "__place-install-manifest is an internal, hidden command run by the package's post-install script as root,\n" +
			"NOT a user command. It copies the staged install manifest STAGE to TARGET from inside TARGET's directory\n" +
			"(a temporary file there, owned root:olivares and mode 0640, synced, then renamed onto TARGET), because\n" +
			"the script's own rename cannot be atomic when the data directory is its own volume. On success it prints\n" +
			"\"INODE MTIME\" for the script's upgrade marker.",
		Example: "  # internal use only — the package's post-install script runs this as root\n" +
			"  olivares __place-install-manifest /var/lib/olivares-package/install-manifest.json.new /var/lib/olivares/install-manifest.json",
		RunE: func(cmd *cobra.Command, args []string) error {
			if os.Geteuid() != 0 {
				return exitcode.New(exitcode.Usage, errors.New("__place-install-manifest runs as root, from the package script"))
			}
			group, err := user.LookupGroup("olivares")
			if err != nil {
				return exitcode.New(exitcode.Usage, fmt.Errorf("the olivares group: %w", err))
			}
			gid, err := strconv.Atoi(group.Gid)
			if err != nil {
				return exitcode.New(exitcode.Usage, fmt.Errorf("the olivares group id %q: %w", group.Gid, err))
			}
			placed, err := localinstall.Place(args[0], args[1], gid)
			if err != nil {
				return exitcode.New(exitcode.Err, err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), placed.String())
			return nil
		},
	}
}
