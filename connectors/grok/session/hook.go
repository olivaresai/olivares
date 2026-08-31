// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"encoding/json"
	"strings"
)

// hook.go is the INBOUND half of the Grok Build hook wire: the event names and the payload
// Grok hands a hook command on stdin.
//
// ⛔ PROCEDENCIA, Y SUS DOS MITADES SON DISTINTAS — no las mezcles al leer esto.
//
// Los nombres, los campos y los códigos de salida están tomados LITERALMENTE de
// `docs.x.ai/build/features/hooks`, leído el 2026-08-18. El repositorio oficial se ancla en
// `xai-org/grok-build @ d71f6e0c1f5acc5469e503e192fe14824e6f8c90` (2026-08-17T18:48:25Z),
// que es el ÚNICO ancla inmutable disponible: el feed de releases está VACÍO y el
// `Cargo.toml` raíz es un manifiesto de workspace sin versión propia.
//
// ⛔⛔ CORREGIDO EL 2026-08-19 CONTRA EL FUENTE, Y LA DOCUMENTACIÓN ESTABA MAL. Lo de arriba
// se escribió leyendo `docs.x.ai/build/features/hooks`, que muestra los eventos en PascalCase
// (`SessionStart`, `PreToolUse`). **El cable no lleva eso.** El repositorio es público y en el
// commit ya anclado dice, en `crates/codegen/xai-grok-hooks/src/event.rs`:
//
//	#[serde(rename_all = "camelCase")]        // ← las CLAVES del sobre
//	pub struct HookEventEnvelope {
//	    pub hook_event_name: HookEventName,   // ← cuyo VALOR es otra cosa
//
// y `HookEventName` deriva `Serialize` con `#[serde(rename_all = "snake_case")]`, con el
// comentario del propio autor al lado: *«`Serialize` stays derived snake_case (wire
// unchanged)»*. ⇒ **Claves camelCase, valor del evento snake_case.** `session_start`, no
// `SessionStart`.
//
// El coste de haberlo dejado como estaba no era un rename: las catorce constantes PascalCase
// no casaban NINGÚN evento real, y `IsKnownEvent` es la puerta del deny-closed. Todo evento
// entrante habría caído en «desconocido». Eso no abre un agujero —deny-closed deniega— pero
// convierte al conector en un negador universal: gobierna a base de romperlo todo, que es la
// otra forma de no gobernar nada.
//
// Y la tabla trae dos cosas más que la página no dice: son **DIECISÉIS** variantes, no catorce
// —faltaban `StopCancelled` y `SubagentEnd`—, y **`SubagentStop` y `SubagentEnd` comparten el
// mismo valor de cable** (`subagent_stop`), así que son 15 valores distintos y no 16.
//
// ⚠ Lo que SIGUE sin medir, y no lo tapa este arreglo: no hemos visto un payload REAL de un
// binario de Grok Build en ejecución. El fuente es fuente primaria y es mucho mejor que la
// página, pero instalar el binario de un tercero no es una acción que esta sesión deba tomar
// sola. Queda como la última milla, y ahora es la única que falta.
//
// ⛔⛔ Y LA DIFERENCIA CON CODEX QUE MÁS CARO SALDRÍA SUPONER: **Grok usa camelCase**
// (`hookEventName`, `sessionId`, `workspaceRoot`) donde Codex usa snake_case
// (`hook_event_name`, `session_id`). Un struct copiado del hermano parsea a CERO campos y no
// falla: `encoding/json` deja los campos vacíos y sigue. El resultado sería un PEP que recibe
// todos los eventos y no reconoce ninguno.

