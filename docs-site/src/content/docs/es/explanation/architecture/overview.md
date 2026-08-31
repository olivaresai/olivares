---
title: "Visión general de la arquitectura"
description: "Cómo está construido Olivares AI: un motor, módulos y conectores — el modelo de plataforma, los ocho subsistemas del núcleo, el access map y las topologías de despliegue."
---

Esta página explica cómo está estructurado Olivares AI y por qué. Es una *explicación*, no una guía paso a paso: te da el modelo mental que necesitas para razonar sobre el control plane antes de instalarlo, configurarlo o extenderlo. Para instrucciones paso a paso, sigue las [guías how-to](/es/how-to/self-hosting/); para los contratos exactos, consulta la [referencia de la API](/reference/api/) y la [referencia de eventos](/es/reference/events/).

:::note[Etapa de diseño]
Buena parte de lo que sigue describe un sistema que está **en beta** y en partes en etapa de diseño. El modelo de plataforma, el modelo de datos, la ruta cooperativa de ingesta y el diferenciador del access map están especificados y se construyen de forma incremental; algunas capacidades a nivel de módulo están planificadas más que entregadas. Cuando una capacidad aún no está construida, esta página lo indica. Trátalo como la arquitectura prevista, no como una afirmación de que cada capa está hoy completa para producción.
:::

<img class="light:sl-hidden" src="/diagrams/02-architecture-dark.svg" alt="Arquitectura: las superficies de agente, las fuentes de auditoría, los pares MCP y A2A y las fuentes de contenido se recogen de tres formas hacia un único binario Go autoalojado con la consola empotrada, que lleva los módulos del producto, la capa de política y aplicación y el libro de evidencia firmado sobre un almacén con alcance por tenant; sirve la consola, la API REST, un subconjunto gRPC acotado, la CLI y el proveedor de Terraform, con el plano de control cloud (construido, sin desplegar) y el portal de licencias (desplegado, con la entrega apagada) como planos aparte." />
<img class="dark:sl-hidden" src="/diagrams/02-architecture-light.svg" alt="Arquitectura: las superficies de agente, las fuentes de auditoría, los pares MCP y A2A y las fuentes de contenido se recogen de tres formas hacia un único binario Go autoalojado con la consola empotrada, que lleva los módulos del producto, la capa de política y aplicación y el libro de evidencia firmado sobre un almacén con alcance por tenant; sirve la consola, la API REST, un subconjunto gRPC acotado, la CLI y el proveedor de Terraform, con el plano de control cloud (construido, sin desplegar) y el portal de licencias (desplegado, con la entrega apagada) como planos aparte." />

## El modelo de plataforma: un motor, módulos, conectores

Olivares AI no es una herramienta de un solo propósito. Es una **plataforma modular** en la línea de Grafana, Backstage y el control plane de Kubernetes: **un motor (núcleo) más módulos más conectores**. El producto abarca un catálogo de módulos — inventario, sesiones, el access map, gobernanza, FinOps, evaluaciones, guardrails y más — pero todos se asientan sobre un único motor compartido.

La restricción que gobierna la arquitectura es la **regla de "no re-arquitectura"**: el motor está diseñado de modo que *cualquier* módulo del catálogo pueda añadirse sin tocar el núcleo ni los demás módulos. En concreto, cada módulo nuevo:

1. **Consume** eventos y datos normalizados del motor;
2. **Declara** sus propias entidades en el modelo de datos compartido;
3. **Expone** sus propios endpoints de API y vistas de UI.

Ningún módulo accede a las interioridades de otro, y ninguno remodela el núcleo para encajar. El motor paga por adelantado el coste de ser multitenant, dirigido por eventos y API-first desde el primer día precisamente para que la amplitud pueda añadirse después sin un rediseño. El mismo principio explica el orden de construcción — primero el motor CLI, la web encima: la CLI *es* el motor y expone toda la funcionalidad por CLI y API; la web es una capa de presentación sobre la **misma API**, sin lógica duplicada. Construir el motor y luego la cara visual encima no es una re-arquitectura.

La capacidad diferenciadora — el read/write access map con el diff de permitido-frente-a-observado — es en sí misma **un módulo** (módulo III) sobre el modelo compartido, no una tubería a medida. Eso es lo que mantiene honesta a la plataforma: la funcionalidad estrella obedece las mismas reglas que todo lo demás.

## Los ocho subsistemas del motor

El motor (el núcleo, "Capa 0") es el conjunto de subsistemas compartidos de los que cuelga todo lo demás. Hay ocho.

