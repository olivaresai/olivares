// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md

package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// ⛔ QUÉ DEFECTO FIJA ESTA BATERÍA. `groupRoster` pedía `Limit: 1000` de una sola vez y ataba el
// `Page` a `_`, y las CUATRO operaciones de grupo compartían esa lista recortada. Por encima de mil
// miembros eso producía cuatro fallos distintos, y el peor no era de lectura:
//
//	· alta   — la idempotencia buscaba dentro del corte ⇒ DUPLICADO si el miembro caía fuera
//	· baja   — igual ⇒ 404 sobre un miembro que SÍ existe
//	· listar — `has_more:false` sobre una lista cortada: la consola no podía desmentirlo
//	· borrar — borraba lo que veía y dejaba el resto HUÉRFANO: corrupción silenciosa
//
// Por eso los casos siembran POR ENCIMA del corte: por debajo, el código defectuoso pasa.
const rosterCut = 1000

// sembrarMiembros inserta n filas de pertenencia directamente en el almacén.
//
// Directamente y no por HTTP: mil altas por la API tardarían minutos y probarían el alta, no el
// corte. Lo que hace falta aquí es un grupo GRANDE, y la vía honesta de construirlo es el almacén.
func sembrarMiembros(t *testing.T, h *harness, tenant model.TenantID, groupID model.ID, n int) []model.ID {
	t.Helper()
	agentes := make([]model.ID, 0, n)
	err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		for i := 0; i < n; i++ {
			// ⛔ EL AGENTE SE CREA DE VERDAD. La primera versión sembraba pertenencias con ids
			//    sintéticos, y el alta respondía 404 porque el handler valida que el agente exista
			//    -- correctamente. Un fixture que no construye el mundo que el sujeto exige mide
			//    otra cosa: allí habría dado el fallo por el motivo equivocado.
			ag, err := sc.Agents().Create(context.Background(), model.Agent{
				ExternalID: "seed-" + model.NewID().String(),
				Name:       "seed",
			})
			if err != nil {
				return err
			}
			if _, err := sc.AgentGroupMembers().Create(context.Background(), model.AgentGroupMember{
				GroupID: groupID, AgentID: ag.ID,
			}); err != nil {
				return err
			}
			agentes = append(agentes, ag.ID)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("sembrando %d miembros: %v", n, err)
	}
	return agentes
}

func grupoConMiembros(t *testing.T, h *harness, n int) (string, model.TenantID, string, []model.ID) {
	t.Helper()
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	r := h.do("POST", "/v1/agent-groups", admin,
		map[string]any{"name": "big", "slug": "big"}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("create group = %d %s", r.code, r.raw)
	}
	groupID := r.body["id"].(string)
	agentes := sembrarMiembros(t, h, tenant, model.ID(groupID), n)
	return admin, tenant, groupID, agentes
}

// TestGroupRosterListDeclaresItsPage pins that the listing paginates and SAYS so.
func TestGroupRosterListDeclaresItsPage(t *testing.T) {
	h := newHarness(t)
	admin, tenant, groupID, _ := grupoConMiembros(t, h, 3)

	// Dirección NO disparadora primero: sin recorte, `has_more` es false. Sin esta mitad, un
	// handler que devolviera `true` fijo satisfaría el otro caso él solo.
	r := h.do("GET", "/v1/agent-groups/"+groupID+"/members", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("list = %d %s", r.code, r.raw)
	}
	if got := r.body["has_more"]; got != false {
		t.Errorf("una lista completa dice has_more = %v, want false", got)
	}

	// Y con una página pedida más pequeña que el grupo, lo DECLARA.
	r = h.do("GET", "/v1/agent-groups/"+groupID+"/members?limit=2", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("list?limit=2 = %d %s", r.code, r.raw)
	}
	if got := r.body["has_more"]; got != true {
		t.Errorf("una lista recortada dice has_more = %v, want true", got)
	}
	filasDevueltas, _ := r.body["items"].([]any)
	if n := len(filasDevueltas); n != 2 {
		t.Errorf("devolvió %d filas con limit=2", n)
	}
}

// TestGroupMemberOpsBeyondTheCut is the one the old helper could not pass.
func TestGroupMemberOpsBeyondTheCut(t *testing.T) {
	h := newHarness(t)
	admin, tenant, groupID, agentes := grupoConMiembros(t, h, rosterCut+5)
	// El último sembrado está MÁS ALLÁ del corte de mil que usaba el helper viejo.
	lejano := agentes[len(agentes)-1].String()

	// (a) ALTA idempotente: no debe crear un duplicado por no haberlo visto.
	r := h.do("PUT", "/v1/agent-groups/"+groupID+"/members/"+lejano, admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("alta de un miembro existente = %d (want 200 idempotente) %s", r.code, r.raw)
	}

	// Y se comprueba en el ALMACÉN que sigue habiendo UNA sola fila: el 200 por sí solo no
	// distingue «lo encontré» de «lo creé y devolví algo».
	filas := 0
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		rows, _, err := sc.AgentGroupMembers().List(context.Background(), model.Query{
			Filters: []model.Filter{
				{Column: "group_id", Op: model.OpEq, Value: groupID},
				{Column: "agent_id", Op: model.OpEq, Value: lejano},
			},
			Limit: 10,
		})
		filas = len(rows)
		return err
	}); err != nil {
		t.Fatalf("contando filas: %v", err)
	}
	if filas != 1 {
		t.Errorf("hay %d filas de pertenencia para el mismo par, want 1 (duplicado)", filas)
	}

	// (b) BAJA de ese mismo miembro lejano: existe, así que no puede ser 404.
	r = h.do("DELETE", "/v1/agent-groups/"+groupID+"/members/"+lejano, admin, nil, tenantHdr(tenant))
	if r.code != http.StatusNoContent {
		t.Fatalf("baja de un miembro real = %d (want 204) %s", r.code, r.raw)
	}
}

// TestDeleteGroupLeavesNoOrphanRows is the worst of the four: silent corruption.
func TestDeleteGroupLeavesNoOrphanRows(t *testing.T) {
	h := newHarness(t)
	admin, tenant, groupID, _ := grupoConMiembros(t, h, rosterCut+5)

	if r := h.do("DELETE", "/v1/agent-groups/"+groupID, admin, nil, tenantHdr(tenant)); r.code != http.StatusNoContent {
		t.Fatalf("delete group = %d %s", r.code, r.raw)
	}

	// El veredicto se toma en el ALMACÉN, no en el código de estado: un borrado parcial también
	// responde 204. Lo que distingue uno de otro son las filas que quedan.
	quedan := 0
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		rows, _, err := sc.AgentGroupMembers().List(context.Background(), model.Query{
			Filters: []model.Filter{{Column: "group_id", Op: model.OpEq, Value: groupID}},
			Limit:   rosterCut + 100,
		})
		quedan = len(rows)
		return err
	}); err != nil {
		t.Fatalf("contando huérfanas: %v", err)
	}
	if quedan != 0 {
		t.Errorf("quedaron %d filas apuntando a un grupo borrado (huérfanas)", quedan)
	}
}
