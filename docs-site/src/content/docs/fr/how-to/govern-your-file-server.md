---
title: "Gouverner votre serveur de fichiers"
description: "Connectez une arborescence de répertoires (locale, NFS ou SMB) comme source de connaissances gouvernée et en lecture seule : les fichiers deviennent des documents, les propriétaires et ACL POSIX sont transposés en ACL de documents, et les lectures sont confinées à la racine par construction."
---

Le connecteur de contenu `filesystem` (`olivares.fs-content`) transforme une
arborescence de répertoires — chemin local, export NFS ou montage SMB — en **documents de
connaissances gouvernés** qui suivent le même pipeline que toutes les autres sources de
contenu (masquer → classifier → fragmenter → intégrer → indexer → servir via MCP), avec
des ACL de documents issues des propriétaires POSIX et une classification issue des
xattrs. Il s'agit d'une **source** de contenu, distincte du **sink** de logs `filelog`
(qui transmet les logs vers l'*extérieur*).

Pour un opérateur auto-hébergé, le serveur de fichiers est souvent le magasin de
documents le plus ancien et le plus volumineux. Il s'agit donc de l'un des connecteurs
les plus utiles du catalogue.

## Sécurité des lectures par construction

Les lectures du connecteur sont **confinées à la racine configurée**, ce que garantit
`os.Root` dans la bibliothèque standard de Go :

- Un **lien symbolique qui pointe hors de la racine**, un **chemin absolu** ou une
  **traversée avec `..`** est **refusé** : le connecteur est physiquement incapable de
  lire un fichier que vous ne lui avez pas indiqué.
- Les liens symboliques ne sont **pas suivis** pendant le parcours (ils sont comptés,
  mais jamais résolus).
- Le corps de chaque fichier est **limité en taille** (les fichiers plus volumineux sont
  tronqués et marqués), seuls les **types texte/document** sont lus (les binaires sont
  ignorés et comptés), le contenu n'est **jamais exécuté**, et le parcours respecte des
  **budgets de nombre de fichiers et de volume total d'octets** afin de ne pas épuiser
  un montage volumineux ou lent (NFS).

Des tests adversariaux prouvent le refus des échappements par lien symbolique et des
traversées de chemin.

## Pointer le connecteur vers une arborescence

```jsonc
// OLIVARES_SOURCES_CONFIG — document sources live under "documents"
{
  "documents": [
    {
      "name": "file-server",
      "kind": "filesystem",
      "config": {
        "root": "/mnt/fileserver/shared",   // local path or an NFS/SMB mount
        "include": "*.md,*.txt,docs/*",       // globs (path or basename); empty = all text
        "exclude": "**/archive/*,*.tmp",
        "max_file_bytes": "1048576",          // per-file cap (hard-capped at 1 MiB)
        "max_files": "100000",                // walk budget
        "max_total_bytes": "1073741824",      // read budget
        "text_only": "true",                  // skip binaries (counted)
        "map_posix_acl": "true",              // owner/group + POSIX.1e ACL → Document ACL
        "classification": "internal",         // default label (an xattr overrides per file)
        "classification_xattr": "user.classification",
        "labels_xattr": "user.olivares.labels"
      }
    }
  ]
}
```

Chaque fichier devient un Document : son corps est le contenu du fichier, son DocID le
chemin relatif à la racine, et ses attributs de provenance contiennent `owner`, `group`,
`mode`, `size`, `world_readable` et `path`.

## Transposition des propriétaires et des ACL — la matrice fidèle

Le connecteur transpose **uniquement ce qu'exprime le système de fichiers** et déclare ce
qu'il ne peut pas transposer :

| Système de fichiers | propriétaire / groupe / mode | ACL POSIX.1e (`getfacl`) | ACL Windows / NFSv4 |
|---|---|---|---|
| **Local** (ext4/xfs/btrfs) | Transposé : propriétaire → `user:<name>`, groupe (si le groupe possède le droit de lecture) → `group:<name>` | Transposé : chaque entrée d'utilisateur ou de groupe nommé qui possède le droit de lecture → une référence de principal | sans objet |
| **NFS** | Transposé, **si les uid/gid sont associés de manière cohérente** (idmapd / même annuaire des deux côtés) | Transposé lorsque le montage expose `system.posix_acl_access` | **Les ACL natives NFSv4 ne sont PAS analysées** (limite déclarée) |
| **SMB / CIFS** | Transposé depuis les `uid=/gid=/file_mode=` du **montage**, c'est-à-dire ses options, **pas** depuis le véritable propriétaire Windows | Généralement absent | **Les descripteurs de sécurité Windows ne sont PAS analysés** (`system.cifs_acl` est un SD binaire ; limite déclarée) |

Les noms de principaux sont résolus par le service de noms de l'hôte (qui peut inclure
**LDAP**, afin que la correspondance `uid`→nom d'utilisateur reflète votre annuaire). Si
un uid/gid ne peut pas être résolu, son identifiant **numérique** est utilisé. Un fichier
pour lequel aucune ACL ne peut être déduite hérite de l'ACL par défaut de la base de
connaissances, toujours appliquée lors de la récupération. Le connecteur **n'invente
jamais** une ACL que le fichier ne porte pas.

### Classification

- Une `classification` par défaut s'applique à chaque fichier.
- Un **xattr** propre au fichier (`user.classification` par défaut) la remplace.
- Le **xattr d'étiquettes externes** (`user.olivares.labels`, valeurs séparées par des
  virgules) ajoute des étiquettes de sensibilité utilisées par la DLP de récupération,
  qui les applique en mode fermé (deny-closed), parallèlement à la classification.

## Limites déclarées

- **Fichiers texte/document uniquement.** Les binaires sont ignorés et comptés. Les
  formats riches qui nécessitent une extraction (PDF/DOCX) ne sont **pas** ingérés par
  ce connecteur (il s'agit d'un suivi déclaré, pas d'une omission silencieuse).
- Le corps est **limité à 1 Mio** ; les fichiers plus volumineux sont tronqués et marqués
  `truncated`.
- **SMB** : le connecteur voit la représentation POSIX synthétique de votre montage,
  pas la véritable ACL Windows.
- Le connecteur **lit** l'arborescence ; il n'y écrit jamais (aucun chemin d'écriture
  n'existe, par conception).

## Preuve en conditions réelles

Des tests adversariaux couvrent les garanties de sécurité (échappement par lien
symbolique, traversée, limite de taille, exclusion des binaires, transposition des
propriétaires/groupes/ACL POSIX et classification xattr). La preuve complète — une
arborescence de test derrière une liaison de dossier, servie via MCP afin qu'une session
Claude Code ne voie que ce qu'autorisent sa liaison et l'ACL du fichier, avec la preuve
du refus d'une sous-arborescence — est fournie par le job d'intégration CI qui compose le
moteur.
