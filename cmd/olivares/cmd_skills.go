// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/skills"
	"github.com/spf13/cobra"
)

func newSkillsCmd() *cobra.Command {
	flags := &authClientFlags{}
	cmd := &cobra.Command{Annotations: map[string]string{"changes_data": "read"}, Use: "skills", Short: "Install and manage immutable skills in the catalog", Long: "Install reviewed instruction packs in the organization catalog. Assign a revision to deliver its members on launch. Portable skills use the selected tool's native discovery; agents, commands and rules use Claude Code.", Args: cobra.NoArgs}
	cmd.Example = "  olivares skills ls"
	flags.addPersistent(cmd)
	client := datalaneClient{flags: flags, base: "/v1/m/skills", what: "skills"}
	cmd.AddCommand(newSkillsImportCmd(client, false), newSkillsImportCmd(client, true), newSkillsListCmd(client), newSkillsGetCmd(client), newSkillsAssignCmd(client), newSkillsAssignmentsCmd(client), newSkillsUnassignCmd(client), newSkillsRemoveCmd(client),
		newSkillsDiffCmd(client), newSkillsPinCmd(client), newSkillsSelectionCmd(client), newSkillsEnableCmd(client, true, "write"), newSkillsEnableCmd(client, false, "delete"))
	return cmd
}
func newSkillsListCmd(client datalaneClient) *cobra.Command {
	var page datalanePageFlags
	var builtin bool
	cmd := &cobra.Command{Annotations: map[string]string{"changes_data": "read"}, Use: "ls", Aliases: []string{"list"}, Short: "List installed catalog packs", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		q, err := page.values(cmd, nil)
		if err != nil {
			return err
		}
		path := "/packs?" + q.Encode()
		if builtin {
			if cmd.Flags().Changed("cursor") || cmd.Flags().Changed("limit") {
				return exitcode.New(exitcode.Usage, fmt.Errorf("--builtin lists the complete built-in catalog; pagination flags apply to installed packs"))
			}
			path = "/builtins"
		}
		raw, status, err := skillsRequest(cmd, client, "GET", path, nil, "", nil)
		if err != nil {
			return err
		}
		if !datalaneOK(status) {
			return datalaneHTTPError("skills", status, raw)
		}
		if builtin {
			return datalaneRenderList(cmd, "skills", raw, "no built-in skills available", datalaneCols("ID", "id", "ORIGIN", "origin", "COMMIT", "resolved_commit", "DIGEST", "source_digest"))
		}
		return datalaneRenderList(cmd, "skills", raw, "no skills installed in the catalog", datalaneCols("ID", "id", "NAME", "name", "STATE", "state", "REVISION", "latest_revision"))
	}}
	cmd.Flags().BoolVar(&builtin, "builtin", false, "browse the pinned built-in catalog")
	cmd.Long = "List installed skill packs or browse available packs with --builtin; use olivares skills get <pack-id> to inspect a pack and its revisions."
	cmd.Example = "  olivares skills ls"
	page.add(cmd)
	return cmd
}
func newSkillsGetCmd(client datalaneClient) *cobra.Command {
	var page datalanePageFlags
	cmd := &cobra.Command{Annotations: map[string]string{"changes_data": "read"}, Use: "get <pack-id>", Short: "Show catalog provenance and immutable revisions", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		path, err := datalanePath("packs", args[0])
		if err != nil {
			return err
		}
		q, err := page.values(cmd, nil)
		if err != nil {
			return err
		}
		raw, status, err := skillsRequest(cmd, client, "GET", path+"?"+q.Encode(), nil, "", nil)
		if err != nil {
			return err
		}
		if !datalaneOK(status) {
			return datalaneHTTPError("skills", status, raw)
		}
		return datalaneResult(cmd, raw, status, "")
	}}
	cmd.Long = "Show a skill pack's origin and immutable revisions; use olivares skills assign to bind a revision."
	cmd.Example = "  olivares skills get <pack-id>"
	page.add(cmd)
	return cmd
}
func newSkillsImportCmd(client datalaneClient, update bool) *cobra.Command {
	var name, builtin, gitURL, ref, subdir, folder, archive, format, key, expected string
	var apply, showDiff bool
	use := "install"
	args := cobra.NoArgs
	if update {
		use = "update <pack-id>"
		args = cobra.ExactArgs(1)
	}
	cmd := &cobra.Command{Annotations: map[string]string{"changes_data": "write"}, Use: use, Short: "Publish a reviewed immutable catalog revision", Args: args, RunE: func(cmd *cobra.Command, args []string) error {
		count := 0
		for _, s := range []string{builtin, gitURL, folder, archive} {
			if s != "" {
				count++
			}
		}
		if update && count == 0 && format == "" {
			// No source named: read the pack's recorded source again (check, then apply).
			return skillsUpdateFromSource(cmd, client, args[0], ref, subdir, expected, key, apply, showDiff)
		}
		if count != 1 || (gitURL == "" && (ref != "" || subdir != "")) || (format != "" && archive == "") {
			return exitcode.New(exitcode.Usage, fmt.Errorf("choose exactly one of --builtin, --git, --folder, or --archive; --ref and --subdir apply to Git, and --format applies to archives"))
		}
		if gitURL != "" && ref == "" {
			ref = "HEAD"
		}
		if !update && name == "" {
			sourceName := folder
			if builtin != "" {
				sourceName = builtin
			}
			if gitURL != "" {
				parsed, err := url.Parse(gitURL)
				if err != nil {
					return exitcode.New(exitcode.Usage, fmt.Errorf("invalid Git URL"))
				}
				sourceName = parsed.Path
			} else if archive != "" {
				sourceName = archive
			}
			name = filepath.Base(filepath.Clean(sourceName))
			for _, suffix := range []string{".tar.gz", ".zip", ".git"} {
				name = strings.TrimSuffix(name, suffix)
			}
			if name == "" || name == "." || name == string(filepath.Separator) {
				name = "skills"
			}
		}
		path := "/packs"
		if update {
			var err error
			path, err = datalanePath("packs", args[0], "revisions")
			if err != nil {
				return err
			}
		}
		if key == "" {
			key = model.NewID().String()
			fmt.Fprintln(cmd.ErrOrStderr(), "Import retry key:", key)
		}
		if len(key) > 128 || strings.IndexFunc(key, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
			return exitcode.New(exitcode.Usage, fmt.Errorf("--idempotency-key must contain 1 to 128 printable non-space ASCII characters"))
		}
		var body []byte
		contentType := "application/json"
		parent := cmd.Context()
		ctx, cancel := context.WithTimeout(parent, 60*time.Second)
		cmd.SetContext(ctx)
		defer func() { cancel(); cmd.SetContext(parent) }()
		if builtin != "" {
			body, _ = json.Marshal(map[string]any{"name": name, "source": map[string]string{"kind": "builtin", "ref": builtin, "expected_digest": expected}})
		} else if gitURL != "" {
			body, _ = json.Marshal(map[string]any{"name": name, "source": map[string]string{"kind": "git", "url": gitURL, "ref": ref, "subdir": subdir, "expected_digest": expected}})
		} else {
			var pack *skills.ValidatedPack
			var err error
			uploadExpected := expected
			if folder != "" {
				pack, err = skills.ImportFolder(ctx, folder)
				if err != nil {
					return exitcode.New(exitcode.Usage, err)
				}
				if expected != "" && expected != pack.SourceDigest {
					return exitcode.New(exitcode.Usage, fmt.Errorf("folder does not match its expected digest"))
				}
				body, err = pack.Zip(ctx)
				format = "zip"
				sum := sha256.Sum256(body)
				uploadExpected = fmt.Sprintf("%x", sum)
			} else {
				info, e := os.Lstat(archive)
				if e != nil || !info.Mode().IsRegular() {
					return exitcode.New(exitcode.Usage, fmt.Errorf("archive must be a regular file"))
				}
				// Nonblocking open also covers a pathname swapped to a FIFO after
				// Lstat. Judge identity and type again on the opened descriptor.
				f, e := os.OpenFile(archive, os.O_RDONLY|syscall.O_NONBLOCK, 0)
				if e != nil {
					return exitcode.New(exitcode.Usage, fmt.Errorf("archive could not be opened"))
				}
				opened, e := f.Stat()
				if e != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
					_ = f.Close()
					return exitcode.New(exitcode.Usage, fmt.Errorf("archive changed or is not a regular file"))
				}
				stopRead := context.AfterFunc(ctx, func() { _ = f.Close() })
				defer stopRead()
				reader := io.LimitReader(f, skills.MaxSourceBytes+1)
				chunk := make([]byte, 32<<10)
				for {
					if err = ctx.Err(); err != nil {
						break
					}
					n, readErr := reader.Read(chunk)
					body = append(body, chunk[:n]...)
					if readErr != nil {
						if readErr != io.EOF {
							err = readErr
						}
						break
					}
				}
				closeErr := f.Close()
				stopRead()
				if ctx.Err() != nil {
					return exitcode.New(exitcode.Usage, ctx.Err())
				}
				if err == nil {
					err = closeErr
				}
				if err != nil || len(body) > skills.MaxSourceBytes {
					return exitcode.New(exitcode.Usage, fmt.Errorf("archive exceeds the import limit or could not be read"))
				}
				if format == "" {
					if strings.HasSuffix(strings.ToLower(filepath.Base(archive)), ".zip") {
						format = "zip"
					} else {
						format = "tar.gz"
					}
				}
				_, err = skills.ImportArchive(ctx, bytes.NewReader(body), format, expected)
			}
			if err != nil {
				return exitcode.New(exitcode.Usage, err)
			}
			var encoded bytes.Buffer
			writer := multipart.NewWriter(&encoded)
			_ = writer.WriteField("name", name)
			_ = writer.WriteField("format", format)
			_ = writer.WriteField("expected_digest", uploadExpected)
			part, err := writer.CreateFormFile("archive", "pack."+format)
			if err != nil {
				return err
			}
			if _, err := part.Write(body); err != nil {
				return err
			}
			if err := writer.Close(); err != nil {
				return err
			}
			body, contentType = encoded.Bytes(), writer.FormDataContentType()
		}
		raw, status, err := skillsRequest(cmd, client, "POST", path, body, contentType, http.Header{"Idempotency-Key": []string{key}})
		if err != nil {
			return err
		}
		if status != http.StatusCreated && status != http.StatusOK {
			return datalaneHTTPError("skills", status, raw)
		}
		return datalaneResult(cmd, raw, status, "")
	}}
	cmd.Long = "Import a reviewed skill pack from the built-in catalog, Git, a folder or an archive; inspect the catalog with olivares skills ls."
	cmd.Example = "  olivares skills " + use + " --folder ./skills"
	cmd.Flags().StringVar(&name, "name", "", "catalog pack name (defaults to the selected source name)")
	cmd.Flags().StringVar(&builtin, "builtin", "", "pinned built-in pack ID (browse with skills ls --builtin)")
	cmd.Flags().StringVar(&gitURL, "git", "", "public HTTPS git URL")
	cmd.Flags().StringVar(&ref, "ref", "", "git ref or full commit (defaults to HEAD; the published revision records the resolved commit)")
	cmd.Flags().StringVar(&subdir, "subdir", "", "selected skills directory inside the git source")
	cmd.Flags().StringVar(&folder, "folder", "", "selected local folder; validated bytes are uploaded")
	cmd.Flags().StringVar(&archive, "archive", "", "local ZIP or tar.gz archive")
	cmd.Flags().StringVar(&format, "format", "", "archive format: zip or tar.gz")
	cmd.Flags().StringVar(&key, "idempotency-key", "", "retain this key to read back an ambiguous import")
	cmd.Flags().StringVar(&expected, "expected-digest", "", "expected SHA-256 source digest")
	if update {
		cmd.Flags().BoolVar(&apply, "apply", false, "with no source flag: publish the update the check showed (needs --expected-digest)")
		cmd.Flags().BoolVar(&showDiff, "diff", false, "with no source flag: print the unified text diff of each changed file")
		cmd.Long = "Publish a new immutable revision of a pack. With --builtin, --git, --folder or --archive it publishes that source. With none it reads the pack's recorded source again, shows what changed and the digest to apply; then run it again with --apply --expected-digest <digest>. Existing pins keep their revision until you move them with olivares skills pin."
	}
	return cmd
}

