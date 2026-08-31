// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// The actor kind a WorkPrincipal carries is written straight into
// sessions_work_event.actor_kind, and that column has a CLOSED vocabulary enforced
// by a trigger. Nothing tied the Go side to the SQL side, so a principal whose kind
// the schema does not admit failed at INSERT -- a 500 at apply time, after validate
// and plan had both answered LIMPIO because neither of them writes.
//
// This reads the vocabulary out of the MIGRATION rather than restating it, so the
// two cannot drift apart in silence.
func TestWorkEventActorKindVocabularyAdmitsEveryKindWeProduce(t *testing.T) {
	const migration = "migrations/sqlite/0084_work_event_message_guard_ins.sql"
	raw, err := os.ReadFile(migration)
	if err != nil {
		t.Fatalf("NO HE PODIDO MIRAR: read %s: %v", migration, err)
	}
	m := regexp.MustCompile(`actor_kind NOT IN \(([^)]*)\)`).FindStringSubmatch(string(raw))
	if m == nil {
		t.Fatalf("NO HE PODIDO MIRAR: %s no longer states an actor_kind vocabulary; "+
			"either the guard moved or this regexp stopped matching it", migration)
	}
	admitted := map[string]bool{}
	for _, part := range strings.Split(m[1], ",") {
		admitted[strings.Trim(strings.TrimSpace(part), "'")] = true
	}
	// Control: the vocabulary must be non-trivial, or "everything is admitted" would
	// make this test pass by finding nothing.
	if len(admitted) < 2 {
		t.Fatalf("NO HE PODIDO MIRAR: parsed %d kinds from %q", len(admitted), m[1])
	}

	// ⛔ ESTO SE PIDE A LA FUNCION DE PRODUCCION, y no se construye a mano.
	//
	// La primera version de esta prueba escribia `WorkPrincipal{ActorKind:"session"}`
	// literalmente y comprobaba ese valor contra la migracion. **Habria pasado con la
	// rama de produccion retirada**: comprobaba su propia suposicion, no el codigo. Lo
	// senalo el contraste `sol max` del 2026-08-24 como hallazgo MEDIO, y es la clase
	// exacta que esta casa tiene medida — un verificador construido sobre la misma
	// suposicion que el codigo confirma lo que el codigo cree.
	const sid = "osn_01a03162-d0b3-7e98-8139-8da49bc8725a"
	credentialOfAConductedSession := auth.Principal{
		Kind: auth.KindToken, CredID: model.NewID(), SessionIdentity: sid,
	}
	sessionPrincipal, err := workPrincipalFromAuth(credentialOfAConductedSession, model.NewTenantID())
	if err != nil {
		t.Fatalf("NO HE PODIDO MIRAR: workPrincipalFromAuth: %v", err)
	}
	if sessionPrincipal.ActorRef != sid {
		t.Fatalf("ActorRef = %q, want the canonical SID %q: the event must name the session "+
			"that acted, not the id of its credential", sessionPrincipal.ActorRef, sid)
	}

	for _, kind := range []string{
		sessionPrincipal.ActorKind, "user", "agent", "system",
	} {
		if !admitted[kind] {
			t.Errorf("we produce actor_kind %q and %s admits only %v; every work event "+
				"written by such a caller is REFUSED by the trigger at INSERT, which surfaces as "+
				"a 500 only at apply", kind, migration, m[1])
		}
	}

	// THE NON-FIRING DIRECTION: "token" is what the principal used to carry, and the
	// schema must still NOT admit it. If a later change widened the vocabulary instead
	// of fixing the attribution, this test would otherwise go quiet about it -- and a
	// work event attributed to a credential id rather than to the session that acted
	// is exactly the attribution this fix exists to correct.
	if admitted["token"] {
		t.Errorf("%s now admits actor_kind \"token\". Widening the vocabulary is not the same "+
			"fix as attributing the act to the SESSION: an event that says a credential id did "+
			"the work cannot be read back as who did it", migration)
	}
}
