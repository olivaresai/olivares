---
title: Installer une licence et passer à Business
description: >-
  Où placer une licence achetée, comment l'installer sans redémarrer le moteur,
  comment vérifier ce qui est installé et comment effectuer sur place le passage
  de Community à Business. La vérification Ed25519 est hors ligne — aucun appel
  réseau n'établit le droit.
---

Vous avez acheté une offre et reçu une licence. Cette page explique quoi en faire : où placer
le fichier, comment l'appliquer à un moteur en cours d'exécution, comment lire ce qui est
installé et — si vous avez acheté une offre Business — comment remplacer le binaire
Community par le binaire commercial sans rien réinstaller.

:::note[Une licence est une attestation, pas un interrupteur à l'exécution]
**Elle ne verrouille aucune fonctionnalité du logiciel que vous exécutez.** Une licence expirée
ou absente ne désactive aucune fonctionnalité, et aucune licence ne plafonne les comptes
utilisateur — les utilisateurs auto-hébergés sont illimités dans chaque niveau. C'est une
déclaration signée de ce à quoi vous avez droit, pas une clé qui déverrouille du code déjà
présent sur votre disque.

**Ce qu'elle verrouille, en revanche, c'est l'ACCÈS AUX ARTEFACTS**, et cette distinction
constitue tout le modèle : une licence active est nécessaire pour télécharger le build Enterprise
et pour effectuer une installation depuis un bundle local (`olivares upgrade --bundle`) ; elle
est vérifiée hors ligne avec la clé intégrée à votre binaire. C'est pourquoi l'édition Enterprise
est un binaire différent que vous récupérez avec un jeton, au lieu d'un feature flag basculé dans
celui que vous possédez — et pourquoi vous dire qu'« elle ne verrouille rien » serait faux.
:::

## Ce que vous avez reçu

| Votre achat | Ce qui arrive | Ce que vous en faites |
|---|---|---|
| Community | rien à installer | déjà en cours d'exécution — rien sur cette page ne s'applique |
| Business / Business Max, auto-hébergé | un **fichier de licence** et un **jeton de téléchargement** | installez la licence, puis passez au binaire Enterprise |
| Cloud | les identifiants d'un tenant hébergé | rien à installer sur l'un de vos hôtes |

La licence est un blob signé unique. Enregistrez-le dans un fichier — `customer.license`, ou
n'importe quel autre nom — et conservez le jeton de téléchargement reçu dans le même e-mail :
ils servent à des étapes différentes et seule la licence est installée.

## 1 · Installer la licence

```sh
olivares license install ./customer.license --data-dir /var/lib/olivares
```

La commande **vérifie le blob avant d'écrire quoi que ce soit** avec la clé publique Ed25519
intégrée à votre build. Ainsi, un copier-coller tronqué échoue ici plutôt qu'au démarrage
suivant. En cas de succès, elle écrit `<data-dir>/license.key` avec le mode `0600` — la licence
canonique au repos que le moteur lit par défaut.

Passez `-` au lieu d'un chemin pour lire le blob depuis l'entrée standard :

```sh
pbpaste | olivares license install - --data-dir /var/lib/olivares
```

Installer par-dessus une licence existante la **remplace** atomiquement et indique laquelle a
été remplacée.

### L'appliquer à un moteur en cours d'exécution — sans redémarrage

Un moteur en cours d'exécution récupère la nouvelle licence sur place. Chacune de ces options
le fait :

```sh
kill -HUP "$(pidof olivares)"                 # signal the running process
curl -X POST .../v1/console/runtime/reload    # the API half
```

…ou utilisez la commande de rechargement propre à la console. Un redémarrage fonctionne aussi,
mais il n'est tout simplement pas nécessaire.

### Où le moteur cherche, dans l'ordre

Si vous injectez déjà la licence d'une autre manière, sachez que le fichier du répertoire de
données est la source de **plus faible priorité** parmi quatre. Le moteur les résout de la
priorité la plus élevée à la plus faible :

1. `--license <path>` (ou `LicenseFile` dans le fichier de configuration)
2. `OLIVARES_LICENSE_PATH=<path>`
3. `OLIVARES_LICENSE=<blob>` — la licence directement dans l'environnement
4. `<data-dir>/license.key` — ce que `license install` écrit

La commande `license install` **refuse** lorsqu'elle peut constater qu'un override est prioritaire
sur le fichier qu'elle s'apprête à écrire : installer sous cet override laisserait un fichier que le
moteur ne lit jamais, et vous verriez une sortie 0 sans aucun changement. La commande indique
quel override elle a trouvé, et `--force` prépare tout de même le fichier — le cas légitime étant
un override que vous êtes sur le point de retirer.

:::caution[Ce que ce refus peut voir — et ne peut pas voir]
La commande lit `OLIVARES_LICENSE_PATH` et `OLIVARES_LICENSE` **dans l'environnement de son propre
processus**. Elle ne peut pas voir un flag `--license` (ni une entrée de configuration `LicenseFile`) transmis
à un moteur qui fonctionne déjà dans un processus distinct — `install` et `uninstall` n'acceptent
d'ailleurs aucun flag `--license`. Ainsi, sur un hôte où le service a été démarré avec un chemin
explicite, les deux commandes peuvent réussir sans rien changer à ce que le moteur lit.

Exécutez `olivares license status` après chacune d'elles. La commande résout la licence selon la
même priorité que le moteur et indique quelle source est réellement en vigueur, ce qui est la
question qui compte.
:::

## 2 · Vérifier ce qui est installé

```sh
olivares license status --data-dir /var/lib/olivares
```

`status` fonctionne hors ligne et résout la licence selon la même priorité que le moteur. Il
répond donc à la question qui compte — *quelle licence est réellement en vigueur* — plutôt
qu'à « existe-t-il un fichier ? ». Il indique la source résolue, le titulaire, l'offre et la
date d'expiration.

Exécutez-le après chaque installation et après le retrait d'un override.

## 3 · Community → Business, sur place

Avec une licence installée, le binaire Enterprise n'est plus qu'à un téléchargement. Rien
n'est réinstallé et aucune donnée n'est déplacée :

```sh
olivares upgrade --enterprise --token <TOKEN>
```

La commande récupère le build Enterprise signé pour votre plateforme et **vérifie la signature
hors ligne** — un artefact altéré interrompt la mise à niveau en laissant intact le binaire en
cours d'exécution — puis le remplace atomiquement en conservant une sauvegarde du précédent.
Utilisez d'abord `--check` si vous souhaitez voir le plan sans l'appliquer :

```sh
olivares upgrade --enterprise --token <TOKEN> --check
```

Redémarrez le service, puis activez les add-ons :

```sh
olivares enterprise enable <preset>     # starter | regulated | full
```

L'activation est encadrée et auditée : elle vous montre d'abord un diff et place en attente
tout add-on nécessitant un secret ou une revue au lieu de l'activer à moitié.
`olivares enterprise status` indique ce qui est actif. Ces commandes existent **uniquement
dans le binaire Enterprise** — si `olivares enterprise` n'est pas une commande, vous utilisez
encore le build Community et le remplacement ci-dessus n'a pas encore eu lieu.

:::caution[Sauvegardez avant le remplacement]
Le remplacement porte sur un binaire, pas sur vos données — mais effectuez tout de même la
sauvegarde demandée par [Mettre à niveau et revenir en arrière](/fr/how-to/upgrade-and-rollback/).
Cette page explique aussi comment revenir au binaire précédent.
:::

## Retirer une licence

```sh
olivares license uninstall --data-dir /var/lib/olivares --yes
```

La commande supprime `<data-dir>/license.key` et indique ce qu'elle a retiré. Comme `install`,
elle **refuse** tant qu'elle peut voir un override `OLIVARES_LICENSE*` — le fichier n'est pas ce
qui est en vigueur, donc sa suppression ne changerait rien — et présente le même angle mort : un
flag `--license` transmis à un moteur exécuté dans un processus distinct lui est invisible. C'est
la moitié hors ligne du propre `DELETE /v1/console/license` de la console.

Retirer la licence ne désactive **rien** de ce que vous exécutiez. Cela retire l'attestation ;
le binaire Enterprise continue de se comporter comme le binaire Enterprise jusqu'à ce que
vous reveniez au précédent.

## Ce qui ne figure *pas* sur cette page

- **Émettre des licences** (`license keygen` / `sign`) est le volet fournisseur de la même
  commande. Vous n'en avez pas besoin en tant que client.
- **Ce que contient chaque offre** se trouve dans les pages de tarification, pas ici.
- **Comment fonctionne le modèle** — pourquoi un abonnement donne accès aux artefacts plutôt
  que d'agir comme un interrupteur — est expliqué dans [Open core et licences](/fr/explanation/open-core-and-licensing/).
