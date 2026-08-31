---
title: "El modelo de seguridad"
description: "La postura secure-by-design detrás de Olivares AI — por qué read-first, datos mínimos, deny-by-default y una auditoría con alteraciones detectables son las decisiones de seguridad de fondo, no la enumeración de amenazas."
---

Olivares AI es un producto de seguridad que corre **dentro de la propia
infraestructura del cliente** y construye un mapa de lo que cada agente de IA puede
alcanzar. Eso lo hace a la vez muy sensible y muy valioso para un atacante: un defecto
en este producto es una brecha del estate del cliente. El listón es, por tanto, el más
alto posible, y la postura está diseñada para **superar un pentest y una auditoría
empresariales desde el principio** en lugar de endurecerse más tarde.

Esta página explica la **postura** — las decisiones de seguridad horneadas en el
diseño y por qué son como son. Deliberadamente **no** repite el modelo de amenazas
formal: el análisis STRIDE-por-componente y el flujo de datos de fronteras de confianza
viven en la página del [modelo de amenazas](/es/explanation/security/threat-model/). Lee
esa página para saber *qué podría salir mal y dónde*; lee esta para saber *por qué la
arquitectura está moldeada para que eso sea difícil*.

:::note[Postura, no un mapa de reconocimiento]
Esta documentación describe la postura de seguridad, no la superficie de ataque. No
enumera cadenas de permisos internos, ubicaciones de ficheros de secretos ni la
distribución de puertos de un despliegue. Eso pertenece al material de endurecimiento
para operadores, no a la documentación pública.
:::

## Read-first: riesgo asimétrico bajo

El núcleo **observa**; no se interpone. El access map se reconstruye a partir de
señales que el estate ya emite — OpenTelemetry, auditoría de bases de datos, trazas de
auditoría en la nube, y (como backstop no cooperativo) eBPF — y el colector **nunca
está en la ruta de datos del agente**.

Esto es una decisión de seguridad antes que de producto. Un enforcer en línea que se
sitúa por delante de cada acción del agente es un único punto de fallo: si se atasca o
se cae, puede tirar producción consigo, y se convierte en un objetivo de alto valor
precisamente *porque* está en la ruta. Un observador read-first lleva el perfil de
riesgo opuesto, **asimétrico**. Si el colector falla, deja de *ver* — no detiene al
agente, y no rompe producción. El fallo en el peor caso de un observador es un hueco de
visibilidad, no una caída.

La misma propiedad neutraliza la evasión obvia. El colector corre como un servicio
separado y privilegiado **fuera del control del agente**, de modo que un agente que
desactive su propia telemetría no silencia al colector — y el backstop eBPF sigue
registrando la acción a nivel de kernel. Un agente conocido que de repente se queda en
silencio se trata él mismo como una señal, no se ignora.

## Datos mínimos: lo que no se almacena no puede filtrarse

El grafo almacena **relaciones**, no contenidos. Una arista registra que un agente tocó
un recurso, en qué modo (lectura / escritura / lectura-escritura), desde qué fuente de
señal, con qué confianza y cuándo. **No** almacena el SQL que ejecutó, el cuerpo de la
petición, el secreto ni la PII que contienen. Donde un valor solo se necesita para
deduplicar, el producto guarda un hash unidireccional, nunca el valor en sí.

El principio rector es contundente: **lo que no se almacena no puede filtrarse.** El
activo más sensible del sistema — el access map — es también el que se construye
deliberadamente a partir de los datos menos sensibles.

Los campos con más probabilidad de portar secretos o PII (una entrada de herramienta,
un comando completo) se **expurgan antes de persistirse**. El expurgo no se deja al
buen comportamiento del handler: el motor la impone en la ruta de escritura,
sustituyendo un valor marcado como sensible por un hash antes de que llegue a
escribirse, como backstop incluso si un handler lo olvida. El colector lee
**identidades** — un rol de base de datos, un nombre de aplicación, un principal IAM —
no valores de credenciales ni payloads. No es un sniffer de datos.

