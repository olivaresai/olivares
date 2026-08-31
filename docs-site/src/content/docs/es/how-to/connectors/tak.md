---
title: "Postura de TAK Server e ingesta gobernada de Cursor-on-Target"
description: >-
  Gobierna un despliegue TAK: lee la postura de TAK Server desde CoreConfig.xml fuera de línea
  (con una sonda opcional de versión en vivo) e ingiere eventos Cursor-on-Target por UDP/TCP como
  señal gobernada de datos mínimos — las coordenadas y el detalle nunca salen del connector, y
  cada edge es honestamente approximate.
sidebar:
  order: 9
---

La fuente `tak` gobierna un despliegue de **TAK** (Team Awareness Kit) como una superficie más.
Hace dos cosas separables, y puedes activar cualquiera de ellas por sí sola:

- **Postura de TAK Server** — informa la configuración de un servidor (sus entradas y sus
  protocolos/puertos, ajustes TLS/keystore, backend de firma de certificados) como findings de
  datos mínimos. La fuente **fundamentada** es el propio `CoreConfig.xml` del servidor, leído
  **fuera de línea** desde disco; una sonda opcional de **versión** en vivo es lo único que se lee
  por red. **No** lee la federación TAK.
- **Ingesta CoT gobernada** — recibe eventos **Cursor-on-Target** en los propios listeners **UDP**
  y **TCP** del connector y convierte cada uno en un edge de acceso gobernado.

El connector es **read-first**: nunca escribe en un TAK Server, nunca se une a una federación y
nunca vuelve a emitir un payload. Sin credencial ni listener configurados es un **no-op** honesto
— no emite nada en lugar de fabricar una postura para un despliegue que nunca contactó.

## Qué emite

| Campo | Valor |
|---|---|
| Fuente de señal | `cot` |
| Modo | `write` — un emisor CoT *contribuye* estado de conciencia situacional al feed |
| Origen | el `uid` del emisor, **hasheado por defecto** (`cot_uid_mode`) |
| Confianza | **`approximate`**, siempre — CoT base no está autenticado (ver más abajo) |
| Findings | cancelaciones drop-track, eventos de error sin límite y rechazos agregados del listener (rate-limit / oversize / malformed / conn-limit) |

## 1. Postura: leer el servidor, primero fuera de línea

La fuente de postura fundamentada es el propio fichero de configuración del servidor. En una
instalación de paquete está en `/opt/tak/CoreConfig.xml`. Apunta el connector a él y leerá las
entradas configuradas, los ajustes TLS/keystore y el backend de firma de certificados **sin tocar
la red**. El elemento `<federation>` deliberadamente no se modela, por lo que no se produce
postura de federación.

La **sonda de versión** en vivo es opcional y solo añade la versión en ejecución. Como TAK Server
autentica operadores con **mTLS**, la sonda es deny-closed: si estableces `server_url` con
`posture` activo pero **omites** el certificado de cliente, el connector **se niega a iniciar** en
lugar de sondear anónimamente e informar una postura que no autenticó. `server_url` debe ser
`https`.

```jsonc
// OLIVARES_SOURCES_CONFIG — posture only
{
  "sources": [{
    "name": "tak-server",
    "kind": "tak",
    "tenant": "<tenant-id>",
    "config": {
      "core_config_path": "/opt/tak/CoreConfig.xml",
      "server_url": "https://takserver.example.mil:8443",
      "client_cert": "${TAK_CLIENT_CERT_PEM}",
      "client_key":  "${TAK_CLIENT_KEY_PEM}"
    }
  }]
}
```

## 2. Ingesta: recibir CoT por UDP y TCP

Activa un listener y el connector recibe CoT — un mensaje por datagrama **UDP**, un mensaje por
conexión **TCP** («open-squirt-close»). Apuntas un feed TAK o clientes CoT a la dirección de
escucha del connector; el connector es el consumidor, no se conecta al servidor para extraer datos.

```jsonc
// OLIVARES_SOURCES_CONFIG — ingest
{
  "sources": [{
    "name": "tak-edge",
    "kind": "tak",
    "tenant": "<tenant-id>",
    "config": {
      "cot_udp_listen": "0.0.0.0:6969",
      "cot_multicast_group": "239.2.3.1",
      "cot_tcp_listen": "0.0.0.0:8087",
      "allow_public_bind": true,
      "feed_ref": "tak"
    }
  }]
}
```

