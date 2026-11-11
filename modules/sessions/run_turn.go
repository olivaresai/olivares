// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const runTurnKind model.Kind = "sessions.run_turn"

// A turn records delivery and outcome, never conversation content. The launch
// fences late output from a process replaced by resume.
func (m *Module) registerRunTurnSchema(reg store.ExtensionRegistry) error {
	return reg.Register(model.EntityDescriptor{
		Kind: runTurnKind, Table: "sessions_run_turn",
		Fields: []model.FieldSpec{
			{Name: "run_ref", Kind: model.KindText, Principal: pdeclNoneRunRef},
			{Name: "launch_id", Kind: model.KindUUID, Principal: model.None("the id of the run's launch generation that opened the turn: run_turn.go:153, run_turn.go:183")},
			{Name: "ordinal", Kind: model.KindInt},
			{Name: "command_id", Kind: model.KindText, Nullable: true, Principal: model.None("opaque input command identity: a one-way digest of the sender and the caller's command id, never an account: run_command.go:54, run_turn.go:163")},
			{Name: "input_digest", Kind: model.KindText, Nullable: true, Principal: model.None("a one-way digest of the command and its input, compared for equality only, never an account or content: run_command.go:58, run_command.go:82")},
			{Name: "state", Kind: model.KindText, Principal: model.None("turn lifecycle state, a closed set, never an account: run_turn.go:63, run_turn.go:200")},
			{Name: "source", Kind: model.KindText, Principal: model.None("input transport, a driver key, stream-json or continuation (the engine's own resume of a cut turn), never an account: run_turn.go:63, run_turn.go:153")},
			{Name: "started_at", Kind: model.KindTimestamp},
			{Name: "ended_at", Kind: model.KindTimestamp, Nullable: true},
			{Name: colRunAuthzWorkspaceID, Kind: model.KindUUID, Nullable: true, Principal: model.None("the run's authorization workspace, never an account: runtime_schema.go:357, runtime_schema.go:397-401")},
		},
		Indexes: []model.IndexSpec{
			{Name: "command", Columns: []string{model.ColTenantID, "run_ref", "command_id"}, Unique: true},
			{Name: "ordinal", Columns: []string{model.ColTenantID, "run_ref", "ordinal"}, Unique: true},
		},
		WorkspaceLineage: model.WorkspaceLineageSpec{Column: colRunAuthzWorkspaceID, Encoding: model.WorkspaceLineageID, Unset: model.WorkspaceUnsetHidden},
	})
}

type runTurnDTO struct {
	Ordinal   int64   `json:"ordinal"`
	CommandID *string `json:"command_id"`
	State     string  `json:"state"`
	Source    string  `json:"source"`
	StartedAt string  `json:"started_at"`
	EndedAt   string  `json:"ended_at,omitempty"`
}

func lastRunTurn(ctx context.Context, sc store.Scope, ref string) (*runTurnDTO, error) {
	repo, err := sc.Ext(runTurnKind)
	if err != nil {
		return nil, err
	}
	rows, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{eq("run_ref", ref)}, Sort: []model.Sort{{Column: "ordinal", Desc: true}}, Limit: 1})
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	r := rows[0]
	out := &runTurnDTO{Ordinal: r.Int("ordinal"), State: r.String("state"), Source: r.String("source"), StartedAt: r.String("started_at"), EndedAt: r.String("ended_at")}
	if id := r.String("command_id"); id != "" {
		out.CommandID = &id
	}
	return out, nil
}

// openRunTurn commits before provider I/O. The caller holds the run input lock.
// A checked input committed its row with its admission (admitRunTurn): that row is
// this turn's, and no second transaction runs between the admission and the write.
func (m *Module) openRunTurn(ctx context.Context, lr *liveRun, commandID, source string) (model.ID, error) {
	lr.mu.Lock()
	id := lr.admittedTurn
	lr.admittedTurn = ""
	lr.mu.Unlock()
	var err error
	if id.IsZero() {
		err = m.Data.Mutate(ctx, lr.tenant, func(sc store.Scope) error {
			id, err = insertRunTurn(ctx, sc, lr, commandID, source, m.now())
			return err
		})
	}
	if err == nil {
		lr.mu.Lock()
		lr.durableTurns = append(lr.durableTurns, id)
		lr.mu.Unlock()
	}
	return id, err
}

