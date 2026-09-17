<div align="center">

<a href="https://olivares.ai"><img src=".github/assets/olivares-banner.png" alt="Olivares AI — Ground truth para la IA empresarial" width="720"></a>

**Idiomas:** [English](./README.md) · **Español** · [简体中文](./README.zh.md) · [Русский](./README.ru.md) · [日本語](./README.ja.md) · [Deutsch](./README.de.md) · [Français](./README.fr.md)

**Ejecuta y gobierna la IA que ya usas — en tu propia infraestructura, con una sola ground truth.**

[Qué es](#qué-es) · [Qué hace](#qué-hace) · [Instalación](#instalación) · [Inicio rápido](#inicio-rápido) · [Consola](#un-vistazo-a-la-consola) · [Ediciones](#ediciones-y-precios) · [Documentación](#documentación) · [Seguridad](#seguridad) · [olivares.ai](https://olivares.ai)

[![License: AGPL-3.0-only](https://img.shields.io/badge/license-AGPL--3.0--only-blue)](LICENSING.md)
[![SDK & connectors: Apache-2.0](https://img.shields.io/badge/SDK%20%26%20connectors-Apache--2.0-blue)](LICENSING.md)
[![Release: v26.9.0](https://img.shields.io/badge/release-v26.9.0-28282B)](https://github.com/olivaresai/olivares/releases/tag/v26.9.0)
[![Status: beta](https://img.shields.io/badge/status-beta-F08000)](CHANGELOG.md)
[![Contributor Covenant](https://img.shields.io/badge/Contributor%20Covenant-2.1-4baaaa)](CODE_OF_CONDUCT.md)

</div>

> **Beta**, en desarrollo activo. **v26.9.0** se entrega con archivos firmados, paquetes nativos e imágenes de contenedor. Qué funciona hoy, qué está bajo demanda y qué se encuentra en fase de diseño se indica en [Honestidad y límites](docs-site/src/content/docs/start/honesty-and-limits.md).

## Qué es

El estate de IA que tienes hoy son agentes de programación, servidores MCP, endpoints de modelos, cuentas de servicio y trabajos programados, repartidos por máquinas que nunca formaron un único sistema. Nadie puede decir, desde un solo lugar, qué se está ejecutando, quién lo puso en marcha, a qué llegó, cuánto costó y quién dio su conformidad.

Olivares AI es **un único binario de Go autoalojado, con la consola incluida**, que mantiene ese estate unido: da a la IA lo que necesita para trabajar (contexto, acceso a recursos, sesiones gestionadas) y te da a ti los permisos, las políticas, los presupuestos y la evidencia para operarlo. Autoalojado, sin telemetría obligatoria, con instalaciones air-gapped admitidas.

Claude Code se integra al nivel más profundo (el hook `PreToolUse`/`PostToolUse`, los ajustes gestionados, el inicio y la detención desde la consola); las CLI oficiales de Codex y Grok son drivers de sesión de primera clase; gemini-cli, Cursor, opencode, goose, cline, OpenHands, OpenClaw, Hermes y endpoints autoalojados como Ollama son conectores, cada uno indicando qué puede aplicar y qué solo puede observar. La build AGPL es todo el producto, nunca limitado por funcionalidades desde dentro; ningún plan cuenta usuarios.

## Qué hace

<div align="center">
<img src=".github/assets/motion-access-map.gif" width="840" alt="Diagrama en movimiento del mapa de acceso de lectura/escritura: agentes, sesiones e identidades a la izquierda, los recursos a los que llegan a la derecha, lecturas en azul, escrituras en naranja, una escritura observada que nunca fue permitida marcada como hallazgo de drift.">
<br><sub><b>El mapa de acceso</b> — lo que lee y escribe cada agente en tu estate, lo permitido frente a lo observado.</sub>
</div>

- **Véelo.** Inventario de cada agente, sesión, modelo, servidor MCP, herramienta e identidad descubiertos; un **mapa de acceso** de lectura/escritura con una vista de **drift** de Permitido frente a Observado; sesiones en vivo, el grafo de orquestación, salud y SLA. Lo que no puede ver se marca como `unknown`, nunca se adivina.
- **Ejecuta el trabajo.** Elementos de trabajo duraderos con titularidad, dependencias, criterios de aceptación y decisiones; leases vallados, para que dos agentes no puedan ser titulares del mismo trabajo a la vez; sesiones de Claude Code, Codex y Grok iniciadas, conectadas, interrumpidas y detenidas desde la consola; delegación a pares autorizados mediante A2A.
- **Gobiérnalo y aplícalo.** Un motor de autorización Cedar y **cuatro puntos de aplicación deny-closed** — el hook de Claude Code, un proxy de inferencia `/v1/messages` en línea, una puerta MCP `tools/call` y una puerta de delegación A2A — para que una acción no autorizada se bloquee, quede retenida a la espera de la aprobación de dos personas o se reescriba antes de ejecutarse. Presupuestos que deniegan o limitan el gasto, break-glass con control dual y un **kill-switch** del estate que falla cerrado.
- **Aliméntalo, con gobierno.** Fuentes de contenido (SharePoint, Confluence, Google Drive, Notion, Salesforce, Snowflake, S3, Azure AI Search, SAP OData, PostgreSQL, un sistema de ficheros confinado a su raíz) hacia una recuperación gobernada, con la habilitación aplicada deny-closed en el momento de la recuperación.
- **Demuéstralo.** Un audit ledger encadenado mediante hashes y firmado con Ed25519; evidencia sellada mapeada a **26 catálogos de marcos** (EU AI Act, NIST AI RMF, ISO 42001, SOC 2, ISO 27001, GDPR…) — familias de controles autoevaluadas, no certificaciones; envío a SIEM/ITSM (CEF/LEEF/syslog/OTLP/OCSF); WebAuthn/FIDO2, PIV/CAC, SSO, SCIM, BYOK/CMEK y derecho al olvido verificado, configurados por despliegue.

**30 módulos**, una consola, **158 integraciones** — recuentos derivados del código y aplicados en cada push por [`scripts/check-public-counts.sh`](scripts/check-public-counts.sh); el desglose está en [`connectors/README.md`](connectors/README.md), cada módulo con su madurez en el [catálogo de módulos](docs-site/src/content/docs/reference/modules/overview.md).

## Instalación

Elige un método: un comando instala, y después `olivares quickstart` imprime la URL de la consola y el token de configuración de un solo uso. Cada versión está firmada con cosign, con procedencia SLSA y SBOM; cada vía de abajo verifica antes de instalar, y `scripts/verify-release.sh` comprueba una descarga manual (cosign + SHA-256, [cómo](INSTALL.md#verifying-a-release)). El motor es **seguro por defecto**: enlace a loopback, HTTPS en el primer arranque, sin credenciales predeterminadas, un token de configuración de un solo uso impreso al primer inicio.

**1 · Un comando, Linux y macOS** — el instalador verificado: detecta el sistema operativo y la arquitectura, verifica los checksums firmados y el SHA-256 del archivo, instala solo el binario, nunca ejecuta `sudo`.

```sh
curl -fsSL https://olivares.ai/olivares/install.sh | sh
olivares quickstart        # prints the console URL and the one-time setup token
```

Añade `--user` para un servicio de usuario (unidad systemd de usuario o LaunchAgent), o ejecuta el script verificado desde un shell privilegiado con `--system --start` para un servicio de sistema. ¿Prefieres descargar, verificar y ejecutar a mano? La vía del binario manual y la matriz por sistema operativo: [`INSTALL.md`](INSTALL.md).

**2 · Docker** — multi-arquitectura, distroless, sin root; el mapeo del host lo mantiene solo en loopback.

```sh
docker run -d --name olivares -p 127.0.0.1:8443:8443 -p 127.0.0.1:8444:8444 \
  -v olivares-data:/var/lib/olivares \
  docker.io/olivaresai/olivares \
  serve --listen :8443 --grpc-listen :8444 --data-dir /var/lib/olivares
```

`ghcr.io/olivaresai/olivares` es la misma imagen por digest; en producción, fija por digest. Variantes de imagen FIPS y STIG: [`INSTALL.md`](INSTALL.md#docker).

**3 · Docker Compose** — una pila endurecida, SQLite de un solo nodo con Postgres y copia de seguridad opcionales.

```sh
git clone --depth 1 https://github.com/olivaresai/olivares.git && cd olivares
docker compose -f deploy/compose/docker-compose.yml up --wait --wait-timeout 120
```

**4 · Kubernetes** — el chart de Helm del árbol, o un manifiesto plano sin Helm; el chart aún no está publicado en un registro OCI.

```sh
helm install olivares deploy/helm/olivares -n olivares-system --create-namespace
# or, Helm-free
kubectl create namespace olivares-system && kubectl apply -n olivares-system -f deploy/manifests/install.yaml
```

**5 · Paquetes Linux** — `.deb`, `.rpm`, `.apk` desde la [página de la versión](https://github.com/olivaresai/olivares/releases/tag/v26.9.0): el binario, un fichero env de ejemplo, un usuario `olivares` sin login y una unidad endurecida; el servicio no se arranca por ti.

```sh
sudo dpkg -i olivares_*_linux_amd64.deb        # Debian / Ubuntu   (sudo rpm -i … on RHEL / Fedora / SUSE; sudo apk add --allow-untrusted … on Alpine)
sudo systemctl enable --now olivares           # OpenRC hosts: sudo rc-service olivares start
```

**6 · Homebrew** — macOS y Linux, comprobado contra los checksums firmados.

```sh
brew install olivaresai/tap/olivares && olivares quickstart
```

**7 · Desde el código fuente** — Go 1.26+, [Task](https://taskfile.dev), pnpm.

```sh
task build && ./bin/olivares quickstart
```

**Air-gapped**: empaqueta la imagen firmada, el chart y el material de verificación y verifica sin red con `scripts/verify-release.sh --key … --offline` ([guía](docs-site/src/content/docs/how-to/air-gap-install.md)). **Windows** aún no se construye: ejecuta el contenedor Linux o WSL2 ([plan](INSTALL.md#windows)). Actualizaciones y rollback: [guía](docs-site/src/content/docs/how-to/upgrade-and-rollback.md).

## Inicio rápido

```sh
# a deterministic demo estate — loopback-only, no real data
olivares serve --seed-demo --insecure --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 --data-dir "$(mktemp -d)"
# open http://127.0.0.1:8901 — inventory, work, orchestration, access map + drift, policies, FinOps

# the real thing — TLS on, loopback; create the first administrator with the printed token
olivares quickstart
```

La semilla de demostración es solo para aprender (contraseña pública en el árbol de fuentes): nunca la apuntes a datos reales. La CI recorre la misma ruta con `task smoke:quickstart` y comprueba los recuentos del mapa de acceso y el drift (20 nodos / 13 aristas, con 8 accesos inesperados y 2 concesiones sin uso). El [inicio rápido completo](docs-site/src/content/docs/start/quickstart.md) conecta un conector pgAudit real.

## Un vistazo a la consola

| | |
|---|---|
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-dark.png"><img src="docs-site/public/console/access-map-light.png" alt="Mapa de acceso: lo que lee y escribe cada agente en tu estate; orígenes a la izquierda, recursos a la derecha."></picture><br><sub><b>Mapa de acceso</b> — orígenes a la izquierda, recursos a la derecha, lectura y escritura por color.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-drift-dark.png"><img src="docs-site/public/console/access-map-drift-light.png" alt="Drift de mínimo privilegio: accesos inesperados y concesiones sin uso superpuestos al mapa de acceso."></picture><br><sub><b>Drift de mínimo privilegio</b> — observado pero no permitido, y concesiones que nadie usa.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/agentops-dark.png"><img src="docs-site/public/console/agentops-light.png" alt="Sesiones de Claude Code creadas, conectadas y gobernadas desde la consola."></picture><br><sub><b>Sesiones</b> — crea, conéctate y gobierna sesiones desde la consola, sin SSH.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/work-dark.png"><img src="docs-site/public/console/work-light.png" alt="Trabajo: el backlog duradero entre sesiones de elementos de trabajo y decisiones."></picture><br><sub><b>Trabajo</b> — el backlog duradero entre sesiones: elementos, titularidad, aceptación, decisiones.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/security-dark.png"><img src="docs-site/public/console/security-light.png" alt="Seguridad y forense: hallazgos de guardrails, la cola de anomalías y análisis forense a prueba de manipulación."></picture><br><sub><b>Seguridad y forense</b> — hallazgos de guardrails, anomalías, análisis forense a prueba de manipulación.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/finops-dark.png"><img src="docs-site/public/console/finops-light.png" alt="FinOps: gasto por modelo, uso de tokens, presupuestos y una proyección de run-rate."></picture><br><sub><b>FinOps</b> — gasto por modelo y agente, presupuestos que deniegan o limitan, run-rate.</sub> |

Cada imagen fija es una captura del estate de demostración sembrado que sirve el binario en ejecución. El mapa completo de pantallas: la [referencia de la consola](docs-site/src/content/docs/reference/console.md).

## Ediciones y precios

La build AGPL es toda la plataforma, nunca limitada por funcionalidades desde dentro. Los add-ons comerciales son código aditivo encima, nunca funcionalidades retiradas; una suscripción es la credencial para descargar packs de módulos firmados. Las cuentas de usuario son ilimitadas en el motor autoalojado, y los **cuatro puntos de aplicación deny-closed** están abiertos.

| Edición | Para quién | Qué añade |
|---|---|---|
| **Community** | Cualquiera. Gratis, AGPL-3.0, usuarios ilimitados. | El producto completo, autoalojado. Sin puerta de licencia sobre el núcleo. |
| **Business** | Una organización que lo adopta. Precio por despliegue, nunca por puesto. | Servicios y packs opcionales, no funciones del núcleo: la licencia comercial, un canal de versiones firmadas y mantenido, soporte por correo en horario laboral, y cuatro add-ons opcionales: **Regulated Operations**, **Compliance Packs**, **AI Runtime Security** e **Identity & Scale** (que incluye el cockpit de sesiones de las herramientas oficiales). Los cuatro juntos son **Business Max**. |
| **Cloud** | Equipos que quieren el mismo plano operado por nosotros, prepago, sobre infraestructura compartida. | Un plano de control gestionado con topes publicados. No hay periodo de prueba de Cloud; la opción gratuita sigue siendo Community autoalojado. |
| **Enterprise** | Estates regulados, multi-entidad y a gran escala. | Un contrato, acordado por correo y firmado en un pedido anual. |

Precios, la matriz de add-ons y las condiciones de compra: [olivares.ai/pricing](https://olivares.ai/pricing). La matriz de abierto/comercial/previsto: [`LICENSING.md`](LICENSING.md).

## Arquitectura

Un único binario estático de Go embebe la consola y expone cuatro superficies: la API REST (principal), un espejo gRPC acotado del núcleo estable, la CLI `olivares` y un proveedor de Terraform. Los collectors se ejecutan dentro de tu infraestructura; el almacén es SQLite o Postgres con seguridad a nivel de fila, aplicada una vez en la API del almacén y de nuevo por Postgres. El cuadro completo, incluido el plano de trabajo: [`ARCHITECTURE.md`](ARCHITECTURE.md).

## Documentación

[docs.olivares.ai](https://docs.olivares.ai) — tutoriales de instalación probados (nodo único, Docker Compose, Kubernetes/Helm, air-gapped), guías de conectores con capturas reales de la consola, un recetario (políticas deny-closed, presupuestos, aprobaciones, ejercicios de kill-switch, envío a SIEM), referencia de API y un glosario. Empieza por [Qué es Olivares AI](docs-site/src/content/docs/start/what-is-olivares-ai.md). En el sitio: [producto](https://olivares.ai/product) · [soluciones](https://olivares.ai/solutions) · [cómo funciona](https://olivares.ai/how-it-works) · [arquitectura](https://olivares.ai/architecture) · [seguridad](https://olivares.ai/security) · [confianza](https://olivares.ai/trust) · [comparar](https://olivares.ai/compare) · [demo](https://olivares.ai/demo) · [changelog](https://olivares.ai/changelog) · [estado](https://olivares.ai/status) · [hoja de ruta](https://olivares.ai/roadmap) · [marca](https://olivares.ai/brand) · [prensa](https://olivares.ai/press). Versiones: [GitHub](https://github.com/olivaresai/olivares/releases) · [`CHANGELOG.md`](CHANGELOG.md).

## Seguridad

Comunica una vulnerabilidad de forma privada mediante [`SECURITY.md`](SECURITY.md), nunca como una issue pública. El motor es de lectura primero y opera con datos mínimos: el mapa de acceso almacena aristas, no payloads, y abrirlo es una acción registrada. Verificar una licencia nunca nos llama; el núcleo AGPL no hace ninguna llamada de licencia. Flujo de avisos: [`docs/security-advisories.md`](docs/security-advisories.md); evidencia de la cadena de suministro: [`docs/openssf-badge.md`](docs/openssf-badge.md).

## Comunidad

[`CONTRIBUTING.md`](CONTRIBUTING.md) (configuración, DCO/CLA, SPDX, la frontera de conectores) · [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md) · [`SUPPORT.md`](SUPPORT.md) · [`GOVERNANCE.md`](GOVERNANCE.md) · [`CHANGELOG.md`](CHANGELOG.md) (Keep a Changelog, CalVer `vYY.M.PATCH`).

## Licencia

`core/`, `modules/` y `web/` son **AGPL-3.0-only**; `sdk/`, `connectors/` y `clients/` son **Apache-2.0**, y un conector nunca importa el motor. Los add-ons comerciales son independientes, opcionales y de código cerrado — se construyen solo con `-tags enterprise`, nunca en este repositorio; licencias comerciales: `enterprise@olivares.ai` — [`LICENSING.md`](LICENSING.md). Las contribuciones requieren un sign-off DCO (`git commit -s`) y el [CLA](CLA.md).

> **Sin garantía, sin responsabilidad.** El software se proporciona **tal cual**, **sin garantía de ningún tipo** y **sin responsabilidad por pérdida de datos, interrupción del negocio o lucro cesante**. En un plano de control no es una formalidad: una mala configuración puede bloquear trabajo legítimo o dejar pasar exactamente lo que pretendías detener. Se aplican AGPL-3.0-only §§15–16, Apache-2.0 §§7–8 y el término suplementario de este proyecto — [`DISCLAIMER.md`](DISCLAIMER.md).

## Apoya el proyecto

El núcleo es libre y seguirá siéndolo; mantener cada versión firmada, verificada y al día es un trabajo sostenido. Patrocínalo mediante GitHub Sponsors — [github.com/sponsors/olivaresai](https://github.com/sponsors/olivaresai) o [github.com/sponsors/fran-olivares](https://github.com/sponsors/fran-olivares) — o con una aportación puntual en Ko-fi. El patrocinio no es un contrato de soporte ([`SUPPORT.md`](SUPPORT.md)); quienes pidan figurar aparecen en [`SUPPORTERS.md`](SUPPORTERS.md).

[![ko-fi](https://ko-fi.com/img/githubbutton_sm.svg)](https://ko-fi.com/Z1R625SAD2)

---

<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/olivares-mark-dark.svg">
  <img src=".github/assets/olivares-mark-light.svg" alt="Olivares AI" width="44">
</picture>

<sub><strong>Ground truth para la IA empresarial.</strong> · <a href="https://olivares.ai">olivares.ai</a> · <a href="LICENSING.md">AGPL-3.0 + commercial</a></sub>

</div>
