// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/cmd/olivares/internal/termrender"
)

// sessionMessages is GET /runs/{ref}/messages (#2129). The JSON form prints the
// engine's answer as it came; the text form prints what the console's message
// navigator shows.
type sessionMessages struct {
	Transcript string `json:"transcript"`
	Total      int    `json:"total"`
	Items      []struct {
		ID        int    `json:"id"`
		Role      string `json:"role"`
		At        string `json:"at"`
		FirstLine string `json:"first_line"`
		Text      string `json:"text"`
	} `json:"items"`
	Next       string `json:"next"`
	Unreadable int    `json:"unreadable"`
}

// messageTimeLayouts are the forms a person types a time in, read in their own zone;
// RFC 3339 carries its own.
var messageTimeLayouts = []string{"2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02"}

// parseMessageTime reads --at: a date, a date and time, or an RFC 3339 time.
func parseMessageTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	for _, layout := range messageTimeLayouts {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, sentence(exitcode.Usage, "Cannot read %q as a time. Use 2026-10-14, \"2026-10-14 10:30\" or 2026-10-14T10:30:00Z.", s)
}

func newSessionMessagesCmd() *cobra.Command {
	var cfg agentClientConfig
	var search, at string
	var all, newest bool
	var limit, cursor, id int
	cmd := &cobra.Command{
		Use:     "messages <session>",
		Aliases: []string{"msgs"},
		Short:   "List, search and find messages of a session",
		Long: "messages lists the messages you sent to a session, with their id, time and first line.\n" +
			"Use --search to find a phrase anywhere in a message and --at to start at a date or time:\n" +
			"the first message at or after it. --all adds the tool's answers. A long session is read in\n" +
			"pages: --newest starts from the latest message and --cursor continues from the id the\n" +
			"last page ended on. --id prints one message whole. It is read from the tool's own\n" +
			"transcript (Claude Code, Codex), the same read the console's message navigator uses.",
		Example: "  olivares session messages my-app\n" +
			"  olivares session messages my-app --search migration --all\n" +
			"  olivares session messages my-app --at \"2026-10-14 10:30\" --limit 5\n" +
			"  olivares session messages my-app --id 12\n" +
			"  olivares session messages my-app --newest -o json",
		Annotations:       map[string]string{"changes_data": "read"},
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeSessions,
		RunE: func(cmd *cobra.Command, args []string) error {
			q := url.Values{}
			if search != "" {
				q.Set("q", search)
			}
			if at != "" {
				when, err := parseMessageTime(at)
				if err != nil {
					return err
				}
				q.Set("at", when.UTC().Format(time.RFC3339))
			}
			if all {
				q.Set("role", "all")
			}
			if newest {
				q.Set("order", "desc")
			}
			for _, n := range []struct {
				name string
				v    int
			}{{"limit", limit}, {"cursor", cursor}, {"id", id}} {
				if !cmd.Flags().Changed(n.name) {
					continue
				}
				if n.v < 0 || (n.name != "cursor" && n.v == 0) {
					return sentence(exitcode.Usage, "--%s needs a whole number above zero.", n.name)
				}
				q.Set(n.name, strconv.Itoa(n.v))
			}
			if id > 0 && (search != "" || at != "" || all || newest || cmd.Flags().Changed("cursor") || cmd.Flags().Changed("limit")) {
				return sentence(exitcode.Usage, "--id prints one message whole; it takes no other flag.")
			}
			if err := cfg.resolve(); err != nil {
				return err
			}
			run, err := cfg.findSession(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			path := sessionRunsPath + "/" + url.PathEscape(str(run, "run_ref")) + "/messages"
			if len(q) > 0 {
				path += "?" + q.Encode()
			}
			_, raw, err := cfg.do(cmd.Context(), "GET", path, nil)
			if err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				return printSessionMessages(w, raw, id > 0, newest)
			}, json.RawMessage(raw))
		},
	}
	cfg.addFlags(cmd)
	cmd.Flags().StringVar(&search, "search", "", "only messages with this phrase anywhere in them (case-insensitive)")
	cmd.Flags().StringVar(&at, "at", "", "start at this date or time: 2026-10-14, \"2026-10-14 10:30\" or 2026-10-14T10:30:00Z")
	cmd.Flags().BoolVar(&all, "all", false, "include the tool's answers, not only your messages")
	cmd.Flags().BoolVar(&newest, "newest", false, "start from the latest message (a page is always printed oldest first)")
	cmd.Flags().IntVar(&limit, "limit", 0, "messages per page (default 50, at most 200)")
	cmd.Flags().IntVar(&cursor, "cursor", 0, "continue from this message id (the page before it with --newest)")
	cmd.Flags().IntVar(&id, "id", 0, "print this message whole")
	return cmd
}

// printSessionMessages prints a page as the navigator lists it, or one message whole.
func printSessionMessages(w io.Writer, raw json.RawMessage, whole, newest bool) error {
	var m sessionMessages
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	r := renderTo(w)
	switch m.Transcript {
	case "pending":
		r.Line("The tool has not written its transcript yet.")
		return nil
	case "missing":
		r.Line("This session's transcript is not in its account home, so its messages cannot be shown.")
		return nil
	}
	if whole && len(m.Items) == 1 {
		item := m.Items[0]
		r.Fields([]termrender.Field{
			{Key: "id", Value: strconv.Itoa(item.ID)},
			{Key: "from", Value: item.Role},
			{Key: "time", Value: messageClock(item.At)},
		})
		r.Blank()
		r.Line(termSafe(item.Text))
		if m.Unreadable > 0 {
			r.Line(r.Paint(fmt.Sprintf("%d records of the transcript could not be read.", m.Unreadable), termrender.RoleWarn))
		}
		return nil
	}
	table := termrender.Table{Header: []string{"ID", "TIME", "FROM", "MESSAGE"}, Empty: "No messages match."}
	for _, item := range m.Items {
		table.Rows = append(table.Rows, []string{strconv.Itoa(item.ID), messageClock(item.At), item.Role, termSafe(item.FirstLine)})
	}
	r.Table(table)
	if m.Unreadable > 0 {
		r.Line(r.Paint(fmt.Sprintf("%d records of the transcript could not be read.", m.Unreadable), termrender.RoleWarn))
	}
	if len(m.Items) > 0 {
		shown := fmt.Sprintf("%d of %d messages.", len(m.Items), m.Total)
		if m.Next != "" {
			shown += " More: "
			if newest {
				shown += "--newest "
			}
			shown += "--cursor " + m.Next
		}
		r.Line(shown)
	}
	return nil
}

// messageClock is a message's time in the person's own zone; a time that does not
// parse is shown as the transcript has it.
func messageClock(at string) string {
	t, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return termSafe(at)
	}
	return t.Local().Format("2006-01-02 15:04:05")
}
