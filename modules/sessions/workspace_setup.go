// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const maxSetupCommandBytes = 8192

type workspaceSetupApproval struct {
	SHA256 string `json:"sha256"`
	Actor  string `json:"actor"`
	At     string `json:"at"`
}

func storedSetupApproval(rec model.Record) *workspaceSetupApproval {
	if rec.String(colWsSetupHash) == "" {
		return nil
	}
	return &workspaceSetupApproval{SHA256: rec.String(colWsSetupHash), Actor: rec.String(colWsSetupActor), At: rec.String(colWsSetupAt)}
}

// handleSetWorkspaceSetup stores the exact setup command text of one folder, or clears it when the text is empty. The text is stored and never run by this operation, and an approval is bound to the SHA-256 of the exact text, so changing the text leaves the earlier approval unable to authorize it.
func (m *Module) handleSetWorkspaceSetup(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if !m.workspaceAllowed(w, r, mc, permWsAdmin, auth.TrusteeModify) {
		return
	}
	var body struct {
		Command json.RawMessage `json:"command"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	command, err := decodeSetupCommand(body.Command)
	if err != nil {
		writeRunErr(w, err)
		return
	}
	dto, err := m.configureWorkspaceSetup(r.Context(), mc.Tenant, chi.URLParam(r, "ref"), &command, "", mc.Principal.Actor(), mc.Principal.ActorKind())
	if err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

// handleApproveWorkspaceSetup records a person's approval of the setup command text whose SHA-256 is posted, and refuses with 409 when the stored text no longer has that hash. An agent identity cannot approve, and the approval commits together with its audit entry.
func (m *Module) handleApproveWorkspaceSetup(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if !m.workspaceAllowed(w, r, mc, permWsAdmin, auth.TrusteeModify) {
		return
	}
	var body struct {
		SHA256 string `json:"sha256"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	if decoded, err := hex.DecodeString(body.SHA256); err != nil || len(decoded) != 32 || body.SHA256 != strings.ToLower(body.SHA256) {
		writeRunErr(w, badRequest("sha256 must name the exact command text reviewed (64 lowercase hexadecimal characters)"))
		return
	}
	// An agent cannot approve its own future command. Operator API tokens keep
	// their authenticated actor in the approval and the audit, as other writes do.
	if mc.Principal.AgentIdentity != "" {
		writeRunErr(w, forbiddenErr("a person must approve the setup command"))
		return
	}
	dto, err := m.configureWorkspaceSetup(r.Context(), mc.Tenant, chi.URLParam(r, "ref"), nil, body.SHA256, mc.Principal.Actor(), mc.Principal.ActorKind())
	if err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

// Keep the last approval as evidence. Editing the command changes its hash, so
// the old approval cannot authorize the new text. Compare-and-save and audit
// share the store transaction; a stale review or failed audit approves nothing.
func (m *Module) configureWorkspaceSetup(ctx context.Context, tenant model.TenantID, ref string, command *string, approveHash, actor, actorKind string) (workspaceDTO, error) {
	if m.Data == nil {
		return workspaceDTO{}, &runErr{http.StatusServiceUnavailable, "workspace registry not available"}
	}
	var out workspaceDTO
	err := m.Data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workspaceKind)
		if err != nil {
			return err
		}
		rec, err := lockWorkspaceRec(ctx, repo, ref)
		if err != nil {
			return err
		}
		op := "configure-setup"
		if command != nil {
			rec[colWsSetupCommand] = *command
		} else {
			if rec.String(colWsSetupCommand) == "" || sha256Hex([]byte(rec.String(colWsSetupCommand))) != approveHash {
				return conflictErr("the setup command changed or is empty; read its text and approve its current SHA-256")
			}
			rec[colWsSetupHash] = approveHash
			rec[colWsSetupActor] = actor
			rec[colWsSetupAt] = model.NewTimestamp(m.now()).String()
			op = "approve-setup"
		}
		updated, err := repo.Update(ctx, rec)
		if err != nil {
			return err
		}
		if err := appendWorkspaceAudit(ctx, sc, wsMutationInput{workspaceID: model.ID(rec.String(model.ColID)), workspaceRef: ref, op: op, path: rec.String(colWsRootPath), contentHash: sha256Hex([]byte(rec.String(colWsSetupCommand))), actor: actor, actorKind: actorKind}); err != nil {
			return err
		}
		out, err = toWorkspaceDTO(updated)
		return err
	})
	return out, err
}

// covers reports whether this approval names a person and a time and was given
// to exactly this command text. The start gate and the API's setup_approved
// share it, so a client never re-derives the rule.
func (a *workspaceSetupApproval) covers(command string) bool {
	return a != nil && a.Actor != "" && a.At != "" && a.SHA256 == sha256Hex([]byte(command))
}

// This part only checks consent. Command execution belongs to the worktree
// preparation step and is deliberately absent here.
func workspaceSetupStartError(ws *resolvedWorkspace, skip bool) error {
	if ws == nil || skip || ws.setupCommand == "" {
		return nil
	}
	if !ws.setupApproval.covers(ws.setupCommand) {
		return conflictErr(fmt.Sprintf("Setup command %q needs approval; run olivares agent workspace approve-setup %s --sha256 %s, or start once with --no-setup.", ws.setupCommand, ws.ref, sha256Hex([]byte(ws.setupCommand))))
	}
	return nil
}

// encoding/json repairs malformed Unicode. Refuse it before the exact command
// text is decoded, while accepting valid UTF-16 pairs and literal U+FFFD.
func decodeSetupCommand(raw json.RawMessage) (string, error) {
	invalid := badRequest("command is required and must be UTF-8 text of at most 8192 bytes without NUL; an empty string clears it")
	if len(raw) == 0 || raw[0] != '"' || !utf8.Valid(raw) {
		return "", invalid
	}
	var command string
	if err := json.Unmarshal(raw, &command); err != nil || len(command) > maxSetupCommandBytes || strings.ContainsRune(command, 0) {
		return "", invalid
	}
	for i := 1; i < len(raw)-1; i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if raw[i] != 'u' {
			continue
		}
		value, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		if err != nil {
			return "", invalid
		} // JSON validation above guarantees four hexadecimal digits.
		i += 4
		switch {
		case value >= 0xdc00 && value <= 0xdfff:
			return "", invalid
		case value >= 0xd800 && value <= 0xdbff:
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return "", invalid
			}
			low, err := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return "", invalid
			}
			i += 6
		}
	}
	return command, nil
}