// Los catorce eventos que declara la documentación de Grok Build. Grok trae CUATRO que Codex
// no tiene —`PostToolUseFailure`, `PermissionDenied`, `StopFailure`, `Notification`— y le
// falta el `PermissionRequest` de Codex. No son listas equivalentes.
// Los QUINCE valores de cable, tomados de la tabla `hook_events!` del fuente. El nombre Go
// conserva la variante de Rust; el valor es lo que viaja.
const (
	EventSessionStart       = "session_start"
	EventSessionEnd         = "session_end"
	EventUserPromptSubmit   = "user_prompt_submit"
	EventPreToolUse         = "pre_tool_use"
	EventPostToolUse        = "post_tool_use"
	EventPostToolUseFailure = "post_tool_use_failure"
	EventPermissionDenied   = "permission_denied"
	EventStop               = "stop"
	EventStopFailure        = "stop_failure"
	EventStopCanceled       = "stop_cancelled"
	EventNotification       = "notification"
	EventSubagentStart      = "subagent_start"
	// EventSubagentStop cubre DOS variantes de Rust: `SubagentStop` y `SubagentEnd` declaran el
	// mismo `display`, así que en el cable son indistinguibles. Modelarlas por separado aquí
	// inventaría una distinción que el protocolo no transporta.
	EventSubagentStop = "subagent_stop"
	EventPreCompact   = "pre_compact"
	EventPostCompact  = "post_compact"
)

// knownEvents es el conjunto CERRADO. Un evento fuera de él es deny-closed (ver render.go).
// Es un mapa y no una cadena de comparaciones a propósito: una cadena de `||` se ensancha por
// descuido con un `||` de más, y un mapa no.
var knownEvents = map[string]struct{}{
	EventSessionStart:       {},
	EventSessionEnd:         {},
	EventUserPromptSubmit:   {},
	EventPreToolUse:         {},
	EventPostToolUse:        {},
	EventPostToolUseFailure: {},
	EventPermissionDenied:   {},
	EventStop:               {},
	EventStopFailure:        {},
	EventStopCanceled:       {},
	EventNotification:       {},
	EventSubagentStart:      {},
	EventSubagentStop:       {},
	EventPreCompact:         {},
	EventPostCompact:        {},
}

// eventAliases mapea las grafías que el PROPIO Grok acepta al leer (su `from_key_str`, misma
// tabla) sobre el valor de cable. No ensancha el conjunto conocido: mapea sinónimos de miembros
// que ya están dentro, y lo que no reconoce sigue siendo desconocido.
//
// ⛔ Por qué existe y no es adorno: el cable sólo lleva snake_case, pero `GROK_HOOK_EVENT` —la
// segunda vía, la que salva un stdin truncado— la pone el agente y no hemos medido su grafía; y
// la página pública de Grok enseña PascalCase, así que un hook escrito siguiéndola manda eso.
// Normalizar es lo que hace el upstream; negar por la grafía sería negar por un detalle que
// ellos mismos consideran equivalente.
var eventAliases = map[string]string{
	"SessionStart": EventSessionStart, "sessionStart": EventSessionStart,
	"SessionEnd": EventSessionEnd, "sessionEnd": EventSessionEnd,
	"UserPromptSubmit": EventUserPromptSubmit, "beforeSubmitPrompt": EventUserPromptSubmit,
	"PreToolUse": EventPreToolUse, "preToolUse": EventPreToolUse,
	"beforeShellExecution": EventPreToolUse,
	"PostToolUse":          EventPostToolUse, "postToolUse": EventPostToolUse,
	"afterShellExecution": EventPostToolUse,
	"PostToolUseFailure":  EventPostToolUseFailure, "postToolUseFailure": EventPostToolUseFailure,
	"PermissionDenied": EventPermissionDenied, "permissionDenied": EventPermissionDenied,
	"Stop":        EventStop,
	"StopFailure": EventStopFailure, "stopFailure": EventStopFailure,
	"StopCancelled": EventStopCanceled, "stopCancelled": EventStopCanceled,
	"Notification":  EventNotification,
	"SubagentStart": EventSubagentStart, "subagentStart": EventSubagentStart,
	"SubagentStop": EventSubagentStop, "subagentStop": EventSubagentStop,
	// Las tres grafías de `SubagentEnd` colapsan al MISMO valor, igual que arriba.
	"SubagentEnd": EventSubagentStop, "subagent_end": EventSubagentStop,
	"subagentEnd": EventSubagentStop,
	"PreCompact":  EventPreCompact, "preCompact": EventPreCompact,
	"PostCompact": EventPostCompact, "postCompact": EventPostCompact,
}

