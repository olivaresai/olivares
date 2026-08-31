> Traducción automática. La versión en inglés es la fuente autoritativa.

# ADR-0024: Semántica offline DDIL por plano y un único formato de bundle firmado

- **Status:** accepted
- **Date:** 2026-07-09
- **Deciders:** Fran Olivares (ratified the three questions below during design, 2026-07-09)
- **References:** the DDIL work brief; the OTA update framework
  (`core/release/manifest.go`); ADR-0009
  (hash-chained audit ledger); ADR-0013 (embedded Cedar PDP); ADR-0021 (durable
  JetStream bus, enterprise add-on); ADR-0022 (source scoping, forbid-overrides);
  the durable bus seam (`core/eventbus`, ADR-0021); break-glass.

## Contexto y planteamiento del problema

Olivares se despliega en el edge táctico / desconectado (DDIL del DoD: «se espera que las
unidades operen al menos parcialmente desconectadas… en redes air-gapped… y en el edge
táctico»). El comprador de edge no nos pide «integrar un enlace por satélite»: un bearer
pLEO/satélite es simplemente IP intermitente y la aplicación funciona sobre él sin cambios.
Lo que exige es que la gobernanza siga funcionando cuando el enlace cae durante horas o
días y regresa en ventanas breves («salida a superficie del submarino»).

Los componentes básicos ya existen y se verificaron durante el discovery:

- El **ledger de auditoría ya es un store local duradero, por tenant, encadenado por hash y
  firmado** (`core/internal/store/sqlstore/audit.go`; ADR-0009). La desconexión no crea
  huecos: simplemente impide que avance el **cursor de forwarding** fuera del equipo
  (`modules/siemforward`, impulsado por la plataforma de eventing). No hay un búfer de
  auditoría solo en RAM que pueda perderse.
- El **PDP evalúa contra el store LOCAL de políticas** (Cedar integrado, ADR-0013), por lo
  que la política ya funciona offline. Lo que no se ha decidido es su *obsolescencia*:
  ¿durante cuánto tiempo puede un nodo desconectado seguir confiando en una política que ya
  no puede actualizar?
- El **bus duradero** es una superposición JetStream, solo para el líder y al menos una vez
  (ADR-0021), cuyo backend es una compilación enterprise privada; el árbol OSS solo entrega
  la costura. Es un backbone de *distribución*, no un spool de disco local.
- **El updater OTA ya define un bundle firmado** para actualizaciones air-gap: un tar gzip
  de un `manifest.json` JSON más una firma Ed25519 separada sobre los bytes literales con
  separación de dominio (`tag || manifest`, tag `olivares.update-manifest.v1\n`), verificada
  ANTES del parseo (`core/release/manifest.go`). También existen un
  `airgap-bundle.sh` separado (cosign, imágenes + chart) y `core/dr/bundle.go` (snapshot DR
  sellado con AES-GCM).

Deben resolverse tres preguntas antes de escribir cualquier código DDIL porque definen la
dirección fail-safe, no el mecanismo.

## Motivadores de la decisión

- **Fail-safe en la dirección correcta.** Un plano de control de gobernanza nunca debe
  *escalar* privilegios por haber perdido el enlace ni *perder silenciosamente* evidencias.
- **Seguridad de la misión en el edge.** Una interrupción del enlace medida en horas no debe
  acabar con una misión si la respuesta segura ya se conocía localmente.
- **Sin proliferación de formatos.** «Un formato de bundle verificable, no dos» (brief de
  diseño DDIL). Una segunda implementación artesanal de un sobre firmado es otro lugar
  donde equivocarse con la separación de dominios: exactamente la trampa de reutilización
  de claves entre protocolos que el updater OTA ya resolvió.
- **Honestidad.** Límites declarados y documentados (presupuestos de disco, TTL, qué no
  sobrevive a una interrupción infinita) en lugar de truncamiento silencioso.

## Opciones consideradas

### Q1 — Confianza offline en la política

- **A. Asimétrica (deny perpetuo, allow caduca).** Las reglas restrictivas (deny ABAC,
  `forbid` Cedar) siguen aplicándose indefinidamente offline; los grants positivos (`allow`
  Cedar con ámbito, ADR-0019/ADR-0022) caducan después de un `policy_max_staleness` firmado
  y fallan con deny-closed.
