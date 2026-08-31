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

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// readSubscriptionRow reads the STORED record, not the rendering of it.
//
// The secret assertion in this file cannot go through `get`: that verb prints a HINT and
// deliberately never prints the value, so a test that watched its output would pass just as
// happily against a version that rotated the secret on every update. What has to be proved is
// that the sealed column itself is byte-for-byte what it was, and only the store can say so.
func readSubscriptionRow(t *testing.T, dir, tenant, id string) model.Record {
	t.Helper()
	ctx := context.Background()
	eng, err := boot(ctx, bootConfig{DataDir: dir, Version: "test", Logger: discardLog()})
	if err != nil {
		t.Fatalf("boot to read subscription: %v", err)
	}
	defer func() { _ = eng.Close() }()
	tid, err := model.ParseTenantID(tenant)
	if err != nil {
		t.Fatalf("parse tenant: %v", err)
	}
	var out model.Record
	if verr := eng.store.View(ctx, tid, func(sc store.Scope) error {
		repo, rerr := sc.Ext(evtSubscriptionKind)
		if rerr != nil {
			return rerr
		}
		rec, rerr := repo.Get(ctx, model.ID(id))
		if rerr != nil {
			return rerr
		}
		out = rec
		return nil
	}); verr != nil {
		t.Fatalf("read subscription %s: %v", id, verr)
	}
	return out
}

// authorizeEgressHosts points the CLI at an operator policy that permits the named hosts.
//
// WITHOUT IT EVERY TEST HERE FAILS FOR THE RIGHT REASON, which is worth stating because the
// first draft of this file did exactly that: on a deployment where no platform operator has
// authorized any destination, changing an endpoint is REFUSED — "this deployment requires a
// platform operator to authorize event destinations before one can be used". That is the
// deny-closed default working, not an obstacle, so the fixture authorizes explicitly instead
// of the test being written around it. A test that had reached for a bypass here would have
// proved the verb works in a configuration nobody runs.
func authorizeEgressHosts(t *testing.T, hosts ...string) {
	t.Helper()
	allow := make([]string, 0, len(hosts))
	for _, h := range hosts {
		allow = append(allow, `{"host":"`+h+`"}`)
	}
	body := `{"default":{"allow":[` + strings.Join(allow, ",") + `]}}`
	// Loopback destinations, because the checker RESOLVES the host before it can compare it
	// against the policy: a `.invalid` name fails with "did not resolve" and the test would
	// then be measuring DNS instead of authorization. 127.0.0.1 and localhost both resolve
	// and are different host strings, so one can be authorized and the other not.
	t.Setenv(eventingAllowLoopbackEnv, "1")
	path := filepath.Join(t.TempDir(), "egress.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write egress policy: %v", err)
	}
	t.Setenv(envEventingEgressPolicy, path)
}

// TestEventingSubscriptionsUpdateKeepsTheSecret is the reason the verb exists.
//
// Before it, moving an endpoint meant rm + create, and create ISSUES A NEW SIGNING SECRET.
// The operator asked to change a URL and got a credential rotation that silently broke every
// receiver still verifying with the old one. If this test ever goes green for the wrong
// reason, the verb has become the thing it replaced.
func TestEventingSubscriptionsUpdateKeepsTheSecret(t *testing.T) {
	authorizeEgressHosts(t, "127.0.0.1")
	dir, tenant := seededTenantDataDir(t)
	const sealed = "sealed-secret-value"
	id := seedSubscriptionRow(t, dir, tenant, "https://127.0.0.1:9443/hook", sealed)

	before := readSubscriptionRow(t, dir, tenant, id)

	out, err := runEventing(t, "subscriptions", "update", "--tenant", tenant,
		"--data-dir", dir, "--id", id, "--endpoint", "https://127.0.0.1:9444/moved",
		"--format", "json")
	if err != nil {
		t.Fatalf("update: %v\n%s", err, out)
	}

	after := readSubscriptionRow(t, dir, tenant, id)

	if got := after.String(evtColSubEndpoint); got != "https://127.0.0.1:9444/moved" {
		t.Fatalf("the endpoint did not move: got %q", got)
	}
	// THE ASSERTION THIS FILE EXISTS FOR, against the stored column and not a rendering.
	if got, want := after.String(evtColSubSecret), before.String(evtColSubSecret); got != want {
		t.Fatalf("update REISSUED the signing secret: stored %q, was %q — this is the rm+create defect returning", got, want)
	}
	if got, want := after.String(evtColSubSecretHint), before.String(evtColSubSecretHint); got != want {
		t.Fatalf("update rewrote the secret hint: %q, was %q", got, want)
	}
	if after.String(evtColSubSecret) == "" {
		t.Fatal("the stored secret is EMPTY after the update — an assertion that both sides are equal passes trivially when both are blank")
	}

	// UNTOUCHED FIELDS SURVIVE. This is the partial-update half, and without it the verb
	// would be a PUT wearing an update's name: an operator fixing one URL would silently
	// clear the retry policy and the event type list.
	for _, c := range []struct{ col, label string }{
		{evtColSubName, "name"},
		{evtColSubTypes, "event types"},
		{evtColSubRole, "role"},
		{evtColSubAuthType, "auth type"},
	} {
		if got, want := after.String(c.col), before.String(c.col); got != want {
			t.Fatalf("update cleared the %s it was not asked to change: %q, was %q", c.label, got, want)
		}
	}

	var doc eventingSubscriptionUpdated
	if jerr := json.Unmarshal([]byte(out), &doc); jerr != nil {
		t.Fatalf("update -o json is not valid JSON: %v\n%s", jerr, out)
	}
	if !doc.SecretUnchanged {
		t.Fatal("the JSON must ASSERT secret_unchanged: a script moving an endpoint needs to check it, and an absent field is not a check")
	}
	if len(doc.Changed) != 1 || doc.Changed[0] != "endpoint" {
		t.Fatalf("changed should name exactly what moved, got %v", doc.Changed)
	}
}

