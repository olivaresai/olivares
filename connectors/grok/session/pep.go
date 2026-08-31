// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// pep.go es el punto de imposición gobernado para las llamadas de hook de Grok Build. Es dueño
// del protocolo de cable y de los deny-closed; la DECISIÓN pertenece al `Decider` inyectado, que
// vive del lado AGPL porque anclar necesita `/core` y este paquete NO puede importarlo.
//
// La separación no es cortesía: `scripts/check-boundary.sh` rompe la compilación si un paquete de
// `connectors/*` alcanza `/core` transitivamente. El compilador sostiene la regla, no la memoria
// de nadie.
//
// ⛔ Y ESTE PEP NO ES EL DE CODEX CON OTRO NOMBRE, aunque su forma sea la misma. Las tres
//    diferencias son justo las que un copiar-pegar borraría, y las tres están medidas contra el
//    fuente público de xAI (mismo commit anclado en `hook.go`):
//
//    1. **El evento del cable es snake_case.** `ParseHookPayload` ya lo canonicaliza, pero un PEP
//       que compare contra literales PascalCase no reconocería ninguno.
//    2. **Hay una segunda vía para el evento: el entorno.** Grok pone `GROK_HOOK_EVENT` además
//       del cuerpo, y un cuerpo truncado —la malformación más probable— no parsea. Por eso
//       `ParseHookPayload` toma `env`, y por eso este PEP se lo pasa: sin ella un stdin cortado
//       se denegaría en la forma más estricta por no saber de qué evento se trata.
//    3. **El veto sólo se expresa en `pre_tool_use`.** `Render` lo sabe y devuelve
//       `expressed=false` cuando el agente no honra el deny de ese evento. Emitir un 2 que el
//       agente ignora no impide nada y además deja en el registro la ilusión de que sí.

// Cabeceras de PISTA de identidad que el cliente sella desde su entorno. El payload de Grok no
// trae tenant/agente/organización, así que las pone el cliente. Son PISTAS: el principal que
// manda es el portador que el decisor resuelve. Una pista que contradiga a una política que
// exige identidad firme es problema del decisor, no algo que aquí se dé por bueno.
const (
	hdrTenant  = "X-Olivares-Grok-Tenant"
	hdrAgent   = "X-Olivares-Grok-Agent"
	hdrOrg     = "X-Olivares-Grok-Org"
	hdrAccount = "X-Olivares-Grok-Account"
)

// Identity es el contexto de atribución de una llamada.
type Identity struct {
	Tenant  string
	Agent   string
	Org     string
	Account string
}

// Request es la vista de datos mínimos que ve el decisor gobernado. No lleva argumentos crudos de
// herramienta: sólo la referencia derivada y saneada y los campos estructurales.
type Request struct {
	Event string
	// ExternalSessionID es el `sessionId` de Grok. Es un ALIAS, nunca nuestra clave.
	ExternalSessionID string
	Tool              string
	ResourceRef       string
	// PermissionMode es la postura que el PROPIO agente declara para esta llamada. El fuente
	// documenta sus cuatro valores —`default`, `auto`, `plan`, `bypassPermissions`—, así que es
	// el agente diciéndonos cuándo corre sin pedir permisos. Se REPORTA, nunca se confunde con
	// una autorización.
	PermissionMode string
	WorkspaceRoot  string
	Cwd            string
	Identity       Identity
	At             time.Time
	// PayloadDigest es un SHA-256 sobre los BYTES EXACTOS que entregó Grok. Es el discriminador
	// de idempotencia para los eventos que no traen identificador de llamada: dos
	// `session_start` de una misma sesión difieren en su payload y no deben colapsar en un solo
	// hecho, mientras que una REENTREGA es idéntica byte a byte y sí debe. Una marca de tiempo
	// del servidor no puede hacer este trabajo: una reentrega tendría otra y anularía la
	// garantía entera.
	PayloadDigest string
}

