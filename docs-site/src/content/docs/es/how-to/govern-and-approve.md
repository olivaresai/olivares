---
title: "Gobierna y aprueba (human-in-the-loop)"
description: "Cómo un operador gobierna el estate: identidad y permisos, el modelo RBAC deny-by-default, el seam de política solo-restringe, y la postura human-in-the-loop donde las decisiones se registran en el audit ledger."
---

Esta página es para el operador que ha conectado al menos una fuente y ahora necesita
**gobernar** el estate: decidir quién y qué puede actuar, revisar lo que la plataforma
muestra y actuar sobre ello. El gobierno vive en el **módulo VI (identidad, permisos,
gobierno)**, se asienta sobre el mismo núcleo de autorización que el resto de la API, y está
**totalmente auditado**.

:::caution[Alcance honesto: el motor de aprobación está construido; la consola del operador todavía está madurando]
Lo que se ejecuta hoy es el **núcleo de autorización** — RBAC deny-by-default, un seam de
política solo-restringe, acceso con alcance de tenant, y un audit ledger firmado y append-only que registra
cada decisión de gobierno y cada lectura privilegiada — **más un motor de aprobación human-in-the-loop
funcional**: solicitudes de aprobación gobernadas ligadas a un hash de plan, abiertas deny-closed y
con límite de tiempo, con **separación de funciones, decisor-duplicado y expiración aplicados del lado del servidor**,
y endpoints de aprobar/denegar bajo el namespace del módulo de gobierno. Lo que **todavía está
madurando** es la **superficie de revisión del operador** más rica — una consola completa de cola de aprobaciones y
una UI de revisión estructurada. Esta página describe el modelo, los endpoints en vivo y la
garantía de decisiones-registradas; donde la UI del operador sigue en fase de diseño, lo dice.
:::

## El modelo de autorización dentro del que gobiernas

Cada decisión de gobierno la toma el mismo núcleo de autorización que protege el
resto del control plane. Entiende sus tres propiedades antes de cambiar nada.

### RBAC es deny-by-default

La autorización corre **RBAC primero**. Un principal sin pertenencia en un tenant queda
**denegado** — no hay concesión implícita. Los permisos tienen alcance de tenant, y el
handler actúa solo sobre el **único tenant al que resolvió la solicitud**, nunca sobre uno que
re-deriva, lo que cierra por construcción las clases de confused-deputy e IDOR.

Los roles integrados forman una escalera de capacidad creciente:

| Rol | Qué puede hacer |
|---|---|
| `viewer` | leer datos operativos y el rastro de auditoría |
| `editor` | lo anterior, más escribir datos operativos |
| `admin` | lo anterior, más IAM del tenant — usuarios, pertenencias, tokens, ajustes |
| `owner` | todos los permisos dentro del tenant |

Un módulo declara sus propios permisos con namespace (`<namespace>:<resource>:<verb>`),
y a los roles se les conceden esos permisos **por nivel de verbo** (viewer mapea a lectura, editor
a escritura, admin y owner a admin). Un módulo nuevo, por tanto, introduce superficie de gobierno
sin una release del motor.

:::note[Ver el grafo de acceso es una acción privilegiada — por diseño]
El access map R/RW del módulo III es el activo más sensible del producto: un mapa
de lo que cada agente puede tocar es una hoja de ruta de reconocimiento para un atacante. Por eso **leer el
grafo de acceso es una acción privilegiada**, concedida desde el **rol editor en adelante — nunca
el viewer más bajo**. Tiene **alcance de tenant** (una lectura solo puede ver el grafo de un tenant),
y **cada lectura se escribe en el audit ledger** — quién miró el acceso de quién, y
cuándo. Privilegio, alcance de tenant y autoauditoría se superponen deliberadamente; véase el
[modelo de seguridad](/es/explanation/security/security-model/).
:::

### El seam de política (ABAC/PDP) solo restringe

Sobre RBAC, el operador puede cablear un **policy decision point (PDP)** externo para
reglas basadas en atributos. Eliges el motor con una única variable de entorno:

```bash
# Choose one. Cedar is the embedded, pure-Go primary; OPA is an over-HTTP adapter.
OLIVARES_PDP_ENGINE=cedar   # or: opa | none
```

Ambos motores se sientan tras un único seam, y el seam tiene un invariante que gobierna cómo
debes razonar sobre él:

:::tip[El PDP solo puede quitar acceso, nunca añadirlo]
El seam de política compone como **RBAC ∩ ABAC nativo ∩ PDP externo**, intersecados. Un PDP
**solo restringe; nunca amplía** lo que RBAC ya permitió. No puedes usar una política Cedar
u OPA para *conceder* acceso que el modelo de roles deniega — solo para denegar acceso que el
modelo de roles permitiría de otro modo. Esto se aplica, no es una convención.
:::

Los dos adaptadores preservan ese invariante de formas distintas, y escribes política
en consecuencia:

- **Cedar (embebido, primario, pure-Go).** Escribes reglas `forbid`. Una regla que coincide
  es una restricción; un conjunto de reglas vacío significa que la decisión de RBAC se mantiene. Un `permit` en Cedar
  nunca puede ampliar la decisión.
- **OPA (sobre HTTP).** Tu Rego debe ser **permit-by-default** (`default allow := true`,
  con cláusulas `allow := false` para tus denegaciones). Un resultado `true` significa sin restricción;
  `false`, un resultado ausente, o cualquier error de transporte o no-2xx **falla cerrado** — la
  solicitud se deniega.

Una **configuración de PDP inválida deshabilita solo el PDP externo** y registra el hecho —
ABAC nativo y RBAC siguen gobernando. Un motor de política mal configurado nunca deja
solicitudes sin gobernar y nunca tumba el control plane. **Cada restricción que el
PDP aplica se audita.**

## Lo que la plataforma muestra para que actúes

El gobierno human-in-the-loop se dirige por lo que la plataforma observa y presenta.
Dos streams le dicen a un operador qué requiere una decisión:

| Stream | Módulo | Qué muestra |
|---|---|---|
| **Least-privilege drift** | III (access map) | el diff **permitido-vs-observado** — una capacidad concedida usada de un modo que nadie pretendía, o una ruta alcanzable pero nunca ejercitada |
| **Hallazgos** | IX (seguridad, guardrails, forense) | hallazgos de guardrails y red-team, más el stream de notificaciones que la plataforma enruta |

El módulo III, el access map, es **read-first** — observa mediante logs,
OpenTelemetry y (como backstop de kernel no cooperativo) eBPF, y **nunca está en la
ruta de datos del agente**, así que un fallo del colector no puede romper producción. También es
**de datos mínimos**: almacena la relación `agent → resource (read/write)`, nunca
payloads, secretos ni PII. La señal que lleva es honesta sobre su propia confianza
(`attributed` vs `approximate`) y su propio alcance.

:::caution[La cobertura está por niveles — el drift no es uniformemente completo]
La fidelidad del access map depende del recurso. La cobertura está **por niveles**: *limpia* para
bases de datos SQL, object stores y warehouses (la auditoría nativa clasifica lectura vs escritura
literalmente); *con pérdidas* para stores como bases de datos documentales y vectoriales; e **imposible de
observar pasivamente** para stores en memoria y embebidos. Gobierna teniendo esto presente: una
ausencia de acceso observado no es prueba de ausencia de acceso donde la cobertura es con pérdidas o nula.
Lee [el modelo de amenazas](/es/explanation/security/threat-model/) para saber qué puede y
no puede atestar cada nivel.
:::

Una clase de señal necesita juicio de gobierno explícito. Las anotaciones de herramienta MCP
(`readOnlyHint` / `destructiveHint`) son una pista útil de lectura/escritura pero **no son fiables
según la especificación MCP** — los clientes deben tratarlas como no fiables. La plataforma
las **corrobora** contra señales fiables y nunca confía en ellas solas, y así
deberías hacer tú al actuar sobre un ítem de drift que se apoya solo en una anotación.

## La postura human-in-the-loop

El bucle de gobierno previsto es: **la plataforma presenta** (drift del módulo III, hallazgos
del módulo IX) → **un operador autorizado decide** → **la decisión se registra en
el audit ledger**.

Las tres partes de ese bucle se ejecutan hoy. **Las superficies son reales** — el módulo III produce
el diff permitido-vs-observado y el módulo IX produce hallazgos. **El motor de aprobación es
real** — una solicitud de aprobación gobernada se abre contra el módulo de gobierno (deny-closed,
ligada a hash de plan, con límite de tiempo); un operador autorizado aprueba o rechaza vía el endpoint
de decisión, y **separación de funciones, decisor-duplicado y expiración se aplican
del lado del servidor** para que el solicitante nunca pueda decidir su propia solicitud y una expirada
nunca pueda ligar. Y **el registro es real y fuerte** — véase la garantía de abajo. Lo que
**sigue en fase de diseño** es la **consola de revisión del operador** desarrollada — una UI rica de
cola de aprobaciones; los endpoints y el motor están entregados, la superficie de revisión pulida
es el camino a seguir para el módulo VI.

