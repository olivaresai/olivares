---
title: "Ein Deployment härten"
description: >-
  Operator-Schritte, um Olivares AI sicher zu betreiben: die sicheren Defaults
  beibehalten, destruktive Aktionen mit Human-in-the-Loop-Genehmigungen governen,
  ein Release vor dem Betrieb verifizieren und Ihre Evidenz off-box halten.
  Defensive Haltung, by design.
---

Dies ist der **Härtungsleitfaden des Operators**: die konkreten Schritte, um die Control
Plane sicher zu betreiben. Er sitzt *auf* den erklärenden Seiten — das
[Sicherheitsmodell](/de/explanation/security/security-model/) und das
[Bedrohungsmodell](/de/explanation/security/threat-model/) erklären die Assets,
Vertrauensgrenzen und warum die Haltung so ist, wie sie ist. Diese Seite ist das *Wie*.

:::note[Defensiv by design]
Olivares AI ist ein defensives Produkt. Es hilft Ihnen, **Ihr eigenes Estate zu governen**;
es ist kein Command-and-Control-Framework und scannt niemandes fremde Credentials. Die
Access Map zu lesen ist eine privilegierte, tenant-gescopte, **auditierte** Aktion
(Editor-Rolle und höher, niemals der niedrigste Viewer). Dieser Leitfaden härtet das
Deployment — er bringt Ihnen nicht bei, ein Estate zu kartieren, das Ihnen nicht gehört.
:::

## 1. Die sicheren Defaults beibehalten

Eine frische Installation ist secure by default. Die Aufgabe hier besteht meist darin, sie
*nicht zu schwächen*.

| Default | Behalten, weil | Operator-Aktion |
|---|---|---|
| **Keine Standard-Credentials** | Der Self-Hosted-Footgun Nummer 1. Der erste Boot prägt ein **einmaliges, einmal verwendbares Setup-Token**; damit erstellen Sie den ersten Administrator. | Lesen Sie das Token aus der Boot-Ausgabe (oder den Container-Logs), erstellen Sie den Admin, dann ist es verbraucht. Backen Sie nie eine Credential in ein Image. |
| **TLS standardmäßig an** | Die Collector→Core- und User→Panel-Kanäle tragen sensible Metadaten. | Lassen Sie TLS an. `--insecure` (Klartext) ist **nur für localhost-Entwicklung** — nie auf einem exponierten Bind. |
| **Loopback-Bind** | Die Engine bindet standardmäßig Loopback, sodass sie nie versehentlich exponiert wird. | Exponieren Sie sie **bewusst**, hinter Ihrem eigenen Ingress/TLS. In Containern bindet der Prozess innerhalb des Containers, und der Compose-Stack mappt den Host-Port auf Loopback — siehe [Self-Hosting](/de/how-to/self-hosting/). |
| **Kein Telemetry-Home** | Ein Sicherheitstool, das nach Hause telefoniert, ist eine Haftung. | Keine Aktion — die Engine macht beim Boot keine verpflichtenden ausgehenden Aufrufe. Im Air-Gapped-Modus gibt es null Egress. |

Jede gefährliche Abweichung von den Defaults ist ein **benanntes, explizites Opt-in** (zum
Beispiel das Klartext-Entwicklungsflag oder das Zulassen einer privilegierten
Datenbankrolle). Wenn Sie keines gesetzt haben, ist es aus. Die vollständige
Secure-Defaults-Haltung und die kryptografischen Garantien des Audit-Ledgers stehen im
[Sicherheitsmodell](/de/explanation/security/security-model/).

### Mutual TLS für entfernte Collectors

In der verteilten Topologie pushen Edge-Collectors Beobachtungen über
**Mutual TLS** mit verifiziertem Client-Zertifikat an den Core. Schalten Sie es ein, indem
Sie dem Core eine Client-CA geben, sodass er ein Client-Zertifikat **verlangt und
verifiziert**:

```bash
./bin/olivares serve \
  --listen 127.0.0.1:8443 --grpc-listen 127.0.0.1:8444 \
  --grpc-client-ca /path/to/collector-ca.pem \
  --data-dir /var/lib/olivares
```

Collectors laufen auf **Ihrer** Infrastruktur mit **keinem eingehenden Listener** (ein
reines Push-Modell), sodass sie keine offenen Ports zu Ihren Produktionshosts hinzufügen.
Schützen und sichern Sie das Datenverzeichnis (restriktive Berechtigungen) — es hält den
Audit-Signing-Key und das TLS-Material — und halten Sie eine Off-Box-Kopie des
Audit-Public-Keys.

## 2. Destruktive Aktionen mit Human-in-the-Loop-Genehmigungen governen

Die Control Plane wird von einem **deny-by-default**-Autorisierungskern governt (RBAC, mit
einem optionalen restrict-only Cedar/OPA-Policy-Decision-Point, der nur Zugriff *wegnehmen*
kann, ihn nie erweitert). Für das Modell — Rollen, die Policy-Naht und die
Recorded-Decisions-Garantie — siehe [governen und genehmigen](/de/how-to/govern-and-approve/).
Die operativen Schritte:

1. **Das Approval-Gate verdrahten.** Jede Modul-Aktion, die Ihre Infrastruktur mutieren
   würde (ein Deployment-Apply, ein Orchestrierungs-Fire, ein Voice-Open), durchläuft ein
   Human-in-the-Loop-Approval-Gate, das eine governte Genehmigung öffnet, die an den exakten
   Plan gebunden, deny-closed und zeitlich begrenzt ist. Es wird durch die Bereitstellung
   der Konfiguration der Brücke aktiviert; ohne sie bleiben diese Aktionen deny-closed.
2. **Ein dediziertes Approver-Service-Konto verwenden — niemals das eines Menschen.** Die
   Komponente, die Genehmigungen *öffnet*, muss als ihr **eigenes Service-Konto laufen, das
   nie im Approver-Pool ist**. Die Funktionstrennung wird engine-seitig durchgesetzt: Die
   Identität, die eine Anfrage geöffnet hat, kann sie nicht entscheiden, und ein
   System-Token kann überhaupt nicht genehmigen. Wenn das Konto des Öffners auch ein
   Approver ist, erzeugen Sie einen Liveness-Deadlock — halten Sie sie also getrennt.
3. **Approver entscheiden, das Ledger erinnert sich.** Ein autorisierter Mensch genehmigt
   oder lehnt ab; die Entscheidung wird dem manipulationserkennbaren Ledger mit dem echten
   Akteur in derselben Transaktion angehängt. Eine abgelaufene Anfrage kann nie eine
   verbindliche Entscheidung erhalten. Sie können keine governte Änderung vornehmen, die das
   Ledger stillschweigend vergisst.

Die Approval-Routen liegen im Namespace des Governance-Moduls und unterliegen demselben
deny-by-default RBAC und derselben Auditierung pro Lesevorgang wie alles andere.

## 3. Ein Release verifizieren, bevor Sie es betreiben

Eine Control Plane ist ein Sicherheitsprodukt — beweisen Sie, dass ein Release das vom
Projekt veröffentlichte ist, bevor Sie es betreiben. Die vollständige Kette (Signatur über
die Checksummen, SLSA-Provenance, SBOM- und OpenVEX-Attestierungen, online keyless oder
vollständig offline) steht in [verifizieren, was Sie heruntergeladen haben](/de/how-to/verify-a-release/).
Die eine Regel, die keine Ausnahmen kennt:

:::danger[Niemals `curl | bash`]
Leiten Sie keinen Installer in eine Shell. Laden Sie die Artefakte herunter,
**verifizieren Sie sie**, und führen Sie sie erst dann aus. Deployen Sie Container-Images
und das Helm-Chart **per Digest**, niemals per mutable Tag.
:::

## 4. Ihre Evidenz — und Ihre Daten — an Ihrem Perimeter halten

- **Das Ledger off-box exportieren.** Das append-only, hash-verkettete,
  Ed25519-signierte Audit-Ledger wird als authentifizierter **Pull**-Export in mehreren
  SIEM-Formaten offengelegt, sodass Ihr SIEM- oder WORM-Store eine unveränderliche Kopie
  behält, die die Kette offline re-verifiziert. Die Off-Box-Kopie ist die echte Kontrolle
  gegen einen vollständig kompromittierten Host — siehe
  [Audit an Splunk weiterleiten](/de/how-to/forward-audit-to-splunk/).
- **Keine verpflichtende Telemetrie, standardmäßig kein Egress der Control Plane.** Die Data
  Plane (die Collectors) läuft immer auf Ihrer Infrastruktur, und die Access Map speichert
  **Relationen, niemals Payloads, Secrets oder PII** — Minimal-Data ist eine Eigenschaft der
  Leitung, keine Einstellung. Ihren Perimeter überschreitet nur, was **Sie** dafür
  konfigurieren: Aufrufe an Ihre Modell-APIs, die von Ihnen eingerichteten
  SIEM-/Webhook-Ausgaben (einschließlich des obigen Off-Box-Exports) und ein externer
  Embedding-Anbieter, falls Sie einen bereitstellen. Das ist das strukturelle Argument für
  Datenresidenz, DSGVO und Air-Gapped-Betrieb; es beschreibt die Architektur und Ihre
  Konfiguration und ist **keine Garantie**.

## Verwandt

- [Sicherheitsmodell](/de/explanation/security/security-model/) — Privileg, Tenant-Scoping, Self-Audit, Minimal-Data.
- [Bedrohungsmodell](/de/explanation/security/threat-model/) — Assets, Vertrauensgrenzen und was jede Coverage-Stufe attestieren kann.
- [Governen und genehmigen](/de/how-to/govern-and-approve/) — das RBAC/PDP-Modell und der Approval-Workflow im Detail.
- [Verifizieren, was Sie heruntergeladen haben](/de/how-to/verify-a-release/) — die vollständige Release-Verifikationskette.
- [Self-Hosting](/de/how-to/self-hosting/) und [Air-Gap-Installation](/de/how-to/air-gap-install/) — die Deployment-Topologien.
