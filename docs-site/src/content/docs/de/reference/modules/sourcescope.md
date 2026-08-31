---
title: "Quellen- & Credential-Scoping"
description: >-
  Bindet eine verbundene Quelle — einen MCP-Server, ein Modell, einen Provider,
  eine Wissensdatenbank oder eine Datenquelle — an einen Workspace oder eine
  Agent-Gruppe und löst in dem Moment, in dem ein Agent oder eine Session danach
  greift, auf, ob der Akteur in Scope ist und welche Credential-Referenz gilt.
  Deny-closed by construction.
---

Quellen- & Credential-Scoping (`modules/sourcescope`) beantwortet zur
Laufzeit eine einzige Frage: Wenn ein Agent oder eine Session nach einer
verbundenen Quelle greift — einem MCP-Server, einem Modell, einem Provider, einer
Wissensdatenbank oder einer Datenquelle — **ist dieser Akteur in Scope, und welche
Credential-Referenz gilt?** Es ist **LIVE**: Die Bindungstabelle, ihre Write-API
und der Resolver, den die Laufzeit-PEPs aufrufen, sind alle im Binary enthalten.

Es ist ein Modul statt einer Spalte, weil der Scope, den es durchsetzt, keine
Eigenschaft einer einzelnen Quellentität ist — MCP-Konfiguration, Modelle,
Provider und Wissensdatenbanken leben in verschiedenen Modulen, und nur die
Agent-/Session-/Ressourcen-Achse trägt überhaupt einen Workspace. Der Scope ist
eine **Bindung**: `(source) → (workspace or agent-group)`, mit einer optionalen
gescopten Credential-Referenz. Dieses Modul besitzt diese Bindungstabelle und den
Resolver.

## Die Bindung und ihre API

`/v1/m/sourcescope/bindings` ist eine standardmäßige CRUD-Fläche, gegatet durch
`sourcescope:binding:read` und `:binding:write`. Eine Bindung adressiert einen
Quellentyp (`mcp`, `model`, `provider`, `knowledge`, `data`) und einen Scope-Baum
(`workspace`, `agent_group`) und trägt eine **wertfreie `CredRef`** — einen
logischen Namen, einen `ref_kind`-Locator (`env`, `vault`, `secret_manager`,
`file`, `other`) und einen optionalen maskierten Hinweis. Kein Feld kann ein
verwendbares Geheimnis halten; der Handler weist ein Inline-Credential zurück —
dieselbe Minimaldaten-Invariante wie `capabilities.mcp_config.secret_refs`.

## Wie der Resolver entscheidet

Die Entscheidung ist deny-closed und komponiert, keine zweite
Autorisierungs-Engine:

- **Containment** — eine an Workspace W gebundene Quelle ist von einem Agenten oder
  einer Session in W ohne weitere Konfiguration auflösbar.
- **Grant** — ein [`x-models`](/de/reference/modules/x-models/)-spannender, gescopter
  Cedar-Grant aus [`vi-governance`](/de/reference/modules/vi-governance/) öffnet einen
  fremden Workspace.
- **RBAC** — tenant-weite Autorität sieht weiterhin alles; der Workspace ist eine
  Soft-Isolation, der Tenant ist die harte Grenze.
- **Forbid** — ein gescopter Cedar-Forbid übersteuert alles Vorstehende.

Das Gate ist **additiv**: Eine ungebundene Quelle bleibt aus Gründen der
Rückwärtskompatibilität global; eine gebundene Quelle ohne enthaltenden Scope, ohne
Grant und ohne RBAC wird **abgelehnt**. Der Resolver ist als `ScopeGate` an der
Model-Execute-Chain und am Retrieval von
[`viii-knowledge`](/de/reference/modules/viii-knowledge/) verdrahtet.

## Bounded Context, klar benannt

- Dies ist **ausschließlich Referenz-Binding**. Die **Nutzung** gescopter
  Credentials in einem tatsächlichen Provider-Aufruf und ein Laufzeit-**MCP-Broker**,
  der einen Server im Auftrag eines Agenten anwählt, **existieren noch nicht im
  Baum** — der Resolver gibt die in-Scope-Referenz zurück, aber nichts hier
  verwendet sie, um einen ausgehenden Aufruf zu authentifizieren.
- Der Scope des Akteurs stammt aus dem Agenten/der Session, **die durch die
  Akteur-Referenz des Callers benannt** ist. Die Scope-Werte werden aus der
  gespeicherten Zeile gelesen (ein Caller kann keinen Workspace injizieren), aber die
  Wahl des Agenten liegt beim Caller; diese Referenz an den Principal zu binden, ist
  ein Hardening-Follow-up. Siehe [Ehrlichkeit und Grenzen](/de/start/honesty-and-limits/).

## Verwandt

- [Governance (vi)](/de/reference/modules/vi-governance/) — die Cedar-Grant/Forbid-
  Algebra und das RBAC, das der Resolver komponiert.
- [Models (x)](/de/reference/modules/x-models/) — die Execute-Chain, in der das
  `ScopeGate` läuft.
- [Knowledge (viii)](/de/reference/modules/viii-knowledge/) — gouverniertes Retrieval,
  der zweite Ort, an dem der Resolver gatet.
