---
title: "Gobierna tu file server"
description: "Conecta un árbol de directorios (local, NFS o SMB) como fuente de conocimiento gobernada y de solo lectura: los ficheros se vuelven documentos, la propiedad y ACLs POSIX se mapean a ACLs de documento, y las lecturas quedan confinadas a la raíz por construcción."
---

El conector de contenido `filesystem` (`olivares.fs-content`) convierte un árbol de
directorios — una ruta local, un export NFS o un montaje SMB — en **documentos de
conocimiento gobernados** que fluyen por el mismo pipeline que cualquier otra fuente
(expurgar → clasificar → chunk → embed → indexar → servir por MCP), con ACLs de documento
mapeadas desde la propiedad POSIX y clasificación desde xattrs. Es una fuente de
contenido, distinta del `filelog` (un SINK de logs que reenvía *hacia fuera*).

Para un operador self-hosted, el file server suele ser el almacén documental más antiguo y
grande, así que este es uno de los conectores de mayor valor del catálogo.

## Seguridad de lectura por construcción

El conector lee **confinado a la raíz configurada**, garantizado por el `os.Root` de la
biblioteca estándar de Go:

- Un **symlink que apunta fuera de la raíz**, una **ruta absoluta** o un **traversal
  `..`** se **rechazan** — el conector no puede leer físicamente un fichero al que no le
  apuntaste.
- Los symlinks **no se siguen** durante el walk (se cuentan, nunca se resuelven).
- Cada cuerpo de fichero tiene **límite de tamaño** (los mayores se truncan y se marcan),
  solo se leen tipos **texto/documento** (los binarios se saltan y se cuentan), el
  contenido **nunca se ejecuta**, y el walk está acotado por **presupuesto de nº de
  ficheros y de bytes** para no tumbar un montaje grande o lento (NFS).

Tests adversariales prueban el rechazo de symlink-escape y traversal.

## Apúntalo a un árbol

```jsonc
// OLIVARES_SOURCES_CONFIG — las fuentes de documentos van bajo "documents"
{
  "documents": [
    {
      "name": "file-server",
      "kind": "filesystem",
      "config": {
        "root": "/mnt/fileserver/shared",   // ruta local o un montaje NFS/SMB
        "include": "*.md,*.txt,docs/*",       // globs (ruta o basename); vacío = todo texto
        "exclude": "**/archive/*,*.tmp",
        "max_file_bytes": "1048576",          // límite por fichero (tope duro 1 MiB)
        "max_files": "100000",                // presupuesto del walk
        "max_total_bytes": "1073741824",      // presupuesto de lectura
        "text_only": "true",                  // salta binarios (contados)
        "map_posix_acl": "true",              // owner/group + ACL POSIX.1e → ACL de documento
        "classification": "internal",         // etiqueta por defecto (un xattr la sobrescribe)
        "classification_xattr": "user.classification",
        "labels_xattr": "user.olivares.labels"
      }
    }
  ]
}
```

Cada fichero se convierte en un Documento: el cuerpo es el contenido del fichero, el DocID
es su ruta relativa a la raíz, y los atributos de procedencia llevan `owner`, `group`,
`mode`, `size`, `world_readable` y `path`.

## Cómo se mapean propiedad y ACLs — la matriz honesta

El conector mapea **solo lo que el sistema de ficheros expresa**, y declara lo que no puede:

| Sistema de ficheros | owner / group / mode | ACL POSIX.1e (`getfacl`) | ACL Windows / NFSv4 |
|---|---|---|---|
| **Local** (ext4/xfs/btrfs) | Mapeado: owner → `user:<nombre>`, group (si es group-readable) → `group:<nombre>` | Mapeado: cada entrada named user/group con el bit de lectura → una referencia de principal | n/a |
| **NFS** | Mapeado, **si uid/gid mapean de forma consistente** (idmapd / el mismo directorio a ambos lados) | Mapeado cuando el montaje expone `system.posix_acl_access` | **Las ACL nativas NFSv4 NO se parsean** (límite declarado) |
| **SMB / CIFS** | Mapeado desde `uid=/gid=/file_mode=` del **montaje** — es decir opciones de montaje, **no** el owner Windows real | Normalmente ausente | **Los security descriptors de Windows NO se parsean** (`system.cifs_acl` es un SD binario; límite declarado) |

Los nombres de principal se resuelven por el name service del host (que puede incluir
**LDAP**, así que `uid`→usuario coincide con tu directorio). Un uid/gid que no
resuelve cae a su id **numérico**. Un fichero **sin ACL derivable** hereda la ACL por
defecto del knowledge base, que retrieval sigue aplicando. El conector **nunca inventa**
una ACL que el fichero no tiene.

### Clasificación

- Una `classification` por defecto se aplica a todos los ficheros.
- Un **xattr** por fichero (`user.classification` por defecto) la sobrescribe.
- El **xattr de etiquetas externas** (`user.olivares.labels`, separado por comas) añade
  etiquetas de sensibilidad que alimentan el DLP de retrieval, aplicadas
  deny-closed junto a la clasificación.

## Límites honestos

- **Solo ficheros de texto/documento.** Los binarios se saltan y se cuentan. Los formatos
  ricos que requieren extracción (PDF/DOCX) **no** los ingiere este conector (un
  seguimiento declarado, no un salto silencioso).
- Un cuerpo se **limita a 1 MiB**; los ficheros mayores se truncan y se marcan `truncated`.
- **SMB**: el conector ve la vista POSIX sintética de tu montaje, no la ACL real de Windows.
- El conector **lee**; nunca escribe en el árbol (no hay ruta de escritura, por diseño).

## Wire-proof

Las garantías de seguridad están cubiertas por tests adversariales aquí (symlink-escape,
traversal, límite de tamaño, salto de binarios, mapeo de owner/group/ACL POSIX,
clasificación por xattr). El wire-proof completo — un árbol de fixtures detrás de un
binding de carpeta, servido por MCP para que una sesión Claude Code vea solo lo que su
binding + la ACL del fichero permiten, con un subárbol denegado probado — es el job de
integración de CI que compone el motor.
