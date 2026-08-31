---
title: "AWS CloudTrail para S3 (nivel limpio R/RW)"
description: >-
  Captura el acceso de lectura/escritura a objetos de S3 a partir de los data
  events de CloudTrail — el flag readOnly tomado al pie de la letra, el
  principal IAM como origen y una atribución aproximada honesta cuando un rol
  asumido oculta al llamante real.
sidebar:
  order: 2
---

La fuente `s3cloudtrail` convierte los **data events de S3** de AWS CloudTrail
en aristas del access map: una arista por evento de S3, con el modo de
lectura/escritura tomado **al pie de la letra del campo `readOnly` de
CloudTrail** — nunca inferido — y el principal IAM al que CloudTrail atribuye la
llamada como origen. Es el nivel limpio para almacenamiento de objetos, la
contraparte en S3 de [pgAudit](/es/how-to/connectors/pgaudit/) para Postgres.

El conector **lee ficheros de log locales y nunca llama a AWS**: tú entregas los
ficheros de CloudTrail (el layout estándar de entrega en S3 que tu trail ya
produce) y él los parsea. Solo se procesan los eventos con
`eventSource == s3.amazonaws.com` — los eventos del management plane pertenecen
al [conector de descubrimiento cloud `aws`](/es/reference/connectors/), no a este.

## Qué emite

| Campo | Valor |
|---|---|
| Fuente de señal | `cloudtrail` |
| Modo | `readOnly: true` → `read`, `false` → `write`, ausente → `unknown` — al pie de la letra, nunca adivinado |
| Origen | el principal IAM (usuario, sesión de rol asumido, servicio de AWS) |
| Confianza | `attributed`; `approximate` para roles asumidos compartidos y llamadas invocadas por servicios |
| Nivel de cobertura | clean |

## 1. Prerrequisitos del lado de AWS

- Un **trail de CloudTrail con los data events de S3 habilitados** para los
  buckets que gobiernas (los data events no están en el trail de management por
  defecto).
- Entrega de los ficheros de log del trail a una ubicación que el host del motor
  pueda leer — el bucket de entrega estándar de S3, sincronizado o montado
  localmente. El conector acepta los ficheros clásicos `{"Records":[…]}` (en
  texto plano o `.json.gz`) y los registros delimitados por saltos de línea.

## 2. Declarar la fuente

```json
{
  "sources": [{
    "name": "prod-s3-trail",
    "kind": "s3cloudtrail",
    "tenant": "<tenant-id>",
    "config": {
      "path": "/var/lib/cloudtrail/prod/",
      "shared_accounts": "arn:aws:iam::123456789012:role/app-runtime"
    }
  }]
}
```

| Clave | Requerida | Significado |
|---|---|---|
| `path` | sí | un fichero de CloudTrail, o un directorio de ficheros `*.json` / `*.json.gz` |
| `shared_accounts` | no | ARNs de roles separados por comas que muchos llamantes comparten — sus aristas son honestamente `approximate` |

(`s3-cloudtrail` se acepta como alias del `kind`.)

## 3. Qué verás en la consola

Los buckets y objetos de S3 se incorporan al **Access map** con insignias de
nivel limpio; las lecturas y escrituras se colorean a partir del flag `readOnly`.
El panel de drift las cruza contra los grants declarados exactamente igual que
con cualquier otra fuente.

En **Inventory**, los principals a los que CloudTrail atribuye las llamadas
aparecen como identidades, listos para vincularse a agentes — ese vínculo es lo
que convierte un `approximate` de rol compartido en un `attributed` por agente.

## Límites honestos — léelos antes de fiarte del mapa

- **Un rol asumido compartido por muchos llamantes no puede nombrar al llamante
  real.** CloudTrail atribuye la llamada a la sesión del rol; si el rol está
  compartido, la arista es deliberadamente `approximate`. Declarar el rol en
  `shared_accounts` lo hace explícito. El arreglo duradero es la identidad por
  agente ([la dependencia de identidad](/es/how-to/connect-a-source/#la-dependencia-dura-identidad-por-agente)).
- **Los data events que no habilitaste no existen.** CloudTrail solo registra
  lo que el trail está configurado para registrar; la ausencia de una arista no
  es ausencia de acceso si los data events están desactivados para un bucket.
- **La latencia de entrega es la de CloudTrail.** Los data events llegan según
  el calendario de entrega de CloudTrail (normalmente minutos); esta fuente no
  es una toma en tiempo real.

## Relacionado

- [pgAudit](/es/how-to/connectors/pgaudit/) — la misma disciplina de nivel limpio
  para PostgreSQL.
- [Conectar una fuente](/es/how-to/connect-a-source/) — el modelo de conectores.
- [Conectores y niveles de cobertura](/es/reference/connectors/) — dónde se sitúa
  honestamente cada store.