| Subsistema | Qué hace | Por qué vive en el núcleo |
|---|---|---|
| **Ingesta + bus de eventos** | Recibe entrada OTLP y de conectores, la normaliza y distribuye eventos a los módulos | Los módulos reaccionan a los eventos sin acoplarse entre sí |
| **SDK de conectores** | Una interfaz estable de conector de entrada/salida — la columna vertebral de la amplitud | Terceros extienden la plataforma sin bifurcar el núcleo |
| **Runtime de módulos** | Carga y ejecuta módulos: compilados in-process más plugins out-of-process | Añade un módulo sin re-arquitecturar ni recompilar el núcleo |
| **Modelo de datos general** | Entidades y relaciones multitenant que sirven a todo el catálogo | Un esquema que todos los módulos comparten y extienden |
| **API (REST/gRPC) + manage-as-code** | Toda la funcionalidad sobre una API, más un proveedor de Terraform | La CLI y la web hablan la misma API; el panel es GitOps-able |
| **AuthN/Z + multitenancy** | RBAC/ABAC, orgs y tenants, aislamiento | Reajustar permisos y tenancy a posteriori es ruinosamente caro — así que, desde el primer día |
| **Auditoría + integridad** | Audit ledger append-only y hash-chained | La evidencia de manipulación es transversal, nunca opcional |
| **Licencia / entitlement** | Validación de licencia Ed25519 offline | Comercial self-serve, funciona air-gapped |

Algunos detalles concretos que merece la pena destacar:

- **Runtime de módulos.** Los módulos del núcleo se compilan dentro del binario; los módulos y conectores out-of-process corren como plugins sobre gRPC usando `hashicorp/go-plugin`. Esto da aislamiento de fallos y permite añadir un módulo sin recompilar el núcleo.
- **Bus de eventos.** In-process por defecto (canales de Go). El binding distribuido sobre **NATS es opcional**, no obligatorio — los despliegues de un solo nodo nunca lo tocan.
- **Manage-as-code.** La API es el contrato de referencia; la superficie de manage-as-code añade un proveedor de Terraform para que el propio control plane pueda declararse y versionarse.
- **Auditoría + integridad.** El ledger es **append-only y hash-chained**, con **checkpoints firmados con Ed25519**. Las entradas llevan un número de secuencia, el hash anterior, el hash actual y una firma — y nunca llevan PII. El ledger sale de la caja por dos vías: un endpoint de exportación **pull** emite CEF, LEEF, syslog, OTLP (un request de exportación completo y posteable; `otlp_envelope` es un alias exacto, y la proyección simple de LogRecord es el token separado `otlp_log_record`) u OCSF, y un **push** — real en cuanto se configura una suscripción de eventing `audit.recorded` — entrega cada registro sellado al menos una vez por el transporte duradero. Consulta [cómo reenviar la auditoría a Splunk](/es/how-to/forward-audit-to-splunk/).
- **Licencia.** La validación es **offline**, usando Ed25519, y el motor no hace ninguna llamada de licencia — que es lo que hace viable la operación air-gapped. El único comando que sale a la red es `olivares upgrade`: por defecto descarga de las releases de GitHub del repositorio público, o del worker de licencias (`licenses.olivares.ai`) con `--enterprise` — salvo que `--endpoint` lo apunte a tu propio espejo o `--bundle` instale desde un paquete que llevas contigo.

Para los detalles de autenticación y autorización (tokens bearer opacos, token de configuración de primer arranque, el policy decision point) consulta el [modelo de seguridad](/es/explanation/security/security-model/); aquí solo se resumen donde la arquitectura depende de ellos.

### El modelo de datos general

Un único esquema multitenant sirve a todo el catálogo. Cada entidad del núcleo lleva un `tenant_id`, y el aislamiento se aplica a nivel de consulta / fila. Las entidades del núcleo cubren orgs y tenants, agentes, sesiones, modelos y proveedores, servidores MCP, skills y herramientas, recursos (bases de datos, servidores, stores, APIs), identidades, políticas, registros de coste, resultados de evaluación, findings, eventos de auditoría, estado de salud y despliegues — y, de forma central, el **`AccessEdge`**.

Cada módulo registra sus propias entidades y relaciones a través de un type registry y tablas por módulo, sin romper el núcleo. Este es el mecanismo detrás de la regla de "no re-arquitectura" en la capa de datos.

