---
title: "Publicación Git gobernada"
description: "Envía commits, abre solicitudes de incorporación y fusiona mediante vínculos aprobados del host Git, con autorización vigente y resultados registrados."
---

El módulo gitpublish permite publicar de forma gobernada en GitHub y GitLab. Forma parte de Community.

Cada efecto exige un destino, un repositorio y una credencial aprobados, además de autorización vigente. Sin la custodia requerida, el ejecutable Git o la autoridad, se rechaza la publicación. Registrar el módulo no deja un repositorio listo para publicar.

El espacio de nombres de la API es `/v1/m/gitpublish`. Expone destinos, intenciones de envío, solicitudes de incorporación y fusión, conciliación y observaciones.

Un resultado remoto incierto no autoriza un reintento. El módulo registra por separado las solicitudes, las observaciones y los acuses de recibo. Una operación remota ya enviada puede coincidir con una revocación local posterior.

[Referencia de la API de módulos](/es/reference/api-beta/).
