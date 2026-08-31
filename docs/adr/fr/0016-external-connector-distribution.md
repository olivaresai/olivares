> Traduction automatique. La version anglaise fait foi.

# ADR-0016: Écosystème de connecteurs externes — SDK public, admission signée, distribution par releases/OCI, index vérifié et organisé

- **Status:** accepted
- **Date:** 2026-06-11
- **Décideurs :** Fran Olivares (périmètre v1 décidé le 2026-06-09)
- **Références :** `LICENSING.md` (frontière de licence), ADR-0007 (runtime go-plugin),
  ADR-0011 (AGPL/Apache/commercial), ADR-0015 (chaîne d'approvisionnement),
  `docs/contracts/S02-sdk-runtime-eventbus.md`,
  `docs/contracts/S142-external-connector-sdk.md`

## Contexte et énoncé du problème

Le SDK de connecteurs (`sdk`, `sdk/plugin`) a été conçu dès le premier jour pour
qu'un connecteur ne lie jamais le moteur AGPL (Apache-2.0, zéro dépendance,
transport de plugin gRPC — ADR-0007, ADR-0011), et l'ADR-0007 anticipait
explicitement que « des tiers peuvent livrer des connecteurs de manière
indépendante ». Mais aucun mécanisme n'existait : les modules du SDK sont sans
tag et consommés uniquement via le workspace du monorepo, la racine de
composition ne lance que les binaires de plugins **embarqués et de première
partie** (`go:embed`), `LoadSourcePlugin` exécute n'importe quel chemin qu'on lui
fournit **sans aucun contrôle d'intégrité ni de provenance**, et le catalogue du
module XIV n'organise que des entrées internes. « Mon équipe ou un partenaire
peut-il construire et publier un connecteur ? » n'avait pas de réponse.

Ouvrir l'écosystème ne peut pas signifier « l'hôte charge n'importe quel binaire
de type `.so` que pointe un opérateur » : c'est un produit de sécurité ; un
exécutable non signé et non attesté, câblé dans le plan d'observation, serait une
faille dans la chaîne d'approvisionnement.

## Facteurs de décision

- L'avantage concurrentiel d'amplitude **ne se compose** que si des tiers peuvent
  contribuer des connecteurs en toute sécurité (`ARCHITECTURE.md`, `LICENSING.md`).
