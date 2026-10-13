// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/skills"
	"github.com/spf13/cobra"
)

// The lifecycle verbs below sit on the catalog and assignment routes the engine
// already serves: they resolve names to packs and revisions, read before they
// write, and leave every authorization decision to those routes.

type skillsPage[T any] struct {
	Items   []T    `json:"items"`
	Cursor  string `json:"cursor"`
	HasMore bool   `json:"has_more"`
}

// skillsReadAll follows a list route's cursor to its end.
func skillsReadAll[T any](cmd *cobra.Command, client datalaneClient, path string, q url.Values) ([]T, error) {
	var out []T
	cursor := ""
	for {
		query := url.Values{"limit": []string{"200"}}
		for k, v := range q {
			query[k] = v
		}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		raw, status, err := skillsRequest(cmd, client, "GET", path+"?"+query.Encode(), nil, "", nil)
		if err != nil {
			return nil, err
		}
		if !datalaneOK(status) {
			return nil, datalaneHTTPError("skills", status, raw)
		}
		var page skillsPage[T]
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, exitcode.New(exitcode.Server, fmt.Errorf("skills list is invalid"))
		}
		out = append(out, page.Items...)
		if !page.HasMore {
			return out, nil
		}
		if page.Cursor == "" || page.Cursor == cursor {
			return nil, exitcode.New(exitcode.Server, fmt.Errorf("skills pagination did not advance"))
		}
		cursor = page.Cursor
	}
}

// skillsPackHistory reads a pack and every revision it holds, oldest first.
func skillsPackHistory(cmd *cobra.Command, client datalaneClient, packID string) (skills.Pack, []skills.Revision, error) {
	path, err := datalanePath("packs", packID)
	if err != nil {
		return skills.Pack{}, nil, err
	}
	var pack skills.Pack
	var revisions []skills.Revision
	cursor := ""
	for {
		query := url.Values{"limit": []string{"200"}}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		raw, status, err := skillsRequest(cmd, client, "GET", path+"?"+query.Encode(), nil, "", nil)
		if err != nil {
			return pack, nil, err
		}
		if !datalaneOK(status) {
			return pack, nil, datalaneHTTPError("skills", status, raw)
		}
		var detail skills.PackDetail
		if err := json.Unmarshal(raw, &detail); err != nil {
			return pack, nil, exitcode.New(exitcode.Server, fmt.Errorf("skills detail is invalid"))
		}
		pack = detail.Pack
		revisions = append(revisions, detail.Revisions...)
		if !detail.HasMore {
			return pack, revisions, nil
		}
		if detail.Cursor == "" || detail.Cursor == cursor {
			return pack, nil, exitcode.New(exitcode.Server, fmt.Errorf("skills revision pagination did not advance"))
		}
		cursor = detail.Cursor
	}
}

func revisionByID(revisions []skills.Revision, id string) (skills.Revision, bool) {
	for _, r := range revisions {
		if r.ID == id {
			return r, true
		}
	}
	return skills.Revision{}, false
}

func hasMember(rev skills.Revision, name string) (skills.Member, bool) {
	for _, m := range rev.Members {
		if m.Name == name {
			return m, true
		}
	}
	return skills.Member{}, false
}

func shortCommit(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}

// skillsJSONCall sends a JSON body (or none) and returns the raw answer of a 2xx.
func skillsJSONCall(cmd *cobra.Command, client datalaneClient, method, path string, body any, headers http.Header) ([]byte, int, error) {
	var encoded []byte
	contentType := ""
	if body != nil {
		var err error
		if encoded, err = json.Marshal(body); err != nil {
			return nil, 0, err
		}
		contentType = "application/json"
	}
	raw, status, err := skillsRequest(cmd, client, method, path, encoded, contentType, headers)
	if err != nil {
		return nil, status, err
	}
	if !datalaneOK(status) {
		return raw, status, datalaneHTTPError("skills", status, raw)
	}
	return raw, status, nil
}

