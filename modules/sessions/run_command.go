// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A caller may name an input with a command id, so a retry of the same send does not
// run the agent twice. The id is the caller's; it is stored only as a digest with the
// sender, so the dedupe is per tenant (the store), per run (the index) and per person:
// another person's id never matches this person's, and can neither take their receipt
// nor suppress their turn. The turn row is written with it before the first byte goes
// to the tool, under the run's input lock.

// errCommandDuplicate: this person already sent this input under this id; the turn
// ran once and the retry answers its receipt.
var errCommandDuplicate = errors.New("sessions: this command was already accepted")

// errCommandReused: the id was already used by this person for another input.
var errCommandReused = &runErr{http.StatusConflict, "command_id was already used for another input to this session"}

type runCommand struct {
	key    string // the stored command_id: who sent it and under which id
	digest string // the stored input_digest: that key and the input it was used for
}

type runCommandKey struct{}

// parseCommandID accepts the canonical form of a UUID and nothing else: 36 characters,
// lower case, not the nil UUID. An empty id is no command.
func parseCommandID(raw string) (string, bool) {
	if raw == "" {
		return "", true
	}
	id, err := model.ParseID(raw)
	if err != nil || id.IsZero() || len(raw) != 36 || id.String() != raw {
		return "", false
	}
	return raw, true
}

func newRunCommand(actor, commandID string, input []byte) runCommand {
	key := sha256.Sum256([]byte("olivares.sessions.command.v1\x00" + actor + "\x00" + commandID))
	c := runCommand{key: hex.EncodeToString(key[:])}
	digest := sha256.New()
	digest.Write([]byte(c.key + "\x00"))
	digest.Write(input)
	c.digest = hex.EncodeToString(digest.Sum(nil))
	return c
}

func withRunCommand(ctx context.Context, c runCommand) context.Context {
	return context.WithValue(ctx, runCommandKey{}, c)
}

func runCommandFrom(ctx context.Context) (runCommand, bool) {
	c, ok := ctx.Value(runCommandKey{}).(runCommand)
	return c, ok && c.key != ""
}

// unseen is nil when no turn of this run carries the command yet; otherwise the retry
// of the same input (errCommandDuplicate) or a reuse for another one (errCommandReused).
func (c runCommand) unseen(ctx context.Context, sc store.Scope, runRef string) error {
	repo, err := sc.Ext(runTurnKind)
	if err != nil {
		return err
	}
	rows, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{eq("run_ref", runRef), eq("command_id", c.key)}, Limit: 1})
	if err != nil || len(rows) == 0 {
		return err
	}
	if rows[0].String("input_digest") == c.digest {
		return errCommandDuplicate
	}
	return errCommandReused
}

// commandSeen answers a retry before anything else runs: a send that waits for the
// previous turn must not wait to learn it was already accepted. The insert checks it
// again under the run's input lock.
func (m *Module) commandSeen(ctx context.Context, tenant model.TenantID, runRef string, c runCommand) error {
	return m.Data.View(ctx, tenant, func(sc store.Scope) error { return c.unseen(ctx, sc, runRef) })
}

// inputReceipt is the answer of an accepted input: a retry with the same command id
// gets the same one.
func inputReceipt(commandID string) map[string]any {
	out := map[string]any{"accepted": true}
	if commandID != "" {
		out["command_id"] = commandID
	}
	return out
}
