// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

// ⛔⛔ ESTAS CELDAS NACEN DE UNA MEDIDA, NO DE UNA SOSPECHA.
//
// Al extraer este núcleo de `codexhookpep.go` corrí su batería —22 casos, todos verdes antes y
// después— y luego comprobé si ESA batería vigilaba de verdad lo extraído. **Tres de cuatro
// mutaciones sobrevivieron**: confundir `CallID` con `Discriminator`, quitarle el prefijo de
// longitud al hash y dejar que un veredicto vacío no cayera a deny. Sólo se cazó la cuarta
// (quitar el ROL de la clave de operación).
//
// Un verde de 22 casos que no cubre tres de las cuatro propiedades que acabas de mover es
// exactamente una sonda que contesta lo mismo para cualquier entrada. Estas celdas cierran el
// hueco, y ahora protegen a los DOS motores en vez de a ninguno.

var perfilDePrueba = hookGovernanceProfile{
	Provider:        "prueba",
	Surface:         "prueba-pep",
	Capability:      "prueba.tool.use",
	ActionRoot:      "prueba.hook",
	OperationDomain: "olivares.prueba.operation.v1",
	EffectDomain:    "olivares.prueba.effect.v1",
	DecisionDomain:  "olivares.prueba.decision.v1",
}

// El prefijo de longitud es lo que impide que ("ab","c") y ("a","bc") den el mismo digest. Sin
// él, dos hechos DISTINTOS colapsan en una clave y el registro pierde uno sin decirlo.
func TestEngineHashNoColisionaAlMoverUnLimite(t *testing.T) {
	t.Parallel()

	if engineHash("ab", "c") == engineHash("a", "bc") {
		t.Fatal("dos entradas distintas dan el mismo hash: falta el prefijo de longitud")
	}
	if engineHash("ab", "c") != engineHash("ab", "c") {
		t.Fatal("el hash no es estable")
	}
	// Y una cadena vacía no es la ausencia del campo.
	if engineHash("") == engineHash() {
		t.Fatal("un campo vacío no puede valer lo mismo que un campo que no está")
	}
}

// ⛔ `CallID` y `Discriminator` NO son el mismo valor, y ésta es la celda que lo fija.
//
// El discriminador lleva el fallback al digest del payload y va SÓLO en la clave de operación; el
// id de llamada va crudo y SÓLO en el hash de petición. Confundirlos cambia los digests de todos
// los eventos de ciclo de vida —los que no traen id de llamada— sin dejar de ser hashes válidos,
// que es la clase de cambio que ninguna prueba de forma detecta y que rompe hacia atrás la
// detección de rebind.
func TestElIdDeLlamadaYElDiscriminadorNoSeConfunden(t *testing.T) {
	t.Parallel()

	tenant := model.TenantID("t-1")
	actor := actorRef{name: "a", kind: "system"}

	// Un evento de ciclo de vida: sin id de llamada, el discriminador cae al digest del payload.
	ciclo := engineFact{
		Event: "session_start", ExternalSessionID: "s-1",
		Discriminator: "digest-del-payload", CallID: "",
		PayloadDigest: "digest-del-payload", Verdict: "allow",
	}
	// El MISMO hecho si alguien confundiera los dos campos.
	confundido := ciclo
	confundido.CallID = ciclo.Discriminator

	a := engineEvidenceBinding(perfilDePrueba, tenant, ciclo, actor, "decision")
	b := engineEvidenceBinding(perfilDePrueba, tenant, confundido, actor, "decision")

	if a.OperationID != b.OperationID {
		t.Fatal("el id de llamada NO puede entrar en la clave de operación")
	}
	if a.EffectDigest == b.EffectDigest {
		t.Fatal("el id de llamada SÍ describe la petición: confundirlo con el discriminador " +
			"deja el digest igual y el cambio pasa desapercibido")
	}
}

