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

:::note[La licence, et non le binaire, active les fonctionnalités commerciales]
**Le build Business n'exécute une fonctionnalité commerciale que tant qu'une licence valide
dont les droits l'incluent est installée.** Il vérifie la licence hors ligne, sur la machine, à
chaque utilisation. Sans licence, ou avec une licence expirée ou révoquée, le nouveau travail de
cette fonctionnalité répond `addon_requires_license` ; lire, vérifier et exporter vos données
continuent de fonctionner, et aucune licence ne plafonne les comptes utilisateur — les
utilisateurs auto-hébergés sont illimités dans chaque niveau. Le binaire Community ne lit jamais
de licence pour activer ou désactiver quoi que ce soit.

**Une licence active donne accès aux artefacts commerciaux.** L’installation hors ligne (`olivares upgrade --bundle`) nécessite le binaire Enterprise. Enterprise installe un paquet Community signé sans licence ; un paquet commercial exige une licence active, vérifiée hors ligne. Community et Business prennent en charge `--bundle --check` sans lire de licence ni installer le paquet.
:::

## Ce que vous avez reçu

| Votre achat | Ce qui arrive | Ce que vous en faites |
|---|---|---|
| Community | rien à installer | déjà en cours d'exécution — rien sur cette page ne s'applique |
| Business / Enterprise, auto-hébergé | un **fichier de licence** et un **jeton de téléchargement** | installez la licence, puis passez au binaire commercial |
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

