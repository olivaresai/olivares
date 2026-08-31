---
title: "Scoping de fuentes y credenciales"
description: >-
  Vincula una fuente conectada — un servidor MCP, modelo, proveedor, base de
  conocimiento o fuente de datos — a un workspace o grupo de agentes, y resuelve,
  en el punto en que un agente o sesión la solicita, si el actor está en alcance y
  qué referencia de credencial aplica. Deny-closed por construcción.
---

El scoping de fuentes y credenciales (`modules/sourcescope`) responde a una
única pregunta en tiempo de ejecución: cuando un agente o sesión solicita una
fuente conectada — un servidor MCP, un modelo, un proveedor, una base de
conocimiento o una fuente de datos — **¿está este actor en alcance, y qué
referencia de credencial aplica?** Está **LIVE**: la tabla de bindings, su API de
escritura y el resolver que invocan los PEP del runtime se envían todos en el
binario.

Es un módulo y no una columna porque el alcance que impone no es una propiedad de
ninguna entidad fuente concreta — la config MCP, los modelos, los proveedores y
las bases de conocimiento viven en módulos distintos, y solo el eje
agente/sesión/recurso lleva siquiera un workspace. El alcance es un **binding**:
`(source) → (workspace o agent-group)`, con una referencia de credencial
scopeada opcional. Este módulo posee esa tabla de bindings y el resolver.

## El binding y su API

`/v1/m/sourcescope/bindings` es una superficie CRUD estándar, gobernada por
`sourcescope:binding:read` y `:binding:write`. Un binding apunta a un tipo de
fuente (`mcp`, `model`, `provider`, `knowledge`, `data`) y un árbol de alcance
(`workspace`, `agent_group`), y lleva una **`CredRef` libre de valor** — un
nombre lógico, un localizador `ref_kind` (`env`, `vault`, `secret_manager`,
`file`, `other`) y una pista enmascarada opcional. Ningún campo puede contener un
secreto usable; el handler rechaza una credencial inline, el mismo invariante de
datos mínimos que `capabilities.mcp_config.secret_refs`.

## Cómo decide el Resolver

La decisión es deny-closed y compuesta, no un segundo motor de autorización:

- **Contención** — una fuente vinculada al workspace W es resoluble por un agente
  o sesión en W sin más configuración.
- **Grant** — un grant Cedar scopeado y abarcador de
  [`x-models`](/es/reference/modules/x-models/), procedente de
  [`vi-governance`](/es/reference/modules/vi-governance/), abre un workspace ajeno.
- **RBAC** — la autoridad a nivel de tenant sigue viéndolo todo; el workspace es
  soft-isolation, el tenant es la frontera dura.
- **Forbid** — un forbid Cedar scopeado anula todo lo anterior.

El gate es **aditivo**: una fuente sin binding permanece global por
retrocompatibilidad; una fuente con binding sin alcance contenedor, sin grant y
sin RBAC es **denegada**. El resolver se cablea como el `ScopeGate` en la cadena
de ejecución de modelos y en la recuperación de
[`viii-knowledge`](/es/reference/modules/viii-knowledge/).

## Contexto acotado, dicho con claridad

- Esto es **solo vinculación de referencias**. El **consumo** de credenciales
  scopeadas en una llamada real a un proveedor, y un **broker MCP** en runtime que
  se conecte a un servidor en nombre de un agente, **aún no existen in-tree** — el
  resolver devuelve la referencia en alcance, pero nada aquí la usa para
  autenticar una llamada saliente.
- El alcance del actor proviene del agente/sesión **nombrado por la referencia de
  actor del llamante**. Los valores de alcance se leen de la fila almacenada (un
  llamante no puede inyectar un workspace), pero la elección del agente es del
  llamante; vincular esa referencia al principal es un follow-up de
  endurecimiento. Véase
  [honestidad y límites](/es/start/honesty-and-limits/).

## Relacionado

- [Gobernanza (vi)](/es/reference/modules/vi-governance/) — el álgebra de
  grant/forbid Cedar y el RBAC que el resolver compone.
- [Modelos (x)](/es/reference/modules/x-models/) — la cadena de ejecución donde corre
  el `ScopeGate`.
- [Conocimiento (viii)](/es/reference/modules/viii-knowledge/) — la recuperación
  gobernada, el segundo lugar donde el resolver gobierna.
