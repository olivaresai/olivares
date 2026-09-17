// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestProtocolReplayNestedRetryOwnership(t *testing.T) {
	for _, engineName := range []store.Engine{store.EngineSQLite, store.EnginePostgres} {
		t.Run(string(engineName), func(t *testing.T) {
			if engineName == store.EnginePostgres {
				requireSplitOwnerPostgresForTest(t)
			}
			for _, scenario := range []string{
				"nested_conflict", "standalone_conflict", "nested_nonretryable",
				"dynamic_nested_success", "kind_swapped_conflict", "nested_store_duplicate",
			} {
				t.Run(scenario, func(t *testing.T) {
					backend := communicationSchemaBackend{name: "retry-ownership-sqlite", engineName: store.EngineSQLite, dsn: filepath.Join(t.TempDir(), "retry-ownership.db")}
					if engineName == store.EnginePostgres {
						backend = splitOwnerPostgresBackendForTest(t, "retry-ownership-postgres")
					}
					fixture := communicationOpenFixtureWithClock(t, backend, nil)
					// 120 s, not 15 s, and the figure is MEASURED, not chosen: under -race on ci-runner-8 with nine
					// runners on the same host (run 35084428433, race-sessions p2, 2026-09-16) every scenario of the
					// sqlite engine took 33-37 s and nested_conflict 43.09 s, so a 15 s budget over the scenario's own
					// work (rows read, two claims, the nested replay, the guard-ledger read at the end) expired inside
					// the last read: "the guard receipt ledger could not be read: context deadline exceeded". The
					// budget starts AFTER the fixture already; what was wrong was its size against the measured
					// work (08 §A row 2: a cap under the measured median is red by construction). 120 s is 2.5x the
					// slowest measured scenario; it is a test budget, not a product timeout.
					ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
					defer cancel()
					if engineName == store.EnginePostgres {
						assertReplayRetryPostgresPosture(t, ctx, backend)
					}
					before := replayRetryRows(t, ctx, fixture)
					if len(before[string(protocolBindingSpecKind)]) != 0 || len(before[string(protocolReplayGuardKind)]) != 0 {
						t.Fatal("retry fixture has prior affected rows")
					}
					outerClaim := replayRetryClaim(fixture, ProtocolReplayJTI, "outer")
					innerClaim := replayRetryClaim(fixture, ProtocolReplayMessageID, "inner")
					if scenario == "kind_swapped_conflict" {
						outerClaim.Kind, innerClaim.Kind = ProtocolReplayMessageID, ProtocolReplayJTI
					}
					outerCalls, innerCalls := 0, 0
					var innerResult ProtocolReplayResult
					var innerErr, duplicateErr error
					effects := []model.ID{}
					injected := errors.New("nonretryable callback failure after real draft")
					mutation := func(joined context.Context) (ProtocolReplaySettlement, error) {
						innerCalls++
						draft, err := createReplayRetryDraft(joined, fixture, fmt.Sprintf("effect-%d", innerCalls))
						if err != nil {
							return ProtocolReplaySettlement{}, err
						}
						effects = append(effects, draft.ID)
						if scenario == "dynamic_nested_success" {
							dependent, err := createReplayRetryDraft(joined, fixture, "from-"+draft.ID.String())
							if err != nil {
								return ProtocolReplaySettlement{}, err
							}
							effects = append(effects, dependent.ID)
						}
						if scenario == "nested_nonretryable" {
							return ProtocolReplaySettlement{}, injected
						}
						if innerCalls == 1 && scenario == "nested_store_duplicate" {
							// The canonical ID and valid record come from the successful domain
							// write. This is a real rejected SQL insert, not an injected sentinel.
							err = fixture.m.workData(fixture.tenant).Mutate(joined, func(sc store.Scope) error {
								repo, err := sc.Ext(protocolBindingSpecKind)
								if err != nil {
									return err
								}
								record, err := repo.Get(joined, draft.ID)
								if err != nil {
									return err
								}
								_, duplicateErr = repo.CreateWithID(joined, draft.ID, record)
								t.Logf("REPLAY_DUPLICATE engine=%s id=%s store_conflict=%t error=%v", engineName, draft.ID, errors.Is(duplicateErr, store.ErrConflict), duplicateErr)
								if !errors.Is(duplicateErr, store.ErrConflict) {
									t.Errorf("valid duplicate did not reach Store conflict classifier: %v", duplicateErr)
								}
								return duplicateErr
							})
							return ProtocolReplaySettlement{}, err
						}
						if innerCalls == 1 && scenario != "dynamic_nested_success" {
							// This caller error follows a successful SQL statement; unlike the
							// duplicate control it does not abort a PostgreSQL transaction.
							return ProtocolReplaySettlement{}, fmt.Errorf("injected callback conflict: %w", store.ErrConflict)
						}
						return ProtocolReplaySettlement{}, nil
					}
					apply := func() (ProtocolReplayResult, error) {
						if scenario == "standalone_conflict" {
							return fixture.m.ApplyProtocolReplay(ctx, fixture.tenant, innerClaim, mutation)
						}
						return fixture.m.ApplyProtocolReplay(ctx, fixture.tenant, outerClaim, func(joined context.Context) (ProtocolReplaySettlement, error) {
							outerCalls++
							innerResult, innerErr = fixture.m.ApplyProtocolReplay(joined, fixture.tenant, innerClaim, mutation)
							if innerErr != nil {
								return ProtocolReplaySettlement{}, innerErr
							}
							return ProtocolReplaySettlement{BindingID: innerResult.Guard.BindingID}, nil
						})
					}
					result, err := apply()
					after := replayRetryRows(t, ctx, fixture)
					logReplayRetryOutcome(t, "initial", outerCalls, innerCalls, result, err, innerResult, innerErr, effects, before, after)
					assertReplayRetryReopen(t, ctx, &fixture, backend, after)
					switch scenario {
					case "standalone_conflict":
						if err != nil || result.Replayed || result.Guard.ID.IsZero() || innerCalls != 2 || outerCalls != 0 || len(effects) != 2 {
							t.Fatalf("standalone retry: outer=%d inner=%d result=%#v err=%v", outerCalls, innerCalls, result, err)
						}
						assertReplayRetryIDs(t, after, []model.ID{effects[1]}, []ProtocolReplayGuard{result.Guard})
					case "dynamic_nested_success":
						if err != nil || innerErr != nil || result.Replayed || innerResult.Replayed || outerCalls != 1 || innerCalls != 1 || len(effects) != 2 {
							t.Fatalf("dynamic nested result: outer=%d inner=%d err=%v innerErr=%v", outerCalls, innerCalls, err, innerErr)
						}
						assertReplayRetryIDs(t, after, effects, []ProtocolReplayGuard{result.Guard, innerResult.Guard})
						for _, row := range after[string(protocolBindingSpecKind)] {
							if row.String(model.ColID) == effects[1].String() && row.String(colBindingKey) != "from-"+effects[0].String() {
								t.Fatal("dependent draft lost first result identity")
							}
						}
					case "nested_nonretryable":
						if outerCalls != 1 || innerCalls != 1 || !errors.Is(err, injected) || !errors.Is(innerErr, injected) || !reflect.DeepEqual(result, ProtocolReplayResult{}) || !reflect.DeepEqual(innerResult, ProtocolReplayResult{}) || !reflect.DeepEqual(before, after) {
							t.Fatalf("nonretryable nested rollback: outer=%d inner=%d err=%v innerErr=%v", outerCalls, innerCalls, err, innerErr)
						}
					default:
						if outerCalls != 1 || innerCalls != 1 || !errors.Is(err, ErrProtocolReplayConflict) || errors.Is(err, store.ErrConflict) || !errors.Is(innerErr, ErrProtocolReplayConflict) || errors.Is(innerErr, store.ErrConflict) || !reflect.DeepEqual(result, ProtocolReplayResult{}) || !reflect.DeepEqual(innerResult, ProtocolReplayResult{}) || !reflect.DeepEqual(before, after) {
							t.Fatalf("nested conflict must propagate once and roll back: outer=%d inner=%d err=%v innerErr=%v specs=%d guards=%d", outerCalls, innerCalls, err, innerErr, len(after[string(protocolBindingSpecKind)]), len(after[string(protocolReplayGuardKind)]))
						}
						if scenario == "nested_store_duplicate" && !errors.Is(duplicateErr, store.ErrConflict) {
							t.Fatal("missing actual duplicate-key conflict")
						}
						if scenario == "nested_conflict" {
							// Only the caller explicitly starts a new owning attempt after the
							// failed call has returned. Reuse the original still-live claims/context.
							retried, retryErr := apply()
							retriedRows := replayRetryRows(t, ctx, fixture)
							logReplayRetryOutcome(t, "explicit_retry", outerCalls, innerCalls, retried, retryErr, innerResult, innerErr, effects, after, retriedRows)
							if retryErr != nil || innerErr != nil || retried.Replayed || innerResult.Replayed || outerCalls != 2 || innerCalls != 2 || len(effects) != 2 {
								t.Fatalf("explicit retry: outer=%d inner=%d err=%v", outerCalls, innerCalls, retryErr)
							}
							assertReplayRetryIDs(t, retriedRows, []model.ID{effects[1]}, []ProtocolReplayGuard{retried.Guard, innerResult.Guard})
							replayed, replayErr := apply()
							exactRows := replayRetryRows(t, ctx, fixture)
							logReplayRetryOutcome(t, "exact_replay", outerCalls, innerCalls, replayed, replayErr, innerResult, innerErr, effects, retriedRows, exactRows)
							if replayErr != nil || !replayed.Replayed || !reflect.DeepEqual(replayed.Guard, retried.Guard) || outerCalls != 2 || innerCalls != 2 || !reflect.DeepEqual(exactRows, retriedRows) {
								t.Fatal("exact replay ran a callback or changed committed rows")
							}
							assertReplayRetryReopen(t, ctx, &fixture, backend, exactRows)
						}
					}
					if ctx.Err() != nil {
						t.Fatal(ctx.Err())
					}
				})
			}
		})
	}
}

