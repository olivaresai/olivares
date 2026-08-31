---
title: "Anthropic Admin Plane (Nutzung, Kosten, Compliance)"
description: >-
  Die Claude-Organisation selbst governen: autoritative abgerechnete Kosten und Nutzung
  via Admin API, API-seitige MCP- und Server-Tool-Allow-Sets als Permitted
  Edges, der Compliance-Activity-Feed und das Org-Directory — jedes Credential
  begrenzt, jeder blinde Fleck benannt.
sidebar:
  order: 6
---

Claude-Code-Telemetrie sagt dir, was auf Entwicklermaschinen läuft. Das
**Anthropic Admin Plane** sagt dir, was die *Organisation* tut: abgerechnete
Kosten, Nutzung pro Workspace, Org-Mitglieder und -Keys, der Compliance-Activity-
Feed. Vier nur-lesende Quellen decken es ab; diese Seite verkabelt die zwei zentralen
und fasst ihre Roster-seitigen Begleiter zusammen.

| Quelle (`kind`) | Was sie liest | Credential |
|---|---|---|
| `claude-api` | Nutzung & abgerechnete Kosten, Modell-/Workspace-Inventar, Claude Code Analytics, API-seitige MCP-/Server-Tool-Governance | Admin API Key (`admin_key`) |
| `claude-compliance` | der Compliance-Activity-Feed (Evidenz-grade Events) + das Org-Directory | Activity-Feed-Key + ein **eigener** Compliance Access Key |
| `claude-console` | Org-IAM-Roster (Mitglieder, Rollen) → SSO/SCIM-Posture-Findings | Console-Credentials |
| `claude-wif` | nicht-menschliche Identitäten (Service-Accounts `svac_…`, föderierte Identitäten) + ihre **erlaubten** Scope-Edges | WIF-Endpoint-Credentials |

Alle sind **nur-lesend und deny-closed**: ein leeres Credential bedeutet, dass dieser Feed
aus ist und das Produkt sagt das — niemals ein fabriziertes leeres Inventar.

## `claude-api`: Kosten, Nutzung und API-seitige Governance

```json
{
  "sources": [{
    "name": "anthropic-org",
    "kind": "claude-api",
    "tenant": "<tenant-id>",
    "config": {
      "admin_key": "<admin-api-key-reference>",
      "cost_report": "true",
      "claude_code": "true"
    }
  }]
}
```

Die Schlüssel, die zählen (aus dem ausgelieferten Descriptor; Defaults in Klammern):

- **`admin_key`** (Secret) — der Anthropic Admin API Key. Leer = nur Offline-
  Katalog.
- **`cost_report`** (`true`) — den **abgerechneten** Kostenbericht ziehen (täglich,
  autoritativ) neben der abgeleiteten Nutzungsschätzung. Das Produkt hält die
  beiden auseinander: Schätzungen werden gegen abgerechnete Zahlen abgeglichen, eine Quelle der Kosten
  pro Sitzung, niemals beide.
- **`lookback`** (`24h`) / **`cost_lookback`** (`48h`) /
  **`bucket_width`** (`1d`; auch `1h`, `1m`) / **`max_pages`** — Pull-Fenster
  und Paginierungsgrenzen.
- **`claude_code`** (`false`) — auch den Claude Code Analytics Feed ziehen
  (geschätzte Kosten pro Entwickler nach Modell) für Chargeback.
- **`claude_code_shadow_auth`** (`true`) — bei aktivem Analytics-Feed jeden
  Entwickler flaggen, dessen Claude-Code-Nutzung als `customer_type=api` abgerechnet wird — ein
  persönlicher/API-Key **außerhalb des Org-Abonnements**, d. h. Identität und Spend,
  die auf einem ungoverned Key reiten. Setze `false` nur, wenn deine Org Claude Code
  absichtlich auf API-Abrechnung betreibt.
- **`gateway`** (`direct`) — die Deployment-Oberfläche, auf der diese Org läuft
  (`direct | claude-platform-aws | bedrock-mantle | bedrock-legacy | vertex |
  foundry`). Auf einer Oberfläche ohne die Admin API (Bedrock/Vertex/Foundry)
  **degradiert die Governance-Ingestion ehrlich mit einem Posture-Finding**, statt
  ein leeres Inventar vorzugeben.
