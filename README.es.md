<div align="center">

<a href="https://olivares.ai"><img src=".github/assets/olivares-banner.png" alt="Olivares AI — Ground truth for enterprise AI" width="720"></a>

**Idiomas:** [English](./README.md) · **Español** · [简体中文](./README.zh.md) · [Русский](./README.ru.md) · [日本語](./README.ja.md) · [Deutsch](./README.de.md) · [Français](./README.fr.md)

**El plano de control para la IA que realmente ejecutas.** Intégralo, ponlo a trabajar, conéctalo a tus sistemas y gobierna cada parte de él: un binario autoalojado, desde un servidor doméstico hasta una empresa regulada.

[Instalación](#install) ·
[Inicio rápido](#quickstart) ·
[Ejemplos](examples/) ·
[Arquitectura](#architecture) ·
[Documentación](#documentation) ·
[Seguridad](SECURITY.md) ·
[Contribuir](CONTRIBUTING.md) ·
[olivares.ai](https://olivares.ai)

[![License: AGPL-3.0-only](https://img.shields.io/badge/license-AGPL--3.0--only-blue)](LICENSING.md)
[![SDK & connectors: Apache-2.0](https://img.shields.io/badge/SDK%20%26%20connectors-Apache--2.0-blue)](LICENSING.md)
[![Status: beta](https://img.shields.io/badge/status-beta-F08000)](CHANGELOG.md)
[![Contributor Covenant](https://img.shields.io/badge/Contributor%20Covenant-2.1-4baaaa)](CODE_OF_CONDUCT.md)

<!-- OpenSSF Best Practices Badge (self-certification).
     Registration at https://www.bestpractices.dev is pending (a maintainer action); the
     evidence map is in docs/openssf-badge.md. Once a project ID is assigned, uncomment:
[![OpenSSF Best Practices](https://www.bestpractices.dev/projects/PROJECT_ID/badge)](https://www.bestpractices.dev/projects/PROJECT_ID)
-->

</div>

> Estado: **beta**, en desarrollo activo. El motor funciona de extremo a extremo: un único binario estático con la consola embebida, que ingiere señales reales de los sistemas donde se ejecuta tu IA. Las API, los esquemas y la superficie de módulos aún pueden cambiar antes de la 1.0, y algunos seams de actuación (puntos de integración declarados, deny-closed) permanecen cerrados hasta que se aprovisionan (consulta [Honestidad y límites](docs-site/src/content/docs/start/honesty-and-limits.md)). Las versiones se publican desde este repositorio; las [rutas de instalación](#install) de más abajo se publicarán con la primera versión etiquetada.

> Cadena de suministro: las versiones se construyen en GitHub Actions con una cadena de confianza firmada por tipo de artefacto: los archivos comprimidos se entregan con SBOM SPDX y atestaciones in-toto, las imágenes de contenedor se firman con cosign con una atestación SBOM de imagen, y cada artefacto (incluidos paquetes y chart) está cubierto por el manifiesto de checksums firmado con cosign, además de un documento OpenVEX y procedencia de build SLSA para el conjunto. Verifica cualquier versión con [`scripts/verify-release.sh`](scripts/verify-release.sh); la cadena exacta por tipo de artefacto, la ruta air-gapped y el chart de Helm se documentan en [`docs/RELEASE-VERIFICATION.md`](docs/RELEASE-VERIFICATION.md) y [`deploy/`](deploy/).

## Qué es Olivares AI

Hace tiempo que la IA dejó de ser una sola ventana de chat. Lo que ejecutas ahora es un pequeño estate (parque): agentes de programación en terminales, servidores MCP, endpoints de modelos, cuentas de servicio y trabajos programados, repartidos por máquinas que nunca se diseñaron como un único sistema. Nada lo mantiene unido, por lo que responder a las preguntas corrientes resulta caro: qué se está ejecutando, quién lo puso en marcha, a qué llegó, cuánto costó y quién dio el visto bueno a alguna de esas cosas.

**Olivares AI es el plano que lo mantiene unido.** Tiene dos mitades y ambas se entregan en el mismo binario:

- **Ejecuta y conéctalo**: un plano duradero para el propio trabajo. Elementos de trabajo con propiedad, dependencias, criterios de aceptación y decisiones; leases que convierten la propiedad en una autoridad que un titular obsoleto no puede seguir usando; sesiones iniciadas, conectadas y detenidas desde la consola, con entrada para una ejecución en vivo; delegación a un par remoto mediante A2A; MCP como superficie de herramientas; y fuentes de contenido gobernadas que alimentan la recuperación. Esta es la mitad descrita en [The work plane](#the-work-plane) más abajo, con el estado de cada pieza expresado sin rodeos.
- **Obsérvalo y gobiérnalo**: inventario de todo lo descubierto, un mapa de acceso de lectura/escritura de lo que cada agente e identidad alcanza realmente, política Cedar, aplicación deny-closed, presupuestos que pueden rechazar gasto y un ledger firmado en cadena hash para demostrarlo todo después.

Ninguna mitad es decoración de la otra. La gobernanza sin un plano de trabajo es un panel sin nada sobre lo que actuar; un plano de trabajo sin gobernanza es trabajo del que nadie puede dar cuenta después.

**Multiproveedor por diseño.** Claude Code se integra al nivel más profundo —el hook `PreToolUse`/`PostToolUse`, los ajustes gestionados, el inicio y la detención desde consola, el acceso a modelos por sujeto—, con Codex y Grok Build a su lado como superficies de comandos de primera clase, y gemini-cli, Cursor, opencode, goose, cline, OpenHands, OpenClaw y Hermes como conectores propios. Cada uno indica lo que puede aplicar y lo que solo puede observar; ninguno es el centro de gravedad del producto. Ollama y otros endpoints autoalojados quedan inventariados y atribuidos mediante el conector local, que por diseño es de solo lectura; las reglas de política y presupuesto se aplican donde la inferencia cruza el proxy gobernado, que es el único lugar donde pueden aplicarse.

**Quién lo ejecuta.** La build abierta es toda la plataforma en cada una de estas escalas —los add-ons comerciales son código aditivo sobre ella, nunca un producto distinto:

| Eres | Lo que implica |
|---|---|
| **Un servidor doméstico o una red homelab** | un binario, SQLite, un volumen Docker, enlazado a loopback, sin servicio externo: la topología Compose suministrada se ejecuta sin root y en modo de solo lectura dentro de 1 CPU y 1 GiB ([`deploy/compose/docker-compose.yml`](deploy/compose/docker-compose.yml)) |
| **Un freelance, autónomo o consultor** | un tenant por cliente —cada operación de módulo queda fijada a uno—, presupuestos que pueden denegar o limitar el gasto antes de que llegue la factura y una exportación de postura que puedes entregar |
| **Un profesional o un usuario avanzado** | el mismo motor que ejecuta una empresa, sin que se oculte nada: la build abierta es toda la plataforma, así que lo que aprendes en tu propio equipo es lo que operas en el trabajo |
| **Un equipo de ingeniería o una pyme** | elementos de trabajo y leases compartidos para que dos agentes —o dos personas— no puedan tener el mismo elemento de trabajo a la vez, SSO, roles y un registro de auditoría que nadie debe montar a mano |
| **Una empresa regulada** | Postgres con seguridad a nivel de fila, HA con un único escritor y réplicas en espera, instalaciones air-gapped, evidencia mapeada a **26 catálogos de marcos** y archivado WORM sobre un sustrato inmutable |

Cada fila corresponde a la misma build. Varias de esas capacidades —SSO, HA, archivado WORM, presupuestos que realmente deniegan— son cosas que **aprovisionas**, no valores predeterminados que obtienes en el primer arranque; la matriz de abajo y [Honestidad y límites](docs-site/src/content/docs/start/honesty-and-limits.md) indican cuál es cuál, por capacidad.

Se ejecuta como un **único binario de Go autoalojado** con la consola embebida —en Linux, Docker, Kubernetes, on-premise o completamente air-gapped—. No hay telemetría obligatoria ni salida del plano de control por defecto: lo que cruza tu perímetro es lo que configuras para que lo cruce —llamadas a tus API de modelos, las salidas SIEM/webhook que conectes, un proveedor externo de embeddings si aprovisionas uno—. Los recopiladores leen de los sistemas que ya ejecutas (pgAudit, CloudTrail, eBPF, MCP, tu IdP), de modo que un recopilador que falla nunca se interpone en la ruta de datos de producción.

La cobertura y la atribución llevan niveles explícitos (`firm`/`approximate`/`unknown`, `clean`/`lossy`/`opaque`), la aplicación es deny-closed donde está cableada y un seam declarado donde no lo está, y la documentación dice sin ambages qué funciona hoy frente a lo que está en fase de diseño. El producto no fabricará una certeza que no pueda demostrar: consulta [Honestidad y límites](docs-site/src/content/docs/start/honesty-and-limits.md).

<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-dark.png">
  <img src="docs-site/public/console/access-map-light.png" width="840"
       alt="Mapa de acceso: Qué lee y escribe cada agente en tu infraestructura — orígenes a la izquierda, los recursos que tocan a la derecha, R/RW por color.">
</picture>

<sub><b>Mapa de acceso</b> — Qué lee y escribe cada agente en tu infraestructura — orígenes a la izquierda, los recursos que tocan a la derecha, R/RW por color.</sub>

</div>

**Compruébalo tú mismo con dos comandos** (Go 1.26+, [Task](https://taskfile.dev), pnpm —[requisitos previos](#quickstart-prerequisites)):

```sh
task build
./bin/olivares serve --seed-demo --insecure --listen 127.0.0.1:8901 \
  --grpc-listen 127.0.0.1:8902 --data-dir "$(mktemp -d)"
# open http://127.0.0.1:8901 — inventory, work, orchestration, access map + drift, policies, FinOps
```

La CI recorre la misma ruta: `task smoke:quickstart` arranca este estate de demostración contra el binario real y comprueba sus recuentos de access map y drift. Para las rutas de instalación y sus valores operativos predeterminados, consulta [Instalación](#install) e [Inicio rápido](#quickstart).

<a name="the-work-plane"></a>
<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/04-environments-dark.svg">
  <img src=".github/assets/04-environments-light.svg" width="840"
       alt="Un binario a cualquier tamaño: un servidor doméstico o homelab, un autónomo con un tenant por cliente, un equipo de ingeniería o una pyme, y una empresa regulada. Corre en Linux, Docker, Kubernetes, Helm y aislado de red, con cloud gestionado en el lanzamiento, y alcanza proveedores de modelos, nubes y directorios, fuentes de contenido gobernadas y conectores de salida — con el mapa de accesos como una capacidad más y no como el centro.">
</picture>

<sub>El mismo build de un homelab a una empresa regulada.</sub>
</div>

## El plano de trabajo

El plano que transporta el trabajo es la parte de Olivares AI que comparten agentes y personas, y la que con más frecuencia se describe como si estuviera terminada en todas partes. No es así, por lo que aquí está cada pieza con lo que realmente la sustenta y hasta dónde llega hoy.

| Pieza | Estado | Dónde reside |
|---|---|---|
| **Elementos de trabajo**: resumen, procedencia, dependencias, criterios de aceptación, decisiones, propietario e historial de eventos; duraderos, con un documento de comandos compartido por REST, CLI y los llamadores en proceso | **en vivo, API pública** | [`modules/sessions/work_model.go`](modules/sessions/work_model.go), rutas en [`modules/sessions/work_api.go`](modules/sessions/work_api.go) |
| **Leases**: propiedad como autoridad vallada y con vencimiento: adquirir, renovar, liberar, asumir, revocar; un titular obsoleto no puede seguir actuando, y la adquisición simultánea produce exactamente un ganador | **en vivo, API pública** | [`modules/sessions/work_lease.go`](modules/sessions/work_lease.go) |
| **Mensajes, acuses y traspasos**: conversación duradera vinculada a un elemento de trabajo, con repetición y rechazo de épocas obsoletas | **en vivo tras un flujo de orquestación; la bandeja de entrada pública general no se cablea deliberadamente** | [`modules/sessions/communication_model.go`](modules/sessions/communication_model.go); el test de arranque que prohíbe cablear el plano público es [`cmd/olivares/communicationauthorityboot_test.go`](cmd/olivares/communicationauthorityboot_test.go) |
| **Inicio para trabajar**: reservar, tomar el lease y *después* iniciar la sesión, persistiendo trabajo/época/valla/ejecución para que un reintento sea seguro | **en vivo mediante orquestación** | [`modules/sessions/runtime_work_launch.go`](modules/sessions/runtime_work_launch.go) |
| **Ejecución remota mediante A2A**: planificar, probar, iniciar, observar y cancelar trabajo en un par autorizado, con recibos duraderos | **en vivo, y solo cuando se configura un destino**; sin destino autorizado, el seam no se monta en absoluto | [`cmd/olivares/wire.go`](cmd/olivares/wire.go), [`cmd/olivares/orchremote.go`](cmd/olivares/orchremote.go) |
| **Modo sombra y autoridad final**: doble informe frente al sistema existente y un comparador antes de que el plano se convierta en autoritativo | **no construido** | solo diseño |

Lee esa tabla como la versión honesta de «agentes que se comunican entre sí»: los elementos de trabajo y los leases son una superficie API normal que puedes usar hoy; la conversación entre agentes es real y duradera, pero está acotada a un flujo de orquestación, y no hay un bus de mensajes general para agentes arbitrarios; la delegación remota funciona y rechaza pares desconocidos. Lo que no existe no aparece en la interfaz como algo próximo: aparece aquí, como ausente.

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/03-agent-communication-dark.svg">
  <img src=".github/assets/03-agent-communication-light.svg" width="840"
       alt="Cómo trabajan juntos los agentes: las superficies de agente alimentan un plano de trabajo durable de work items, leases vallados donde actúa un titular cada vez, lanzamiento para trabajo, y mensajes y acuses con alcance de workspace. La delegación llega a un par autorizado a través de su puerta de aplicación. El plano emite un grafo de orquestación, un bus de eventos, un mapa de accesos con deriva y un libro firmado que llega a tu SIEM. El modo sombra y la autoridad final se dibujan como una caja discontinua porque no están construidos.">
</picture>

<sub>Los agentes comparten un plano de trabajo durable. Lo que no está construido se dibuja ausente.</sub>
</div>

## Qué abarca

Un binario, **30 módulos**, una consola: a lo largo de toda la huella de tu IA, no de una sola funcionalidad. Cada capacidad lleva un estado de madurez explícito —en vivo, bajo demanda, observada o un seam deny-closed declarado—, expresado por elemento en [Honestidad y límites](docs-site/src/content/docs/start/honesty-and-limits.md).

- **Ejecuta el trabajo.** Elementos de trabajo duraderos, leases, inicio orquestado y delegación A2A como se describe en [The work plane](#the-work-plane); la vista Work de la consola es la superficie del operador para el mismo almacén, y la vista Orchestration dibuja la topología de delegación a partir de señales observadas.
- **Obsérvalo.** Inventario de cada agente, sesión, modelo, servidor MCP, herramienta e identidad **descubiertos**: la cobertura sigue lo que conectas, lleva indicadores explícitos y marca lo que no puede ver como `unknown` en lugar de adivinar; un **access map** de lectura/escritura de lo que cada uno alcanza realmente, con una vista de **drift** Permitido-vs-Observado; sesiones en vivo, el grafo de orquestación, salud y SLA.
- **Gobiérnalo y aplícalo.** Un motor de autorización Cedar (RBAC + deny-overlay + concesiones positivas con alcance) y **cuatro puntos de aplicación deny-closed**: el hook `PreToolUse`/`PostToolUse` de Claude Code, un proxy de inferencia `/v1/messages` en línea, una puerta MCP `tools/call` y una puerta de delegación A2A, de modo que las acciones no autorizadas no se ejecutan: se bloquean, se envían a aprobación de dos personas o se reescriben antes de ejecutarse. Ese adjetivo está medido, no afirmado: un punto solo cuenta mientras un test recorre su ruta *sin configurar* —ninguna puerta cableada, un documento de política vacío, un almacén de políticas que no responde— y comprueba la denegación. El censo de pares seam-prueba es [`scripts/enforcement-seams.tsv`](scripts/enforcement-seams.tsv); elimina una prueba y el recuento baja y la build falla. La política llega al interior de la propia sesión: reglas allow/ask/deny por ruta y por subárbol en el hook, presupuestos de ventana de contexto por superficie y por grupo, y alcance de fuentes hasta sesión, agente, usuario, grupo o rol. Además, administración con alcance y roles personalizados, break-glass con control dual y un **kill-switch** del estate que falla cerrado.
- **Claude y el ecosistema de agentes.** Gobierna Claude Code en el hook; inicia, conéctate, gobierna y detén sesiones de Claude Code y su workspace desde la consola; entrega managed-settings empresariales; gobierna qué modelo puede usar cada sujeto y en qué superficie; MCP (servidor de recursos protegido por OAuth, postura, registro, `.mcpb`); A2A v1 entre pares autorizados; y superficies para los agentes que tus equipos ejecutan realmente —gemini-cli, Cursor, Codex CLI, opencode, goose, cline, OpenHands, OpenClaw y Hermes (aplicación donde cada superficie la expone, observación de postura de solo lectura donde no; cada conector indica cuál)—, además de notificaciones de Teams con deep-links de aprobación.
- **Aliméntalo, con gobierno.** El lado del contexto de la misma moneda: las fuentes de contenido (SharePoint, Confluence, Google Drive, Notion, Salesforce, Snowflake, S3, Azure AI Search, SAP OData, PostgreSQL, además de una fuente de sistema de ficheros confinada a su raíz para montajes locales/NFS/SMB) alimentan un pipeline RAG gobernado con valores predeterminados funcionales: recuperación léxica con cero salida lista para usar, recuperación semántica respaldada por modelo cuando aprovisionas un proveedor de embeddings (Voyage, compatible con OpenAI o autoalojado; `embed_policy=model_backed` falla cerrado en vez de degradarse silenciosamente), procedencia por fuente, y habilitación y alcance aplicados deny-closed en el momento de la recuperación; además de un catálogo de data products con contratos versionados y puertas de calidad. Consulta [Governed data for Claude](docs-site/src/content/docs/how-to/governed-data-for-claude.md).
- **Identidad y acceso.** Identidad humana (WebAuthn/FIDO2, PIV/CAC, step-up de AAL) y ciclo de vida de **identidad no humana**; federación de identidad de agentes (Entra Agent ID, AWS AgentCore, Google, SPIFFE/SPIRE); reconciliación de plantilla desde AD/LDAP/Okta/Entra/Vault/Infisical con SCIM.
- **Protege los datos.** Guardrails en línea (PII, prompt-injection, jailbreak), DLP de salida, cifrado de sobre BYOK/CMEK en tres backends KMS (AWS KMS, Google Cloud KMS, Azure Key Vault), grabación de sesiones privilegiadas, derecho al olvido con destrucción de claves verificada, retención y legal-hold, atestación de residencia y establecimiento de clave postcuántica híbrida TLS 1.3 (X25519MLKEM768 cuando el par lo admite; las firmas siguen siendo clásicas hoy).
- **Demuéstralo.** Un audit ledger en cadena hash firmado con Ed25519; evidencia de cumplimiento sellada y de solo anexado mapeada a **26 catálogos de marcos** (EU AI Act, NIST AI RMF, ISO 42001, SOC 2, ISO 27001, GDPR…); envío a SIEM/ITSM (CEF/LEEF/syslog/OTLP/OCSF).
- **Opéralo bien.** Presupuestos FinOps que pueden denegar o limitar el gasto; evaluaciones calibradas de LLM como juez con una puerta de CI bloqueante (bajo demanda: sin una credencial de juez, las ejecuciones informan `SKIPPED`, nunca un aprobado silencioso); sandboxes de red-team aislados a nivel de SO (gVisor/Firecracker; sin un sandbox aprovisionado, las ejecuciones informan `DEGRADED`, nunca un aprobado fabricado); un panel de salud de conectores con una página de estado pública; copias de seguridad y restauración gestionadas desde la consola.

En **158 integraciones** con las nubes, directorios, almacenes de secretos, proveedores de modelos, superficies de agentes, SIEM y pipelines que ya ejecutas: un recuento derivado del código y aplicado en cada push por [`scripts/check-public-counts.sh`](scripts/check-public-counts.sh). La unidad es el directorio de conectores que contiene código Go: de los 159 directorios del árbol, 158 cumplen, y el gate deriva la cifra de ese modo en cada push. 12 de ellos son paquetes compartidos de contrato/biblioteca, no capacidades: se cuentan, y [`connectors/README.md`](connectors/README.md) contiene el desglose completo de qué es cada directorio. El mapa completo de cada capacidad y su madurez está en [`docs-site/`](docs-site/), y su propia suite de tests lo vigila.

<a name="whats-open-whats-enterprise-whats-planned"></a>
## Qué está abierto, qué es enterprise y qué está previsto

Esta tabla mapea cada área de capacidad al lugar donde se entrega —la build abierta (AGPL) o uno de los add-ons comerciales separados y opcionales—; la madurez de cada capacidad se expresa con honestidad en [Honestidad y límites](docs-site/src/content/docs/start/honesty-and-limits.md). La lista completa de seams reservados se declara en el propio árbol público ([`cmd/olivares/wire_noenterprise.go`](cmd/olivares/wire_noenterprise.go)): una capacidad que reserva el binario abierto responde `501` o no hace nada, y su comentario lo indica; nada está oculto y nada abierto se elimina.

| Área | Abierto (AGPL) | Add-ons comerciales | Previsto |
|---|---|---|---|
| Trabajo y orquestación | elementos de trabajo duraderos (resumen, dependencias, aceptación, decisiones, eventos), leases vallados con asunción y revocación, inicio orquestado de sesiones para un elemento de trabajo, con entrada y detención restringidas al trabajo en la API de sesiones, delegación A2A a pares autorizados con recibos duraderos, mensajes/acuses/traspasos acotados a flujo, vistas Work y Orchestration de la consola | — | doble informe en sombra y el cambio de autoridad que convierte este plano en el sistema de registro |
| Visibilidad | inventario de agentes/sesiones/modelos/servidores MCP/herramientas/identidades, access map de lectura/escritura con drift Permitido-vs-Observado, sesiones en vivo, grafo de orquestación, salud/SLA | — | — |
| Política y aplicación | motor de autorización Cedar (RBAC + deny-overlay + concesiones con alcance), cuatro puntos de aplicación deny-closed (hook de Claude Code, proxy en línea `/v1/messages`, puerta MCP `tools/call`, puerta de delegación A2A), aprobaciones de dos personas, break-glass con control dual, kill-switch del estate | endurecimiento de hooks, control de salida de herramientas de servidor, puerta de gobernanza de computer-use, pins de definición de herramientas MCP (deny-closed ante una definición cambiada), circuit breaker automático con escalado a kill-switch | — |
| Claude y el ecosistema de agentes | Claude Code gobernado en el hook, inicio/conexión/gobierno/detención de sesiones de Claude Code desde la consola, entrega de managed-settings empresariales, acceso a modelos por sujeto/por superficie, MCP (servidor de recursos protegido por OAuth, postura, registro, `.mcpb`), A2A v1, superficies para gemini-cli/Cursor/Codex CLI/opencode/goose/cline/OpenHands/OpenClaw/Hermes (aplicación donde la superficie la expone, observación de postura donde no), notificaciones de Teams con deep-links de aprobación | inspección de contenido de renderizado de MCP App, mediación de elicitation/sampling | — |
| Contexto y conocimiento | diez fuentes de contenido en vivo (SharePoint, Confluence, Google Drive, Notion, Salesforce, Snowflake, S3, Azure AI Search, SAP OData, PostgreSQL) más una fuente de sistema de ficheros confinada a su raíz (montajes locales/NFS/SMB), RAG gobernado (recuperación léxica por defecto, semántica respaldada por modelo con un proveedor de embeddings aprovisionado: falla cerrado bajo `embed_policy=model_backed`) con habilitación deny-closed en el momento de la recuperación, procedencia por fuente, catálogo de data products con contratos versionados y puertas de calidad | — | — |
| Identidad y acceso | SSO de IdP único (OIDC + SAML 2.0), WebAuthn/FIDO2, PIV/CAC, step-up de AAL, ciclo de vida de identidad no humana, federación de identidad de agentes (Entra Agent ID, AWS AgentCore, Google, SPIFFE/SPIRE), reconciliación de plantilla (AD/LDAP/Okta/Entra/Vault/Infisical) con SCIM, receptor de eventos CAEP | federación multi-IdP, aplicación de SSO, SCIM gestionado, rotación de NHI con CyberArk Conjur, transmisor CAEP (SET firmados a receptores SSF) | — |
| Seguridad de datos | guardrails en línea (PII, prompt-injection, jailbreak), DLP de salida, BYOK/CMEK en tres backends KMS (AWS KMS, Google Cloud KMS, Azure Key Vault), grabación de sesiones privilegiadas, derecho al olvido con destrucción de claves verificada, retención y legal-hold, atestación de residencia, establecimiento de clave PQC híbrida TLS 1.3 (X25519MLKEM768) | firewall de contenido/DLP | — |
| Evidencia y cumplimiento | audit ledger en cadena hash firmado con Ed25519, evidencia sellada de solo anexado, 26 catálogos de marcos, archivo en directorio/S3 con exportación/verificación (el directorio es WORM solo sobre un sustrato inmutable; S3 usa Object Lock), exportación OSCAL (tres modelos abiertos), vista abierta de riesgo TIC de DORA, envío a SIEM/ITSM (CEF/LEEF/syslog/OTLP/OCSF) | ingesta de perfiles/SSP OSCAL + constructor de POA&M, mínimos de retención regulatorios + bloqueo de modo de cumplimiento (SEC 17a-4/FINRA 4511/CFTC 1.31), Registro de Información de DORA + informes de incidentes graves, legal-holds WORM de largo horizonte + paquetes de evidencia de nivel de examinador, destinos WORM en Azure/GCS, pack AIMS de ISO 42001, packs de profundidad de cumplimiento + de clasificación NIS2, informes empresariales | — |
| Operaciones | presupuestos FinOps que deniegan o limitan el gasto, evaluaciones calibradas de LLM como juez con puerta de CI bloqueante (bajo demanda: se requiere credencial de juez; si no, `SKIPPED`), sandboxes de red-team aislados a nivel de SO (gVisor/Firecracker; las ejecuciones sin aprovisionar informan `DEGRADED`), panel de salud de conectores con página de estado pública, copias de seguridad y restauración gestionadas desde consola, consultas abiertas de rutas de ataque | catálogo de inteligencia de amenazas compilado, cierre de incidentes en bucle | — |
| Plataforma y despliegue | binario estático único con consola embebida, SQLite o Postgres con seguridad a nivel de fila, Docker/Kubernetes/Helm/air-gapped, proveedor de Terraform, SDK de cliente generados (Go, Java, Python, TypeScript), bus in-proc abierto + puente Core-NATS | bus JetStream duradero (at-least-once + deduplicación) | paquetes Windows (hoy: contenedor Linux o compilación desde código fuente), fine-tuning de modelos post-v1, sonda de telemetría de voz (hoy seam deny-closed declarado) |

La build AGPL es toda la plataforma y nunca se limita desde dentro por funcionalidades. Los add-ons comerciales son código nuevo aditivo, nunca funcionalidades retiradas del producto abierto. Una suscripción es la credencial con la que descargas artefactos firmados —el modelo SUSE—, no una clave que desbloquea código que ya está en tu disco. Las cuentas de usuario son ilimitadas en el motor autohospedado: ninguna de sus ediciones impone un tope de asientos y el mecanismo del binario para los asientos es una operación nula incondicional. El nivel Cloud alojado es la única excepción: su plano de control admite asientos por tenant, una propiedad de ese servicio y no de este binario. Consulta [`LICENSING.md`](LICENSING.md) y [Honestidad y límites](docs-site/src/content/docs/start/honesty-and-limits.md).

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/05-editions-dark.svg">
  <img src=".github/assets/05-editions-light.svg" width="840"
       alt="Qué contiene cada edición: el núcleo AGPL es la plataforma completa y los add-ons son código aditivo encima. Community es el producto AGPL completo con usuarios ilimitados. Business añade profundidad comercial en informes, onboarding, inteligencia de amenazas, postura PQC y NIS2. Regulated Operations añade gobernador de retención, archivo de auditoría WORM, retención legal y profundidad de borrado. Business Max es Business con los cuatro add-ons. Cloud Standard es el servicio gestionado, con cuotas de plan que incluyen asientos de servicio. Una suscripción es la credencial con la que descargas artefactos firmados.">
</picture>

<sub>Ediciones por composición. Empaquetado y precios a petición.</sub>
</div>

## Un vistazo a la consola

<div align="center">

<img src=".github/assets/olivares-reel.gif" width="720" alt="Un breve reel que alterna vistas reales de la consola de Olivares AI: access map, sesiones, políticas, FinOps y cumplimiento.">

<sub>Unos segundos de la consola real. Cada imagen fija de abajo es una captura del estate de demostración sembrado servida por el binario en ejecución: regenera tú mismo las capturas en bruto con <code>bash scripts/docs-captures.sh</code> (el conjunto seleccionado aquí procede de su salida).</sub>

</div>

| | |
|---|---|
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-drift-dark.png"><img src="docs-site/public/console/access-map-drift-light.png" alt="Drift de mínimo privilegio: Superpone el diff de mínimo privilegio: resalta accesos inesperados (observados sin permitir) y permisos sin usar."></picture><br><sub><b>Drift de mínimo privilegio</b> — Superpone el diff de mínimo privilegio: resalta accesos inesperados (observados sin permitir) y permisos sin usar.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/orchestration-dark.png"><img src="docs-site/public/console/orchestration-light.png" alt="Orquestación y A2A: Topología agente-a-agente: quién delega en quién, los flujos de delegación en vivo y las cadencias declaradas. Las lecturas del grafo de comunicación son privilegiadas y autoauditadas."></picture><br><sub><b>Orquestación y A2A</b> — Topología agente-a-agente: quién delega en quién, los flujos de delegación en vivo y las cadencias declaradas. Las lecturas del grafo de comunicación son privilegiadas y autoauditadas.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/inventory-dark.png"><img src="docs-site/public/console/inventory-light.png" alt="Inventario: Todos los agentes, sesiones, MCP, modelos e identidades descubiertos en tu infraestructura."></picture><br><sub><b>Inventario</b> — Todos los agentes, sesiones, MCP, modelos e identidades descubiertos en tu infraestructura.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/observability-dark.png"><img src="docs-site/public/console/observability-light.png" alt="Observabilidad e interoperabilidad: Salud de ingesta basada en estándares y desglose de trazas correlacionadas con el libro mayor. Las cifras son de todo el motor (globales del proceso), no por inquilino; los estándares se fijan a las versiones y madureces que declaran los organismos correspondientes."></picture><br><sub><b>Observabilidad e interoperabilidad</b> — Salud de ingesta basada en estándares y desglose de trazas correlacionadas con el libro mayor. Las cifras son de todo el motor (globales del proceso), no por inquilino; los estándares se fijan a las versiones y madureces que declaran los organismos correspondientes.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/dashboards-dark.png"><img src="docs-site/public/console/dashboards-light.png" alt="Visión ejecutiva: Coste, uso, riesgo y cumplimiento de un vistazo; baja a la vista operativa para el detalle."></picture><br><sub><b>Visión ejecutiva</b> — Coste, uso, riesgo y cumplimiento de un vistazo; baja a la vista operativa para el detalle.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/home-dark.png"><img src="docs-site/public/console/home-light.png" alt="Resumen: Tu patrimonio de IA de un vistazo: inventario, actividad, riesgo, cumplimiento, gasto y salud."></picture><br><sub><b>Resumen</b> — Tu patrimonio de IA de un vistazo: inventario, actividad, riesgo, cumplimiento, gasto y salud.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/security-dark.png"><img src="docs-site/public/console/security-light.png" alt="Seguridad y forense: Hallazgos de guardrail, la postura de enforcement, la cola de anomalías y el forense de incidentes con evidencia a prueba de manipulación. El plano es detective por defecto: registra, no bloquea por su cuenta salvo que el enforcement esté habilitado y gobernado."></picture><br><sub><b>Seguridad y forense</b> — Hallazgos de guardrail, la postura de enforcement, la cola de anomalías y el forense de incidentes con evidencia a prueba de manipulación. El plano es detective por defecto: registra, no bloquea por su cuenta salvo que el enforcement esté habilitado y gobernado.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/session-viewer-dark.png"><img src="docs-site/public/console/session-viewer-light.png" alt="Visor de grabación de sesión: Línea de tiempo unificada de la actividad del agente y evidencia de gobernanza para una única sesión."></picture><br><sub><b>Visor de grabación de sesión</b> — Línea de tiempo unificada de la actividad del agente y evidencia de gobernanza para una única sesión.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/identity-dark.png"><img src="docs-site/public/console/identity-light.png" alt="Identidad y NHI: SSO, SCIM, inventario de identidades, ciclo de vida de NHI, grafo WIF y acceso privilegiado: observados, gobernados y auditados."></picture><br><sub><b>Identidad y NHI</b> — SSO, SCIM, inventario de identidades, ciclo de vida de NHI, grafo WIF y acceso privilegiado: observados, gobernados y auditados.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/knowledge-dark.png"><img src="docs-site/public/console/knowledge-light.png" alt="Datos, conocimiento y contexto: Bases de conocimiento gobernadas, trazabilidad de recuperación, registro de prompts, memoria de agentes y políticas de contexto."></picture><br><sub><b>Datos, conocimiento y contexto</b> — Bases de conocimiento gobernadas, trazabilidad de recuperación, registro de prompts, memoria de agentes y políticas de contexto.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/work-apply-refused-dark.png"><img src="docs-site/public/console/work-apply-refused-light.png" alt="Plan: Planificando el cambio. En este paso no se escribe nada."></picture><br><sub><b>Plan</b> — Planificando el cambio. En este paso no se escribe nada.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/killswitch-dark.png"><img src="docs-site/public/console/killswitch-light.png" alt="Kill switch: La parada de emergencia del estate: un clic detiene todas las superficies de actuación gobernadas. Activarla es deliberadamente barato; la recuperación exige dos cuentas de usuario distintas y una revisión posterior forzosa."></picture><br><sub><b>Kill switch</b> — La parada de emergencia del estate: un clic detiene todas las superficies de actuación gobernadas. Activarla es deliberadamente barato; la recuperación exige dos cuentas de usuario distintas y una revisión posterior forzosa.</sub> |

<a name="install"></a>
## Instalación

Cada versión se publica bajo una **cadena de confianza firmada con cosign**: un manifiesto de checksums firmado con cosign que cubre cada artefacto, y cubre transitivamente los archivos comprimidos y binarios estáticos; una atestación SBOM in-toto por archivo comprimido; firmas cosign directamente sobre la imagen de contenedor —con una atestación SBOM para la imagen de contenedor— y sobre el chart de Helm; y procedencia de build SLSA para el conjunto. Para un producto de seguridad, la cadena de suministro forma parte del modelo de confianza, así que [verifícalo](docs/RELEASE-VERIFICATION.md) antes de ejecutarlo. La matriz completa por SO y la configuración de producción están en [`INSTALL.md`](INSTALL.md); los tutoriales de despliegue (Compose, Kubernetes/Helm, air-gapped) están en [`docs-site/`](docs-site/).

El motor es **seguro por defecto**: se enlaza a loopback, sirve HTTPS con un certificado autofirmado en el primer arranque, se entrega sin credenciales predeterminadas e imprime un token de configuración de un solo uso en la consola. El primer comando que ejecutas es el seguro.

**Desde el código fuente** (la ruta admitida hasta la primera versión etiquetada):

```sh
# Build the single binary (Go 1.26+, Task, pnpm — the web console is embedded).
task build

# Start it — one guided, secure-by-default command (TLS on, loopback-only, no
# default credentials). It prints your console URL and a one-time setup token.
./bin/olivares quickstart
```

**Con la primera versión**, la ruta recomendada pasa a ser una única instalación verificada: paquetes `.deb`/`.rpm`/`.apk` con una unidad systemd endurecida, una imagen Docker multi-arquitectura, un cask de Homebrew y un chart de Helm, cada uno cubierto por el manifiesto de checksums firmado con cosign de la versión (las imágenes se firman directamente), cada uno instalable en un paso y aún seguro por defecto. Aún no se han publicado; hasta que llegue la etiqueta, compila desde el código fuente como arriba. **Windows** aún no se ha construido: ejecuta el contenedor Linux o compila desde el código fuente ([plan en `INSTALL.md`](INSTALL.md#windows)).

> ¿Quieres echar un vistazo primero, sin conectar fuentes reales? Un estate sintético se ejecuta en loopback con un solo comando: consulta [Inicio rápido](#quickstart) más abajo.

<a name="quickstart"></a>
## Inicio rápido

Dos formas de empezar: explorar un estate sintético de inmediato o apuntar el motor a una fuente real. Ambas ejecutan el mismo binario real.

### Evalúalo en cinco minutos

1. Compila con `task build` (Go 1.26+, Task, pnpm; consulta los [requisitos previos](#quickstart-prerequisites)).
2. Arranca el estate de demostración con el comando exacto del paso 2a siguiente.
3. En la consola, inspecciona el access map y su drift Permitido-vs-Observado (20 nodos / 13 aristas, con 8 accesos inesperados y 2 concesiones sin uso), una política Cedar y un flujo de aprobación, la vista de evidencia de cumplimiento (26 catálogos de marcos) y un presupuesto FinOps.
4. Después lee qué es real y qué está previsto: la matriz de funcionalidades anterior, [The work plane](#the-work-plane) y [Honestidad y límites](docs-site/src/content/docs/start/honesty-and-limits.md).

<a name="quickstart-prerequisites"></a>
Requisitos previos para compilar desde el código fuente: Go 1.26+, [Task](https://taskfile.dev) (go-task) y pnpm (la UI web está embebida). Consulta [`CONTRIBUTING.md`](CONTRIBUTING.md) para la configuración de desarrollo completa.

**1. Compilar:**

```sh
task build && ./bin/olivares version
```

**2a. Explora el estate de demostración**: observaciones sintéticas mediante el motor real, solo loopback (rechaza direcciones que no sean loopback), sin datos reales:

```sh
./bin/olivares serve --seed-demo --insecure \
  --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 \
  --data-dir "$(mktemp -d)"
```

Abre `http://127.0.0.1:8901`, inicia sesión con las credenciales de demostración del banner de arranque y recorre la consola: inventario, access map y drift, sesiones, orquestación, políticas, FinOps y cumplimiento. La semilla de demostración es solo para aprender (contraseña pública en el árbol de fuentes); nunca la apuntes a datos reales.

**2b. O inícialo de verdad**: un único comando guiado y seguro por defecto:

```sh
./bin/olivares quickstart        # TLS on, loopback; prints the console URL + a one-time setup token
```

Abre la consola en la URL impresa y crea tu primer administrador con el token: sin curl ni pasos adicionales. (`olivares serve` es el mismo motor con banderas explícitas, para producción y contenedores.) Después conecta una fuente. El [inicio rápido completo](docs-site/src/content/docs/start/quickstart.md) conecta un **conector pgAudit real** a un registro de auditoría de PostgreSQL —sin semilla de demostración— y enlaza las rutas de instalación de producción (systemd, Docker Compose, Kubernetes mediante [`deploy/manifests/install.yaml`](deploy/manifests/install.yaml), air-gapped).

El estate de demostración es determinista. Los números no son aspiracionales: `task smoke:quickstart` recorre esta misma ruta contra el binario real (sus propios puertos y directorio de datos) y comprueba los recuentos de access map y drift indicados arriba, por lo que esta sección no puede alejarse del código sin hacer ruido.

<a name="architecture"></a>
## Arquitectura

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/02-architecture-dark.svg">
  <img src=".github/assets/02-architecture-light.svg" width="840"
       alt="Arquitectura: las superficies de agente, las fuentes de auditoría, los pares MCP y A2A y las fuentes de contenido se recogen de tres formas hacia un único binario Go autoalojado con la consola empotrada, que lleva los módulos del producto, la capa de política y aplicación y el libro de evidencia firmado sobre un almacén con alcance por tenant; sirve la consola, la API REST, un subconjunto gRPC acotado, la CLI y el proveedor de Terraform, con el plano de control cloud (construido, sin desplegar) y el portal de licencias (desplegado, con la entrega apagada) como planos aparte.">
</picture>
</div>

El motor es un único binario estático de Go (`olivares`) que embebe la UI web y expone sus capacidades mediante cuatro superficies, cada una con cobertura documentada: una API REST (la superficie principal), un espejo gRPC acotado y congelado del núcleo estable, la propia CLI `olivares` —68 comandos de nivel superior agrupados, desde `quickstart` y `serve` hasta `work`, `orchestration`, `agent`, `mcp` y `compliance`, con un test que mantiene el total de grupos de ayuda para que ningún comando nuevo llegue sin agrupar—, y un proveedor Terraform para los recursos gestionados como código. Los recopiladores se ejecutan dentro de la infraestructura del cliente en tres modos: fuentes en proceso de ruta rápida, plugins fuera de proceso que el motor supervisa mediante un canal autenticado por inicio (AutoMTLS), y un despliegue opcional de recopilador remoto→núcleo mediante TLS mutuo de certificado de cliente verificado. El núcleo almacena los datos en SQLite (nodo único, air-gap) o Postgres con seguridad a nivel de fila, donde cada operación de módulo queda fijada a un tenant en la API de almacenamiento y Postgres lo aplica de nuevo mediante FORCE row-level security: un rol de conexión con privilegios suficientes para eludirla en silencio (superusuario o `BYPASSRLS`) se rechaza en el arranque, y la única forma de superar ese rechazo es un flag explícito de activación voluntaria que dice lo que cuesta. Las lecturas de sistema entre tenants pasan por un pool de administración `BYPASSRLS` independiente, de mínimo privilegio, que nunca se usa para trabajo acotado a un tenant —una puerta declarada, no una ausente.

Visión general: [`ARCHITECTURE.md`](ARCHITECTURE.md).

## Open core, por directorio

La licencia queda resuelta desde el primer commit: **open core**, el producto completo bajo AGPL, un SDK y conectores permisivos para que el ecosistema crezca sin fricción de copyleft, y un pequeño conjunto de add-ons comerciales **aditivos** —construidos solo con `-tags enterprise`, cada uno licenciado por separado bajo términos comerciales y ausente del binario público— para las capacidades reservadas. La build AGPL es toda la plataforma de gobernanza y nunca se mutila para incentivar una venta superior; los add-ons comerciales *añaden* código nuevo que nunca estuvo en el producto abierto, de modo que una build enterprise no es idéntica a la abierta, pero nada se quita de lo que se entrega en abierto. Cada fichero fuente lleva una cabecera `SPDX-License-Identifier`, aplicada en CI.

| Directorio | Licencia | Contenido |
|---|---|---|
| `core/` | `AGPL-3.0-only` | Motor: ingesta, bus de eventos, modelo de datos, runtime de módulos, API, authn/z, auditoría, multitenancy |
| `modules/` | `AGPL-3.0-only` | Los 30 módulos de producto (inventario, access map, trabajo y leases, identidad, FinOps, evaluaciones, guardrails, …) |
| `web/` | `AGPL-3.0-only` | UI de React, embebida en el binario mediante `go:embed` |
| `sdk/` | `Apache-2.0` | Interfaces estables `SourceConnector` / `OutputConnector` / `Module` + contrato gRPC + tipos |
| `connectors/` | `Apache-2.0` | Conectores propios y de la comunidad (Claude, MCP, pg-audit, eBPF, cloud, SIEM, …) |
| `clients/` | `Apache-2.0` | SDK de cliente generados (Go, Java, Python, TypeScript) |
| Add-ons comerciales *(repositorio privado separado)* | `LicenseRef-Olivares-Commercial` | Familias de add-ons aditivas y licenciadas por separado en aplicación, MCP, identidad, seguridad de datos, profundidad de cumplimiento, operaciones y plataforma; enumeradas por área en [the matrix above](#whats-open-whats-enterprise-whats-planned), cada una un seam declarado en [`cmd/olivares/wire_noenterprise.go`](cmd/olivares/wire_noenterprise.go); construidas solo con `-tags enterprise`, nunca en este repositorio ni en el binario público |
| `docs/`, `docs-site/` | — | Documentos de diseño y el sitio de documentación del producto |

Un conector solo puede importar de `sdk/`, nunca de `core/`. Esto mantiene limpia la frontera AGPL / Apache y permite que terceros escriban conectores sin obligaciones de copyleft: aplicado por [`scripts/check-boundary.sh`](scripts/check-boundary.sh) en CI.

## Seguridad y cadena de suministro

Olivares AI se ejecuta en los hosts del cliente y mapea lo que cada agente puede tocar, por lo que el listón de seguridad es alto por diseño: primero lectura; datos mínimos en el plano de observación (el access map almacena aristas, no payloads —el almacén Knowledge gobernado solo contiene el contenido que ingieres explícitamente—); mínimo privilegio; mTLS; auditoría de solo anexado en cadena hash con checkpoints firmados; versiones firmadas. El propio access map es una superficie privilegiada y auditada: abrirlo es una acción registrada, y también lo es leer el grafo de comunicación entre agentes.

Para comunicar una vulnerabilidad o leer la política de divulgación, consulta [`SECURITY.md`](SECURITY.md) (informe privado, nunca una issue pública). El flujo de avisos se documenta en [`docs/security-advisories.md`](docs/security-advisories.md); la evidencia de preparación de la cadena de suministro está en el mapa de Best Practices de [`docs/openssf-badge.md`](docs/openssf-badge.md).

<a name="documentation"></a>
## Documentación

La documentación del producto está en [`docs-site/`](docs-site/): un sitio Diátaxis con tutoriales de instalación probados (nodo único, Docker Compose, Kubernetes/Helm, air-gapped), guías por conector con capturas reales de consola, un recetario (políticas deny-closed, presupuestos, aprobaciones, ejercicios de kill-switch, envío a SIEM), referencia de API y un glosario. Empieza por [What is Olivares AI](docs-site/src/content/docs/start/what-is-olivares-ai.md) y [Honestidad y límites](docs-site/src/content/docs/start/honesty-and-limits.md), la página que indica sin ambages qué funciona hoy, qué está en fase de diseño y qué no hace deliberadamente el producto.

## Comunidad y gobernanza

Los ficheros de salud comunitaria y gobernanza que espera quien adopta el producto están presentes y al día:

- **Cómo se toman las decisiones:** [`GOVERNANCE.md`](GOVERNANCE.md) (dirigido por mantenedores / open-core, honesto sobre la etapa del proyecto) y [`.github/CODEOWNERS`](.github/CODEOWNERS) (enrutamiento de revisión mapeado a la frontera de licencia).
- **Contributing:** [`CONTRIBUTING.md`](CONTRIBUTING.md) (configuración, DCO/CLA, SPDX, la frontera de conectores): cada cambio se envía mediante la [pull-request template](.github/PULL_REQUEST_TEMPLATE.md).
- **Conducta:** [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md) (Contributor Covenant 2.1).
- **Obtener ayuda:** [`SUPPORT.md`](SUPPORT.md), y dónde **no** informar de problemas de seguridad.
- **Cambios:** [`CHANGELOG.md`](CHANGELOG.md) (Keep a Changelog 1.1 + CalVer `vYY.M.PATCH`; beta).

## Licencia

El producto (`core/`, `modules/`, `web/`) se licencia bajo la **GNU Affero General Public License, versión 3** (`AGPL-3.0-only`). El SDK de conectores, los conectores y los SDK de cliente (`sdk/`, `connectors/`, `clients/`) se licencian bajo **Apache-2.0**. La licencia que rige un fichero concreto se expresa en su cabecera SPDX y, para una versión, en su SBOM.

> **Sin garantía, sin responsabilidad: léelo antes de desplegar.** El software libre se proporciona **tal cual**, **sin garantía de ningún tipo** y **sin responsabilidad por pérdida de datos, corrupción, interrupción de negocio o lucro cesante**. En un plano de control, eso no es una formalidad: una mala configuración puede bloquear trabajo legítimo e interrumpir producción, o dejar pasar exactamente lo que pretendías detener. Se aplican AGPL-3.0-only §§15–16 y Apache-2.0 §§7–8, además del término suplementario de este proyecto conforme a AGPL §7(a): el texto completo, incluidos usos de alto riesgo, resultados de cumplimiento y componentes de terceros, está en [`DISCLAIMER.md`](DISCLAIMER.md).

Una **licencia comercial** ofrece una excepción privada a la AGPL para organizaciones que no pueden operar bajo sus términos. Las capacidades aditivas de `enterprise/` —las familias de add-ons enumeradas por área en [the matrix above](#whats-open-whats-enterprise-whats-planned), cada una un seam declarado en el árbol público— se ofrecen como **add-ons separados y opcionales** bajo sus propios términos comerciales: código cerrado construido solo con `-tags enterprise`, nunca presente en el binario abierto. Empaquetado y precios bajo consulta. El núcleo AGPL es completo y nunca se limita desde dentro por funcionalidades. Para licencias comerciales o consultas enterprise, contacta con `enterprise@olivares.ai`. Consulta [`LICENSING.md`](LICENSING.md).

Las contribuciones requieren un sign-off DCO (`git commit -s`) y un Contributor License Agreement; consulta [`CONTRIBUTING.md`](CONTRIBUTING.md) y [`CLA.md`](CLA.md).

## Apoya el proyecto

Olivares AI es AGPL-3.0 y autoalojado: el núcleo es libre y seguirá siéndolo. Si te resulta útil y quieres apoyar directamente el trabajo, puedes patrocinarlo mediante el botón **Sponsor** de este repositorio.

El patrocinio **no** es un contrato de soporte ni compra prioridad: para saber cómo se gestionan las preguntas y los informes de error, consulta [`SUPPORT.md`](SUPPORT.md); para los términos comerciales y los add-ons enterprise, consulta [`LICENSING.md`](LICENSING.md).

---

<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/olivares-mark-dark.svg">
  <img src=".github/assets/olivares-mark-light.svg" alt="Olivares AI" width="44">
</picture>

<sub><strong>La verdad de terreno para la IA empresarial.</strong> · <a href="https://olivares.ai">olivares.ai</a> · <a href="LICENSING.md">AGPL-3.0 + commercial</a></sub>

</div>
