// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

// ⛔ LA CELDA QUE JUSTIFICA EL PAQUETE. Grok usa camelCase y Codex snake_case, y un struct
// copiado del hermano **no falla al parsear**: `encoding/json` deja los campos vacíos y
// devuelve nil. El PEP recibiría cada evento y no reconocería ninguno, permitiéndolo todo.
//
// El emparejamiento de `encoding/json` ignora MAYÚSCULAS pero no PUNTUACIÓN, así que
// `hook_event_name` no casa con `hookEventName` — que es justo lo que hace peligroso el
// error y lo que esta celda fija.
//
// LA MUTACIÓN: poner las etiquetas en snake_case y esto se pone rojo por las dos direcciones.
func TestPayloadEsCamelCaseNoSnakeCase(t *testing.T) {
	t.Parallel()

	enGrok := []byte(`{"hookEventName":"PreToolUse","sessionId":"s-1","cwd":"/w","workspaceRoot":"/w","toolName":"bash"}`)
	p, ok := ParseHookPayload(enGrok, nil)
	if !ok {
		t.Fatalf("un payload de Grok legítimo no parseó: %+v", p)
	}
	if p.HookEventName != EventPreToolUse || p.SessionID != "s-1" || p.ToolName != "bash" {
		t.Fatalf("campos mal leídos de un payload camelCase: %+v", p)
	}
	if p.WorkspaceRoot != "/w" {
		t.Fatalf("workspaceRoot no se leyó: %+v", p)
	}

	// La otra dirección, y sin ella la de arriba la satisface un struct que acepte AMBAS:
	// un payload con la forma de Codex NO debe parsear como si fuera de Grok.
	enCodex := []byte(`{"hook_event_name":"PreToolUse","session_id":"s-1","tool_name":"bash"}`)
	q, ok := ParseHookPayload(enCodex, nil)
	if ok {
		t.Fatalf("un payload con la forma de CODEX parseó como Grok — el cable estaría abierto: %+v", q)
	}
	if q.HookEventName != "" || q.SessionID != "" {
		t.Fatalf("se leyeron campos de un payload snake_case: %+v", q)
	}
}

// El conjunto cerrado, con su cuenta. Un número aquí no es ceremonia: si una versión de Grok
// añade un evento y alguien lo mete en la lista sin mirar el resto del cable, esto lo obliga a
// pasar por aquí y decidir su mecanismo en render.go.
func TestConjuntoDeEventosEsCerradoYCompleto(t *testing.T) {
	t.Parallel()

	// ⛔ VALORES DE CABLE, no nombres de la página. Salen de la tabla `hook_events!` de
	//    `crates/codegen/xai-grok-hooks/src/event.rs` en el commit anclado del repositorio
	//    público de xAI: el enum deriva `Serialize` con `rename_all = "snake_case"` y su autor
	//    lo anota al lado — «Serialize stays derived snake_case (wire unchanged)».
	//
	//    Son QUINCE y no dieciséis porque `SubagentStop` y `SubagentEnd` declaran el mismo
	//    `display`; en el cable son el mismo valor.
	quiere := []string{
		"notification", "permission_denied", "post_compact", "post_tool_use",
		"post_tool_use_failure", "pre_compact", "pre_tool_use", "session_end",
		"session_start", "stop", "stop_cancelled", "stop_failure", "subagent_start",
		"subagent_stop", "user_prompt_submit",
	}
	got := KnownEvents()
	sort.Strings(got)
	if len(got) != len(quiere) {
		t.Fatalf("la documentación declara %d eventos y el conector conoce %d: %v", len(quiere), len(got), got)
	}
	for i := range quiere {
		if got[i] != quiere[i] {
			t.Fatalf("evento %d: quiere %q, tiene %q", i, quiere[i], got[i])
		}
	}

	// ⛔ LOS CUATRO QUE CODEX NO TIENE. Se nombran porque el riesgo real es copiar la lista del
	//    hermano: los cuatro quedarían fuera del conjunto cerrado y por tanto deny-closed —
	//    seguro, pero inservible, y nadie sabría por qué el agente se queja.
	for _, e := range []string{"post_tool_use_failure", "permission_denied", "stop_failure", "notification"} {
		if !IsKnownEvent(e) {
			t.Fatalf("%s es de Grok y no de Codex: falta en el conjunto", e)
		}
	}
	// Y el de Codex que Grok NO tiene, en la dirección contraria.
	if IsKnownEvent("permission_request") || IsKnownEvent("PermissionRequest") {
		t.Fatal("PermissionRequest es de Codex; Grok no lo declara y no debe estar aquí")
	}
	if IsKnownEvent("") || IsKnownEvent("pre_tool_use ") || IsKnownEvent("pretooluse") {
		t.Fatal("el conjunto cerrado admitió una variante que la documentación no declara")
	}
}

