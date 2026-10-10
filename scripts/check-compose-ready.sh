#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
# Static, network-free DIST-24-12 wiring and claim contract.
#
# The same program holds the compose-ready workflow's runtime refusals, so the battery can
# exercise them without Docker:
#   owner                                    this run's projects and image tag, from the job env
#   compose-version TEXT                     Compose >= 2.24.4, needed by the no-ports layer
#   effective-config JSON PROJECT IMAGE SVC… the effective stack publishes no host port and
#                                            holds only resources of its owned project
# Each answers 0 accepted, 1 refused, 2 could not look.
set -euo pipefail

root="${OLIVARES_ROOT:-$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)}"
for tool in bash grep python3; do
	command -v "$tool" >/dev/null 2>&1 || {
		printf 'compose-ready contract: COULD NOT CHECK — missing %s\n' "$tool" >&2
		exit 2
	}
done

contract() {
	python3 - "$root" "$@" <<'PY'
import json
import os
import pathlib
import re
import sys

root = pathlib.Path(sys.argv[1])
mode, args = sys.argv[2], sys.argv[3:]

# This run's resources on the shared Docker daemon: both Compose projects and the local image
# tag carry one owner, <repository_id>-<run_id>-<run_attempt>.
OWNER = r"[0-9]+-[0-9]+-[0-9]+"
PROJECTS = {
    "READY_POSITIVE_PROJECT": re.compile(rf"olivares-ready-({OWNER})-positive"),
    "READY_NEGATIVE_PROJECT": re.compile(rf"olivares-ready-({OWNER})-negative"),
}
IMAGE = re.compile(rf"olivares-compose-ready:({OWNER})")
OWNED = (*PROJECTS, "OLIVARES_IMAGE")
ENGINE_SERVICES = ("olivares", "olivares-standby")
MIN_COMPOSE = (2, 24, 4)
NO_PORTS = "deploy/compose/docker-compose.ready-no-ports.ci.yml"


def owner_problems(values):
    problems, owners = [], set()
    for name in OWNED:
        match = PROJECTS.get(name, IMAGE).fullmatch(values.get(name) or "")
        if match:
            owners.add(match.group(1))
        else:
            problems.append(f"{name}={values.get(name)!r} is not owned by one repository, run and attempt")
    if len(owners) > 1:
        problems.append(f"the owned projects and image tag name different owners {sorted(owners)}")
    return problems


def refuse(label, problems):
    for problem in problems:
        print(f"compose-ready {label}: REFUSED — {problem}", file=sys.stderr)
    sys.exit(1)


def cannot_look(label, why):
    print(f"compose-ready {label}: COULD NOT CHECK — {why}", file=sys.stderr)
    sys.exit(2)


if mode == "owner":
    values = {name: os.environ.get(name) for name in OWNED}
    problems = owner_problems(values)
    if problems:
        refuse("owner", problems)
    print("compose-ready owner: " + IMAGE.fullmatch(values["OLIVARES_IMAGE"]).group(1))
    sys.exit(0)

if mode == "compose-version":
    text = " ".join(args)
    match = re.search(r"(\d+)\.(\d+)\.(\d+)", text)
    if not match:
        cannot_look("compose version", f"no version in {text!r}")
    version = ".".join(match.groups())
    if tuple(int(part) for part in match.groups()) < MIN_COMPOSE:
        refuse("compose version", [f"Compose {version} predates 2.24.4, which the no-ports layer's !override tag needs"])
    print(f"compose-ready compose version: {version} >= 2.24.4")
    sys.exit(0)

if mode == "effective-config":
    if len(args) < 4:
        cannot_look("effective config", "usage: effective-config JSON PROJECT IMAGE SERVICE...")
    path, project, image, services = args[0], args[1], args[2], sorted(args[3:])
    try:
        config = json.loads(pathlib.Path(path).read_text(encoding="utf-8"))
    except (OSError, ValueError) as error:
        cannot_look("effective config", f"{path}: {error.__class__.__name__}")
    if not isinstance(config, dict):
        cannot_look("effective config", f"{path} is not a JSON object")
    problems = []
    checkout = pathlib.Path(os.path.realpath(root))
    project_match = next((m for m in (p.fullmatch(project) for p in PROJECTS.values()) if m), None)
    image_match = IMAGE.fullmatch(image)
    if not project_match or not image_match or project_match.group(1) != image_match.group(1):
        problems.append(f"project {project!r} and image {image!r} are not one owned identity")
    if config.get("name") != project:
        problems.append(f"effective project name {config.get('name')!r}, want {project!r}")
    declared = config.get("services") if isinstance(config.get("services"), dict) else {}
    if sorted(declared) != services:
        problems.append(f"effective services {sorted(declared)}, want {services}")
    for name, service in sorted(declared.items()):
        service = service if isinstance(service, dict) else {}
        if service.get("ports"):
            problems.append(f"service {name} publishes {len(service['ports'])} host port(s)")
        if service.get("container_name"):
            problems.append(f"service {name} fixes container_name {service['container_name']!r}")
        if name in ENGINE_SERVICES and service.get("image") != image:
            problems.append(f"service {name} runs image {service.get('image')!r}, want {image!r}")
        # Sessions run in the engine: a bind of, inside or above the checkout gives them
        # deploy/compose/dr-pass and .env, which Compose users keep there.
        mounts = service.get("volumes", []) if name in ENGINE_SERVICES else []
        if not isinstance(mounts, list) or not all(isinstance(mount, dict) for mount in mounts):
            cannot_look("effective config", f"service {name} volumes are not a list of mounts")
        for mount in mounts:
            if mount.get("type") != "bind":
                continue
            if not isinstance(mount.get("source"), str) or not mount["source"] or "\0" in mount["source"]:
                cannot_look("effective config", f"service {name} binds {mount.get('source')!r}")
            source = pathlib.Path(os.path.realpath(mount["source"]))
            if checkout.is_relative_to(source) or source.is_relative_to(checkout):
                problems.append(
                    f"service {name} mounts the Olivares checkout ({mount['source']}) at "
                    f"{mount.get('target')}: set OLIVARES_PROJECT_DIR to your project folder"
                )
    for kind in ("volumes", "networks"):
        resources = config.get(kind) if isinstance(config.get(kind), dict) else {}
        for key, resource in sorted(resources.items()):
            resource = resource if isinstance(resource, dict) else {}
            if resource.get("external") or resource.get("name") != f"{project}_{key}":
                problems.append(f"{kind[:-1]} {key} is named {resource.get('name')!r}, outside project {project}")
    if problems:
        refuse("effective config", problems)
    print(f"compose-ready effective config: {project} publishes no host port; services {services}")
    sys.exit(0)

if mode != "contract":
    cannot_look("contract", f"unknown mode {mode!r}")

read = lambda p: (root / p).read_text(encoding="utf-8")
main = read("cmd/olivares/main.go")
command = read("cmd/olivares/cmd_readyz.go")
probe = read("cmd/olivares/internal/readyzprobe/probe.go")
probe_test = read("cmd/olivares/internal/readyzprobe/probe_test.go")
compose = read("deploy/compose/docker-compose.yml")
standby = read("deploy/compose/docker-compose.standby-not-ready.ci.yml")
workflow = read(".github/workflows/compose-ready.yml")
taskfile = read("Taskfile.yml")
mainline = read(".github/workflows/mainline-ci.yml")
battery = read("scripts/test-compose-ready.sh")
goreleaser = read(".goreleaser.yaml")

assert "newReadyzCmd()" in main
assert '"readyz": "engine"' in main
for token in (
    'readyzprobe.Check(', 'exitcode.New(exitcode.Err, nil)',
    'exitcode.New(exitcode.Usage, nil)', 'cannot inspect readiness:',
):
    assert token in command
for token in (
    'response.StatusCode == http.StatusOK', 'host.IsLoopback()',
    'Proxy:             nil', 'http.ErrUseLastResponse', 'u.RawQuery != ""',
):
    assert token in probe
for token in (
    "TestCheckMapsLocalReadyzFixtures", "TestCheckRejectsReadyzDown",
    "TestCheckTrustsTheExplicitLocalCertificate", "TestCheckDoesNotFollowRedirects",
):
    assert token in probe_test

health = (
    "healthcheck:\n"
    "      test:\n"
    "        - CMD\n"
    "        - /usr/local/bin/olivares\n"
    "        - readyz\n"
    "        - --server=https://127.0.0.1:8443\n"
    "        - --ca-cert=/var/lib/olivares/tls.crt\n"
    "        - --timeout=3s"
)
assert health in compose
for token in ('user: "65532:65532"', "read_only: true", "memory: 2G"):
    assert token in compose


# A Docker daemon refuses to create a container whose CPU limit exceeds the daemon's CPU count, so
# a `cpus` above 1 is a minimum of that many CPUs: `up` fails on a 1-CPU host. A shipped Compose
# file therefore carries no `cpus` above 1, whether it sits under
# deploy.resources.limits, at service level, in flow style, with a quoted key or in an anchored
# x- block; the host's CPUs bound the engine, and an operator who wants a lower ceiling adds it
# in a Compose override. The scan is textual because the checker has no YAML parser.
CPUS_KEY = re.compile(r"""(?<![\w.-])["']?cpus["']?\s*:\s*["']?(\$\{[^}]*\}|[^\s,"'}]+)""")
compose_files = sorted((root / "deploy/compose").glob("*compose*.y*ml"))
assert compose_files, "deploy/compose holds no compose file"
for compose_file in compose_files:
    text = "\n".join(re.sub(r"(^|\s)#.*", "", line) for line in compose_file.read_text(encoding="utf-8").splitlines())
    for value in CPUS_KEY.findall(text):
        assert re.fullmatch(r"[0-9]+(\.[0-9]*)?|\.[0-9]+", value) and float(value) <= 1, (
            f"{compose_file.name} sets cpus {value!r}: a Docker daemon refuses a CPU limit above "
            "its CPU count, so a shipped limit above 1 stops `up` on a 1-CPU host; drop it or use 1.0 or less"
        )
# The README container reaches one host project folder: the base file mounts
# OLIVARES_PROJECT_DIR at /project, or the empty olivares-project volume when it is unset,
# never PWD (the documented commands run from the checkout, whose deploy/compose/ holds the
# DR passphrase and .env), and every README that sends the reader to Compose names the
# mount and its variable.
project_mount = '      - "${OLIVARES_PROJECT_DIR:-olivares-project}:/project"\n'
base_service, top_volumes = compose.split("\nservices:\n", 1)[1].split("\nvolumes:\n", 1)
assert project_mount in base_service and "\n  olivares-project:\n" in f"\n{top_volumes}", (
    "deploy/compose/docker-compose.yml must mount the host project folder at /project: "
    "source OLIVARES_PROJECT_DIR, defaulting to the declared olivares-project volume, "
    "never PWD or a relative path"
)
for readme in ("README.md", "README.de.md", "README.es.md", "README.fr.md", "README.ja.md",
               "README.ru.md", "README.zh.md", "deploy/compose/README.md"):
    text = read(readme)
    assert "`OLIVARES_PROJECT_DIR`" in text and "`/project`" in text, (
        f"{readme} must document the project mount (`/project`, `OLIVARES_PROJECT_DIR`)"
    )
# The install block must not run from the Olivares clone: there PWD is the clone, and the
# container would get the product's own source as its session folder.
for readme in ("README.md", "README.de.md", "README.es.md", "README.fr.md", "README.ja.md",
               "README.ru.md", "README.zh.md"):
    block = read(readme).split("```sh\n", 1)[1].split("```", 1)[0]
    up = block.index("up --wait")
    assert 'export OLIVARES_PROJECT_DIR="$PWD"' in block[:up] and "&& cd olivares" not in block, (
        f"{readme} install block must set OLIVARES_PROJECT_DIR to the user's project folder "
        "before `up` and must not cd into the clone"
    )
assert 'export OLIVARES_PROJECT_DIR=' in read("INSTALL.md").split("### Docker Compose", 1)[1].split("```", 2)[1], (
    "INSTALL.md Docker Compose block must set OLIVARES_PROJECT_DIR before `up`"
)
assert "## Work on a host project folder" in read("deploy/compose/README.md"), (
    "deploy/compose/README.md must keep the 'Work on a host project folder' section the READMEs link to"
)
# The real namespace probe covers kernel enforcement. This guard keeps its host
# preparation reachable from every documented quick install, before Compose starts.
assert "apparmor=${OLIVARES_APPARMOR_PROFILE:-docker-default}" in compose
assert "  userns," in read("deploy/apparmor/olivares-sessions.conf")
for doc in [*root.glob("README*.md"), root / "INSTALL.md"]:
    text = doc.read_text(encoding="utf-8")
    section = text.split("### Docker Compose", 1)[1] if doc.name == "INSTALL.md" else text
    block = section.split("```", 2)[1]
    up = block.index(" up --wait")
    for token in ("set -e\n", "userns_create", "deploy/apparmor/olivares-sessions.conf",
                  "sudo apparmor_parser -r /etc/apparmor.d/olivares-sessions",
                  "export OLIVARES_APPARMOR_PROFILE=olivares-sessions"):
        assert token in block[:up], f"{doc.name} must prepare AppArmor before Compose up: {token}"
assert standby.count("condition: service_healthy") == 2
assert health in standby
for token in ("--engine=postgres", "olivares-standby-data", "read_only: true"):
    assert token in standby

guard = (
    "vars.OLIVARES_DEV_CI == 'true' ||\n      (github.event_name != 'schedule' &&\n"
    "      (github.repository == 'olivaresai/olivares' || vars.OLIVARES_RELEASE_PROFILE == 'preprod'))"
)
assert 'push:\n    tags: ["[0-9]*.[0-9]*"]' in workflow, (
    "compose-ready.yml must qualify bare MAJOR.MINOR release tags"
)
for token in (
    "workflow_dispatch:", "schedule:\n    - cron:", guard, "permissions:\n  contents: read",
    "docker build", "up --wait --wait-timeout 120", "up --wait --wait-timeout 90",
    'if [ "$rc" -ne 1 ]', "{{.State.Running}}", "HTTP 503",
):
    assert token in workflow
# After the positive `up` from the checkout root, /project exists and is not the checkout.
assert re.search(r"(?m)^          docker exec \"\$cid\" test -d /project -a ! -e "
                 r"/project/deploy/compose/docker-compose\.yml$", workflow), (
    "compose-ready.yml must check that /project in the running container is not the checkout"
)
for forbidden in ("docker push", "build-push-action", "docker/login-action", "push: true"):
    assert forbidden not in workflow

# The dispatch job must be runnable where it is routed, with only what its own steps
# provide. Explicit self-hosted Linux x64 labels leave no GitHub-hosted fallback when the
# pool variable is unset.
selector = "    runs-on: [self-hosted, linux, x64, \"${{ vars.CI_RUNNER || 'self-hosted' }}\"]"
runs_on = re.findall(r"(?m)^[ \t]*runs-on:.*$", workflow)
assert runs_on == [selector], (
    f"compose-ready.yml must route its job to {selector.strip()!r}, found {runs_on}"
)

steps = re.split(r"(?m)^      - ", workflow.split("\n    steps:\n", 1)[1])[1:]
step_name = lambda block: block.split("\n", 1)[0].strip()
assert len(steps) >= 8, f"compose-ready.yml step split found {len(steps)} steps"

# A self-hosted runner is not assumed to ship Task: the job installs the repository's
# pinned version before its first task invocation and proves that it is on PATH.
task_pin = re.compile(
    r"(?m)^[ \t]*go install github\.com/go-task/task/v3/cmd/task@(v\d+\.\d+\.\d+)[ \t]*$"
)
mainline_pins = set(task_pin.findall(mainline))
# Mainline installs Task through the reviewed local cache action. Resolve its
# declared default and each literal override so every caller shares the same pin.
cache_uses = re.compile(r"(?m)^        uses: \./\.github/actions/olivares-tool-cache[ \t]*$")
cache_steps = [block for block in re.split(r"(?m)^      - ", mainline) if cache_uses.search(block)]
if cache_steps:
    action = read(".github/actions/olivares-tool-cache/action.yml")
    fields = re.findall(r"(?m)^  task_version:\n((?:    [^\n]*\n|\n)+)", action)
    assert len(fields) == 1, "Task cache action has no unique task_version input"
    defaults = re.findall(r"(?m)^    default:[ \t]*(.*)$", fields[0])
    assert len(defaults) == 1, "Task cache action has no unique task_version default"

    def literal_task_version(value):
        match = re.fullmatch(r'(?:"(\d+\.\d+\.\d+)"|\'(\d+\.\d+\.\d+)\'|(\d+\.\d+\.\d+))', value.strip())
        assert match, "Task cache version must be a literal semantic version"
        return "v" + next(part for part in match.groups() if part is not None)

    default = literal_task_version(defaults[0])
    for block in cache_steps:
        overrides = re.findall(r"(?m)^          task_version:[ \t]*(.*)$", block)
        declarations = re.findall(r"(?m)^[ \t]*task_version:", block)
        assert len(overrides) == len(declarations) and len(overrides) <= 1, "Task cache version override has an unsupported form"
        mainline_pins.add(literal_task_version(overrides[0]) if overrides else default)
assert len(mainline_pins) == 1, f"mainline-ci.yml Task pins are not one version: {sorted(mainline_pins)}"
installs = [i for i, block in enumerate(steps) if cache_uses.search(block)]
users = [
    i for i, block in enumerate(steps)
    if re.search(r"(?m)^[ \t]*(?:run:[ \t]*)?task[ \t]+[a-z]", block)
]
assert users, "compose-ready.yml invokes no task; the Task setup assertion has no subject"
assert len(installs) == 1 and installs[0] < users[0], (
    f"compose-ready.yml runs {step_name(steps[users[0]])!r} without first installing the pinned Task"
)
install = steps[installs[0]]
versions = re.findall(r"(?m)^          task_version:[ \t]*(.*)$", install)
assert len(versions) == 1, "compose-ready.yml must declare its Task version explicitly"
assert {literal_task_version(versions[0])} == mainline_pins, "compose-ready.yml Task pin differs from mainline-ci.yml"
assert "command -v task" in action, "the shared Task action lacks its following-step PATH check"
assert 'echo "$job_dir" >> "${GITHUB_PATH:?GITHUB_PATH is not set}"' in read(
    ".github/actions/olivares-tool-cache/install.sh"
), "the shared Task installer does not publish its verified directory"

# The runner's Docker daemon is shared. The CI-only final layer carries exactly two changes,
# both about sharing one daemon: the engine's published ports are replaced by an empty list,
# and the fixed container_name the shipped base pins for operators is reset, because a
# container name is unique per daemon and two runs would collide on it.
no_ports = [
    line for line in read(NO_PORTS).splitlines() if line.strip() and not line.lstrip().startswith("#")
]
assert no_ports == [
    "services:", "  olivares:", "    container_name: !reset null", "    ports: !override []",
], (
    f"{NO_PORTS} must contain only services.olivares.container_name: !reset null and "
    f"ports: !override [], found {no_ports}"
)

# And the shipped base must carry BOTH of the things that layer exists to undo. A base that
# stopped pinning the name would make the reset above inert and nobody would notice.
assert "\nname: olivares\n" in compose, (
    "deploy/compose/docker-compose.yml must name its project, or the container is named after "
    "the directory the file lives in (compose-olivares-1)"
)
assert "    container_name: olivares\n" in compose, (
    "deploy/compose/docker-compose.yml must pin container_name: olivares, or the documented "
    "`docker exec olivares olivares first-boot` is not one argv an operator can type"
)

# Both projects and the image tag are defined once, in the job env, and are evaluated here for
# fixture contexts: a value that stays the same when the repository, the run or the attempt
# changes could be shared with another run on the daemon.
job_env_block = re.search(r"(?m)^    env:\n((?:      [A-Za-z_][A-Za-z0-9_]*:.*\n)+)", workflow)
job_env = dict(re.findall(
    r"(?m)^      ([A-Za-z_][A-Za-z0-9_]*):[ \t]*(.*?)[ \t]*$", job_env_block.group(1)
)) if job_env_block else {}
for name in OWNED:
    assert name in job_env, f"compose-ready.yml job env does not declare the owned {name}"
CONTEXT = ("repository_id", "run_id", "run_attempt")


def evaluate(name, context):
    def substitute(match):
        expression = match.group(1).strip()
        field = expression.removeprefix("github.")
        assert expression.startswith("github.") and field in CONTEXT, (
            f"{name} uses the expression {expression!r}; only github.repository_id, "
            "github.run_id and github.run_attempt may name its owner"
        )
        return context[field]
    return re.sub(r"\$\{\{(.*?)\}\}", substitute, job_env[name])


context = {"repository_id": "101", "run_id": "202", "run_attempt": "3"}
owned = {name: evaluate(name, context) for name in OWNED}
problems = owner_problems(owned)
assert not problems, problems
for field in CONTEXT:
    other = dict(context, **{field: context[field] + "7"})
    for name in OWNED:
        assert evaluate(name, other) != owned[name], (
            f"{name} does not vary with {field}; two runs could share it on the daemon"
        )


def code(text):
    return "\n".join(line for line in text.splitlines() if not line.lstrip().startswith("#"))


def step_env(block):
    env_block = re.search(r"(?m)^        env:\n((?:          [A-Za-z_][A-Za-z0-9_]*:.*\n)+)", block)
    return dict(re.findall(
        r"(?m)^          ([A-Za-z_][A-Za-z0-9_]*):[ \t]*(.*?)[ \t]*$", env_block.group(1)
    )) if env_block else {}


def compose_identities(block):
    """Every Compose invocation of a step except `version`, as (project variable, layer files)."""
    text = code(block)
    array = re.search(r"(?ms)^[ \t]*files=\(\n(.*?)^[ \t]*\)", text)
    array_files = re.findall(r"-f[ \t]+(deploy/compose/[A-Za-z0-9_.-]+\.ya?ml)", array.group(1)) if array else []
    calls = []
    for line in re.sub(r"\\\n[ \t]*", " ", text).splitlines():
        for match in re.finditer(r"docker compose\b", line):
            rest = line[match.end():]
            if re.match(r"[ \t]+version\b", rest):
                continue
            owner = re.match(r'[ \t]+-p "\$(READY_POSITIVE_PROJECT|READY_NEGATIVE_PROJECT)"', rest)
            assert owner, f"step {step_name(block)!r} runs Compose outside its owned project: {line.strip()[:120]!r}"
            if '"${files[@]}"' in rest:
                files = array_files
            else:
                files = re.findall(r"-f[ \t]+(deploy/compose/[A-Za-z0-9_.-]+\.ya?ml)", rest)
            calls.append((owner.group(1), tuple(files)))
    return calls


# Each step that runs Compose, including its always() cleanup, supplies every variable its
# layered files require with :? interpolation: Compose interpolates the whole project for
# down as well as up. Fixture passwords are URI-safe and pairwise distinct, as
# docker-compose.postgres.yml requires for its DSNs and roles.
def compose_required(path):
    text = "\n".join(line for line in read(path).splitlines() if not line.lstrip().startswith("#"))
    return set(re.findall(r"\$\{([A-Za-z_][A-Za-z0-9_]*):\?", text))


compose_steps = []
for index, block in enumerate(steps):
    if "docker compose" not in block:
        continue
    compose_steps.append(index)
    files = sorted(set(re.findall(r"-f[ \t]+(deploy/compose/[A-Za-z0-9_.-]+\.ya?ml)", block)))
    assert files, f"step {step_name(block)!r} runs Compose without a named -f file"
    env = step_env(block)
    redefined = sorted(set(env) & set(OWNED))
    assert not redefined, f"step {step_name(block)!r} redefines the job's owned {redefined}"
    env = {**job_env, **env}
    missing = sorted(name for name in set().union(*map(compose_required, files)) if not env.get(name))
    assert not missing, (
        f"step {step_name(block)!r} runs Compose without {missing}, required with :? by {files}"
    )
    passwords = {name: value for name, value in env.items() if name.endswith("_PASSWORD")}
    for name, value in passwords.items():
        assert re.fullmatch(r"[A-Za-z0-9._~-]+", value), f"step {step_name(block)!r}: {name} is not URI-safe"
    assert len(set(passwords.values())) == len(passwords), (
        f"step {step_name(block)!r} reuses a fixture password across {sorted(passwords)}"
    )
assert len(compose_steps) == 4, f"compose-ready.yml Compose step count {len(compose_steps)}, want 4"

# Every Compose command of a step names one owned project and ends its layers with the no-ports
# file; each always() cleanup follows its leg and removes exactly that leg's project and layers.
identities, legs, cleanups = {}, [], []
for index in compose_steps:
    block = steps[index]
    calls = compose_identities(block)
    assert calls and len(set(calls)) == 1, (
        f"step {step_name(block)!r} does not run every Compose command on one project and layer list: "
        f"{sorted(set(calls))}"
    )
    project, files = calls[0]
    assert files and files[-1] == NO_PORTS and files.count(NO_PORTS) == 1, (
        f"step {step_name(block)!r} runs Compose without the final no-ports layer {NO_PORTS}"
    )
    identities[index] = calls[0]
    if "up --wait" in block:
        legs.append(index)
    else:
        assert "down --volumes --remove-orphans" in block, f"step {step_name(block)!r} neither starts nor removes a fixture"
        cleanups.append(index)
assert len(legs) == 2 and len(cleanups) == 2, f"compose-ready.yml legs {legs} and cleanups {cleanups}, want two of each"
assert sorted(identities[leg][0] for leg in legs) == sorted(PROJECTS), "the two fixture legs do not use the two owned projects"
for leg, cleanup in zip(legs, cleanups):
    assert cleanup == leg + 1 and "if: ${{ always() }}" in steps[cleanup], (
        f"{step_name(steps[cleanup])!r} is not the always() cleanup directly after {step_name(steps[leg])!r}"
    )
    assert identities[cleanup] == identities[leg], (
        f"{step_name(steps[cleanup])!r} does not clean exactly the project and layers of {step_name(steps[leg])!r}"
    )

# Before `up`, each leg refuses unsupported Compose and then reads the effective configuration
# of exactly its own project and layers.
for leg in legs:
    block = code(steps[leg])
    project = identities[leg][0]
    cursor = -1
    for token in (
        "bash scripts/check-compose-ready.sh owner",
        'compose_version="$(docker compose version --short)"',
        'bash scripts/check-compose-ready.sh compose-version "$compose_version"',
        f'config="$RUNNER_TEMP/${project}.config.json"',
        f'docker compose -p "${project}" "${{files[@]}}" config --format json >"$config"',
        f'bash scripts/check-compose-ready.sh effective-config "$config" "${project}" "$OLIVARES_IMAGE"',
        "up --wait",
    ):
        found = block.find(token, cursor + 1)
        assert found > cursor, (
            f"step {step_name(block)!r} does not refuse unsupported Compose or a published port before up: "
            f"{token!r} is missing or out of order"
        )
        cursor = found

# Every step that touches Docker validates the owned identities first. The build keeps the
# source commit as its metadata and tags only the owned image.
docker_steps = [i for i, block in enumerate(steps) if "docker " in code(block)]
assert len(docker_steps) == 6, f"compose-ready.yml Docker step count {len(docker_steps)}, want 6"
for index in docker_steps:
    block = code(steps[index])
    assert -1 < block.find("bash scripts/check-compose-ready.sh owner") < block.find("docker "), (
        f"step {step_name(block)!r} touches Docker before validating the owned identities"
    )
builds = [i for i in docker_steps if "docker build" in steps[i]]
assert len(builds) == 1, f"compose-ready.yml build step count {len(builds)}, want 1"
for token in ('--build-arg COMMIT="${GITHUB_SHA}"', '--tag "$OLIVARES_IMAGE"'):
    assert token in steps[builds[0]], f"the build step lacks {token!r}"

# After both owned projects are down, only the exact owned tag is removed. Absence is reported,
# while a failed inspection or removal stays a failure.
removals = [i for i, block in enumerate(steps) if "docker image rm" in code(block)]
assert len(removals) == 1 and removals[0] > max(cleanups), (
    "compose-ready.yml must remove its image tag in one step after both owned projects are down"
)
removal = [line.strip() for line in code(steps[removals[0]]).splitlines()]
swallowed = [line for line in removal if "||" in line or "2>/dev/null" in line]
assert not swallowed, f"the image cleanup step swallows an inspection or removal failure: {swallowed}"
deletions = [
    line.strip() for line in code(workflow).splitlines() if re.search(r"\bdocker\b.*\b(?:rm|rmi|prune)\b", line)
]
assert deletions == ['docker image rm "$OLIVARES_IMAGE"'], (
    f"compose-ready.yml must delete only its exact owned image tag, without force, prune or image IDs: {deletions}"
)
listing = "--filter \"reference=$OLIVARES_IMAGE\" --format '{{.Repository}}:{{.Tag}}')\"; then"
for token in (
    "if: ${{ always() }}",
    "bash scripts/check-compose-ready.sh owner",
    'if ! listed="$(docker image ls ' + listing,
    'if ! grep -qxF -- "$OLIVARES_IMAGE" <<<"$listed"; then',
    'docker image rm "$OLIVARES_IMAGE"',
    'if ! remaining="$(docker image ls ' + listing,
    'if grep -qxF -- "$OLIVARES_IMAGE" <<<"$remaining"; then',
):
    assert token in removal, f"the image cleanup step lacks {token!r}"

dockerfiles = ["Dockerfile", "Dockerfile.release"] + [p for p in ("Dockerfile.fips", "Dockerfile.stig") if (root / p).is_file()]
for dockerfile in dockerfiles:
    text = read(dockerfile)
    assert "--chown=65532:65532 --chmod=0700 packaging/container/data-dir/ /var/lib/olivares/" in text
assert goreleaser.count("- packaging/container/data-dir") == len(dockerfiles)

# Every named volume that a shipped service running the engine image mounts must be seeded
# in each runtime image, for that image's non-root user. Docker gives a fresh named volume
# the ownership of the image directory it mounts over; an unseeded target is a root-owned
# mountpoint the service cannot write. The targets come from the Compose callers, not from
# a list kept here: the backup service's /backups was the target this missed.
backup = read("deploy/compose/docker-compose.backup.yml")


def engine_volume_targets(text):
    declared = set()
    if "\nvolumes:\n" in text:
        declared = set(re.findall(r"^  ([A-Za-z0-9][A-Za-z0-9_.-]*):\s*$", text.split("\nvolumes:\n", 1)[1], re.M))
    services = text.split("\nservices:\n", 1)[1].split("\nvolumes:\n", 1)[0]
    targets = set()
    for block in re.split(r"^  (?=[A-Za-z0-9][A-Za-z0-9_-]*:\s*$)", services, flags=re.M):
        if not re.search(r"^    image: \$\{OLIVARES_IMAGE[:}]", block, re.M):
            continue
        for source, target in re.findall(r"^      - ([A-Za-z0-9][A-Za-z0-9_.-]*):(/[^:\s]+)", block, re.M):
            if source in declared:
                targets.add(target.rstrip("/"))
    return targets


engine_targets = engine_volume_targets(compose) | engine_volume_targets(backup)
assert "/var/lib/olivares" in engine_targets and len(engine_targets) >= 2, engine_targets
for dockerfile in dockerfiles:
    text = read(dockerfile)
    assert "USER 65532:65532" in text, dockerfile
    for target in sorted(engine_targets):
        seed = f"--chown=65532:65532 --chmod=0700 packaging/container/data-dir/ {target}/"
        assert seed in text, f"{dockerfile} does not seed the named-volume target {target} for 65532"

for target in ("lint:compose-ready", "lint:compose-ready:selftest"):
    assert f"  {target}:" in taskfile
    assert f"run: task {target}" in mainline
assert "sed 's/if response.StatusCode == http.StatusOK {/if true {/'" in battery
assert "TestCheckRejectsReadyzDown" in battery

docs = "\n".join(read(p) for p in (
    "INSTALL.md", "deploy/compose/README.md",
    "docs-site/src/content/docs/tutorials/getting-started/docker-compose.mdx",
))
assert docs.count("Compose readiness contract") == 3
assert len(re.findall(
    r"Docker qualification\s+remains unmeasured until the\s+dispatch workflow succeeds",
    docs,
)) == 3
PY
}

