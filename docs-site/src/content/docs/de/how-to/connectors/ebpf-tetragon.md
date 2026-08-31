---
title: "eBPF / Tetragon (der Kernel-Backstop)"
description: >-
  Verdrahte die nicht-kooperative Hälfte der Access map: Tetragon erfasst
  Kernel-Datei- und -Netzwerk-Events außerhalb der Kontrolle des Agenten, und
  der Connector verwandelt seinen JSON-Export in ehrlich approximative
  Access-Edges — plus einen optionalen Anti-Evasion-Detektor.
sidebar:
  order: 3
---

Die Quelle `ebpf` ist die **Anti-Evasion-Hälfte** der R/RW-Map. Wo der
kooperative Pfad sieht, was ein Agent *meldet*, sieht diese, was der Kernel
*getan hat* — Datei-Reads/-Writes und ausgehende Verbindungen — selbst wenn
ein Agent seine eigene Telemetrie deaktiviert, denn sie läuft **außerhalb der
Kontrolle des Agenten**.

Zwei Designentscheidungen definieren sie, und beide sind die
Sicherheits-Posture:

- **Sie lädt eBPF-Programme nicht selbst.** [Tetragon](https://tetragon.io)
  erledigt die Kernel-Erfassung, deployt als separater gehärteter Dienst, der
  `CAP_BPF` + `CAP_PERFMON` hält. Der Connector ist ein
  **capability-loser, schreibgeschützter Konsument** von Tetragons
  JSON-Event-Export (eine geteilte Datei/FIFO, Modus `0600`, oder stdin).
- **Sie ist blind für TLS-Bodies und Payloads.** Sie beobachtet
  Zugriffsbeziehungen — niemals Inhalt.

Das Repository liefert das Referenz-Deployment unter `connectors/ebpf/deploy/`:
ein gehärtetes Tetragon-DaemonSet, die zwei TracingPolicies (Dateizugriff,
Netzwerk) und eine Compose-Variante für Einzelhosts.

## Was er emittiert

| Feld | Wert |
|---|---|
| Signalquelle | `ebpf` |
| Modus | Datei-`read` / `write`, Netzwerk-Connect-Edges |
| Ursprung | eine **Runtime-Identität** (Prozess/Container) — Art `identity`, niemals ein aufgelöster Agent |
| Konfidenz | **immer `approximate`** — siehe unten |
| Coverage-Tier | Kernel-Backstop |

Das `approximate` ist präzise, nicht bescheiden: Der *Zugriff* ist
Kernel-Ground-Truth (der Syscall ist passiert); was der Kernel nicht geben
kann, ist der *Agent* — er kennt den Prozess und die cgroup, nicht, welcher
governte Agent das war. Das Access-Map-Modul wertet die Attribution auf, wenn
eine Identitätsquelle die Runtime-Identität an einen Agenten bindet.

## 1. Tetragon deployen (der Sensor)

Auf Kubernetes wendest du das gelieferte DaemonSet und die TracingPolicies an:

```bash
kubectl apply -f connectors/ebpf/deploy/tetragon-daemonset.yaml
kubectl apply -f connectors/ebpf/deploy/tracingpolicy-file-access.yaml
kubectl apply -f connectors/ebpf/deploy/tracingpolicy-network.yaml
```

Tetragon schreibt seinen JSON-Export in das geteilte Volume
(`/var/run/olivares/tetragon.log`); der Connector liest ihn von der anderen
Seite. Auf einem Einzelhost ist `connectors/ebpf/deploy/docker-compose.yaml`
derselbe Split ohne Kubernetes. Die vollständige Architektur und die
Härtungshinweise stehen in `connectors/ebpf/deploy/README.md`.

## 2. Die Quelle deklarieren

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

| Schlüssel | Default | Bedeutung |
|---|---|---|
| `events_path` | `-` (stdin) | Tetragon-JSON-Event-Stream — Datei, FIFO oder stdin |
| `follow` | `true` | weiterlesen, während der Stream wächst |
| `detect_evasion` | `false` | Opt-in: einen bekannten Agentenprozess markieren, dessen kooperative Telemetrie verstummt, während der Kernel ihn noch agieren sieht |
| `evasion_window` | `5m` | Karenzzeit, bevor eine fehlende kooperative Verbindung markiert wird |
| `agent_signatures` | `claude,claude-code` | als kooperative Agenten klassifizierte Executable-Namen für den Detektor |
| `otlp_endpoints` | `127.0.0.1:4317,127.0.0.1:4318` | die kooperativen Telemetrie-Endpunkte, deren Verbindungen der Detektor korreliert |

Der Connector konsumiert Tetragon-`ProcessKprobe`-Events (Dateioperationen und
Netzwerk-Connects) und `ProcessExit` (Detektor-Zustand); `ProcessExec` wird für
den Attributionskontext genutzt und niemals als Edge emittiert.

## 3. Was du in der Konsole siehst

Kernel-Edges treten der Access map zugeschrieben zu Runtime-Identitäten bei,
stets als `approximate` markiert. Die Ausgabe des Detektors landet in
**Security** als Findings — eine Session, die aufhört zu emittieren, während
der Kernel noch Aktivität sieht, ist genau der Fall, für den diese Quelle
existiert:

<img class="light:sl-hidden" src="/console/security-dark.png" alt="Die Security-Ansicht listet Findings aus den detektivischen Quellen des Estates." />
<img class="dark:sl-hidden" src="/console/security-light.png" alt="Die Security-Ansicht listet Findings aus den detektivischen Quellen des Estates." />

## Ehrliche Grenzen

- **Ihre Ende-zu-Ende-Attributionstiefe wird noch erprobt.** Der kooperative
  Pfad und das store-native Audit sind die verifizierten, hochauflösenden
  Signale; behandle den Kernel-Backstop als Niveauanheber, nicht als fertige
  Primärquelle ([Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/)).
- **Tetragons Scope sind seine TracingPolicies.** Die gelieferten Policies
  decken Dateizugriff und Netzwerk-Connects ab; was sie nicht tracen,
  existiert nicht im Export.
- **Prozess ≠ Agent.** Ohne eine Identitätsbindung bleibt jede Kernel-Edge
  `approximate` — by design, nicht aus Versehen.

## Verwandt

- [Claude Code anbinden](/de/how-to/connect-claude-code/) — die kooperative
  Hälfte, die dies absichert.
- [SSO/SCIM & Identitätsquellen](/de/how-to/connectors/sso-scim-identity/) — wie
  die Attribution aufgewertet wird.
- [Security-Härtung](/de/how-to/security-hardening/) — wo der Backstop in die
  Verteidigungs-Posture passt.