// CanonicalEvent devuelve el valor de cable de una grafía conocida, o la entrada tal cual si no
// la reconoce — dejar pasar el original es lo correcto: quien decide es `IsKnownEvent`, y un
// desconocido tiene que seguir siendo desconocido.
// ⛔ NO recorta espacios a propósito: un espacio de más NO es una grafía alternativa, es un
// cuerpo malformado, y `IsKnownEvent("pre_tool_use ")` tiene que seguir siendo falso. El
// recorte lo hace quien parsea, antes de llamar aquí — mezclarlos ensancharía el conjunto
// cerrado por un camino que nadie revisó.
func CanonicalEvent(event string) string {
	if _, ok := knownEvents[event]; ok {
		return event
	}
	if c, ok := eventAliases[event]; ok {
		return c
	}
	return event
}

// IsKnownEvent indica si el evento tiene contrato en este conector. Un evento desconocido no
// es un error: es un DENY, porque una versión de Grok que añada un evento no debe adquirir en
// silencio un camino no gobernado.
func IsKnownEvent(event string) bool {
	_, ok := knownEvents[CanonicalEvent(event)]
	return ok
}

// KnownEvents devuelve el conjunto cerrado, para que la raíz de composición pueda comprobar
// cobertura sin importar el mapa.
func KnownEvents() []string {
	out := make([]string, 0, len(knownEvents))
	for e := range knownEvents {
		out = append(out, e)
	}
	return out
}

// HookPayload es la unión de los campos documentados. Grok envía un objeto por evento con sólo
// los campos de ese evento; modelar la unión mantiene el despacho del PEP en un sitio, y cada
// consumidor comprueba lo que necesita en vez de suponer presencia.
type HookPayload struct {
	HookEventName string `json:"hookEventName"`
	SessionID     string `json:"sessionId"`
	Cwd           string `json:"cwd,omitempty"`
	WorkspaceRoot string `json:"workspaceRoot,omitempty"`
	// Timestamp, TranscriptPath, ClientIdentifier y PromptID los declara el sobre del fuente
	// (`HookEventEnvelope`) y faltaban aquí. Los tres últimos son `Option`, así que van
	// `omitempty`; `timestamp` NO lo es y llega siempre.
	Timestamp        string `json:"timestamp,omitempty"`
	TranscriptPath   string `json:"transcriptPath,omitempty"`
	ClientIdentifier string `json:"clientIdentifier,omitempty"`
	PromptID         string `json:"promptId,omitempty"`
	// ⭐ PermissionMode es el campo de GOBIERNO del sobre, y es el que más caro salía omitir:
	// el fuente documenta sus cuatro valores literalmente —`default`, `auto`, `plan`,
	// `bypassPermissions`— así que el propio agente nos dice cuándo está corriendo SIN pedir
	// permisos. Una observación de sesión que no lo lleva no puede distinguir una sesión
	// gobernada de una que se saltó la puerta.
	PermissionMode string          `json:"permissionMode,omitempty"`
	ToolName       string          `json:"toolName,omitempty"`
	ToolInput      json.RawMessage `json:"toolInput,omitempty"`
}

// Las variables de entorno que Grok pone además del stdin. Se declaran porque son la ÚNICA vía
// cuando el cuerpo no parsea: un deny hay que emitirlo igual, y para eso hace falta al menos
// saber qué evento era.
const (
	EnvHookEvent     = "GROK_HOOK_EVENT"
	EnvHookName      = "GROK_HOOK_NAME"
	EnvSessionID     = "GROK_SESSION_ID"
	EnvWorkspaceRoot = "GROK_WORKSPACE_ROOT"
)

