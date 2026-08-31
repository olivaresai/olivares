---
title: Open core et licences
description: >-
  Open core : le produit complet est sous AGPL-3.0-only, le SDK et les connecteurs
  sont sous Apache-2.0, et un petit volet entreprise additif est commercial. La
  build AGPL n'est jamais bridée pour vous pousser vers le payant, mais elle n'est
  pas identique à l'édition commerciale. Ce que cela implique pour l'auto-hébergement
  et les auteurs de connecteurs.
---

Olivares AI est un **open core**. Le **produit complet** est publié sous la GNU
Affero General Public License, et la build AGPL est la plateforme de gouvernance
tout entière — jamais bridée de l'intérieur pour vous pousser vers une édition
payante. Par-dessus vient un petit ensemble d'add-ons commerciaux **additifs** dans
`enterprise/`, construits uniquement avec `-tags enterprise` et absents du binaire
public. Une licence commerciale fournit l'exception légale au copyleft ; les
capacités `enterprise/` sont licenciées comme des **add-ons séparés et optionnels** —
si bien que les éditions ouverte et commerciale ne sont
**pas** identiques, sans que rien de publié en open ne soit jamais déplacé derrière
le mur (le modèle `ee/` de GitLab, pas un péage de fonctionnalités sur le cœur).

## La frontière de licence

Les licences suivent l'arborescence des sources. Chaque fichier porte un en-tête
SPDX, et la frontière est appliquée en CI (un connecteur ne peut jamais importer le
moteur) :

| Chemin | Licence | Ce que c'est |
|---|---|---|
| `core/` | **AGPL-3.0-only** | le moteur : ingestion, bus d'événements, modèle de données, runtime des modules, API, authz, audit |
| `modules/` | **AGPL-3.0-only** | les 30 modules (inventaire, la carte R/RW, FinOps, évaluations, garde-fous, …) |
| `web/` | **AGPL-3.0-only** | l'interface React |
| `sdk/` | **Apache-2.0** | les interfaces connecteur/module, le contrat gRPC et les types partagés |
| `connectors/` | **Apache-2.0** | les connecteurs (Claude, OpenAI, pgAudit, eBPF, cloud, Slack, SIEM, …) |
| `enterprise/` | **commercial** | modules complémentaires additifs, gardés par tag de build, jamais dans le binaire public : fédération multi-IdP, pare-feu de contenu/DLP, durcissement des hooks, catalogue compilé de renseignement sur les menaces, contrôle de l'egress des outils serveur, CyberArk Conjur, bouclage des incidents (`LicenseRef-Olivares-Commercial`) |

Le site de documentation que vous lisez fait partie du produit AGPL.

## Ce que cela signifie pour vous

- **Auto-héberger le produit (AGPL).** Vous pouvez exécuter, étudier, modifier et
  redistribuer le produit complet sous l'AGPL. La clause d'usage en réseau de l'AGPL
  s'applique : si vous proposez une version modifiée à des tiers via un réseau, vous
  devez leur proposer vos sources modifiées. Pour un auto-hébergement interne, c'est
  rarement un problème ; si vous voulez construire un produit *par-dessus* Olivares AI
  sans cette obligation, la licence commerciale existe précisément pour cela.
- **Construire des connecteurs (Apache-2.0).** Le SDK et les connecteurs sont sous
  **Apache-2.0** — permissive, sans copyleft. Vous pouvez écrire un connecteur, le
  garder propriétaire et le distribuer comme bon vous semble. La frontière
  architecturale qui rend cela sûr est appliquée : un connecteur Apache-2.0
  **n'importe jamais le moteur AGPL** ; il ne dépend que du SDK. Cela maintient
  l'écosystème de connecteurs libre de toute friction copyleft.
- **Une licence commerciale.** Les organisations qui doivent éviter les obligations
  de l'AGPL (par exemple, intégrer le produit dans une offre propriétaire) peuvent
  obtenir une licence commerciale — contact : **enterprise@olivares.ai** (tarifs
  sur demande). Les modules complémentaires additifs `enterprise/` ci-dessus sont
  licenciés séparément, chacun comme un droit optionnel.

