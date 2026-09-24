---
title: Installer avec Homebrew
description: >-
  La coordonnée du cask Homebrew macOS pour Olivares AI, ce que le cask fait
  avec Gatekeeper, et l’état de publication du bump du tap v26.9.0.
draft: false
---

C’est le chemin macOS que `INSTALL.md` nomme recommandé. Il installe le
binaire `olivares` signé via le cask Homebrew et lève la quarantaine
Gatekeeper. Ce n’est pas le chemin des paquets Linux
([Installer depuis un paquet](/how-to/install-from-packages/)) ni Docker
([Déployer avec Docker](/how-to/docker-deployment/)).

:::note[Bêta — le cask v26.9.0 est publié]
Le témoin des surfaces d’installation enregistre Homebrew comme
**published** (`docs/releases/v26.9.0-install-surfaces.json`, mesuré le
2026-09-23T20:28:11Z) : `Casks/olivares.rb` du tap nomme la version 26.9.0 et quatre
archives de plateforme dont les SHA-256 sont ceux de la release. Le producteur est
`.goreleaser.yaml` `homebrew_casks:` ; le job de release met à jour le cask du tap. La
commande ci-dessous est la coordonnée que nomme `INSTALL.md` (`brew install olivaresai/tap/olivares`).
:::

## 1. Installer le cask

```sh
brew install olivaresai/tap/olivares
```

Homebrew vérifie chaque téléchargement de cask contre son SHA-256 enregistré
(`INSTALL.md`). Le cask installe le binaire signé et **lève la quarantaine
Gatekeeper**.

Les binaires Darwin sont signés par cosign (confiance de la chaîne
d’approvisionnement) et **ne sont pas encore notariés par Apple**. Un
téléchargement manuel de l’archive est mis en quarantaine ; le cask s’en
charge, ou vous le levez avec `xattr -d com.apple.quarantine olivares` comme
le montre `INSTALL.md` pour le chemin manuel.

## 2. Premier démarrage

```sh
olivares quickstart
```

Valeurs sûres : TLS actif, loopback, pas d’identifiants par défaut. Le moteur
imprime l’URL de la console et le jeton de configuration à usage unique.
Continuez avec [Votre première heure](/how-to/first-hour/).

Un estate synthétique éphémère (loopback, texte en clair) sert seulement à
regarder :

```sh
olivares serve --seed-demo --insecure --data-dir "$(mktemp -d)"
```

`--seed-demo` n’est pas une visite du produit. Voir
[Votre première heure](/how-to/first-hour/).

## Voir aussi

- [Auto-héberger le plan de contrôle](/how-to/self-hosting/) — autres formes d’installation.
- [Vérifier une version](/how-to/verify-a-release/) — cosign, SBOM, provenance.
- [Installer depuis un paquet](/how-to/install-from-packages/) — `.deb` / `.rpm` / `.apk` Linux.