- **`mcp_toolsets`** / **`server_tool_grants`** — operatordeklarierte
  Allow-Sets für API-getriebene Claude-Agenten (welche MCP-Tools, welche Anthropic-
  Server-Tool-Typen ein Agent verwenden *darf*). Jeder erlaubte Eintrag wird zu einer
  **Permitted Edge** in Modul III, gekreuzt gegen beobachteten Zugriff — der
  gleiche Permitted-vs-Observed-Diff wie überall sonst. Der `agent_ref` muss
  die externe ID des Agenten sein, wie sie zur Laufzeit entdeckt wird, sonst ist der Grant ein ehrlicher
  No-op statt ein falscher Treffer.

:::caution[Der Analytics-Feed hat eine benannte Grenze]
Der Claude Code Analytics Feed verfolgt Nutzung nur auf der **Claude API**.
Flotten auf Claude Platform on AWS, Bedrock, Gemini Enterprise Agent Platform (formerly Vertex AI) oder Microsoft Foundry sind
**nicht darin** — das Fehlen von Findings dort ist kein Beleg für das Fehlen. Für
diese Oberflächen ist das [OTel Plane](/de/how-to/claude-code-enterprise-otel/) die
Beobachtung, die du hast.
:::

## `claude-compliance`: der Evidenz-Feed und das Directory

```json
{
  "sources": [{
    "name": "anthropic-compliance",
    "kind": "claude-compliance",
    "tenant": "<tenant-id>",
    "config": {
      "api_key": "<activity-feed-key-reference>",
      "compliance_access_key": "<compliance-access-key-reference>"
    }
  }]
}
```

Zwei **eigene** Credentials, bewusst:

- **`api_key`** — ein Admin API Key mit `read:compliance_activities`; zieht
  den Activity-Feed (Evidenz-grade Events).
- **`compliance_access_key`** — ein separater Key mit
  `read:compliance_org_data` / `read:compliance_user_data`; aktiviert die Org-
  **Directory**-Ingestion (Orgs, Users, Rollen, Gruppen — einschließlich des
  SCIM-Provisioning-Signals, das die Admin API nicht sehen kann). Leer = Directory aus,
  deny-closed.

Der Lösch-Scope (`delete:compliance_user_data`, vom Right-to-Erasure-Pfad verwendet)
wird separat provisioniert und dual-control-gegated — dieser Read-
Connector hält ihn niemals.

## Was du in der Console sehen wirst

Abgerechneter und geschätzter Spend, aufgeschlüsselt nach den Dimensionen, die die Telemetrie trägt
(Team- und Projekt-Labels werden erstklassig), in **Cost & FinOps**; Org-
Mitglieder, nicht-menschliche Identitäten und ihre Scopes in **Identity & NHI**; Posture-
Findings (Shadow Auth, Surface-Degradation, WIF-Footguns) in **Security**:

<img class="light:sl-hidden" src="/console/finops-dark.png" alt="Die Cost-&-FinOps-Ansicht: Spend nach Modell und Dimension, mit Budgets und Alerts." />
<img class="dark:sl-hidden" src="/console/finops-light.png" alt="Die Cost-&-FinOps-Ansicht: Spend nach Modell und Dimension, mit Budgets und Alerts." />

## Ehrliche Grenzen

- **Die Kostenautorität ist der abgerechnete Bericht.** Nutzungsabgeleitete Zahlen sind
  Schätzungen und werden abgeglichen, niemals doppelt gezählt.
- **Das Admin Plane sieht Anthropic-betriebene Oberflächen.** Drittanbieter-gehostetes
  Claude (Bedrock/Vertex/Foundry) ist für es unsichtbar — explizit benannt via
  `gateway`, abgedeckt durch das OTel Plane.
- **`claude-console`-Posture-Findings enthalten einen blinden Fleck:** die Console
  kann nicht beobachten, ob SSO/SCIM upstream durchgesetzt wird — das Finding sagt das,
  statt zu raten.

## Verwandt

- [Enterprise-OTel für Claude Code](/de/how-to/claude-code-enterprise-otel/) —
  das Per-Session-Plane, das diese Org-Level-Feeds ergänzen.
- [Budgets & FinOps-Guardrails](/de/how-to/cookbook/budgets-and-finops-guardrails/)
  — den Kostenstream in durchgesetzte Limits verwandeln.
- [Connectoren & Coverage-Stufen](/de/reference/connectors/) — der vollständige Katalog.