// renderPackDiff prints what changed between two revisions: members first, what a
// reviewer must look at (content the tool acts on) next, then the files.
func renderPackDiff(out io.Writer, d skills.PackDiff, full bool) {
	counts := map[string]int{}
	for _, m := range d.Members {
		counts[m.Change]++
	}
	fmt.Fprintf(out, "skills: %d added, %d changed, %d removed\n", counts["added"], counts["changed"], counts["removed"])
	mark := map[string]string{"added": "+", "changed": "~", "removed": "-"}
	for _, m := range d.Members {
		fmt.Fprintf(out, "  %s %s\n", mark[m.Change], termSafe(m.Name))
	}
	// A changed member that carries content its tool acts on is listed even when
	// the item was already there: its body may be what changed.
	for _, m := range d.Members {
		if len(m.ActiveContent) > 0 {
			fmt.Fprintf(out, "  ! %s carries %s (acts when loaded; assigning it needs --approve-active-content)\n", termSafe(m.Name), termSafe(strings.Join(m.ActiveContent, ", ")))
		}
	}
	fmt.Fprintf(out, "files: %d differ\n", len(d.Files))
	letters := map[string]string{"added": "A", "changed": "M", "removed": "D"}
	for _, f := range d.Files {
		note := ""
		if f.DiffOmitted != "" {
			note = " (no text diff: " + f.DiffOmitted + ")"
		}
		fmt.Fprintf(out, "  %s %s%s\n", letters[f.Change], termSafe(f.Path), note)
		if full && f.Diff != "" {
			fmt.Fprint(out, termSafe(f.Diff))
		}
	}
}

func newSkillsDiffCmd(client datalaneClient) *cobra.Command {
	var from, to string
	var full bool
	cmd := &cobra.Command{Use: "diff <pack-id>", Short: "Show what changed between two revisions of a pack", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		path, err := datalanePath("packs", args[0], "diff")
		if err != nil {
			return err
		}
		q := url.Values{}
		if from != "" {
			q.Set("from", from)
		}
		if to != "" {
			q.Set("to", to)
		}
		if len(q) > 0 {
			path += "?" + q.Encode()
		}
		raw, _, err := skillsJSONCall(cmd, client, "GET", path, nil, nil)
		if err != nil {
			return err
		}
		var d skills.RevisionDiff
		if err := json.Unmarshal(raw, &d); err != nil {
			return exitcode.New(exitcode.Server, fmt.Errorf("skills diff is invalid"))
		}
		return renderOut(cmd, func(out io.Writer) error {
			before := "(first revision)"
			if d.From != nil {
				before = fmt.Sprintf("revision %d", d.From.Number)
			}
			fmt.Fprintf(out, "%s -> revision %d\n", before, d.To.Number)
			renderPackDiff(out, d.PackDiff, full)
			return nil
		}, json.RawMessage(raw))
	}}
	cmd.Long = "Compare two immutable revisions of a pack from their verified bytes. With no flags: the latest revision against the one before it. --diff prints the text of each changed file."
	cmd.Example = "  olivares skills diff <pack-id>\n  olivares skills diff <pack-id> --from <revision-id> --to <revision-id> --diff"
	cmd.Flags().StringVar(&from, "from", "", "revision to compare from (defaults to the one before --to)")
	cmd.Flags().StringVar(&to, "to", "", "revision to compare to (defaults to the latest)")
	cmd.Flags().BoolVar(&full, "diff", false, "print the unified text diff of each changed file")
	return cmd
}

