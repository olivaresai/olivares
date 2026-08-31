// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/grok/session"
	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk/event"
)

// El OBSERVADOR es el cable, y hasta hoy no lo probaba nada — ni el de Grok ni el de Codex. El
// módulo `connectors/grok/session/observations.go` tiene siete pruebas y todas miran las FUNCIONES:
// ninguna mira si el observador las LLAMA. Son cosas distintas, y la diferencia es la de mi propia
// nota: algo cableado puede estar verde y no publicar nada.
//
// Lo que este test fija es que las TRES puertas llegan al bus. `modules/sessions` pliega el edge de
// origen «session» y los findings de ciclo de vida y de negativa; si el observador emitiera sólo
// una, el decisor seguiría decidiendo bien y la sesión existiría a medias en la vista viva — que
// para un plano de gobierno es casi lo mismo que no gobernar.
func TestGrokObserverPublishesAllThreeGates(t *testing.T) {
	bus := eventbus.NewInProc(eventbus.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	t.Cleanup(func() { _ = bus.Close() })

	var mu sync.Mutex
	var vistos []event.Event
	if _, err := bus.Subscribe(nil, func(_ context.Context, e event.Event) error {
		mu.Lock()
		defer mu.Unlock()
		vistos = append(vistos, e)
		return nil
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	eng := &engine{bus: bus}
	obs := grokObserver(eng, model.TenantID("t-grok"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	at := time.Date(2026, 8, 19, 9, 0, 0, 0, time.UTC)

	// ⛔ HACEN FALTA VARIOS CASOS, y esto es el arreglo de un agujero que este mismo test tuvo: con
	// un solo PreToolUse denegado, quitar la emisión del CICLO DE VIDA no rompía nada — porque
	// `LifecycleFinding` sólo dispara en SessionStart/SessionEnd y mi caso no la abría NUNCA. El
	// número esperado lo deriva el test del propio módulo (para que no envejezca), y esa misma
	// derivación lo volvía ciego a la puerta que su entrada no tocaba. Un mutante superviviente.
	casos := []struct {
		nombre string
		req    session.Request
		dec    session.Decision
	}{
		{"arranque de sesión", session.Request{
			Event: session.EventSessionStart, ExternalSessionID: "grok-uuid",
			PermissionMode: "bypassPermissions", At: at,
		}, session.Decision{Verdict: session.VerdictAllow, SessionSID: "sid-1"}},
		{"llamada denegada e impuesta", session.Request{
			Event: session.EventPreToolUse, ExternalSessionID: "grok-uuid", Tool: "Bash",
			ResourceRef: "Bash", PermissionMode: "bypassPermissions", At: at,
		}, session.Decision{Verdict: session.VerdictDeny, Enforced: true, SessionSID: "sid-1", Reason: "regla"}},
	}

	// Cuántas puertas DEBE abrir cada caso, preguntado al módulo y no a mi memoria.
	abiertas := map[string]int{}
	total := 0
	for _, c := range casos {
		if _, ok := session.EdgeFor(c.req, c.dec); ok {
			abiertas["edge"]++
			total++
		}
		if _, ok := session.LifecycleFinding(c.req, c.dec); ok {
			abiertas["ciclo"]++
			total++
		}
		if _, ok := session.DenyFinding(c.req, c.dec); ok {
			abiertas["negativa"]++
			total++
		}
	}

	// ⭐ EL CONTROL POSITIVO DEL PROPIO TEST: si una puerta no se ejercita NI UNA VEZ, este test no
	// puede ver que el observador deje de emitirla. Sin esta guarda el agujero vuelve en silencio
	// la próxima vez que alguien cambie un caso.
	for _, puerta := range []string{"edge", "ciclo", "negativa"} {
		if abiertas[puerta] == 0 {
			t.Fatalf("ningún caso abre la puerta %q: este test no podría ver que el observador "+
				"dejara de publicarla — añade un caso que la dispare", puerta)
		}
	}

	for _, c := range casos {
		obs(c.req, c.dec)
	}

	esperar(t, &mu, &vistos, total)
	mu.Lock()
	defer mu.Unlock()
	if len(vistos) != total {
		t.Fatalf("el módulo abre %d puerta(s) (edge=%d ciclo=%d negativa=%d) y al bus llegaron %d: "+
			"el observador publica de menos", total, abiertas["edge"], abiertas["ciclo"],
			abiertas["negativa"], len(vistos))
	}
	for _, e := range vistos {
		if e.Tenant != "t-grok" {
			t.Fatalf("una observación salió con el tenant %q: la vista viva la plegaría en otro sitio", e.Tenant)
		}
	}
}

// Y el bus caído NO puede cambiar el veredicto: para cuando el observador corre, la decisión ya
// está tomada y anclada. Perder la fila es un hueco visible; retrasar al agente sería peor.
func TestGrokObserverSurvivesADeadBus(t *testing.T) {
	bus := eventbus.NewInProc(eventbus.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	_ = bus.Close()
	eng := &engine{bus: bus}
	obs := grokObserver(eng, model.TenantID("t-grok"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	req := session.Request{
		Event: session.EventPreToolUse, ExternalSessionID: "grok-uuid", Tool: "Bash",
		ResourceRef: "Bash", At: time.Date(2026, 8, 19, 9, 0, 0, 0, time.UTC),
	}
	obs(req, session.Decision{Verdict: session.VerdictDeny, Enforced: true, SessionSID: "sid-1"})
	// no explota, y no hay nada que afirmar salvo que volvemos con vida
}

// Y sin motor tampoco: `grok-hook` puede correr en un nodo sin plano, y ahí observar es imposible
// — pero eso NO puede tumbar la decisión.
func TestGrokObserverWithoutEngine(t *testing.T) {
	obs := grokObserver(nil, model.TenantID("t-grok"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	obs(session.Request{Event: session.EventPreToolUse, ExternalSessionID: "x", At: time.Now()},
		session.Decision{Verdict: session.VerdictAllow, SessionSID: "sid"})
}

func esperar(t *testing.T, mu *sync.Mutex, vistos *[]event.Event, n int) {
	t.Helper()
	// La entrega del bus es asíncrona: sin espera esto mediría la velocidad de la máquina.
	for i := 0; i < 200; i++ {
		mu.Lock()
		hay := len(*vistos)
		mu.Unlock()
		if hay >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}