// admitRunTurn writes, inside a checked input's admission transaction, the row of the
// turn that input starts: a user frame on stream-json, or text to a driver with no
// active turn. It is held for openRunTurn and committed or rolled back with the
// admission. The caller holds the run input lock.
func (m *Module) admitRunTurn(ctx context.Context, sc store.Scope, lr *liveRun, mode RuntimeInputMode, raw []byte) error {
	source := ""
	switch {
	case mode == RuntimeInputRaw:
		if frame, ok := parseStreamJSON(raw); ok && frame.Type == "user" {
			source = "stream-json"
		}
	case mode == RuntimeInputText && lr.session != nil && lr.session.ActiveTurn() == "":
		source = lr.driver.Key()
	}
	if source == "" {
		return nil
	}
	id, err := insertRunTurn(ctx, sc, lr, "", source, m.now())
	if err != nil {
		return err
	}
	lr.mu.Lock()
	lr.admittedTurn = id
	lr.mu.Unlock()
	return nil
}

// releaseAdmittedTurn settles an admitted row no write took up (the input was refused
// after its admission committed, or a turn was already running), so it is never left
// open with nothing to close it.
func (m *Module) releaseAdmittedTurn(lr *liveRun) {
	lr.mu.Lock()
	id := lr.admittedTurn
	lr.admittedTurn = ""
	lr.mu.Unlock()
	if !id.IsZero() {
		m.finishRunTurn(lr, turnUnsent, id, false)
	}
}

// turnUnsent is the outcome of a row whose input definitely never reached the tool. It
// is stored as failed and drops the row's command_id and input_digest: the retry of
// that input under the same id must be sent, not answered as already accepted.
const turnUnsent = "unsent"

func insertRunTurn(ctx context.Context, sc store.Scope, lr *liveRun, commandID, source string, now time.Time) (model.ID, error) {
	if isEngineContinuation(ctx) {
		source = "continuation"
	}
	digest := ""
	if c, ok := runCommandFrom(ctx); ok && commandID == "" {
		if err := c.unseen(ctx, sc, lr.runRef); err != nil {
			return "", err
		}
		commandID, digest = c.key, c.digest
	}
	runs, err := sc.Ext(runKind)
	if err != nil {
		return "", err
	}
	run, err := findRunRec(ctx, runs, lr.runRef)
	if err != nil {
		return "", err
	}
	if run.String(colRuntimeLaunchID) != lr.launchID.String() {
		return "", conflictErr("the session launch changed; input was not sent")
	}
	last, err := lastRunTurn(ctx, sc, lr.runRef)
	if err != nil {
		return "", err
	}
	ordinal := int64(1)
	if last != nil {
		ordinal = last.Ordinal + 1
	}
	row := model.Record{"run_ref": lr.runRef, "launch_id": lr.launchID.String(), "ordinal": ordinal, "state": "open", "source": source, "started_at": model.NewTimestamp(now).String()}
	if commandID != "" {
		row["command_id"] = commandID
	}
	if digest != "" {
		row["input_digest"] = digest
	}
	if ws := run.String(colRunAuthzWorkspaceID); ws != "" {
		row[colRunAuthzWorkspaceID] = ws
	}
	repo, err := sc.Ext(runTurnKind)
	if err != nil {
		return "", err
	}
	created, err := repo.Create(ctx, row)
	if err != nil {
		return "", err
	}
	return model.ID(created.String(model.ColID)), nil
}

// closeRunTurns changes only open rows. An id settles one turn; all settles
// process loss. An interrupt acknowledgement may follow its result frame; only
// that exact turn can be reclassified as interrupted.
func (m *Module) closeRunTurns(ctx context.Context, tenant model.TenantID, ref, launch, state string, id model.ID, all bool) error {
	if launch == "" {
		return nil
	}
	return m.Data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runTurnKind)
		if err != nil {
			return err
		}
		filters := []model.Filter{eq("run_ref", ref), eq("launch_id", launch), eq("state", "open")}
		if !id.IsZero() {
			filters = append(filters, eq(model.ColID, id.String()))
			if state == "interrupted" {
				filters[2] = model.Filter{Column: "state", Op: model.OpIn, Value: []string{"open", "completed", "failed"}}
			}
		}
		for {
			limit := 1
			if all {
				limit = 200
			}
			rows, _, err := repo.List(ctx, model.Query{Filters: filters, Sort: []model.Sort{{Column: "ordinal"}}, Limit: limit})
			if err != nil {
				return err
			}
			for _, row := range rows {
				row["state"], row["ended_at"] = state, model.NewTimestamp(m.now()).String()
				if state == turnUnsent {
					row["state"], row["command_id"], row["input_digest"] = "failed", nil, nil
				}
				if _, err := repo.Update(ctx, row); err != nil {
					return fmt.Errorf("close session turn: %w", err)
				}
			}
			if !all || len(rows) < limit {
				return nil
			}
		}
	})
}