// skillsUpdateFromSource is `skills update <pack-id>` with no source flag: the
// engine reads the pack's recorded source again and answers current or
// available. Applying publishes exactly the digest the reviewer was shown.
func skillsUpdateFromSource(cmd *cobra.Command, client datalaneClient, packID, ref, subdir, expected, key string, apply, full bool) error {
	if apply && expected == "" {
		return exitcode.New(exitcode.Usage, fmt.Errorf("--apply publishes only a reviewed update: run `olivares skills update %s` first and pass the digest it prints as --expected-digest", packID))
	}
	if !apply && expected != "" {
		return exitcode.New(exitcode.Usage, fmt.Errorf("--expected-digest applies with --apply"))
	}
	path, err := datalanePath("packs", packID, "update")
	if err != nil {
		return err
	}
	body := map[string]any{"apply": apply}
	if ref != "" {
		body["ref"] = ref
	}
	if subdir != "" {
		body["subdir"] = subdir
	}
	var headers http.Header
	if apply {
		body["expected_digest"] = expected
		if key == "" {
			key = model.NewID().String()
			fmt.Fprintln(cmd.ErrOrStderr(), "Import retry key:", key)
		}
		headers = http.Header{"Idempotency-Key": []string{key}}
	}
	raw, status, err := skillsJSONCall(cmd, client, "POST", path, body, headers)
	if err != nil {
		return err
	}
	if apply {
		return datalaneResult(cmd, raw, status, "")
	}
	var check skills.UpdateCheck
	if err := json.Unmarshal(raw, &check); err != nil {
		return exitcode.New(exitcode.Server, fmt.Errorf("skills update answer is invalid"))
	}
	return renderOut(cmd, func(out io.Writer) error {
		name := check.Pack.Name
		if name == "" {
			name = packID
		}
		at := fmt.Sprintf("revision %d", check.Current.Number)
		if check.Current.ResolvedCommit != "" {
			at += " (" + shortCommit(check.Current.ResolvedCommit) + ")"
		}
		fmt.Fprintf(out, "%s: read %s\n", termSafe(name), termSafe(describeSource(check.Source)))
		switch {
		case check.State == "current":
			fmt.Fprintf(out, "%s is current\n", at)
		case check.State == "available" && check.Candidate != nil:
			fmt.Fprintf(out, "update available over %s\n", at)
			if check.Candidate.ResolvedCommit != "" {
				fmt.Fprintf(out, "source commit: %s\n", termSafe(check.Candidate.ResolvedCommit))
			}
			fmt.Fprintf(out, "digest: %s\n", check.Candidate.ManifestDigest)
			if check.Diff != nil {
				renderPackDiff(out, *check.Diff, full)
			}
			apply := fmt.Sprintf("olivares skills update %s", packID)
			if ref != "" {
				apply += " --ref " + shellWord(ref)
			}
			if subdir != "" {
				apply += " --subdir " + shellWord(subdir)
			}
			fmt.Fprintf(out, "apply what you reviewed: %s --apply --expected-digest %s\n", apply, check.Candidate.ManifestDigest)
		default:
			return exitcode.New(exitcode.Server, fmt.Errorf("the engine answered an update state this CLI does not know: %q", check.State))
		}
		return nil
	}, json.RawMessage(raw))
}

// describeSource names where an update was read from.
func describeSource(src skills.Source) string {
	text := src.Kind
	if src.Origin != "" {
		text += " " + src.Origin
	}
	if src.RequestedRef != "" {
		text += " @ " + src.RequestedRef
	}
	if src.Kind == "git" {
		if src.Subdir == "" {
			text += " (repository root)"
		} else {
			text += " (directory " + src.Subdir + ")"
		}
	}
	return text
}