// TestEventingSubscriptionsUpdateRefusesToChangeNothing — an update naming no field is
// almost always a mistyped flag, and reporting success would teach the operator that
// something ran. This is the direction a verb usually gets wrong: doing nothing quietly.
func TestEventingSubscriptionsUpdateRefusesToChangeNothing(t *testing.T) {
	dir, tenant := seededTenantDataDir(t)
	id := seedSubscriptionRow(t, dir, tenant, "https://127.0.0.1:9443/hook", "sealed-secret-value")

	out, err := runEventing(t, "subscriptions", "update", "--tenant", tenant, "--data-dir", dir, "--id", id)
	if err == nil {
		t.Fatalf("an update that names no field must FAIL, got success:\n%s", out)
	}
	if !strings.Contains(err.Error()+out, "changes nothing") {
		t.Fatalf("the refusal must say what to pass, got: %v\n%s", err, out)
	}
}

// TestEventingSubscriptionsUpdateUnknownIDFails — the deny direction. An id that does not
// exist must fail rather than create one or report success over an empty record.
func TestEventingSubscriptionsUpdateUnknownIDFails(t *testing.T) {
	dir, tenant := seededTenantDataDir(t)
	_ = seedSubscriptionRow(t, dir, tenant, "https://127.0.0.1:9443/hook", "sealed-secret-value")

	out, err := runEventing(t, "subscriptions", "update", "--tenant", tenant,
		"--data-dir", dir, "--id", "sub-does-not-exist", "--name", "renamed")
	if err == nil {
		t.Fatalf("update of an unknown id must FAIL, got success:\n%s", out)
	}
}

// TestEventingSubscriptionsUpdateHonoursTheEndpointPolicy — the deny direction, and the one
// that would rot silently. `update` is a SECOND write path into the same column the API
// guards, and this repository has already paid for that shape once: the CLI `create` used to
// write the endpoint raw while its own help text promised "https required". If this verb ever
// stops asking the checker, an operator could move a subscription to a destination the
// platform operator never authorized, and nothing else in the tree would notice.
func TestEventingSubscriptionsUpdateHonoursTheEndpointPolicy(t *testing.T) {
	authorizeEgressHosts(t, "127.0.0.1")
	dir, tenant := seededTenantDataDir(t)
	id := seedSubscriptionRow(t, dir, tenant, "https://127.0.0.1:9443/hook", "sealed-secret-value")

	out, err := runEventing(t, "subscriptions", "update", "--tenant", tenant,
		"--data-dir", dir, "--id", id, "--endpoint", "https://localhost:9443/hook")
	if err == nil {
		t.Fatalf("update moved the endpoint to an UNAUTHORIZED host:\n%s", out)
	}

	// ...and the refusal must not have written anything. A guard that rejects after the
	// mutation is not a guard.
	rec := readSubscriptionRow(t, dir, tenant, id)
	if got := rec.String(evtColSubEndpoint); got != "https://127.0.0.1:9443/hook" {
		t.Fatalf("the refused update still moved the stored endpoint to %q", got)
	}

	// POSITIVE CONTROL: the same command with an AUTHORIZED host succeeds. Without it this
	// test would pass against a verb that refuses every update, which is the failure mode a
	// deny-direction test invites.
	authorizeEgressHosts(t, "127.0.0.1", "localhost")
	if out, err := runEventing(t, "subscriptions", "update", "--tenant", tenant,
		"--data-dir", dir, "--id", id, "--endpoint", "https://localhost:9445/allowed"); err != nil {
		t.Fatalf("an AUTHORIZED destination must be accepted: %v\n%s", err, out)
	}
}