// Un cuerpo que no parsea tiene que seguir entregando el evento cuando se puede recuperar: el
// deny hay que emitirlo igual y el evento decide si se puede.
func TestPayloadRotoRecuperaElEvento(t *testing.T) {
	t.Parallel()

	entorno := func(k string) string {
		if k == EnvHookEvent {
			return EventPreToolUse
		}
		return ""
	}
	roto := []byte(`{"hookEventName":"PreToolUse","sessionId":`)
	p, ok := ParseHookPayload(roto, entorno)
	if ok {
		t.Fatal("un JSON truncado no puede darse por bueno")
	}
	if p.HookEventName != EventPreToolUse {
		t.Fatalf("no se recuperó el evento de un cuerpo roto: %q", p.HookEventName)
	}
	// SIN la segunda vía, el mismo cuerpo no puede recuperar nada — y eso prueba que la celda
	// de arriba mide el ENTORNO y no un parseo parcial que no existe.
	if q, _ := ParseHookPayload(roto, nil); q.HookEventName != "" {
		t.Fatalf("sin entorno no hay de dónde recuperar, y salió %q", q.HookEventName)
	}
	// Y el entorno no es una puerta abierta: un valor fuera del conjunto cerrado no pasa.
	hostil := func(k string) string {
		if k == EnvHookEvent {
			return "EventoInventado"
		}
		return ""
	}
	if q, _ := ParseHookPayload(roto, hostil); q.HookEventName != "" {
		t.Fatalf("el entorno coló un evento que la documentación no declara: %q", q.HookEventName)
	}

	// Y cuando NO se puede recuperar, "" — nunca un evento cómodo por defecto.
	if e := EventNameOf([]byte("no es json")); e != "" {
		t.Fatalf("EventNameOf inventó un evento: %q", e)
	}
	if p2, ok := ParseHookPayload([]byte(`{"hookEventName":"PreToolUse"}`), nil); ok {
		t.Fatalf("un payload SIN sessionId no puede ser válido: %+v", p2)
	}
}

// El tope de tamaño, con su control positivo: por debajo entra, por encima se rechaza y aun
// así se recupera el evento.
func TestPayloadEnormeSeRechazaSinPerderElEvento(t *testing.T) {
	t.Parallel()

	entorno := func(k string) string {
		if k == EnvHookEvent {
			return EventPreToolUse
		}
		return ""
	}
	relleno := strings.Repeat("a", maxHookBody)
	grande, err := json.Marshal(HookPayload{
		HookEventName: EventPreToolUse,
		SessionID:     "s-1",
		ToolInput:     json.RawMessage(`"` + relleno + `"`),
	})
	if err != nil {
		t.Fatalf("no se pudo construir el payload grande: %v", err)
	}
	if len(grande) <= maxHookBody {
		t.Fatalf("el payload de prueba no supera el tope (%d <= %d)", len(grande), maxHookBody)
	}
	p, ok := ParseHookPayload(grande, entorno)
	if ok {
		t.Fatal("un cuerpo por encima del tope no puede darse por bueno")
	}
	if p.HookEventName != EventPreToolUse {
		t.Fatalf("se perdió el evento al rechazar por tamaño: %q", p.HookEventName)
	}
}

