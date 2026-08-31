---
title: "Postgres como fuente de contexto gobernada"
description: "Conecta una base de datos PostgreSQL como fuente de conocimiento gobernada y de solo lectura: materializa filas como documentos, mapea ACLs de forma honesta, clasifica columnas sensibles y mantén la garantía de solo lectura por construcción."
---

El conector de contenido `postgres` (`olivares.pg-content`) permite apuntar el control plane
a una base de datos PostgreSQL y convertir sus filas en **documentos de conocimiento
gobernados** que fluyen por el mismo pipeline que cualquier otra fuente de contenido —
expurgar → clasificar → chunk → embed → indexar → servir por MCP — con ACLs por documento y
clasificación por columna.

Es el equivalente, para bases de datos operacionales, de las fuentes de contenido SaaS/almacén
(gdrive, confluence, s3content, snowflake…). Dos cosas que **no** es:

- **No es `pgaudit`.** `pgaudit` observa *aristas de acceso* R/RW para el mapa de accesos;
  nunca lee el contenido de las filas. `pg-content` materializa *filas como documentos*. Son
  conectores distintos para trabajos distintos.
- **No es NL-a-SQL.** Este conector ingiere filas como contenido; **no** genera SQL a partir
  de lenguaje natural en tiempo de consulta. (Algunos incumbentes llaman "knowledge base con
  datos estructurados" a una función text-to-SQL — eso es una superficie de agente, no una
  fuente de contenido gobernada. Este conector es deliberadamente lo segundo.)

## Solo lectura por construcción

El conector nunca escribe en tu base de datos, y lo garantiza en **tres capas
independientes**, de modo que una escritura es imposible, no solo desaconsejada:

1. **Consultas solo-SELECT.** El conector solo *construye* sentencias `SELECT`. Si aportas
   tu propia `query`, se valida que sea un único `SELECT`/`WITH` de solo lectura — una
   segunda sentencia, un CTE que modifica datos (`WITH x AS (DELETE …)`), `COPY`, `SELECT …
   INTO` o cualquier DDL se rechaza en `Open`, fail-closed.
2. **Una sesión de solo lectura.** Cada sentencia corre en una transacción `READ ONLY` sobre
   una sesión abierta con `default_transaction_read_only = on`, así que el propio PostgreSQL
   rechaza cualquier escritura. En `Open` el conector *verifica* que la sesión es de solo
   lectura y se niega a arrancar si no lo es — una garantía de postura, no un consejo.
3. **Un rol de mínimo privilegio.** Le das al conector un rol con `SELECT` y nada más. Ver el
   rol de referencia abajo.

Esto es más fuerte que cualquier incumbente gestionado, que documenta el solo-lectura solo
como *recomendación*.

### El rol de mínimo privilegio

```sql
CREATE ROLE olivares_ro LOGIN PASSWORD '…';
GRANT USAGE  ON SCHEMA public TO olivares_ro;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO olivares_ro;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO olivares_ro;
-- Nunca concedas INSERT/UPDATE/DELETE/DDL. Opcionalmente fija el rol a solo lectura:
ALTER ROLE olivares_ro SET default_transaction_read_only = on;
```

Concede `SELECT` solo sobre las tablas que vayas a ingerir para el alcance más estricto.

## Define cómo una fila se convierte en documento

La definición del documento es declarativa — indicas qué columnas son la clave, el cuerpo,
el título, la ACL, la clasificación y el cursor de sync:

```jsonc
// OLIVARES_SOURCES_CONFIG — las fuentes de documentos van bajo "documents"
{
  "documents": [
    {
      "name": "support-articles",
      "kind": "postgres",
      "config": {
        "mode": "live",
        "dsn": "vault:secret/data/pg-ro#dsn",   // REFERENCIA al secret store, nunca inline
        "schema": "public",
        "table": "kb_articles",
        "key_columns": "id",                     // el id estable del documento
        "body_columns": "title,body",            // concatenadas en el cuerpo del documento
        "title_column": "title",
        "updated_at_column": "updated_at",       // dirige el sync incremental (delta)
        "acl_columns": "owner_group",            // → ACL "group:<valor>"
        "acl_prefix": "group:",
        "classification_column": "sensitivity",
        "sensitive_columns": "email,ssn",        // → etiqueta externa "pii:<columna>"
        "sensitive_label": "pii",
        "metadata_columns": "url_path",
        "sslmode": "require",
        "statement_timeout": "30s",
        "max_rows": "100000"
      }
    }
  ]
}
```

En lugar de una `table` puedes dar una `query` de solo lectura (un `SELECT` validado) — útil
para unir una tabla de ACL o filtrar las filas que quieres exponer. La credencial es siempre
una **referencia al secret store** (`vault:…`, `aws-secretsmanager:…`, …); un secreto en
claro se rechaza.

## Por qué el mapeo de ACL es *honesto*

El conector mapea **solo lo que la fila expresa**. Construye la ACL de un documento a partir
de los valores de tus `acl_columns` declaradas (p. ej. una columna `owner_group` → `group:eng`).
**No** inventa una ACL por-fila que la fuente no exprese, y hace explícitos estos límites:

| Situación | Qué hace el conector |
|---|---|
| Una columna `owner_group` / de rol | Mapea cada valor a una referencia ACL (`<acl_prefix><valor>`). |
| Sin `acl_columns` declaradas | El documento hereda la **ACL por defecto** del knowledge base — retrieval la sigue aplicando. |
| **Row-level security (RLS)** en la tabla | Respetada implícitamente: el rol del conector ve exactamente las filas que RLS le permite. El conector no reimplementa RLS; la hereda. |
| Un permiso que la tabla **no** modela como columna | **No derivable** → no se mapea. Modélalo como columna (o una tabla ACL unida vía `query`) si quieres que se aplique. |

Esta es la diferencia deliberada con los incumbentes gestionados, que te obligan a
escribir a mano las columnas de ACL *y* no ofrecen passthrough de RLS. Aquí también mapeas a
mano las columnas de ACL, **pero** el conector además respeta RLS y nunca fabrica permisos
que la fila no tiene.

## Clasificación por columna

Lista las columnas sensibles en `sensitive_columns`. Cuando una fila tiene valor en una, el
documento gana una etiqueta externa `"<sensitive_label>:<columna>"` (p. ej. `pii:ssn`). Estas
etiquetas alimentan el DLP de retrieval y se aplican deny-closed junto a la
`classification_column` de la fila.

## Live vs export

- **`mode: live`** lee la base de datos por el pool de solo lectura y admite **sync
  incremental (delta)** por el cursor `updated_at_column`, con reconciliación full-list como
  fallback cuando no hay cursor configurado.
- **`mode: export`** parsea un snapshot estático de filas (un dump JSON que produces aparte).
  Un snapshot **nunca se presenta como live** — la fuente señala su modo honestamente.

## Límites honestos

- El **cuerpo de un documento se limita a 1 MiB**; una fila mayor se trunca (el streaming de
  columnas muy grandes es un seguimiento).
- Una **columna con el nombre literal de una palabra clave SQL** (p. ej. `update`) en una
  `query` aportada por el operador debe llevar alias — el guard de solo lectura es fail-closed.
- El conector lee contenido; **actuar sobre la base de datos está fuera de alcance** (no hay
  ruta de escritura, por diseño), igual que el streaming CDC y el NL-a-SQL.

## Wire-proof

El conector incluye un E2E wire-proof (`-tags e2e`, CI) que corre contra un PostgreSQL real:
verifica la sesión de solo lectura en `Open`, ingiere filas semilla con su ACL/clasificación
mapeadas, y prueba que una escritura sobre la sesión de solo lectura es **rechazada** por
PostgreSQL. Ver `connectors/pgcontent/testdata/docker-compose.e2e.yml`.