// Decision es el veredicto gobernado de una llamada.
//
// `Enforced` y `Verdict` son campos SEPARADOS a propósito: un deny que el evento no puede
// expresar sigue siendo un deny para el registro —el operador tiene que ver que se intentó
// impedir— y a la vez NO es una imposición. Colapsarlos obligaría a mentir en uno de los dos
// sitios.
type Decision struct {
	Verdict    Verdict
	Reason     string
	SessionSID string
	Enforced   bool
}

// DenyClosed construye la negativa por defecto. Es la única forma de decir que no en este
// paquete: cualquier camino que no llegue a una decisión gobernada termina aquí.
func DenyClosed(event, reason string) Decision {
	return Decision{Verdict: VerdictDeny, Reason: reason, Enforced: CanVeto(event)}
}

// Decider es la costura de decisión. La raíz de composición lo implementa contra el PDP vivo, el
// plano de identidad de sesión y el registro. `bearer` es la credencial entrante: es opaca aquí y
// NO se registra ni se guarda. Un decisor nulo, o cualquier error devuelto, es un DENY.
type Decider interface {
	Decide(ctx context.Context, req Request, bearer string) (Decision, error)
}

// Observer recibe un hecho por llamada gobernada para que el conector lo suba al bus. Va separado
// del `Decider` porque emitir es trabajo del conector y decidir no lo es; tenerlos aparte es lo
// que impide que el camino de emisión crezca un segundo escritor de registro.
type Observer func(req Request, dec Decision)

// PEP es la superficie HTTP a la que escribe el cliente del hook.
type PEP struct {
	decider Decider
	observe Observer
	now     func() time.Time
	env     func(string) string
	maxBody int64
	// onEmitPanic reporta un pánico que escape del observador. nil = la pérdida queda contenida
	// y SIN REPORTAR, que es por lo que la raíz de composición debería fijarlo siempre.
	onEmitPanic func(event string, cause any)
}

// OnEmitPanic registra el sumidero de un pánico del observador. Existe para que una emisión
// tragada sea al menos una emisión tragada REGISTRADA: una observación que falta es un agujero en
// la evidencia, y un agujero que nadie cuenta es el peor.
func (p *PEP) OnEmitPanic(fn func(event string, cause any)) { p.onEmitPanic = fn }

var _ http.Handler = (*PEP)(nil)

// NewPEP construye el punto. Un decisor nulo está permitido y deniega todas las llamadas: una
// postura deny-closed VISIBLE, nunca una puerta abierta en silencio.
//
// `env` es una ENTRADA del contrato, no ambiente: se pasa explícito para que el paquete se pueda
// probar sin ensuciar el proceso. nil significa «no hay segunda vía para el evento».
func NewPEP(d Decider, observe Observer, now func() time.Time, env func(string) string) *PEP {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &PEP{decider: d, observe: observe, now: now, env: env, maxBody: maxHookBody}
}

// ServeHTTP gatea una llamada de hook de Grok. Sólo POST. Lo que no entienda se deniega en la
// forma que ese evento honra — nunca se deja pasar, y nunca se deniega en una forma que el evento
// ignora, que vendría a ser lo mismo.
func (p *PEP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, p.maxBody))
	if err != nil {
		p.write(w, "", DenyClosed("", "could not read the hook request body (deny-closed)"))
		return
	}
	payload, ok := ParseHookPayload(body, p.env)
	if !ok {
		p.write(w, payload.HookEventName,
			DenyClosed(payload.HookEventName, "unreadable hook payload (deny-closed)"))
		return
	}
	if !IsKnownEvent(payload.HookEventName) {
		// No es un error: una versión de Grok que añada un evento no debe adquirir por eso un
		// camino no gobernado. El código de salida le da al cliente el segundo canal.
		p.write(w, payload.HookEventName,
			DenyClosed(payload.HookEventName, "unknown hook event (deny-closed)"))
		return
	}

	req := p.requestFrom(payload, r, body)

	if p.decider == nil {
		p.write(w, req.Event, DenyClosed(req.Event, "no governed decider is wired (deny-closed)"))
		return
	}
	dec, derr := p.decider.Decide(r.Context(), req, bearerOf(r))
	if derr != nil {
		p.write(w, req.Event, DenyClosed(req.Event, "governed decision failed (deny-closed)"))
		return
	}
	// Una decisión que no resolvió sesión canónica no es un hecho de sesión. El veredicto se
	// contesta igual —al agente hay que decírselo— pero no se emite nada: emitir con la
	// referencia vacía escribe una fila que la vista viva descarta y parecería un hecho
	// entregado.
	if p.observe != nil && dec.SessionSID != "" {
		p.emit(req, dec)
	}
	p.write(w, req.Event, dec)
}