func newSkillsPinCmd(client datalaneClient) *cobra.Command {
	var revision string
	var dryRun, approve bool
	cmd := &cobra.Command{Use: "pin <pack-id>", Short: "Move every pin of a pack to one revision (update or roll back)", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		pack, revisions, err := skillsPackHistory(cmd, client, args[0])
		if err != nil {
			return err
		}
		if revision == "" {
			revision = pack.LatestRevisionID
		}
		target, ok := revisionByID(revisions, revision)
		if !ok {
			return exitcode.New(exitcode.Usage, fmt.Errorf("revision does not belong to this pack"))
		}
		path, err := datalanePath("packs", args[0], "assignments")
		if err != nil {
			return err
		}
		pins, err := skillsReadAll[skills.Assignment](cmd, client, path, nil)
		if err != nil {
			return err
		}
		type outcome struct {
			Assignment string `json:"assignment_id"`
			TargetKind string `json:"target_kind"`
			TargetID   string `json:"target_id"`
			From       string `json:"from_revision"`
			Result     string `json:"result"`
			Reason     string `json:"reason,omitempty"`
		}
		var results []outcome
		failed := 0
		for _, pin := range pins {
			res := outcome{Assignment: pin.ID, TargetKind: pin.Kind, TargetID: pin.Target.ID, From: pin.RevisionID, Result: "unchanged"}
			if pin.RevisionID != target.ID {
				// What the write would refuse is refused here too, so --dry-run and the
				// real run agree: a skill missing from the revision, or one that acts
				// when loaded and was not approved.
				reason := pinMoveRefusal(target, pin, approve)
				switch {
				case reason != "":
					res.Result, res.Reason = "failed", reason
					failed++
				case dryRun:
					res.Result = "would move"
				default:
					put, err := datalanePath("assignments", pin.ID)
					if err != nil {
						res.Result, res.Reason = "failed", err.Error()
						failed++
						break
					}
					req := skills.AssignmentRequest{Target: pin.Target, RevisionID: target.ID, Members: pin.Members, ApproveActiveContent: approve}
					if _, status, err := skillsJSONCall(cmd, client, "PUT", put, req, http.Header{"If-Match": []string{strconv.FormatInt(pin.Version, 10)}}); err != nil {
						res.Result, res.Reason = "failed", fmt.Sprintf("HTTP %d: %v", status, err)
						failed++
					} else {
						res.Result = "moved"
					}
				}
			}
			results = append(results, res)
		}
		err = renderOut(cmd, func(out io.Writer) error {
			fmt.Fprintf(out, "pack %s -> revision %d\n", termSafe(pack.Name), target.Number)
			for _, r := range results {
				line := fmt.Sprintf("  %-10s %s %s:%s", r.Result, r.Assignment, r.TargetKind, r.TargetID)
				if r.Reason != "" {
					line += " (" + r.Reason + ")"
				}
				fmt.Fprintln(out, line)
			}
			if len(results) == 0 {
				fmt.Fprintln(out, "  no pins you can read")
			}
			return nil
		}, map[string]any{"pack_id": pack.ID, "revision_id": target.ID, "revision": target.Number, "dry_run": dryRun, "pins": results})
		if err != nil {
			return err
		}
		if failed > 0 {
			return exitcode.New(exitcode.Degraded, fmt.Errorf("%d of %d pins could not be moved", failed, len(results)))
		}
		return nil
	}}
	cmd.Long = "Move every pin of a pack that you can write to one revision. The selected skills of each pin are kept; a pin whose skills are not in the revision is reported and left as it was. Use it to apply an update after reviewing it, and with --revision of an older revision to roll back."
	cmd.Example = "  olivares skills pin <pack-id>\n  olivares skills pin <pack-id> --revision <older-revision-id>"
	cmd.Flags().StringVar(&revision, "revision", "", "revision to pin (defaults to the latest)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "list what would move and write nothing")
	cmd.Flags().BoolVar(&approve, "approve-active-content", false, "approve skills that carry hooks, shell interpolation or tool pre-approvals")
	return cmd
}

// pinMoveRefusal says why a pin cannot move to the revision, or "" when it can.
func pinMoveRefusal(target skills.Revision, pin skills.Assignment, approve bool) string {
	selected := pin.Members
	if len(selected) == 0 {
		for _, m := range target.Members {
			selected = append(selected, m.Name)
		}
	}
	for _, name := range selected {
		m, ok := hasMember(target, name)
		switch {
		case !ok:
			return fmt.Sprintf("skill %q is not in revision %d", termSafe(name), target.Number)
		case len(m.Active) > 0 && !approve:
			return fmt.Sprintf("skill %q acts when loaded (%s): review it and pass --approve-active-content", termSafe(name), termSafe(strings.Join(m.Active, ", ")))
		}
	}
	return ""
}

