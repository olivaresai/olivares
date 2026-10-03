// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestRetirementActiveLegalHoldBlocksUntilReleased(t *testing.T) {
	onConsentEngines(t, func(t *testing.T, e *consentEstate) {
		for _, scope := range []string{"subject", "tenant"} {
			t.Run(scope, func(t *testing.T) {
				sub := e.forSubtest(t)
				user := sub.onboard(sub.tT, scope+"-hold@consent.test", "viewer")
				holdRecord := model.Record{
					"matter_ref": "matter-retirement", "scope_kind": scope, "subject_kind": "user",
					"subject_ref": "user:" + user.String(), "reason": "PRIVATE HOLD CONTENT", "status": "active", "created_by": "test",
				}
				if scope == "tenant" {
					holdRecord["subject_kind"], holdRecord["subject_ref"] = nil, nil
				}
				id := sub.seedFenced(sub.tT, "compliance.legal_hold", holdRecord, user)
				sub.scimDelete(sub.tT, user)
				sub.runPump()
				sub.wantBlocked(t, user, sub.tT, id.String())
				rec, _ := sub.record(user, sub.tT)
				if !strings.Contains(rec.BlockingRefs, "legal_hold") || !strings.Contains(rec.ModuleResults, "matter-retirement") || strings.Contains(rec.ModuleResults, "PRIVATE HOLD CONTENT") {
					t.Fatalf("hold receipt must name the hold and matter without content: %+v", rec)
				}
				if r := sub.readmit(sub.tT, scope+"-hold@consent.test", "viewer"); r.code != http.StatusConflict {
					t.Fatalf("readmission under active hold=%d %s", r.code, r.raw)
				}
				sub.releaseSeededRetirementHold(id)
				if _, err := sub.eng.authr.AdvanceRetirement(context.Background(), sub.pump().steps(), user, sub.tT); err != nil {
					t.Fatal(err)
				}
				sub.wantRetired(t, user, sub.tT)
				if !sub.rowExists(sub.tT, "compliance.legal_hold", id) {
					t.Fatal("retirement erased the hold record")
				}
			})
		}
	})
}

// A fixture release changes only the existing hold lifecycle's recorded verdict;
// release authorization and dual control remain covered by compliance's tests.
func (e *consentEstate) releaseSeededRetirementHold(id model.ID) {
	e.t.Helper()
	err := e.eng.store.Mutate(context.Background(), e.tT, func(sc store.Scope) error {
		repo, err := sc.Ext("compliance.legal_hold")
		if err != nil {
			return err
		}
		rec, err := repo.Get(context.Background(), id)
		if err != nil {
			return err
		}
		rec["status"] = "released"
		_, err = repo.Update(context.Background(), rec)
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
}

func TestDormantComplianceStillBlocksRetirementOnAnActiveHold(t *testing.T) {
	for _, engineName := range []string{"sqlite", "postgres"} {
		t.Run(engineName, func(t *testing.T) {
			backing := consentBacking(t, engineName)
			prepareCompositionTestBoot(t)
			cfg := backing.bootConfig()
			cfg.ApplyModuleProfile = true
			// A remote store without a node selection first boots every module for
			// pre-listen reconciliation. Pin the standard selection this case exercises.
			if err := saveNodeModuleSelection(cfg.DataDir, standardModuleSelection(), time.Now()); err != nil {
				t.Fatal(err)
			}
			eng, err := boot(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = eng.Close() })
			if eng.moduleProfile.Active("compliance") {
				t.Fatal("fixture must boot the default profile with dormant compliance")
			}
			e := &consentEstate{t: t, engine: engineName, eng: eng, h: eng.api.Handler()}
			e.setupRoot()
			e.tT = e.createOrg("dormant-holds")
			e.tB = e.createOrg("control")
			user := e.onboard(e.tT, "dormant-hold@consent.test", "viewer")
			id := e.seedFenced(e.tT, "compliance.legal_hold", model.Record{
				"matter_ref": "matter-dormant", "scope_kind": "subject", "subject_kind": "user",
				"subject_ref": user.String(), "reason": "PRIVATE DORMANT HOLD CONTENT", "status": "active", "created_by": "test",
			}, user)
			if r := e.do("GET", "/v1/m/compliance/holds", e.admin, e.tT, nil); r.code != http.StatusNotFound || r.errorCode() != "module_not_enabled" {
				t.Fatalf("dormant compliance route=%d %s", r.code, r.raw)
			}
			e.scimDelete(e.tT, user)
			e.runPump()
			e.wantBlocked(t, user, e.tT, id.String())
			rec, _ := e.record(user, e.tT)
			if !strings.Contains(rec.ModuleResults, "matter-dormant") || strings.Contains(rec.ModuleResults, "PRIVATE DORMANT HOLD CONTENT") {
				t.Fatalf("dormant hold receipt=%s", rec.ModuleResults)
			}
			e.releaseSeededRetirementHold(id)
			if _, err := e.eng.authr.AdvanceRetirement(context.Background(), e.pump().steps(), user, e.tT); err != nil {
				t.Fatal(err)
			}
			e.wantRetired(t, user, e.tT)
		})
	}
}

func TestRetirementLegalHoldStoreNamesNonUserHolds(t *testing.T) {
	onConsentEngines(t, func(t *testing.T, e *consentEstate) {
		user := e.onboard(e.tT, "session-hold@consent.test", "viewer")
		e.scimDelete(e.tT, user)
		var out auth.RetirementOutcome
		if err := json.Unmarshal([]byte(`{"Stores":[{"store":"session-cockpit:sqlite:owned-node","state":"legal_hold","holds":[{"id":"session-hold-id","matter_ref":"matter-session"},{"id":"agent-hold-id","matter_ref":"matter-agent"}]}]}`), &out); err != nil {
			t.Fatal(err)
		}
		step := &erasureFixtureStep{st: e.eng.store, outcome: out}
		pass := erasureFixturePass(t, e, user, step)
		for _, name := range []string{"legal_hold", "session-hold-id", "matter-session", "agent-hold-id", "matter-agent"} {
			if !strings.Contains(pass.Record.BlockingRefs, name) || !strings.Contains(pass.Record.ModuleResults, name) {
				t.Errorf("hold %q lost in blocking receipt: %+v", name, pass.Record)
			}
		}
		step.outcome.Stores[0].State = "clean"
		if step.outcome.Clean() {
			t.Fatal("an active hold was accepted as a clean store")
		}
		erasureFixturePass(t, e, user, step)
		e.wantBlocked(t, user, e.tT, "session-hold-id")
		if err := json.Unmarshal([]byte(`{"Stores":[{"store":"session-cockpit:sqlite:owned-node","state":"clean","holds":[]}]}`), &step.outcome); err != nil {
			t.Fatal(err)
		}
		erasureFixturePass(t, e, user, step)
		e.wantRetired(t, user, e.tT)
	})
}

func TestRetirementHoldSubjectKindIsNotAUserAlias(t *testing.T) {
	onConsentEngines(t, func(t *testing.T, e *consentEstate) {
		user := e.onboard(e.tT, "unrelated-hold@consent.test", "viewer")
		for _, kind := range []string{"session", "agent"} {
			e.seedFenced(e.tT, "compliance.legal_hold", model.Record{
				"matter_ref": "matter-unrelated", "scope_kind": "subject", "subject_kind": kind,
				"subject_ref": user.String(), "reason": "PRIVATE OTHER SUBJECT CONTENT", "status": "active", "created_by": "test",
			}, user)
		}
		e.scimDelete(e.tT, user)
		e.runPump()
		e.wantRetired(t, user, e.tT)
	})
}
