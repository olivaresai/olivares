// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package grok

import (
	"time"

	"sort"
	"strings"

	"github.com/olivaresai/olivares/sdk/model"
)

// sandbox.go traduce el perfil de sandbox observado a hallazgos.
//
// La tabla sale literal de `docs.x.ai/build/features/sandbox`. El bloqueo de red se documenta
// «enforced on Linux only», y eso viaja en el hallazgo en vez de quedarse en un comentario:
// un cliente que corra el agente en macOS tiene una promesa distinta del mismo perfil.

// perfil describe lo que un perfil concede, tal y como lo publica x.ai.
type perfil struct {
	escritura string
	red       bool // true = permitida
}

// perfiles es el conjunto CERRADO documentado. Un perfil fuera de él no se interpreta: se
// reporta como desconocido, porque adivinar qué concede un nombre nuevo es exactamente cómo se
// publica una garantía que el agente no da.
var perfiles = map[string]perfil{
	"off":       {escritura: "unrestricted", red: true},
	"workspace": {escritura: "CWD, ~/.grok/, and temp", red: true},
	"devbox":    {escritura: "top-level directories except /data", red: true},
	"read-only": {escritura: "~/.grok/ and temp", red: false},
	"strict":    {escritura: "CWD, ~/.grok/, and temp", red: false},
}

