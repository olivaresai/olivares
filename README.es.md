<div align="center">

<a href="https://olivares.ai"><img src=".github/assets/olivares-banner.png" alt="Olivares AI — Ground truth para la IA empresarial" width="720"></a>

**Idiomas:** [English](./README.md) · **Español** · [简体中文](./README.zh.md) · [Русский](./README.ru.md) · [日本語](./README.ja.md) · [Deutsch](./README.de.md) · [Français](./README.fr.md)

**Ejecuta y gobierna la IA que ya usas, en tu propia infraestructura.**

[Qué es](#qué-es) · [Qué hace](#qué-hace) · [Instalación](#instalación) · [Inicio rápido](#inicio-rápido) · [Consola](#un-vistazo-a-la-consola) · [Ediciones](#ediciones-y-precios) · [Documentación](#documentación) · [Seguridad](#seguridad) · [olivares.ai](https://olivares.ai)

[![License: AGPL-3.0-only](https://img.shields.io/badge/license-AGPL--3.0--only-blue)](LICENSING.md)
[![SDK & connectors: Apache-2.0](https://img.shields.io/badge/SDK%20%26%20connectors-Apache--2.0-blue)](LICENSING.md)
[![Release: 26.10.0](https://img.shields.io/badge/release-26.10.0-28282B)](https://github.com/olivaresai/olivares/releases/tag/26.10.0)
[![Status: beta](https://img.shields.io/badge/status-beta-F08000)](CHANGELOG.md)
[![Contributor Covenant](https://img.shields.io/badge/Contributor%20Covenant-2.1-4baaaa)](CODE_OF_CONDUCT.md)

</div>

> **Beta.** 26.10.0 se entrega con archivos firmados, paquetes nativos e imágenes de contenedor. [Honestidad y límites](docs-site/src/content/docs/start/honesty-and-limits.md) indica qué funciona hoy, qué funciona bajo demanda y qué sigue en diseño.

## Qué es

Olivares AI es un plano de control autoalojado para agentes de IA: un único binario de Go con la consola incluida. Da a los agentes contexto, acceso a recursos y sesiones gestionadas, y te da a ti permisos, políticas, presupuestos y evidencia. No hay telemetría obligatoria y admite instalaciones air-gapped.

Claude Code se conecta mediante su hook `PreToolUse`/`PostToolUse`, los ajustes gestionados y el inicio y la detención desde la consola. Las CLI oficiales de Codex y Grok funcionan como sesiones gestionadas. gemini-cli, Cursor, opencode, goose, cline, OpenHands, OpenClaw, Hermes y endpoints autoalojados como Ollama son conectores; cada uno indica qué aplica y qué solo observa.

## Qué hace

<div align="center">
<img src=".github/assets/motion-access-map.gif" width="840" alt="Diagrama en movimiento del mapa de acceso de lectura/escritura: agentes, sesiones e identidades a la izquierda, los recursos a los que llegan a la derecha, lecturas en azul, escrituras en naranja, una escritura observada que nunca fue permitida marcada como hallazgo de drift.">
<br><sub><b>El mapa de acceso</b> — lo que lee y escribe cada agente en tu estate, lo permitido frente a lo observado.</sub>
</div>

- **Obsérvalo.** Un inventario de los agentes, sesiones, modelos, servidores MCP, herramientas e identidades que observan los conectores; un **mapa de acceso** de lectura/escritura con una vista de **drift** entre lo permitido y lo observado; sesiones en vivo, el grafo de orquestación, salud y SLA. El acceso que no puede clasificar aparece como `unknown`.
- **Ejecuta el trabajo.** Elementos de trabajo con responsable, dependencias, criterios de aceptación y decisiones; leases vallados, para que dos agentes no tengan el mismo elemento a la vez; sesiones de Claude Code, Codex y Grok iniciadas, conectadas, interrumpidas y detenidas desde la consola; delegación a pares autorizados mediante A2A.
- **Gobiérnalo y aplícalo.** Un motor de autorización Cedar y **cuatro puntos de aplicación deny-closed**: el hook de Claude Code, un proxy de inferencia `/v1/messages` en línea, una puerta MCP `tools/call` y una puerta de delegación A2A. Una acción no autorizada se bloquea, queda retenida para la aprobación de dos personas o se reescribe antes de ejecutarse. Los presupuestos deniegan o limitan el gasto, el break-glass necesita a dos personas y el **kill-switch** falla cerrado.
- **Aliméntalo, con gobierno.** SharePoint, Confluence, Google Drive, Notion, Salesforce, Snowflake, S3, Azure AI Search, SAP OData, PostgreSQL y un sistema de ficheros confinado a su raíz alimentan una recuperación gobernada; la habilitación se comprueba en el momento de la recuperación.
- **Demuéstralo.** Un audit ledger encadenado por hashes y firmado con Ed25519; evidencia sellada mapeada a **26 catálogos de marcos** (EU AI Act, NIST AI RMF, ISO 42001, SOC 2, ISO 27001, GDPR y otros) como familias de controles autoevaluadas, no certificaciones; envío a SIEM e ITSM (CEF, LEEF, syslog, OTLP, OCSF); WebAuthn/FIDO2, PIV/CAC, SSO, SCIM, BYOK/CMEK y derecho al olvido verificado, configurados por despliegue.

**31 módulos**, una consola, **159 integraciones**, contados a partir del código por [`scripts/check-public-counts.sh`](scripts/check-public-counts.sh). El desglose está en [`connectors/README.md`](connectors/README.md), y cada módulo con su madurez en el [catálogo de módulos](docs-site/src/content/docs/reference/modules/overview.md).

## Instalación

Elige un método. Después, `olivares quickstart` imprime la URL de la consola y un token de configuración de un solo uso. Las versiones están firmadas con cosign, con procedencia SLSA y SBOM; cada método de abajo verifica antes de instalar, y `scripts/verify-release.sh` comprueba una descarga manual (cosign y SHA-256, [cómo](INSTALL.md#verifying-a-release)). El motor arranca con HTTPS, sin credenciales predeterminadas y con un token de configuración de un solo uso.

**1 · Un comando, Linux y macOS.** El instalador detecta el sistema operativo y la arquitectura, verifica los checksums firmados y el SHA-256 del archivo, instala solo el binario y nunca ejecuta `sudo`.

```sh
curl -fsSL https://olivares.ai/olivares/install.sh | sh
olivares quickstart        # prints the console URL and the one-time setup token
```

Añade `--user` para un servicio de usuario (unidad systemd de usuario o LaunchAgent), o ejecuta el script desde un shell con privilegios con `--system --start` para un servicio de sistema. La vía manual y la matriz por sistema operativo: [`INSTALL.md`](INSTALL.md).

**2 · Docker.** Multi-arquitectura, distroless, sin root. Los puertos se publican en todas las interfaces del host; antepón `127.0.0.1:` a los mapeos `-p` para mantenerlos locales.

```sh
docker run -d --name olivares -p 8443:8443 -p 8444:8444 \
  -v olivares-data:/var/lib/olivares \
  docker.io/olivaresai/olivares \
  serve --listen :8443 --grpc-listen :8444 --data-dir /var/lib/olivares
```

`ghcr.io/olivaresai/olivares` es la misma imagen; en producción, fíjala por digest. Variantes FIPS y STIG: [`INSTALL.md`](INSTALL.md#docker).

**3 · Docker Compose.** Un stack reforzado: SQLite en un nodo, con Postgres y copia de seguridad opcionales.

```sh
git clone --depth 1 https://github.com/olivaresai/olivares.git && cd olivares
docker compose -f deploy/compose/docker-compose.yml up --wait --wait-timeout 120
```

**4 · Kubernetes.** El chart de Helm de este repositorio, o un manifiesto plano sin Helm. El chart aún no tiene publicación OCI (`publication-unverified`).

```sh
helm install olivares deploy/helm/olivares -n olivares-system --create-namespace
# or, Helm-free
kubectl create namespace olivares-system && kubectl apply -n olivares-system -f deploy/manifests/install.yaml
```

**5 · Paquetes Linux.** `.deb`, `.rpm` y `.apk` en la [página de la versión](https://github.com/olivaresai/olivares/releases/tag/26.10.0): el binario, un fichero env de ejemplo, un usuario `olivares` sin login y una unidad reforzada. El servicio no arranca al instalar.

```sh
sudo dpkg -i olivares_*_linux_amd64.deb        # Debian / Ubuntu   (sudo rpm -i … on RHEL / Fedora / SUSE; sudo apk add --allow-untrusted … on Alpine)
sudo systemctl enable --now olivares           # OpenRC hosts: sudo rc-service olivares start
```

**6 · Homebrew.** macOS y Linux, comprobado con los checksums firmados.

```sh
brew install olivaresai/tap/olivares && olivares quickstart
```

**7 · Desde el código fuente.** Go 1.26+, [Task](https://taskfile.dev) y pnpm.

```sh
task build && ./bin/olivares quickstart
```

**Air-gapped:** empaqueta la imagen, el chart y el material de verificación firmados, y verifica sin conexión con `scripts/verify-release.sh --key … --offline` ([guía](docs-site/src/content/docs/how-to/air-gap-install.md)). **Windows** aún no tiene build nativa: usa el contenedor Linux o WSL2 ([plan](INSTALL.md#windows)). Actualización y reversión: [guía](docs-site/src/content/docs/how-to/upgrade-and-rollback.md).

## Inicio rápido

```sh
# a deterministic demo estate — loopback-only (the demo password is public), no real data
olivares serve --seed-demo --insecure --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 --data-dir "$(mktemp -d)"
# open http://127.0.0.1:8901 — inventory, work, orchestration, access map + drift, policies, FinOps

# the real thing — TLS on, reachable from your network; create the first administrator with the printed token
olivares quickstart
```

La contraseña de la demo es pública: no uses la demo con datos reales. El [inicio rápido completo](docs-site/src/content/docs/start/quickstart.md) conecta una fuente pgAudit real.

## Un vistazo a la consola

| | |
|---|---|
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-dark.png"><img src="docs-site/public/console/access-map-light.png" alt="Mapa de acceso: lo que lee y escribe cada agente en tu estate; orígenes a la izquierda, recursos a la derecha."></picture><br><sub><b>Mapa de acceso</b> — orígenes a la izquierda, recursos a la derecha, lectura y escritura por color.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-drift-dark.png"><img src="docs-site/public/console/access-map-drift-light.png" alt="Drift de mínimo privilegio: accesos inesperados y concesiones sin uso superpuestos al mapa de acceso."></picture><br><sub><b>Drift de mínimo privilegio</b> — observado pero no permitido, y concesiones que nadie usa.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/agentops-dark.png"><img src="docs-site/public/console/agentops-light.png" alt="Sesiones de Claude Code creadas, conectadas y gobernadas desde la consola."></picture><br><sub><b>Sesiones</b> — crea, conéctate y gobierna sesiones desde la consola, sin SSH.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/work-dark.png"><img src="docs-site/public/console/work-light.png" alt="Trabajo: el backlog duradero entre sesiones de elementos de trabajo y decisiones."></picture><br><sub><b>Trabajo</b> — el backlog duradero entre sesiones: elementos, titularidad, aceptación, decisiones.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/security-dark.png"><img src="docs-site/public/console/security-light.png" alt="Seguridad y forense: hallazgos de guardrails, la cola de anomalías y análisis forense a prueba de manipulación."></picture><br><sub><b>Seguridad y forense</b> — hallazgos de guardrails, anomalías, análisis forense a prueba de manipulación.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/finops-dark.png"><img src="docs-site/public/console/finops-light.png" alt="FinOps: gasto por modelo, uso de tokens, presupuestos y una proyección de run-rate."></picture><br><sub><b>FinOps</b> — gasto por modelo y agente, presupuestos que deniegan o limitan, run-rate.</sub> |

Las pantallas vienen de la demo sobre el binario en ejecución. Todas las pantallas: la [referencia de la consola](docs-site/src/content/docs/reference/console.md).

## Ediciones y precios

Community es el producto completo bajo AGPL-3.0, con usuarios ilimitados y los cuatro puntos de aplicación deny-closed. Business y Enterprise añaden código comercial, compilado solo con `-tags enterprise`; nada de Community se quita ni se limita.

| Edición | Precio | Incluye |
|---|---|---|
| **Community** | Gratis, AGPL-3.0 | El producto completo autoalojado. Usuarios ilimitados, un proveedor de identidad activo. |
| **Business** | 129 USD/mes o 1.290 USD/año | La licencia comercial, el canal de versiones firmado, soporte por correo en horario laboral, y **Regulated Operations**, **AI Runtime Security**, **Compliance Packs** e **Identity & Scale**. Usuarios ilimitados; una entidad jurídica; hasta dos despliegues de producción, cada uno con uno de staging; hasta cinco proveedores de identidad activos. |
| **Enterprise** | Contrato | Más entidades jurídicas, despliegues y proveedores de identidad, mirrors air-gapped, LTS a medida y condiciones de soporte, con un pedido anual. |

Condiciones de compra: [olivares.ai/pricing](https://olivares.ai/pricing). Qué es abierto y qué es comercial: [`LICENSING.md`](LICENSING.md).

## Arquitectura

Un único binario estático de Go incluye la consola y sirve cuatro interfaces: la API REST, un espejo gRPC del núcleo estable, la CLI `olivares` y un proveedor de Terraform. Los colectores se ejecutan dentro de tu infraestructura. El almacén es SQLite o Postgres con seguridad a nivel de fila, aplicada en la API del almacén y de nuevo por Postgres. Detalles, incluido el plano de trabajo: [`ARCHITECTURE.md`](ARCHITECTURE.md).

## Documentación

[docs.olivares.ai](https://docs.olivares.ai) — tutoriales de instalación probados (nodo único, Docker Compose, Kubernetes/Helm, air-gapped), guías de conectores con capturas reales de la consola, un recetario (políticas deny-closed, presupuestos, aprobaciones, ejercicios de kill-switch, envío a SIEM), referencia de API y un glosario. Empieza por [Qué es Olivares AI](docs-site/src/content/docs/start/what-is-olivares-ai.md). En el sitio: [producto](https://olivares.ai/product) · [soluciones](https://olivares.ai/solutions) · [cómo funciona](https://olivares.ai/how-it-works) · [arquitectura](https://olivares.ai/architecture) · [seguridad](https://olivares.ai/security) · [confianza](https://olivares.ai/trust) · [comparar](https://olivares.ai/compare) · [demo](https://olivares.ai/demo) · [changelog](https://olivares.ai/changelog) · [estado](https://olivares.ai/status) · [hoja de ruta](https://olivares.ai/roadmap) · [marca](https://olivares.ai/brand) · [prensa](https://olivares.ai/press). Versiones: [GitHub](https://github.com/olivaresai/olivares/releases) · [`CHANGELOG.md`](CHANGELOG.md).

## Seguridad

Informa de una vulnerabilidad en privado a través de [`SECURITY.md`](SECURITY.md), no en un issue público. El mapa de acceso guarda aristas, no contenidos, y abrirlo queda auditado. La verificación de licencia es offline; el núcleo AGPL no hace ninguna llamada de licencia. Avisos: [`docs/security-advisories.md`](docs/security-advisories.md); evidencia de la cadena de suministro: [`docs/openssf-badge.md`](docs/openssf-badge.md).

## Comunidad

[`CONTRIBUTING.md`](CONTRIBUTING.md) (configuración, DCO/CLA, SPDX, la frontera de conectores) · [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md) · [`SUPPORT.md`](SUPPORT.md) · [`GOVERNANCE.md`](GOVERNANCE.md) · [`CHANGELOG.md`](CHANGELOG.md) (Keep a Changelog, CalVer `YY.M.PATCH`).

## Licencia

`core/`, `modules/` y `web/` son **AGPL-3.0-only**; `sdk/`, `connectors/` y `clients/` son **Apache-2.0**, y un conector nunca importa el motor. El código comercial se compila solo con `-tags enterprise` y no está en este repositorio. Licencias comerciales: `enterprise@olivares.ai` — [`LICENSING.md`](LICENSING.md). Las contribuciones necesitan un sign-off DCO (`git commit -s`) y el [CLA](CLA.md).

> **Sin garantía.** El software se proporciona **tal cual**, **sin garantía de ningún tipo** y **sin responsabilidad por pérdida de datos, interrupción del negocio o lucro cesante**. Se aplican AGPL-3.0-only §§15–16, Apache-2.0 §§7–8 y la cláusula complementaria de este proyecto — [`DISCLAIMER.md`](DISCLAIMER.md).

## Apoya el proyecto

Patrocina el proyecto con GitHub Sponsors — [github.com/sponsors/olivaresai](https://github.com/sponsors/olivaresai) o [github.com/sponsors/fran-olivares](https://github.com/sponsors/fran-olivares) — o una vez en Ko-fi. El patrocinio no es un contrato de soporte ([`SUPPORT.md`](SUPPORT.md)); los patrocinadores que piden aparecer figuran en [`SUPPORTERS.md`](SUPPORTERS.md).

[![ko-fi](https://ko-fi.com/img/githubbutton_sm.svg)](https://ko-fi.com/Z1R625SAD2)

---

<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/olivares-mark-dark.svg">
  <img src=".github/assets/olivares-mark-light.svg" alt="Olivares AI" width="44">
</picture>

<sub><strong>Ground truth para la IA empresarial.</strong> · <a href="https://olivares.ai">olivares.ai</a> · <a href="LICENSING.md">AGPL-3.0 + commercial</a></sub>

</div>