// TestEventingSubscriptionsRotateSecret — the other half of `update`, and the pair only makes
// sense together: update never rotates, so rotating on purpose must have a door of its own.
// Before this verb the only way was rm + create, which rotates AND mints a new subscription id.
// REACTIVAR ES HACER EFECTIVO UN DESTINO, y hasta el 2026-08-20 el CLI no lo comprobaba.
//
// Lo encontro el contraste Codex `sol max` (hallazgo C08-04-3, VERIFICADO). El verbo solo llamaba
// al checker de egreso cuando el TEXTO del endpoint cambiaba, asi que `--enabled=true` sobre una
// suscripcion desactivada cuyo host habia perdido autorizacion salia 0 y la dejaba almacenada como
// activa. El mismo endpoint, pasado por `--endpoint`, si era rechazado.
//
// ⚠ ALCANCE, tal como lo acoto el contraste y sin inflarlo: NO era una fuga de bytes. El envio
// vuelve a evaluar el URL de forma autoritaria y devuelve `dead` ante una denegacion. Lo que este
// testigo defiende es que una configuracion no pueda declararse ACTIVA, con exito de CLI, cuando su
// destino ya no esta autorizado.
func TestEventingSubscriptionsEnablingRechecksTheEndpointPolicy(t *testing.T) {
	authorizeEgressHosts(t, "127.0.0.1")
	dir, tenant := seededTenantDataDir(t)
	id := seedSubscriptionRow(t, dir, tenant, "https://127.0.0.1:9443/hook", "sealed-secret-value")

	// Desactivarla mientras su host TODAVIA esta autorizado: esa mitad debe seguir siendo posible.
	if out, err := runEventing(t, "subscriptions", "update", "--tenant", tenant,
		"--data-dir", dir, "--id", id, "--enabled=false"); err != nil {
		t.Fatalf("disabling a subscription on an authorized host failed:\n%s\n%v", out, err)
	}
	if rec := readSubscriptionRow(t, dir, tenant, id); rec.Bool(evtColSubEnabled) {
		t.Fatalf("the subscription is still enabled after --enabled=false")
	}

	// La politica cambia y deja de autorizar ese host.
	authorizeEgressHosts(t, "localhost")

	// EL CASO: reactivar sin tocar el endpoint. Antes salia 0.
	out, err := runEventing(t, "subscriptions", "update", "--tenant", tenant,
		"--data-dir", dir, "--id", id, "--enabled=true")
	if err == nil {
		t.Fatalf("re-enabling a subscription whose endpoint LOST authorization succeeded:\n%s", out)
	}

	// Y el rechazo no puede haber escrito: una guarda que rechaza despues de la mutacion no es una
	// guarda. Es la misma propiedad que exige el testigo del endpoint, y por la misma razon.
	if rec := readSubscriptionRow(t, dir, tenant, id); rec.Bool(evtColSubEnabled) {
		t.Fatalf("the refused re-enable still stored enabled=true")
	}

	// CONTROL NEGATIVO 1 — DESACTIVAR SIGUE EXENTO. Es la trampa que la exencion original existe
	// para cortar: una suscripcion cuyo host es anterior a la politica de hoy tiene que poder
	// apagarse. Sin este caso, "comprueba siempre" pasaria igual y romperia el apagado.
	if out, err := runEventing(t, "subscriptions", "update", "--tenant", tenant,
		"--data-dir", dir, "--id", id, "--enabled=false"); err != nil {
		t.Fatalf("disabling under a REVOKED policy must stay possible:\n%s\n%v", out, err)
	}

	// CONTROL NEGATIVO 2 — con el host RE-autorizado, reactivar funciona. Sin el, este testigo
	// pasaria contra un verbo que rechaza toda reactivacion, que es el fallo de la otra direccion:
	// un control que solo sabe decir "no" no distingue "correcto" de "no hace nada".
	authorizeEgressHosts(t, "127.0.0.1")
	if out, err := runEventing(t, "subscriptions", "update", "--tenant", tenant,
		"--data-dir", dir, "--id", id, "--enabled=true"); err != nil {
		t.Fatalf("re-enabling on an AUTHORIZED host must succeed:\n%s\n%v", out, err)
	}
	if rec := readSubscriptionRow(t, dir, tenant, id); !rec.Bool(evtColSubEnabled) {
		t.Fatalf("the accepted re-enable did not store enabled=true")
	}
}

