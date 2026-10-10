// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// newSessionPeersCmd chooses which sessions a session may message or hand work to.
// The engine refuses a peer send to a session that is not on the sender's
// list, and only the console could set it. It is the engine's PUT /runs/{ref}/peers:
// the engine checks every ID again at send time.
func newSessionPeersCmd() *cobra.Command {
	var (
		cfg          agentClientConfig
		sameTemplate bool
		none         bool
	)
	cmd := &cobra.Command{
		Use:   "peers <session> [peer...]",
		Short: "Choose which sessions a session may message",
		Long: "peers sets the sessions this session's agent may message or hand work to with its\n" +
			"olivares_peer_send tool. Name each peer by its name, its id or its session ID (osn_...).\n" +
			"--same-template allows every running session started from the same template; --none\n" +
			"allows none. With no peer and no flag it prints the current choice. Tell the agent the\n" +
			"peer's session ID it prints: that is the ID the tool sends to.",
		Example: "  olivares session peers lead reviewer tester\n" +
			"  olivares session peers lead --same-template\n" +
			"  olivares session peers lead --none\n" +
			"  olivares session peers lead",
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: completeSessions,
		RunE: func(cmd *cobra.Command, args []string) error {
			peers := args[1:]
			if (sameTemplate && none) || ((sameTemplate || none) && len(peers) > 0) {
				return sentence(exitcode.Usage, "Choose peers, --same-template or --none, not more than one")
			}
			if err := cfg.resolve(); err != nil {
				return err
			}
			run, err := cfg.findSession(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			names := map[string]string{}
			if len(peers) > 0 || sameTemplate || none {
				body := map[string]any{"peers": []string{}}
				if sameTemplate {
					body = map[string]any{"peers_rule": "same-template"}
				}
				ids := []string{}
				for _, arg := range peers {
					sid, label, err := cfg.peerSessionID(cmd, arg)
					if err != nil {
						return err
					}
					ids, names[sid] = append(ids, sid), label
				}
				if len(peers) > 0 {
					body = map[string]any{"peers": ids}
				}
				_, b, err := cfg.do(cmd.Context(), "PUT", sessionRunsPath+"/"+url.PathEscape(str(run, "run_ref"))+"/peers", body, http.StatusOK)
				if err != nil {
					return err
				}
				run = map[string]any{}
				if err := json.Unmarshal(b, &run); err != nil {
					return err
				}
			}
			// A peer named by its ID, or read back, is shown by its session's name too.
			if runs, err := cfg.listRuns(cmd.Context()); err == nil {
				for _, r := range runs {
					if sid := str(r, "canonical_sid"); sid != "" && (names[sid] == "" || names[sid] == sid) {
						names[sid] = sessionLabel(r)
					}
				}
			}
			return renderOut(cmd, func(w io.Writer) error {
				return printSessionPeers(w, run, names)
			}, run)
		},
	}
	cmd.Flags().BoolVar(&sameTemplate, "same-template", false, "allow every running session started from the same template")
	cmd.Flags().BoolVar(&none, "none", false, "allow no session")
	cfg.addFlags(cmd)
	return cmd
}

// peerSessionID is a peer's canonical session ID: an osn_ ID as given, else the
// session with that name or id, which must have one.
func (c *agentClientConfig) peerSessionID(cmd *cobra.Command, arg string) (string, string, error) {
	if strings.HasPrefix(arg, "osn_") {
		return arg, arg, nil
	}
	peer, err := c.findSession(cmd.Context(), arg)
	if err != nil {
		return "", "", err
	}
	sid := str(peer, "canonical_sid")
	if sid == "" {
		return "", "", sentence(exitcode.Conflict, "%s has no session ID on this engine. See it: olivares session show %s",
			sessionLabel(peer), sessionLabel(peer))
	}
	return sid, sessionLabel(peer), nil
}

// printSessionPeers is the session's choice in a person's words, one line per peer
// with the session ID its agent sends to.
func printSessionPeers(w io.Writer, run map[string]any, names map[string]string) error {
	label := sessionLabel(run)
	if str(run, "peers_rule") == "same-template" {
		_, err := fmt.Fprintf(w, "%s may message every running session started from its template.\n", label)
		return err
	}
	peers, _ := run["peers"].([]any)
	if len(peers) == 0 {
		_, err := fmt.Fprintf(w, "%s may message no session.\n", label)
		return err
	}
	if _, err := fmt.Fprintf(w, "%s may message:\n", label); err != nil {
		return err
	}
	for _, p := range peers {
		sid := termSafe(fmt.Sprint(p))
		line := "  " + sid
		if name := names[sid]; name != "" && name != sid {
			line = "  " + name + "  " + sid
		}
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	return nil
}
