---
title: "Proxy de inferencia en línea (PEP para /v1/messages)"
description: >-
  Un punto de aplicación de políticas opcional y de adhesión explícita que se
  antepone al contrato /v1/messages de Claude para llamantes con SDK directo y
  curl, aplicando residencia, acceso a modelos, ventana de contexto, DLP y
  presupuesto en banda antes de reenviar — cerrando el bypass de
  ANTHROPIC_BASE_URL — con grabación con evidencia de manipulación anclada antes
  del reenvío por defecto, y sus excepciones dichas en la página en vez de
  descubiertas. La configuración y la autoría de DLP están operativas; el listener
  permanece sin montar hasta que un operador lo aprovisiona.
---

El proxy de inferencia en línea es el punto de
aplicación para el tráfico de inferencia que **no** es Claude Code — llamantes
con SDK directo y `curl` que acceden directamente al contrato `/v1/messages` de
Claude. La decisión se toma en la raíz de composición
(`cmd/olivares/inferenceproxy.go`);
`modules/inferenceproxy` posee la configuración de gobierno por tenant y la
política DLP de egreso de inferencia, que sirven de entrada a esa decisión, y no
decide nada por sí mismo sobre una petición en vivo.
La configuración gestionada por el servidor no puede alcanzar ese
tráfico: un `ANTHROPIC_BASE_URL` personalizado la sortea por completo. El proxy
se antepone a `api.anthropic.com` y ejecuta una tubería gobernada **en banda** —
residencia, [acceso a modelos](/es/reference/modules/x-models/), DLP y los gates
de contenido, y después el dimensionamiento de la ventana de contexto y el
presupuesto — antes de reenviar un solo byte por `/v1/messages`.

La grabación es **previa al reenvío por defecto**: la intención autorizada se
escribe en el ledger con alteraciones detectables *antes* del reenvío, y sin
evidencia no hay reenvío (deny-closed). Un tenant puede desactivarlo
deliberadamente (`record_mandatory: false`), y entonces la evidencia se ancla
*después* del reenvío, en modo best-effort y ruidoso — un anclaje fallido se
reporta, nunca se oculta.

Ese default era el contrario, y la diferencia no es académica: un tenant que
nunca ha abierto la página de configuración es precisamente aquel sobre el que
nadie ha razonado, así que anclarlo best-effort dejaba la garantía de evidencia
como opt-in para todo el que no había optado por nada. Conviene decir dos
límites en vez de descubrirlos. Primero, la postura gobierna **únicamente** el
momento previo al reenvío: después del reenvío la llamada ya ha ocurrido y
ninguna postura puede deshacerla, así que ese camino es un hueco ruidoso por
construcción. Segundo, un operador que ha fijado el spool de auditoría en
`degrade` ya ha dicho qué debe pasar cuando se agote: para un tenant que nunca
eligió postura de evidencia, ese `degrade` declarado gana y la llamada se
reenvía con un hueco registrado. A un tenant que fijó `record_mandatory: true`
se le deniega en cambio — su propia elección manda sobre la del spool.

La comprobación previa de dimensionamiento `count_tokens` también es un egreso al
proveedor, por lo que solo se ejecuta **después** de que hayan pasado todos los gates
locales de contenido: un prompt denegado por DLP o por el firewall nunca se transmite,
ni siquiera para contar tokens. El proxy es uno de los **cuatro PEP deny-closed** que
incluye la plataforma.

## Madurez, dicho con claridad

**PARCIAL.** La división es honesta y deliberada:

- **OPERATIVO** — la configuración de gobierno por tenant y la política DLP de
  egreso de inferencia: autoría, persistencia y auditoría. Dos stores bajo
  `/v1/m/inferenceproxy/`: un `config` singleton (toggles por gate, la postura
  de fallo con el proxy caído, el modo de DLP de respuesta, el mandato de
  grabación) y un conjunto `dlp/rules` (una regla por clase de sensibilidad →
  `allow`|`deny`).
- **DE ADHESIÓN EXPLÍCITA, sin montar por defecto** — el listener real de
  `/v1/messages`. Por defecto se enlaza a **loopback** (`127.0.0.1:8448`), y un
  operador puede configurar explícitamente otra dirección; por defecto falla
  **CERRADO** (un proxy que no puede decidir no debe reenviar) y no se monta
  salvo que un operador lo aprovisione.

Este módulo **no decide nada** sobre una petición en vivo. Es la política
durable y autorable desde consola que la raíz de composición lee vía `Policy()`;
la decisión se compone a partir de seams existentes (`EvaluateModelAccess`,
`CheckBudget`, residencia, `ClassifySensitivity`, la comprobación de ventana de
contexto) en el borde.

## La tubería en banda

Cada gate está **habilitado** por defecto, y cada uno permanece inerte bajo su
propia adhesión explícita nativa hasta que se configura — DLP hasta la primera
regla, acceso a modelos hasta la primera concesión, residencia solo cuando se
fija una región, presupuesto hasta que existe un presupuesto que aplique. Un
tenant relaja un gate concreto de forma explícita, y la auditoría registra quién
abrió el perímetro. Escribir la política DLP de egreso es de **nivel admin**:
autorizar qué contenido puede salir es un cambio de gobierno privilegiado.

## Contexto acotado

- **Datos mínimos por diseño.** Ninguna fila que persiste — config, regla DLP,
  auditoría — porta jamás un prompt, una respuesta, un secreto o un valor de PII
  detectado. Los bytes que el proxy inspecciona en vuelo se huellan digitalmente
  (SHA-256) y la raíz de composición los ancla al ledger, nunca se almacenan
  aquí.
- Es la **tercera pata** del proxy: la cáscara de protocolo (parsear, reenviar,
  duplicar los cuerpos) es el connector Apache ciego a la identidad; el decisor
  gobernado es el motor. Este módulo posee únicamente la política que ambos
  consultan — manteniendo la decisión fuera de la frontera del connector
  open-core.

## Relacionado

- [Módulo X — gestión de modelos y proveedores](/es/reference/modules/x-models/) — la
  política de acceso a modelos y de ventana de contexto por superficie que este
  proxy aplica.
- [Ejecutar Claude Code con Olivares](/es/how-to/run-claude-code-with-olivares/) — la
  vía gobernada de Claude Code, que cubre el hook en proceso; este proxy es el
  PEP de respaldo para los llamantes que esa vía no puede alcanzar.
- [Honestidad y límites](/es/start/honesty-and-limits/) — qué está operativo, en
  adhesión explícita o en fase de diseño en toda la plataforma.
