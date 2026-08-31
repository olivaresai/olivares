// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"

	"github.com/spf13/cobra"
)

// maxCLIUpload bounds a client-side upload before the server's per-workspace limit
// applies (the server is the authority; this just guards the local read).
const maxCLIUpload = 64 << 20

// readLocalFile reads a local file for upload, bounded by maxCLIUpload.
func readLocalFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, maxCLIUpload))
}

// cmd_agent_workspace.go is the operator CLI for the governed workspace plane:
// `olivares agent workspace {add|ls|rm-workspace|files|stat|get|put|mkdir|mv|rm}`.
// Every subcommand is a THIN HTTP client against /v1/m/sessions/workspaces* — all jail,
// RBAC, DLP and audit logic lives server-side (the CLI never touches the filesystem).

func newAgentWorkspaceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workspace",
		Short: "Manage governed workspaces and their files (browse/read/write/move/delete)",
		Long: "workspace registers the host directories a governed session is allowed to see,\n" +
			"and gives the operator the same governed file access the session gets: list,\n" +
			"stat, read, write, move and delete — each subject to the workspace's mode and\n" +
			"DLP posture rather than to the caller's own filesystem permissions.\n\n" +
			"rm deletes content INSIDE a workspace; rm-workspace deregisters the workspace\n" +
			"and never touches the host files.",
		Example: "  olivares agent workspace ls -o json\n" +
			"  olivares agent workspace add /srv/projects/acme --name acme --mode ro --dlp deny\n" +
			"  olivares agent workspace get ws-123 reports/q3.csv > q3.csv",
	}
	cmd.AddCommand(
		newWorkspaceAddCmd(),
		newWorkspaceListCmd(),
		newWorkspaceRemoveCmd(),
		newWorkspaceFilesCmd(),
		newWorkspaceStatCmd(),
		newWorkspaceGetCmd(),
		newWorkspacePutCmd(),
		newWorkspaceMkdirCmd(),
		newWorkspaceMoveCmd(),
		newWorkspaceRmCmd(),
	)
	return cmd
}

func newWorkspaceAddCmd() *cobra.Command {
	var (
		cfg                     agentClientConfig
		name, mode, target, dlp string
		maxRead                 int64
		subpaths                []string
	)
	cmd := &cobra.Command{
		Use:     "add <root-path>",
		Short:   "Register a host directory as a governed workspace",
		Long:    "add registers a host directory with its mount mode, container target, DLP posture and optional allowed subpaths.",
		Example: "  olivares agent workspace add /srv/projects/acme --name acme --mode ro --dlp deny",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			body := map[string]any{
				"root_path": args[0], "name": name, "mount_mode": mode,
				"container_target": target, "dlp_mode": dlp, "allow_subpaths": subpaths,
			}
			if maxRead > 0 {
				body["max_read_bytes"] = maxRead
			}
			status, b, err := cfg.do(cmd.Context(), "POST", "/v1/m/sessions/workspaces", body)
			if err != nil {
				return err
			}
			return printWorkspace(cmd, status, b, http.StatusCreated)
		},
	}
	cfg.addFlags(cmd)
	cmd.Flags().StringVar(&name, "name", "", "display name")
	cmd.Flags().StringVar(&mode, "mode", "rw", "mount mode: rw|ro")
	cmd.Flags().StringVar(&target, "target", "/workspace", "container mount target path")
	cmd.Flags().StringVar(&dlp, "dlp", "label", "DLP posture on reads: label|deny|off")
	_ = cmd.RegisterFlagCompletionFunc("mode", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"rw", "ro"}, cobra.ShellCompDirectiveNoFileComp
	})
	_ = cmd.RegisterFlagCompletionFunc("dlp", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"label", "deny", "off"}, cobra.ShellCompDirectiveNoFileComp
	})
	cmd.Flags().Int64Var(&maxRead, "max-read", 0, "per-read size cap in bytes (0 = default 5 MiB)")
	cmd.Flags().StringSliceVar(&subpaths, "subpath", nil, "restrict the file API to these relative subpaths (repeatable)")
	addDeprecatedJSONFlag(cmd)
	return cmd
}

func newWorkspaceListCmd() *cobra.Command {
	var cfg agentClientConfig
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List registered workspaces",
		Long:    "ls lists the governed workspaces registered for the configured tenant, including their mount and DLP posture.",
		Example: "  olivares agent workspace ls -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			status, b, err := cfg.do(cmd.Context(), "GET", "/v1/m/sessions/workspaces", nil)
			if err != nil {
				return err
			}
			if status != http.StatusOK {
				return httpErr(status, b)
			}
			var resp struct {
				Items []map[string]any `json:"items"`
			}
			if err := json.Unmarshal(b, &resp); err != nil {
				return err
			}
			return renderListOut(cmd, resp.Items, "no workspaces registered", func(out io.Writer, it map[string]any) error {
				_, err := fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%s\n",
					str(it, "workspace_ref"), str(it, "mount_mode"), str(it, "dlp_mode"), str(it, "state"), str(it, "root_path"))
				return err
			}, json.RawMessage(b))
		},
	}
	cfg.addFlags(cmd)
	addDeprecatedJSONFlag(cmd)
	return cmd
}