func newSkillsRemoveCmd(client datalaneClient) *cobra.Command {
	var version int64
	cmd := &cobra.Command{Annotations: map[string]string{"changes_data": "delete"}, Use: "rm <pack-id>", Aliases: []string{"remove"}, Short: "Retire an unused catalog pack and retain its provenance", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		path, err := datalanePath("packs", args[0])
		if err != nil {
			return err
		}
		if version < 0 {
			return exitcode.New(exitcode.Usage, fmt.Errorf("--version must be positive"))
		}
		if version == 0 {
			raw, status, err := skillsRequest(cmd, client, "GET", path, nil, "", nil)
			if err != nil {
				return err
			}
			if status != http.StatusOK {
				return datalaneHTTPError("skills", status, raw)
			}
			var detail skills.PackDetail
			if err := json.Unmarshal(raw, &detail); err != nil || detail.Pack.Version < 1 {
				return exitcode.New(exitcode.Server, fmt.Errorf("skills detail is invalid"))
			}
			version = detail.Pack.Version
		}
		raw, status, err := skillsRequest(cmd, client, "DELETE", path, nil, "", http.Header{"If-Match": []string{strconv.FormatInt(version, 10)}})
		if err != nil {
			return err
		}
		if !datalaneOK(status) {
			return datalaneHTTPError("skills", status, raw)
		}
		return datalaneResult(cmd, raw, status, "")
	}}
	cmd.Long = "Retire an unused skill pack while retaining its provenance; inspect the catalog with olivares skills ls."
	cmd.Example = "  olivares skills rm <pack-id>"
	cmd.Flags().Int64Var(&version, "version", 0, "expected pack version (defaults to the current server version)")
	return cmd
}

