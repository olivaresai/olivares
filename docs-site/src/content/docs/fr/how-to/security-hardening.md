---
title: "Durcir un déploiement"
description: >-
  Étapes opérateur pour exécuter Olivares AI en toute sécurité : conserver les
  valeurs par défaut sécurisées, gouverner les actions destructrices par des
  approbations human-in-the-loop, vérifier une version avant de l'exécuter, et
  garder vos preuves hors de la machine. Posture défensive, par conception.
---

C'est le **guide de durcissement de l'opérateur** : les étapes concrètes pour exécuter le control
plane en toute sécurité. Il s'appuie *par-dessus* les pages explicatives — le
[modèle de sécurité](/fr/explanation/security/security-model/) et le
[modèle de menace](/fr/explanation/security/threat-model/) expliquent les actifs, les frontières de
confiance et pourquoi la posture est ce qu'elle est. Cette page est le *comment*.

:::note[Défensif par conception]
Olivares AI est un produit défensif. Il vous aide à **gouverner votre propre estate** ; ce n'est pas
un framework de commandement et contrôle et il ne scanne les credentials de personne d'autre. Lire
l'access map est une action privilégiée, cloisonnée par tenant et **auditée** (rôle editor et au-dessus,
jamais le viewer le plus bas). Ce guide durcit le déploiement — il ne vous apprend pas à cartographier un
estate qui ne vous appartient pas.
:::

## 1. Conserver les valeurs par défaut sécurisées

Une installation neuve est sécurisée par défaut. Le travail ici consiste surtout à *ne pas l'affaiblir*.

| Par défaut | Conservez-le car | Action opérateur |
|---|---|---|
| **Pas de credentials par défaut** | Le piège n°1 du self-hosted. Le premier démarrage émet un **jeton de configuration à usage unique** ; vous créez le premier administrateur avec lui. | Lisez le jeton dans la sortie de démarrage (ou les logs du conteneur), créez l'admin, après quoi il est consommé. N'incorporez jamais un credential dans une image. |
| **TLS activé par défaut** | Les canaux collector→core et user→panel transportent des métadonnées sensibles. | Laissez le TLS activé. `--insecure` (texte clair) est réservé au **développement sur localhost** — jamais sur un bind exposé. |
| **Bind loopback** | Le moteur se lie au loopback par défaut afin de ne jamais être exposé accidentellement. | Exposez-le **délibérément**, derrière votre propre ingress/TLS. Dans les conteneurs, le processus se lie à l'intérieur du conteneur et la stack Compose mappe le port de l'hôte sur le loopback — voir [self-hosting](/fr/how-to/self-hosting/). |
| **Pas de telemetry-home** | Un outil de sécurité qui téléphone à la maison est une responsabilité. | Aucune action — le moteur ne fait aucun appel sortant obligatoire au démarrage. En mode air-gapped, l'egress est nul. |

Chaque écart dangereux par rapport aux valeurs par défaut est un **opt-in nommé et explicite** (par
exemple le drapeau texte-clair de développement, ou l'autorisation d'un rôle de base de données
privilégié). Si vous n'en avez défini aucun, il est désactivé. La posture complète des valeurs par défaut
sécurisées et les garanties cryptographiques du ledger d'audit se trouvent dans le
[modèle de sécurité](/fr/explanation/security/security-model/).

### TLS mutuel pour les collectors distants

Dans la topologie distribuée, les collectors en périphérie poussent des observations vers le core via un
**TLS mutuel** à certificat-client vérifié. Activez-le en donnant au core une CA cliente afin qu'il
**exige et vérifie** un certificat client :

```bash
./bin/olivares serve \
  --listen 127.0.0.1:8443 --grpc-listen 127.0.0.1:8444 \
  --grpc-client-ca /path/to/collector-ca.pem \
  --data-dir /var/lib/olivares
```

Les collectors s'exécutent sur **votre** infrastructure avec **aucun écouteur entrant** (un modèle
push pur), donc ils n'ajoutent aucun port ouvert à vos hôtes de production. Protégez et sauvegardez le
répertoire de données (permissions restrictives) — il contient la clé de signature d'audit et le
matériel TLS — et gardez une copie hors-machine de la clé publique d'audit.

## 2. Gouverner les actions destructrices par des approbations human-in-the-loop

Le control plane est gouverné par un cœur d'autorisation **deny-by-default** (RBAC, avec un
policy decision point Cedar/OPA optionnel en mode restrict-only qui ne peut que *retirer de l'accès*,
jamais l'élargir). Pour le modèle — rôles, le seam de politique et la garantie de décisions enregistrées
— voir [gouverner et approuver](/fr/how-to/govern-and-approve/). Les étapes opérationnelles :

