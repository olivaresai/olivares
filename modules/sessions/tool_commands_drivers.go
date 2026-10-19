// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// ACP pushes its catalog; reading it sends nothing to the child. Before the
// correlated session/new response binds a conversation, retain only the latest
// candidate. It is served only if its session id matches the confirmed one.
type acpCommandCatalog struct {
	sessionID string
	items     []toolCommand
	err       error
	reported  bool
}

func (s *acpSession) observeCommands(n acpSessionNotification) {
	var update struct {
		Kind     string             `json:"sessionUpdate"`
		Commands *[]json.RawMessage `json:"availableCommands"`
	}
	var kind struct {
		Kind string `json:"sessionUpdate"`
	}
	if json.Unmarshal(n.Update, &kind) != nil || kind.Kind != "available_commands_update" {
		return
	}
	if n.SessionID == "" || (s.sessionID != "" && n.SessionID != s.sessionID && n.SessionID != s.replayFor) {
		return
	}
	items := []toolCommand{}
	var err error
	if json.Unmarshal(n.Update, &update) != nil || update.Commands == nil {
		err = errNotACatalog("")
	} else {
		for _, raw := range *update.Commands {
			var c struct {
				Name        string `json:"name"`
				Description string `json:"description"`
				Input       *struct {
					Hint string `json:"hint"`
				} `json:"input"`
			}
			if json.Unmarshal(raw, &c) != nil || !validACPCommandName(c.Name) {
				continue
			}
			item := toolCommand{Name: c.Name, Description: clipToolText(c.Description), Kind: "command"}
			if c.Input != nil {
				item.ArgumentHint = clipToolText(c.Input.Hint)
			}
			items = append(items, item)
			if len(items) == maxToolCommands {
				break
			}
		}
		if len(*update.Commands) > 0 && len(items) == 0 {
			err = &runErr{http.StatusBadGateway, "none of the entries the tool reported is a command"}
		}
	}
	s.commands = acpCommandCatalog{sessionID: n.SessionID, items: items, err: err, reported: err == nil}
}

// Gemini advertises subcommands such as "memory show". Keep their native
// spelling, allowing ASCII word separators but no newlines or invisible text.
func validACPCommandName(name string) bool {
	if name == "" || len(name) > maxToolCommandName || strings.Join(strings.Fields(name), " ") != name {
		return false
	}
	for _, word := range strings.Split(name, " ") {
		if !validToolCommandName(word) {
			return false
		}
	}
	return true
}

func (s *acpSession) commandCatalog(context.Context) ([]toolCommand, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.sessionID == "" || s.commands.sessionID != s.sessionID {
		return []toolCommand{}, false, nil
	}
	return append([]toolCommand{}, s.commands.items...), s.commands.reported, s.commands.err
}

// Codex's stable skills/list scans the live app-server's own configuration and
// this session's cwd. It reports skills, not the TUI's built-ins or agent list.
func (s *codexSession) commandCatalog(ctx context.Context) ([]toolCommand, bool, error) {
	params := struct {
		Cwds        []string `json:"cwds"`
		ForceReload bool     `json:"forceReload"`
	}{Cwds: []string{}, ForceReload: true}
	if s.cfg.WorkDir != "" {
		params.Cwds = []string{s.cfg.WorkDir}
	}
	// Bound the response wait from the start of this read. The sender owns a
	// separate write deadline independent of caller cancellation and deadlines;
	// RPC still lets the caller leave, and late catalog answers remain withheld.
	requestCtx, cancel := context.WithTimeout(ctx, toolCommandsWait)
	defer cancel()
	raw, err := s.conn.call(requestCtx, "skills/list", params, toolCommandsWait)
	if err != nil {
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		if errors.Is(err, errRPCClosed) {
			return nil, false, errToolEnded
		}
		var timeout *runErr
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.status == http.StatusGatewayTimeout) {
			return nil, false, &runErr{http.StatusGatewayTimeout, "Codex did not report its skills in time"}
		}
		return nil, false, &runErr{http.StatusBadGateway, codexSkillsFailed}
	}
	items, err := parseCodexCommands(raw, s.cfg.WorkDir)
	return items, err == nil, err
}