:::note[La cobertura es escalonada, y el producto lo dice]
La fidelidad de lectura/escritura depende de lo que exponga el almacén subyacente. Es
alta en almacenes con auditoría nativa (SQL, almacenamiento de objetos, warehouses),
con pérdidas en algunos almacenes documentales/vectoriales, e **imposible de
reconstruir pasivamente** en otros. Donde no puede determinarse lectura frente a
escritura, la arista se marca como `unknown`, y la atribución colapsa a `approximate`
cuando una cuenta de servicio compartida oculta la identidad por agente. El producto
muestra esto con honestidad en lugar de fabricar certeza — consulta
[honestidad y límites](/es/start/honesty-and-limits/).
:::

## Tokens opacos y revocables frente a JWT

La autenticación usa **tokens bearer opacos**, no JWT. El token es un handle aleatorio;
toda la autoridad vive del lado del servidor, ligada a un registro que el motor
controla. Es una elección de postura. Un JWT autocontenido es un portador de claims
permanente y verificable offline que resulta incómodo de revocar antes de su
expiración; un token opaco es **revocable de inmediato** invalidando su registro del
lado del servidor, no porta claims embebidos que filtrar o en los que confiar
indebidamente, y mantiene el binding del tenant bajo control del motor en lugar de en
una firma que sostiene el cliente. Los tokens de sesión y de API son tipos distintos, y
el tenant se resuelve desde el propio binding del token — una petición cuya cabecera de
tenant contradice su token se **rechaza**, no se reconcilia.

## Sin credenciales por defecto, token de configuración de un solo uso

El fallo más común de un producto autoalojado es una **credencial por defecto**.
Olivares AI no envía **ninguna**. En el primer arranque, el motor imprime un **token de
configuración de un solo uso** por la salida estándar; el administrador lo usa para
crear el primer usuario, y a partir de ahí queda gastado. No hay cuenta integrada, ni
contraseña compartida, ni nada que olvidar cambiar. (Existe una semilla de demo solo
para evaluación; lleva una contraseña pública y **se niega a enlazar a algo que no sea
loopback** para que nunca pueda convertirse en un punto de apoyo en producción.)

## Autorización deny-by-default, una junta ABAC que solo restringe

La autorización es **deny-by-default**. El control de acceso basado en roles no concede
nada que no se le diga explícitamente que conceda. Por encima de RBAC se sitúa una
junta de política basada en atributos — el operador puede correr un motor de política
embebido en Go puro, un servicio de política externo sobre HTTP, o ninguno, todo
detrás de una única interfaz — y el invariante crítico es que **la capa ABAC solo puede
estrechar el acceso, nunca ampliarlo.** Una política puede quitar permiso; nunca puede
conceder un permiso que RBAC no permitiera ya. Ese orden significa que una política mal
configurada o demasiado permisiva no puede convertirse en una ruta de escalada de
privilegios: lo peor que puede hacer una política mala es dejar fuera a la gente, no
dejarla entrar.

## Ver el grafo es una acción privilegiada, con alcance de tenant y auditada

Como el access map es una potente herramienta de reconocimiento, el diseño trata
**leerlo como una acción privilegiada**, no como una capacidad por defecto. Se concede
desde un rol de nivel editor hacia arriba y **nunca** está disponible para el rol de
visor más bajo. Cada lectura tiene **alcance de tenant** — un cliente nunca puede ver
el estate de otro — y **cada lectura queda registrada en el audit ledger**: quién miró
el access map de qué agente, y cuándo. La defensa está estratificada aquí a propósito:
privilegio, aislamiento de tenant y autoauditoría juntos, de modo que incluso el acceso
legítimo a la vista más sensible deja un rastro responsable.

