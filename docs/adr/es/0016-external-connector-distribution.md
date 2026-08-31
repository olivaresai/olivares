> Traducción automática. La versión en inglés es la fuente autoritativa.

# ADR-0016: Ecosistema de connectors externos — SDK público, admisión firmada, distribución por releases/OCI, índice curado de verificados

- **Status:** accepted
- **Fecha:** 2026-06-11
- **Responsables de la decisión:** Fran Olivares (alcance v1 decidido el 2026-06-09)
- **Referencias:** `LICENSING.md` (frontera de licencia), ADR-0007 (runtime go-plugin),
  ADR-0011 (AGPL/Apache/comercial), ADR-0015 (cadena de suministro),
  `docs/contracts/S02-sdk-runtime-eventbus.md`,
  `docs/contracts/S142-external-connector-sdk.md`

## Contexto y planteamiento del problema

El SDK de connectors (`sdk`, `sdk/plugin`) se diseñó desde el primer día para que un
connector nunca enlace con el motor AGPL (Apache-2.0, cero dependencias, transporte de
plugin gRPC — ADR-0007, ADR-0011), y la ADR-0007 anticipaba explícitamente
que "los terceros pueden distribuir connectors de forma independiente". Pero no existía
ningún mecanismo: los módulos del SDK no están etiquetados (untagged) y solo se consumen a
través del workspace del monorepo, la raíz de composición lanza únicamente binarios de
plugin **integrados y de primera parte** (`go:embed`), `LoadSourcePlugin` ejecuta cualquier
ruta que se le pase **sin comprobación de integridad ni procedencia**, y el catálogo del
módulo XIV cura solo entradas internas. "¿Puede mi equipo o un socio construir y publicar un
connector?" no tenía respuesta.

Abrir el ecosistema no puede significar "el host carga cualquier binario tipo `.so` al que un
operador apunte": esto es un producto de seguridad; un ejecutable sin firmar y sin atestación
cableado al plano de observación sería un agujero en la cadena de suministro.

## Factores de la decisión

- El foso (moat) de amplitud **se compone** solo si los terceros pueden contribuir
  connectors de forma segura (`ARCHITECTURE.md`, `LICENSING.md`).
- La frontera de licencia (connector = Apache, nunca importa de `/core`) debe ser
  verificable **por el tercero**, no solo en nuestra CI.
- La maquinaria de firma + admisión ya existe y está probada (admisión de modelos,
  admisión de entradas MCP, `core/secure/modelsign`): reutilizar, nunca
  reimplementar.
- Sin infraestructura de marketplace alojado en v1 (decisión comercial diferida).

## Opciones consideradas

- **Opción A — servicio de marketplace alojado**: un servicio de registro operado por
  Olivares.AI con subida/revisión/servido.
- **Opción B — SDK + certificación + firma, distribución sobre GitHub
  releases/OCI, índice estático curado de "connectors verificados" en el sitio de docs;
  admisión firmada deny-closed (denegado por defecto) en el host.**
- **Opción C — carga abierta de plugins** (ruta suministrada por el operador, sin firma),
  certificación solo como documentación.

## Resultado de la decisión

Opción elegida: **Opción B** (decidida el 2026-06-09).

1. **Contrato de SDK público.** `sdk` y `sdk/plugin` se declaran **estables v1**
   para los autores de connectors, con una política explícita de versionado/deprecación
   (`sdk/VERSIONING.md`, expuesta en la página de estabilidad del sitio de docs). Los
   tags semver (`sdk/v1.*`, `sdk/plugin/v1.*`) aterrizan con la primera versión pública del
   repositorio; hasta entonces los autores fijan un commit (el `-sdk-path` del scaffold
   cubre el bucle de desarrollo).
2. **Scaffold + guía.** Un generador sin dependencias
   (`sdk/scaffold`, CLI `olivares-connector-new`) emite un repositorio de connector
   completo y fuera del árbol (out-of-tree) — esqueleto de fuente/salida correcto según el
   contrato, test de ciclo de vida, `main` de plugin, README y una **comprobación de frontera
   independiente** (la misma regla `go list -deps` que `scripts/check-boundary.sh` aplica en
   nuestra CI, de modo que el tercero verifica la frontera AGPL/Apache en *su* CI).
3. **Canal de distribución.** Un connector publicado se entrega como un **asset de release de
   GitHub** (binario + `sha256` + paquete de atestación Sigstore) y/o un **artefacto OCI**
   (ORAS, atestación como referrer). Sin marketplace alojado en v1.
4. **Admisión firmada, deny-closed en el host.** Un plugin externo se ejecuta solo
   si la configuración de fuentes del operador fija su digest Y una atestación de cadena de
   suministro Sigstore/DSSE (procedencia SLSA / predicado SBOM) sobre ese digest
   verifica contra una política de confianza configurada por el operador
   (`connector_trust`), reutilizando `modelsign.VerifyAttestation`. El
   cargador (loader) además fija el checksum en tiempo de ejecución (`SecureConfig` de
   go-plugin). **No hay modo de observación ni escotilla de escape allow-unsigned para los
   binarios externos** — el bucle de desarrollo es "firma con tu propia clave, confía en tu
   propia clave pública" (modo bare-key).
5. **Registro de certificación (overlay del catálogo).** El módulo XIV gana un tipo de
   entrada `connector` con su propio par de admisión
   (`catalog.connector_admission_policy` / `catalog.connector_admission`):
   veredictos de procedencia/SBOM verificados por entrada, puerta de aprobación deny-closed,
   modo de observación por defecto — el rastro de certificación de cara al tenant, desacoplado
   de la puerta de ejecución del host (defensa en profundidad, como el par admit-route +
   deployment-gate de la admisión de modelos).
6. **Índice de connectors verificados.** Una **página estática curada** en el sitio de docs
   (`reference/verified-connectors`) lista los connectors de terceros cuya
   versión los mantenedores han vuelto a verificar (frontera, firma, procedencia,
   revisión de datos mínimos). El listado es por pull request; el índice es
   documentación de la verificación realizada, **no** una raíz de confianza — los operadores
   siguen fijando la identidad/clave del publicador en `connector_trust`.

### Consecuencias

- **Bueno:** los terceros construyen, firman y distribuyen connectors sin tocar el
  motor AGPL; el host nunca ejecuta código sin atestación; la certificación reutiliza
  maquinaria probada; cero servicios nuevos que operar.
- **Malo / compromisos:** no hay UX de descubrimiento/instalación más allá de docs +
  releases (un marketplace alojado la daría); los operadores gestionan los anclajes de
  confianza a mano; los connectors **de salida** externos se construyen y distribuyen del mismo
  modo, pero el cableado externo del lado del host cubre primero las fuentes de observación (la
  composición de notify aún no tiene una ruta de plugin externo).
- **Neutral / seguimientos:** *pull* OCI por parte del host (hoy el operador coloca el
  binario en disco; el pin del digest hace que el transporte sea irrelevante para la confianza);
  los módulos fuera de proceso siguen sin cablear; una capacidad de cumplimiento sondeada
  desde las admisiones de connectors; el scope npm `@olivaresai` y los tags del module-proxy en
  la exportación pública.

## Por qué se rechazaron las alternativas

- **Opción A** — operar un marketplace es un compromiso comercial que se
  difirió explícitamente; añade un servicio crítico para la confianza sin
  demanda en v1.
- **Opción C** — "cargar cualquier binario" es exactamente el agujero de cadena de
  suministro que este producto existe para cerrar; la certificación-como-prosa sin aplicación
  efectiva sería teatro de diseño-para-auditoría (`docs/SECURITY-HARDENING.md`).
