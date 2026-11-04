// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/cmd/olivares/internal/netfolder"
	"github.com/olivaresai/olivares/modules/sessions"
)

// `olivares netfolder` makes an NFS export or an SMB share on the engine host a session folder.
// It is the one host-side verb of the folder plane: it writes the systemd mount and automount
// units (and the SMB credential file) through internal/netfolder, so it runs as root, and then
// registers the mount point as a folder through the same route as `agent workspace add`.
func newNetfolderCmd() *cobra.Command {
	root := &cobra.Command{
		Annotations: map[string]string{"changes_data": "write"},
		Use:         "netfolder",
		Short:       "Network folders: an NFS or SMB share as a session folder",
		Long: "A network folder is an NFS export or an SMB share that systemd mounts on this host and that\n" +
			"sessions use as their folder. `add` writes the mount and automount units, starts the automount\n" +
			"and registers the folder; `ls` shows each folder and whether it is mounted, not mounted yet or\n" +
			"not responding. Both read the host's units, so run them on the engine host, `add` as root.\n\n" +
			"Everyone who has the folder acts as one account: the share cannot tell people apart, Olivares\n" +
			"decides who receives the folder. A file server that is down fails the use of the folder, never\n" +
			"the boot.",
		Example: "  sudo olivares netfolder add data fileserver:/export\n  olivares netfolder ls",
	}
	root.AddCommand(newNetfolderAddCmd(), newNetfolderListCmd())
	return root
}

func newNetfolderAddCmd() *cobra.Command {
	var (
		cfg           agentClientConfig
		user, mode    string
		passwordStdin bool
	)
	cmd := &cobra.Command{
		Annotations: map[string]string{"changes_data": "write"},
		Use:         "add <name> <source>",
		Short:       "Mount a network share and register it as a session folder",
		Long: "add mounts <source> at /srv/olivares/mnt/<name> and registers that path as a folder. <source> is\n" +
			"//server/share for SMB or server:/export for NFS. The name is lower-case letters and digits.\n\n" +
			"SMB needs --user and the password on standard input (--password-stdin). The password goes into a\n" +
			"file only root can read; it is never an argument, a unit line or output. NFS needs no credential.\n\n" +
			"The folder mounts on first use, with soft timeouts: a file server that is down fails the use of the\n" +
			"folder after a few seconds and never blocks the boot. A folder that exists is never overwritten.",
		Example: "  printf '%s\\n' \"$SMB_PASSWORD\" | sudo olivares netfolder add files //fileserver/projects --user svc --password-stdin\n" +
			"  sudo olivares netfolder add data fileserver:/export --mode write_no_delete",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			spec, err := netfolder.Parse(args[0], args[1])
			if err != nil {
				return exitcode.New(exitcode.Usage, err)
			}
			if os.Geteuid() != 0 {
				return exitcode.New(exitcode.Auth, errors.New("writing the mount units needs root: run it with sudo"))
			}
			password := ""
			if spec.Type == "cifs" {
				if user == "" || !passwordStdin {
					return exitcode.New(exitcode.Usage, errors.New("an SMB folder needs --user and --password-stdin"))
				}
				line, err := bufio.NewReader(io.LimitReader(cmd.InOrStdin(), 4096)).ReadString('\n')
				if err != nil && !errors.Is(err, io.EOF) {
					return err
				}
				password = strings.TrimRight(line, "\r\n")
			}
			if err := cfg.resolve(); err != nil { // before the host changes: a missing sign-in fails first
				return err
			}
			host := netfolder.LinuxHost(sessions.RootResponds)
			if err := host.Install(spec, user, password); err != nil {
				return err
			}
			where := filepath.Join(host.MountRoot, spec.Name)
			status, b, err := cfg.do(cmd.Context(), "POST", "/v1/m/sessions/workspaces",
				map[string]any{"root_path": where, "name": spec.Name, "mount_mode": mode}, http.StatusCreated)
			if err != nil {
				return fmt.Errorf("%s is mounted from %s, but it was not registered as a folder (%w); once the share answers, run `olivares agent workspace add %s`", where, spec.What, err, where)
			}
			return printWorkspace(cmd, status, b, http.StatusCreated)
		},
	}
	cfg.addFlags(cmd)
	cmd.Flags().StringVar(&user, "user", "", "SMB user (the account every user of the folder acts as)")
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read the SMB password from the first line of standard input")
	cmd.Flags().StringVar(&mode, "mode", "rw", "mount mode: rw|ro|write_no_delete")
	_ = cmd.RegisterFlagCompletionFunc("mode", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"rw", "ro", "write_no_delete"}, cobra.ShellCompDirectiveNoFileComp
	})
	addDeprecatedJSONFlag(cmd)
	return cmd
}

func newNetfolderListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Annotations: map[string]string{"changes_data": "read"},
		Use:         "ls",
		Aliases:     []string{"list"},
		Short:       "List the network folders of this host and their state",
		Long: "ls lists the network folders added on this host with their source, mount point and state:\n" +
			"mounted, unmounted (it mounts on first use, and ls does not trigger that) or not responding\n" +
			"(its file server did not answer within 10 seconds).",
		Example: "  olivares netfolder ls\n  olivares netfolder ls -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			entries, err := netfolder.LinuxHost(sessions.RootResponds).List()
			if err != nil {
				return err
			}
			items := make([]map[string]any, 0, len(entries))
			for _, e := range entries {
				items = append(items, map[string]any{"name": e.Name, "type": e.Type, "source": e.What, "mount_point": e.Where, "state": e.State})
			}
			return renderListOut(cmd, items, "no network folders", func(out io.Writer, it map[string]any) error {
				_, err := fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%s\n", str(it, "name"), str(it, "state"), str(it, "type"), str(it, "source"), str(it, "mount_point"))
				return err
			}, map[string]any{"items": items})
		},
	}
	addDeprecatedJSONFlag(cmd)
	return cmd
}