- **B. Deny-closed total al caducar el TTL.** Tras el TTL, el nodo deja de gobernar por
  completo.
- **C. No caducar nunca, solo advertir.**

### Q2 — Comportamiento de auditoría al agotarse el presupuesto de disco local

- **A. Fail-closed predeterminado, degradación opt-in.** `block` predeterminado: rechazar
  nuevas acciones gobernadas antes de perder evidencias. `degrade` opt-in: sellar el
  segmento y añadir un **marcador de hueco firmado y dentro de la cadena** para que la
  pérdida sea evidente ante manipulaciones, nunca silenciosa.
- **B. Siempre fail-closed.**
- **C. Siempre degradar.**

### Q3 — Unificación del formato de bundle

- **A. Extraer `core/sigbundle` + un registro de tags de dominio.** Elevar el sobre de
  actualización OTA a un paquete compartido; refactorizar `core/release` para consumirlo
  tras una prueba golden de identidad de bytes; este trabajo DDIL y el feed de security
  advisories añaden sus propios tags de dominio.
- **B. Dejar `core/release` intacto; cada sesión copia el patrón.**

## Resultado de la decisión

**Q1 → Opción A (asimétrica).** Offline, después de `policy_max_staleness`:

| Clase de regla | Offline, TTL caducado | Justificación |
|---|---|---|
| Deny ABAC | **se sigue aplicando** | una restricción obsoleta solo puede restringir, nunca escalar |
| `forbid` Cedar (absoluto, ADR-0022) | **se sigue aplicando** | igual; forbid ya prevalece sobre todo |
| Grant positivo / `allow` Cedar | **caducado → deny-closed** | «un grant caducado nunca debe autorizar» |
| Break-glass | disponible, con su propia caducidad de 1 h/24 h | la vía de escape offline sancionada |

`policy_max_staleness` es un ajuste del operador (72 h por defecto) transportado y firmado
en el bundle de políticas; la consola/CLI muestran de forma destacada la antigüedad y la
caducidad.

**Q2 → Opción A (fail-closed predeterminado, degradación opt-in).** Configuración
`audit.spool.on_full`:

- `block` (predeterminado): se rechazan las nuevas acciones gobernadas (`503`, deny-closed);
  las lecturas siguen sirviéndose; la consola/CLI muestran «audit spool full — governance
  halted».
- `degrade` (opt-in explícito): sella el segmento actual y añade un marcador `audit.gap`
  firmado y dentro de la cadena `{from_seq, to_seq, reason: "spool_full", count, at}` para
  que la cadena siga siendo continua y la pérdida se pueda demostrar.
  `audit.spool.max_bytes` se declara y documenta.

El marcador de hueco es la ÚNICA discontinuidad sancionada de la cadena; el verificador de
archivos offline (`core/audit/archiveverify.go`) se amplía para reconocer un marcador de
hueco firmado como límite *declarado* en lugar de un fallo `seq-gap`.

**Q3 → Opción A (extraer `core/sigbundle`).** Un único sobre:

```
core/sigbundle/
  SigningInput(tag, payload) = tag || payload           // verbatim, no canonicalization
  Sign(tag, payload, priv) / Verify(tag, bundle, sig, pub)   // Ed25519, detached, verify-BEFORE-parse
  Envelope: tar.gz{ manifest.json, manifest.json.sig, <payload files by sha256> }
  Manifest: schema_version, kind, created_at, expires?, entries[{name, sha256, size}]
```

`core/release` se refactoriza para reutilizar `sigbundle.SigningInput` con el tag
`olivares.update-manifest.v1\n`, protegido por una prueba golden que afirma que
`release.ManifestSigningInput(b)` permanece idéntico byte a byte (para que todas las
firmas de versiones ya emitidas sigan verificándose). El **registro de tags de dominio**
(una tabla + una prueba de unicidad/sin colisión de prefijos) registra cada tag:

| Tag | Propietario | Nota |
|---|---|---|
| `olivares.update-manifest.v1\n` | `core/release` (update manifest) | idéntico byte a byte tras la refactorización |
| `olivares.ddil-bundle.v1\n` | este trabajo DDIL | NUEVO: bundle air-gap de política+auditoría+evidencias |
| `olivares.security-advisories.v1\n` | el feed de security advisories | NUEVO: feed firmado de advisories OSV |