// El ROL dice lo que una entrada ES —la decisión, o su compensación— y tiene que separar las
// claves. Si no, la compensación colisiona con la decisión que compensa.
func TestElRolSeparaLaDecisionDeSuCompensacion(t *testing.T) {
	t.Parallel()

	f := engineFact{Event: "pre_tool_use", ExternalSessionID: "s-1", Discriminator: "d", Verdict: "deny"}
	dec := engineEvidenceBinding(perfilDePrueba, model.TenantID("t-1"), f, actorRef{}, "decision")
	comp := engineEvidenceBinding(perfilDePrueba, model.TenantID("t-1"), f, actorRef{}, "compensating-downgrade")
	if dec.OperationID == comp.OperationID {
		t.Fatal("la compensación colisiona con la decisión que compensa")
	}
}

// Dos motores no pueden escribir la misma acción ni la misma clave: si lo hicieran, sus
// decisiones serían indistinguibles en el registro.
func TestDosMotoresNoColisionanEntreSi(t *testing.T) {
	t.Parallel()

	f := engineFact{Event: "pre_tool_use", ExternalSessionID: "mismo-id", Discriminator: "d", Verdict: "deny"}

	// ⛔ SÓLO CAMBIA EL PROVEEDOR, y ése es el punto. La primera versión de esta celda variaba
	//    el proveedor Y el dominio de hash a la vez, así que la aserción se cumplía por el
	//    dominio y el proveedor no era portante: quitarlo del alias sobrevivía a la prueba. Es
	//    la trampa del OR cuyas dos ramas una fixture satisface siempre por la misma.
	soloProveedor := perfilDePrueba
	soloProveedor.Provider = "otro"
	a := engineEvidenceBinding(perfilDePrueba, model.TenantID("t-1"), f, actorRef{}, "decision")
	b := engineEvidenceBinding(soloProveedor, model.TenantID("t-1"), f, actorRef{}, "decision")
	if a.OperationID == b.OperationID {
		t.Fatal("el MISMO id externo bajo dos proveedores dio la misma operación: el alias no " +
			"lleva el motor, y una reentrega de un motor colisionaría con la del otro")
	}

	// Y la segunda defensa, medida por separado: el dominio de hash también separa.
	soloDominio := perfilDePrueba
	soloDominio.OperationDomain = "olivares.otro.operation.v1"
	c := engineEvidenceBinding(soloDominio, model.TenantID("t-1"), f, actorRef{}, "decision")
	if a.OperationID == c.OperationID {
		t.Fatal("el dominio de hash no separa: la segunda defensa no existe")
	}

	// Y la acción del registro, que es lo que un auditor lee.
	soloRaiz := perfilDePrueba
	soloRaiz.ActionRoot = "otro.hook"
	if engineLedgerAction(perfilDePrueba, "deny", "deny") == engineLedgerAction(soloRaiz, "deny", "deny") {
		t.Fatal("dos motores escriben la misma acción en el registro")
	}
}

// ⛔ Un veredicto VACÍO se registra como DENY. No saber lo que se decidió no puede escribirse
// como un permiso: sería la única forma de que un fallo de lectura acabe pareciendo una
// autorización.
func TestUnVeredictoVacioSeRegistraComoDeny(t *testing.T) {
	t.Parallel()

	if got := engineLedgerAction(perfilDePrueba, "", "deny"); got != "prueba.hook.deny" {
		t.Fatalf("un veredicto vacío tiene que registrarse como deny, salió %q", got)
	}
	// Control positivo: un veredicto presente NO se sustituye.
	if got := engineLedgerAction(perfilDePrueba, "allow", "deny"); got != "prueba.hook.allow" {
		t.Fatalf("un veredicto presente manda, salió %q", got)
	}
}

// Un error del almacén que no reconocemos NO es «sin fallo»: se clasifica como fallo de
// escritura, para que el recibo rechace en vez de suponer que el registro está bien.
func TestUnErrorDesconocidoDelAlmacenNoEsAusenciaDeFallo(t *testing.T) {
	t.Parallel()

	casos := map[error]sdk.EvidenceFault{
		nil:                                   sdk.EvidenceFaultNone,
		store.ErrAuditSpoolFull:               sdk.EvidenceFaultSpoolFull,
		store.ErrNotLeader:                    sdk.EvidenceFaultLedgerUnavailable,
		errors.New("algo que nadie ha visto"): sdk.EvidenceFaultWriteError,
	}
	for err, quiere := range casos {
		if got := engineClassifyStoreFault(err); got != quiere {
			t.Fatalf("%v → %q, quiere %q", err, got, quiere)
		}
	}
}
