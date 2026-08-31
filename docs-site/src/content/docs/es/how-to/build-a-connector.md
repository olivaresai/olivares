---
title: Construir y publicar un connector
description: >-
  Genera el esqueleto, implementa, prueba, firma y distribuye un connector de
  terceros con el SDK de conectores público Apache-2.0 — y cabléalo en un
  control plane con admisión firmada deny-closed.
---

Esta guía te lleva desde cero hasta un **connector de terceros firmado** que un
operador puede cablear en el control plane. El SDK de conectores es Apache-2.0 y no
importa nada del motor AGPL, así que tu connector es **tu** código bajo **tu**
licencia, construido en **tu** repositorio.

Lo que construyes es un programa Go normal: un tipo que implementa
`sdk.SourceConnector` (reúne hechos, emite observaciones) o `sdk.OutputConnector`
(entrega notificaciones), o `sdk.ContentSource` (sirve documentos y referencias
ACL al conocimiento gobernado), empaquetado como un binario
[go-plugin](https://github.com/hashicorp/go-plugin) que el motor lanza fuera de
proceso y con el que habla por gRPC (loopback mutuamente autenticado, AutoMTLS). Lee
primero [conectar una fuente](/es/how-to/connect-a-source/) para el *modelo* de connector
— observe-only, minimal-data, los tres tipos de observación.

:::note[Estabilidad]
El contrato del SDK (`Descriptor/Open/Gather/Close`, el cable, el handshake del
plugin) es **stable v1** — consulta
[estabilidad de la API](/es/reference/api-stability/) y `sdk/VERSIONING.md` en el
repositorio. Hasta que se publiquen los primeros tags semver públicos, compila contra
un checkout del repositorio (`-sdk-path` más abajo).
:::

## 1. Generar el esqueleto

Porcelain recomendado:

```sh
# from the repository checkout root
go run ./cmd/olivares connector init acme.widget-audit \
  --dir ~/olivares-connector-widget \
  --module github.com/acme/olivares-connector-widget \
  --template access-edge-source \
  --plugin \
  --sdk-path "$PWD/sdk"
```

Elige uno de los cinco arquetipos. Son presets sobre superficies estables del
SDK, no contratos nuevos para autores:

| Template | Superficies declaradas | Cuándo usarlo |
|---|---|---|
| `content-source` | `knowledge.document` | Documentos para ingesta de conocimiento gobernado, incluidas content sources fuera de proceso. |
| `access-edge-source` | `observation.edge` | Hechos de grafo de acceso, identidad, SaaS e infraestructura. |
| `output-sink` | `notify.sink` | Sinks de notificación o tickets. |
| `agent-surface` | `observation.edge`, `observation.finding` | Adaptadores de runtime de agentes que reportan edges y findings. |
| `model-provider` | `observation.cost`, `observation.edge` | Inventario de proveedores, uso y coste; la gobernanza de modelos queda en el motor. |

El scaffold standalone anterior sigue siendo válido y genera los mismos
contratos estables:

Ejecuta esto desde un checkout del repositorio (hasta que se publiquen los primeros
tags públicos del SDK, el paquete se resuelve a través del workspace, y `-sdk-path`
apunta al `sdk/` de ese checkout):

```sh
# from the repository checkout root
go run ./sdk/scaffold/cmd/olivares-connector-new \
  -dir ~/olivares-connector-widget \
  -name acme.widget-audit \
  -module github.com/acme/olivares-connector-widget \
  -kind source -plugin \
  -sdk-path "$PWD/sdk"
```

Obtienes un repositorio completo: el esqueleto del connector, un test de ciclo de vida,
el `main` del plugin, un README con todo este ciclo de vida y
`scripts/check-boundary.sh` — la **misma comprobación de frontera de licencia que
ejecuta nuestro CI**, para el tuyo. `-name` es tu `Descriptor.Name`: globalmente único,
con puntos, `<vendor>.<connector>`.

## 2. Implementar

El contrato, en breve (el godoc de `sdk.SourceConnector` es normativo):

- **`Open`** lee la configuración (declarada en tu `Descriptor.ConfigFields`; los
  secretos son *referencias*, marcados con `Secret: true`, nunca en línea). Falla aquí,
  no en `Gather`.
- **`Gather`** emite observaciones al `Sink` del motor. **El motor es dueño de la
  programación**: una fuente por lotes hace su trabajo y retorna; una fuente en
  streaming se bloquea hasta que se cancela `ctx`. Nunca tengas tu propio ticker.
- La entrega es **at-least-once**; los consumidores deduplican por la clave natural de
  la observación. No lleves el estado de entrega.
- **Minimal data**: emite referencias y metadatos, nunca payloads, prompts ni valores
  de secretos.
- Para `content-source`, **`List`** retorna refs baratos de enumerar,
  **`Fetch`** retorna el cuerpo de un documento, y `DeltaContentSource`
  opcional añade deltas en vivo y refresco de ACL. Los plugins content-source
  que implementan esa interfaz declaran automáticamente `content.delta`; los
  hosts no llaman métodos delta si la capacidad no fue declarada.

Ejecuta tus tests, luego demuestra la frontera de licencia en tu CI:

```sh
go test ./...
./scripts/check-boundary.sh   # fails if anything links github.com/olivaresai/olivares/core
```

## 3. Empaquetar y firmar

Compila el binario del plugin, fija su digest y adjunta una atestación de cadena de
suministro como un **Sigstore bundle**. El control plane verifica la procedencia SLSA
o las atestaciones SBOM (predicados SPDX / CycloneDX) — firma con tu propia clave
(mostrado aquí) o de forma keyless con tu identidad de CI:

```sh
go build -trimpath -o widget-audit ./cmd/acme-widget-audit
sha256sum widget-audit

# keyed (the dev loop: trust your own public key)
cosign generate-key-pair
cosign attest-blob --key cosign.key \
  --type slsaprovenance1 --predicate provenance.json \
  --bundle widget-audit.sigstore.json widget-audit

# keyless alternative (CI): same command with --yes and an OIDC identity,
# or GitHub artifact attestations (gh attestation download produces the bundle).
```

## 4. Distribuir

Publica una **release de GitHub** con el binario, su `sha256` y el bundle
`.sigstore.json` — o empuja los mismos artefactos a un registro OCI con `oras push`
(la atestación como referrer). Versiona con semver; declara la `ProtocolVersion` contra
la que compilaste (v1 hoy) en tu README.

## 5. Operar (lo que hacen tus usuarios)

El operador coloca el binario y el bundle en el host y fija **tanto el digest como la
confianza** en la configuración de fuentes (`OLIVARES_SOURCES_CONFIG`):

```json
{
  "connector_trust": {
    "trusted_keys": ["-----BEGIN PUBLIC KEY-----\n…acme's cosign.pub…\n-----END PUBLIC KEY-----\n"],
    "allowed_predicates": ["https://slsa.dev/provenance/v1"]
  },
  "sources": [
    {
      "name": "widget-prod",
      "tenant": "<tenant-id>",
      "config": { "endpoint_ref": "…" },
      "plugin": {
        "path": "/opt/olivares/plugins/widget-audit",
        "sha256": "<the released digest>",
        "bundle": "/opt/olivares/plugins/widget-audit.sigstore.json"
      }
    }
  ]
}
```

La admisión es **deny-closed, sin escotilla de escape**: sin anclas de confianza, sin
bundle, un digest que no coincide, un firmante no confiable o un tipo de predicado
incorrecto significan todos que la fuente **no se cablea** (el arranque dice por qué).
En caso de éxito el motor vuelve a hacer el hash del binario en el exec (go-plugin
`SecureConfig`) de modo que los bytes verificados sean los bytes ejecutados, y el canal
del subproceso está fijado por AutoMTLS.

Los plugins content-source usan el mismo `connector_trust` raíz y la misma forma
por fuente `plugin { path, sha256, bundle }` dentro del bloque de configuración
`documents`. Ahora son content sources fuera de proceso de primera clase para la
ingesta de conocimiento.

Una ancla de confianza es **obligatoria** — un `connector_trust` sin `trusted_roots`
ni `trusted_keys` se rechaza de plano. Para firma **keyless** el ancla es la raíz de
Fulcio (o de una CA privada), así que el operador define `trusted_roots` (el PEM de la
raíz, p. ej. de `cosign initialize`) **más** `allowed_identities` y `allowed_issuers`
(ambos, juntos — la identidad SAN y el emisor OIDC que debe llevar la firma); solo se
reemplaza `trusted_keys`. El ejemplo de clave simple de arriba es el ancla más sencilla.

## 6. Certificarse (opcional pero recomendado)

Dos registros complementarios:

- **Certificación en el producto** — tus usuarios curan tu connector como una entrada
  de catálogo (kind `connector`, módulo XIV) y registran un veredicto de admisión de
  procedencia/SBOM verificado contra tu digest publicado
  (`POST /entries/{id}/admit`); con `require_signed` activado, la aprobación es
  deny-closed según ese veredicto. Consulta
  [módulo XIV](/es/reference/modules/xiv-catalog/).
- **El índice de connectors verificados** — envía tu connector para su listado en
  [Connectors verificados](/es/reference/verified-connectors/): los mantenedores
  reverifican tu release (frontera, firma, procedencia, revisión de minimal-data) y la
  listan. El índice documenta la verificación; **no** es una raíz de confianza — los
  operadores siguen fijando *tu* identidad/clave ellos mismos.

## Gobernado por construcción

La aplicación de controles vive en el motor por construcción: los connectors no
enlazan código de gobernanza y no pueden optar por salirse. El motor aplica los
controles sobre la identidad de fuente configurada (`source_type`,
`source_ref`): scoping de fuentes, intersección de ACL, escaneo DLP/retrieval,
admisión y auditoría. `Descriptor.Surfaces` es metadato orientativo, nunca una
entrada de enforcement.

Los connectors privados son de primera clase. Puedes mantener un connector
dentro de tu empresa, no publicarlo y no listarlo en ningún índice; sigue estando
gobernado cuando el operador fija el digest del binario y la raíz de confianza.
El índice de connectors verificados documenta certificación; no es una raíz de
confianza.

## Límites honestos (v1)

- El cableado externo cubre **fuentes de observación** y **content sources**; un
  connector de salida se construye y publica de forma idéntica, pero la
  composición de notify todavía no carga plugins de salida externos.
- Los **módulos** fuera de proceso no están disponibles (el proto está congelado, el
  pegamento del host queda intencionadamente sin cablear).
- El sum type de observación está **sellado**: emites edges, cost samples y findings —
  con vocabularios de cadena abiertos — pero no puedes definir nuevos tipos de
  observación.