- La frontière de licence (connecteur = Apache, n'importe jamais `/core`) doit être
  vérifiable **par le tiers**, et pas uniquement dans notre CI.
- La machinerie de signature + admission existe déjà et a fait ses preuves
  (admission de modèles, admission d'entrées MCP, `core/secure/modelsign`) :
  réutiliser, ne jamais réimplémenter.
- Aucune infrastructure de marketplace hébergée en v1 (décision commerciale différée).

## Options envisagées

- **Option A — service de marketplace hébergé** : un service de registre exploité
  par Olivares.AI avec téléversement/revue/diffusion.
- **Option B — SDK + certification + signature, distribution via GitHub
  releases/OCI, index statique organisé de « connecteurs vérifiés » dans le site
  de documentation ; admission signée deny-closed au niveau de l'hôte.**
- **Option C — chargement de plugins ouvert** (chemin fourni par l'opérateur, sans
  signature), certification sous forme de documentation uniquement.

## Résultat de la décision

Option retenue : **Option B** (décidée le 2026-06-09).

1. **Contrat SDK public.** `sdk` et `sdk/plugin` sont déclarés **stables v1** pour
   les auteurs de connecteurs, avec une politique explicite de
   versionnage/dépréciation (`sdk/VERSIONING.md`, exposée dans la page de
   stabilité du site de documentation). Les tags semver (`sdk/v1.*`,
   `sdk/plugin/v1.*`) arrivent avec la première publication publique du dépôt ;
   jusque-là, les auteurs épinglent un commit (le `-sdk-path` du scaffold couvre
   la boucle de développement).
2. **Scaffold + guide.** Un générateur sans dépendance
   (`sdk/scaffold`, CLI `olivares-connector-new`) produit un dépôt de connecteur
   hors arborescence complet — squelette source/sortie conforme au contrat, test
   de cycle de vie, `main` de plugin, README et un **contrôle de frontière
   autonome** (la même règle `go list -deps` que `scripts/check-boundary.sh`
   applique dans notre CI, de sorte que le tiers vérifie la frontière AGPL/Apache
   dans *sa propre* CI).
3. **Canal de distribution.** Un connecteur publié est livré sous forme
   d'**asset de release GitHub** (binaire + `sha256` + bundle d'attestation
   Sigstore) et/ou d'**artefact OCI** (ORAS, attestation en tant que referrer).
   Aucun marketplace hébergé en v1.
4. **Admission signée, deny-closed au niveau de l'hôte.** Un plugin externe ne
   s'exécute que si la configuration des sources de l'opérateur épingle son digest
   ET qu'une attestation de chaîne d'approvisionnement Sigstore/DSSE (provenance
   SLSA / prédicat SBOM) portant sur ce digest se vérifie selon une politique de
   confiance configurée par l'opérateur (`connector_trust`), en réutilisant
   `modelsign.VerifyAttestation`. Le loader épingle en outre la somme de contrôle
   au moment de l'exécution (`SecureConfig` de go-plugin). **Il n'existe ni mode
   observe ni échappatoire allow-unsigned pour les binaires externes** — la boucle
   de développement consiste à « signer avec sa propre clé, faire confiance à sa
   propre clé publique » (mode bare-key).
5. **Enregistrement de certification (overlay de catalogue).** Le module XIV gagne
   un type d'entrée `connector` avec sa propre paire d'admission
   (`catalog.connector_admission_policy` / `catalog.connector_admission`) :
   verdicts de provenance/SBOM vérifiés par entrée, porte d'approbation
   deny-closed, mode observe par défaut — la piste de certification destinée au
   locataire, découplée de la porte d'exécution de l'hôte (défense en profondeur,
   à l'image de la paire admit-route + deployment-gate de l'admission de modèles).
6. **Index des connecteurs vérifiés.** Une **page statique organisée** dans le site
   de documentation (`reference/verified-connectors`) liste les connecteurs tiers
   dont les mainteneurs ont re-vérifié la release (frontière, signature,
   provenance, revue de données minimales). L'inscription se fait par pull
   request ; l'index est une documentation de la vérification effectuée, et **non**
   une racine de confiance — les opérateurs épinglent toujours
   l'identité/la clé de l'éditeur dans `connector_trust`.

### Conséquences

- **Avantages :** les tiers construisent, signent et livrent des connecteurs sans
  toucher au moteur AGPL ; l'hôte n'exécute jamais de code non attesté ; la
  certification réutilise une machinerie éprouvée ; aucun nouveau service à exploiter.
- **Inconvénients / compromis :** aucune UX de découverte/installation au-delà de la
  documentation + releases (un marketplace hébergé en fournirait une) ; les
  opérateurs gèrent les ancres de confiance à la main ; les connecteurs **de sortie**
  externes se construisent et se livrent de la même façon, mais le câblage externe
  côté hôte couvre d'abord les sources d'observation (la composition notify n'a
  pas encore de chemin pour les plugins externes).
- **Neutre / suites :** le *pull* OCI par l'hôte (aujourd'hui l'opérateur place le
  binaire sur disque ; l'épinglage du digest rend le transport sans incidence sur
  la confiance) ; les modules hors-processus restent non câblés ; une capacité de
  conformité sondée à partir des admissions de connecteurs ; le scope npm
  `@olivaresai` et les tags du module-proxy lors de l'export public.

## Pourquoi les alternatives ont été rejetées

- **Option A** — exploiter un marketplace est un engagement commercial qui a été
  explicitement différé ; cela ajoute un service critique pour la confiance sans
  aucune demande en v1.
- **Option C** — « charger n'importe quel binaire » est exactement la faille de
  chaîne d'approvisionnement que ce produit existe pour combler ; une
  certification sous forme de prose sans application serait du théâtre
  design-for-audit (`docs/SECURITY-HARDENING.md`).
