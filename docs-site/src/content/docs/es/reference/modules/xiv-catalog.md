---
title: "Módulo XIV — catálogo interno y marketplace"
description: >-
  El registro interno y curado de agentes, servidores MCP, skills, plantillas,
  modelos y conectores de terceros aprobados para la organización. Cómo se versiona,
  congela, hash-pinea y firma una entrada al aprobarse, cómo se gobierna la
  instanciación self-service, y los límites.
---

El módulo XIV es el **catálogo interno** de la organización — un registro curado y
gobernado de los agentes, servidores MCP, skills, plantillas, modelos y conectores de
terceros que han sido **aprobados para reutilización** en toda la empresa. Existe para que
un estate se estandarice sobre capacidades verificadas y versionadas en lugar de copias
ad-hoc, y para que "aprobado" signifique algo verificable en vez de una palabra en una wiki.
Está en la capa de Inteligencia y **no tiene superficie de actuación**: cura y registra,
mientras que el aprovisionamiento ocurre en otro lugar.

## Qué es

El catálogo es un **registro**, no un almacén de documentos. Una **entrada** es una
definición curada y versionada de una capacidad reutilizable, de tipo `agent`, `mcp`,
`skill`, `template`, `model` o `connector`. Cada `(kind, slug, version)` es su **propio
artefacto inmutable** — publicar una nueva versión crea una nueva entrada, y la aprobación y
la firma ocurren **por versión**. Una entrada recorre un ciclo de vida fijo:

`draft → pending → approved → deprecated`

Solo un **draft** es mutable; **la aprobación lo congela**. La spec de una entrada es una
*definición* escrita por el operador — referencias de transporte, modelo y prompt, alcance, y
**referencias** a secretos — nunca un valor de credencial. La ruta de crear/aprobar rechaza
una spec que lleve credenciales en línea, de modo que el módulo almacena definiciones,
referencias y metadatos de gobernanza y nunca secretos ni payloads.

## Versionado, congelación y firma

La aprobación es donde "aprobado" se vuelve verificable:

- **Hash de contenido.** Al aprobar, la entrada se clava con un **hash de contenido
  SHA-256** sobre su preimagen canónica y serializada de forma determinista. Se cubre cada
  campo escrito por el operador, de modo que cualquier mutación posterior de una entrada
  aprobada es **detectable** — con alteraciones detectables incluso sin firma.
- **Atestación en el ledger.** La aprobación se registra en el audit ledger append-only y
  hash-chained, atribuida al **principal real** que la aprobó.
- **Firma Ed25519.** Cuando hay una clave de firma del catálogo aprovisionada, la aprobación
  también produce una **firma Ed25519 desacoplada** sobre el hash de contenido, que lleva la
  clave pública y una huella corta — "aprobado = verificable". La clave de firma se carga o
  se acuña en el arranque bajo la costura fail-closed de claves del motor, **independiente
  de** la clave del audit ledger; el módulo posee su clave de catálogo y nunca alcanza el
  firmante de auditoría interno del motor, manteniendo limpia la frontera de confianza.

La verificación recalcula el hash y, cuando hay una clave configurada en el nodo, trata la
firma como el **ancla de confianza**: una firma despojada (downgrade) o una hecha por
cualquier otra clave (sustitución) se reporta **no verificada**. `GET …/pubkey` informa de si
la firma está habilitada; el estado `verified` / `signed` / `signed_by` por entrada lo
devuelven las rutas de entrada y de verificación.

## Conectores de terceros verificados

Una entrada `connector` cura un **plugin de conector de terceros publicado** — un binario
construido o un artefacto OCI. Su spec registra qué cura: el `sha256` del artefacto
(`artifact_digest`), la referencia release/OCI, el publicador y el nombre del descriptor del
conector. La entrada es el **registro de certificación** de cara al tenant del ecosistema de
conectores externos: "aprobado" puede hacerse significar "su atestación de cadena de
suministro fue verificada", no solo "alguien pulsó aprobar".

El flujo refleja el par de admisión de las entradas MCP, con sus propios registros de
política y veredicto (la evidencia se cuenta por tipo, de modo que los veredictos de
conector nunca comparten tablas con los veredictos MCP):

- `GET`/`PUT …/connector-admission/policy` — la raíz de confianza por tenant:
  `require_signed`, opcional `require_subject_digest`, pines de identidad/issuer de Sigstore,
  claves públicas desnudas, raíces CA, y la **allow-list de predicados** in-toto (por defecto
  SLSA provenance v1/v0.2 y SBOMs SPDX/CycloneDX — formas de provenance y SBOM, porque un
  conector es un artefacto construido, no pesos de modelo). Sin política significa **modo
  observe** — nada se bloquea hasta que el tenant opta por activarlo, y el endpoint de
  política lo dice honestamente.