`core/license` (payload JSON puro que empieza por `{`) y los dominios de eventos/checkpoints
de auditoría (`olivares.audit.*`) permanecen demostrablemente disjuntos de cada tag (un tag
nunca empieza por `{` y los dominios de auditoría son preimágenes prefijadas por longitud,
no bundles tar). `core/dr/bundle.go` se deja **intencionadamente como está**: es un snapshot
DR *sellado* (AES-GCM) y sin firmar, con un modelo de confianza distinto
(confidencialidad, no autenticidad del editor); incorporarlo mezclaría ambos.

### Consecuencias

- **Bueno:** fail-safe en la dirección correcta en ambos planos; un sobre auditado y una
  disciplina de separación de dominios en lugar de tres; el edge sigue denegando lo que
  siempre se denegó incluso tras una interrupción larga; la pérdida de evidencias es
  imposible por defecto y evidente ante manipulaciones cuando se permite explícitamente.
- **Malo / compromisos:** los grants positivos dejan de funcionar después de
  `policy_max_staleness` durante una interrupción realmente larga (mitigado mediante
  break-glass y haciendo del TTL una elección del operador); el modo `degrade` intercambia
  evidencias por disponibilidad y debe activarse conscientemente; refactorizar
  `core/release` toca el código recién integrado del updater OTA (mitigado mediante la
  prueba golden de identidad de bytes).
- **Neutral / seguimientos:** el feed de security advisories depende de `core/sigbundle` y
  de su propio tag; el verificador de archivos incorpora un vocabulario `declared-gap`;
  `docs/deploy/ddil.md` documenta los presupuestos de disco, el TTL y qué no sobrevive a una
  interrupción infinita.

## Por qué se rechazaron las alternativas

- **Q1-B (deny-closed total):** acaba con la misión. La caída de un enlace durante más que
  el TTL detendría una unidad de edge aunque sus reglas deny nunca estuvieran en duda.
- **Q1-C (no caducar nunca):** un grant revocado en el centro seguiría activo para siempre
  en el edge; una ventana de autorización ilimitada es inaceptable para un plano de
  gobernanza.
- **Q2-B (siempre fail-closed):** elimina un compromiso legítimo del operador (algunas
  misiones de edge no pueden detenerse); el marcador de hueco firmado ya hace honesta la
  degradación.
- **Q2-C (siempre degradar):** un valor predeterminado débil para un producto de gobernanza;
  perder evidencias silenciosamente por política es precisamente lo que el ledger pretende
  impedir.
- **Q3-B (copiar el patrón):** tres implementaciones del sobre y tres oportunidades de
  equivocarse con la separación de dominios; la enseñanza sobre reutilización de claves
  entre protocolos era precisamente que una clave sobre dos tipos de mensaje sin tag crea
  un vector de falsificación.

## Nota de implementación (2026-07-10)

Q2 está implementada según lo ratificado. El marcador de hueco declara el rango descartado
`{from_seq, to_seq, count, reason, at}` como un hueco de secuencia cuyo enlace hash se
mantiene continuo, y el verificador de cadena en vivo, el exportador de archivos y el
verificador de archivos offline reconocen un marcador correctamente declarado y firmado
como límite declarado (`declared_gaps` en sus informes), mientras siguen fallando ante
cualquier discontinuidad no declarada o incoherente. El presupuesto mide los bytes lógicos
exactos de los valores de eventos almacenados mediante un contador incremental que se
recalcula desde el ledger en cada arranque con presupuesto; la maquinaria de integridad
(checkpoints, anchors de archivo y el propio marcador) se admite por encima del presupuesto
pero se contabiliza por completo, y el plano del sistema se somete al presupuesto como
cualquier otro writer.

Una implementación paralela que mantenía la cadena sin huecos (un marcador de resumen sin
hueco de secuencia, medición física de páginas/relaciones y una exención para el plano del
sistema) se integró el mismo día y esta la sustituyó durante la reconciliación: el texto
ratificado especifica el rango declarado y la ampliación del verificador, y el contador
exacto elimina la histéresis de medición y los problemas de modificación de la migración v3
del enfoque físico. La variante sustituida permanece en el historial como referencia.
