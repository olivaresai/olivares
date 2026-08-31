// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/grok/session"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// Se reutilizan los dobles del hermano —`recordingResolver`, `codexAuthenticator`— porque están
// en este mismo paquete y porque un segundo doble del mismo puerto es una segunda verdad que
// deriva. Lo que NO se reutiliza es el store: como allí, es un SQLite REAL. Las afirmaciones
// sobre lo que el registro hace con una reclamación repetida no las puede probar un mock que
// devuelva lo que le digamos.

type grokFixture struct {
	tenant model.TenantID
	dec    *grokHookDecider
	res    *recordingResolver
}

func newGrokFixture(t *testing.T) *grokFixture {
	t.Helper()
	ctx := context.Background()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate audit signing key: %v", err)
	}
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatalf("build audit signer: %v", err)
	}
	st, err := coreengine.Open(ctx, store.Config{
		Engine: store.EngineSQLite, DSN: ":memory:", SignEvent: signer.SignEvent,
	}, nil)
	if err != nil {
		t.Fatalf("open signed store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		org, err := sys.CreateOrg(ctx, model.Org{Name: "grok-hook", Slug: "grok-hook", Status: model.StatusActive})
		if err == nil {
			tenant = org.TenantID
		}
		return err
	}); err != nil {
		t.Fatalf("provision tenant: %v", err)
	}

	res := &recordingResolver{}
	dec := &grokHookDecider{
		tenant:   tenant,
		authr:    codexAuthenticator{principal: auth.ScopedPrincipal(model.NewID(), "grok-agent", tenant, auth.RoleEditor)},
		sessions: res,
		store:    st,
		clock:    func() time.Time { return time.Date(2026, 8, 19, 9, 0, 0, 0, time.UTC) },
		log:      discardLog(),
	}
	return &grokFixture{tenant: tenant, dec: dec, res: res}
}

func peticionGrok() session.Request {
	return session.Request{
		Event:             session.EventPreToolUse,
		ExternalSessionID: "grok-sesion-1",
		Tool:              "Bash",
		ResourceRef:       "Bash",
		PermissionMode:    "bypassPermissions",
		At:                time.Date(2026, 8, 19, 9, 0, 0, 0, time.UTC),
		PayloadDigest:     "digest-de-esta-llamada",
	}
}

// ⛔ SIN PRINCIPAL AUTENTICADO SE DENIEGA. La forma de saltarse la autorización no puede ser
// mandar NADA: sin pista, sin portador, y la llamada gobernada bajo el tenant de este punto como
// si perteneciera aquí.
func TestGrokSinPrincipalSeDeniega(t *testing.T) {
	t.Parallel()

	f := newGrokFixture(t)
	f.dec.authr = nil
	dec, err := f.dec.Decide(context.Background(), peticionGrok(), "")
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if dec.Verdict != session.VerdictDeny {
		t.Fatal("sin principal la llamada tiene que denegarse")
	}
	if f.res.calls != 0 {
		t.Fatal("no se puede tocar el plano de identidad antes de autorizar")
	}
}

// Una pista de tenant sólo puede CONFIRMAR el que este punto gobierna, nunca seleccionarlo.
func TestGrokLaPistaDeTenantNoPuedeRedirigir(t *testing.T) {
	t.Parallel()

	f := newGrokFixture(t)
	req := peticionGrok()
	req.Identity.Tenant = model.NewID().String()
	dec, err := f.dec.Decide(context.Background(), req, "token")
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if dec.Verdict != session.VerdictDeny {
		t.Fatalf("una pista que no es la de este punto tiene que denegar: %+v", dec)
	}
	// Y una pista AUSENTE es legítima: no confirma nada y no redirige nada.
	sinPista, err := f.dec.Decide(context.Background(), peticionGrok(), "token")
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if sinPista.Verdict != session.VerdictAllow {
		t.Fatalf("sin pista y con principal miembro se permite: %+v", sinPista)
	}
}

// ⛔ EL PROVEEDOR NO SALE NUNCA DEL PAYLOAD. Un proveedor que el llamante elige es un proveedor
// que el llamante puede suplantar, y toda la garantía de no colisión descansa en que forme parte
// de la clave del alias.
func TestGrokElProveedorNoSaleDelPayload(t *testing.T) {
	t.Parallel()

	f := newGrokFixture(t)
	if _, err := f.dec.Decide(context.Background(), peticionGrok(), "token"); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if f.res.calls != 1 {
		t.Fatalf("el plano de identidad tenía que consultarse una vez, %d", f.res.calls)
	}
	var visto string
	for k := range f.res.minted {
		visto = k
	}
	if !strings.HasPrefix(visto, sdkmodel.EngineGrok+":") {
		t.Fatalf("el alias tiene que ir bajo el proveedor grok, fue %q", visto)
	}
}