func replayRetryClaim(f communicationSchemaFixture, kind ProtocolReplayKind, key string) ProtocolReplayClaim {
	return ProtocolReplayClaim{WorkspaceID: f.workspace, Protocol: BindingProtocolA2A, PeerAuthority: "https://peer.example", Kind: kind, ReplayID: key, ExpiresAt: time.Now().UTC().Add(time.Minute)}
}
func createReplayRetryDraft(ctx context.Context, f communicationSchemaFixture, key string) (ProtocolBindingSpec, error) {
	input := protocolSpecInputForTest(f.workspace, BindingProtocolA2A, key, 1, "")
	command := ProtocolBindingSpecCommand{Operation: ProtocolBindingSpecCreateDraft, WorkspaceID: f.workspace, Input: &input, IdempotencyKey: "retry:" + key}
	plan, err := f.m.PlanProtocolBindingSpec(ctx, f.tenant, command)
	if err != nil {
		return ProtocolBindingSpec{}, err
	}
	command.ExpectedPlanHash = plan.PlanHash
	result, err := f.m.ApplyProtocolBindingSpec(ctx, f.tenant, command)
	return result.Spec, err
}
func replayRetryRows(t *testing.T, ctx context.Context, f communicationSchemaFixture) map[string][]model.Record {
	t.Helper()
	out := map[string][]model.Record{}
	err := f.st.View(ctx, f.tenant, func(sc store.Scope) error {
		for _, kind := range []model.Kind{protocolBindingSpecKind, protocolReplayGuardKind} {
			repo, err := sc.Ext(kind)
			if err != nil {
				return err
			}
			rows, err := listAll(ctx, repo)
			if err != nil {
				return err
			}
			if rows == nil {
				rows = []model.Record{}
			}
			sort.Slice(rows, func(i, j int) bool { return rows[i].String(model.ColID) < rows[j].String(model.ColID) })
			out[string(kind)] = rows
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func assertReplayRetryIDs(t *testing.T, rows map[string][]model.Record, effects []model.ID, guards []ProtocolReplayGuard) {
	t.Helper()
	specRows, guardRows := rows[string(protocolBindingSpecKind)], rows[string(protocolReplayGuardKind)]
	if len(specRows) != len(effects) || len(guardRows) != len(guards) {
		t.Fatalf("committed rows: specs=%d guards=%d, want %d/%d", len(specRows), len(guardRows), len(effects), len(guards))
	}
	for _, id := range effects {
		found := false
		for _, row := range specRows {
			found = found || row.String(model.ColID) == id.String()
		}
		if !found || id.IsZero() {
			t.Fatalf("committed draft missing: %s", id)
		}
	}
	for _, guard := range guards {
		found := false
		for _, row := range guardRows {
			found = found || (row.String(model.ColID) == guard.ID.String() && row.String(colReplayKind) == string(guard.ReplayKind))
		}
		if !found || guard.ID.IsZero() {
			t.Fatalf("committed guard missing: %s", guard.ID)
		}
	}
}
func assertReplayRetryReopen(t *testing.T, ctx context.Context, f *communicationSchemaFixture, backend communicationSchemaBackend, expected map[string][]model.Record) {
	t.Helper()
	if backend.engineName != store.EngineSQLite {
		return
	}
	if err := f.st.Close(); err != nil {
		t.Fatal(err)
	}
	m := New()
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: backend.dsn, Debug: true}, m.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	m.UseData(api.NewModuleData(st))
	t.Cleanup(func() { _ = st.Close() })
	f.m, f.st = m, st
	actual := replayRetryRows(t, ctx, *f)
	if !reflect.DeepEqual(expected, actual) {
		t.Fatalf("complete rows changed after SQLite close/reopen: want=%#v got=%#v", expected, actual)
	}
}
func logReplayRetryOutcome(t *testing.T, phase string, outer, inner int, result ProtocolReplayResult, err error, innerResult ProtocolReplayResult, innerErr error, effects []model.ID, before, after map[string][]model.Record) {
	t.Helper()
	errorText := func(err error) string {
		if err == nil {
			return ""
		}
		return err.Error()
	}
	raw, marshalErr := json.Marshal(map[string]any{"phase": phase, "outer_callbacks": outer, "inner_callbacks": inner, "result": result, "error": errorText(err), "inner_result": innerResult, "inner_error": errorText(innerErr), "effects": effects, "before": before, "after": after})
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	t.Logf("REPLAY_RETRY_OUTCOME %s", raw)
}
func assertReplayRetryPostgresPosture(t *testing.T, ctx context.Context, backend communicationSchemaBackend) {
	t.Helper()
	db, err := sql.Open("pgx", backend.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var role, owner, version string
	var super, bypass bool
	err = db.QueryRowContext(ctx, `SELECT current_user,r.rolsuper,r.rolbypassrls,current_setting('server_version'),pg_catalog.pg_get_userbyid(c.relowner) FROM pg_catalog.pg_roles r CROSS JOIN pg_catalog.pg_class c WHERE r.rolname=current_user AND c.oid=to_regclass($1)`, protocolBindingSpecTable).Scan(&role, &super, &bypass, &version, &owner)
	if err != nil {
		t.Fatal(err)
	}
	if super || bypass || role == owner || !strings.HasPrefix(version, "16.") {
		t.Fatal("PostgreSQL16 split-owner posture absent")
	}
	t.Logf("REPLAY_RETRY_PG role=%s table_owner=%s superuser=%t bypassrls=%t version=%s", role, owner, super, bypass, version)
}