// Uses the existing config resolution, trust transport, redaction and bounded
// response reader. Skills metadata keeps the catalog's admitted revision budget.
func skillsRequest(cmd *cobra.Command, c datalaneClient, method, path string, body []byte, contentType string, extra http.Header) ([]byte, int, error) {
	opts, err := c.flags.resolutionOptions(cmd)
	if err != nil {
		return nil, 0, err
	}
	resolved, err := resolveCLIConfig(opts)
	if err != nil {
		return nil, 0, redactCoded(err, c.flags.token)
	}
	client, headers, err := cliTransport(cliTransportOptions{Resolved: resolved, Insecure: c.flags.insecure, Timeout: c.flags.timeout, Stderr: cmd.ErrOrStderr()})
	if err != nil {
		return nil, 0, exitcode.Or(exitcode.Server, redactCoded(err, resolved.Token))
	}
	req, err := http.NewRequestWithContext(cmd.Context(), method, resolved.Server+c.base+path, bytes.NewReader(body))
	if err != nil {
		return nil, 0, redactCoded(err, resolved.Token)
	}
	req.Header = headers.Clone()
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for key, values := range extra {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	response, err := cliDo(client, req)
	if err != nil {
		if method == http.MethodGet {
			return nil, 0, exitcode.Or(exitcode.Server, redactCoded(err, resolved.Token))
		}
		return nil, 0, exitcode.New(exitcode.Indeterminate, redactCoded(err, resolved.Token))
	}
	defer response.Body.Close()
	raw, err := readCLIHTTPResponse(response, req, skills.MaxCatalogResponseBytes+1, datalaneOK(response.StatusCode), func(status int, body []byte) error { return datalaneHTTPError("skills", status, body) })
	if err != nil {
		return nil, response.StatusCode, wrapCLIResponseReadError(err, "read skills response")
	}
	if len(raw) > skills.MaxCatalogResponseBytes {
		return nil, response.StatusCode, exitcode.New(exitcode.Server, fmt.Errorf("skills response exceeds its limit"))
	}
	return raw, response.StatusCode, nil
}

type skillsTargetFlags struct{ workspace, profile, folder, group, agent, template, session string }

func (f *skillsTargetFlags) add(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.workspace, "workspace", "", "stored workspace (department) identifier")
	cmd.Flags().StringVar(&f.folder, "folder", "", "registered folder workspace reference")
	cmd.Flags().StringVar(&f.group, "group", "", "stored agent group identifier")
	cmd.Flags().StringVar(&f.agent, "agent", "", "stored agent identifier")
	cmd.Flags().StringVar(&f.template, "template", "", "stored template identifier")
	cmd.Flags().StringVar(&f.session, "session", "", "stored governed session identifier")
}

