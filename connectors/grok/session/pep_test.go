// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

type decisorFalso struct {
	dec    Decision
	err    error
	visto  Request
	bearer string
	veces  int
}

func (d *decisorFalso) Decide(_ context.Context, req Request, bearer string) (Decision, error) {
	d.veces++
	d.visto = req
	d.bearer = bearer
	return d.dec, d.err
}

func postear(t *testing.T, p *PEP, cuerpo string, cabeceras map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/hook", strings.NewReader(cuerpo))
	for k, v := range cabeceras {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	return w
}

const cuerpoOK = `{"hookEventName":"pre_tool_use","sessionId":"s-1","toolName":"Bash",` +
	`"workspaceRoot":"/w","cwd":"/w","permissionMode":"bypassPermissions",` +
	`"toolInput":{"command":"curl -H 'Authorization: Bearer SECRETO-QUE-NO-DEBE-VIAJAR'"}}`

// ⛔ SIN DECISOR SE DENIEGA, y es la celda que más vale de todas: un PEP que dejase pasar
// mientras no hay nada cableado sería una puerta abierta que además parece gobernada.
func TestSinDecisorCableadoSeDeniega(t *testing.T) {
	t.Parallel()

	p := NewPEP(nil, nil, nil, nil)
	w := postear(t, p, cuerpoOK, nil)
	if !strings.Contains(w.Body.String(), "deny") {
		t.Fatalf("sin decisor el veredicto tiene que ser deny: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "no governed decider is wired (deny-closed)") {
		t.Fatalf("la razón deny-closed debe salir en inglés: %s", w.Body.String())
	}
}

// Un evento que este conector no conoce NO adquiere un camino no gobernado por el hecho de que
// Grok lo añada en una versión futura.
func TestUnEventoDesconocidoSeDeniega(t *testing.T) {
	t.Parallel()

	d := &decisorFalso{dec: Decision{Verdict: VerdictAllow, SessionSID: "sid"}}
	p := NewPEP(d, nil, nil, nil)
	w := postear(t, p, `{"hookEventName":"inventado_manana","sessionId":"s-1"}`, nil)
	if !strings.Contains(w.Body.String(), "deny") {
		t.Fatalf("un evento desconocido tiene que denegarse: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "unknown hook event (deny-closed)") {
		t.Fatalf("la razón deny-closed debe salir en inglés: %s", w.Body.String())
	}
	if d.veces != 0 {
		t.Fatal("un evento desconocido no debe llegar siquiera al decisor")
	}
}

// ⛔ EL CUERPO TRUNCADO ES LA MALFORMACIÓN MÁS PROBABLE, y la que prueba que la segunda vía de
// Grok —`GROK_HOOK_EVENT` en el entorno— está de verdad conectada aquí. Sin ella el deny se
// emitiría en la forma más estricta por no saber de qué evento se trata.
func TestUnCuerpoTruncadoRecuperaElEventoDelEntorno(t *testing.T) {
	t.Parallel()

	env := func(k string) string {
		if k == EnvHookEvent {
			return "pre_tool_use"
		}
		return ""
	}
	p := NewPEP(nil, nil, nil, env)
	w := postear(t, p, `{"hookEventName":"pre_tool_`, nil)
	cuerpo := w.Body.String()
	if !strings.Contains(cuerpo, "deny") {
		t.Fatalf("un cuerpo truncado se deniega: %s", cuerpo)
	}
	if !strings.Contains(cuerpo, "unreadable hook payload (deny-closed)") {
		t.Fatalf("la razón deny-closed debe salir en inglés: %s", cuerpo)
	}
	// Y la dirección que importa: SIN entorno el evento no se recupera, así que la respuesta
	// no puede ser la misma. Si lo fuera, esta celda no estaría midiendo el entorno.
	sinEnv := NewPEP(nil, nil, nil, nil)
	w2 := postear(t, sinEnv, `{"hookEventName":"pre_tool_`, nil)
	if w2.Body.String() == cuerpo {
		t.Fatal("con y sin entorno sale lo mismo: la segunda vía no está conectada")
	}
}

// ⛔ EL SECRETO DEL `toolInput` NO PUEDE VIAJAR. Es el campo donde vive —un token en un argumento
// de shell— y `ResourceRef` acaba en un registro que se audita.
func TestElInputCrudoNoViajaAlDecisor(t *testing.T) {
	t.Parallel()

	d := &decisorFalso{dec: Decision{Verdict: VerdictAllow, SessionSID: "sid"}}
	p := NewPEP(d, nil, nil, nil)
	postear(t, p, cuerpoOK, nil)
	if d.veces != 1 {
		t.Fatalf("el decisor tenía que ver la llamada, lo vio %d veces", d.veces)
	}
	for campo, v := range map[string]string{
		"ResourceRef":    d.visto.ResourceRef,
		"Tool":           d.visto.Tool,
		"WorkspaceRoot":  d.visto.WorkspaceRoot,
		"Cwd":            d.visto.Cwd,
		"PermissionMode": d.visto.PermissionMode,
	} {
		if strings.Contains(v, "SECRETO-QUE-NO-DEBE-VIAJAR") {
			t.Fatalf("%s lleva el secreto del toolInput: %q", campo, v)
		}
	}
	// Control positivo: la vista NO está vacía, o la comprobación de arriba pasaría sola.
	if d.visto.Tool != "Bash" || d.visto.PermissionMode != "bypassPermissions" {
		t.Fatalf("la vista mínima perdió campos que sí debe llevar: %+v", d.visto)
	}
}

// Un decisor que falla es un DENY, no un 500 ni un pase.
func TestUnDecisorQueFallaEsDeny(t *testing.T) {
	t.Parallel()

	d := &decisorFalso{err: errors.New("el PDP no contesta")}
	p := NewPEP(d, nil, nil, nil)
	w := postear(t, p, cuerpoOK, nil)
	if !strings.Contains(w.Body.String(), "deny") {
		t.Fatalf("un decisor que falla deniega: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "governed decision failed (deny-closed)") {
		t.Fatalf("la razón deny-closed debe salir en inglés: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "el PDP no contesta") {
		t.Fatal("el error interno del decisor no puede viajar al agente")
	}
}

// Sin sesión canónica NO se emite: una fila con referencia vacía la descarta la vista viva y
// parecería un hecho entregado.
func TestSinSesionCanonicaNoSeEmite(t *testing.T) {
	t.Parallel()

	var emitidos int
	obs := func(Request, Decision) { emitidos++ }

	conSesion := &decisorFalso{dec: Decision{Verdict: VerdictAllow, SessionSID: "sid"}}
	postear(t, NewPEP(conSesion, obs, nil, nil), cuerpoOK, nil)
	if emitidos != 1 {
		t.Fatalf("con sesión resuelta se emite una vez, se emitieron %d", emitidos)
	}

	emitidos = 0
	sinSesion := &decisorFalso{dec: Decision{Verdict: VerdictAllow}}
	w := postear(t, NewPEP(sinSesion, obs, nil, nil), cuerpoOK, nil)
	if emitidos != 0 {
		t.Fatalf("sin sesión resuelta no se emite nada, se emitieron %d", emitidos)
	}
	// Y aun así el agente RECIBE su veredicto: no contestarle sería peor que denegarle.
	if !strings.Contains(w.Body.String(), "\"verdict\"") {
		t.Fatalf("al agente hay que contestarle aunque no se emita el hecho: %s", w.Body.String())
	}
}

// ⛔ UN PÁNICO AL EMITIR NO PUEDE TUMBAR UN VEREDICTO YA TOMADO — y tiene que REPORTARSE, porque
// una observación que falta es un agujero en la evidencia y uno que nadie cuenta es el peor.
func TestUnPanicoAlEmitirSeContieneYSeReporta(t *testing.T) {
	t.Parallel()

	d := &decisorFalso{dec: Decision{Verdict: VerdictAllow, SessionSID: "sid"}}
	p := NewPEP(d, func(Request, Decision) { panic("el bus se cayó") }, nil, nil)
	var reportado string
	p.OnEmitPanic(func(event string, _ any) { reportado = event })

	w := postear(t, p, cuerpoOK, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("un pánico al emitir no puede cambiar la respuesta: código %d", w.Code)
	}
	if reportado != EventPreToolUse {
		t.Fatalf("el pánico tiene que reportarse con su evento, llegó %q", reportado)
	}
}

// El sello del agente se usa SI parsea, y sólo entonces. Uno ilegible no degrada a «ahora» en
// silencio: eso ataría el hecho a la hora de recepción haciéndola pasar por la de origen.
func TestElSelloDelAgenteSeUsaSoloSiParsea(t *testing.T) {
	t.Parallel()

	ahora := time.Date(2026, 8, 19, 9, 0, 0, 0, time.UTC)
	reloj := func() time.Time { return ahora }

	d := &decisorFalso{dec: Decision{Verdict: VerdictAllow, SessionSID: "sid"}}
	postear(t, NewPEP(d, nil, reloj, nil),
		`{"hookEventName":"pre_tool_use","sessionId":"s","timestamp":"2026-08-18T07:30:00Z"}`, nil)
	if !d.visto.At.Equal(time.Date(2026, 8, 18, 7, 30, 0, 0, time.UTC)) {
		t.Fatalf("un sello válido manda sobre el reloj local: %v", d.visto.At)
	}

	d2 := &decisorFalso{dec: Decision{Verdict: VerdictAllow, SessionSID: "sid"}}
	postear(t, NewPEP(d2, nil, reloj, nil),
		`{"hookEventName":"pre_tool_use","sessionId":"s","timestamp":"ayer por la tarde"}`, nil)
	if !d2.visto.At.Equal(ahora) {
		t.Fatalf("un sello ilegible cae al reloj local: %v", d2.visto.At)
	}
}

// El portador se extrae de Authorization y NO se filtra a ningún otro campo.
func TestElPortadorSeExtraeYNoViajaEnLaVista(t *testing.T) {
	t.Parallel()

	d := &decisorFalso{dec: Decision{Verdict: VerdictAllow, SessionSID: "sid"}}
	postear(t, NewPEP(d, nil, nil, nil), cuerpoOK, map[string]string{
		"Authorization":          "Bearer t-123",
		"X-Olivares-Grok-Tenant": "acme",
	})
	if d.bearer != "t-123" {
		t.Fatalf("portador = %q, quiere t-123", d.bearer)
	}
	if d.visto.Identity.Tenant != "acme" {
		t.Fatalf("la pista de tenant no llegó: %+v", d.visto.Identity)
	}
	// Sin el prefijo no hay portador: "" es un deny para cualquier decisor que exija identidad.
	d2 := &decisorFalso{dec: Decision{Verdict: VerdictAllow, SessionSID: "sid"}}
	postear(t, NewPEP(d2, nil, nil, nil), cuerpoOK, map[string]string{"Authorization": "t-123"})
	if d2.bearer != "" {
		t.Fatalf("sin el prefijo Bearer no hay portador, llegó %q", d2.bearer)
	}
}

// Sólo POST. Un GET no puede convertirse en una consulta al PDP.
func TestSoloPost(t *testing.T) {
	t.Parallel()

	d := &decisorFalso{dec: Decision{Verdict: VerdictAllow, SessionSID: "sid"}}
	p := NewPEP(d, nil, nil, nil)
	w := httptest.NewRecorder()
	p.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/hook", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("un GET tiene que salir 405, salió %d", w.Code)
	}
	if d.veces != 0 {
		t.Fatal("un GET no puede llegar al decisor")
	}
}

// ⛔⛔ LA CELDA QUE JUSTIFICA EL SOBRE, y nació de un rojo de este mismo fichero.
//
// `Render` está hecho para el PROCESO: un deny sobre un evento SIN veto sale con cuerpo NIL y
// código 0 — byte a byte lo mismo que un allow. Si el PEP contestara con ese stdout, la negativa
// se perdería EN EL TRANSPORTE, no en la política, y ninguna auditoría podría verla.
//
// LA MUTACIÓN que mata: volver a escribir el stdout de `Render` como cuerpo de la respuesta.
func TestUnDenyNoExpresableSeDistingueDeUnAllow(t *testing.T) {
	t.Parallel()

	// `stop` no admite veto; `pre_tool_use` sí. El deny es el mismo en los dos.
	if CanVeto(EventStop) {
		t.Fatal("premisa rota: este caso necesita un evento SIN veto")
	}
	den := &decisorFalso{dec: Decision{Verdict: VerdictDeny, Reason: "política X", SessionSID: "sid"}}
	sinVeto := postear(t, NewPEP(den, nil, nil, nil),
		`{"hookEventName":"stop","sessionId":"s-1"}`, nil).Body.String()

	alw := &decisorFalso{dec: Decision{Verdict: VerdictAllow, SessionSID: "sid"}}
	permitido := postear(t, NewPEP(alw, nil, nil, nil),
		`{"hookEventName":"stop","sessionId":"s-1"}`, nil).Body.String()

	if sinVeto == permitido {
		t.Fatalf("un deny NO EXPRESABLE sale idéntico a un allow: %q", sinVeto)
	}
	if !strings.Contains(sinVeto, `"verdict":"deny"`) {
		t.Fatalf("el deny tiene que viajar aunque no pueda impedir nada: %s", sinVeto)
	}
	// Y la otra mitad, que es la que impide mentir en la dirección contraria: NO se puede
	// presentar como impuesto algo que el agente no honra.
	if !strings.Contains(sinVeto, `"expressed":false`) {
		t.Fatalf("un deny sobre un evento sin veto NO es una imposición: %s", sinVeto)
	}
	if !strings.Contains(sinVeto, `"exit":0`) {
		t.Fatalf("un 2 que el agente no honra no impide nada y falsea el registro: %s", sinVeto)
	}

	// Control positivo del par: donde SÍ hay veto, el mismo deny sale expresado y con código 2.
	conVeto := postear(t, NewPEP(&decisorFalso{dec: den.dec}, nil, nil, nil),
		`{"hookEventName":"pre_tool_use","sessionId":"s-1"}`, nil).Body.String()
	if !strings.Contains(conVeto, `"expressed":true`) || !strings.Contains(conVeto, `"exit":2`) {
		t.Fatalf("en pre_tool_use el deny SÍ se expresa: %s", conVeto)
	}
}

// ⛔ RECORTAR NO PUEDE ROMPER UN CARÁCTER. Una referencia larga con runas multibyte —una ruta con
// acentos, un nombre en japonés o en cirílico— cortada por BYTES produce UTF-8 inválido, y eso
// acaba en una entrada de auditoría que después nadie puede leer ni comparar.
//
// LA MUTACIÓN que mata: volver a `s[:maxRefLen]`.
func TestElRecorteNoRompeUnCaracter(t *testing.T) {
	t.Parallel()

	// 400 runas de 3 bytes cada una: cortar en el byte 200 cae SIEMPRE a mitad de carácter.
	largo := strings.Repeat("日", 400)
	got := clipRef(largo)

	if !utf8.ValidString(got) {
		t.Fatalf("el recorte produjo UTF-8 inválido: %q", got)
	}
	if utf8.RuneCountInString(got) > maxRefLen+1 { // +1 por el carácter de elisión
		t.Fatalf("el recorte no acotó: %d runas", utf8.RuneCountInString(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("un recorte tiene que decirse: %q", got)
	}
	// Control positivo: algo corto NO se toca, o la celda la pasaría una función que devuelva
	// siempre lo mismo.
	if clipRef("Bash") != "Bash" {
		t.Fatal("una referencia corta no se recorta")
	}
	// Y el límite se mide en RUNAS: 200 caracteres japoneses son 600 bytes y no deben recortarse
	// más que 200 latinos.
	justo := strings.Repeat("日", maxRefLen)
	if clipRef(justo) != justo {
		t.Fatal("la cota se estrecha con el idioma: 200 runas japonesas se recortaron y 200 latinas no")
	}
}

// Y la referencia que viaja al decisor va acotada, no sólo la función suelta.
func TestLaReferenciaQueViajaVaAcotada(t *testing.T) {
	t.Parallel()

	d := &decisorFalso{dec: Decision{Verdict: VerdictAllow, SessionSID: "sid"}}
	cuerpo := `{"hookEventName":"pre_tool_use","sessionId":"s","cwd":"` + strings.Repeat("a", 5000) + `"}`
	postear(t, NewPEP(d, nil, nil, nil), cuerpo, nil)
	if len(d.visto.ResourceRef) > maxRefLen*4+4 {
		t.Fatalf("una referencia sin acotar la fija el llamante: %d bytes", len(d.visto.ResourceRef))
	}
	if len(d.visto.ResourceRef) == 0 {
		t.Fatal("y acotar no puede ser vaciar")
	}
}
