<div align="center">

<a href="https://olivares.ai"><img src=".github/assets/olivares-banner.png" alt="Olivares AI — Ground truth for enterprise AI" width="720"></a>

**Idiomas:** [English](./README.md) · **Español** · [简体中文](./README.zh.md) · [Русский](./README.ru.md) · [日本語](./README.ja.md) · [Deutsch](./README.de.md) · [Français](./README.fr.md)

**Ejecuta la IA que tu equipo ya usa, con el mismo control que tienes sobre el resto de tu infraestructura.**

[Qué hace](#qué-hace) · [Instalación](#instalación) · [Ediciones](#ediciones-y-precios) · [Documentación](#documentación) · [Comunidad](#comunidad) · [olivares.ai](https://olivares.ai)

[![License: AGPL-3.0-only](https://img.shields.io/badge/license-AGPL--3.0--only-blue)](LICENSING.md)
[![SDK & connectors: Apache-2.0](https://img.shields.io/badge/SDK%20%26%20connectors-Apache--2.0-blue)](LICENSING.md) <!-- release -->
[![Next release: 0.1](https://img.shields.io/badge/release-0.1-28282B)](https://github.com/olivaresai/olivares/releases/tag/0.1)<!-- /release -->
[![Status: beta](https://img.shields.io/badge/status-beta-F08000)](CHANGELOG.md)
[![Contributor Covenant](https://img.shields.io/badge/Contributor%20Covenant-2.1-4baaaa)](CODE_OF_CONDUCT.md)

</div>


Tus desarrolladores trabajan con Claude Code y Codex. Los agentes llaman a servidores MCP, modelos y API internas, y las tareas programadas se ejecutan por su cuenta. Cada componente tiene sus propios registros y permisos, así que no es fácil responder a preguntas sencillas: ¿qué agente cambió este archivo?, ¿quién lo aprobó?, ¿cuánto nos costó la IA este mes?

Olivares AI reúne las respuestas en un solo lugar. Se conecta a los agentes y herramientas que ya usas, muestra qué hace cada uno, aplica tus reglas antes de ejecutar una acción y mantiene un registro firmado de todo. Es un único programa que se ejecuta en tus propios servidores, y el producto completo es gratuito y de código abierto.

<div align="center">
<img src=".github/assets/motion-access-map.gif" width="840" alt="Motion diagram of the read/write access map: agents, sessions and identities on the left, the resources they reach on the right, reads in blue, writes in orange, one observed write that was never permitted flagged as a drift finding.">
<br><sub><b>El mapa de acceso</b> — lo que cada agente lee y escribe, y la escritura que nadie autorizó.</sub>
</div>

## Qué hace

- **Saber qué se está ejecutando.** Todos los agentes, sesiones, modelos, servidores MCP y herramientas en un inventario. El mapa de acceso muestra qué lee y escribe cada uno, y señala los accesos que ninguna regla permite.
- **Detener una acción antes de que cause daños.** Olivares AI tiene **cuatro puntos de aplicación deny-closed** que comprueban cada acción antes de ejecutarla: dentro de Claude Code, en el proxy de modelos, en cada llamada a una herramienta MCP y entre agentes. Una acción de riesgo espera a una segunda persona; una acción prohibida no se ejecuta. Un solo interruptor detiene todos los agentes a la vez. Si una comprobación no puede decidir, la acción no se ejecuta.
- **Controlar el gasto en IA.** Los presupuestos por equipo, agente o modelo avisan, frenan o detienen el gasto antes de que llegue la factura.
- **Dar a los agentes acceso seguro al conocimiento de tu empresa.** Conecta SharePoint, Confluence, Google Drive, Notion, Salesforce, Snowflake, S3 y PostgreSQL. Cada agente ve solo lo que la persona que lo usa tiene permiso para ver.
- **Continuar el trabajo entre sesiones.** Las tareas, los responsables y las decisiones se conservan cuando termina una sesión. Inicia, únete y detén sesiones de Claude Code, Codex y Grok desde el navegador, sin SSH.
- **Aportar pruebas cuando te las pidan.** Cada decisión se guarda en un registro firmado que hace detectable cualquier modificación posterior. Business Compliance Packs vincula las evidencias a 26 catálogos de marcos y genera informes para tu equipo de seguridad y tus auditores. Community conserva las evidencias almacenadas y sus exportaciones JSON/CSV.

Funciona con las herramientas que ya tienes: Claude Code, Codex, Grok, Cursor, gemini-cli, opencode, OpenHands y modelos locales mediante Ollama. **32 módulos** y **136 integraciones**: [todos los módulos](docs-site/src/content/docs/reference/modules/overview.md) · [todos los conectores](connectors/README.md).

Community conserva la observabilidad local, los ajustes guardados y la exportación de copias de seguridad. El envío SIEM/ITSM, la telemetría externa y la exportación de postura se incluyen en la edición base de Business.

## Instalación

Elige un método y copia su bloque. Al terminar, `olivares quickstart` muestra la dirección de la consola y un token de un solo uso para crear el primer administrador. Todas las versiones están firmadas, y cada método verifica lo que descarga antes de instalarlo ([verificar una descarga por tu cuenta](INSTALL.md#verifying-a-release)).

**Linux y macOS, un comando.** Detecta tu sistema, verifica la versión, instala solo el binario y nunca usa `sudo`.

```sh
curl -fsSL https://olivares.ai/olivares/install.sh | sh
olivares quickstart
```

**Docker.** Multi-arquitectura. Las imágenes de contenedor se basan en Debian 13 slim (con Node.js 24 para las herramientas de agente) y se ejecutan como usuario no root. Escucha en todas las interfaces del host; añade `127.0.0.1:` antes de cada `-p` para limitarlo al equipo local.

<!-- release -->
```sh
docker run -d --name olivares -p 8443:8443 -p 8444:8444 \
  -v olivares-data:/var/lib/olivares \
  docker.io/olivaresai/olivares:0.1 \
  serve --listen :8443 --grpc-listen :8444 --data-dir /var/lib/olivares
```
<!-- /release -->

**Docker Compose.** SQLite en un solo nodo, con Postgres y copias de seguridad opcionales.

```sh
git clone --depth 1 https://github.com/olivaresai/olivares.git && cd olivares
docker compose -f deploy/compose/docker-compose.yml up --wait --wait-timeout 120
```

Las sesiones trabajan en una carpeta del host: define `OLIVARES_PROJECT_DIR` con su ruta absoluta antes de `up` y Compose la monta en `/project`. En hosts cuya política de AppArmor limita los espacios de nombres de usuario (Ubuntu 24.04 y posteriores), carga antes el perfil de sesiones: consulta [Docker Compose](INSTALL.md#docker-compose).

**Debian y Ubuntu.** El paquete añade un usuario `olivares` sin inicio de sesión y un servicio reforzado; tú lo inicias.

```sh
curl -fsSLO https://github.com/olivaresai/olivares/releases/download/0.1/olivares_0.1_linux_amd64.deb
sudo dpkg -i olivares_0.1_linux_amd64.deb && sudo systemctl enable --now olivares
```

**RHEL, Fedora y SUSE.**

```sh
curl -fsSLO https://github.com/olivaresai/olivares/releases/download/0.1/olivares_0.1_linux_amd64.rpm
sudo rpm -i olivares_0.1_linux_amd64.rpm && sudo systemctl enable --now olivares
```

**Alpine.**

```sh
curl -fsSLO https://github.com/olivaresai/olivares/releases/download/0.1/olivares_0.1_linux_amd64.apk
sudo apk add --allow-untrusted olivares_0.1_linux_amd64.apk && sudo rc-service olivares start
```

En servidores ARM, usa `arm64` en lugar de `amd64`. Todos los archivos de la versión: [página de la versión](https://github.com/olivaresai/olivares/releases/tag/0.1).

**Homebrew.** macOS y Linux.

```sh
brew install olivaresai/tap/olivares && olivares quickstart
```

**Desde el código fuente.** Go 1.26+, [Task](https://taskfile.dev) y pnpm.

```sh
git clone --depth 1 https://github.com/olivaresai/olivares.git && cd olivares
task build && ./bin/olivares quickstart
```

**Redes sin conexión:** reúne la imagen firmada, el chart y el material de verificación; después, [instala en un entorno aislado](docs-site/src/content/docs/how-to/air-gap-install.md). **Windows** aún no tiene un binario nativo: usa la imagen de Docker o WSL2. Actualizaciones y vuelta a una versión anterior: [guía](docs-site/src/content/docs/how-to/upgrade-and-rollback.md). Todas las opciones en detalle: [`INSTALL.md`](INSTALL.md).

**Pruébalo primero con datos de demostración**, solo en tu equipo (la contraseña de la demo es pública):

```sh
olivares serve --seed-demo --insecure --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 --data-dir "$(mktemp -d)"
```

Después, abre http://127.0.0.1:8901.

Helm, el operador de Kubernetes, Terraform, el appliance y los artefactos de despliegue FIPS/STIG se distribuyen con Business; la instalación sin conexión requiere Enterprise. Consulta las [ediciones](docs/editions.md).

## Ediciones y precios

Community es el producto completo, gratuito y de código abierto. Business añade lo que una empresa necesita para usarlo en producción. Enterprise está pensado para grupos con infraestructuras más grandes o sujetas a regulación.

| | **Community** | **Business** | **Enterprise** |
|---|---|---|---|
| **Precio** | Gratis, AGPL-3.0 | 129 USD/mes o 1.290 USD/año | Contrato anual |
| **Qué incluye** | El producto completo: usuarios ilimitados y los cuatro puntos de aplicación deny-closed | Todo lo de Community, más Regulated Operations, AI Runtime Security, Compliance Packs e Identity & Scale, la licencia comercial, actualizaciones firmadas y soporte por correo | Todo lo de Business, más empresas, despliegues y proveedores de identidad, réplicas sin conexión y condiciones de soporte acordadas contigo |
| **Alcance** | Un proveedor de identidad activo | Una empresa, una instancia activa a la vez | Según el contrato |

**Regulated Operations** añade plazos mínimos de conservación regulatorios, la conciliación de retenciones legales en los archivos y archivos WORM en Azure y GCS. **AI Runtime Security** añade una inspección más profunda de lo que los agentes envían, reciben y ejecutan. **Compliance Packs** prepara borradores del registro de información de DORA y del paquete ISO/IEC 42001 para tu auditor. **Identity & Scale** conecta varios proveedores de identidad a la vez y permite crecer con despliegues más grandes.

[olivares.ai/pricing](https://olivares.ai/pricing) · [Qué incluye cada edición](docs/editions.md) · [Qué es abierto y qué es comercial](LICENSING.md)

## Arquitectura

Un único binario de Go con la consola integrada. Ofrece una API REST, una API gRPC, la línea de comandos `olivares` y un proveedor de Terraform. Los colectores se ejecutan dentro de tu red, y los datos permanecen en SQLite o PostgreSQL en tus servidores. [Cómo encaja todo](ARCHITECTURE.md).

## Documentación

[docs.olivares.ai](https://docs.olivares.ai) contiene guías de instalación, una guía para cada conector, recetas para políticas habituales y la referencia de la API. Empieza por [Qué es Olivares AI](docs-site/src/content/docs/start/what-is-olivares-ai.md). Lo que funciona hoy y lo que sigue previsto: [Honestidad y límites](docs-site/src/content/docs/start/honesty-and-limits.md). Versiones: [GitHub](https://github.com/olivaresai/olivares/releases) · [`CHANGELOG.md`](CHANGELOG.md).

En la web: [producto](https://olivares.ai/product) · [soluciones](https://olivares.ai/solutions) · [cómo funciona](https://olivares.ai/how-it-works) · [arquitectura](https://olivares.ai/architecture) · [seguridad](https://olivares.ai/security) · [confianza](https://olivares.ai/trust) · [comparar](https://olivares.ai/compare) · [demo](https://olivares.ai/demo) · [changelog](https://olivares.ai/changelog) · [estado](https://olivares.ai/status) · [hoja de ruta](https://olivares.ai/roadmap) · [marca](https://olivares.ai/brand) · [prensa](https://olivares.ai/press).

## Seguridad

¿Has encontrado una vulnerabilidad? Comunícala en privado siguiendo [`SECURITY.md`](SECURITY.md). Olivares AI registra qué agente accedió a qué recurso, no el contenido, y la consulta de ese registro también queda registrada. Las licencias se comprueban sin conexión; el núcleo de código abierto nunca se comunica con nosotros.

## Comunidad

Las contribuciones son bienvenidas. [`CONTRIBUTING.md`](CONTRIBUTING.md) explica la preparación del entorno, el sign-off y cómo encajan los conectores. [Código de conducta](CODE_OF_CONDUCT.md) · [Soporte](SUPPORT.md) · [Gobernanza](GOVERNANCE.md) · [Registro de cambios](CHANGELOG.md).

## Apoya el proyecto

Olivares AI se desarrolla de forma abierta. Si te resulta útil, patrocina su desarrollo en GitHub Sponsors — [olivaresai](https://github.com/sponsors/olivaresai) o [fran-olivares](https://github.com/sponsors/fran-olivares) — o invítanos a un café en Ko-fi. Los patrocinadores que quieran aparecer figuran en [`SUPPORTERS.md`](SUPPORTERS.md). El patrocinio no es un contrato de soporte ([`SUPPORT.md`](SUPPORT.md)).

[![ko-fi](https://ko-fi.com/img/githubbutton_sm.svg)](https://ko-fi.com/Z1R625SAD2)

## Licencia

El motor, los módulos y la consola son **AGPL-3.0-only**; el SDK, los conectores y los clientes son **Apache-2.0**. El código comercial se compila por separado y no está en este repositorio; licencias comerciales: `enterprise@olivares.ai`. Las contribuciones necesitan un sign-off DCO (`git commit -s`) y el [CLA](CLA.md).

> Se proporciona **tal cual**, sin garantía de ningún tipo ni responsabilidad por pérdida de datos, interrupción del negocio o lucro cesante. Consulta [`DISCLAIMER.md`](DISCLAIMER.md).

---

<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/olivares-mark-dark.svg">
  <img src=".github/assets/olivares-mark-light.svg" alt="Olivares AI" width="44">
</picture>

<sub><strong>Ground truth para la IA empresarial.</strong> · <a href="https://olivares.ai">olivares.ai</a> · <a href="LICENSING.md">AGPL-3.0 + commercial</a></sub>

</div>