### Claves de configuración (del descriptor distribuido con el connector)

| Clave | Tipo | Predeterminado | Secreto | Significado |
|---|---|---|:--:|---|
| `core_config_path` | string | — | no | Ruta a `CoreConfig.xml` (instalaciones de paquete: `/opt/tak/CoreConfig.xml`) — la fuente de postura fundamentada y fuera de línea |
| `server_url` | string | — | no | URL base de TAK Server (p. ej. `https://takserver.example.mil:8443`). Opcional: activa solo una sonda de versión en vivo |
| `version_path` | string | `/Marti/api/version` | no | Endpoint de versión Marti en `server_url`. Configurable porque la referencia API de tak.gov está restringida por cuenta |
| `client_cert` | string | — | **sí** | Certificado de cliente PEM para mTLS de TAK Server, por referencia |
| `client_key` | string | — | **sí** | Clave privada PEM para el certificado de cliente, por referencia |
| `ca_cert` | string | — | no | Bundle CA PEM para el certificado de TAK Server. Vacío usa el almacén de confianza del host |
| `posture` | bool | `true` | no | Emite findings de postura de TAK Server |
| `request_timeout` | duration | `15s` | no | Tiempo máximo por solicitud contra la API de TAK Server |
| `feed_ref` | string | `tak` | no | Referencia estable para este feed CoT — el `source_ref` que acota un binding sourcescope (`source_type=data`) |
| `cot_udp_listen` | string | — | no | Dirección de escucha UDP para CoT (p. ej. `127.0.0.1:6969`). Vacío desactiva la ingesta UDP |
| `cot_tcp_listen` | string | — | no | Dirección de escucha TCP para CoT open-squirt-close (p. ej. `127.0.0.1:8087`). Vacío desactiva la ingesta TCP |
| `cot_multicast_group` | string | — | no | Grupo multicast opcional al que unirse en el listener UDP (el predeterminado SA de TAK es `239.2.3.1`) |
| `cot_max_event_bytes` | int | `65536` | no | Bytes máximos para un evento CoT |
| `cot_max_detail_bytes` | int | `32768` | no | Bytes máximos para el tramo opaco `<detail>` de un evento CoT |
| `cot_rate_limit_eps` | int | `500` | no | Eventos CoT máximos aceptados por segundo entre todos los listeners; el exceso se descarta y cuenta |
| `cot_max_tcp_conns` | int | `128` | no | Conexiones CoT TCP concurrentes máximas |
| `cot_uid_mode` | string | `hash` | no | Cómo sale un `uid` del connector: `hash` (predeterminado, unidireccional) o `raw`. Un uid identifica un dispositivo, y un dispositivo identifica a quien lo porta |

## Puertos (TAK Server Configuration Guide v5.2)

Contexto sobre aquello con lo que te integras. Los propios listeners del connector enlazan el
`host:port` que configures — los ejemplos reutilizan estos números solo por familiaridad.

| Puerto / grupo | Convención |
|---|---|
| **8089** | Entrada de streaming CoT TLS — el canal autenticado cliente↔servidor |
| **6969** + multicast **239.2.3.1** | Grupo multicast de conciencia situacional (SA) |
| **8087** | Puerto de entrada convencional; el ejemplo canónico de la guía lo enlaza como **UDP**. Configurable por protocolo — 8087 **no** es intrínsecamente TCP |
| **8088** | `stcp` — entrada TCP sin cifrar, **solo pruebas** |
| **8443** | UI web administrativa |
| **8446** | Inscripción de certificados |

## Privacidad: las coordenadas y el detalle nunca salen del connector

CoT es un protocolo de informe de posición — la señal con mayor densidad de PII que ingiere este
producto — por lo que los datos mínimos se aplican estrictamente:

- Los `lat` / `lon` / `hae` de `<point>` **nunca salen del connector.** Una coordenada es la
  ubicación de una persona; el producto registra que se recibió un evento, de qué emisor y de qué
  tipo CoT — nunca dónde está alguien.
- El tramo opaco `<detail>` nunca sale del connector; solo se conservan su **tamaño** y un
  **digest SHA-256**, de modo que payloads idénticos se correlacionan sin almacenar el payload.