- `POST …/entries/{id}/admit` — una única ruta compartida, despachada por tipo de entrada
  (`mcp` o `connector`): verifica un bundle de atestación Sigstore suministrado por el
  operador y registra un **veredicto claim-vs-verified** por entrada. Cuando la petición no
  clava un `expected_digest`, el binding **toma por defecto el `spec.artifact_digest` de la
  entrada** — la entrada nombra el artefacto que cura, así que la admisión se enlaza a ese
  artefacto salvo override explícito. Un bundle malformado es un `400`; un bundle bien
  formado que falla la verificación es un **veredicto negativo registrado**, no un error.
- `GET …/connector-admissions` — los veredictos registrados, filtrables por entrada
  (`entry_ref`) y restringibles a veredictos verificados (`verified=true`).
- **Puerta de aprobación deny-closed.** Con `require_signed` activo, una entrada de conector
  solo puede aprobarse (y por tanto listarse como conector *verificado*) con un veredicto de
  admisión de provenance/SBOM verificado **enlazado al digest que la entrada cura actualmente**
  (`spec.artifact_digest`); con `require_subject_digest` activo, ese binding del artefacto debe
  estar él mismo confirmado. Editar el digest curado tras una admisión invalida la puerta — se
  requiere una re-admisión frente al nuevo artefacto.

:::caution[Límites honestos]
El catálogo **certifica**, no ejecuta: la puerta del lado del host que decide si un plugin de
conector puede *ejecutarse* realmente vive en el control plane, no aquí. Los bundles de
atestación los suministra el operador (`cosign download attestation` /
`gh attestation download`) — obtenerlos de los referrers OCI es un paso externo, y la
**inclusión** en el transparency-log de Rekor no se verifica nativamente (el veredicto
registra la presencia del material y dice exactamente qué se comprobó).
:::

## Instanciación self-service gobernada

Una **instancia** es una petición self-service para instanciar una entrada **aprobada** —
solo una entrada aprobada puede instanciarse. El módulo registra la petición, su
**provenance** (de qué versión de entrada procede), su destino y su estado de gobernanza, y
hace cumplir una máquina de estados sensata (`requested → approved`/`rejected → active`).
**No** decide quién puede aprobar, ni aprovisiona: la **decisión** de aprobación pertenece a
la gobernanza y el aprovisionamiento real al despliegue. Aprobar, deprecar, firmar e
instanciar son acciones **privilegiadas, gated por RBAC y autoauditadas** al principal real.

:::caution[Límites honestos]
- **Sin actuación, sin aprovisionamiento.** El módulo XIV registra y gobierna la *petición*;
  nunca levanta una capacidad. La decisión de aprobación es de la gobernanza y el cableado del
  despliegue — y el `apply`/`retire` vivo allí es en sí una costura deny-closed (`503` hasta
  que se aprovisiona un executor). Consulta [Honestidad y límites](/es/start/honesty-and-limits/).
- **La firma es real pero depende de la clave.** La firma Ed25519 está implementada y la clave
  de firma se aprovisiona en el arranque activada por defecto. En un nodo **sin clave
  configurada** (o con una clave inválida), una entrada aprobada está **hash-pineada y atestada
  en el ledger pero sin firmar** — la API lo dice honestamente vía `signing_enabled`/`signed`
  en lugar de dar a entender que existe una firma.
- **Curado, no observado.** El catálogo **no** se suscribe ni emite en el bus de eventos; lo
  pueblan personas a través de su API, no se deriva de observaciones vivas. Asevera lo que la
  organización *aprobó para reutilización*, no lo que está corriendo actualmente.
- **El módulo no hace cumplir la política de aprobación.** Hace cumplir la máquina de estados
  y los tiers de verbo RBAC; *quién* puede aprobar y bajo qué condiciones lo decide la
  gobernanza.
:::

## Relacionado

- [Catálogo de módulos](/es/reference/modules/overview/) — dónde encaja el módulo XIV y la
  división gobernar/observar vs actuar.
- [Gobernar y aprobar](/es/how-to/govern-and-approve/) — el flujo de aprobación human-in-the-loop.
- [Visión general de la arquitectura](/es/explanation/architecture/overview/) — el motor, las
  capas y el modelo de datos compartido en el que declaran las entradas.
- [Referencia del bus de eventos](/es/reference/events/) — el bus que este módulo
  deliberadamente no consume.
- [Honestidad y límites](/es/start/honesty-and-limits/) — la postura de actuación a lo largo del producto.
