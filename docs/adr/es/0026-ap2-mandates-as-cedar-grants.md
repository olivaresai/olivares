> Traducción automática. La versión en inglés es la fuente autoritativa.

# ADR-0026: Los mandatos de pago AP2 como grants Cedar con ámbito (aprovisionamiento gobernado)

- **Status:** proposed (design only; the enterprise build lands in a separate phase)
- **Date:** 2026-07-20
- **Deciders:** Fran Olivares
- **References:** ADR-0019 (Cedar scoped grants), ADR-0022 (source-scoping subject axes),
  ADR-0025 (FinOps reserve→commit/release ledger, TOCTOU-safe), ADR-0009 (append-only
  hash-chained audit); the companion AP2 governed-payment threat-model spec; the AP2 v0.2.0
  specification (github.com/google-agentic-commerce/AP2, verified 2026-07-20).

## Contexto y planteamiento del problema

Los pagos agénticos están llegando como una capa de protocolo. **AP2 (Agent Payments
Protocol)**, de Google, es uno de los más visibles; su especificación actual es **v0.2.0
(publicada el 2026-04-28)** y ese mismo día se donó a la FIDO Alliance. AP2 permite que un
usuario delegue un **mandato** firmado en un agente de compras, que este vincula después a una
transacción concreta que comprueban los **Verificadores** (comerciante, proveedor de
credenciales, red, procesador de pagos).

Dos hechos fijan la forma de esta decisión:

1. **Actualidad (la realidad medida gana al plan).** La planificación anterior se basaba en
   AP2 v0.1 y describía un triplete de mandatos *Intent / Cart / Payment* firmado mediante
   «credenciales verificables». Ese modelo está **superado**. v0.2 define exactamente **dos**
   tipos de mandato —**Checkout Mandate** y **Payment Mandate**—, cada uno en un estado
   **Open** (portador de restricciones, firmado por el usuario) y un estado **Closed**
   (vinculado a la transacción; el agente genera un Key Binding JWT / Proof-of-Possession
   sobre la clave del claim `cnf` del mandato abierto). Los mandatos son **SD-JWT** (RFC
   9901); el **hash de vinculación / Key Binding JWT DEBE usar un esquema no determinista
   (ES256/ECDSA) y NO uno determinista (Ed25519)**: la especificación afirma que esto protege
   la vinculación por hash. Este ADR se dirige a **v0.2**, fijado a los sufijos de esquema
   `vct` publicados (según la especificación v0.2, `mandate.checkout.1` /
   `mandate.payment.1`; verificar contra `docs/ap2/*` de la especificación en tiempo de
   compilación).

2. **Qué es Olivares y qué no es.** Olivares es un **plano de control de gobernanza**: un
   Policy Decision Point (PDP) y un ledger de evidencias evidente ante manipulaciones. **No**
   es un procesador de pagos, ni un PSP, ni una red de tarjetas, ni un wallet, ni un custodio
   de fondos, y este ADR no lo convierte en ninguna de esas cosas. El propio AP2 es
   **pre-1.0**, con una **adopción temprana y en gran medida aspiracional** (las páginas del
   propio PayPal mencionan AP2 solo de forma taxonómica y destacan ACP de OpenAI + UCP de
   Google; «Agent Pay» de Mastercard es un programa distinto; la cifra de «60+
   organizaciones» es un recuento del lanzamiento de septiembre de 2025; la lista de firmantes
   de FIDO es de ~12). El etiquetado honesto prohíbe afirmar soporte de AP2 más allá de lo
   verificable.

El problema: **¿cómo gobierna Olivares una compra agéntica mediada por AP2 usando las
primitivas que ya tiene, nacida con un caso de uso enterprise concreto y cubriendo las lagunas
que AP2 deja deliberadamente a la capa superior, sin introducir un fall-through de
autorización ni una degradación silenciosa de las restricciones?**

El caso de uso concreto con el que nace este diseño: un **agente de aprovisionamiento
gobernado**; una empresa compra mediante un agente que opera bajo un mandato abierto AP2 cuyas
restricciones codifican la política de compras (límite de presupuesto, proveedores permitidos,
límites por artículo, recurrencia, ventana de ejecución); Olivares autoriza cada compra
concreta frente a esa política, escala las de alto valor a una persona y sella el
mandato+recibo como evidencia no repudiable.

**Precondición (gate in-path).** Todas las garantías siguientes se sostienen únicamente allí
donde el despliegue encamina la compra **a través de Olivares como gate in-path**: el agente
DEBE obtener una autorización nueva de Olivares antes de presentar un mandato cerrado a la capa
de liquidación. Como PDP lateral/consultivo, Olivares no puede alcanzar un mandato cerrado ya
entregado a un comerciante, igual que tampoco puede AP2. La implementación DEBE documentar este
requisito de despliegue.

