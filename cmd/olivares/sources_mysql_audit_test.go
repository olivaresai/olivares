// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/mysqlaudit"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

func TestBuildInProcSourceMySQLAudit(t *testing.T) {
	const unicodeReadLogLine = "2026-10-03T00:00:06.000000Z\t7 Query\tSELECTé/**/'synthetic-private-row'\n"  // language-data: SQL keyword boundary rejection input
	const unicodeWriteLogLine = "2026-10-03T00:00:07.000000Z\t7 Query\tUPDATEé/**/'synthetic-private-row'\n" // language-data: SQL keyword boundary rejection input
	ctx := context.Background()
	bus := eventbus.NewInProc(eventbus.Options{Logger: quietLog()})
	t.Cleanup(func() { _ = bus.Close() })
	rt := runtime.New(runtime.Options{Logger: quietLog(), Bus: bus})
	st, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, rt.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error { _, e := sys.EnsureSystemTenant(ctx); return e }); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stop, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stop)
	})
	sr := newSourceReconciler(rt, auth.NewSourceStore(st), nil, nil, t.TempDir(), nil, quietLog())
	if roster, err := sr.ListSources(ctx); err != nil || len(roster) != 0 || len(rt.LiveSourceInventory()) != 0 {
		t.Fatalf("no source should be active before explicit configuration: %+v, %v", roster, err)
	}

	infos, err := sr.ListConnectors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var info api.ConnectorInfo
	for _, candidate := range infos {
		if candidate.Kind == "mysql-audit" {
			info = candidate
		}
	}
	if info.Kind == "" || info.Transport != "in_process" || !info.FieldsKnown {
		t.Fatalf("MySQL audit must be offered with its real built-in fields: %+v", info)
	}
	fields := map[string]api.ConnectorField{}
	for _, field := range info.Fields {
		fields[field.Key] = field
	}
	if len(fields) != 4 || !fields["log_path"].Required || fields["format"].Default != "mariadb_audit" ||
		fields["follow"].Default != "true" || fields["shared_accounts"].Type != "string" {
		t.Fatalf("published MySQL audit configuration fields changed: %+v", info.Fields)
	}

	logPath := filepath.Join(t.TempDir(), "general.log")
	log := "2026-10-03T00:00:00.000000Z\t7 Connect\tdeveloper@127.0.0.1 on development\n" +
		"2026-10-03T00:00:01.000000Z\t7 Query\tSELECT payload FROM notes WHERE payload = 'synthetic-private-row'\n" +
		"2026-10-03T00:00:02.000000Z\t7 Query\tINSERT INTO notes VALUES ('synthetic-private-row')\n" +
		"2026-10-03T00:00:03.000000Z\t7 Query\tSELECT/**/'synthetic-private-row'\n" +
		"2026-10-03T00:00:04.000000Z\t7 Query\tSELECT$x/**/'synthetic-private-row'\n" +
		"2026-10-03T00:00:05.000000Z\t7 Query\tUPDATE$x/**/'synthetic-private-row'\n" +
		unicodeReadLogLine +
		unicodeWriteLogLine +
		"2026-10-03T00:00:08.000000Z\t7 Query\tſELECT/**/'synthetic-private-row'\n" +
		"2026-10-03T00:00:09.000000Z\t7 Query\tıNSERT/**/'synthetic-private-row'\n" +
		"2026-10-03T00:00:10.000000Z\t7 Query\tSELECT$/**/'synthetic-private-row'\n"
	if err := os.WriteFile(logPath, []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	seen := make(chan event.Event, 10)
	sub, err := bus.Subscribe([]event.Type{event.TypeEdgeObserved}, func(_ context.Context, e event.Event) error {
		seen <- e
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Unsubscribe)
	in := api.ConnectorOnboardInput{
		Name: "mysql-opt-in", Kind: "mysql-audit", Tenant: "acme",
		Config: map[string]string{"log_path": logPath, "format": "general_log", "follow": "false"},
	}
	res, err := sr.PutConnector(ctx, recAdmin(), in)
	if err != nil || !res.Persisted || !res.Applied || res.Action != "disabled" || rt.SourceIsRegistered(in.Name) {
		t.Fatalf("disabled onboarding must not activate MySQL audit: %+v, %v", res, err)
	}
	select {
	case e := <-seen:
		t.Fatalf("disabled source emitted an observation: %+v", e)
	default:
	}

	in.Enabled = true
	res, err = sr.PutConnector(ctx, recAdmin(), in)
	if err != nil || !res.Persisted || !res.Applied || !rt.SourceIsRegistered(in.Name) {
		t.Fatalf("explicit MySQL audit activation failed: %+v, %v", res, err)
	}
	for _, want := range []struct {
		mode sdkmodel.AccessMode
		verb string
	}{
		{sdkmodel.ModeRead, "SELECT"}, {sdkmodel.ModeWrite, "INSERT"}, {sdkmodel.ModeRead, "SELECT"},
		{sdkmodel.ModeUnknown, "QUERY"}, {sdkmodel.ModeUnknown, "QUERY"},
		{sdkmodel.ModeUnknown, "QUERY"}, {sdkmodel.ModeUnknown, "QUERY"},
		{sdkmodel.ModeUnknown, "QUERY"}, {sdkmodel.ModeUnknown, "QUERY"},
		{sdkmodel.ModeUnknown, "QUERY"},
	} {
		select {
		case e := <-seen:
			raw, err := json.Marshal(e)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{"SELECT payload FROM", "INSERT INTO notes", "synthetic-private-row"} {
				if strings.Contains(strings.ToLower(string(raw)), strings.ToLower(forbidden)) {
					t.Fatalf("observation contains raw SQL or content: %s", raw)
				}
			}
			edge, ok := event.EdgeOf(e)
			if !ok || e.Source != in.Name || edge.Source != mysqlaudit.SignalMySQLAudit ||
				edge.OriginRef != "developer@127.0.0.1" || edge.ResourceKind != "mysql.database" ||
				edge.ResourceRef != "development" || edge.Mode != want.mode || edge.ToolRef != want.verb ||
				edge.Confidence != sdkmodel.ConfidenceAttributed {
				t.Fatalf("incorrect access metadata: event=%+v, edge=%+v, want=%+v", e, edge, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("explicit source did not emit its %s observation", want.verb)
		}
	}
	if roster, err := sr.ListSources(ctx); err != nil || len(roster) != 1 || roster[0].Component != mysqlaudit.Name ||
		roster[0].Name != in.Name || roster[0].Config["log_path"] != logPath {
		t.Fatalf("configured source identity/settings were not preserved: %+v, %v", roster, err)
	}
	in.Enabled = false
	res, err = sr.PutConnector(ctx, recAdmin(), in)
	if err != nil || !res.Persisted || !res.Applied || rt.SourceIsRegistered(in.Name) {
		t.Fatalf("disabling MySQL audit must stop its registration: %+v, %v", res, err)
	}
	if roster, err := sr.ListSources(ctx); err != nil || len(roster) != 1 || roster[0].Enabled || roster[0].Status != "disabled" {
		t.Fatalf("disabled source configuration must remain stored: %+v, %v", roster, err)
	}
}
