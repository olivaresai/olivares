// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dr_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/dr"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/store"
)

func TestOfflineDRReaderPreservesVerificationGuards(t *testing.T) {
	e := newEstate(t)
	for i := 0; i < 2; i++ {
		e.appendN(t, e.newTenant(t), 3)
	}
	e.checkpointAll(t)
	want := manifestFor(t, e)
	reader, err := sqlstore.OpenDRReader(t.Context(), store.Config{Engine: store.EngineSQLite, DSN: e.dbPath})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	build := func() *dr.Manifest {
		m, err := dr.BuildManifestFromReader(t.Context(), reader, e.pub(), e.cpVerifier(t), dr.BuildOptions{
			EngineKind: "sqlite", Version: "test", TipMatch: dr.TipExact, Keys: want.Keys, Now: time.Now(),
		})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	m := build()
	if !reflect.DeepEqual(m.Tenants, want.Tenants) {
		t.Fatalf("offline tips differ: %+v / %+v", m.Tenants, want.Tenants)
	}
	rep, err := dr.RestoreVerifyFromReader(t.Context(), reader, m, e.pub(), e.cpVerifier(t))
	if err != nil || !rep.OK {
		t.Fatalf("clean reader: %+v %v", rep, err)
	}
	wrongKey, _ := genKey(t)
	rep, err = dr.RestoreVerifyFromReader(t.Context(), reader, m, wrongKey, e.cpVerifier(t))
	if err != nil || rep.OK || rep.Key.Match {
		t.Fatalf("wrong key certified: %+v %v", rep, err)
	}
	partial := *m
	partial.Tenants = m.Tenants[1:]
	rep, err = dr.RestoreVerifyFromReader(t.Context(), reader, &partial, e.pub(), e.cpVerifier(t))
	if err != nil || rep.OK || !strings.Contains(strings.Join(rep.Problems, ";"), "not in the manifest") {
		t.Fatalf("omitted tenant certified: %+v %v", rep, err)
	}
	wrongTip := *m
	wrongTip.Tenants = append([]dr.TenantTip(nil), m.Tenants...)
	wrongTip.Tenants[0].HeadSeq++
	rep, err = dr.RestoreVerifyFromReader(t.Context(), reader, &wrongTip, e.pub(), e.cpVerifier(t))
	if err != nil || rep.OK {
		t.Fatalf("wrong exact tip certified: %+v %v", rep, err)
	}
	rawExec(t, e.dbPath, "DROP TRIGGER audit_events_no_update", "UPDATE audit_events SET sig = randomblob(64) WHERE action = 'audit.checkpoint'")
	bad := build()
	refused := false
	for _, tip := range bad.Tenants {
		refused = refused || (!tip.VerifiedAtBackup && strings.HasPrefix(tip.VerifyReason, "checkpoints:"))
	}
	if !refused {
		t.Fatal("backup certified a corrupt checkpoint")
	}
	rep, err = dr.RestoreVerifyFromReader(t.Context(), reader, m, e.pub(), e.cpVerifier(t))
	if err != nil || rep.OK || !strings.Contains(strings.Join(rep.Problems, ";"), "checkpoints") {
		t.Fatalf("corrupt checkpoint certified: %+v %v", rep, err)
	}
	rawExec(t, e.dbPath, "UPDATE audit_events SET sig = randomblob(64) WHERE action = 'agent.create'")
	bad = build()
	refused = false
	for _, tip := range bad.Tenants {
		refused = refused || (!tip.VerifiedAtBackup && strings.HasPrefix(tip.VerifyReason, "events:"))
	}
	if !refused {
		t.Fatal("backup certified a corrupt ordinary event signature")
	}
	rep, err = dr.RestoreVerifyFromReader(t.Context(), reader, m, e.pub(), e.cpVerifier(t))
	if err != nil {
		t.Fatal(err)
	}
	eventsRefused := false
	for _, tenant := range rep.Tenants {
		eventsRefused = eventsRefused || !tenant.EventsOK
	}
	if rep.OK || !rep.Key.Match || !eventsRefused {
		t.Fatalf("corrupt ordinary event signature certified with the correct key: %+v %v", rep, err)
	}
}