func TestEventingSubscriptionsRotateSecret(t *testing.T) {
	authorizeEgressHosts(t, "127.0.0.1")
	dir, tenant := seededTenantDataDir(t)
	const sealed = "sealed-secret-value"
	id := seedSubscriptionRow(t, dir, tenant, "https://127.0.0.1:9443/hook", sealed)

	before := readSubscriptionRow(t, dir, tenant, id)

	out, err := runEventing(t, "subscriptions", "rotate-secret", "--tenant", tenant,
		"--data-dir", dir, "--id", id, "--yes", "--format", "json")
	if err != nil {
		t.Fatalf("rotate-secret: %v\n%s", err, out)
	}
	after := readSubscriptionRow(t, dir, tenant, id)

	if got := after.String(evtColSubSecret); got == before.String(evtColSubSecret) {
		t.Fatal("rotate-secret did NOT change the stored secret — the whole verb is a no-op")
	}
	if after.String(evtColSubSecret) == "" {
		t.Fatal("rotate-secret left the stored secret EMPTY: every delivery would be unsigned")
	}
	// THE HINT TRAVELS WITH THE SECRET. `get` prints the hint and never the value, so a hint
	// left behind would keep telling the operator the OLD credential is current — the one
	// reading that is most likely to be trusted, and the only one they can see.
	if got, was := after.String(evtColSubSecretHint), before.String(evtColSubSecretHint); got == was {
		t.Fatalf("the secret rotated and the hint did not: get would still name the old credential (%q)", got)
	}

	// NOTHING ELSE MOVES. rm+create rotated the secret and changed the id with it, which is
	// what this verb exists to avoid; the same argument covers every other column.
	for _, c := range []struct{ col, label string }{
		{evtColSubEndpoint, "endpoint"},
		{evtColSubName, "name"},
		{evtColSubTypes, "event types"},
		{evtColSubMaxAttempts, "max attempts"},
	} {
		if got, was := after.String(c.col), before.String(c.col); got != was {
			t.Fatalf("rotate-secret changed the %s: %q, was %q", c.label, got, was)
		}
	}

	var doc eventingSubscriptionSecretRotated
	if jerr := json.Unmarshal([]byte(out), &doc); jerr != nil {
		t.Fatalf("rotate-secret -o json is not valid JSON: %v\n%s", jerr, out)
	}
	if doc.Secret == "" || doc.Secret == sealed {
		t.Fatalf("the new secret must be returned in the clear exactly once, got %q", doc.Secret)
	}
	if doc.ID != id {
		t.Fatalf("rotate-secret reported id %q, want the SAME id %q — changing it is the rm+create defect", doc.ID, id)
	}
}

// TestEventingSubscriptionsRotateSecretIsDestructive — it must ask. Between this returning and
// the receiver being reconfigured every delivery fails its signature check, so an accidental
// rotation is an outage, and a verb that performs one silently is worse than no verb.
func TestEventingSubscriptionsRotateSecretIsDestructive(t *testing.T) {
	authorizeEgressHosts(t, "127.0.0.1")
	dir, tenant := seededTenantDataDir(t)
	id := seedSubscriptionRow(t, dir, tenant, "https://127.0.0.1:9443/hook", "sealed-secret-value")
	before := readSubscriptionRow(t, dir, tenant, id)

	out, err := runEventing(t, "subscriptions", "rotate-secret", "--tenant", tenant,
		"--data-dir", dir, "--id", id)
	if err == nil {
		t.Fatalf("rotate-secret without --yes must not proceed unattended:\n%s", out)
	}
	if got, was := readSubscriptionRow(t, dir, tenant, id).String(evtColSubSecret), before.String(evtColSubSecret); got != was {
		t.Fatal("the refused rotation still wrote a new secret — a guard that fires after the mutation is not a guard")
	}
}