func newSkillsSelectionCmd(client datalaneClient) *cobra.Command {
	var target skillsTargetFlags
	cmd := &cobra.Command{Use: "selection", Short: "Show the skills a new session will get, before it starts", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		targets := target.targets()
		if len(targets) == 0 {
			return exitcode.New(exitcode.Usage, fmt.Errorf("name the targets of the session: --workspace, --folder, --template, --agent, --group or --session"))
		}
		q := url.Values{}
		for _, t := range targets {
			q.Add("target", t.Kind+":"+t.ID)
		}
		raw, _, err := skillsJSONCall(cmd, client, "GET", "/selection?"+q.Encode(), nil, nil)
		if err != nil {
			return err
		}
		var selection skills.Selection
		if err := json.Unmarshal(raw, &selection); err != nil {
			return exitcode.New(exitcode.Server, fmt.Errorf("skills selection is invalid"))
		}
		names := map[string]string{}
		if packs, err := skillsReadAll[skills.Pack](cmd, client, "/packs", nil); err != nil {
			fmt.Fprintln(cmd.ErrOrStderr(), "pack names could not be read; showing pack identifiers:", err)
		} else {
			for _, p := range packs {
				names[p.ID] = termSafe(p.Name)
			}
		}
		return renderOut(cmd, func(out io.Writer) error {
			if len(selection.Members) == 0 {
				_, err := fmt.Fprintln(out, "no skills: nothing is pinned to these targets")
				return err
			}
			fmt.Fprintf(out, "%d skills a new session there gets:\n", len(selection.Members))
			for _, m := range selection.Members {
				pack := names[m.PackID]
				if pack == "" {
					pack = m.PackID
				}
				var from []string
				for _, o := range m.Origins {
					from = append(from, o.Kind+":"+o.Target.ID)
				}
				fmt.Fprintf(out, "  %-28s %-24s from %s\n", termSafe(m.Name), termSafe(pack), strings.Join(from, ", "))
				for _, o := range m.Overrides {
					replaced := names[o.PackID]
					if replaced == "" {
						replaced = o.PackID
					}
					fmt.Fprintf(out, "    replaces the skill of %s pinned on %s:%s\n", termSafe(replaced), o.Kind, o.Target.ID)
				}
			}
			return nil
		}, json.RawMessage(raw))
	}}
	cmd.Long = "Resolve what a new session would receive for the named targets, with the launch's own rules: every target's pins together, a narrower target replacing a broader one's skill of the same name. Name the whole tree of the session, for example its workspace and its folder."
	cmd.Example = "  olivares skills selection --workspace <workspace-id> --folder <folder-ref>"
	target.add(cmd)
	return cmd
}

// skillsCandidate is an enabled pack with every revision and its latest one.
type skillsCandidate struct {
	pack skills.Pack
	all  []skills.Revision
	rev  skills.Revision
}

func newSkillsEnableCmd(client datalaneClient, enable bool) *cobra.Command {
	var target skillsTargetFlags
	var packID string
	var approve bool
	use, short := "enable <skill>...", "Enable skills on a workspace, folder, template, group, agent or session"
	if !enable {
		use, short = "disable <skill>...", "Disable skills pinned to a workspace, folder, template, group, agent or session"
	}
	cmd := &cobra.Command{Use: use, Short: short, Args: cobra.MinimumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		t, err := target.target()
		if err != nil {
			return err
		}
		pins, err := skillsReadAll[skills.Assignment](cmd, client, "/assignments", url.Values{"target_kind": []string{t.Kind}, "target_id": []string{t.ID}})
		if err != nil {
			return err
		}
		if !enable {
			return skillsDisable(cmd, client, t, args, packID, pins)
		}
		return skillsEnable(cmd, client, t, args, packID, approve, pins)
	}}
	if enable {
		cmd.Long = "Enable installed skills on one target by name. The pack that holds each skill is found for you (use --pack when two packs hold the same name); its latest revision is pinned, or the revision already pinned there is extended. Skills that carry hooks, shell interpolation or tool pre-approvals need --approve-active-content."
		cmd.Example = "  olivares skills enable tdd-workflow code-review --folder <folder-ref>"
		cmd.Flags().StringVar(&packID, "pack", "", "catalog pack identifier to take the skills from")
		cmd.Flags().BoolVar(&approve, "approve-active-content", false, "approve skills that carry hooks, shell interpolation or tool pre-approvals")
	} else {
		cmd.Long = "Remove skills from the pins of one target. A pin left with no skills is removed. A skill inherited from a broader target stays there: disable it on that target, or pin your own skill of the same name on this one. With --pack only that pack's pin changes."
		cmd.Example = "  olivares skills disable deploy --folder <folder-ref>"
		cmd.Flags().StringVar(&packID, "pack", "", "catalog pack identifier: change only this pack's pin")
	}
	target.add(cmd)
	return cmd
}

