> Traduction automatique. La version anglaise fait foi.

# ADR-0007 : Runtime hors-processus pour modules/connecteurs via go-plugin (gRPC)

- **Status:** accepted
- **Date:** 2026-06
- **Deciders:** Olivares AI
- **References:** stack design (module runtime); license-boundary design

## Contexte et énoncé du problème

La plateforme doit permettre aux connecteurs et modules de première et de tierce partie de l'étendre
sans entraîner leurs arbres de dépendances dans le moteur, et sans contaminer l'écosystème
permissif des connecteurs avec la licence copyleft du moteur.

## Facteurs de décision

- Isoler les dépendances des connecteurs du build/SBOM du moteur.
- Un contrat stable et versionné à travers la frontière de processus.
- Garder propre la frontière Apache-2.0 des connecteurs (un connecteur ne lie jamais le moteur AGPL).

## Options envisagées

- **`hashicorp/go-plugin` via gRPC** pour les modules/connecteurs hors-processus, plus
  des modules de cœur (core) compilés in-process.
- **Plugins in-process uniquement** (paquet Go `plugin` ou compilé en dur).

## Décision retenue

Option retenue : **`hashicorp/go-plugin` (gRPC)** pour les connecteurs/modules hors-processus,
les connecteurs de première partie étant embarqués et lancés comme sous-processus isolés, et les modules
de cœur compilés en dur. Le SDK de connecteur est une interface Go plus un contrat gRPC/protobuf
versionné.

### Conséquences

- **Bon :** les dépendances d'un connecteur n'entrent pas dans le binaire/SBOM du moteur ; la
  frontière Apache/AGPL reste propre et est imposée en CI ; les tiers peuvent livrer des
  connecteurs de manière indépendante.
- **Mauvais / compromis :** un contrat gRPC à versionner et un saut IPC pour les composants
  hors-processus.
- **Neutre :** le binaire unique embarque toujours les connecteurs de première partie (isolés en
  sous-processus) afin de rester un seul artefact.

## Pourquoi les alternatives ont été écartées

- **In-process uniquement** — fait entrer les dépendances de chaque connecteur dans le moteur et rend
  la frontière de licence impossible à imposer mécaniquement.