Aquí es también donde se traza la línea de uso responsable del producto. Olivares AI
está planteado **defensivamente** — ayuda a los defensores a ver y gobernar su propio
estate. No es un framework de mando y control y no escanea las credenciales de otros.
Esa línea se mantiene explícita en el [modelo de amenazas](/es/explanation/security/threat-model/).

## Auditoría append-only, hash-chained y firmada — con la exportación externa como control real

El audit ledger es **append-only** y **hash-chained**: cada registro porta el hash del
anterior, de modo que cualquier alteración silenciosa rompe la cadena y es detectable.
Por encima de la cadena, el motor produce checkpoints **firmados con Ed25519**, de modo
que la cola no puede reescribirse sin la clave de firma.

El producto es honesto sobre el límite de un ledger en la caja: un atacante con control
total del directorio de datos y de la clave en la caja podría, en principio, refirmar
una cadena falsificada. La firma por evento defiende contra el compromiso
**solo-de-base-de-datos** — inyección, una copia de seguridad o réplica robada, un
bypass de row-level-security — y contra la eliminación de checkpoints; no defiende, por
sí sola, contra el compromiso total del host.

Por eso el **control anti-manipulación real es externo**. El ledger se exporta a un
sistema **WORM/SIEM** que el cliente controla, en formatos estándar (`cef`, `leef`,
`syslog`, `otlp`, `otlp_envelope`, `otlp_log_record`, `ocsf`),
portando número de secuencia, hash previo, hash y firma, y
**nunca PII**. Una vez que una copia vive en almacenamiento inmutable fuera del
producto, un atacante que comprometa el host de Olivares no puede volver a alcanzar y
reescribir lo que el SIEM ya guarda. Esa copia externa inmutable — y no la cadena en la
caja por sí sola — es lo que pide un auditor empresarial, y es lo que la telemetría
nativa no da.

:::note[Dos caminos fuera de la caja: pull y un push real]
El ledger verificable llega a un SIEM por dos vías. La exportación **pull**
(`GET /v1/audit/export`) está siempre disponible y es el artefacto que un operador
archiva. Un **push** es real cuando se configura: una suscripción de eventing
`audit.recorded` arranca una bomba de ledger por tenant que entrega cada registro
sellado **al menos una vez** por el transporte duradero, con guardia SSRF, reintentos y
dead-letter (`modules/siemforward/forwarder.go`, cableado en `cmd/olivares/boot.go`).
`NopForwarder` es lo que aplica cuando no hay forwarding configurado — no es la única
implementación que existe. El [how-to de Splunk](/es/how-to/forward-audit-to-splunk/)
documenta ambas rutas; la verificación de firmas ocurre fuera de la caja, contra la
clave pública.
:::

## TLS activado por defecto, sin fallback a texto plano, mTLS para colectores remotos

El transporte está **cifrado por defecto y falla cerrado**. TLS está activado, y **no
hay fallback silencioso a texto plano** — una conexión que no puede asegurarse se
rechaza, no se degrada. Existe un modo de texto plano estrictamente para el desarrollo
en localhost y debe pedirse explícitamente; nunca es el valor por defecto ni la ruta de
producción.

En la topología distribuida, los colectores remotos **empujan** al núcleo central (no
hay listener entrante en el host de producción, lo que mantiene en cero la superficie
de puertos abiertos del colector), y ese canal puede exigir **mTLS** con un certificado
de cliente verificado. El cifrado en reposo lo proporciona el despliegue — de disco
completo, a nivel de sistema de ficheros o de base de datos — en lugar de un pragma a
nivel de producto, con permisos de fichero estrictos sobre el directorio de datos.

## La licencia es solo atestación — el núcleo abierto nunca se bloquea

