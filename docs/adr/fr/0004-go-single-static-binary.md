> Traduction automatique. La version anglaise fait foi.

# ADR-0004: Moteur en Go, un binaire statique unique avec le web embarqué

- **Status:** accepted
- **Date:** 2026-06
- **Deciders:** Fran Olivares
- **References:** registre des décisions de stack (T1, T5) ; architecture de la stack

## Contexte et énoncé du problème

Un control plane de sécurité auto-hébergeable et compatible air-gap doit être trivial à
déployer, natif de l'écosystème eBPF/OpenTelemetry/cloud-native, et livrable en un seul
artefact. Le langage du moteur et la façon dont l'UI est livrée découlent tous deux de là.

## Facteurs de décision

- Un artefact unique et autonome pour l'auto-hébergement et l'air-gap.
- eBPF natif et un runtime de modules/plugins mature.
- Une concurrence robuste pour l'ingestion et le bus d'événements.

## Options envisagées

- **Go**, binaire statique unique, web embarqué via `go:embed`.
- Moteur en **Rust**.
- Moteur en **Node/TypeScript**.
- **SPA séparée** (deux artefacts) au lieu d'une UI embarquée.

## Résultat de la décision

Option retenue : **Go**, compilé en un binaire statique unique, avec l'UI web React
**embarquée via `go:embed`** et servie depuis la même origine que l'API — de sorte que le
produit entier soit **un seul fichier**.

### Conséquences

- **Bon :** un seul artefact à livrer, vérifier et exécuter ; eBPF natif ; excellente
  adéquation cloud-native ; concurrence adaptée à l'ingestion.
- **Mauvais / compromis :** l'UI est construite et embarquée dans le cadre du build du
  binaire.
- **Neutre :** Node/TypeScript est utilisé pour l'UI web uniquement, pas pour le moteur.

## Pourquoi les alternatives ont été rejetées

- **Rust** — build/itération plus lents et surdimensionné pour les besoins de la v1.
- **Moteur Node/TS** — mauvaise prise en charge d'eBPF et pas de binaire statique unique,
  malgré une zone de confort.
- **SPA séparée** — deux artefacts à déployer et à versionner ; l'UI embarquée n'en fait
  qu'un.