// skillsEnable resolves every skill and checks every approval before the first
// write, then writes pack by pack in a fixed order and says what was written if a
// later write fails.
func skillsEnable(cmd *cobra.Command, client datalaneClient, t skills.Target, names []string, packID string, approve bool, pins []skills.Assignment) error {
	var packs []skills.Pack
	if packID != "" {
		pack, _, err := skillsPackHistory(cmd, client, packID)
		if err != nil {
			return err
		}
		packs = []skills.Pack{pack}
	} else {
		all, err := skillsReadAll[skills.Pack](cmd, client, "/packs", nil)
		if err != nil {
			return err
		}
		for _, p := range all {
			if p.State == "enabled" {
				packs = append(packs, p)
			}
		}
	}
	var candidates []skillsCandidate
	for _, p := range packs {
		pack, revisions, err := skillsPackHistory(cmd, client, p.ID)
		if err != nil {
			return err
		}
		if latest, ok := revisionByID(revisions, pack.LatestRevisionID); ok {
			candidates = append(candidates, skillsCandidate{pack: pack, all: revisions, rev: latest})
		}
	}
	wanted := map[string][]string{} // pack id -> skill names
	byPack := map[string]skillsCandidate{}
	for _, name := range names {
		var holders []skillsCandidate
		for _, c := range candidates {
			if _, ok := hasMember(c.rev, name); ok {
				holders = append(holders, c)
			}
		}
		switch len(holders) {
		case 0:
			return exitcode.New(exitcode.NotFound, fmt.Errorf("no installed pack holds the skill %q: browse the catalog with `olivares skills ls --builtin` and install a pack", termSafe(name)))
		case 1:
			wanted[holders[0].pack.ID] = append(wanted[holders[0].pack.ID], name)
			byPack[holders[0].pack.ID] = holders[0]
		default:
			var packNames []string
			for _, h := range holders {
				packNames = append(packNames, termSafe(h.pack.Name)+" ("+h.pack.ID+")")
			}
			return exitcode.New(exitcode.Usage, fmt.Errorf("the skill %q is in %d packs: %s; choose one with --pack", termSafe(name), len(holders), strings.Join(packNames, ", ")))
		}
	}
	type write struct {
		pack     skillsCandidate
		skills   []string
		existing *skills.Assignment
		request  skills.AssignmentRequest
	}
	var plan []write
	for _, id := range slices.Sorted(maps.Keys(wanted)) {
		c, skillNames := byPack[id], wanted[id]
		revision, members := c.rev, slices.Clone(skillNames)
		var existing *skills.Assignment
		for i := range pins {
			if pins[i].PackID == id {
				existing = &pins[i]
			}
		}
		added := skillNames
		if existing != nil {
			pinned, ok := revisionByID(c.all, existing.RevisionID)
			if !ok {
				return exitcode.New(exitcode.Server, fmt.Errorf("the revision pinned here is not in the pack's history"))
			}
			for _, name := range skillNames {
				if _, ok := hasMember(pinned, name); !ok {
					return exitcode.New(exitcode.Usage, fmt.Errorf("the skill %q is not in revision %d pinned on this target; move the pin first with `olivares skills pin %s`", termSafe(name), pinned.Number, id))
				}
			}
			revision = pinned
			members = append(members, existing.Members...)
			added = slices.DeleteFunc(slices.Clone(skillNames), func(n string) bool { return slices.Contains(existing.Members, n) })
		}
		slices.Sort(members)
		members = slices.Compact(members)
		if !approve {
			for _, name := range added {
				if m, ok := hasMember(revision, name); ok && len(m.Active) > 0 {
					return exitcode.New(exitcode.Usage, fmt.Errorf("the skill %q carries %s, which its tool acts on without asking; review it with `olivares skills diff %s` and re-run with --approve-active-content", termSafe(name), termSafe(strings.Join(m.Active, ", ")), id))
				}
			}
		}
		plan = append(plan, write{pack: c, skills: skillNames, existing: existing, request: skills.AssignmentRequest{Target: t, RevisionID: revision.ID, Members: members, ApproveActiveContent: approve}})
	}
	var done []string
	for _, w := range plan {
		method, path, headers := "POST", "/assignments", http.Header(nil)
		if w.existing != nil {
			var err error
			if path, err = datalanePath("assignments", w.existing.ID); err != nil {
				return err
			}
			method, headers = "PUT", http.Header{"If-Match": []string{strconv.FormatInt(w.existing.Version, 10)}}
		}
		if _, _, err := skillsJSONCall(cmd, client, method, path, w.request, headers); err != nil {
			if len(done) > 0 {
				fmt.Fprintf(cmd.ErrOrStderr(), "already enabled before this failure: %s\n", strings.Join(done, "; "))
			}
			return err
		}
		line := fmt.Sprintf("%s on %s:%s (pack %s)", strings.Join(w.skills, ", "), t.Kind, t.ID, termSafe(w.pack.pack.Name))
		done = append(done, line)
		fmt.Fprintf(cmd.OutOrStdout(), "enabled %s\n", termSafe(line))
	}
	return nil
}

