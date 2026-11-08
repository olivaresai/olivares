// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/oas"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type conversationCandidate struct {
	ProfileRef string    `json:"provider_profile_ref"`
	ID         string    `json:"conversation_id"`
	Folder     string    `json:"folder"`
	Title      string    `json:"title"`
	Modified   time.Time `json:"modified"`
}

// Only product-created account homes are sources. A registered path or an adopted
// profile never grants discovery of the server user's conversations.
func (m *Module) importAccount(ctx context.Context, tenant model.TenantID, ref string) (ProviderAccount, error) {
	a, err := m.GetProviderAccount(ctx, tenant, ref)
	if err != nil {
		return a, err
	}
	if a.HomeMode != AccountHomeManaged || a.Driver != providerDriverClaude || a.State != ProfileActive || a.EnvironmentRef != m.ExecutionEnvironmentRef() {
		return a, badRequest("import needs an active Claude Code account managed on this server")
	}
	rel := accountHomeRelative(a)
	if rel == "" || m.accountsRoot == "" || !within(filepath.Join(m.accountsRoot, rel), a.ConfigHome) {
		return a, ErrAccountsRootUnavailable
	}
	if err := revalidateHome("config_home", a.ConfigHome); err != nil {
		return a, err
	}
	return a, nil
}

func readConversationCandidate(ctx context.Context, home *os.Root, ref, id string) (conversationCandidate, error) {
	tr, err := openClaudeTranscriptIn(home, id)
	if err != nil {
		return conversationCandidate{}, err
	}
	// The caller owns home; only the transcript descriptor belongs to this read.
	defer tr.f.Close()
	c := conversationCandidate{ProfileRef: ref, ID: id, Modified: tr.info.ModTime()}
	reader := bufio.NewReader(io.LimitReader(tr.f, importRolloutHead))
	for {
		if err := ctx.Err(); err != nil {
			return c, err
		}
		line, err := readBoundedLine(reader, importRolloutHead)
		if len(line) > 0 {
			var record struct {
				Cwd       string `json:"cwd"`
				SessionID string `json:"sessionId"`
				claudeLine
			}
			if json.Unmarshal(line, &record) == nil && !record.IsSidechain && (record.SessionID == id || record.SessionID == "") {
				if c.Folder == "" && filepath.IsAbs(record.Cwd) {
					c.Folder = filepath.Clean(record.Cwd)
				}
				if c.Title == "" {
					if role, text, _ := claudeMessage(record.claudeLine); role == messageRoleUser {
						c.Title = clipBytes(strings.SplitN(text, "\n", 2)[0], messageFirstLineMax)
					}
				}
			}
		}
		if c.Folder != "" && c.Title != "" {
			return c, nil
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return c, err
		}
	}
	if c.Title == "" {
		c.Title = id
	}
	return c, nil
}

