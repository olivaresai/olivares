#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
# Static, network-free DIST-24-16 uninstall and migration contract.
set -euo pipefail

root="${OLIVARES_ROOT:-$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)}"
for tool in bash grep python3; do
	command -v "$tool" >/dev/null 2>&1 || {
		printf 'uninstall contract: NO HE PODIDO MIRAR — missing %s\n' "$tool" >&2
		exit 2
	}
done

for path in \
	cmd/olivares/cmd_uninstall.go cmd/olivares/cmd_uninstall_test.go \
	cmd/olivares/internal/localinstall/manifest.go \
	cmd/olivares/internal/localinstall/manifest_owner_unix.go \
	cmd/olivares/internal/localinstall/manifest_owner_other.go \
	cmd/olivares/internal/localinstall/uninstall.go \
	cmd/olivares/internal/localinstall/uninstall_test.go \
	cmd/olivares/cmd_dr_migration_test.go \
	cmd/olivares/cmd_dr_drill.go core/api/dr_handler.go \
	core/dr/bundle.go core/dr/integrity.go core/dr/lowlevel_test.go \
	docs/contracts/release-index.schema.json scripts/render-release-index.sh \
	scripts/check-release-index.sh \
	scripts/install.sh scripts/install-bootstrap.sh scripts/install-service.sh \
	packaging/nfpm/postinstall.sh packaging/nfpm/preremove.sh packaging/nfpm/postremove.sh \
	packaging/nfpm/apk-preupgrade.sh packaging/openrc/olivares.sh packaging/openrc/load-env.sh \
	packaging/nfpm/package-init-openrc.txt packaging/nfpm/package-init-systemd.txt \
	scripts/check-uninstall-contract.sh scripts/test-uninstall-migration.sh \
	scripts/test-nfpm-openrc.sh scripts/nfpm-apk-postinstall-runtime.sh \
	INSTALL.md docs/RELEASE-INSTALLER.md docs/DR-RUNBOOK.md \
	docs-site/src/content/docs/how-to/install-from-packages.md; do
	[[ -f "$root/$path" ]] || {
		printf 'uninstall contract: FAIL — missing %s\n' "$path" >&2
		exit 1
	}
done
for script in \
	scripts/check-uninstall-contract.sh scripts/test-uninstall-migration.sh \
	scripts/install.sh scripts/install-bootstrap.sh scripts/install-service.sh \
	packaging/nfpm/postinstall.sh packaging/nfpm/preremove.sh packaging/nfpm/postremove.sh \
	packaging/nfpm/apk-preupgrade.sh packaging/openrc/olivares.sh packaging/openrc/load-env.sh \
	scripts/test-nfpm-openrc.sh scripts/nfpm-apk-postinstall-runtime.sh; do
	bash -n "$root/$script"
done

# ⛔ EL CENSO DE .go MIDE LOS FICHEROS SEGUIDOS, NO EL DISCO.
#
# Antes recorria `root.rglob("*.go")`, que entra tambien en lo IGNORADO. El 2026-09-03 un
# `.export-tmp/tmp.NZajCfxMeT` filtrado el 23-08 —650 ficheros, 20 MB, once dias— llevaba un
# `cmd_support_test.go` con `dr.WriteBundle(` que el arbol ya NO tiene, y el gate se puso rojo
# acusando a un fichero limpio: medido, 0 ocurrencias en `origin/main` y 0 en la rama, y la
# unica llamada viva del repositorio es la permitida en `core/dr/lowlevel_test.go`.
#
# Un censo que mide el disco mide los residuos de OTROS guiones. `git ls-files` mide lo que el
# repositorio dice que existe, que es el sujeto que esta regla quiere vigilar.
censo_go="$(mktemp)"
trap 'rm -f "$censo_go"' EXIT
if ! git -C "$root" ls-files -z -- '*.go' >"$censo_go" 2>/dev/null; then
	echo "check-uninstall-contract: 2 NO PUDE MIRAR — $root no es un arbol de git, y el censo de" >&2
	echo "  ficheros seguidos no se puede hacer. Un recorrido del disco NO es un sustituto: veria" >&2
	echo "  los residuos ignorados de otros guiones y acusaria a ficheros limpios." >&2
	exit 2
fi

python3 - "$root" "$censo_go" <<'PY'
import pathlib
import sys

root = pathlib.Path(sys.argv[1])
# El censo de Go de mas abajo mide el REPOSITORIO, no el disco. Lo recibe ya hecho, con NUL
# de separador para que un salto de linea en un nombre no lo parta.
censo = [b.decode() for b in pathlib.Path(sys.argv[2]).read_bytes().split(b"\0") if b]
read = lambda p: (root / p).read_text(encoding="utf-8")