// addSkills adds the profile target to skills commands. MCP shares the existing
// target flags, but its roster does not support profile targets.
func (f *skillsTargetFlags) addSkills(cmd *cobra.Command) {
	f.add(cmd)
	cmd.Flags().StringVar(&f.profile, "profile", "", "provider profile reference")
}

// targets returns the targets named on the command line, in flag order.
func (f *skillsTargetFlags) targets() []skills.Target {
	var out []skills.Target
	for _, pair := range []struct{ kind, id string }{{"workspace", f.workspace}, {"profile", f.profile}, {"folder", f.folder}, {"agent_group", f.group}, {"agent", f.agent}, {"template", f.template}, {"session", f.session}} {
		if pair.id != "" {
			out = append(out, skills.Target{Kind: pair.kind, ID: pair.id})
		}
	}
	return out
}
func (f *skillsTargetFlags) target() (skills.Target, error) {
	targets := f.targets()
	if len(targets) != 1 {
		return skills.Target{}, exitcode.New(exitcode.Usage, fmt.Errorf("choose exactly one target flag"))
	}
	return targets[0], nil
}
func newSkillsAssignCmd(client datalaneClient) *cobra.Command {
	var target skillsTargetFlags
	var revision, assignment string
	var members []string
	var version int64
	var approve bool
	cmd := &cobra.Command{Annotations: map[string]string{"changes_data": "write"}, Use: "assign <pack-id>", Short: "Pin a catalog revision for new conversations", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		t, err := target.target()
		if err != nil {
			return err
		}
		if revision == "" {
			return exitcode.New(exitcode.Usage, fmt.Errorf("--revision is required"))
		}
		// Verify that the named pack actually contains the revision; the server
		// rechecks the immutable revision under its own tenant/target authority.
		path, err := datalanePath("packs", args[0])
		if err != nil {
			return err
		}
		cursor := ""
		found := false
		for {
			query := url.Values{"limit": []string{"200"}, "cursor": []string{cursor}}
			raw, status, err := skillsRequest(cmd, client, "GET", path+"?"+query.Encode(), nil, "", nil)
			if err != nil {
				return err
			}
			if status != http.StatusOK {
				return datalaneHTTPError("skills", status, raw)
			}
			var detail skills.PackDetail
			if err := json.Unmarshal(raw, &detail); err != nil {
				return exitcode.New(exitcode.Server, fmt.Errorf("skills detail is invalid"))
			}
			for _, r := range detail.Revisions {
				if r.ID == revision {
					found = true
				}
			}
			if found || !detail.HasMore {
				break
			}
			if detail.Cursor == "" || detail.Cursor == cursor {
				return exitcode.New(exitcode.Server, fmt.Errorf("skills revision pagination did not advance"))
			}
			cursor = detail.Cursor
		}
		if !found {
			return exitcode.New(exitcode.Usage, fmt.Errorf("revision does not belong to this pack"))
		}
		body, _ := json.Marshal(skills.AssignmentRequest{Target: t, RevisionID: revision, Members: members, ApproveActiveContent: approve})
		method, path, headers := "POST", "/assignments", http.Header{}
		if assignment != "" {
			if version < 1 {
				return exitcode.New(exitcode.Usage, fmt.Errorf("--version is required when changing --assignment"))
			}
			path, err = datalanePath("assignments", assignment)
			if err != nil {
				return err
			}
			method = "PUT"
			headers.Set("If-Match", strconv.FormatInt(version, 10))
		}
		raw, status, err := skillsRequest(cmd, client, method, path, body, "application/json", headers)
		if err != nil {
			return err
		}
		if !datalaneOK(status) {
			return datalaneHTTPError("skills", status, raw)
		}
		return datalaneResult(cmd, raw, status, "")
	}}
	cmd.Long = "Pin a catalog revision to a workspace (department), agent group, agent, template or session; inspect bindings with olivares skills assignments."
	cmd.Example = "  olivares skills assign <pack-id> --revision <revision-id> --workspace <workspace-id>"
	target.addSkills(cmd)
	cmd.Flags().StringVar(&revision, "revision", "", "immutable revision identifier")
	cmd.Flags().StringSliceVar(&members, "member", nil, "selected member names (all revision members when omitted)")
	cmd.Flags().StringVar(&assignment, "assignment", "", "existing binding to change")
	cmd.Flags().Int64Var(&version, "version", 0, "current binding version for a change")
	cmd.Flags().BoolVar(&approve, "approve-active-content", false, "approve skills that carry hooks, shell interpolation, tool pre-approvals or plugin files")
	return cmd
}
func newSkillsAssignmentsCmd(client datalaneClient) *cobra.Command {
	var target skillsTargetFlags
	var page datalanePageFlags
	var pack string
	cmd := &cobra.Command{Annotations: map[string]string{"changes_data": "read"}, Use: "assignments", Short: "List pinned bindings on an authorized target or of one pack", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		path, filter := "/assignments", url.Values{}
		if pack != "" {
			if len(target.targets()) != 0 {
				return exitcode.New(exitcode.Usage, fmt.Errorf("choose --pack or one target, not both"))
			}
			var err error
			if path, err = datalanePath("packs", pack, "assignments"); err != nil {
				return err
			}
		} else {
			t, err := target.target()
			if err != nil {
				return err
			}
			filter = url.Values{"target_kind": []string{t.Kind}, "target_id": []string{t.ID}}
		}
		q, err := page.values(cmd, filter)
		if err != nil {
			return err
		}
		raw, status, err := skillsRequest(cmd, client, "GET", path+"?"+q.Encode(), nil, "", nil)
		if err != nil {
			return err
		}
		if !datalaneOK(status) {
			return datalaneHTTPError("skills", status, raw)
		}
		return datalaneRenderList(cmd, "skills assignments", raw, "no skills assignments you can read", datalaneCols("ID", "id", "TARGET", "target_kind", "TARGET ID", "target_id", "PACK", "pack_id", "REVISION", "pack_revision_id", "VERSION", "version"))
	}}
	cmd.Long = "List pinned revisions for one workspace (department), agent group, agent, template or session, or with --pack every readable target a pack is pinned to; use olivares skills get <pack-id> to inspect a pack."
	cmd.Example = "  olivares skills assignments --workspace <workspace-id>\n  olivares skills assignments --pack <pack-id>"
	target.addSkills(cmd)
	cmd.Flags().StringVar(&pack, "pack", "", "catalog pack identifier: list the targets it is pinned to")
	page.add(cmd)
	return cmd
}
func newSkillsUnassignCmd(client datalaneClient) *cobra.Command {
	var version int64
	cmd := &cobra.Command{Annotations: map[string]string{"changes_data": "delete"}, Use: "unassign <assignment-id>", Short: "Remove future inheritance; recorded conversations retain their snapshot", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if version < 1 {
			return exitcode.New(exitcode.Usage, fmt.Errorf("--version is required"))
		}
		path, err := datalanePath("assignments", args[0])
		if err != nil {
			return err
		}
		raw, status, err := skillsRequest(cmd, client, "DELETE", path, nil, "", http.Header{"If-Match": []string{strconv.FormatInt(version, 10)}})
		if err != nil {
			return err
		}
		if !datalaneOK(status) {
			return datalaneHTTPError("skills", status, raw)
		}
		return datalaneResult(cmd, raw, status, "")
	}}
	cmd.Long = "Remove a binding from future conversations while retaining existing snapshots; inspect bindings with olivares skills assignments."
	cmd.Example = "  olivares skills unassign <assignment-id> --version <version>"
	cmd.Flags().Int64Var(&version, "version", 0, "current binding version")
	return cmd
}
