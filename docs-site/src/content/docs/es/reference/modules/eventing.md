---
title: "Plataforma de eventing y webhooks"
description: >-
  La superficie de suscripción orientada al integrador sobre el bus de eventos
  del motor: suscripciones a eventos tipados con entrega de webhooks firmados,
  semántica durable de al-menos-una-vez, reintentos/backoff, una cola de
  mensajes muertos y replay por cursor. Es la frontera de durabilidad que el bus
  en proceso no proporciona.
---

Eventing (`modules/eventing`, **LIVE**) convierte el bus de eventos en
proceso del motor en una **superficie de suscripción externa**. Mientras que el
bus en sí es de a-lo-sumo-una-vez y descarta al apagarse, este módulo es la
**frontera de durabilidad**: una vez que un evento se captura en la transacción
de captura, la entrega es durable y auditable. Sus rutas se montan bajo
`/v1/m/eventing/`.

## A qué te suscribes

Una **suscripción** registra los tipos de evento que quiere, un filtro de origen
opcional, una URL de endpoint del consumidor, el rol bajo el que se autorizan sus
entregas y un secreto de firma HMAC generado por el servidor (devuelto
exactamente una vez, y a partir de ahí retenido solo a través del seam sellado en
reposo). Los tipos suscribibles provienen de un catálogo tipado — `GET
/event-types` devuelve cada tipo con su nivel de estabilidad y el permiso que lo
controla. Gestionar suscripciones es privilegiado y auditado: crear/actualizar/
rotar-secreto es de nivel write; eliminar, hacer replay, reenviar y probar
entregas es de nivel admin.

## Garantías de entrega

La entrega es de **al-menos-una-vez con claves de idempotencia del consumidor** —
exactamente-una-vez se rechazó por ser una promesa falsa. Cada evento capturado
se convierte en una fila de entrega durable por cada suscripción coincidente,
encolada en la misma transacción. Los workers reclaman filas por versión
optimista (seguro bajo HA), hacen POST del sobre del evento firmado, y o bien
confirman (2xx) o programan el siguiente intento:

- **Reintentos/backoff** — 408/425/429/5xx y errores de red reintentan según un
  calendario de backoff; cualquier otro estado es terminal. Las redirecciones no
  se siguen nunca.
- **Cola de mensajes muertos** — las entregas agotadas acaban en el estado
  `dead`; un estado `denied` registra un rechazo RBAC por evento.
- **Replay por cursor** — una secuencia monótona por tenant (asignada desde una
  fila de cursor, no `max(seq)`) te permite hacer replay desde un punto del log
  durable, acotado por la ventana de retención.

Cada intento lleva la firma HMAC-SHA256 con marca temporal al estilo Stripe más un
id de evento estable como clave de idempotencia. Antes de cada intento el
dispatcher ejecuta la pipeline completa RBAC+ABAC deny-closed contra el rol de la
suscripción, de modo que un evento saliente se filtra exactamente igual que lo
haría una lectura en vivo.

## Contexto acotado, dicho con claridad

- El **bus en proceso es de a-lo-sumo-una-vez** con descarte al apagarse; la
  durabilidad empieza en la transacción de captura, no en la publicación. Los
  eventos publicados mientras ninguna suscripción habilitada coincide no se
  capturan (frugal en almacenamiento), de modo que el replay solo alcanza hasta la
  captura.
- El puente NATS multinodo es honestamente de **a-lo-sumo-una-vez** — esta
  plataforma es la capa durable por encima de él, no una garantía sobre el propio
  bus distribuido.
- Es la superficie **orientada al integrador**; [notify](/es/reference/modules/xv-notify/)
  sigue siendo el enrutador de alertas orientado al operador. Consulta
  [honestidad y límites](/es/start/honesty-and-limits/) para las convenciones
  live / on-demand / deny-closed.

## Relacionado

- [Reenvío a SIEM](/es/reference/modules/siemforward/) — envía el ledger sellado y
  los hallazgos a las torres SIEM; construido directamente sobre esta plataforma.
- [Notify](/es/reference/modules/xv-notify/) — el enrutador de alertas orientado al
  operador hacia destinos aprovisionados.
- [Referencia de eventos](/es/reference/events/) — el vocabulario de eventos al que
  te suscribes y la forma del sobre entregado.
