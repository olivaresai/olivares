---
title: "Fine-tuning de modèles propres et inférence locale — exécution (planifié)"
description: >-
  Ce qui reste planifié côté modèles propres : que la plateforme exécute elle-même les
  jobs de fine-tuning et serve elle-même l'inférence locale. Le registre de modèles
  propres, l'admission des modèles signés, les enregistrements de lignage et les preuves
  AIBOM sont déjà livrés en tant qu'opérations des modèles ; cette page est honnête sur
  la moitié exécutante qui ne l'est pas.
---

L'histoire des modèles propres — gouverner **les modèles que l'entreprise entraîne ou
héberge elle-même** — se divise en deux moitiés, et une seule reste planifiée.

La **moitié gouvernante est livrée aujourd'hui** sous la forme du
[Module XXIII — opérations des modèles](/fr/reference/modules/xxiii-model-operations/) : un
**registre versionné de modèles propres** (`hosted`, `fine_tuned`, `imported`), la porte
d'**admission** des modèles signés, des **enregistrements de lignage des datasets et des
jobs de fine-tuning**, des **enregistrements gouvernés de déploiements d'inférence
locale** (vLLM, Ollama, llama.cpp, autres) avec re-vérification enforce-signed au moment
du déploiement, et la génération d'**AIBOM / model card** avec scellement ancré au
ledger. Ses entités et endpoints sont déclarés et servis sous les routes de module bêta
(`/v1/m/models/owned-models`, `/v1/m/models/model-versions`,
`/v1/m/models/finetune-jobs`, `/v1/m/models/inference-deployments`,
`/v1/m/models/aiboms`, …) — voir la [référence des routes de module](/reference/api-beta/).

Cette page suit la **moitié exécutante, planifiée et délibérément non construite** : que
la plateforme *exécute* elle-même ce travail.

## Ce qui est livré aujourd'hui (ailleurs)

La gouvernance des modèles propres est réelle et documentée sur la page
[opérations des modèles](/fr/reference/modules/xxiii-model-operations/) :

- un **registre de modèles propres** avec versions immuables, pour qu'un modèle
  fine-tuné ou auto-hébergé soit une entité gouvernée de premier rang plutôt qu'un
  endpoint non géré ;
- **des jobs de fine-tuning comme enregistrements de lignage** — inventaire du travail
  d'entraînement exécuté à l'extérieur et de la version de modèle produite par chacun ;
- **des déploiements d'inférence locale comme enregistrements gouvernés** — les runtimes
  de service que vous opérez, placés sous l'application d'admission (`require_signed`) et
  l'audit.

## Ce qui reste planifié

- **Exécuter les jobs de fine-tuning.** Le module livré enregistre l'état et le lignage
  du travail de fine-tuning exécuté ailleurs ; la plateforme ne démarre, n'annule ni
  n'exécute jamais un job d'entraînement, et ne stocke ni poids ni contenus de datasets.
  Un pipeline qui *exécute* le fine-tuning depuis la plateforme est un travail planifié.
- **Servir l'inférence locale.** Les déploiements sont des enregistrements gouvernés de
  runtimes que l'opérateur fait tourner ; la plateforme n'héberge ni ne sert l'inférence
  elle-même. Le service d'inférence locale de première partie est un travail planifié.

Pour cette moitié exécutante, aucun schéma de job, contrat de scheduler ou contrat de
runtime de service n'est déclaré, et cette page n'en invente délibérément aucun.

## Pourquoi planifié, et non livré

La plateforme est construite pour que toute capacité se rattache sans réarchitecturer le
reste ; l'exécution pourra donc s'ajouter plus tard au-dessus des surfaces de gouvernance
livrées. Elle a été placée **après** la v1 par une décision produit explicite : la
priorité de la première version est de gouverner les modèles et agents qu'une
organisation exécute déjà, et exécuter l'entraînement/le service ne change pas assez
cette valeur centrale pour concourir à l'effort v1.

Quand elle sera construite, sa couture naturelle est déjà livrée : un fine-tuning exécuté
produirait une **version** de modèle dans le registre des
[opérations des modèles](/fr/reference/modules/xxiii-model-operations/) et passerait la même
porte d'**admission** des modèles signés que tout artefact produit à l'extérieur, la
politique de la pile fournisseurs restant dans la
[gestion des modèles et fournisseurs](/fr/reference/modules/x-models/).

:::caution[Limites honnêtes]
- **Les surfaces gouvernantes sont livrées, les surfaces exécutantes ne le sont pas.**
  Ne lisez pas cette page comme un déni du registre, de l'admission, des enregistrements
  de lignage, de la gouvernance des déploiements ou des preuves AIBOM — ils existent et
  sont documentés dans les [opérations des modèles](/fr/reference/modules/xxiii-model-operations/).
- **Aucune surface d'exécution n'existe aujourd'hui.** Il n'y a ni pipeline
  d'entraînement, ni scheduler de jobs de fine-tuning, ni service d'inférence de première
  partie dans le binaire livré, et aucune entité, endpoint ou événement n'est déclaré
  pour eux — pas même une interface qui refuse.
- **Rien ici n'est une promesse de date ou de profondeur.** Le périmètre ci-dessus est la
  direction planifiée ; le schéma de jobs et les contrats de runtime seront conçus au
  moment de la construction. Ils restent délibérément non spécifiés plutôt que fabriqués.
:::

## Connexe

- [Module XXIII — opérations des modèles](/fr/reference/modules/xxiii-model-operations/) — la surface de gouvernance des modèles propres déjà livrée : registre, admission, lignage, déploiements, AIBOM.
- [Catalogue des modules](/fr/reference/modules/overview/) — les 30 modules livrés et où se situe le travail sur les modèles propres.
- [Module X — gestion des modèles et fournisseurs](/fr/reference/modules/x-models/) — le voisin livré qui gouverne la pile de modèles fournisseurs.
- [Honnêteté et limites](/fr/start/honesty-and-limits/) — le contrat observer-largement / agir-sur-un-sous-ensemble et ce que « planifié » signifie.