main = read("cmd/olivares/main.go")
command = read("cmd/olivares/cmd_uninstall.go")
manifest = read("cmd/olivares/internal/localinstall/manifest.go")
manifest_owner = read("cmd/olivares/internal/localinstall/manifest_owner_unix.go")
uninstall = read("cmd/olivares/internal/localinstall/uninstall.go")
render = read("scripts/render-release-index.sh")
release_checker = read("scripts/check-release-index.sh")
schema = read("docs/contracts/release-index.schema.json")
installer = read("scripts/install.sh")
bootstrap = read("scripts/install-bootstrap.sh")
service = read("scripts/install-service.sh")
postinstall = read("packaging/nfpm/postinstall.sh")
preremove = read("packaging/nfpm/preremove.sh")
postremove = read("packaging/nfpm/postremove.sh")
apk_preupgrade = read("packaging/nfpm/apk-preupgrade.sh")
openrc_unit = read("packaging/openrc/olivares.sh")
openrc_env = read("packaging/openrc/load-env.sh")
goreleaser = read(".goreleaser.yaml")
pkg_init_apk = read("packaging/nfpm/package-init-openrc.txt").strip()
pkg_init_sysd = read("packaging/nfpm/package-init-systemd.txt").strip()
dr_command = read("cmd/olivares/cmd_dr.go")
dr_drill = read("cmd/olivares/cmd_dr_drill.go")
dr_api = read("core/api/dr_handler.go")
dr_bundle = read("core/dr/bundle.go")
dr_integrity = read("core/dr/integrity.go")
taskfile = read("Taskfile.yml")
hook = read(".githooks/pre-push")
mainline = read(".github/workflows/mainline-ci.yml")

assert "newUninstallCmd()" in main
for token in (
    "--plan", "--preserve", "--purge", "confirmDestructive",
    "exitcode.New(exitcode.Usage", "localinstall.Load", "localinstall.Execute",
):
    assert token in command
assert command.index("confirmDestructive") < command.index("localinstall.Execute")
for token in (
    "DisallowUnknownFields", "systemLayout", "userSuffixLayout",
    "absent from release-index install_layout", "must contain one allowed binary, config and unit",
    "unexpected system service account", "validateTuple",
    # Custom data roots: a shape policy, never an allowlist, and the drop-in /
    # runtime env are derived from the closed unit and config paths.
    "LayoutCustom", "customDataDir", "at least two levels deep",
    "DropinPath(units[0])", "RuntimeEnvPath(m.Config)",
):
    assert token in manifest
for token in ("want = 0", "writable by group or others"):
    assert token in manifest_owner
for token in (
    "BuildPlan", "if opts.Operation == Plan", "os.Lstat(target)",
    "through a symlink", "purgeDataTree(target, filepath.Base(m.ManifestPath))", "UserCreated", "GroupCreated",
    "reloadServiceManager",
    # A custom data directory needs the closed-layout unit as a second witness
    # before even a plan is printed; an explicit workspace is never removed.
    "corroborateLayout(m, opts.Root)", "does not name data directory", "UnitDataDir(m.Init",
    # The witness preserve records beside the config is the only thing that can
    # stand in for the unit, and a purge removes the tree before the manifest
    # and the witness last, so an interrupted purge stays retryable.
    #
    # It also has to say WHAT is still pending. A record that only proves an uninstall
    # STARTED was read as proof that the software it listed was already gone, so a purge
    # interrupted before it could remove the binary reported success on the retry and
    # left that binary installed (independent review, 2026-09-05, R2). pending_software
    # is that list, and recordPending keeps it truthful when a removal fails.
    "WitnessPath(m)", "writeWitness(m, opts.Root, opts.Operation, pending)", "purgeDataTree(target",
    "PendingSoftware", "pendingSoftware(m, opts.Operation, kept)", "recordPending(",
    "DisallowUnknownFields",
    # Owning the DATA is not controlling the live SERVICE. Only a definition at the
    # closed unit path that executes THIS estate's engine makes this installation the
    # live one; with somebody else's there, nothing of theirs is stopped, removed or
    # deleted (R1).
    "serviceControlOf(m, opts.Root)", "keptByOthers(m, control, liveUnit, witness)",
    "control == serviceOwn", "control != serviceForeign",
):
    assert token in uninstall