1. **Câbler le gate d'approbation.** Toute action de module qui muterait votre infrastructure
   (un apply de déploiement, un déclenchement d'orchestration, une ouverture de voix) passe par un
   gate d'approbation human-in-the-loop qui ouvre une approbation gouvernée liée au plan exact,
   deny-closed et bornée dans le temps. Il est activé en fournissant la configuration du pont ;
   sans elle, ces actions restent deny-closed.
2. **Utiliser un compte de service approbateur dédié — jamais celui d'un humain.** Le composant qui
   *ouvre* les approbations doit s'exécuter sous son **propre compte de service qui n'est jamais dans le
   pool des approbateurs**. La séparation des tâches est appliquée côté moteur : l'identité qui a ouvert
   une requête ne peut pas la décider, et un jeton système ne peut pas approuver du tout. Si le compte de
   l'ouvreur est aussi un approbateur, vous créez un blocage de liveness — gardez-les donc séparés.
3. **Les approbateurs décident, le ledger se souvient.** Un humain autorisé approuve ou rejette ;
   la décision est ajoutée au ledger à altération détectable avec l'acteur réel dans la même
   transaction. Une requête expirée ne peut jamais recevoir de décision contraignante. Vous ne pouvez pas
   effectuer un changement gouverné que le ledger oublierait silencieusement.

Les routes d'approbation résident sous le namespace du module de gouvernance et sont soumises au
même RBAC deny-by-default et au même audit par lecture que tout le reste.

## 3. Vérifier une version avant de l'exécuter

Un control plane est un produit de sécurité — prouvez qu'une version est bien celle que le projet a
publiée avant de l'exécuter. La chaîne complète (signature sur les sommes de contrôle, provenance SLSA,
attestations SBOM et OpenVEX, keyless en ligne ou entièrement hors ligne) se trouve dans
[vérifier ce que vous avez téléchargé](/fr/how-to/verify-a-release/). La seule règle qui n'a aucune
exception :

:::danger[Ne jamais `curl | bash`]
Ne canalisez pas un installateur dans un shell. Téléchargez les artefacts, **vérifiez-les**, et seulement
ensuite exécutez-les. Déployez les images de conteneur et le chart Helm **par digest**, jamais par un
tag mutable.
:::

## 4. Gardez vos preuves — et vos données — à votre périmètre

- **Exportez le ledger hors-machine.** Le ledger d'audit append-only, hash-chaîné et signé Ed25519 est
  exposé sous forme d'export **pull** authentifié dans plusieurs formats SIEM, afin que votre
  SIEM ou store WORM conserve une copie immuable qui re-vérifie la chaîne hors ligne. La
  copie hors-machine est le véritable contrôle contre un hôte entièrement compromis — voir
  [transférer l'audit vers Splunk](/fr/how-to/forward-audit-to-splunk/).
- **Pas de télémétrie obligatoire ni de sortie du plan de contrôle par défaut.** Le data plane
  (les collectors) s'exécute toujours sur votre infrastructure, et l'access map stocke des
  **relations, jamais de payloads, de secrets ou de PII** — la donnée minimale est une propriété
  du fil, pas un réglage. Ne franchit votre périmètre que ce que **vous** configurez à cette fin :
  les appels à vos API de modèles, les sorties SIEM/webhook que vous raccordez (dont l'export
  hors-machine ci-dessus) et, si vous en provisionnez un, un fournisseur externe d'embeddings.
  C'est l'argument structurel pour la résidence des données, le RGPD et l'exploitation air-gapped ;
  il décrit l'architecture et votre configuration, **ce n'est pas une garantie**.

## Voir aussi

- [Modèle de sécurité](/fr/explanation/security/security-model/) — privilège, cloisonnement par tenant, self-audit, donnée minimale.
- [Modèle de menace](/fr/explanation/security/threat-model/) — actifs, frontières de confiance et ce que chaque palier de couverture peut attester.
- [Gouverner et approuver](/fr/how-to/govern-and-approve/) — le modèle RBAC/PDP et le workflow d'approbation en détail.
- [Vérifier ce que vous avez téléchargé](/fr/how-to/verify-a-release/) — la chaîne complète de vérification d'une version.
- [Self-hosting](/fr/how-to/self-hosting/) et [installation air-gap](/fr/how-to/air-gap-install/) — les topologies de déploiement.
