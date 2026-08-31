// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/store"
)

// Re-running `serve --seed-demo` against a data dir that already holds the demo
// estate is an ORDINARY operator mistake, and the engine does the right thing: it
// refuses rather than half-seeding. What it did badly was say so in the store's
// language. Measured on 2026-08-24:
//
//	Error: seed demo estate: create demo org: version conflict: constraint failed:
//	UNIQUE constraint failed: orgs.slug (2067)
//
// Whoever reads that does not learn the answer is "use another --data-dir"; they
// learn something broke and has a number. This pins the message that tells them.
func TestReseedingAnAlreadySeededDataDirSaysWhatToDo(t *testing.T) {
	ctx := context.Background()
	st, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, nil)
	if err != nil {
		t.Fatalf("NO HE PODIDO MIRAR: open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error {
		_, e := sys.EnsureSystemTenant(ctx)
		return e
	}); err != nil {
		t.Fatalf("NO HE PODIDO MIRAR: system tenant: %v", err)
	}

	// ⛔ ESTA GUARDA MIRABA LA CADENA EQUIVOCADA Y POR ESO NUNCA DISPARABA.
	//
	// Comprobaba `strings.Contains(err, "create demo org")`, pero en este fixture el primer
	// sembrado falla MAS TARDE — en los read models, porque aqui no hay runtime ni modulos
	// registrados — asi que la guarda no casaba nunca y el test seguia contra un store
	// PARCIALMENTE sembrado sin decirlo. Y quedo doblemente rancia: tras el arreglo la ruta
	// de conflicto ya no contiene esa cadena tampoco. Lo cazo una revision sobre mi diff.
	//
	// Lo que este test necesita del primer sembrado es UNA cosa —que la org exista— asi que
	// eso es lo que se comprueba, con el mismo ayudante que usa produccion. Que la siembra
	// posterior falle en este fixture es ESPERADO y se dice en voz alta, no se traga.
	if _, err := seedDemoEstate(ctx, st, nil, time.Now().UTC()); err != nil {
		t.Logf("primer sembrado incompleto, esperado en este fixture (sin runtime ni modulos): %v", err)
	}
	if exists, err := demoOrgExists(ctx, st); err != nil {
		t.Fatalf("NO HE PODIDO MIRAR si la org demo existe tras el primer sembrado: %v", err)
	} else if !exists {
		t.Fatalf("el primer sembrado no dejo la org demo, asi que este test no tiene sujeto")
	}

	// Second seed on the same store: this is the case an operator hits.
	_, err = seedDemoEstate(ctx, st, nil, time.Now().UTC())
	if err == nil {
		t.Fatal("re-seeding an already-seeded store SUCCEEDED; it must refuse rather than " +
			"half-seed on top of an existing estate")
	}
	msg := err.Error()

	// 1 · it still IS the same wrapped conflict -- nothing is swallowed.
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("the refusal no longer wraps store.ErrConflict, so callers that classify it "+
			"lose the ability: %v", err)
	}
	// 2 · and it names the remedy, which is the whole point.
	// El mensaje ya no promete que no haya media siembra —no puede— y en cambio AVISA de
	// que puede haberla. Eso es lo que se fija aqui.
	for _, want := range []string{"already has the demo org", "INCOMPLETE", "--data-dir"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("the refusal does not say %q, so the operator is told something broke and "+
				"not what to do.\n  got: %s", want, msg)
		}
	}
}