assert uninstall.index("through a symlink") < uninstall.index("stopService(m, opts.Run)")
assert uninstall.index("corroborateLayout(m, opts.Root)") < uninstall.index('fmt.Fprintf(opts.Out, "  %-12s')
assert uninstall.index("writeWitness(m, opts.Root, opts.Operation, pending)") < uninstall.index("removeExact(target)")
assert 'Role: "workspace"' in uninstall and 'Role: "witness"' in uninstall
# The service is stopped only when this estate holds the live definition, and the shared
# identities are removed only when it is not somebody else's.
assert 'opts.Root == "" && control == serviceOwn' in uninstall
assert 'opts.Root == "" && m.Mode == "system" && control != serviceForeign' in uninstall
unitparse = read("cmd/olivares/internal/localinstall/unitparse.go")
for token in (
    "func UnitDataDir(", '"ExecStart"', '"command_args="', '"command_args_base"', "ProgramArguments", "ambiguous",
    # OpenRC consumes command_args= only. A command_args_base= witnesses where a
    # command_args= assignment copies it in as the whole word $command_args_base, and
    # every command_args= assignment must prove the same directory: an unreferenced or
    # later-replaced base is a stale mention (independent review 2026-09-05, B1).
    "openrcBaseReference(", "no --data-dir in another",
    # A directive only witnesses when the init system would RUN it and it runs the
    # recorded program: an ExecStart= in [Unit] and one starting another binary are
    # inert text about this estate (R3).
    'section != "Service"', "args[0] != program",
):
    assert token in unitparse, token

system_paths = (
    "/opt/olivares", "/opt/olivares/bin/olivares", "/usr/bin/olivares",
    "/usr/local/bin/olivares", "/etc/olivares/olivares.env",
    "/Library/Preferences/dev.olivares.olivares.env", "/var/lib/olivares",
    "/Library/Application Support/Olivares", "/etc/init.d/olivares",
    "/etc/systemd/system/olivares.service", "/usr/lib/systemd/system/olivares.service",
    "/Library/LaunchDaemons/dev.olivares.olivares.plist",
)
for path in system_paths:
    assert path in render, path
    assert path in manifest, path
    assert path in release_checker, path
for suffix in (
    "/.local/bin/olivares", "/.config/olivares/olivares.env",
    "/.local/share/olivares", "/.config/systemd/user/olivares.service",
    "/Library/LaunchAgents/dev.olivares.olivares.plist",
):
    assert suffix in render, suffix
    assert suffix in manifest, suffix
    assert suffix in release_checker, suffix
for token in ("install_layout", "olivares.ai/install-layout/v1", "uniqueItems"):
    assert token in schema

for source in (installer, bootstrap):
    assert source.index('if [ "$uninstall" -eq 1 ]') < source.index("dl() {")
    assert 'exec "$@"' in source
    assert '--yes is valid only with --uninstall --purge' in source
    assert 'installed binary path is not absolute' in source
for source in (service, postinstall):
    assert "olivares.ai/local-install/v2" in source
    assert "user_created" in source and "group_created" in source
# The adapter records which layout it rendered and refuses the shapes the
# consumer refuses, so no manifest it writes is one the uninstaller rejects.
for token in ('"layout": "%s"', "at least two levels deep", "refuse_link_components", "quote_unit"):
    assert token in service, token