- El `uid` del emisor se **hashea por defecto** (`cot_uid_mode=hash`, separado por dominio y
  unidireccional). `raw` es un opt-in explícito del operador.

## Confianza: un uid CoT no es una identidad autenticada

El CoT base no lleva **ninguna autenticación** — cualquier host que pueda llegar a un listener
puede afirmar cualquier `uid`. El TLS de TAK Server protege el canal cliente↔**servidor** (puerto
8089); no dice nada sobre un evento que este connector recibe en su propio listener UDP/TCP en
claro. Por tanto, **cada** edge de un listener CoT base se califica como **`approximate`**, por
diseño — no hay ninguna ruta de código que devuelva `attributed`.

:::caution[Un `uid` es una afirmación, no una prueba]
Lee un `uid` CoT como *«un emisor que afirma este id publicó en el feed»*, no como una identidad
autenticada. Solo pasaría a estar autenticado si un listener terminara mTLS y vinculara el uid al
certificado del par.
:::

## Ámbito: gobernar el feed con un binding sourcescope

El feed es una fuente gobernada de primera clase. Un binding **sourcescope** delimita quién puede
usarlo con `source_type=data` y `source_ref=<feed_ref>`, en cualquier eje de sujeto — **session /
agent / user / user_group / role**. Los efectos son `allow` (predeterminado) o `forbid`, y
**`forbid` es absoluto** (forbid prevalece sobre allow).

```http
POST /v1/m/sourcescope/bindings
Content-Type: application/json

{
  "source_type": "data",
  "source_ref":  "tak",
  "scope_tree":  "agent",
  "scope_ref":   "agent:recon-planner",
  "effect":      "allow",
  "enabled":     true
}
```

Establece `"effect": "forbid"` (por ejemplo, con `"scope_tree": "user_group"`) para
sustraer el acceso de todo un grupo, incluso donde exista un allow.

## Licencia y procedencia clean-room

El formato de transmisión CoT es una implementación **clean-room** escrita solo a partir de la
**especificación MITRE de liberación pública** — no se leyó, copió ni derivó código fuente de TAK
ni ATAK:

- *The Developer's Guide to Cursor on Target*, Butler, MITRE, ago. 2005 — DTIC **ADA637348**,
  MITRE **Case #06-0249**.
- `Event-PUBLIC.xsd`, el esquema de eventos base CoT (versión 2.0) — MITRE **Case #11-3895**.
- *TAK Server Configuration Guide* **v5.2** — para las convenciones de puerto/protocolo.

ATAK-CIV y TAK Server son **GPLv3** y están fuera de los límites del connector (Apache-2.0), lo
que aplica la comprobación de frontera de licencia. Ambos llevan una marca federal estadounidense
**«Distribution A»**, que es una **declaración de liberación gubernamental, no una licencia de
software** — los árboles de código son GPLv3. El esquema y la guía de liberación pública de MITRE
son lo que hace legítima una implementación clean-room.

## Límites honestos

- **Sin portadores mesh/radio** — solo UDP y TCP; sin serial, malla TAK ni radio.
- **Sin plugins ATAK/WinTAK** — el connector no implementa ningún cliente TAK de usuario final.
- **Sin federación TAK** — solo *observa* que la federación está configurada; nunca federa.
- **Sin Link-16 / MIL-STD** ni protocolo táctico restringido por certificación, y **sin
  acreditación Iron Bank / DoD** — rutas opcionales independientes para clientes.
- El subesquema CoT `<detail>` **no se modela** — solo se analiza el evento base; detail son bytes
  opacos, con límite de tamaño y digest.
- La **pérdida UDP es incontable** — la contrapresión ralentiza los listeners; para UDP el
  **kernel** descarta datagramas antes de que los vea este proceso, y esos descartes no pueden
  contarse. Solo los eventos que el connector rechazó realmente se agregan en findings de rechazo.

## Relacionado

- [Conectar una fuente](/es/how-to/connect-a-source/) — el modelo de connector y la taxonomía de
  niveles honestos.
- [Gobernar y aprobar](/es/how-to/govern-and-approve/) — el modelo de autorización al que se
  conecta un binding sourcescope.
- [Connectors y niveles de cobertura](/es/reference/connectors/) — el catálogo completo.