// maxHookBody acota un payload entrante. Sólo `toolInput` es ilimitado en principio.
const maxHookBody = 1 << 20 // 1 MiB

// ParseHookPayload decodifica un payload. Uno que no parsea sigue devolviendo el nombre del
// evento cuando puede recuperarse, porque la respuesta deny-closed hay que emitirla de todos
// modos y el evento decide cómo.
//
// ⛔ `env` NO ES UN PARÁMETRO DE COMODIDAD PARA LOS TESTS, y lo escribo porque la primera
//
//	versión no lo tenía y sus dos celdas de recuperación salieron ROJAS: recuperar el evento
//	leyendo el JSON **sólo funciona si el JSON está bien**, y un cuerpo truncado —la
//	malformación más probable de todas, y la que un stdin cortado produce— no parsea, así que
//	devolvía "" y el deny se habría emitido en la forma más estricta por no saber el evento.
//
//	Una recuperación que exige que el cuerpo esté sano no es una recuperación. La segunda vía
//	la da el propio agente y está documentada: Grok pone `GROK_HOOK_EVENT` en el entorno
//	ADEMÁS de mandar el cuerpo por stdin. Un canal no se corrompe con el otro.
//
//	Se pasa explícito en vez de llamar a `os.Getenv` dentro: un paquete que lee el entorno por
//	su cuenta no se puede probar sin ensuciar el proceso, y aquí el entorno es una ENTRADA del
//	contrato, no ambiente. `nil` es válido y significa «no hay segunda vía».
func ParseHookPayload(body []byte, env func(string) string) (HookPayload, bool) {
	if len(body) > maxHookBody {
		return HookPayload{HookEventName: RecoverEvent(body[:maxHookBody], env)}, false
	}
	var p HookPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return HookPayload{HookEventName: RecoverEvent(body, env)}, false
	}
	p.HookEventName = CanonicalEvent(strings.TrimSpace(p.HookEventName))
	p.SessionID = strings.TrimSpace(p.SessionID)
	p.ToolName = strings.TrimSpace(p.ToolName)
	if p.HookEventName == "" || p.SessionID == "" {
		// Parseó, pero le falta lo obligatorio. El entorno puede completar el evento; NO el
		// sessionId, porque un identificador inventado ataría observaciones a una sesión que no
		// es. Falta de identidad es falta de identidad.
		if p.HookEventName == "" {
			p.HookEventName = RecoverEvent(body, env)
		}
		return p, false
	}
	return p, true
}

// RecoverEvent recupera el nombre del evento por las dos vías que Grok ofrece, en orden: el
// cuerpo, y si no, el entorno. Devuelve "" cuando ninguna contesta, y "" encamina al
// deny-closed más estricto — nunca a un evento por defecto cómodo.
func RecoverEvent(body []byte, env func(string) string) string {
	if e := EventNameOf(body); e != "" {
		return e
	}
	if env == nil {
		return ""
	}
	// Se valida contra el conjunto cerrado: el entorno lo puede fijar cualquiera que lance el
	// proceso, así que un valor arbitrario aquí no puede convertirse en un evento reconocido.
	if e := CanonicalEvent(strings.TrimSpace(env(EnvHookEvent))); IsKnownEvent(e) {
		return e
	}
	return ""
}

// EventNameOf recupera `hookEventName` de un cuerpo que puede no parsear entero. Devuelve ""
// cuando no puede, y "" encamina al deny-closed más estricto — NUNCA a un evento por defecto
// cómodo. Adivinar aquí `PreToolUse` emitiría un veredicto que un `Stop` no honra.
func EventNameOf(body []byte) string {
	var probe struct {
		HookEventName string `json:"hookEventName"`
	}
	if json.Unmarshal(body, &probe) == nil {
		return CanonicalEvent(strings.TrimSpace(probe.HookEventName))
	}
	return ""
}
