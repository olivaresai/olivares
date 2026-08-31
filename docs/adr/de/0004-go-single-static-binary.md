> Maschinell übersetzt. Maßgeblich ist die englische Fassung.

# ADR-0004: Engine in Go, ein einziges statisches Binary mit eingebettetem Web

- **Status:** accepted
- **Date:** 2026-06
- **Deciders:** Fran Olivares
- **References:** stack decisions register (T1, T5); stack architecture

## Kontext und Problemstellung

Eine selbst hostbare, air-gap-freundliche Security-Control-Plane muss trivial zu
deployen, im eBPF/OpenTelemetry/Cloud-native-Ökosystem heimisch und als ein einziges
Artefakt auslieferbar sein. Die Sprache der Engine und die Art der Auslieferung des UI
ergeben sich beide daraus.

## Entscheidungsfaktoren

- Ein einziges, in sich geschlossenes Artefakt für Self-Hosting und Air-Gap.
- Natives eBPF und eine ausgereifte Modul-/Plugin-Runtime.
- Starke Nebenläufigkeit für Ingest und den Event-Bus.

## Betrachtete Optionen

- **Go**, ein einziges statisches Binary, Web eingebettet über `go:embed`.
- **Rust**-Engine.
- **Node/TypeScript**-Engine.
- **Separate SPA** (zwei Artefakte) statt eines eingebetteten UI.

## Ergebnis der Entscheidung

Gewählte Option: **Go**, kompiliert zu einem einzigen statischen Binary, mit dem
React-Web-UI **eingebettet über `go:embed`** und vom selben Origin wie die API
ausgeliefert — sodass das gesamte Produkt **eine Datei** ist.

### Konsequenzen

- **Gut:** ein Artefakt zum Ausliefern, Verifizieren und Betreiben; natives eBPF;
  hervorragende Cloud-native-Eignung; Nebenläufigkeit, die zu Ingest passt.
- **Schlecht / Trade-offs:** das UI wird als Teil des Binary-Builds gebaut und eingebettet.
- **Neutral:** Node/TypeScript wird ausschließlich für das Web-UI verwendet, nicht für die
  Engine.

## Warum die Alternativen verworfen wurden

- **Rust** — langsamerer Build/Iteration und überdimensioniert für die Anforderungen von
  v1.
- **Node/TS-Engine** — schwache eBPF-Story und kein einziges statisches Binary, trotz
  Komfortzone.
- **Separate SPA** — zwei Artefakte zum Deployen und Versionieren; das eingebettete UI
  hält es bei einem.