func newWorkspaceRemoveCmd() *cobra.Command {
	var cfg agentClientConfig
	cmd := &cobra.Command{
		Use:               "rm-workspace <ref>",
		Short:             "Deregister a workspace (does NOT delete host files)",
		Long:              "rm-workspace removes a workspace registration from Olivares without deleting any files from the host directory.",
		Example:           "  olivares agent workspace rm-workspace ws-123",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeWorkspaces,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			status, b, err := cfg.do(cmd.Context(), "DELETE", "/v1/m/sessions/workspaces/"+args[0], nil)
			if err != nil {
				return err
			}
			if status != http.StatusOK {
				return httpErr(status, b)
			}
			return nil
		},
	}
	cfg.addFlags(cmd)
	return cmd
}

func newWorkspaceFilesCmd() *cobra.Command {
	var (
		cfg  agentClientConfig
		path string
	)
	cmd := &cobra.Command{
		Use:               "files <ref>",
		Short:             "List one directory level in a workspace",
		Long:              "files lists one directory level within a governed workspace; use --path to select a directory below its root.",
		Example:           "  olivares agent workspace files ws-123 --path reports/2026",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeWorkspaces,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			status, b, err := cfg.do(cmd.Context(), "GET", filesPath(args[0], "", path), nil)
			if err != nil {
				return err
			}
			if status != http.StatusOK {
				return httpErr(status, b)
			}
			var resp struct {
				Entries []map[string]any `json:"entries"`
			}
			if err := json.Unmarshal(b, &resp); err != nil {
				return err
			}
			return renderListOut(cmd, resp.Entries, "no files", func(out io.Writer, e map[string]any) error {
				_, err := fmt.Fprintf(out, "%s\t%v\t%s\n", str(e, "type"), e["size"], str(e, "path"))
				return err
			}, json.RawMessage(b))
		},
	}
	cfg.addFlags(cmd)
	cmd.Flags().StringVar(&path, "path", "", "relative directory path (default: workspace root)")
	addDeprecatedJSONFlag(cmd)
	return cmd
}

func newWorkspaceStatCmd() *cobra.Command {
	var cfg agentClientConfig
	cmd := &cobra.Command{
		Use:               "stat <ref> <path>",
		Short:             "Show metadata for one path",
		Long:              "stat retrieves governed metadata for one file or directory path inside a registered workspace.",
		Example:           "  olivares agent workspace stat ws-123 reports/q3.csv",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeWorkspaces,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			status, b, err := cfg.do(cmd.Context(), "GET", filesPath(args[0], "stat", args[1]), nil)
			if err != nil {
				return err
			}
			if status != http.StatusOK {
				return httpErr(status, b)
			}
			return printRaw(cmd, b)
		},
	}
	cfg.addFlags(cmd)
	return cmd
}

func newWorkspaceGetCmd() *cobra.Command {
	var cfg agentClientConfig
	cmd := &cobra.Command{
		Use:               "get <ref> <path>",
		Short:             "Read a file's content to stdout (DLP-governed)",
		Long:              "get reads one file through the governed workspace API, applies the workspace DLP posture, and writes its content to stdout.",
		Example:           "  olivares agent workspace get ws-123 reports/q3.csv > q3.csv",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeWorkspaces,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			status, b, err := cfg.do(cmd.Context(), "GET", filesPath(args[0], "raw", args[1]), nil)
			if err != nil {
				return err
			}
			if status != http.StatusOK {
				return httpErr(status, b)
			}
			var resp struct {
				Encoding string `json:"encoding"`
				Content  string `json:"content"`
			}
			if err := json.Unmarshal(b, &resp); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if resp.Encoding == "base64" {
				dec, derr := base64.StdEncoding.DecodeString(resp.Content)
				if derr != nil {
					return derr
				}
				_, err = out.Write(dec)
				return err
			}
			_, err = io.WriteString(out, resp.Content)
			return err
		},
	}
	cfg.addFlags(cmd)
	return cmd
}

func newWorkspacePutCmd() *cobra.Command {
	var (
		cfg  agentClientConfig
		from string
	)
	cmd := &cobra.Command{
		Use:               "put <ref> <path>",
		Short:             "Write a file from --from (a local file or '-' for stdin)",
		Long:              "put writes content from a local file or stdin to a path inside a writable governed workspace.",
		Example:           "  olivares agent workspace put ws-123 reports/q3.csv --from ./q3.csv",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeWorkspaces,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			content, err := readPutSource(cmd, from)
			if err != nil {
				return err
			}
			status, b, err := cfg.putRaw(cmd.Context(), filesPath(args[0], "raw", args[1]), content)
			if err != nil {
				return err
			}
			if status != http.StatusOK {
				return httpErr(status, b)
			}
			return printRaw(cmd, b)
		},
	}
	cfg.addFlags(cmd)
	cmd.Flags().StringVar(&from, "from", "-", "source: a local file path, or '-' for stdin")
	return cmd
}

