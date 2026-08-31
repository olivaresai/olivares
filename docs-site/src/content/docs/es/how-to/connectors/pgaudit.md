---
title: "PostgreSQL pgAudit (R/RW de nivel limpio)"
description: >-
  Captura el acceso de lectura/escritura a PostgreSQL desde su rastro nativo
  pgAudit — la señal de nivel limpio: READ/WRITE tomados literalmente del CLASS
  de auditoría, nunca inferidos del SQL, con el conector leyendo solo el fichero
  de log.
sidebar:
  order: 1
---

La fuente `pgaudit` convierte el propio rastro de auditoría de PostgreSQL en
aristas del mapa de accesos: una arista por cada acceso a datos auditado, con el
modo de lectura/escritura tomado **literalmente del campo CLASS de pgAudit** —
nunca inferido del texto SQL. Es la fuente canónica de **nivel limpio**: un store
relacional/de objetos que clasifica el acceso en su rastro nativo.

El conector es **de solo lectura sobre un fichero de log**. Nunca se conecta a la
base de datos, nunca ve los resultados de las consultas y nunca captura el cuerpo
del SQL — la identidad, el objeto y la clasificación son todos salida propia de
pgAudit.

## Qué emite

| Campo | Valor |
|---|---|
| Fuente de señal | `pg_audit` |
| Modo | desde CLASS, literal: READ → `read`, WRITE → `write`, DDL → `write` (una escritura de esquema), FUNCTION → `unknown` (pgAudit no lo indica); ROLE/MISC se omiten, no se adivinan |
| Origen | el `application_name` si está presente (→ `attributed`), si no el rol de sesión |
| Confianza | `attributed`, o `approximate` para roles/apps que declares compartidos |
| Nivel de cobertura | clean |

## 1. Activar pgAudit, logs estructurados, UTC

En el lado de PostgreSQL (la configuración estándar de pgAudit — consulta la
documentación de pgAudit para tu versión mayor):

```ini
# postgresql.conf
shared_preload_libraries = 'pgaudit'
pgaudit.log = 'read, write'        # the classes this source consumes
logging_collector = on
log_destination = 'csvlog'         # or 'jsonlog' (PostgreSQL 15+)
log_timezone = 'UTC'               # REQUIRED — see below
```

Dos restricciones provienen de cómo parsea el conector, ambas verificadas contra
su implementación:

- **El servidor debe registrar en UTC.** PostgreSQL escribe las marcas de tiempo
  con una *abreviatura* de zona, y una abreviatura no-UTC no puede resolverse de
  forma fiable a un offset — así que el conector **omite** tales registros en
  lugar de adivinar una marca de tiempo errónea. `log_timezone = 'UTC'` es la
  configuración soportada.
- **`csvlog` es por lotes; `jsonlog` puede seguirse.** Los registros csvlog
  pueden abarcar varios saltos de línea, así que ese formato se lee como un lote
  en cada pasada; `jsonlog` está delimitado por líneas y soporta seguimiento
  continuo (`follow`, el valor por defecto).

Para que la atribución sea precisa, haz que las aplicaciones establezcan
`application_name` por agente — eso es lo que eleva una arista de un rol
compartido a un origen atribuido (consulta
[la dependencia de identidad](/es/how-to/connect-a-source/#la-dependencia-dura-identidad-por-agente)).

## 2. Declarar la fuente

En tu [configuración de fuentes](/es/how-to/connect-a-source/#cablear-una-fuente-real)
(`OLIVARES_SOURCES_CONFIG`):

```json
{
  "sources": [{
    "name": "salesdb-pgaudit",
    "kind": "pgaudit",
    "tenant": "<tenant-id>",
    "config": {
      "log_path": "/var/log/postgresql/postgresql.csv",
      "format": "csvlog",
      "shared_accounts": "etl_role,app_pool"
    }
  }]
}
```

Claves de configuración (del descriptor que envía el conector):

| Clave | Obligatoria | Por defecto | Significado |
|---|---|---|---|
| `log_path` | sí | — | ruta al fichero de log de PostgreSQL que el host del motor puede leer |
| `format` | no | `csvlog` | `csvlog` o `jsonlog` |
| `follow` | no | `true` | seguir continuamente (**solo jsonlog** — csvlog es por lotes) |
| `shared_accounts` | no | — | roles / application_names separados por comas que son compartidos; sus aristas se marcan honestamente como `approximate` |

Reinicia el motor y confirma la línea de arranque
`ingest: wired source … kind=pgaudit`.

## 3. Qué verás en la consola

Abre el **Mapa de accesos**. Cada acceso auditado se representa como una arista
del rol o la aplicación hacia la tabla, coloreada como lectura o escritura, con
la insignia de cobertura `CLEAN` en los recursos Postgres. El panel **Permitido
frente a observado** revela cualquier acceso sin una concesión que lo respalde —
con pgAudit conectado y sin concesiones declaradas todavía, *cada* acceso
observado es deriva honesta, que es el primer estado esperado.

## Límites honestos

- **Ve lo que pgAudit registra.** Las clases que no actives (`pgaudit.log`) no se
  observan; una ausencia de aristas no es prueba de ausencia de acceso si la
  clase está desactivada.
- **La atribución es la de la base de datos.** Un rol compartido sin
  `application_name` colapsa a varios llamantes en una sola identidad —
  decláralo en `shared_accounts` para que el mapa diga `approximate` en lugar de
  fingir.
- **FUNCTION es `unknown` por diseño** — ejecutar una función puede leer o
  escribir, y pgAudit no indica cuál; el producto no forzará una etiqueta. Las
  clases no relativas a datos (ROLE, MISC) se omiten en lugar de emitirse como
  aristas sin sentido.

## Relacionado

- [Conectar una fuente](/es/how-to/connect-a-source/) — el modelo de conector y la
  taxonomía de niveles honestos.
- [CloudTrail](/es/how-to/connectors/cloudtrail/) — la misma idea de nivel limpio
  para objetos S3.
- [Conectores y niveles de cobertura](/es/reference/connectors/) — el catálogo
  completo.
