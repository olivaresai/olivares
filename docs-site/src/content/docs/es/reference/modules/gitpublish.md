---
title: "Publicación Git gobernada"
description: "Envía commits, abre solicitudes de incorporación y fusiona mediante vínculos aprobados del host Git, con autorización vigente y resultados registrados."
---

El módulo gitpublish permite publicar de forma gobernada en GitHub, GitLab y remotos git simples (solo push). Forma parte de Community.

Cada efecto exige un destino, un repositorio y una credencial aprobados, además de autorización vigente. Sin la custodia requerida, el ejecutable Git o la autoridad, se rechaza la publicación. Registrar el módulo no deja un repositorio listo para publicar.

El espacio de nombres de la API es `/v1/m/gitpublish`. Expone destinos, intenciones de envío, solicitudes de incorporación y fusión, conciliación y observaciones.

Las lecturas de destinos (`GET /targets` y `GET /targets/{id}`) incluyen un `host` de solo lectura (`github`, `gitlab` o `git`) resuelto a partir del vínculo de credencial aprobado. La consola ofrece solicitudes de incorporación y fusiones solo para GitHub y GitLab, incluida la solicitud de incorporación en borrador que se ofrece tras un envío de sesión. Si un vínculo ya no está aprobado, o un motor antiguo omite el host, la consola ofrece únicamente el envío; el motor sigue comprobando la custodia y la autorización de cada efecto. Un fallo en la búsqueda del vínculo devuelve una respuesta de no disponible.

Un envío puede nombrar una ejecución de sesión (`session_run`). Olivares trae entonces el commit desde la carpeta de esa ejecución al repositorio del servidor antes de publicar, solo por el protocolo de archivos local. La ejecución debe pertenecer al espacio de trabajo del destino; una solicitud nunca nombra una ruta.

Un resultado remoto incierto no autoriza un reintento. El módulo registra por separado las solicitudes, las observaciones y los acuses de recibo. Una operación remota ya enviada puede coincidir con una revocación local posterior.

[Referencia de la API de módulos](/reference/api-beta/).