// perfilesConocidos devuelve el conjunto ordenado, para mensajes y para que una celda pueda
// comprobar cobertura sin importar el mapa.
func perfilesConocidos() []string {
	out := make([]string, 0, len(perfiles))
	for p := range perfiles {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// findings construye las observaciones de una corrida.
func (s *Source) findings(cfg grokConfig, estado estadoConfig, req grokConfig, reqEstado estadoConfig, desact []string, desactEstado estadoConfig) []model.FindingReport {
	at := s.clock().UTC()
	out := make([]model.FindingReport, 0, 4)

	// ── 1 · ¿ESTÁ IMPUESTO EL PERFIL? Ahora es una MEDIDA, no una afirmación ────────────
	//
	// ⛔⛔ ESTE HALLAZGO SE EMITÍA SIEMPRE Y AFIRMABA ALGO FALSO. Decía que **«no hay
	//     imposición administrativa documentada»**, y eso se escribió leyendo `docs.x.ai`. El
	//     repositorio es público y dice otra cosa (mismo commit anclado en `session/hook.go`,
	//     verificado el 2026-08-19):
	//
	//       · `paths.rs:25-31`      → en unix el directorio de sistema es **`/etc/grok`**
	//       · `validation.rs:76-86` → lee ahí **`requirements.toml`** con `is_system: true`
	//       · `validation.rs`       → y una capa **MDM de macOS**, «forced values only», puesta
	//                                 la última «so it wins the deep-merge» y marcada `is_system`
	//                                 «so security decisions trust it like the root-owned layer»
	//       · `config_layers.rs:65` → requisitos y MDM **ACOTAN** («clamp»), no sólo fusionan, y
	//                                 son la capa MÁS ALTA: por encima del overlay `GROK_CONFIG`,
	//                                 del usuario, del `managed` y del `system_managed`
	//
	//     ⇒ Sí hay imposición de administrador, y de la clase fuerte: fichero de root más MDM.
	//     Un producto de gobierno que le dice a un operador que un control **no existe** cuando
	//     existe es peor que no traer el control: le quita la razón para ponerlo.
	//
	//     El hallazgo bueno no es «no se puede imponer» sino **«se puede y NO está puesto»**, que
	//     es accionable; y cuando sí lo está, decirlo y bajar la severidad, porque entonces
	//     `--sandbox off` y `GROK_SANDBOX=off` ya no mandan.
	out = append(out, s.hallazgoImposicion(req, reqEstado, at))
	out = append(out, s.hallazgoHooksDesactivados(desact, desactEstado, at))

	// ── 1-ter · La superficie MCP: qué alcanza la sesión y si un admin lo ha acotado ────
	out = append(out, s.hallazgosMCP(cfg, estado, req, reqEstado, at)...)

	// ── 1-bis · La vía de compatibilidad con Claude, que también es enforcement ─────────
	//
	// `paths.rs:8-11,33-38`: Grok Build lee el `managed-settings.json` de **Claude Code**
	// —`/etc/claude-code/managed-settings.json` en Linux, `/Library/Application
	// Support/ClaudeCode/…` en macOS— «for settings compat». Quien ya gobierna Claude por esa vía
	// gobierna también este agente, y eso es un hecho de inventario que un operador quiere ver.
	out = append(out, model.FindingReport{
		Kind:        "inventory",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectManaged,
		SubjectRef:  s.agentRef,
		Title: "Grok Build reads Claude Code's managed-settings.json for compatibility " +
			"(/etc/claude-code/managed-settings.json on Linux): governing Claude through that path " +
			"also governs this agent",
		OccurredAt: at,
	})

	// ── 2 · El perfil efectivo que se ha podido observar ────────────────────────────────
	switch estado {
	case configAusente:
		out = append(out, model.FindingReport{
			Kind:        "inventory",
			Severity:    model.SeverityInfo,
			SubjectKind: subjectConfig,
			SubjectRef:  s.agentRef,
			Title:       "No Grok Build config.toml exists at the configured path: no sandbox profile was observed (the agent uses its default, and the other two paths remain available)",
			OccurredAt:  at,
		})
	case configIlegible:
		// ⛔ ILEGIBLE NO ES AUSENTE. El agente sí va a leer ese fichero; nosotros no hemos
		//    podido. Reportarlo como «no configurado» sería un verde sobre algo que no se ha
		//    mirado, que es la única clase de fallo que este repo no perdona.
		out = append(out, model.FindingReport{
			Kind:        findingKindPosture,
			Severity:    model.SeverityMedium,
			SubjectKind: subjectConfig,
			SubjectRef:  s.agentRef,
			Title:       "Grok Build's config.toml exists and could NOT be read: no claim is made about its sandbox profile — this is not \"unconfigured\"",
			OccurredAt:  at,
		})
	case configLeido:
		out = append(out, s.hallazgoPerfil(cfg.Sandbox.Profile, at))
	}

	// ── 3 · La superficie que SÍ es enforcement ─────────────────────────────────────────
	if s.managedPath == "" {
		out = append(out, model.FindingReport{
			Kind:        "coverage",
			Severity:    model.SeverityInfo,
			SubjectKind: subjectManaged,
			SubjectRef:  s.agentRef,
			Title:       "No managed_settings_path is configured: no claim is made about the managed-settings.json honored by Grok Build (permission rules and MCP allowlists)",
			OccurredAt:  at,
		})
	}
	return out
}

// hallazgoPerfil interpreta el perfil observado.
func (s *Source) hallazgoPerfil(nombre string, at time.Time) model.FindingReport {
	base := model.FindingReport{
		Kind:        findingKindPosture,
		SubjectKind: subjectSandbox,
		SubjectRef:  s.agentRef,
		OccurredAt:  at,
	}
	if nombre == "" {
		base.Severity = model.SeverityInfo
		base.Title = "Grok Build's config.toml does not set [sandbox] profile: the agent's default profile applies"
		return base
	}
	p, ok := perfiles[strings.ToLower(nombre)]
	if !ok {
		// Un perfil que la documentación no declara no se interpreta.
		base.Severity = model.SeverityMedium
		base.Title = "Unrecognized Grok Build sandbox profile: " + nombre + " (documented: " + strings.Join(perfilesConocidos(), ", ") + ") — no claim is made about what it grants"
		return base
	}
	red := "network BLOCKED (documented as \"enforced on Linux only\": not guaranteed on other systems)"
	base.Severity = model.SeverityInfo
	if p.red {
		red = "network ALLOWED"
		base.Severity = model.SeverityMedium
	}
	if strings.EqualFold(nombre, "off") {
		base.Severity = model.SeverityHigh
	}
	base.Title = "Observed Grok Build sandbox profile: " + nombre + " — write access: " + p.escritura + "; " + red
	return base
}

// hallazgoImposicion contesta si el perfil de sandbox está IMPUESTO por la capa de requisitos.
//
// Tres respuestas, y la tercera es la que este repositorio no perdona omitir: «existe y no se ha
// podido leer» NO es «no está puesto». El agente sí lo va a leer; nosotros no hemos podido, y
// afirmar ausencia sobre algo que no se ha mirado es la única clase de verde que aquí es un
// defecto.
func (s *Source) hallazgoImposicion(req grokConfig, estado estadoConfig, at time.Time) model.FindingReport {
	base := model.FindingReport{
		Kind:        findingKindPosture,
		SubjectKind: subjectSandbox,
		SubjectRef:  s.agentRef,
		OccurredAt:  at,
	}
	switch estado {
	case configLeido:
		if perfil := strings.TrimSpace(req.Sandbox.Profile); perfil != "" {
			base.Severity = model.SeverityInfo
			base.Title = "Grok Build's sandbox profile is ENFORCED in " + s.requirementsPath +
				": " + perfil + " — the requirements layer constrains it above the user, GROK_CONFIG, and --sandbox"
			return base
		}
		base.Severity = model.SeverityMedium
		base.Title = s.requirementsPath + " exists but does NOT set [sandbox] profile: the profile " +
			"is still selected by --sandbox, config.toml, and GROK_SANDBOX, and can be enforced by adding it there"
		return base
	case configIlegible:
		base.Severity = model.SeverityMedium
		base.Title = s.requirementsPath + " exists and could NOT be read: no claim is made about " +
			"whether the sandbox profile is enforced — this is not \"not enforced\"; it is \"not measured\""
		return base
	default:
		base.Severity = model.SeverityMedium
		base.Title = "Grok Build's sandbox profile is NOT enforced and CAN be enforced: there is no " +
			s.requirementsPath + " (system layer that constrains it above --sandbox, config.toml, and GROK_SANDBOX)"
		return base
	}
}
