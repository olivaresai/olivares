# nfpm-openrc-baseline — the APK-shipped-systemd defect, as a self-contained witness

**These three files are deliberately wrong. Do not "fix" them.**

`scripts/test-nfpm-openrc.sh` keeps one packaging defect permanently red: the APK
package used to ship a systemd unit (`/usr/lib/systemd/system/olivares.service`) with
no OpenRC unit, its post-install recorded `"init": "systemd"` for every package
format, and its pre-remove stopped nothing on Alpine because it classified the
package by whether `systemctl` happened to be on PATH. The correction ships
`/etc/init.d/olivares` plus a `package-init` stamp for APK and scopes the systemd
unit to deb/rpm.

The gate used to read that baseline with `git show <sha>:…` from the development
history. The public export is a curated new history, so that object does not exist
there and the gate could only answer "could not look". This directory replaces the
history lookup: it is the pre-correction packaging **reduced to the lines the witness
reads**, and it is read from the working tree only. No git object, no private SHA.

| file | what it reproduces |
|---|---|
| `nfpms.yaml` | the nfpms block with ONE unscoped systemd unit for `[deb, rpm, apk]`, no `/etc/init.d/olivares`, no `package-init` stamp, no `apk:` scripts |
| `postinstall.sh` | the hook that writes `"init": "systemd"` unconditionally and only knows `systemctl` |
| `preremove.sh` | the hook whose Alpine branch runs `uninstall --plan` and never `rc-service olivares stop` |

How the gate uses it: the block and the two hooks are overlaid on a copy of the real
`packaging/` tree in a throwaway project, built with the same pinned goreleaser as the
real packages, and the resulting `.apk` is inspected with the **current** contract.
It must fail, and it must fail for the causal reason (`apk missing
/etc/init.d/olivares`), while its payload provably carries the systemd unit and its
`.post-install` provably hard-codes `init=systemd`. A static predicate over the three
files is kept as well, with the corrected tree as its negative control.

If a future change makes these files read as the correction, the gate goes red with
`baseline fixture no longer reads as the defect it witnesses` — that is the fixture
doing its job, not a reason to edit it.