// emit entrega el hecho al observador con un recover alrededor.
//
// La razón merece nombre: emitir es TELEMETRÍA y decidir no lo es. Un pánico en el observador no
// puede convertir un veredicto ya tomado en un 500 — el agente se quedaría sin respuesta y, en
// `pre_tool_use`, sin veredicto es peor que un deny. Se contiene, y se REPORTA.
func (p *PEP) emit(req Request, dec Decision) {
	defer func() {
		if c := recover(); c != nil && p.onEmitPanic != nil {
			p.onEmitPanic(req.Event, c)
		}
	}()
	p.observe(req, dec)
}

// requestFrom traduce el payload a la vista de datos mínimos.
func (p *PEP) requestFrom(pl HookPayload, r *http.Request, body []byte) Request {
	sum := sha256.Sum256(body)
	at := p.now().UTC()
	if pl.Timestamp != "" {
		// El sello del agente se usa SI parsea, y sólo entonces. Un timestamp ilegible no
		// degrada a «ahora» en silencio: eso ataría el hecho a la hora de recepción haciéndolo
		// pasar por la de origen.
		if t, err := time.Parse(time.RFC3339, pl.Timestamp); err == nil {
			at = t.UTC()
		}
	}
	return Request{
		Event:             pl.HookEventName,
		ExternalSessionID: pl.SessionID,
		Tool:              pl.ToolName,
		ResourceRef:       refFrom(pl),
		PermissionMode:    pl.PermissionMode,
		WorkspaceRoot:     pl.WorkspaceRoot,
		Cwd:               pl.Cwd,
		Identity: Identity{
			Tenant:  strings.TrimSpace(r.Header.Get(hdrTenant)),
			Agent:   strings.TrimSpace(r.Header.Get(hdrAgent)),
			Org:     strings.TrimSpace(r.Header.Get(hdrOrg)),
			Account: strings.TrimSpace(r.Header.Get(hdrAccount)),
		},
		At:            at,
		PayloadDigest: hex.EncodeToString(sum[:]),
	}
}

// refFrom deriva la referencia de recurso SIN mirar los valores del input.
//
// ⛔ Deliberadamente NO lee `toolInput`. Ahí es donde viven los secretos —un token en un
// argumento de shell, una ruta con el nombre de un cliente— y este campo viaja a un registro que
// se audita. Lo que se puede afirmar sin abrirlo es el ámbito, y con eso basta para gobernar.
func refFrom(pl HookPayload) string {
	switch {
	case pl.ToolName != "":
		return clipRef(pl.ToolName)
	case pl.WorkspaceRoot != "":
		return clipRef(pl.WorkspaceRoot)
	default:
		return clipRef(pl.Cwd)
	}
}

// maxRefLen acota una referencia derivada. Una ruta de trabajo puede ser arbitrariamente larga y
// esto viaja a un registro que se audita: sin cota, una entrada del registro la fija el llamante.
const maxRefLen = 200

