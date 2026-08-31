---
title: "eBPF / Tetragon (el backstop del kernel)"
description: >-
  Cablea la mitad no cooperativa del access map: Tetragon captura eventos de
  fichero y red del kernel fuera del control del agente, y el conector convierte
  su exportación JSON en aristas de acceso honestamente aproximadas — más un
  detector anti-evasión opcional.
sidebar:
  order: 3
---

La fuente `ebpf` es la **mitad anti-evasión** del mapa R/RW. Donde el camino
cooperativo ve lo que un agente *reporta*, esta ve lo que el kernel *hizo* —
lecturas/escrituras de fichero y conexiones salientes — incluso cuando un agente
desactiva su propia telemetría, porque se ejecuta **fuera del control del
agente**.

Dos decisiones de diseño la definen, y ambas son la postura de seguridad:

- **No carga programas eBPF por sí misma.** [Tetragon](https://tetragon.io)
  hace la captura del kernel, desplegado como un servicio endurecido aparte que
  posee `CAP_BPF` + `CAP_PERFMON`. El conector es un **consumidor de solo
  lectura y cero capacidades** de la exportación de eventos JSON de Tetragon (un
  fichero/FIFO compartido, modo `0600`, o stdin).
- **Es ciega a los cuerpos TLS y a los payloads.** Observa relaciones de acceso
  — nunca contenido.

El repositorio incluye el despliegue de referencia en `connectors/ebpf/deploy/`:
un DaemonSet endurecido de Tetragon, las dos TracingPolicies (acceso a ficheros,
red) y una variante de Compose para hosts individuales.

## Qué emite

| Campo | Valor |
|---|---|
| Fuente de señal | `ebpf` |
| Modo | `read` / `write` de fichero, aristas de conexión de red |
| Origen | una **identidad de runtime** (proceso/contenedor) — clase `identity`, nunca un agente resuelto |
| Confianza | **siempre `approximate`** — ver más abajo |
| Nivel de cobertura | backstop del kernel |

El `approximate` es preciso, no modesto: el *acceso* es ground truth del kernel
(la syscall ocurrió); lo que el kernel no puede dar es el *agente* — conoce el
proceso y el cgroup, no qué agente gobernado era. El módulo de access map mejora
la atribución cuando una fuente de identidad vincula la identidad de runtime a
un agente.

## 1. Desplegar Tetragon (el sensor)

En Kubernetes, aplica el DaemonSet y las TracingPolicies incluidos:

```bash
kubectl apply -f connectors/ebpf/deploy/tetragon-daemonset.yaml
kubectl apply -f connectors/ebpf/deploy/tracingpolicy-file-access.yaml
kubectl apply -f connectors/ebpf/deploy/tracingpolicy-network.yaml
```

Tetragon escribe su exportación JSON en el volumen compartido
(`/var/run/olivares/tetragon.log`); el conector la lee desde el otro lado. En un
host individual, `connectors/ebpf/deploy/docker-compose.yaml` es la misma
separación sin Kubernetes. La arquitectura completa y las notas de endurecimiento
están en `connectors/ebpf/deploy/README.md`.

## 2. Declarar la fuente

```json
{
  "sources": [{
    "name": "node-kernel-backstop",
    "kind": "ebpf",
    "tenant": "<tenant-id>",
    "config": {
      "events_path": "/var/run/olivares/tetragon.log",
      "detect_evasion": "true"
    }
  }]
}
```

| Clave | Por defecto | Significado |
|---|---|---|
| `events_path` | `-` (stdin) | flujo de eventos JSON de Tetragon — fichero, FIFO o stdin |
| `follow` | `true` | seguir leyendo a medida que el flujo crece |
| `detect_evasion` | `false` | opt-in: marca un proceso de agente conocido cuya telemetría cooperativa queda en silencio mientras el kernel lo sigue viendo actuar |
| `evasion_window` | `5m` | periodo de gracia antes de marcar una conexión cooperativa ausente |
| `agent_signatures` | `claude,claude-code` | nombres de ejecutable clasificados como agentes cooperativos para el detector |
| `otlp_endpoints` | `127.0.0.1:4317,127.0.0.1:4318` | los endpoints de telemetría cooperativa cuyas conexiones correlaciona el detector |

El conector consume eventos `ProcessKprobe` de Tetragon (operaciones de fichero
y conexiones de red) y `ProcessExit` (estado del detector); `ProcessExec` se usa
como contexto de atribución y nunca se emite como arista.

## 3. Qué verás en la consola

Las aristas del kernel se incorporan al access map atribuidas a identidades de
runtime, siempre marcadas como `approximate`. La salida del detector aterriza en
**Security** como findings — una sesión que deja de emitir mientras el kernel
sigue viendo actividad es exactamente el caso para el que existe esta fuente:

<img class="light:sl-hidden" src="/console/security-dark.png" alt="La vista de Security listando findings de las fuentes detectivescas del estate." />
<img class="dark:sl-hidden" src="/console/security-light.png" alt="La vista de Security listando findings de las fuentes detectivescas del estate." />

## Límites honestos

- **Su profundidad de atribución de extremo a extremo todavía se está
  probando.** El camino cooperativo y la auditoría nativa del store son las
  señales verificadas y de alta fidelidad; trata el backstop del kernel como un
  elevador del suelo, no como una fuente primaria terminada
  ([Honestidad y límites](/es/start/honesty-and-limits/)).
- **El alcance de Tetragon son sus TracingPolicies.** Las políticas incluidas
  cubren el acceso a ficheros y las conexiones de red; lo que no rastrean no
  existe en la exportación.
- **Proceso ≠ agente.** Sin un vínculo de identidad, cada arista del kernel
  permanece `approximate` — por diseño, no por accidente.

## Relacionado

- [Conectar Claude Code](/es/how-to/connect-claude-code/) — la mitad cooperativa
  que esto respalda.
- [SSO/SCIM y fuentes de identidad](/es/how-to/connectors/sso-scim-identity/) —
  cómo se mejora la atribución.
- [Endurecimiento de seguridad](/es/how-to/security-hardening/) — dónde encaja el
  backstop en la postura defensiva.