Installer une licence Business ou Enterprise valide sur un build Community laisse ce
build en fonctionnement. Le cockpit de session et les autres pages commerciales
n'apparaissent qu'une fois cette installation passée en Business. La vue **Edition & license**
de la console nomme la licence installée séparément du build. **Expires** est la durée
payée ; **Lease until**, lorsqu'il est affiché, est le bail d'instance provisoire
distinct. Vous utilisez Docker ? Allez à [Docker et Compose](#docker-et-compose) ci-dessous.

:::note[Business et le canal des artefacts commerciaux]
L'option `--enterprise` désigne le canal des artefacts commerciaux, pas l'édition.
Les éditions sont Community, Business et Enterprise. Business comprend Regulated Operations,
AI Runtime Security, Compliance Packs et Identity & Scale dans un abonnement.
Chaque famille peut être activée ou désactivée ; aucune n'est vendue séparément.
:::

Avec une licence installée, le binaire commercial n'est plus qu'à un téléchargement. Rien
n'est réinstallé et aucune donnée n'est déplacée :

```sh
export OLIVARES_UPGRADE_TOKEN=<TOKEN>   # not needed once this installation is connected
olivares upgrade --edition business
```

`olivares license install` et `olivares license status` affichent la commande exacte pour la façon dont cette installation a été faite, et la vue **Edition & license** de la console propose la même étape sous la forme d'un bouton, **Passer à Business** : le moteur télécharge le build, le vérifie, le remplace sur place et redémarre dessus. Le jeton de téléchargement se trouve sur la page **Downloads** du portail de téléchargement sous licence ([https://licenses.olivares.ai/portal/downloads](https://licenses.olivares.ai/portal/downloads)), à côté de ces commandes, et dans votre e-mail de licence ; passez-le par son nom, comme ci-dessus, pour qu'il reste hors de la liste des processus. Une installation connectée (`olivares license connect start`) n'a besoin d'aucun jeton. `--enterprise` est l'ancienne graphie de la même route. Le bouton de la console demande l'un des deux, le jeton dans l'environnement du moteur lui-même ou cette connexion ; sans l'un ni l'autre, la vue affiche la commande à la place.

:::caution[Installations par paquet]
Sur une installation deb, rpm, apk ou AUR, le gestionnaire de paquets possède `/usr/bin/olivares` : la
commande est donc refusée, avec le code de sortie 2, avant tout téléchargement. Utilisez le paquet
Business dès que le canal de paquets avec droits le propose. D'ici là, ajoutez
`--target /usr/bin/olivares --data-dir /var/lib/olivares` pour remplacer vous-même ce fichier ; la base de données des paquets ne le
décrit alors plus correctement. `--check` montre toujours le plan.
:::

La commande récupère le build commercial signé pour votre plateforme et **vérifie la signature
hors ligne** — un artefact altéré interrompt la mise à niveau en laissant intact le binaire en
cours d'exécution — puis le remplace atomiquement en conservant une sauvegarde du précédent.
Business porte le même numéro de version que Community : la commande fonctionne donc aussi lorsque vous
exécutez déjà cette version. Utilisez d'abord `--check` si vous souhaitez voir le plan sans l'appliquer ;
il indique le changement par `edition: community -> enterprise` :

```sh
olivares upgrade --edition business --check
```

Redémarrez le service, puis activez les modules :

```sh
olivares enterprise enable <preset>     # starter | regulated | full
```

La commande et les noms de preset sont ce que le binaire commercial accepte. Les presets ne sont pas des éditions et ne correspondent pas un à un aux quatre familles de capacités Business.
L'activation est encadrée et auditée : elle vous montre d'abord un diff et place en attente
tout module nécessitant un secret ou une revue au lieu de l'activer à moitié.
`olivares enterprise status` indique ce qui est actif. Ces commandes existent **uniquement
dans le binaire commercial** — si `olivares enterprise` n'est pas une commande, vous utilisez
encore le build Community et le remplacement ci-dessus n'a pas encore eu lieu.

:::caution[Sauvegardez avant le remplacement]
Le remplacement porte sur un binaire, pas sur vos données — mais effectuez tout de même la
sauvegarde demandée par [Mettre à niveau et revenir en arrière](/fr/how-to/upgrade-and-rollback/).
Cette page explique aussi comment revenir au binaire précédent.
:::

### Docker et Compose

L'image publique `docker.io/olivaresai/olivares` est Community. Dans un conteneur,
`olivares upgrade` installe la version dans le volume de données et non dans l'image, de sorte
que recréer le conteneur la redémarre. Cela applique la version Business lorsqu'elle est plus
récente que l'image ou de même version (`--check` indique le changement par `edition: community -> enterprise`) ;
construire vous-même l'image, comme décrit plus bas, est l'alternative. Une fois la licence installée
(section 2), lancez la mise à niveau dans le conteneur en cours d'exécution, puis redémarrez-le.
Le jeton figure dans votre e-mail de livraison ; transmettez-le par son nom pour qu'il
n'apparaisse pas dans la liste des processus :

```sh
export OLIVARES_UPGRADE_TOKEN=<TOKEN>
docker compose exec -e OLIVARES_UPGRADE_TOKEN olivares olivares upgrade --edition business --yes
docker compose restart olivares
```

La version est conservée dans `ota/` sous le répertoire de données (`/var/lib/olivares/ota`
dans l'image fournie), avec le manifeste signé et l'artefact dont elle a été vérifiée. Le
binaire de l'image la démarre lorsqu'elle est plus récente que l'image, ou qu'elle porte la
même version compilée autrement (une image Community de la version que vous avez appliquée en
Business). Rien dans le volume n'est cru sur parole : chaque démarrage vérifie la signature du
manifeste avec la clé OTA intégrée à l'image, et l'artefact avec l'empreinte que signe le
manifeste. La preuve est conservée dans le répertoire temporaire du conteneur (le fichier
Compose fourni y monte un `tmpfs`), pas dans le volume ; recréer le conteneur la vide. Si
l'image est plus récente que la version appliquée, l'image s'exécute et le journal de démarrage
le dit ; relancez la mise à niveau. Une preuve qui ne se vérifie pas est ignorée avec un
message, et l'image s'exécute. Le volume ne garde aucune sauvegarde du binaire précédent : pour
revenir au binaire de l'image, supprimez `ota/`. Cela suppose un volume qui autorise
l'exécution de fichiers (un volume nommé le permet ; sur un montage `noexec`, `olivares serve`
s'arrête et en donne la raison au lieu d'exécuter silencieusement le binaire de l'image).

Vous pouvez aussi construire l'image vous-même. Téléchargez la **même version et la même architecture Linux** du binaire Business via
le [portail de téléchargement sous licence](https://licenses.olivares.ai/portal), puis vérifiez
sa signature et sa somme de contrôle en suivant les instructions du téléchargement. Vous
pouvez ensuite superposer ce binaire vérifié à l'image d'exécution Community correspondante
sur votre propre hôte. Dans un répertoire contenant le binaire vérifié nommé `olivares`,
utilisez ce `Dockerfile` :

```dockerfile
ARG COMMUNITY_IMAGE
FROM ${COMMUNITY_IMAGE}
COPY --chmod=0755 olivares /usr/local/bin/olivares
```

Construisez avec le digest de l'image Community de cette version, vérifié comme dans
[Vérifier une version](/fr/how-to/verify-a-release/). Le résultat conserve l'utilisateur non root,
le point d'entrée et les ports de cette image ; le `Dockerfile` n'a donc besoin d'aucune
ligne `USER` :

```sh
docker build --build-arg COMMUNITY_IMAGE=docker.io/olivaresai/olivares@sha256:<digest> \
  --tag olivares-business-local:X.Y .
```

Gardez cette image privée et conservez la licence commerciale et les avis fournis avec
le téléchargement. Ne placez ni votre fichier de licence ni votre jeton de téléchargement
dans le contexte de build. Définissez l'`image` de votre service Compose sur
`olivares-business-local:X.Y`, puis recréez ce service avec
`docker compose up -d --no-deps --force-recreate olivares`. Pour Docker en mode
autonome, recréez le conteneur avec cette image, le même volume et les mêmes arguments.
Conservez le volume de données : n'utilisez pas `down --volumes`.

Vérifiez **Edition & license** après le redémarrage : l'édition doit indiquer **Business**,
et la licence installée reste dans le volume de données. Les contrats Enterprise utilisent
aussi ce build commercial ; tout artefact propre à un contrat suit ses instructions de
téléchargement.

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
le binaire commercial continue de se comporter comme le binaire commercial jusqu'à ce que
vous reveniez au précédent.

## Ce qui ne figure *pas* sur cette page

- **Émettre des licences** (`license keygen` / `sign`) est le volet fournisseur de la même
  commande. Vous n'en avez pas besoin en tant que client.
- **Ce que contient chaque offre** se trouve dans les pages de tarification, pas ici.
- **Comment fonctionne le modèle** — pourquoi un abonnement donne accès aux artefacts plutôt
  que d'agir comme un interrupteur — est expliqué dans [Open core et licences](/fr/explanation/open-core-and-licensing/).
- **La répétition achat → remboursement** (fixtures de bac à sable, pas une vraie carte) est
  [Répéter le cycle commercial](/how-to/rehearse-commercial-cycle/).
