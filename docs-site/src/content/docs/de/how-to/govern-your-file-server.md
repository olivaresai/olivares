---
title: "Ihren Dateiserver governen"
description: "Binden Sie einen Verzeichnisbaum (lokal, NFS oder SMB) als read-only governte Wissensquelle an: Dateien werden zu Dokumenten, POSIX-Eigentum und ACLs werden auf Dokument-ACLs abgebildet, und Reads bleiben konstruktionsbedingt auf das Root-Verzeichnis beschränkt."
---

Der Content-Connector `filesystem` (`olivares.fs-content`) verwandelt einen
Verzeichnisbaum — einen lokalen Pfad, einen NFS-Export oder einen SMB-Mount —
in **governte Wissensdokumente**, die dieselbe Pipeline wie jede andere
Inhaltsquelle durchlaufen (maskieren → klassifizieren → in Chunks teilen →
einbetten → indexieren → über MCP bereitstellen). Dokument-ACLs werden aus dem
POSIX-Eigentum und Klassifizierungen aus xattrs abgebildet. Er ist eine
Content-**Quelle** und unterscheidet sich vom Log-**Sink** `filelog` (der Logs
*hinaus* weiterleitet).

Für einen Self-Hosted-Operator ist der Dateiserver häufig der älteste und
größte Dokumentenspeicher und damit einer der wertvollsten Connectoren im
Katalog.

## Konstruktionssichere Reads

Der Connector liest **ausschließlich innerhalb des konfigurierten
Root-Verzeichnisses**, garantiert durch `os.Root` aus der
Go-Standardbibliothek:

- Ein **Symlink aus dem Root-Verzeichnis heraus**, ein **absoluter Pfad** oder
  eine **`..`-Traversal** wird **abgelehnt** — der Connector kann physisch
  keine Datei lesen, auf die Sie ihn nicht verwiesen haben.
- Symlinks werden beim Durchlaufen **nicht verfolgt** (sie werden gezählt,
  niemals aufgelöst).
- Jeder Datei-Body ist **größenbegrenzt** (größere Dateien werden abgeschnitten
  und markiert), nur **Text-/Dokumenttypen** werden gelesen (Binärdateien
  werden übersprungen und gezählt), Inhalte werden **niemals ausgeführt**, und
  das Durchlaufen wird durch **Budgets für Dateianzahl und Gesamtbytes**
  begrenzt, damit ein großer oder langsamer (NFS-)Mount nicht erschöpft werden
  kann.

Adversariale Tests belegen die Ablehnung von Symlink-Escapes und Traversals.

## Auf einen Verzeichnisbaum verweisen

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

Jede Datei wird zu einem Dokument: Der Body ist der Dateiinhalt, die DocID ihr
Root-relativer Pfad, und die Herkunftsattribute enthalten `owner`, `group`,
`mode`, `size`, `world_readable` und `path`.

## Abbildung von Eigentum und ACLs — die ehrliche Matrix

Der Connector bildet **nur das ab, was das Dateisystem ausdrückt**, und
deklariert, was er nicht abbilden kann:

| Dateisystem | Owner / Group / Mode | POSIX.1e ACL (`getfacl`) | Windows-/NFSv4-ACL |
|---|---|---|---|
| **Lokal** (ext4/xfs/btrfs) | Abgebildet: Owner → `user:<name>`, Group (wenn gruppenlesbar) → `group:<name>` | Abgebildet: jeder benannte Benutzer-/Gruppeneintrag mit Read-Bit → eine Principal-Referenz | n/a |
| **NFS** | Abgebildet, **wenn uid/gid konsistent gemappt sind** (idmapd / dasselbe Verzeichnis auf beiden Seiten) | Abgebildet, wenn der Mount `system.posix_acl_access` bereitstellt | **NFSv4-native ACLs werden NICHT geparst** (deklarierte Grenze) |
| **SMB / CIFS** | Aus `uid=/gid=/file_mode=` des **Mounts** abgebildet — also aus Mount-Optionen, **nicht** aus dem tatsächlichen Windows-Owner | Normalerweise nicht vorhanden | **Windows-Sicherheitsdeskriptoren werden NICHT geparst** (`system.cifs_acl` ist ein binärer SD; deklarierte Grenze) |

Principal-Namen werden über den Namensdienst des Hosts aufgelöst (der **LDAP**
einschließen kann, sodass `uid`→Benutzername Ihrem Verzeichnis entspricht).
Eine nicht auflösbare uid/gid fällt auf ihre **numerische** ID zurück. Eine
Datei **ohne ableitbare ACL** erbt die Standard-ACL der Knowledge Base, die
beim Retrieval weiterhin erzwungen wird. Der Connector **erfindet niemals**
eine ACL, die eine Datei nicht trägt.

### Klassifizierung

- Eine Standard-`classification` gilt für jede Datei.
- Ein dateispezifisches **xattr** (standardmäßig `user.classification`)
  überschreibt sie.
- Das **xattr für externe Labels** (`user.olivares.labels`, kommasepariert)
  fügt Sensitivity-Labels hinzu, die das Retrieval-DLP speisen und neben der
  Klassifizierung deny-closed erzwungen werden.

## Ehrliche Grenzen

- **Nur Text-/Dokumentdateien.** Binärdateien werden übersprungen und gezählt.
  Rich-Formate, die eine Extraktion benötigen (PDF/DOCX), werden von diesem
  Connector **nicht** aufgenommen (eine deklarierte Folgemaßnahme, kein
  stillschweigendes Überspringen).
- Ein Body ist **auf 1 MiB begrenzt**; größere Dateien werden abgeschnitten und
  mit `truncated` markiert.
- **SMB**: Der Connector sieht die synthetische POSIX-Sicht Ihres Mounts, nicht
  die tatsächliche Windows-ACL.
- Der Connector **liest**; er schreibt niemals in den Verzeichnisbaum (es gibt
  konstruktionsbedingt keinen Write-Pfad).

## Wire-Proof

Die Sicherheitsgarantien werden hier durch adversariale Tests abgedeckt
(Symlink-Escape, Traversal, Größenbegrenzung, Überspringen von Binärdateien,
Abbildung von POSIX-Owner/Group/ACL, xattr-Klassifizierung). Der vollständige
Wire-Proof — ein Fixture-Baum hinter einer Folder-Bindung, über MCP
bereitgestellt, sodass eine Claude-Code-Sitzung nur sieht, was ihre Bindung und
die Datei-ACL erlauben, einschließlich des Nachweises eines abgelehnten
Unterbaums — ist der CI-Integrationsjob, der die Engine zusammensetzt.
