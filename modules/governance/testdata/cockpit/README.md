<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Fixture Cedar del cockpit de sesiones

Las políticas de ejemplo de la arquitectura del cockpit (§7.3), **escritas contra el evaluador real** de este
árbol (cedar-go v1.8.0, `modules/governance/grants.go`) en vez de contra el modelo de entidades que
el documento de diseño supone. La batería que las ejecuta es
`modules/governance/cockpit_policy_test.go`.

## ⛔ La traducción que hubo que hacer, y por qué NO es cosmética

La v3 escribe sus ejemplos como `resource is Session`, `resource is ShellTarget` y
`resource is InputTarget`. **Este motor no materializa ningún tipo de entidad así.** Medido en el
árbol:

- el recurso es **siempre** `Resource::"<id>"` (`modules/governance/grants.go`, `resourceUID`), y el
  KIND viaja como **atributo**, no como tipo, *«so a dotted kind like "core.agent" (not a valid
  Cedar type name) never breaks compilation»* (`modules/governance/cedar.go:32-34`);
- la acción es `Action::"<permiso de ruta>"` (`modules/governance/grants.go`, `actionUID`), no un
  Action ID separado. El seam de acción por ruta es el que traducirá permiso→Action ID;
  **hoy no existe**, así que la fixture usa el permiso de ruta.

**El coste de no traducirlas sería silencioso, que es lo caro:** una condición sobre un atributo
que Cedar no puede resolver **hace que la regla ERRE y Cedar la salta**. Un `forbid` escrito con
`resource is Session` no fallaría ruidosamente: **no confinaría a nadie y nadie lo notaría**. Es
exactamente el motivo por el que `baseResourceAttrs` mantiene `kind` y `sensitivity` siempre
presentes (`modules/governance/grants.go`, comentario de `baseResourceAttrs`).

⇒ La fixture es la **prueba de que las políticas del diseño se pueden expresar en este motor**, y
la forma en que se expresan. Cuando el seam `CedarAction` aterrice, cambia el lado izquierdo de
`action ==` y nada más.

## Los ficheros

| Fichero | Qué demuestra |
|---|---|
| `department-forbid.cedar` | el confinamiento por departamento como **forbid-unless**, que es la única forma que confina bajo `(RBAC ∨ Grant)` |
| `delegated-transcript.cedar` | un **permit positivo** que habilita a un viewer delegado sobre un AgentGroup |
| `terminal-operators.cedar` | los permits exactos de `shell:open` e `input:write`, con host, UID, clasificación y AAL3 |

Las tres se compilan y se evalúan contra el motor real en la batería; ninguna se da por buena por
leerla.