El store empieza como **SQLite** (el driver pure-Go `modernc`, para que el binario no necesite CGO y corra air-gapped) en despliegues de un solo nodo, y pasa a **Postgres con row-level security** para multitenancy y escala.

## Módulo III: el access map como vista sobre el modelo

El módulo estrella es el **read/write access map** y su **diff de permitido-frente-a-observado** — least-privilege drift. El punto arquitectónico crítico es que esto es **una vista sobre el modelo de datos general, no un esquema aparte**. El map se materializa a partir de entidades `AccessEdge`, y el propio `AccessEdge` **lleva tanto el lado permitido como el lado observado**, junto con la fuente de la señal y un nivel de confianza. El diff es por tanto una consulta sobre el mismo modelo multitenant que usa cualquier otro módulo.

### Read-first y minimal-data

El map es **read-first**: observa desde logs, OpenTelemetry y (como respaldo) eBPF — nunca está en la ruta de datos de las llamadas del agente. También es **minimal-data**: almacena la *relación* (un agente lee/escribe un recurso), nunca payloads, secretos ni PII. La asimetría es deliberada — señal alta, riesgo bajo.

### La ruta cooperativa cruzada con la auditoría nativa del store

La fidelidad viene de cruzar dos tipos independientes de evidencia:

- **La ruta cooperativa** — Claude Code y los agentes emiten telemetría sobre **OpenTelemetry (OTLP)**, complementada con **introspección MCP** de las herramientas y recursos que expone un servidor. El receptor OTLP forma parte de la ingesta del núcleo y escucha en loopback por defecto. Consulta [conectar Claude Code](/es/how-to/connect-claude-code/).
- **Auditoría nativa del store** — el store te dice qué pasó realmente. **pgAudit clasifica `READ` frente a `WRITE`** literalmente en Postgres; **CloudTrail expone `readOnly`** para S3; existe auditoría nativa equivalente para otros motores.

Cuando la ruta cooperativa y la propia auditoría del store coinciden en un edge, tienes una relación de lectura/escritura corroborada.

### El respaldo eBPF, las anotaciones no fiables y la cobertura por niveles

Tres propiedades adicionales hacen que el map sea fiable en lugar de ingenuo:

- **eBPF / Tetragon es el respaldo no cooperativo.** Para rutas que no cooperan, un observador a nivel de kernel proporciona la verdad de campo sobre la intención de lectura/escritura a nivel de proceso y host. Corre fuera del control del agente (anti-evasión) pero es ciego a los payloads TLS — lo cual está bien, porque el map solo necesita la *relación*, no el contenido.
- **Las anotaciones MCP no son fiables.** Las pistas de read-only / destructive de MCP son una señal útil, pero la propia especificación MCP indica que los clientes deben tratarlas como no fiables. Por tanto, el map las **corrobora** contra otras fuentes y **nunca confía en una anotación por sí sola**.
- **La cobertura es por niveles, y el producto lo dice.** Algunos stores son **limpios** de observar pasivamente (bases de datos SQL, object stores, warehouses); algunos son **con pérdidas** (Mongo, bases de datos vectoriales); y algunos son **imposibles de observar pasivamente** (Redis, SQLite, D1). El map muestra niveles de confianza (atribuido frente a aproximado) en lugar de fingir una precisión que no tiene.

:::caution[Una dependencia dura: identidad por agente]
La auditoría nativa atribuye la actividad a una credencial o rol, no a un agente. Una cuenta de servicio compartida más un pool de conexiones colapsa la atribución — ya no puedes saber qué agente hizo qué. Resolver esto requiere emitir o forzar **identidad por agente**, que es el puente del access map al módulo de gobernanza. Esto está en etapa de diseño, y una prueba de concepto sobre la ruta cooperativa (Claude Code OTEL + MCP hacia Postgres pgAudit) es la puerta decisiva antes de desarrollar el módulo.
:::

### Llegar al map

Ver el grafo de accesos es una **acción privilegiada**: con alcance de tenant, disponible para el rol de editor y superiores (nunca el rol de viewer más bajo), y **cada lectura se audita**. Las rutas del map — el grafo y el resultado del drift — no forman parte del contrato estable del núcleo; se publican en la [referencia de rutas de módulos](/reference/api-beta/) **beta** separada (servida en `/openapi.beta.json`), y sus formas a nivel de campo viven en interfaces tipadas de Go y TypeScript. El resultado de permitido-frente-a-observado se expone en la ruta `drift` del motor (`/v1/m/accessmap/drift`); no hay un endpoint `diff` separado. La superficie REST estable del núcleo — 53 paths renderizados a partir del propio contrato OpenAPI 3.1 del producto — está documentada en la [referencia de la API](/reference/api/). Para la lista completa de módulos, consulta el [catálogo de módulos](/es/reference/modules/overview/).

