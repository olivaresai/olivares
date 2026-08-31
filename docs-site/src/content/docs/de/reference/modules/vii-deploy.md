---
title: "Modul VII — Deployment & Integration"
description: >-
  Das einzige Modul, das auf Ihre Infrastruktur wirkt: Es plant und steuert den
  deklarativen Lebenszyklus von Agenten und MCP-Servern sowie deren Anbindung an
  das Estate. Mutationen sind HITL-gegated, Dry-run-vor-Apply und reversibel — und
  das Live-Apply bleibt deny-closed (503), bis ein Executor bereitgestellt ist.
---

Modul VII ist das **einzige** Modul, das die Kundeninfrastruktur mutiert — jeder andere
Teil des Produkts ist read-first. Es stellt Agenten und MCP-Server bereit, aktualisiert
und stilllegt sie als **deklarative, versionierte, reversible** Operationen und deklariert
die Konnektivität sowie die referenzierte Identität, die ein Agent nutzt, um eine
Unternehmensressource zu erreichen. Da es handelt, ist seine Sicherheitslatte die höchste
im Produkt, und die Live-Aktuierung wird hinter einer deny-closed Naht zurückgehalten, bis
ein Operator sie explizit bereitstellt.

## Planen und steuern, dann (vielleicht) anwenden

Der Lebenszyklus ist `plan → apply → verify → retire` und gleicht einen **gewünschten**
Zustand gegen den **realen** ab. Die entscheidende Trennung ist **deklarieren ≠ mutieren**:

- Das **Deklarieren** des gewünschten Zustands — eine Definition erstellen, aktualisieren,
  zurückrollen (auch über die Manage-as-Code-Ressource `olivares_deployment`) — ist
  reine Control-Plane-Sache und **berührt niemals die Infrastruktur**.
- **`plan`** ist ein reiner Dry-run-Diff; **`verify`** prüft Drift und aktualisiert den
  Snapshot. Keines von beiden mutiert.
- **`apply` und `retire`** sind die einzigen mutierenden Operationen. Sie sind
  **zweiphasig** und **deny-by-default**: Phase eins berechnet den Diff und *fordert* eine
  an den Plan-Hash gebundene menschliche Freigabe an, ohne etwas zu verändern; Phase zwei
  schreitet nur fort, wenn die Freigabe `approved` ist **und** der Plan-Hash weiterhin
  übereinstimmt — jeder andere Zustand (pending, expired, rejected, kein Gate, veralteter
  Plan) wird abgelehnt und protokolliert. Eine erneute Spezifikation ändert den Hash und
  macht die Freigabe ungültig (Anti-TOCTOU).

Mutierendes apply/retire ist **standardmäßig nicht live**. Die Aktuierungsnaht
([`Executor`](/de/reference/modules/overview/)) ist deny-closed: Ohne bereitgestellten
Executor scheitern apply/retire/plan/verify **fail-closed mit einem `503`** — die Control
Plane kann den gewünschten Zustand deklarieren, aber nicht mit der realen Infrastruktur
abgleichen. Eine reale Engine (Tofu/Terraform, GitOps, Kubernetes, Docker, Nomad,
Crossplane) plus eine kurzlebige, pro Operation attestierte Credential-Quelle werden
**nur bei Operator-Konfiguration** eingebunden; ohne diese handelt das Modul niemals
stillschweigend.

## Entitäten und der deklarierte Vertrag

Das Modul deklariert vier namespaced Entitäten plus das Kern-`Deployment` als angewandten
Snapshot:

| Entität | Rolle |
|---|---|
| **definition** | gewünschter Zustand — gewünschte vs. angewandte Version, Spec-Hash, Verknüpfung zum Kern-`Deployment` |
| **revision** | append-only, unveränderliche Spec-Historie — die reversible Quelle für Rollback |
| **wiring** | die **erlaubte** Konnektivität `agent → resource`, die es deklariert (der Vertrag, den Modul III gegenüberstellt) |
| **operation** | append-only Change-Management-Ledger — Version, Plan-Hash, wer freigegeben hat, Ergebnis |