const (
	processEndCloseTries   = 3 // a row left open on a stopped run has nothing else to close it
	processEndCloseBackoff = 200 * time.Millisecond
	runTurnWriteTimeout    = 10 * time.Second // the stdout reader settles rows: a stalled store must not stall it
)

func (m *Module) finishRunTurn(lr *liveRun, state string, id model.ID, all bool) {
	tries := 1
	if all {
		tries = processEndCloseTries
	}
	var err error
	for i := 0; i < tries; i++ {
		if i > 0 {
			time.Sleep(processEndCloseBackoff)
		}
		ctx, cancel := context.WithTimeout(context.Background(), runTurnWriteTimeout)
		err = m.settleRunTurns(ctx, lr, state, id, all)
		cancel()
		if err == nil {
			return
		}
	}
	m.warnf("sessions: turn outcome could not be stored", "run_ref", lr.runRef, "err", redactErr(err))
}

// keepUnsettled remembers an outcome whose store write failed.
func (lr *liveRun) keepUnsettled(id model.ID, state string) {
	lr.mu.Lock()
	defer lr.mu.Unlock()
	if lr.unsettledTurns == nil {
		lr.unsettledTurns = map[model.ID]string{}
	}
	lr.unsettledTurns[id] = state
}

// settleRunTurns closes one turn, or with all every open one. A failed write for one
// turn is kept in memory, and the all pass writes it first: a result that did arrive
// is then not recorded as the process's failure, interruption or cut. While such a
// write still fails, the all pass waits for the next try instead of overwriting it.
func (m *Module) settleRunTurns(ctx context.Context, lr *liveRun, state string, id model.ID, all bool) error {
	if !all && id.IsZero() {
		return nil
	}
	launch := lr.launchID.String()
	if all {
		lr.mu.Lock()
		pending := lr.unsettledTurns
		lr.unsettledTurns = nil
		lr.mu.Unlock()
		var errs []error
		for turn, outcome := range pending {
			if err := m.closeRunTurns(ctx, lr.tenant, lr.runRef, launch, outcome, turn, false); err != nil {
				errs = append(errs, err)
				lr.keepUnsettled(turn, outcome)
			}
		}
		if len(errs) > 0 {
			return errors.Join(errs...)
		}
	}
	err := m.closeRunTurns(ctx, lr.tenant, lr.runRef, launch, state, id, all)
	if !all {
		if err != nil {
			lr.keepUnsettled(id, state)
		} else {
			lr.mu.Lock()
			delete(lr.unsettledTurns, id)
			lr.mu.Unlock()
		}
	}
	return err
}

// Keep interrupted rows until their result arrives. A late result must consume
// that turn, rather than close a queued successor.
func (lr *liveRun) durableTurn(pop bool) model.ID {
	lr.mu.Lock()
	defer lr.mu.Unlock()
	if len(lr.durableTurns) == 0 {
		return ""
	}
	id := lr.durableTurns[0]
	if pop {
		lr.durableTurns = lr.durableTurns[1:]
	}
	return id
}

func driverTurnOutcome(data []byte) string {
	var frame struct {
		Params struct {
			Turn struct {
				Status string `json:"status"`
			} `json:"turn"`
		} `json:"params"`
		Result struct {
			StopReason string `json:"stopReason"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(data, &frame) == nil {
		switch frame.Params.Turn.Status {
		case "failed", "interrupted":
			return frame.Params.Turn.Status
		}
		if frame.Result.StopReason == "cancelled" {
			return "interrupted"
		}
		if len(frame.Error) > 0 && string(frame.Error) != "null" {
			return "failed"
		}
	}
	return "completed"
}

func (lr *liveRun) forgetDurableTurn(id model.ID) {
	lr.mu.Lock()
	defer lr.mu.Unlock()
	for i, existing := range lr.durableTurns {
		if existing == id {
			lr.durableTurns = append(lr.durableTurns[:i], lr.durableTurns[i+1:]...)
			return
		}
	}
}