La dependencia que hace creíble este bucle es la **identidad por agente**. La auditoría de la plataforma
atribuye la actividad a una credencial o rol, no inherentemente a un agente; una cuenta de
servicio compartida con un pool de conexiones colapsa la atribución. Gobernar bien, por tanto,
significa **emitir y aplicar identidad por agente** — el puente desde la observación
(módulo III) hasta el gobierno (módulo VI). El lado de identidad de esto se construye en torno a
credenciales opacas, revocables y de primera parte y un roster de identidades no humanas; la
**única primitiva de acuñación de credenciales** del producto es opt-in, atestada, auditada, y
nunca persiste el token acuñado. Véase el
[catálogo de módulos](/es/reference/modules/overview/) para saber cómo identidad, permisos y
gobierno componen a lo largo del estate.

:::tip[La garantía de decisiones-registradas]
Sea cual sea la profundidad del flujo de trabajo por encima, **una decisión de gobierno es un hecho
registrado**. Las acciones mutantes se añaden al audit ledger con el **actor real** en
la **misma transacción** que el cambio, y las lecturas sensibles (el grafo de acceso, el
propio ledger) se autoauditan en una escritura confirmada. El ledger es **append-only,
hash-chained, y protegido por firmas Ed25519** — cada registro lleva
`seq`, `prev_hash`, `hash` y `sig`, de modo que reescribir la historia es criptográficamente
detectable, y **nunca contiene PII**. No puedes hacer un cambio sin gobernar que
el ledger olvide en silencio.
:::

### Saca el registro de serie

Para una copia externa e inmutable — lo que un auditor de empresa pide y que la telemetría
nativa no proporciona — el ledger se expone como un **pull export autenticado**:

```bash
# Pull the signed, hash-chained ledger for offline re-verification.
# Requires a token whose role can read the audit trail (viewer and up).
curl -fsS "https://localhost:8443/v1/audit/export?format=cef" \
  -H "Authorization: Bearer $OLVK_TOKEN" \
  -H "X-Olivares-Tenant: $TENANT" >> /var/log/olivares/audit.cef
```

Los valores de `format` soportados son `cef`, `leef`, `syslog`, `otlp`, `otlp_envelope`, `otlp_log_record` y `ocsf` —
`otlp` emite el request de exportación completo y posteable, `otlp_envelope` es un alias exacto de este, y
`otlp_log_record` es la proyección simple con un LogRecord por línea. Cada registro
lleva los campos de integridad de cadena para que tu SIEM o store WORM pueda **re-verificar la cadena
offline**. La firma desacoplada defiende contra un compromiso solo-de-BD (inyección, una
copia de seguridad o réplica robada, un rol que evade RLS) y contra la eliminación de checkpoints; una
**copia fuera de la máquina** es el control contra un host totalmente comprometido. Véase
[reenviar la auditoría a Splunk](/es/how-to/forward-audit-to-splunk/) para una tubería
completa de file-tail.

El least-privilege drift sobre el que actúan estas decisiones es el resultado permitido-vs-observado
del access map. El [tutorial cero-a-grafo](/es/tutorials/zero-to-graph/)
recorre cómo alcanzarlo concretamente sobre el estate de demo; la superficie del módulo de access-map
está sujeta al mismo RBAC deny-by-default, alcance de tenant y auditoría por lectura que
todo lo demás, razón por la que leerlo es una acción de editor-en-adelante.

## A dónde ir después

- [Modelo de seguridad](/es/explanation/security/security-model/) — privilegio, alcance de tenant,
  autoauditoría, y la postura de datos mínimos en detalle.
- [Modelo de amenazas](/es/explanation/security/threat-model/) — los activos, las fronteras de confianza,
  y qué puede atestar cada nivel de cobertura.
- [Catálogo de módulos](/es/reference/modules/overview/) — cómo identidad, permisos y
  gobierno (módulo VI) componen con el access map (módulo III) y los hallazgos
  (módulo IX).
- [Conecta una fuente](/es/how-to/connect-a-source/) — cablea las señales sobre las que se construyen el drift y
  los hallazgos.