// skillsDisable finds every named skill before it changes any pin, so a typo
// writes nothing, and says what was changed if a later write fails.
func skillsDisable(cmd *cobra.Command, client datalaneClient, t skills.Target, names []string, packID string, pins []skills.Assignment) error {
	if packID != "" {
		pins = slices.DeleteFunc(slices.Clone(pins), func(p skills.Assignment) bool { return p.PackID != packID })
	}
	found := map[string]bool{}
	for _, pin := range pins {
		for _, m := range pin.Members {
			if slices.Contains(names, m) {
				found[m] = true
			}
		}
	}
	for _, n := range names {
		if !found[n] {
			return exitcode.New(exitcode.NotFound, fmt.Errorf("the skill %q is not pinned to %s:%s; if it is inherited, disable it on the broader target", termSafe(n), t.Kind, t.ID))
		}
	}
	var done []string
	for _, pin := range pins {
		var keep, dropped []string
		for _, m := range pin.Members {
			if slices.Contains(names, m) {
				dropped = append(dropped, m)
				continue
			}
			keep = append(keep, m)
		}
		if len(dropped) == 0 {
			continue
		}
		path, err := datalanePath("assignments", pin.ID)
		if err != nil {
			return err
		}
		headers := http.Header{"If-Match": []string{strconv.FormatInt(pin.Version, 10)}}
		method, body := "DELETE", any(nil)
		if len(keep) > 0 {
			method, body = "PUT", skills.AssignmentRequest{Target: pin.Target, RevisionID: pin.RevisionID, Members: keep}
		}
		if _, _, err := skillsJSONCall(cmd, client, method, path, body, headers); err != nil {
			if len(done) > 0 {
				fmt.Fprintf(cmd.ErrOrStderr(), "already disabled before this failure: %s\n", strings.Join(done, ", "))
			}
			return err
		}
		done = append(done, dropped...)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "disabled %s on %s:%s\n", termSafe(strings.Join(done, ", ")), t.Kind, t.ID)
	return nil
}