## Ce qui est ouvert et ce qui est entreprise

Le binaire ouvert est la plateforme de gouvernance tout entière ; le volet
`enterprise/` est **additif**. Deux frontières méritent d'être signalées, car la
build ouverte y répond honnêtement au lieu de faire semblant :

- **SSO** — l'authentification à IdP unique (OIDC + SAML 2.0) est **ouverte** dans le
  binaire par défaut : connexion réelle, sans `-tags enterprise`. Les IdP actifs
  multiples (par tenant / par domaine), l'application du SSO et le SCIM managé
  constituent le volet entreprise réservé ; activer un second IdP actif renvoie
  `multi_idp_requires_enterprise`.
- **Comptes utilisateurs** — **illimités dans toutes les éditions**. La build
  communautaire n'a aucun plafond d'utilisateurs, la build entreprise non plus : aucun
  état de licence (valide, expirée, absente) ne peut limiter le nombre de comptes d'un
  déploiement. Le plafond de trois comptes actifs antérieur au 2026-07-27 a été
  supprimé ; la couture de sièges reste dans le code comme un no-op de compatibilité
  qui ne refuse rien, et l'expiration d'une licence ne plafonne, ne désactive ni ne
  supprime jamais un compte.

Voir [Honnêteté et limites](/fr/start/honesty-and-limits/) pour le tableau complet de
ce qui est ouvert et de ce qui est entreprise.

## La clé de licence ne bride jamais le produit ouvert

C'est important et délibéré : dans le binaire ouvert (AGPL), la validation de licence
est une **simple attestation**. Le moteur enregistre qui détient une licence et son
statut ; il **ne désactive, ne dégrade ni ne bloque jamais** aucune requête, aucun
module, ni le démarrage sur une vérification de licence, et il fonctionne **hors
ligne** (une signature Ed25519, sans serveur de licence), c'est pourquoi le produit
ouvert fonctionne en environnement isolé (air-gapped). Le seul endroit où la licence
est *consommée* plutôt qu'affichée est la build entreprise fermée, et uniquement pour
donner droit aux add-ons couverts par l'accord commercial, évalués add-on par
add-on — une décision locale dans
l'édition commerciale, jamais une vérification dans le binaire ouvert. Elle ne
plafonne jamais les utilisateurs : les comptes sont illimités dans toutes les
éditions. La build ouverte est donc réellement
complète et non plafonnée par licence ; ce qui diffère dans l'édition commerciale, ce
sont les modules complémentaires additifs `enterprise/`, pas une clé de licence qui
activerait des fonctionnalités à l'intérieur du même binaire.

## Pourquoi ce modèle

Brider le cœur a été rejeté : l'édition ouverte fait tout le travail — la boucle de
gouvernance complète sur un seul nœud — donc la plafonner en ferait un produit de
moindre qualité et éroderait la confiance. Le tout permissif (MIT/Apache sur le cœur)
donnerait le cœur sans assise commerciale. Les licences source-available non OSS
(BSL, SSPL et similaires) tueraient l'adoption open source qui est tout l'enjeu d'un
écosystème de connecteurs extensible. Le modèle est donc l'**open core** : un produit
copyleft complet et crédible à lui seul, un SDK permissif qui garde l'écosystème de
connecteurs sans friction, et un petit volet commercial **additif** de code nouveau
qui n'a jamais fait partie de la build ouverte — plus une exception commerciale
propre — *sans jamais dégrader ce que vous pouvez auto-héberger*.

## Contribuer

Les contributions sont acceptées selon les conditions de contribution du projet (le
dépôt fournit à la fois un DCO et un CLA, ainsi qu'une politique de marques). Voir le
guide `CONTRIBUTING` du dépôt pour le processus en vigueur.

## Voir aussi

- [Installer une licence et passer à Enterprise](/fr/how-to/install-a-license/) — où placer
  une licence achetée et comment effectuer sur place le passage de Community à Enterprise.
  Cette page explique le modèle ; l'autre détaille les étapes.
- [Modèle de sécurité](/fr/explanation/security/security-model/) — pourquoi une licence
  par simple attestation compte pour un produit de sécurité en environnement isolé.