// TestTodoEventoEsSnakeCase fija la FAMILIA, no un miembro. Una celda que compruebe un evento
// concreto la satisface un conector con catorce PascalCase y uno bien; ésta cae con el primero
// que se escriba con la grafía de la página pública, que es el error que ya cometimos.
func TestTodoEventoEsSnakeCase(t *testing.T) {
	t.Parallel()

	for _, e := range KnownEvents() {
		if e == "" {
			t.Fatal("un evento vacío no es un evento")
		}
		if e != strings.ToLower(e) {
			t.Fatalf("%q lleva mayúsculas: el cable de Grok es snake_case, la página no", e)
		}
		for _, r := range e {
			if !(r >= 'a' && r <= 'z') && r != '_' {
				t.Fatalf("%q tiene %q, y los valores de cable son [a-z_]", e, r)
			}
		}
	}
	// Control positivo: si `KnownEvents` devolviera vacío, el bucle de arriba pasaría sin
	// comprobar nada. Es la forma exacta de una sonda que contesta lo mismo para toda entrada.
	if len(KnownEvents()) != 15 {
		t.Fatalf("quiere 15 eventos de cable, tiene %d", len(KnownEvents()))
	}
}

// TestGrafiasAlternativasNormalizan cubre las dos vías por las que puede llegar una grafía que
// no es la del cable: la página pública enseña PascalCase, y `GROK_HOOK_EVENT` la pone el
// agente. Normalizar es lo que hace el propio Grok en `from_key_str`.
func TestGrafiasAlternativasNormalizan(t *testing.T) {
	t.Parallel()

	casos := map[string]string{
		"PreToolUse":           EventPreToolUse,
		"preToolUse":           EventPreToolUse,
		"beforeShellExecution": EventPreToolUse,
		"SessionStart":         EventSessionStart,
		"beforeSubmitPrompt":   EventUserPromptSubmit,
		// Las tres grafías de SubagentEnd colapsan al valor de SubagentStop: el `display` es el
		// mismo en el fuente, así que el cable no las distingue.
		"SubagentEnd":  EventSubagentStop,
		"subagent_end": EventSubagentStop,
		"subagentEnd":  EventSubagentStop,
	}
	for grafia, quiere := range casos {
		if got := CanonicalEvent(grafia); got != quiere {
			t.Fatalf("CanonicalEvent(%q) = %q, quiere %q", grafia, got, quiere)
		}
		if !IsKnownEvent(grafia) {
			t.Fatalf("%q es una grafía que el propio Grok acepta y aquí sale desconocida", grafia)
		}
	}
	// Y la dirección que importa más: normalizar NO ensancha el conjunto. Un evento que no
	// existe sigue sin existir, y un espacio no es una grafía.
	for _, e := range []string{"PermissionRequest", "preToolUseX", "pre_tool_use ", "PRE_TOOL_USE"} {
		if IsKnownEvent(e) {
			t.Fatalf("%q no es un evento de Grok y el conector lo da por conocido", e)
		}
	}
}

// TestSobreCompletoDelFuente fija los campos que el sobre declara y que faltaban. El que manda
// es `permissionMode`: sus cuatro valores están literalmente en el fuente, y sin él una
// observación no distingue una sesión gobernada de una lanzada con los permisos saltados.
func TestSobreCompletoDelFuente(t *testing.T) {
	t.Parallel()

	cuerpo := []byte(`{"hookEventName":"pre_tool_use","sessionId":"s-1","cwd":"/w",` +
		`"workspaceRoot":"/w","timestamp":"2026-08-19T06:00:00Z","transcriptPath":"/t.jsonl",` +
		`"clientIdentifier":"grok-cli","promptId":"p-9","permissionMode":"bypassPermissions",` +
		`"toolName":"Bash"}`)
	p, ok := ParseHookPayload(cuerpo, nil)
	if !ok {
		t.Fatal("un sobre completo y bien formado tiene que parsear")
	}
	for campo, tiene := range map[string]string{
		"timestamp":        p.Timestamp,
		"transcriptPath":   p.TranscriptPath,
		"clientIdentifier": p.ClientIdentifier,
		"promptId":         p.PromptID,
		"permissionMode":   p.PermissionMode,
	} {
		if tiene == "" {
			t.Fatalf("%s llegó en el cuerpo y el conector lo perdió", campo)
		}
	}
	if p.PermissionMode != "bypassPermissions" {
		t.Fatalf("permissionMode = %q, quiere bypassPermissions", p.PermissionMode)
	}
}