Die gewünschte Spec ist **typisiert und aus dem Struct re-serialisiert** (niemals ein
Operator-JSON-Round-Trip): unbekannte Felder werden abgelehnt, ein Inline-Credential-Guard
läuft, und eine Spec, die Credential-Material im Klartext trägt, wird **bei der
Deklaration abgelehnt**. Credentials reisen **nur per Referenz**
(`<scheme>:<locator>`, allow-listed Scheme) — eine Eigenschaft der Leitung, niemals ein
gespeichertes Secret.

## Was es auf dem Bus erzeugt (die PERMITTED-Seite von Modul III)

Modul VII schreibt niemals die Access Map; Modul III ist der einzige Schreiber ihrer Kanten.
Bei einem committeten `apply` veröffentlicht das Modul für jede Wiring ein Policy-Grant-Event
[`edge.observed`](/de/reference/events/) (`Source = policy`), das nur Referenzen und den Modus
trägt. Modul III gleicht es in die **PERMITTED**-Seite seines Permitted-vs-Observed-Diffs ab —
sodass das, was dieses Modul deklariert, exakt das ist, was Modul III gegen seine
Beobachtungen kontrastiert. Die Identität wird pro Agent über Governance gebunden: Eine feste,
eindeutige Non-Human-Identität ergibt eine `attributed`-Kante; eine geteilte oder fehlende
Identität wird als `approximate` gemeldet — **gekennzeichnet, niemals vorgetäuscht**.

:::caution[Ehrliche Grenzen]
- **Live-Apply ist eine deny-closed Naht.** Ohne bereitgestellten Executor liefern
  `apply`/`retire` (und `plan`/`verify`) einen klaren `503`. Das Modul plant, steuert,
  versioniert und deklariert heute den gewünschten Zustand; es gleicht erst dann mit der
  realen Infrastruktur ab, wenn ein Operator einen Executor einbindet — niemals
  standardmäßig, niemals ein stillschweigender No-op.
- **Freigabe und Attribution fallen ebenfalls sicher aus.** Ohne das Freigabe-Gate wird
  jede Mutation verweigert; ohne den Identity-Binder ist die Attribution einer Wiring
  herabgestuft, nicht erfunden. `Start()` warnt einmal pro nicht verkabelter Naht, sodass
  ein kaputtes Deployment sichtbar ist.
- **Das Stilllegen einer Wiring zieht ihre veröffentlichte PERMITTED-Kante nicht zurück.**
  Das Kantenmodell hat kein Verb zum Zurückziehen; die Wiring wird als revoked markiert und
  Modul III gleicht die Veraltung ab. Deklariert, nicht versteckt.
- **Die Backend-Tiefe variiert.** Über die Aktuierungs-Backends hinweg sind manche
  Observe-Pfade flacher als andere (z. B. Health auf Oberflächenebene bei bestimmten
  Runtimes); diese werden als ehrliche Lücken vermerkt, niemals als erfundenes In-Sync
  gemeldet.
:::

## Verwandt

- [Modulkatalog](/de/reference/modules/overview/) — die Trennung Govern/Observe vs. Actuate und die `503`-Naht.
- [Modul III — die Access Map](/de/reference/modules/iii-access-map/) — konsumiert die PERMITTED-Wiring, die dieses Modul deklariert.
- [Referenz Event-Bus](/de/reference/events/) — das `edge.observed`-Event und seine Minimal-Data-Payload.
- [Steuern und freigeben](/de/how-to/govern-and-approve/) — der HITL-Freigabe-Flow hinter jeder Mutation.
- [Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/) — was heute aktuiert wird und was nicht.
- [Architekturüberblick](/de/explanation/architecture/overview/) — wo Modul VII in der Management-Schicht sitzt.