## Motivadores de la decisión

- **Reutilizar el plano de autorización existente, no bifurcarlo**, pero solo allí donde la
  semántica encaje de verdad (véase más abajo la corrección Abstain frente a deny).
- **Cubrir en nuestra capa las lagunas declaradas de AP2** (véase la especificación
  complementaria del modelo de amenazas): AP2 **no tiene revocación**, convierte el rechazo del
  doble gasto en el lado del verificador en algo **opcional (MAY)**, **no** demuestra la
  identidad humana / SCA, **guarda silencio sobre la confianza en el reloj** y deja fuera de
  ámbito la retención/recuperación de evidencias y la responsabilidad. Un PDP que «asume que
  todos los agentes son atacantes potenciales» (el propio modelo de amenazas de AP2) debe
  hacerlas obligatorias.
- **Fail-closed ante cualquier cosa no modelable.** Una restricción que no podamos codificar,
  una divulgación que el agente retenga, un algoritmo desconocido: cada una debe rechazar el
  mandato, nunca ampliarlo.
- **Ámbito honesto y riesgo pre-1.0.** Diseñar ahora, fijar a `vct`, no publicar afirmaciones
  que no podamos verificar y mantener Olivares estrictamente en el lado PDP/evidencias de la
  línea.

## Opciones consideradas

- **Opción A — Los mandatos AP2 como grants Cedar con ámbito; Olivares como el Verificador/PDP
  gobernante.** Modelar un **mandato abierto** AP2 como un **grant Cedar** redactado
  (ADR-0019) vinculado a ese único mandato, cuyas condiciones `when` son las restricciones del
  mandato; tratar un **mandato cerrado** como una **petición de autorización** (principal = la
  clave del agente en `cnf`; action = `purchase`/`pay`; resource = el beneficiario / el
  checkout) evaluada con **denegación por defecto para las acciones de pago**. Olivares
  ejecuta como PDP las reglas de verificación de AP2, somete las de alto valor a la aprobación
  HITL de un solo uso, reserva los presupuestos de FinOps (ADR-0025) con fail-closed y sella
  el mandato+recibo firmado completo como evidencia.
- **Opción B — un motor de mandatos AP2 a medida, paralelo a Cedar.**
- **Opción C — solo observar.**

## Resultado de la decisión

Opción elegida: **Opción A**, porque el modelo de restricciones se proyecta sobre las
condiciones de los grants Cedar y los controles circundantes (aprobaciones, ledger de
reservas, cadena de auditoría firmada) ya existen, **siempre que se apliquen las tres
correcciones semánticas siguientes**, sin las cuales la reutilización no es segura.

### Las tres correcciones semánticas que hacen sólida la reutilización

1. **Las acciones de pago se DENIEGAN POR DEFECTO; no son «abstain difiere al RBAC».** El
   motor de grants con ámbito devuelve **`EffectAbstain`** (no deny) cuando ningún permit
   coincide: «sin grant», «grant caducado» y «sin grants con ámbito para el tenant» se
   abstienen todos, y Abstain significa que *prevalece la decisión RBAC base*
   (`modules/governance/grants.go:31-38`, la invariante de retrocompatibilidad del RBAC).
   Equiparar ingenuamente «ningún mandato coincidente» con «deny» es **erróneo**: un desajuste
   de cnf, un mandato caducado o un grant revocado se abstendrían y podrían acabar cayendo en
   un **allow del RBAC**. Corrección: `purchase`/`pay` se autorizan **únicamente** mediante un
   grant coincidente, válido y vinculado a un mandato, **sin recurso al RBAC**. La
   implementación DEBE aplicarlo mediante (i) demostrar que el autorizador base no concede
   ningún permit de `purchase`/`pay` a ningún rol (de modo que Abstain→deny) o (ii) una capa
   de pagos que trate Abstain en una acción de pago como deny. Un mandato presente pero
   inválido redacta además un **`forbid`** explícito. Una prueba de conformidad DEBE afirmar
   que el RBAC por sí solo nunca autoriza un pago.

2. **El traductor mandato→grant FALLA CERRADO ante cualquier restricción no modelable.** «Una
   restricción desconocida DEBE fallar» es una obligación de **tiempo de traducción**, no algo
   que aporte la denegación por defecto de Cedar: si el traductor omite en silencio una
   restricción que no puede codificar, produce un grant **más amplio que el que el usuario
   firmó** y Cedar permite porque nunca vio la restricción. Corrección: traducir contra una
   **allowlist** de claves, operadores y unidades de restricción reconocidos; ante cualquier
   elemento no reconocido, **rechazar el mandato entero y no redactar ningún grant**.

