// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/core/eventbus/natsbus"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
)

func TestSourceBindingAdmissionBeforeQueueAndReplay(t *testing.T) {
	ctx := context.Background()
	ss, st, tenant := newSessionsStore(t)
	ss.UseExecutionEnvironmentRef("env-1")
	bus := eventbus.NewInProc(eventbus.Options{Logger: quietLog()})
	rt := runtime.New(runtime.Options{Logger: quietLog(), Bus: bus, SourceRegistrationAdmission: ss.AdmitSourceRegistration})
	capture := &regCaptureModule{got: make(chan event.Event, 16)}
	if err := rt.AddModule(capture, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := rt.AddModule(ss, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stop, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		_ = rt.Stop(stop)
		_ = bus.Close()
	})
	sourceStore := auth.NewSourceStore(st)
	sr := newSourceReconciler(rt, sourceStore, nil, nil, t.TempDir(), nil, quietLog())
	sr.useEnvironmentRef("env-1")
	source := &regEmitSource{name: "olivares.claude", fire: make(chan struct{}, 4)}
	sr.prepare = func(_ context.Context, _ model.SourceDef) (*runtime.PreparedSource, sdk.Config, string) {
		return rt.PrepareInProcSource(source), sdk.Config{}, ""
	}
	row, err := sourceStore.Put(ctx, recAdmin(), model.SourceDef{Scope: auth.GlobalSourceScope, Name: "profile-admission", Kind: "claude", Tenant: tenant.String(), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sr.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	ss.UseProviderSourceResolver(&providerSourceResolver{store: sourceStore, sr: sr, authz: auth.NewAuthorizer(nil), env: "env-1"})
	profile, err := ss.CreateProfile(ctx, tenant, sessions.CreateProfileInput{Driver: "claude", ConfigHome: t.TempDir(), UserHome: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := ss.CreateBinding(ctx, tenant, recAdmin(), sessions.CreateBindingInput{SourceID: row.ID, SourceRevision: row.Version, ProfileRef: profile.Ref})
	if err != nil {
		t.Fatal(err)
	}
	recv := func() event.Event {
		t.Helper()
		select {
		case e := <-capture.got:
			return e
		case <-time.After(5 * time.Second):
			t.Fatal("no source event")
		}
		return event.Event{}
	}
	source.fire <- struct{}{}
	admitted := recv()
	if admitted.SourceRegistration == nil || admitted.SourceRegistration.BindingRef != binding.Ref {
		t.Fatal("origin did not stamp its approved decision")
	}
	encoded, err := natsbus.EncodeEvent(admitted)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := natsbus.DecodeEvent(encoded, natsbus.DefaultDecoders())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ss.RevokeBinding(ctx, tenant, binding.Ref); err != nil {
		t.Fatal(err)
	}
	if err := bus.Publish(ctx, replay); err != nil {
		t.Fatal(err)
	}
	replayed := recv()
	if replayed.ID != admitted.ID || *replayed.SourceRegistration != *admitted.SourceRegistration {
		t.Fatal("codec/replay changed admitted provenance")
	}
	source.fire <- struct{}{}
	fresh := recv()
	if fresh.SourceRegistration == nil || fresh.SourceRegistration.BindingRef != "" || fresh.ID == admitted.ID {
		t.Fatal("new post-revoke observation reused the old decision")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		samples, err := ss.SampleLive(ctx, tenant, sessions.LiveSampleQuery{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		observed, unattributed := false, false
		for _, s := range samples {
			if s.Attribution == "observed" && s.ProfileRef == profile.Ref {
				observed = true
			}
			if s.Attribution == "source" && s.ProfileRef == "" {
				unattributed = true
			}
			if s.Attribution == "managed" {
				t.Fatal("cooperative source acquired managed identity")
			}
		}
		if observed && unattributed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("source admission/fold did not retain both historical and new scopes")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