// handleConversationCandidates reads existing native conversations without starting
// tools or writing their stores. Profile visibility is checked before opening a home.
func (m *Module) handleConversationCandidates(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if !m.mayImportConversations(r.Context(), mc) {
		writeRunErr(w, forbiddenErr("reading tool accounts is required to list conversations"))
		return
	}
	accounts, _, err := listVisible(r.Context(), m, mc, model.Query{Limit: importMaxRollouts},
		func(q model.Query) ([]ProviderAccount, model.Page, error) {
			return m.ListProviderAccounts(r.Context(), mc.Tenant, ProviderAccountFilter{Driver: providerDriverClaude, State: ProfileActive}, q)
		},
		func(a ProviderAccount) (string, string) { return a.Ref, a.Driver })
	if err != nil {
		writeRunErr(w, err)
		return
	}
	out := []conversationCandidate{}
	for _, a := range accounts {
		if a.HomeMode != AccountHomeManaged || a.EnvironmentRef != m.ExecutionEnvironmentRef() {
			continue
		}
		if _, err := m.importAccount(r.Context(), mc.Tenant, a.Ref); err != nil {
			writeRunErr(w, err)
			return
		}
		bound, err := m.boundConversationIDs(r.Context(), mc.Tenant, a.Ref, "")
		if err != nil {
			writeRunErr(w, err)
			return
		}
		home, err := os.OpenRoot(a.ConfigHome)
		if err != nil {
			writeRunErr(w, err)
			return
		}
		projects, err := openRealDir(home, claudeProjectsDir)
		if errors.Is(err, fs.ErrNotExist) {
			home.Close()
			continue
		}
		if err != nil {
			home.Close()
			writeRunErr(w, err)
			return
		}
		dirs, err := fs.ReadDir(projects.FS(), ".")
		projects.Close()
		if err != nil {
			home.Close()
			writeRunErr(w, err)
			return
		}
		ids := map[string]bool{}
		for _, dir := range dirs {
			if !dir.IsDir() {
				continue
			}
			project, err := openRealDir(home, path.Join(claudeProjectsDir, dir.Name()))
			if err != nil {
				home.Close()
				writeRunErr(w, err)
				return
			}
			files, err := fs.ReadDir(project.FS(), ".")
			project.Close()
			if err != nil {
				home.Close()
				writeRunErr(w, err)
				return
			}
			for _, file := range files {
				id := strings.TrimSuffix(file.Name(), ".jsonl")
				if !file.Type().IsRegular() || id == file.Name() || !claudeSessionIDPattern.MatchString(id) || ids[id] || bound[id] {
					continue
				}
				ids[id] = true
				if len(out) >= importMaxRollouts {
					break
				}
				c, err := readConversationCandidate(r.Context(), home, a.Ref, id)
				if err != nil {
					home.Close()
					writeRunErr(w, err)
					return
				}
				out = append(out, c)
			}
		}
		home.Close()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (m *Module) validateConversationImport(ctx context.Context, tenant model.TenantID, p CreateRunParams) error {
	if p.ImportConversationID == "" {
		return nil
	}
	if !claudeSessionIDPattern.MatchString(p.ImportConversationID) || p.WorkspaceRef == "" || p.Worktree || p.RunOn != "" || p.RepositoryURL != "" {
		return badRequest("import needs a conversation id, its managed account and an allowed folder")
	}
	a, err := m.importAccount(ctx, tenant, p.ProviderProfileRef)
	if err != nil {
		return err
	}
	home, err := os.OpenRoot(a.ConfigHome)
	if err != nil {
		return err
	}
	defer home.Close()
	_, err = readConversationCandidate(ctx, home, a.Ref, p.ImportConversationID)
	if err != nil {
		return err
	}
	bound, err := m.boundConversationIDs(ctx, tenant, a.Ref, p.ImportConversationID)
	if err != nil {
		return err
	}
	if bound[p.ImportConversationID] {
		return conflictErr("this conversation is already a session; open it from Sessions")
	}
	return nil
}

// boundConversationIDs uses the same account-scoped run lookup for discovery and
// the import race guard. Discovery walks all pages so older runs stay excluded.
func (m *Module) boundConversationIDs(ctx context.Context, tenant model.TenantID, ref, id string) (map[string]bool, error) {
	bound := map[string]bool{}
	err := m.Data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		q := model.Query{Limit: importMaxRollouts, Filters: []model.Filter{eq(colRunProfileID, ref), eq(colRunProfileDriver, providerDriverClaude)}}
		if id != "" {
			q.Limit = 1
			q.Filters = append(q.Filters, eq(colClaudeSessionID, id))
		}
		for {
			rows, page, err := repo.List(ctx, q)
			if err != nil {
				return err
			}
			for _, row := range rows {
				if sid := row.String(colClaudeSessionID); sid != "" {
					bound[sid] = true
				}
			}
			if id != "" || !page.HasMore {
				return nil
			}
			q.Cursor = page.Cursor
		}
	})
	return bound, err
}

func (m *Module) mayImportConversations(ctx context.Context, mc api.ModuleContext) bool {
	if m.WorkAuthorizer == nil {
		return false
	}
	for _, permission := range []auth.Permission{permAccountRead, permRunOutput} {
		if !m.WorkAuthorizer.Authorize(ctx, auth.Request{Principal: mc.Principal, Tenant: mc.Tenant, Permission: permission, Resource: auth.ResourceFor(permission)}).Allow {
			return false
		}
	}
	return true
}

func sessionsConversationCandidatesResponses(method, pattern string) (map[string]any, bool) {
	if method != http.MethodGet || pattern != "/conversation-candidates" {
		return nil, false
	}
	text := oas.Obj("type", "string")
	item := sessionsClosureClosedObject(oas.Obj(
		"provider_profile_ref", text, "conversation_id", text, "folder", text, "title", text, "modified", sessionsTimestampSchema(),
	), "provider_profile_ref", "conversation_id", "folder", "title", "modified")
	responses := api.GenericModuleResponses()
	responses["200"] = oas.JSONRespSchema("Existing conversations in the caller's managed tool accounts", sessionsClosureClosedObject(oas.Obj("items", oas.Obj("type", "array", "items", item)), "items"))
	return responses, true
}