3. **La divulgación completa es obligatoria; el agente no confiable no puede retener una
   restricción.** En SD-JWT es el *holder* (el agente no confiable) quien elige qué
   divulgaciones revelar. Podría presentar solo las divulgaciones que pasan y retener una
   restricción más estricta. Corrección: el adaptador de verificación enumera los digests
   `_sd` y, si algún digest de un claim relevante para la política está **sin divulgar**, lo
   trata como una restricción no evaluable y **falla cerrado**.

### Correspondencia (con las correcciones aplicadas)

| Concepto de AP2 v0.2 | Primitiva de Olivares (file:line) |
|---|---|
| Mandato abierto (restricciones, firmado por el usuario) | **Grant** Cedar con ámbito vinculado al `jti`/`sd_hash` de ese mandato (`modules/governance/grants.go:67`, ADR-0019) |
| Mandato cerrado | **Petición** de autorización, evaluada con **denegación por defecto para `purchase`/`pay`** (corrección 1) |
| «Verification and Processing Rules» | Verificación de la cadena en el adaptador + comprobación de divulgación completa (corrección 3) + traducción fail-closed (corrección 2) + decisión del PDP |
| `payment.budget` (acumulativo) / `amount_range` (por transacción) | Ledger de reservas de FinOps (`modules/finops/budgets.go`, `spendlimits.go`, ADR-0025) con una **clave de reserva por mandato totalmente nueva**; reservar frente al tope del mandato Y a todos los ámbitos de Olivares de forma atómica (NO `min()`) |
| `payment.agent_recurrence` (recuento/velocidad) | Limitador de recuento/velocidad **totalmente nuevo** (seguro ante TOCTOU conforme a ADR-0025) — NO un presupuesto existente basado en importes |
| `allowed_payees` / `allowed_merchants` / `allowed_payment_instruments` | Condiciones `when` de pertenencia a conjunto en Cedar |
| `execution_date` {not_before,not_after} | Condición temporal frente al **reloj dead-man firmado y de confianza de DDIL** (`modules/governance/ddiladopt.go`), inyectado también en el adaptador SD-JWT |
| Aprobación del usuario; gate para alto valor | Consumo de aprobación **HITL de un solo uso** (`modules/governance/approvals.go`) |
| Checkout/Payment Mandate + Receipt (evidencia de disputa) | **Ledger de auditoría del runtime** encadenado por hash y con clave `transaction_id` (`modules/sessions/runtime_ledger.go`, `sc.Audit().Append`, ADR-0009) — véase la decisión 1 sobre QUÉ se almacena |

### Las decisiones que toma este ADR

1. **Representación del mandato: autoridad y evidencia son stores distintos.**
   - La **autoridad** es el **grant Cedar** (la política evaluada), vinculado al id estable del
     mandato abierto concreto (`jti`/`sd_hash`), de modo que un mandato cerrado solo puede
     evaluarse frente al grant redactado a partir de *su* mandato abierto (evita la
     **sustitución de mandatos**: un agente que posee un mandato laxo A no puede conseguir que
     un mandato cerrado de B se evalúe frente al grant de A). El grant **nunca** es el blob en
     bruto tratado como autoridad autoafirmada.
   - La **evidencia** es el **artefacto firmado completo**: el SD-JWT abierto, el Key Binding
     JWT cerrado y las **divulgaciones realmente presentadas**, conservados (cifrados y con
     control de acceso) para que una disputa pueda *reproducir la secuencia de verificación de
     firmas de AP2*, algo que un hash no permite. Esta evidencia contiene PII (importes,
     beneficiarios), por lo que es **evidencia cifrada mínima necesaria, no «nunca PII»**: la
     regla de datos mínimos se aplica a la *autoridad/grant* y a los logs operativos, no al
     registro sellado de la disputa.