## Topología de despliegue

El mismo binario soporta varias topologías. Una restricción se mantiene en todas ellas: el **data plane — los collectors — siempre corre en la infraestructura del cliente**. Eso hace posibles la privacidad y la operación air-gapped. No hay telemetría obligatoria ni egreso del plano de control de forma predeterminada. Solo cruza el perímetro del cliente lo que este configura para que lo cruce: llamadas a sus API de modelos, las salidas SIEM/webhook que conecta y un proveedor externo de embeddings si aprovisiona uno.

### Binario único

El predeterminado. Un único binario estático de Go lleva el motor CLI, la **UI web embebida vía `go:embed`** (servida desde el mismo origen que la API) y **SQLite** como store. Entregas un solo artefacto y lo self-hosteas. Esta es la topología detrás del [tutorial de cero a grafo](/es/tutorials/zero-to-graph/) y la [guía de self-hosting](/es/how-to/self-hosting/).

### Distribuido

Para estates multi-host, a escala y multitenant: los collectors en el borde **empujan a un núcleo central sobre gRPC con mutual TLS**, el store pasa a ser **Postgres** (con row-level security) y el bus de eventos corre sobre **NATS**. Los collectors no tienen listener entrante — empujan, no sirven — lo que mantiene mínima la superficie de ataque del borde.

### Air-gapped

En esta topología todo corre localmente con **cero egress**: el store es local y la licencia se valida **offline**. `olivares upgrade` —el único comando que si no nos contactaría— instala aquí desde un bundle transportado (`--bundle`) en vez de desde el canal de actualizaciones. Consulta [instalación air-gap](/es/how-to/air-gap-install/).

### Gestionado (futuro)

Un control plane alojado está en el roadmap. Incluso entonces, la restricción se mantiene: **los collectors siguen corriendo en la infraestructura del cliente**, y solo el control plane está alojado. Esto está en etapa de diseño.

:::tip[La topología, en una línea]
El control plane (el motor) puede self-hostearse como un binario o, en el futuro, gestionarse; el data plane (los collectors) está siempre en la infraestructura del cliente. La web es siempre una vista sobre la propia API del motor — nunca un servicio separado con su propia lógica.
:::

## Fronteras de confianza y licencia

Dos fronteras dan forma a la arquitectura más allá de la topología de runtime:

- **La frontera del conector.** Un conector **nunca importa del núcleo** — depende solo del SDK. Esto evita que los conectores de terceros contaminen el núcleo y mantiene limpia la frontera de licencia.
- **La frontera de licencia.** El núcleo, los módulos y la web son **AGPL-3.0-only**; el SDK y los conectores son **Apache-2.0**; el tier enterprise es comercial. La frontera del conector anterior es lo que hace que la división Apache/AGPL sea aplicable en código. Consulta [open core y licencia](/es/explanation/open-core-and-licensing/).

## Postura de seguridad, en breve

La arquitectura es segura por diseño: observación read-first (riesgo bajo y asimétrico), collectors push-only sin listener entrante, mutual TLS entre collector y núcleo, datos mínimos (edges, nunca payloads), evidencia de manipulación mediante el audit ledger append-only y hash-chained, aislamiento multitenant enraizado en el modelo de datos y self-hosting sin telemetría obligatoria ni egreso del plano de control de forma predeterminada. Solo cruza el perímetro del cliente lo que este configura para que lo cruce: llamadas a sus API de modelos, las salidas SIEM/webhook que conecta y un proveedor externo de embeddings si aprovisiona uno. El análisis completo — incluyendo cómo se defiende cada frontera de confianza y qué queda explícitamente fuera de alcance — vive en el [modelo de seguridad](/es/explanation/security/security-model/) y el [modelo de amenazas](/es/explanation/security/threat-model/).

## A dónde ir después

- [Catálogo de módulos](/es/reference/modules/overview/) — el conjunto completo de módulos y cómo se mapean a las capas anteriores.
- [Referencia de eventos](/es/reference/events/) — los eventos normalizados que la capa de ingesta distribuye a los módulos.
- [Modelo de amenazas](/es/explanation/security/threat-model/) — los adversarios, las fronteras de confianza y las mitigaciones.
- [Honestidad y límites](/es/start/honesty-and-limits/) — qué corre hoy frente a qué está planificado.
