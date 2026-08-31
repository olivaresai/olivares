// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package sessions

import (
	"context"
	"testing"
)

// ⛔ EL CONJUNTO DE PROVEEDORES ES ABIERTO, Y ESO HAY QUE PROBARLO, NO DEDUCIRLO DE UN COMENTARIO.
//
// `SessionBinding.Provider` es una cadena libre normalizada a minúsculas: no hay validación
// contra una lista. La consecuencia buena es que un motor nuevo —Grok Build entró como TIER 1 el
// 2026-08-17— no necesita tocar este módulo. La consecuencia peligrosa es que nada impide que dos
// proveedores compartan una identidad si la clave estuviera mal formada.
//
// Esta celda fija las dos mitades: que un TERCER proveedor resuelve, y que el MISMO id externo
// bajo dos proveedores da DOS sesiones distintas. Si colisionaran, una sesión de Grok podría
// resolver a la identidad de una de Codex — que es precisamente la colisión que este plano existe
// para impedir.
func TestIdentity_UnTercerProveedorResuelveYNoColisiona(t *testing.T) {
	t.Parallel()

	m, _, tenant, _ := newSess(t)
	ctx := context.Background()

	const mismoID = "id-compartido-entre-motores"

	grok, err := m.ResolveSession(ctx, tenant, SessionBinding{
		Provider: "grok", ExternalID: mismoID, Origin: OriginObserved, At: baseTime,
	})
	if err != nil {
		t.Fatalf("un proveedor que este módulo no enumera tiene que resolver igual: %v", err)
	}
	if grok == "" {
		t.Fatal("resolvió a una sesión vacía")
	}

	codex, err := m.ResolveSession(ctx, tenant, SessionBinding{
		Provider: "codex", ExternalID: mismoID, Origin: OriginObserved, At: baseTime,
	})
	if err != nil {
		t.Fatalf("resolve codex: %v", err)
	}

	if grok == codex {
		t.Fatalf("el mismo id externo bajo DOS proveedores resolvió a la MISMA sesión (%q): "+
			"una sesión de un motor podría secuestrar la identidad de otro", grok)
	}

	// Y la normalización sigue siendo normalización, no un segundo proveedor: «Grok» es «grok».
	otra, err := m.ResolveSession(ctx, tenant, SessionBinding{
		Provider: "Grok", ExternalID: mismoID, Origin: OriginObserved, At: baseTime,
	})
	if err != nil {
		t.Fatalf("resolve Grok: %v", err)
	}
	if otra != grok {
		t.Fatalf("«Grok» y «grok» dieron dos identidades (%q vs %q): la caja no es un proveedor", otra, grok)
	}
}
