> Traducción automática. La versión en inglés es la fuente autoritativa.

# ADR-0011: Frontera de licencias — producto AGPL, SDK/conectores Apache, enterprise comercial

- **Status:** accepted
- **Date:** 2026-06
- **Deciders:** Fran Olivares
- **References:** diseño de licencias (decisión final); frontera de licencias del stack

## Contexto y planteamiento del problema

El producto necesitaba un modelo de licencias que lo mantenga genuinamente abierto, que
mantenga un ecosistema de conectores de terceros libre de la fricción del copyleft y que
deje una vía comercial limpia — sin limitación de funcionalidades (véase ADR-0010).

## Factores de decisión

- Un producto genuinamente abierto y copyleft (no source-available, no mutilado).
- Un ecosistema de conectores permisivo para que los terceros lo extiendan con libertad.
- Una excepción comercial limpia para quienes la necesiten.

## Opciones consideradas

- **Licencia dual pura:** producto AGPL + SDK/conectores Apache-2.0 + excepción comercial.
- **Open core con funcionalidades limitadas** (núcleo MIT/Apache + funcionalidades de pago).
- **Todo permisivo** (núcleo MIT/Apache).
- **Source-available** (BSL, SSPL, PolyForm).

## Resultado de la decisión

Opción elegida: una **licencia dual pura**. `core/`, `modules/`, `web/` son
**AGPL-3.0-only**; `sdk/` y `connectors/` son **Apache-2.0**; `enterprise/` es
**comercial** (`LicenseRef-Olivares-Commercial`). La frontera se aplica desde el primer
commit mediante cabeceras SPDX por fichero y una comprobación de CI: un conector
Apache-2.0 **nunca** importa el motor AGPL.

### Consecuencias

- **Bueno:** el producto es genuinamente abierto y copyleft; los conectores permanecen
  permisivos y sin fricción; la frontera se aplica de forma mecánica; existe una vía
  comercial sin capar nada.
- **Malo / compromisos:** los contribuyentes deben mantener correctas las cabeceras SPDX
  y respetar la frontera de importación (la CI detecta las violaciones).
- **Neutral:** la excepción comercial es autoservicio más un contacto enterprise.

## Por qué se rechazaron las alternativas

- **Open core con funcionalidades limitadas** — capa el producto (véase ADR-0010),
  rechazada.
- **Todo permisivo** — regala el núcleo sin base comercial.
- **Source-available (BSL/SSPL/PolyForm)** — no es OSS; mata la adopción de la que
  depende el ecosistema de conectores.

## Enmienda (2026-06-23) — el modelo es open core

La **frontera de licencias anterior no cambia y es correcta**: `core/`+`modules/`+`web/`
son AGPL-3.0-only, `sdk/`+`connectors/` son Apache-2.0, `enterprise/` es comercial y un
conector Apache nunca importa el motor AGPL. Lo que se corrige es el *enfoque*: el
producto distribuido es **open core** (el modelo `ee/` de GitLab), **no** una «licencia
dual pura» sin diferencias de funcionalidades. La compilación AGPL es la plataforma de
gobernanza completa y nunca se mutila internamente para incentivar una venta adicional,
pero **no es idéntica** a la edición comercial: la línea `enterprise/` (federación
multi-IdP, firewall de contenido/DLP, endurecimiento de hooks, feed de inteligencia de
amenazas, egress de herramientas de servidor, CyberArk Conjur, cierre de incidentes en
bucle) es **código nuevo aditivo que nunca estuvo en la compilación abierta** (sin
rug-pull). Por tanto, «considerada/elegida: licencia dual pura» debe leerse como la
decisión sobre la *frontera* AGPL/Apache; la decisión de *ediciones* abierta frente a
comercial es open core. Véase `LICENSING.md`

La **frontera** de licencias de este ADR no queda reemplazada. Lo que cambió por separado
fue la **distribución** de la línea comercial: el código fuente de `enterprise/` ya no se
distribuye en el repositorio público, sino que se trasladó a un repositorio privado para
que el gating por build tag sea real, no cosmético. Es una decisión de distribución,
registrada en **ADR-0020**; la frontera y la licencia solo de atestación (ADR-0010) no
cambian.

## Enmienda (2026-07-28) — dos afirmaciones obsoletas de la nota de 2026-06-23

La frontera y el enfoque open-core anteriores se mantienen. Dos elementos de la lista
enterprise de la enmienda de 2026-06-23 ya no describen el producto; la propia nota se
deja exactamente como fue escrita, porque es un registro fechado de lo que se creía
entonces.

1. **«el entitlement de plazas que levanta el límite de usuarios de community» ya no
   existe.** La decisión B10 (2026-07-27) eliminó por completo el límite de usuarios: las
   cuentas autoalojadas son ilimitadas en todas las ediciones,
   `core/auth.CommunitySeatLimit` es `0`, `enforceSeatCapTx` es un no-op incondicional y
   ninguna compilación — abierta o comercial — lee una licencia para limitar usuarios.
   Decisión actual: el canon comercial de precios (mantenido en privado) (`self_hosted.users: unlimited`) y
   `LICENSING.md`
2. **«feed de inteligencia de amenazas» no describe cómo puede venderse el add-on.**
   `enterprise/threatintel` distribuye un catálogo base compilado en la build, además de
   artefactos opcionales de feed firmados y versionados: el operador fija una clave de
   editor para ellos y los aplica. Olivares no opera ninguna distribución de feed curada
   ni publica una cadencia de lanzamientos. El canon comercial
   (el canon comercial de precios (mantenido en privado), `self_hosted.business.preset`) prohíbe comercializarlo como
   un «feed» salvo que se opere realmente un feed firmado. La CLI de operador conserva la
   palabra para el artefacto que verifica y aplica
   (`olivares threatintel verify|apply|pull`): es el nombre del artefacto, no una
   afirmación sobre quién lo publica.

Ninguno de los dos elementos afecta a la frontera de licencias que decide este ADR.
