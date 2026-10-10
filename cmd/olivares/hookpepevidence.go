// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

// hookpepevidence.go shares evidence handling between the Codex and Grok hook PEPs.
// Extracting it from codexhookpep.go preserved the three hash domains, the separation
// of operations from effects, and ledger error handling.
//
// Sharing rather than copying preserves three corrections: key by external session
// ID rather than resolved SID; use the payload digest when no call ID exists; and
// encode what an entry IS in its role, never what its verdict WAS. Separate copies
// could drift when one changes. hookGovernanceProfile supplies each engine's identity.
//
// Claude's `hookEvidenceBinding` in `modules/sessions/hookpep/claudehookpep.go` uses
// a different idempotency contract. Its per-decision nonce
// (`hookpep.MetaDecisionAttemptID`) is shared by a decision and its compensation to
// deduplicate an internal double-write after an ambiguous commit. Codex keys the call
// by alias, event, discriminator and role, independently of its outcome, to deduplicate
// a hook redelivered by the agent. Unifying them would change the ledger contract;
// this core is shared by Codex/Grok while Claude keeps its own implementation.
//
// Hash domains and ActionRoot remain in each engine's profile. Sharing them would
// make different engines' decisions indistinguishable in the ledger, which
// TestLedgerActionIsCodexNotClaude prevents.

// hookFact es la vista NEUTRA de una llamada gobernada y su respuesta. Existe porque cada motor
// tiene su propio paquete de sesión con sus propios tipos, y el núcleo de evidencia no puede
// depender de ninguno de los dos sin arrastrar al otro.
//
// Los campos que un motor no tenga van vacíos, y eso es correcto: el hash es de longitud
// prefijada, así que un campo ausente no puede confundirse con otro corrido.
type engineFact struct {
	Event string
	Tool  string
	// Discriminator distingue dos llamadas del MISMO evento en la MISMA sesión, y va SÓLO en la
	// clave de operación. Es el id de llamada del motor cuando lo hay, y el digest del payload
	// cuando no — los eventos de ciclo de vida no traen id, y un fallback más pobre colapsaría
	// todos los arranques de una sesión en una sola clave.
	Discriminator string
	// CallID es el id de llamada CRUDO, sin el fallback, y va sólo en el hash de PETICIÓN.
	//
	// ⛔ Son dos campos y no uno a propósito. El original usa el valor CON fallback para la
	//    clave de operación y el valor SIN fallback para describir la petición, y confundirlos
	//    cambia los digests de todos los eventos de ciclo de vida —los que no traen id— sin que
	//    ninguna prueba de forma lo note: seguirían siendo hashes válidos, sólo que otros. Un
	//    registro cuyos digests cambian de valor sin cambiar de significado rompe la detección
	//    de rebind hacia atrás.
	CallID string
	// ExternalSessionID es el id del motor, NO el sid resuelto. Va así a propósito: los merges
	// de identidad son explícitos y auditados, y tras uno el mismo alias resuelve al GANADOR;
	// clavar la clave en el sid daría dos ids a un mismo hecho a través de un merge.
	ExternalSessionID string
	ResourceKind      string
	ResourceRef       string
	Mode              string
	Model             string
	PermissionMode    string
	PayloadDigest     string
	Verdict           string
	Reason            string
	PolicyVersion     string
}

// engineEvidenceBinding deriva el par {OperationID, EffectDigest}.
//
// OperationID identifica QUÉ acto gobernado es esto; EffectDigest describe QUÉ se hizo al
// respecto. El almacén lo exige: la misma operación con otro efecto es un `ErrEvidenceRebind` y
// no una segunda entrada silenciosa. Por eso el OperationID no lleva NINGUNA parte del resultado.
//
// El EffectDigest deliberadamente NO lleva el sid resuelto: ése es nuestro nombre interno de la
// sesión y SE MUEVE cuando dos identidades se fusionan; un digest que se moviera con él haría que
// el reintento de un ALLOW legítimo tras un merge se leyera como rebind, y la regla de
// «allow sin recibo se degrada» fabricaría un DENY a partir de un evento de contabilidad.
func engineEvidenceBinding(p hookGovernanceProfile, tenant model.TenantID, f engineFact, actor actorRef, role string) sdk.EvidenceBinding {
	alias := p.Provider + ":" + f.ExternalSessionID
	op := engineHash(p.OperationDomain, tenant.String(), alias, f.Event, f.Discriminator, role)

	request := engineHash(p.DecisionDomain,
		f.Event, f.Tool, f.CallID, f.ResourceKind, f.ResourceRef, f.Mode,
		f.Model, f.PermissionMode, f.PayloadDigest)
	effect := engineHash(p.EffectDomain, tenant.String(), p.Surface, role,
		request, f.Verdict, f.Reason, actor.name, actor.kind, f.PolicyVersion)

	return sdk.EvidenceBinding{
		OperationID:  sdk.OperationID(op),
		EffectDigest: sdk.EffectDigest(effect),
	}
}

// engineLedgerAction es el verbo del registro: «<raíz>.<veredicto>». La raíz va por motor para que
// dos motores nunca escriban la misma acción. Un veredicto vacío se trata como DENY: no saber lo
// que se decidió no puede registrarse como un permiso.
func engineLedgerAction(p hookGovernanceProfile, verdict, denyName string) string {
	if verdict == "" {
		verdict = denyName
	}
	return p.ActionRoot + "." + verdict
}

// engineHash lleva la longitud delante para que ("ab","c") y ("a","bc") no puedan producir el mismo
// digest.
func engineHash(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprintf(h, "%d:%s|", len(p), p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// engineClassifyStoreFault mapea un error de transacción sobre la taxonomía de fallos de evidencia.
// Un error NO reconocido no es «sin fallo»: se clasifica como fallo de escritura, de modo que el
// recibo rechace en vez de suponer que el registro está bien.
func engineClassifyStoreFault(err error) sdk.EvidenceFault {
	switch {
	case err == nil:
		return sdk.EvidenceFaultNone
	case errors.Is(err, store.ErrAuditSpoolFull):
		return sdk.EvidenceFaultSpoolFull
	case errors.Is(err, store.ErrNotLeader):
		return sdk.EvidenceFaultLedgerUnavailable
	default:
		return sdk.EvidenceFaultWriteError
	}
}
