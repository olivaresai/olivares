---
title: "MCP-Introspektion & Registry-Governance"
description: >-
  Inventarisiere jeden MCP-Server, den deine Agenten erreichen können, behandle
  seine selbstdeklarierten Hints laut Spezifikation als untrusted, scanne den
  Katalog auf Tool-Poisoning und Posture-Probleme und gleiche ihn gegen die
  öffentlichen und föderierten Registries ab.
sidebar:
  order: 7
---

Die Quelle `mcp` governt die **Capability-Surface**, die deine Agenten sehen:
Sie introspiziert MCP-Server (Tools, Ressourcen, Prompts), leitet aus ihren
Annotationen Lese-/Schreib-*Hints* ab und gleicht — optional — das, was läuft,
gegen die öffentliche MCP Registry, deine föderierten Registries und den Docker
MCP Catalog ab, wobei sie unterwegs die Posture benotet.

Eine Regel verankert alles, was diese Quelle emittiert:

:::caution[MCP-Annotationen sind untrusted, per Spezifikation]
Die `readOnlyHint` / `destructiveHint` eines Servers sind Selbstdeklarationen,
und die MCP-Spezifikation sagt, dass Clients sie als untrusted behandeln
MÜSSEN. Jede Edge, die diese Quelle erzeugt, ist ein **deklarierter
Capability-Hint** — `approximate`, weder observed noch permitted — der die
Surface liefert, gegen die gediffed wird. Sie wird durch beobachtete Quellen
korroboriert, niemals allein für vertrauenswürdig gehalten.
:::

## Die Quelle deklarieren

```json
{
  "sources": [{
    "name": "mcp-estate",
    "kind": "mcp",
    "tenant": "<tenant-id>",
    "config": {
      "config_path": "/etc/claude-code/.mcp.json",
      "posture_scan": "true",
      "registry_enabled": "true"
    }
  }]
}
```

Richte sie auf beide Weisen auf Server aus:

| Schlüssel | Bedeutung |
|---|---|
| `servers` | inline JSON-Array von MCP-Server-Specs zum Introspizieren |
| `config_path` | Pfad zu einer Claude-Code-`.mcp.json`, deren `mcpServers` introspiziert werden |
| `timeout` | Introspektions-Timeout pro Server |

## Die Governance-Schichten (jede optional, jede ehrlich)

- **Posture-Scan** (`posture_scan`, Default `true`) — scannt die
  introspizierten Katalog-Metadaten auf Tool-Poisoning, Injection, Homoglyphen
  und zu breite Scopes und benotet die Posture gegen die OWASP MCP Top-10. Nur
  Katalog-*Metadaten* — sie probt oder exploitet keine Server.
- **Public Registry** (`registry_enabled`, Default `false`; `registry_url`) —
  schreibgeschützte Provenance-Anreicherung aus der MCP Registry (Preview
  upstream; der Connector self-verifiziert, was er liest).
- **Registry-Sync** (`registry_sync` + `owned_namespaces`) — enumeriert die
  Reverse-DNS-Namespaces, die deine Organisation in der öffentlichen Registry
  besitzt, um zurückgezogene oder unverwaltete Publikationen zu erkennen (der
  Supply-Chain-Aspekt), und entlastet deine internen Server vom
  Shadow-Flagging.
- **Interne Reconciliation** (`internal_servers`) — ein JSON-Array genehmigter
  interner Server (`{name, registry_name, version}`); laufende Server werden
  dagegen abgeglichen, mit Versions-Drift-Erkennung. Was läuft, aber nicht auf
  der Liste steht, ist ein **Shadow**-Finding.
- **Föderierte Registries** (`federated_registries`) — GitHub-BYO-Org-Registries,
  Azure API Center und private Subregistries, die die gepinnte
  **`/v0.1`-Registry-OpenAPI** implementieren.
- **Deprecation-Feed** (`deprecation_feed`) — holt bei jedem Durchlauf die
  offizielle MCP-Deprecated-Features-Registry, um Regel-Drift zu erkennen; die
  kompilierten Deprecation-Regeln hängen niemals vom Abruf ab.
- **Docker MCP Catalog** (`docker_catalog`) — Image-Digest-Pin-Drift plus
  Docker-built (signiert) vs. Community (unattestiert) Provenance pro Server.
- **Next-Revision-Preview** (`next_revision_preview`) — introspiziert Server im
  Stateless-Modus des MCP-2026-07-28-RC, während sie noch 2025-11-25
  annoncieren; explizit ein Preview-Schalter.

Findings landen pro Schicht: Posture-Noten, Provenance-Lücken,
Shadow-Server, Nutzung deprecated Features, Registry-Drift.

## Was du in der Konsole siehst

**MCP & skills** ist der Live-Capability-Katalog — Server, ihre Tools und
deklarierten Hints, Skills und wie jedes in Agenten verdrahtet ist:

<img class="light:sl-hidden" src="/console/capabilities-dark.png" alt="Die MCP-&-skills-Ansicht: der Live-Capability-Katalog mit Servern, Tools, Verdrahtung und Managed Configs." />
<img class="dark:sl-hidden" src="/console/capabilities-light.png" alt="Die MCP-&-skills-Ansicht: der Live-Capability-Katalog mit Servern, Tools, Verdrahtung und Managed Configs." />

Die Hints tragen die *deklarierte* Surface zur **Access map** bei; das
Drift-Panel ist der Ort, an dem ein als read-only deklariertes Tool, das beim
Schreiben beobachtet wird, aufhört, ein Hint-Problem zu sein, und zu einem
Finding wird.

## Ehrliche Grenzen

- **Introspektion ist ein Snapshot dessen, was Server behaupten.** Ein Server
  kann lügen; das ist die eigene Position der Spezifikation und der Grund,
  warum jede Edge so markiert ist, wie sie ist. Die Korroboration kommt aus
  beobachteten Quellen.
- **Ein partieller Registry-Snapshot ist ein Fehler, kein Resultat** — der
  Connector weigert sich, gegen einen Registry-Read zu benoten, den er nicht
  abschließen konnte.
- **Der Posture-Scan liest Metadaten.** Er führt keine Tools aus, fuzzt keine
  Server und erkennt keine hintertürte Implementierung hinter einem sauberen
  Katalog.

## Verwandt

- [Claude Code anbinden](/de/how-to/connect-claude-code/) — wo MCP-Hints auf
  Session-Telemetrie treffen.
- [Modul V — MCP, Skills & Capabilities](/de/reference/modules/v-capabilities/).
- [Einen Connector bauen und ausliefern](/de/how-to/build-a-connector/) — die
  deny-closed signierte Admission-Story für die Connector-Binaries selbst.
