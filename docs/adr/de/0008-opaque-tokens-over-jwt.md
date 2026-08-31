> Maschinell übersetzt. Maßgeblich ist die englische Fassung.

# ADR-0008: Opake serverseitige Tokens statt JWT für First-Party-Authentifizierung

- **Status:** accepted
- **Datum:** 2026-06
- **Entscheider:** Olivares AI (durch adversariales Review bestätigt)
- **Referenzen:** API-/Authz-/Audit-Vertrag (§2, Entscheidung §13.2)

## Kontext und Problemstellung

Der First-Party-Authentifizierungsmechanismus musste gewählt werden. Der ursprüngliche Scope
erwähnte „Sessions/JWT“. Für ein Sicherheitsprodukt sind die Fehlermodi von Bearer-Credentials —
Widerruf, geheimnistragende Claims, Risiko der Parsing-Bibliothek — von großer Bedeutung.

## Entscheidungstreiber

- Sofortiger Widerruf.
- Keine Geheimnisse, die innerhalb des Tokens getragen werden.
- Minimale kryptografische Parsing-Angriffsfläche; standardmäßig sicher.

## Betrachtete Optionen

- **Opake serverseitige Tokens** (ein zufälliges Geheimnis, gehasht gespeichert, serverseitig nachgeschlagen).
- **JWT** für First-Party-Sessions.

## Entscheidungsergebnis

Gewählte Option: **opake serverseitige Tokens** für First-Party-Authentifizierung. Tokens werden
nach Zweck mit einem Präfix versehen (`olvs_` Session, `olvk_` API-Key); der Server speichert nur
einen öffentlichen Selektor und einen SHA-256 des Geheimnisses und vergleicht in konstanter Zeit.
JWT bleibt auf die SSO-/Federation-Schnittstelle beschränkt, nicht auf First-Party-Sessions.

### Konsequenzen

- **Gut:** Tokens sind widerrufbar, tragen keine Geheimnisse und benötigen kein Krypto-Parsing
  von durch Angreifer gelieferten Claims; standardmäßig sicher.
- **Schlecht / Abwägungen:** Die Validierung erfordert einen serverseitigen Lookup (für eine
  Control Plane akzeptabel).
- **Neutral:** Federation verwendet weiterhin JWT, wo das Protokoll es erfordert.

## Warum die Alternativen verworfen wurden

- **JWT für First-Party-Sessions** — schwer vor Ablauf zu widerrufen, neigt dazu, Claims zu
  tragen, und fügt Parsing-/Validierungs-Angriffsfläche ohne Nutzen hinzu.