// clipRef recorta SIN romper un carácter.
//
// ⛔ NO es `s[:maxRefLen]`, y la diferencia importa justo donde más caro sale. Cortar por BYTES
//
//	parte un rune multibyte por la mitad y produce UTF-8 INVÁLIDO — una ruta con acentos, un
//	nombre de cliente en cirílico o japonés— y ese resultado acaba en una entrada de auditoría
//	que después nadie puede leer ni comparar. El hermano de Codex corta por bytes
//	(`connectors/codex/session/resource.go:120-123`); aquí no, y hay celda que lo fija.
//
//	Se mide en RUNAS y no en bytes también para que el límite signifique lo mismo en cualquier
//	idioma: 200 bytes son 200 caracteres en inglés y 66 en japonés, y una cota que se estrecha
//	según el idioma del cliente es una cota que discrimina sin querer.
func clipRef(s string) string {
	if len(s) <= maxRefLen {
		return s
	}
	runas := []rune(s)
	if len(runas) <= maxRefLen {
		return s
	}
	return string(runas[:maxRefLen]) + "…"
}

// Response es el sobre que viaja del PEP al cliente del hook.
//
// ⛔⛔ AQUÍ ESTÁ LA DIFERENCIA REAL CON EL PEP DE CODEX, y la encontró una celda de este paquete
//
//	antes que yo. La tentación es que el PEP llame a `Render` y escriba su stdout, que es lo
//	que hace el hermano. **En Grok eso PIERDE el veredicto.** `Render` está hecho para el
//	PROCESO —devuelve stdout, código de salida y si el deny pudo expresarse— y sus salidas son:
//
//	  allow                        → cuerpo NIL, código 0
//	  deny en evento SIN veto      → cuerpo NIL, código 0, expressed=false
//	  deny en `pre_tool_use`       → {"decision":"deny",...}, código 2
//
//	⇒ Un deny sobre un evento sin veto viajaría como **200 con cuerpo vacío**, byte a byte
//	idéntico a un allow. El cliente no podría distinguirlos, y la negativa se perdería EN EL
//	TRANSPORTE — no en la política, que es lo que la haría invisible en cualquier auditoría.
//
//	Por eso el PEP contesta el VEREDICTO y **renderiza el cliente**, que es quien tiene el
//	stdout y el código de salida del agente delante. Cada uno hace lo suyo, y ninguno de los
//	dos tiene que adivinar lo del otro.
type Response struct {
	Verdict string `json:"verdict"`
	Reason  string `json:"reason,omitempty"`
	// Expressed dice si un deny puede llegar a IMPEDIR algo en este evento. Va explícito
	// porque un deny no expresable sigue siendo un deny para el registro y NO es una
	// imposición: quien lo lea tiene que poder decir cuál de las dos cosas está viendo.
	Expressed bool `json:"expressed"`
	// Exit es el código que el cliente debe devolverle al agente. Se calcula aquí, junto al
	// veredicto, para que no haya dos tablas del mismo mapeo.
	Exit int `json:"exit"`
	// Event viaja de vuelta porque el cliente puede haber llegado aquí SIN saberlo: un cuerpo
	// truncado se recupera por el entorno dentro del PEP.
	Event string `json:"event"`
}

// write contesta el veredicto. Siempre 200: el resultado de la POLÍTICA no es el resultado del
// TRANSPORTE, y un deny devuelto como error HTTP se confundiría con un PEP caído — que es
// justamente la situación en la que el cliente debe fallar cerrado por su cuenta.
func (p *PEP) write(w http.ResponseWriter, event string, dec Decision) {
	_, code, expressed := Render(event, dec.Verdict, dec.Reason)
	resp := Response{
		Verdict:   verdictName(dec.Verdict),
		Reason:    dec.Reason,
		Expressed: expressed,
		Exit:      code,
		Event:     event,
	}
	// El cuerpo renderizado lo produce el CLIENTE a partir de este sobre; no se reenvía, para
	// que no haya dos fuentes de la misma verdad.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func verdictName(v Verdict) string {
	if v == VerdictDeny {
		return "deny"
	}
	return "allow"
}

// bearerOf extrae el portador. Devuelve "" cuando no hay uno con la forma esperada, y "" es un
// deny para cualquier decisor que exija identidad.
func bearerOf(r *http.Request) string {
	h := strings.TrimSpace(r.Header.Get("Authorization"))
	const p = "Bearer "
	if len(h) > len(p) && strings.EqualFold(h[:len(p)], p) {
		return strings.TrimSpace(h[len(p):])
	}
	return ""
}
