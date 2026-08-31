---
title: Construire et livrer un connecteur
description: >-
  Échafaudez, implémentez, testez, signez et distribuez un connecteur tiers avec
  le SDK de connecteur public sous Apache-2.0 — et câblez-le dans un control
  plane avec une admission signée deny-closed.
---

Ce guide vous mène de rien à un **connecteur tiers signé** qu'un opérateur peut
câbler dans le control plane. Le SDK de connecteur est sous Apache-2.0 et
n'importe rien du moteur AGPL, de sorte que votre connecteur est **votre** code
sous **votre** licence, construit dans **votre** dépôt.

Ce que vous construisez est un programme Go normal : un type implémentant
`sdk.SourceConnector` (recueille des faits, émet des observations) ou
`sdk.OutputConnector` (délivre des notifications), ou `sdk.ContentSource`
(sert des documents et des références d'ACL aux connaissances gouvernées),
empaqueté sous forme de
binaire [go-plugin](https://github.com/hashicorp/go-plugin) que le moteur lance
hors processus et avec lequel il dialogue via gRPC (loopback mutuellement
authentifié, AutoMTLS). Lisez d'abord [connecter une source](/fr/how-to/connect-a-source/)
pour le *modèle* de connecteur — observe-only, données minimales, les trois
sortes d'observations.

:::note[Stabilité]
Le contrat du SDK (`Descriptor/Open/Gather/Close`, le wire, le handshake du
plugin) est **stable en v1** — voir
[stabilité de l'API](/fr/reference/api-stability/) et `sdk/VERSIONING.md` dans le
dépôt. Tant que les premiers tags semver publics ne sont pas publiés, construisez
contre un checkout du dépôt (`-sdk-path` ci-dessous).
:::

## 1. Échafaudage

Interface privilégiée :

```sh
# from the repository checkout root
go run ./cmd/olivares connector init acme.widget-audit \
  --dir ~/olivares-connector-widget \
  --module github.com/acme/olivares-connector-widget \
  --template access-edge-source \
  --plugin \
  --sdk-path "$PWD/sdk"
```

Choisissez l'un des cinq archétypes. Il s'agit de préréglages appliqués à des
surfaces stables du SDK, pas de nouveaux contrats d'auteur :

| Modèle | Surfaces déclarées | Cas d'usage |
|---|---|---|
| `content-source` | `knowledge.document` | Documents pour l'ingestion de connaissances gouvernée, y compris les sources de contenu hors processus. |
| `access-edge-source` | `observation.edge` | Faits sur le graphe d'accès, l'identité et les relations SaaS et infrastructure. |
| `output-sink` | `notify.sink` | Destinations de notification ou de ticketing. |
| `agent-surface` | `observation.edge`, `observation.finding` | Adaptateurs de runtime d'agent qui signalent des arêtes d'accès et des findings. |
| `model-provider` | `observation.cost`, `observation.edge` | Inventaire des fournisseurs et observations d'utilisation et de coût ; la gouvernance des modèles reste côté moteur. |

L'ancien outil d'échafaudage autonome reste valide et génère les mêmes contrats
d'auteur stables :

Exécutez ceci depuis un checkout du dépôt (tant que les premiers tags SDK
publics ne sont pas publiés, le package se résout via le workspace, et
`-sdk-path` pointe vers le `sdk/` de ce checkout) :

```sh
# from the repository checkout root
go run ./sdk/scaffold/cmd/olivares-connector-new \
  -dir ~/olivares-connector-widget \
  -name acme.widget-audit \
  -module github.com/acme/olivares-connector-widget \
  -kind source -plugin \
  -sdk-path "$PWD/sdk"
```

Vous obtenez un dépôt complet : le squelette du connecteur, un test de cycle de
vie, le `main` du plugin, un README avec tout ce cycle de vie, et
`scripts/check-boundary.sh` — la **même vérification de frontière de licence que
notre CI exécute**, pour la vôtre. `-name` est votre `Descriptor.Name` :
globalement unique, en notation pointée, `<vendor>.<connector>`.

## 2. Implémentation

Le contrat, en bref (le godoc sur `sdk.SourceConnector` fait foi) :

- **`Open`** lit la configuration (déclarée dans votre `Descriptor.ConfigFields` ;
  les secrets sont des *références*, marquées `Secret: true`, jamais inlinées).
  Échouez ici, pas dans `Gather`.
- **`Gather`** émet des observations vers le `Sink` du moteur. Le **moteur
  possède l'ordonnancement** : une source par lots fait son travail et retourne ;
  une source en streaming se bloque jusqu'à l'annulation de `ctx`. Ne possédez
  jamais votre propre ticker.
- La livraison est **at-least-once** ; les consommateurs déduplicent sur la clé
  naturelle de l'observation. Ne suivez pas l'état de livraison.
- **Données minimales** : émettez des références et des métadonnées, jamais des
  payloads, des prompts ou des valeurs secrètes.
- Pour `content-source`, **`List`** renvoie des références assez peu coûteuses à
  énumérer, **`Fetch`** renvoie le corps d'un document, et l'interface facultative
  `DeltaContentSource` ajoute les deltas live et l'actualisation des ACL. Les
  plugins de source de contenu qui implémentent cette interface facultative
  déclarent automatiquement `content.delta` ; les hôtes n'appellent pas les
  méthodes de delta si cette capacité n'a pas été déclarée.

Exécutez vos tests, puis prouvez la frontière de licence dans votre CI :

```sh
go test ./...
./scripts/check-boundary.sh   # fails if anything links github.com/olivaresai/olivares/core
```

## 3. Empaqueter et signer

Construisez le binaire du plugin, figez son digest, et attachez une attestation
de chaîne d'approvisionnement sous forme de **bundle Sigstore**. Le control plane
vérifie la provenance SLSA ou les attestations SBOM (prédicats SPDX / CycloneDX)
— signez avec votre propre clé (montré ici) ou sans clé avec votre identité CI :

```sh
go build -trimpath -o widget-audit ./cmd/acme-widget-audit
sha256sum widget-audit

# keyed (the dev loop: trust your own public key)
cosign generate-key-pair
cosign attest-blob --key cosign.key \
  --type slsaprovenance1 --predicate provenance.json \
  --bundle widget-audit.sigstore.json widget-audit

# keyless alternative (CI): same command with --yes and an OIDC identity,
# or GitHub artifact attestations (gh attestation download produces the bundle).
```

## 4. Distribution

Publiez une **release GitHub** avec le binaire, son `sha256` et le bundle
`.sigstore.json` — ou poussez les mêmes artefacts vers un registre OCI avec
`oras push` (l'attestation comme referrer). Versionnez avec semver ; déclarez la
`ProtocolVersion` contre laquelle vous avez été construit (v1 aujourd'hui) dans
votre README.

## 5. Exploitation (ce que font vos utilisateurs)

L'opérateur place le binaire et le bundle sur l'hôte et épingle **à la fois le
digest et la confiance** dans la configuration des sources
(`OLIVARES_SOURCES_CONFIG`) :

```json
{
  "connector_trust": {
    "trusted_keys": ["-----BEGIN PUBLIC KEY-----\n…acme's cosign.pub…\n-----END PUBLIC KEY-----\n"],
    "allowed_predicates": ["https://slsa.dev/provenance/v1"]
  },
  "sources": [
    {
      "name": "widget-prod",
      "tenant": "<tenant-id>",
      "config": { "endpoint_ref": "…" },
      "plugin": {
        "path": "/opt/olivares/plugins/widget-audit",
        "sha256": "<the released digest>",
        "bundle": "/opt/olivares/plugins/widget-audit.sigstore.json"
      }
    }
  ]
}
```

L'admission est **deny-closed, sans échappatoire** : aucune ancre de confiance,
aucun bundle, un digest qui ne correspond pas, un signataire non approuvé ou un
type de prédicat erroné signifient tous que la source **n'est pas câblée** (le
démarrage dit pourquoi). En cas de succès, le moteur re-hache le binaire à l'exec
(go-plugin `SecureConfig`) afin que les octets vérifiés soient les octets
exécutés, et le canal du sous-processus est épinglé en AutoMTLS.

Les plugins de source de contenu utilisent le même `connector_trust` racine et la
même forme `plugin { path, sha256, bundle }` par source dans le bloc de
configuration `documents`. Ce sont des sources de contenu hors processus de
première classe pour l'ingestion de connaissances.

Une ancre de confiance est **obligatoire** — un `connector_trust` sans
`trusted_roots` ni `trusted_keys` est refusé d'emblée. Pour une signature
**keyless**, l'ancre est la racine Fulcio (ou d'une CA privée), donc l'opérateur
définit `trusted_roots` (le PEM de la racine, par ex. issu de `cosign
initialize`) **plus** `allowed_identities` et `allowed_issuers` (les deux,
ensemble — l'identité SAN et l'émetteur OIDC que la signature doit porter) ;
seul `trusted_keys` est remplacé. L'exemple à clé nue ci-dessus est l'ancre la
plus simple.

## 6. Se faire certifier (optionnel mais recommandé)

Deux enregistrements complémentaires :

- **Certification dans le produit** — vos utilisateurs curent votre connecteur
  comme une entrée de catalogue (sorte `connector`, module XIV) et enregistrent
  un verdict d'admission de provenance/SBOM vérifié face à votre digest publié
  (`POST /entries/{id}/admit`) ; avec `require_signed` activé, l'approbation est
  deny-closed sur ce verdict. Voir
  [module XIV](/fr/reference/modules/xiv-catalog/).
- **L'index des connecteurs vérifiés** — soumettez votre connecteur pour
  référencement sur
  [Connecteurs vérifiés](/fr/reference/verified-connectors/) : les mainteneurs
  re-vérifient votre release (frontière, signature, provenance, revue des
  données minimales) et la référencent. L'index documente la vérification ; ce
  n'est **pas** une racine de confiance — les opérateurs épinglent toujours
  eux-mêmes *votre* identité/clé.

## Gouverné par construction

L'application des règles se fait côté moteur par construction : les connecteurs
ne lient aucun code de gouvernance et ne peuvent pas s'y soustraire. Le moteur
indexe les contrôles sur l'identité de source configurée (`source_type`,
`source_ref`), applique le cadrage de la source, l'intersection des ACL, le scan
DLP/de récupération, l'admission et l'audit, et traite `Descriptor.Surfaces`
uniquement comme une métadonnée indicative — jamais comme une entrée de décision.

Les connecteurs privés sont de première classe. Vous pouvez conserver un
connecteur dans votre entreprise, ne jamais le publier ni le référencer
publiquement ; il reste gouverné dès lors que l'opérateur épingle le condensé du
binaire et l'ancre de confiance. L'index des connecteurs vérifiés documente la
certification ; ce n'est pas une racine de confiance.

## Limites honnêtes (v1)

- Le câblage externe couvre les **sources d'observation** et les **sources de
  contenu** ; un connecteur de sortie se construit et se livre à l'identique,
  mais la composition de notification ne charge pas encore de plugins de sortie
  externes.
- Les **modules** hors processus ne sont pas disponibles (le proto est gelé, la
  colle hôte intentionnellement non câblée).
- Le type somme d'observation est **scellé** : vous émettez des edges, des
  échantillons de coût et des findings — avec des vocabulaires de chaînes
  ouverts — mais vous ne pouvez pas définir de nouvelles sortes d'observations.
