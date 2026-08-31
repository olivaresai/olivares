---
title: Cómo está organizada esta documentación
description: >-
  Estos docs siguen Diátaxis — cuatro modos (tutoriales, guías how-to, referencia,
  explicación), cada uno respondiendo a una necesidad distinta. Aquí está cómo navegarlos.
---

Esta documentación está organizada con el framework **[Diátaxis](https://diataxis.fr/start-here/)**.
Diátaxis observa que la documentación técnica sirve a cuatro necesidades
distintas, y que mezclarlas empeora los docs para todos. Por eso la parte superior
de la barra lateral son **cuatro modos**, no una lista de funciones del producto:

| Modo | Orientación | Responde | Cuando estás… |
|---|---|---|---|
| **[Tutoriales](/es/tutorials/zero-to-graph/)** | aprendizaje | "Llévame de cero a un resultado funcional." | nuevo, y quieres aprender haciendo |
| **[Guías how-to](/es/how-to/self-hosting/)** | una tarea | "¿Cómo logro *esta cosa concreta*?" | trabajando, y necesitas una receta |
| **[Referencia](/es/reference/)** | información | "¿Qué son exactamente la API, los eventos, los módulos, los flags?" | construyendo sobre ello, y necesitas precisión |
| **[Explicación](/es/explanation/)** | comprensión | "¿*Por qué* está construido así?" | evaluando, y quieres el razonamiento |

Un mapa rápido de dónde vive cada cosa:

- **Tutoriales** — los caminos de aprendizaje: [de cero a un grafo de acceso
  de lectura/escritura](/es/tutorials/zero-to-graph/), y empezar por escenario real —
  [nodo único](/es/tutorials/getting-started/single-node/),
  [Docker Compose](/es/tutorials/getting-started/docker-compose/),
  [Kubernetes](/es/tutorials/getting-started/kubernetes/),
  [air-gapped](/es/tutorials/getting-started/air-gapped/).
- **Guías how-to** — instalar y operar ([self-host](/es/how-to/self-hosting/),
  [backup y restauración](/es/how-to/backup-and-restore/),
  [monitorización](/es/how-to/monitor-with-prometheus/),
  [troubleshooting](/es/how-to/troubleshooting/)), las
  [guías por conector](/es/how-to/connectors/pgaudit/) (pgAudit, CloudTrail,
  eBPF, Claude Code, MCP, identidad), y el
  [cookbook](/es/how-to/cookbook/deny-closed-policies/) de recetas de gobierno
  (políticas deny-closed, presupuestos, aprobaciones, triage de drift, el kill switch,
  push a SIEM).
- **Referencia** — la [API REST](/reference/api/) (renderizada desde el propio
  contrato OpenAPI 3.1 del producto), la [política de estabilidad de la API](/es/reference/api-stability/),
  el [bus de eventos](/es/reference/events/) (un contrato AsyncAPI 3.0),
  el [catálogo de módulos](/es/reference/modules/overview/), la
  [CLI](/es/reference/cli/) y la [configuración](/es/reference/configuration/).
- **Explicación** — la [arquitectura](/es/explanation/architecture/overview/), el
  [modelo de seguridad](/es/explanation/security/security-model/) y el
  [threat model](/es/explanation/security/threat-model/), el
  [licenciamiento open-core](/es/explanation/open-core-and-licensing/).

## Convenciones

- **La búsqueda** es local y del lado del cliente (Pagefind). Corre enteramente en tu navegador;
  nada se envía a un servicio de búsqueda externo — coherente con el diseño self-hosted
  del producto, en el que tú decides qué cruza tu perímetro.
- **Versionada.** La documentación está versionada: cuando se distribuye una nueva versión del producto,
  los docs de la anterior se preservan. El selector de versión vive en la
  barra superior.
- **Honesta sobre los límites.** Allí donde una capacidad está en fase de diseño, es posterior a v1, o simplemente
  no está construida todavía, los docs lo dicen llanamente. Consulta
  [Honestidad y límites](/es/start/honesty-and-limits/). Los comandos de tutoriales y how-to están
  pensados para **ejecutarse tal como están escritos**.
- **Idiomas.** La documentación canónica está en inglés; hay traducciones
  disponibles en español, chino simplificado, ruso, japonés, alemán y francés
  (traducción automática, con el inglés como versión autorizada, volviendo al
  inglés en las páginas aún no traducidas).
