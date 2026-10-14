//go:build !enterprise || !addon_ids

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestCommunityCAEPRefusesConfigurationAndVerifiedEventsWithoutChangingStoredConfig(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	tenant := provisionTenant(t, st, "caep-edition")
	a := auth.NewAuthenticator(st, nil)
	actor := auth.Principal{Kind: auth.KindUser, UserID: model.NewID(), Superadmin: true}
	signer := newES256Signer(t)
	pub := auth.SETPublisher{ID: "edition-caep", Enabled: true, Issuer: "https://edition.example.test", Audiences: []string{"edition-receiver"}, Keys: []auth.SETVerificationKey{{Kid: "k1", Alg: "ES256", PEM: signer.pem}}}
	if err := a.ConfigurePublisher(ctx, actor, tenant, pub); err != nil {
		t.Fatal(err)
	}
	stored := map[string]any{"enabled": true, "publisher_id": pub.ID, "device_noncompliant_action": "step_up"}
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		org, err := sc.Org(ctx)
		if err != nil {
			return err
		}
		if org.Settings == nil {
			org.Settings = map[string]any{}
		}
		org.Settings["caep_set"] = stored
		_, err = sc.SetOrgSettings(ctx, org.Settings)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	claims := map[string]any{"iss": pub.Issuer, "aud": pub.Audiences, "iat": time.Now().Unix(), "jti": "community-caep-edition", "events": map[string]any{"https://edition.example.test/ignored": map[string]any{}}}
	parts := strings.Split(string(signer.signSET(t, "k1", claims)), ".")
	if len(parts) != 3 {
		t.Fatal("signed SET is not compact JWS")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.CAEPReceiveEvent(ctx, actor, tenant, auth.CAEPEventEnvelope{SETEnvelope: auth.SETEnvelope{Alg: "ES256", Kid: "k1", SigningInput: []byte(parts[0] + "." + parts[1]), Signature: sig, Issuer: pub.Issuer, Audience: pub.Audiences, JTI: "community-caep-edition", IssuedAt: time.Now().Unix()}, Action: auth.CAEPIgnore}); !errors.Is(err, auth.ErrCAEPRequiresBusiness) {
		t.Error("Community acknowledged a verified CAEP event; want explicit Business refusal")
	}
	if err := a.ConfigureCAEPSet(ctx, actor, tenant, auth.CAEPSetConfig{}); !errors.Is(err, auth.ErrCAEPRequiresBusiness) {
		t.Error("Community changed the stored CAEP receiver configuration; want explicit Business refusal")
	}
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		org, err := sc.Org(ctx)
		if err != nil {
			return err
		}
		got, _ := json.Marshal(org.Settings["caep_set"])
		want, _ := json.Marshal(stored)
		if string(got) != string(want) {
			t.Errorf("stored CAEP config changed: got %s, want %s", got, want)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
