> Traducción automática. La versión en inglés es la fuente autoritativa.

# ADR-0005: SQLite embebido por defecto, Postgres + RLS para escala

- **Status:** accepted
- **Date:** 2026-06
- **Deciders:** Fran Olivares
- **References:** stack decisions register (T4); data-model design

## Contexto y planteamiento del problema

El control plane almacena un modelo de datos multi-tenant (el grafo de acceso es una
*vista* sobre él). Debe ejecutarse como un único binario sin dependencias para
instalaciones pequeñas/air-gapped, y a la vez escalar a despliegues multi-host y
multi-tenant.

## Factores de la decisión

- Cero dependencias externas para la vía de binario único / air-gap.
- Aislamiento multi-tenant robusto a escala.
- Sin CGO, para preservar un binario estático en Go puro.

## Opciones consideradas

- **SQLite (Go puro) → Postgres + row-level security.**
- **Una base de datos de grafos** (Neo4j, Dgraph) para el grafo de acceso.

## Resultado de la decisión

Opción elegida: **SQLite embebido** (`modernc.org/sqlite`, Go puro, sin CGO) para nodo
único y air-gap; **Postgres** (vía `pgx`) con **row-level security** indexada por
`tenant_id` para multi-host, escala y multi-tenancy. El grafo de acceso se modela como una
**vista sobre el modelo de datos general**, no como un almacén aparte.

### Consecuencias

- **Bueno:** el binario único no tiene ninguna base de datos que instalar; el mismo modelo
  escala a Postgres con aislamiento RLS por tenant.
- **Malo / contrapartidas:** dos backends de almacenamiento que mantener; la corrección del
  RLS debe probarse (y se prueba — bajo RLS forzado en CI).
- **Neutral:** el grafo de acceso no necesita ningún motor de grafos especial porque es una
  vista.

## Por qué se rechazaron las alternativas

- **Base de datos de grafos** — pesada de auto-hospedar y excesiva: el grafo de acceso es
  una vista sobre el modelo relacional, no una carga de trabajo que necesite un motor de
  grafos dedicado.