func newWorkspaceMkdirCmd() *cobra.Command {
	var cfg agentClientConfig
	cmd := &cobra.Command{
		Use:               "mkdir <ref> <path>",
		Short:             "Create a directory (and parents)",
		Long:              "mkdir creates a directory and any missing parents inside a writable governed workspace.",
		Example:           "  olivares agent workspace mkdir ws-123 reports/2026/q3",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeWorkspaces,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			status, b, err := cfg.do(cmd.Context(), "POST", filesPath(args[0], "dir", args[1]), nil)
			if err != nil {
				return err
			}
			if status != http.StatusCreated {
				return httpErr(status, b)
			}
			return printRaw(cmd, b)
		},
	}
	cfg.addFlags(cmd)
	return cmd
}

func newWorkspaceMoveCmd() *cobra.Command {
	var cfg agentClientConfig
	cmd := &cobra.Command{
		Use:               "mv <ref> <from> <to>",
		Short:             "Move/rename a path within the workspace",
		Long:              "mv moves or renames a file or directory between two paths in the same governed workspace.",
		Example:           "  olivares agent workspace mv ws-123 drafts/q3.csv reports/q3.csv",
		Args:              cobra.ExactArgs(3),
		ValidArgsFunction: completeWorkspaces,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			status, b, err := cfg.do(cmd.Context(), "POST",
				"/v1/m/sessions/workspaces/"+url.PathEscape(args[0])+"/files/move",
				map[string]any{"from": args[1], "to": args[2]})
			if err != nil {
				return err
			}
			if status != http.StatusOK {
				return httpErr(status, b)
			}
			return nil
		},
	}
	cfg.addFlags(cmd)
	return cmd
}

func newWorkspaceRmCmd() *cobra.Command {
	var (
		cfg       agentClientConfig
		recursive bool
		yes       bool
	)
	cmd := &cobra.Command{
		Use:   "rm <ref> <path>",
		Short: "Delete a file or (with --recursive) a directory subtree",
		Long: "rm deletes a file from a governed workspace, or deletes a directory subtree when --recursive is supplied.\n\n" +
			"A --recursive delete asks for confirmation, because the caller names a directory and the\n" +
			"server deletes everything under it. In a non-interactive session there is nobody to ask, so\n" +
			"it refuses unless --yes states the intent. List what would go first with `workspace files`.",
		Example: "  olivares agent workspace files ws-123 --path obsolete\n" +
			"  olivares agent workspace rm ws-123 obsolete --recursive --yes",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeWorkspaces,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			path := filesPath(args[0], "", args[1])
			if recursive {
				// A recursive delete is the one verb here whose blast radius is not
				// the argument the operator typed: it is everything beneath it.
				if err := confirmDestructive(cmd, yes, fmt.Sprintf(
					"recursively delete %q and everything under it in workspace %q",
					args[1], args[0])); err != nil {
					return err
				}
				path += "&recursive=true"
			}
			status, b, err := cfg.do(cmd.Context(), "DELETE", path, nil)
			if err != nil {
				return err
			}
			if status != http.StatusOK {
				return httpErr(status, b)
			}
			return nil
		},
	}
	cfg.addFlags(cmd)
	cmd.Flags().BoolVar(&recursive, "recursive", false, "delete a directory and its contents")
	addYesFlag(cmd, &yes)
	return cmd
}

// filesPath builds /v1/m/sessions/workspaces/<ref>/files[/<sub>]?path=<rel>. sub is
// "" (list/delete), "stat", "raw", or "dir".
func filesPath(ref, sub, rel string) string {
	p := "/v1/m/sessions/workspaces/" + url.PathEscape(ref) + "/files"
	if sub != "" {
		p += "/" + sub
	}
	return p + "?path=" + url.QueryEscape(rel)
}

// putRaw sends a raw (non-JSON) body, used to upload file content verbatim.
func (c *agentClientConfig) putRaw(ctx context.Context, path string, raw []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.server+path, bytes.NewReader(raw))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-Olivares-Tenant", c.tenant)
	req.Header.Set("Content-Type", "application/octet-stream")
	client, err := c.transport(c.timeout)
	if err != nil {
		return 0, nil, err
	}
	resp, err := cliDo(client, req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, b, err
}

// readPutSource reads the upload content from a local file or stdin ('-').
func readPutSource(cmd *cobra.Command, from string) ([]byte, error) {
	if from == "" || from == "-" {
		return io.ReadAll(io.LimitReader(cmd.InOrStdin(), maxCLIUpload))
	}
	return readLocalFile(from)
}

// printWorkspace renders a workspace DTO response (human summary or raw JSON).
func printWorkspace(cmd *cobra.Command, status int, b []byte, want int) error {
	if status != want {
		return httpErr(status, b)
	}
	var ws map[string]any
	if err := json.Unmarshal(b, &ws); err != nil {
		return err
	}
	return renderOut(cmd, func(out io.Writer) error {
		_, err := fmt.Fprintf(out, "workspace %s mode=%s dlp=%s root=%s\n",
			str(ws, "workspace_ref"), str(ws, "mount_mode"), str(ws, "dlp_mode"), str(ws, "root_path"))
		return err
	}, json.RawMessage(b))
}