2. **Verificación de firmas: en cadena, con algoritmos fijados y raíces de confianza
   separadas.** Verificar la cadena SD-JWT y el enlace abierto→cerrado mediante el Key Binding
   JWT vinculado a `cnf` (PoP), confirmar que el mandato cerrado conserva sin cambios los
   claims del mandato abierto y evaluar todas las restricciones (correcciones 2 y 3). Dos
   reglas de endurecimiento que la especificación en bruto no aporta:
   - **Fijación de algoritmos.** Vincular cada clave de raíz de confianza a su conjunto de
     algoritmos permitidos y verificar estrictamente contra él; **ignorar el `alg` anunciado
     por el token**. Rechazar `alg:none`, la confusión HS/ES y la degradación de
     curva/robustez: la prohibición de Ed25519 de AP2 es una regla estrecha dentro de una
     superficie de negociación controlada por la cabecera que dirige el agente no confiable.
   - **Raíces de confianza separadas.** La raíz **User-Credential** (OpenID4VP) verifica que
     la *persona autorizó* el mandato abierto; la lista **Trusted-Agent-Provider** rige
     únicamente qué identidad de agente puede **poseer/vincular** la clave `cnf`. Acreditan
     hechos distintos y **ambas son obligatorias, cada una por su propia obligación**: nunca un
     OR intercambiable (una atestación del proveedor del agente no sustituye a la firma de
     autorización del usuario). Deny-closed si falta la raíz requerida.

3. **Caducidad, uso único y revocación (limitado a los flujos con gate de Olivares).** AP2 **no
   tiene revocación**. Olivares lo cierra para los despliegues **in-path**: (a) el grant
   vinculado al mandato es **revocable de primera clase**: revocarlo hace que toda *futura
   autorización de Olivares* para ese mandato se deniegue por defecto (corrección 1); no puede
   alcanzar un mandato cerrado ya entregado a la liquidación (el mismo límite que AP2,
   declarado con honestidad). (b) Un mandato cerrado de alto valor consume una **aprobación de
   un solo uso**, de modo que una aprobación no se puede reproducir. (c) `exp`,
   `execution_date` y la recurrencia se aplican frente al **reloj firmado de confianza de
   DDIL**, y el adaptador SD-JWT toma su `now` de ese mismo reloj para que las dos capas no
   puedan discrepar.

4. **Replay / doble gasto: la deduplicación en el lado del verificador es OBLIGATORIA
   (in-path).** AP2 sitúa el MUST antidoble-gasto sobre el *agente de compras* (un atacante en
   su propio modelo de amenazas) y deja la comprobación del verificador en un simple MAY. El
   PDP de Olivares registra los nonces / `transaction_id` de mandato cerrado presentados por
   cada mandato abierto y rechaza las presentaciones solapadas o repetidas, para las
   autorizaciones que se encaminan a través de Olivares (la precondición in-path).

5. **Lo que Olivares NO hace.** Sin custodia de fondos, sin ejecución de pagos, sin emisión de
   tarjetas/tokens, sin actuar como PSP/red/wallet. Olivares es el **PDP** que autoriza la
   compra agéntica frente a la política y el **plano de evidencias** que sella el
   mandato/recibo. La liquidación queda en manos del comerciante/PSP/red.

### Consecuencias

- **Bueno:** reutiliza Cedar/ledger de reservas/aprobaciones/cadena de auditoría allí donde la
  semántica encaja de verdad; las lagunas de AP2 se convierten en garantías aplicadas;
  evidencia sellada y no repudiable; posicionamiento honesto y verificable.
- **Malo / compromisos:** la reutilización es **condicional**: necesita una capa de denegación
  por defecto para las acciones de pago, un traductor fail-closed, el enforcement de la
  divulgación completa, una clave de reserva por mandato y un limitador de recurrencia
  totalmente nuevo (nada de eso sale gratis); AP2 es pre-1.0 (una v0.3 forzará un remapeo,
  aislado tras el adaptador y fijado a `vct`); conservar evidencia firmada con PII añade una
  obligación de cifrado/retención.
- **Neutral / seguimientos:** la delegación de mandatos entre agentes queda **fuera del ámbito
  de AP2** → fuera del nuestro; x402 (extensión de AP2 para raíles cripto) y ACP
  (OpenAI/Stripe) son independientes y se siguen, pero no se construyen aquí.

## Por qué se rechazaron las alternativas

- **Opción B (motor a medida)**: rechazada; duplica la maquinaria de ledger de
  reservas/aprobaciones/auditoría para un protocolo pre-1.0; las correcciones anteriores
  muestran que la reutilización es sólida una vez implantadas la denegación por defecto para
  las acciones de pago y la traducción fail-closed.
- **Opción C (solo observar)**: rechazada; la dirección ratificada es diseñar ahora y empezar
  pronto la implementación enterprise *sin bloquear la publicación pública*. Solo observar
  renunciaría al diferenciador (gasto agéntico gobernado con evidencia sellada) mientras el
  estándar se consolida en FIDO. La preocupación por el etiquetado honesto se atiende
  entregando el **diseño** ahora y condicionando la **implementación** a una necesidad
  verificada, no mediante la inacción.