// Un fallo de identidad NO es licencia para seguir: sin sid la llamada no se puede atribuir,
// registrar ni auditar después.
func TestGrokUnFalloDeIdentidadDeniega(t *testing.T) {
	t.Parallel()

	f := newGrokFixture(t)
	f.res.err = errors.New("el plano de identidad no contesta")
	dec, err := f.dec.Decide(context.Background(), peticionGrok(), "token")
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if dec.Verdict != session.VerdictDeny {
		t.Fatal("sin identidad resuelta se deniega")
	}
	if dec.SessionSID != "" {
		t.Fatalf("no se puede inventar una sesión: %q", dec.SessionSID)
	}
}

// ⛔⛔ UN ALLOW QUE NO SE PUDO ANCLAR SE DEGRADA A DENY. Es la regla que hace que la evidencia no
// sea opcional: si el registro no puede recoger que permitimos algo, no lo permitimos.
func TestGrokUnAllowSinReciboSeDegrada(t *testing.T) {
	t.Parallel()

	f := newGrokFixture(t)
	f.dec.store = nil // el registro no está cableado
	dec, err := f.dec.Decide(context.Background(), peticionGrok(), "token")
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if dec.Verdict != session.VerdictDeny {
		t.Fatalf("un allow sin recibo tiene que degradarse: %+v", dec)
	}
	if !strings.Contains(dec.Reason, "evidence unavailable") {
		t.Fatalf("y tiene que decir por qué: %q", dec.Reason)
	}
	// El sid SE CONSERVA: la sesión existe aunque el registro flaqueara, y perderlo dejaría el
	// hecho sin dónde colgarse.
	if dec.SessionSID == "" {
		t.Fatal("degradar no puede perder la sesión")
	}
}

// ⛔ Y LA DIRECCIÓN CONTRARIA, que es la que se hace mal: un DENY sin recibo SE MANTIENE. Perder
// la evidencia es grave, pero debilitar el veredicto porque el registro flaqueó sería al revés.
func TestGrokUnDenySinReciboSeMantiene(t *testing.T) {
	t.Parallel()

	f := newGrokFixture(t)
	f.dec.store = nil
	f.dec.eval = evaluadorQueDeniega{}
	dec, err := f.dec.Decide(context.Background(), peticionGrok(), "token")
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if dec.Verdict != session.VerdictDeny {
		t.Fatal("un deny se mantiene")
	}
	if strings.Contains(dec.Reason, "evidence unavailable") {
		t.Fatalf("un deny de POLÍTICA no puede reescribirse como un fallo de evidencia: %q", dec.Reason)
	}
	if !strings.Contains(dec.Reason, "la política lo prohíbe") {
		t.Fatalf("la razón de la política tiene que sobrevivir: %q", dec.Reason)
	}
}

type evaluadorQueDeniega struct{}

func (evaluadorQueDeniega) Evaluate(context.Context, auth.Request) (auth.Decision, error) {
	return auth.Decision{Allow: false, Reason: "la política lo prohíbe"}, nil
}

// ⛔ LA ACCIÓN DEL REGISTRO ES DE GROK, NUNCA DE CODEX NI DE CLAUDE. Si dos motores escribieran
// la misma acción, sus decisiones serían indistinguibles para quien audita.
func TestGrokLaAccionDelRegistroEsSuya(t *testing.T) {
	t.Parallel()

	if got := engineLedgerAction(grokProfile, "allow", "deny"); got != "grok.hook.allow" {
		t.Fatalf("acción = %q, quiere grok.hook.allow", got)
	}
	if grokProfile.ActionRoot == codexProfile.ActionRoot {
		t.Fatal("los dos motores comparten raíz de acción")
	}
	for nombre, a := range map[string]string{
		"operación": grokProfile.OperationDomain,
		"efecto":    grokProfile.EffectDomain,
		"decisión":  grokProfile.DecisionDomain,
	} {
		for otro, b := range map[string]string{
			"operación": codexProfile.OperationDomain,
			"efecto":    codexProfile.EffectDomain,
			"decisión":  codexProfile.DecisionDomain,
		} {
			if a == b {
				t.Fatalf("el dominio de %s de grok es el mismo que el de %s de codex: %q", nombre, otro, a)
			}
		}
	}
}

// ⛔ EL VERBO DEL PDP NO SE ADIVINA. Grok no declara si una herramienta lee o escribe; derivarlo
// del nombre convertiría una suposición en el sujeto de una decisión de política — una regla
// escrita contra «:read» creería gatear lecturas y gatearía lo que nuestro adivino llamó así.
func TestGrokElVerboDelPdpNoSeAdivina(t *testing.T) {
	t.Parallel()

	f := newGrokFixture(t)
	espia := &evaluadorEspia{}
	f.dec.eval = espia
	if _, err := f.dec.Decide(context.Background(), peticionGrok(), "token"); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if espia.visto != "grok.tool.use:use" {
		t.Fatalf("permiso = %q, quiere grok.tool.use:use", espia.visto)
	}
	if strings.Contains(espia.visto, ":read") || strings.Contains(espia.visto, ":write") {
		t.Fatal("el verbo salió adivinado del modo, que Grok no declara")
	}
}

type evaluadorEspia struct{ visto string }

func (e *evaluadorEspia) Evaluate(_ context.Context, r auth.Request) (auth.Decision, error) {
	e.visto = string(r.Permission)
	return auth.Decision{Allow: true}, nil
}