assert "uninstall --preserve" in preremove and "uninstall --plan" in preremove
assert "rm -rf /var/lib/olivares" not in postremove
assert "uninstall --purge" in postremove
assert pkg_init_apk == "openrc", pkg_init_apk
assert pkg_init_sysd == "systemd", pkg_init_sysd
assert '"init": "$OLIVARES_PKG_INIT"' in postinstall
assert '"init": "systemd"' not in postinstall
assert postinstall.index("pkg_init") < postinstall.index("command -v systemctl")
assert 'OLIVARES_PKG_INIT" = systemd ] && command -v systemctl' in postinstall
assert "/etc/init.d/olivares" in postinstall
assert "rc-service olivares start" in postinstall
assert "rc-update add olivares default" in postinstall
assert "does not enable or start" in postinstall
assert "journalctl -u olivares" in postinstall
assert "rc-service olivares status" in preremove
assert "rc-service olivares stop" in preremove
assert "rc-update del olivares default" in preremove
assert "apk-preupgrade.sh" in preremove
assert "rc-service olivares stop" in apk_preupgrade
assert "rc-update add" not in apk_preupgrade
assert "pkg-upgrade-was-active" in apk_preupgrade and "pkg-upgrade-was-active" in postinstall
assert "export \"$line\"" not in openrc_unit
assert "export \"$line\"" not in openrc_env
assert "olivares_load_env" in openrc_env
assert "command_user=\"olivares:olivares\"" in openrc_unit
assert "command=\"/usr/bin/olivares\"" in openrc_unit
assert "serve --data-dir=/var/lib/olivares" in openrc_unit
assert "--listen=127.0.0.1:8443" in openrc_unit
assert "--grpc-listen=127.0.0.1:8444" in openrc_unit
assert "/usr/lib/olivares/openrc-load-env.sh" in openrc_unit
assert "output_log=\"/var/log/olivares.log\"" in openrc_unit
assert "error_log=\"/var/log/olivares.log\"" in openrc_unit
assert "output_logger" not in openrc_unit
assert "chown olivares:olivares /var/lib/olivares" in openrc_unit
assert "ip link set lo up" in openrc_unit
assert "/var/log/olivares.log" in postinstall
assert ". \"$olivares_config\"" not in openrc_unit
assert "source " not in openrc_env
assert "packager: apk" in goreleaser
assert "packaging/openrc/olivares.sh" in goreleaser
assert "dst: /etc/init.d/olivares" in goreleaser
assert "preupgrade: packaging/nfpm/apk-preupgrade.sh" in goreleaser
assert "postupgrade: packaging/nfpm/postinstall.sh" in goreleaser
# The systemd unit must not be an unscoped contents entry (that shipped it in APK).
systemd_src = "src: packaging/systemd/olivares.service"
assert goreleaser.count(systemd_src) == 2
assert "packager: deb" in goreleaser and "packager: rpm" in goreleaser
import yaml
nfpm = yaml.safe_load(goreleaser)["nfpms"]
assert len(nfpm) == 1
contents = nfpm[0]["contents"]
apk_units = [c for c in contents if c.get("dst") == "/etc/init.d/olivares"]
assert len(apk_units) == 1 and apk_units[0].get("packager") == "apk"
assert int(apk_units[0].get("file_info", {}).get("mode", 0)) in (0o755, 755, 493)
sysd = [c for c in contents if c.get("src") == "packaging/systemd/olivares.service"]
assert {c.get("packager") for c in sysd} == {"deb", "rpm"}
stamps = [c for c in contents if c.get("dst") == "/usr/lib/olivares/package-init"]
assert {c.get("packager") for c in stamps} == {"deb", "rpm", "apk"}
apk_stamp = [c for c in stamps if c.get("packager") == "apk"][0]
assert apk_stamp.get("src") == "packaging/nfpm/package-init-openrc.txt"
data_dirs = [c for c in contents if c.get("dst") == "/var/lib/olivares"]
assert {c.get("packager") for c in data_dirs} == {"deb", "rpm"}
assert nfpm[0]["apk"]["scripts"]["preupgrade"] == "packaging/nfpm/apk-preupgrade.sh"
assert nfpm[0]["apk"]["scripts"]["postupgrade"] == "packaging/nfpm/postinstall.sh"
assert "scripts/test-nfpm-openrc.sh" in taskfile
assert "scripts/nfpm-apk-postinstall-runtime.sh" in taskfile
assert "  lint:nfpm-apk-postinstall-runtime:" in taskfile
assert "run: task lint:nfpm-apk-postinstall-runtime" in mainline

for token in (
    "AuthenticateBundle", "VerifyBundleIntegrity", "hmac-sha256-kek-v1",
    "manifest authentication mismatch", "payload digest mismatch",
):
    assert token in dr_integrity
for token in (
    "CheckImportCompatibility", "allow-legacy-unsigned", "VerifyBundleIntegrity",
):
    assert token in dr_command
for source in (dr_command, dr_drill, dr_api):
    assert "WriteAuthenticatedBundle" in source
for source in (dr_command, dr_api):
    assert "CheckImportCompatibility" in source
for source in (dr_command, dr_drill, dr_api):
    assert "VerifyBundleIntegrity" in source
assert "refusing DR import from newer engine" in dr_integrity
assert "requires an authenticated manifest" in dr_bundle

# The low-level serializer is deliberately internal-by-convention: an added
# producer must not bypass AuthenticateBundle. This census watches every Go
# caller without relying on rg (which the house hook does not provide).
for rel in censo:
    if rel in ("core/dr/bundle.go",):
        continue
    path = root / rel
    body = path.read_text(encoding="utf-8")
    if rel == "core/dr/lowlevel_test.go":
        assert body.count("dr.WriteBundle(") == 1, rel
        continue
    assert "dr.WriteBundle(" not in body, rel

for target in ("lint:uninstall-contract", "lint:uninstall-contract:selftest"):
    assert f"  {target}:" in taskfile
    assert f"task {target}" in hook
    assert f"run: task {target}" in mainline

docs = "\n".join(read(p) for p in (
    "INSTALL.md", "docs/RELEASE-INSTALLER.md", "docs/DR-RUNBOOK.md",
    "docs-site/src/content/docs/how-to/install-from-packages.md",
))
for token in (
    "olivares uninstall --plan", "olivares uninstall --preserve",
    "olivares uninstall --purge", "hmac-sha256-kek-v1",
    "--allow-legacy-unsigned", "newer engine",
):
    assert token in docs, token
PY

printf '%s\n' 'uninstall contract: OK — indexed removal, authenticated migration and gate parity wired'
