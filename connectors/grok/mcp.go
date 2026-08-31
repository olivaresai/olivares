// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package grok

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

// mcp.go mide la superficie MCP de Grok Build: qué servidores puede alcanzar una sesión y si un
// administrador lo ha acotado.
//
// ⛔ TODO LO QUE AFIRMA SALE DEL FUENTE PÚBLICO, no de la documentación — que ya nos falló tres
//    veces en este mismo conector. Commit anclado en `session/hook.go`:
//
//    · `xai-grok-config/src/config_layers.rs:42-51` — el overlay `GROK_CONFIG` funciona por LISTA
//      BLANCA, y **toda tabla peligrosa está fuera de ella y se descarta por defecto**:
//      `mcp_servers`, `auth_provider`, `model_providers`, `plugins`, `auth`, `endpoints`,
//      `marketplace`… Su comentario lo dice explícito: «a newly added dangerous table stays out
//      until it is explicitly allowlisted», y «the overlay therefore cannot spawn a new command
//      sink, set auth policy, redirect [egress]».
//    · `config_layers.rs:244-264` — `reapply_requirements` vuelve a fusionar los requisitos
//      DESPUÉS de las campañas y del overlay, y MDM va el último. Su propia prueba
//      (`effective_config_mdm_requirements_win_over_system_and_user`) lo formula así: «an
//      admin-forced value clamps the effective config».
//
//    ⇒ Dos hechos con signo OPUESTO, y hay que dar los dos: la variable de entorno **no** puede
//    añadir un servidor MCP (bueno, y es fail-closed por diseño de xAI), y el fichero de
//    requisitos **sí** puede fijar la lista (bueno, y es la palanca del administrador). Contar
//    sólo el segundo dejaría al operador creyendo que tiene que defenderse de una vía que ya
//    está cerrada.

// hallazgosMCP construye las observaciones de la superficie MCP.
//
// `cfg` es la configuración del usuario y `req` la de requisitos; sus estados van aparte porque
// «no hay fichero» y «hay fichero y no se ha podido leer» son hechos distintos, y colapsarlos
// daría un verde sobre algo no mirado.
func (s *Source) hallazgosMCP(cfg grokConfig, estado estadoConfig, req grokConfig, reqEstado estadoConfig, at time.Time) []model.FindingReport {
	out := make([]model.FindingReport, 0, 3)

	// ── 1 · Qué servidores alcanza la sesión ──────────────────────────────────────────
	base := model.FindingReport{
		Kind:        "inventory",
		SubjectKind: subjectMCP,
		SubjectRef:  s.agentRef,
		OccurredAt:  at,
	}
	switch estado {
	case configLeido:
		nombres := ordenados(cfg.MCPServers)
		if len(nombres) == 0 {
			base.Severity = model.SeverityInfo
			base.Title = "Grok Build's config.toml declares no MCP servers: the session reaches none through that path"
		} else {
			// ⛔ LA LISTA, NO LA CIFRA. «3 servidores MCP» obliga a otra sesión a ir a buscar
			//    cuáles, y el nombre es lo único que permite decir si alguno no debería estar.
			base.Severity = model.SeverityInfo
			base.Title = "MCP servers declared in Grok Build's config.toml (" +
				strconv.Itoa(len(nombres)) + "): " + strings.Join(nombres, ", ")
		}
	case configIlegible:
		base.Severity = model.SeverityMedium
		base.Title = "Grok Build's config.toml exists and could NOT be read: no claim is made " +
			"about which MCP servers the session can reach — this is not \"none\"; it is \"not measured\""
	default:
		base.Severity = model.SeverityInfo
		base.Title = "There is no Grok Build config.toml: no user-declared MCP servers were observed"
	}
	out = append(out, base)

	// ── 2 · ¿Los ha fijado un administrador? ──────────────────────────────────────────
	imp := model.FindingReport{
		Kind:        findingKindPosture,
		SubjectKind: subjectMCP,
		SubjectRef:  s.agentRef,
		OccurredAt:  at,
	}
	switch reqEstado {
	case configLeido:
		if nombres := ordenados(req.MCPServers); len(nombres) > 0 {
			imp.Severity = model.SeverityInfo
			imp.Title = "MCP servers are PINNED in " + s.requirementsPath + " (" +
				strings.Join(nombres, ", ") + "): the requirements layer constrains them above the user"
		} else {
			imp.Severity = model.SeverityMedium
			imp.Title = s.requirementsPath + " exists but does NOT pin [mcp_servers]: the user " +
				"decides the server list, which can be constrained by adding it there"
		}
	case configIlegible:
		imp.Severity = model.SeverityMedium
		imp.Title = s.requirementsPath + " exists and could NOT be read: no claim is made about " +
			"whether MCP servers are pinned — this is not \"not pinned\"; it is \"not measured\""
	default:
		imp.Severity = model.SeverityMedium
		imp.Title = "Grok Build MCP servers are NOT pinned and CAN be pinned: there is no " +
			s.requirementsPath + " (system layer that constrains them above the user's configuration)"
	}
	out = append(out, imp)

	// ── 3 · La vía que YA está cerrada, y hay que decirlo ─────────────────────────────
	//
	// Un operador que no sepa esto gastará esfuerzo defendiendo una puerta que el propio agente
	// tiene cerrada por diseño, y —peor— puede concluir que la variable de entorno es un agujero
	// y desconfiar de todo lo demás.
	out = append(out, model.FindingReport{
		Kind:        "inventory",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectMCP,
		SubjectRef:  s.agentRef,
		Title: "GROK_CONFIG cannot add an MCP server: the overlay uses an allowlist, and the " +
			"execution, auth, egress, and discovery tables are excluded, so they are dropped by " +
			"default (xAI's fail-closed behavior, not ours)",
		OccurredAt: at,
	})

	return out
}

// ordenados devuelve los nombres en orden estable. Un hallazgo cuyo texto cambia de orden entre
// dos corridas produce un digest distinto para el mismo hecho, y eso rompe la deduplicación.
func ordenados(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		if k = strings.TrimSpace(k); k != "" {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