La licencia comercial se verifica **offline** con una firma Ed25519, y en el
**núcleo abierto (AGPL)** es una **atestación, no un gate de funciones**: nada del
producto abierto se apaga jamás por una comprobación de licencia. Los add-ons
comerciales se licencian por término pagado — un derecho que termina con el término —
pero cualquier consecuencia de ello es una decisión local y offline del build
comercial; no hay kill switch remoto, y verificar la licencia nunca nos llama. Descargar
lo que has pagado sí: la suscripción es la credencial con la que se obtienen los add-ons
comerciales, sus actualizaciones y sus parches — el modelo SUSE/Novell, descrito en
[autoalojamiento](/es/how-to/self-hosting/). Esto importa
especialmente para el caso air-gapped: el producto debe seguir haciendo su trabajo de
seguridad — observar, registrar, auditar — independientemente del estado de la
licencia, porque un control de seguridad que se degrada silenciosamente ante un
problema de licencia es él mismo una vulnerabilidad. La revocación se gestiona mediante
la expiración de la suscripción, no inutilizando el motor en marcha.

<a id="autoalojado-los-datos-permanecen-dentro-del-perímetro-del-cliente"></a>

## Autoalojado: el cliente decide qué cruza su perímetro

La propiedad estructural más fuerte del diseño es que **no hay telemetría obligatoria ni
egreso del plano de control de forma predeterminada**. Solo cruza el perímetro del cliente
lo que este configura para que lo cruce: llamadas a sus API de modelos, las salidas
SIEM/webhook que conecta y un proveedor externo de embeddings si aprovisiona uno.
Olivares AI corre en los propios hosts del cliente; el
plano de datos (los colectores) **siempre** corre en infraestructura del cliente; y
**no hay telemetría-a-casa** — no se envía nada a Olivares AI como efecto de ejecutar. Al
proveedor solo se le llega cuando el cliente le pide algo — `olivares upgrade`, o una descarga
por suscripción de add-ons comerciales y sus actualizaciones — y el proveedor no ve el
access map del cliente.

Esa es una respuesta directa y defendible a los requisitos de **RGPD y residencia de
datos**: cada cruce es uno que el cliente ha aprovisionado, por lo que es el cliente quien
determina y demuestra la residencia, no el proveedor quien la concede. Y hace de la
topología **air-gapped** un despliegue de primera clase — todo
local, **cero egreso**, licencia offline — en lugar de una ocurrencia tardía, para
estates que deben correr sin ninguna red saliente en absoluto. Consulta las guías de
[autoalojamiento](/es/how-to/self-hosting/) y [instalación air-gap](/es/how-to/air-gap-install/).

:::tip[Diseña para auditoría, certifica después]
La arquitectura está construida para **mapearse sobre** los controles que buscan SOC 2,
ISO 27001 y la EU AI Act — registro de auditoría, control de acceso, integridad,
cifrado, gestión de cambios — de modo que supere la revisión cuando llegue el momento.
La certificación formal es un paso posterior y separado; el diseño la habilita, no la
reclama. La página de [honestidad y límites](/es/start/honesty-and-limits/) es el contrato
vinculante sobre lo que está construido hoy frente a lo que está diseñado.
:::

## Por qué estas decisiones se sostienen juntas

Ninguna de estas elecciones se sostiene sola. Read-first mantiene al producto fuera del
radio de impacto de los mismos sistemas que vigila. Datos mínimos reduce lo que una
brecha del producto podría siquiera exponer. Los tokens opacos, la ausencia de
credenciales por defecto, el RBAC deny-by-default y una junta ABAC que solo restringe
hacen que la autoridad sea pequeña, revocable e imposible de ampliar por accidente. El
ledger hash-chained, firmado y exportado externamente hace que la propia honestidad del
producto sea **verificable** en lugar de meramente prometida. Y el autoalojamiento implica
que no hay telemetría obligatoria ni egreso del plano de control de forma predeterminada.
Solo cruza el perímetro del cliente lo que este configura para que lo cruce: sus API de
modelos, las salidas SIEM/webhook que conecta y un proveedor externo de embeddings si
aprovisiona uno. La postura es el argumento
de seguridad; el [modelo de amenazas](/es/explanation/security/threat-model/) es donde cada
una de estas se contrasta contra una amenaza concreta.