func parseCodexCommands(raw []byte, cwd string) ([]toolCommand, error) {
	var reply struct {
		Data *[]struct {
			Cwd    string             `json:"cwd"`
			Skills *[]json.RawMessage `json:"skills"`
			Errors []json.RawMessage  `json:"errors"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &reply) != nil || reply.Data == nil || len(*reply.Data) != 1 {
		return nil, errNotACatalog("")
	}
	entry := (*reply.Data)[0]
	if (cwd != "" && entry.Cwd != cwd) || entry.Skills == nil || len(entry.Errors) > 0 {
		return nil, &runErr{http.StatusBadGateway, "Codex could not read the session's skills"}
	}
	items := []toolCommand{}
	invalid := 0
	for _, raw := range *entry.Skills {
		var skill struct {
			Name             string  `json:"name"`
			Description      string  `json:"description"`
			ShortDescription string  `json:"shortDescription"`
			Enabled          *bool   `json:"enabled"`
			Scope            string  `json:"scope"`
			PluginID         *string `json:"pluginId"`
			Interface        *struct {
				ShortDescription string `json:"shortDescription"`
			} `json:"interface"`
		}
		if json.Unmarshal(raw, &skill) != nil || skill.Enabled == nil || !validToolCommandName(skill.Name) {
			invalid++
			continue
		}
		if !*skill.Enabled {
			continue
		}
		description := skill.Description
		if skill.ShortDescription != "" {
			description = skill.ShortDescription
		}
		if skill.Interface != nil && skill.Interface.ShortDescription != "" {
			description = skill.Interface.ShortDescription
		}
		source := ""
		switch skill.Scope {
		case "user":
			source = "user"
		case "repo":
			source = "project"
		}
		if skill.PluginID != nil && *skill.PluginID != "" {
			source = "plugin"
		}
		items = append(items, toolCommand{Name: skill.Name, Description: clipToolText(description), Source: source, Kind: "command", InsertText: "$" + skill.Name})
		if len(items) == maxToolCommands {
			break
		}
	}
	if invalid > 0 && len(items) == 0 {
		return nil, errNotACatalog("")
	}
	return items, nil
}

// codexSkillsFailed is the only text a failed skills/list read gives its caller:
// nothing the tool wrote comes with it.
const codexSkillsFailed = "Codex did not report its skills"

// oversizedCatalogResponse recovers an answer to one of this session's own
// skills/list requests that is over the pump's line limit (the size of the
// answer is the person's skill set). Codex writes the id first, so the retained
// prefix names the request; only an issued id is recovered, and the replacement
// takes the ordinary path through takeCatalogResponse. Nothing from the tool's
// oversized payload is published or kept. Any other over-long line ends the
// session as before, including a server request that happens to share an id.
func (s *codexSession) oversizedCatalogResponse(prefix []byte) []byte {
	ids := toolResponseFields(prefix, "id")
	if len(ids) != 1 || len(toolResponseFields(prefix, "method")) != 0 {
		return nil
	}
	s.mu.Lock()
	_, issued := s.catalogRequests[string(ids[0])]
	s.mu.Unlock()
	if !issued {
		return nil
	}
	reply, err := json.Marshal(errorOut{ID: ids[0], Error: rpcError{Code: -32603, Message: codexSkillsFailed}})
	if err != nil {
		return nil
	}
	return reply
}

// Consume only responses to catalog requests we issued, including late answers.
// Correlate before validating the envelope: an EOF prefix or malformed method
// can still carry discovery paths. Ambiguous or malformed replies fail every
// matching waiter with a sanitized error and never reach attach or recording.
func (s *codexSession) takeCatalogResponse(data []byte) bool {
	s.mu.Lock()
	outstanding := len(s.catalogRequests)
	s.mu.Unlock()
	if outstanding == 0 {
		return false // no catalog request in flight: nothing issued can be answered
	}
	ids := toolResponseFields(data, "id")
	if len(ids) == 0 {
		return false
	}
	var frame rpcFrame
	decodeErr := json.Unmarshal(data, &frame)
	// Server requests have their own id namespace. A valid request must still
	// reach the driver even if its id equals one of our catalog request ids.
	if decodeErr == nil && len(ids) == 1 && frame.Method != "" && len(frame.Result) == 0 && frame.Error == nil {
		return false
	}
	consumed := false
	for _, id := range ids {
		s.mu.Lock()
		_, issued := s.catalogRequests[string(id)]
		delete(s.catalogRequests, string(id))
		s.mu.Unlock()
		if !issued {
			continue
		}
		consumed = true
		reply := data
		if decodeErr != nil || frame.Method != "" || len(ids) != 1 {
			reply, _ = json.Marshal(errorOut{ID: id, Error: rpcError{Code: -32603, Message: codexSkillsFailed}})
		}
		s.conn.deliver(reply)
	}
	return consumed
}