case "${1:-}" in
owner | compose-version | effective-config)
	contract "$@"
	exit 0
	;;
'') ;;
*)
	printf 'compose-ready contract: COULD NOT CHECK — unknown mode %s\n' "$1" >&2
	exit 2
	;;
esac

_cls=""
if [ -f "$root/scripts/hub-leg.sh" ]; then
	_cls="$(bash "$root/scripts/hub-leg.sh" --classify --root "$root" 2>/dev/null || true)"
fi
for path in \
	.github/workflows/compose-ready.yml \
	cmd/olivares/cmd_readyz.go cmd/olivares/cmd_readyz_test.go \
	cmd/olivares/internal/readyzprobe/probe.go \
	cmd/olivares/internal/readyzprobe/probe_test.go \
	deploy/compose/docker-compose.yml \
	deploy/compose/docker-compose.postgres.yml \
	deploy/compose/docker-compose.standby-not-ready.ci.yml \
	deploy/compose/docker-compose.ready-no-ports.ci.yml \
	packaging/container/data-dir/.volume-owner.txt \
	scripts/check-compose-ready.sh scripts/test-compose-ready.sh \
	INSTALL.md deploy/compose/README.md \
	docs-site/src/content/docs/tutorials/getting-started/docker-compose.mdx \
	design/DIST-24-12-COMPOSE-READY-2026-09-02-CODEX.md; do
	if [[ -f "$root/$path" ]]; then
		continue
	fi
	# export-closure: hub-only design/DIST-24-12-COMPOSE-READY-2026-09-02-CODEX.md — design/ publishes zero paths; the contract still grades the shipped Compose/probe/docs
	if [[ "$path" == design/* ]] && [ "$_cls" = "public" ]; then
		printf 'compose-ready contract: SCOPED — %s is hub-only design material curated out of the published tree\n' "$path"
		continue
	fi
	printf 'compose-ready contract: FAIL — missing %s\n' "$path" >&2
	exit 1
done
bash -n "$root/scripts/check-compose-ready.sh"
bash -n "$root/scripts/test-compose-ready.sh"

contract contract

printf 'compose-ready contract: OK — local probe, hardened Compose, owned no-port Docker dispatch and honest docs wired\n'
