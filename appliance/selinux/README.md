# The appliance's SELinux policy module

The Fedora appliance runs with SELinux enforcing and the targeted policy. This directory holds the policy module
`olivares` that confines the appliance's own services, and the package `olivares-selinux` that installs it.

## What it confines

| Domain | Runs | Started by |
|---|---|---|
| `olivares_portal_t` | the Appliance Console (`olivares-portal.service`) | its socket, `olivares-portal.socket` |
| `olivares_repair_console_t` | the repair console on tty1 | PID 1 |
| `olivares_helper_power_t` | the power helper, one instance per connection | `olivares-helper-power.socket` |
| `olivares_helper_bundle_t` | the support-bundle helper, one instance per connection | `olivares-helper-support-bundle.socket` |
| `olivares_snapshotcore_t` | the return-point observe, good, verify and excluded-state guard units | PID 1 |
| `olivares_firstboot_t` | first boot and its readiness unit | PID 1 |
| `olivares_t` | the product engine (`olivares.service`) | PID 1, enabled by first boot |
| `olivares_cli_t` | `olivares-appliance`, the local API's command-line and terminal client | an operator's SSH login, through one Fedora interface call |
| `olivares_net_guard_t` | the network guard (`olivares-net-guard.service`), NetworkManager's sole writer, and its root runtime initializer | its socket, `olivares-net-guard.socket`; `olivares-network-runtime.service` |
| `olivares_helper_netrestore_t` | netrestore, root's restoration of the baseline keyfile, one instance per connection | `olivares-helper-netrestore.socket` |
| `olivares_helper_netprobe_t` | netprobe, the unprivileged network probe, one instance per connection | `olivares-helper-netprobe.socket` |

Every unit's program has its own entry type, so no appliance service runs in a catch-all domain. The booted-root
capture runs in the initrd, before the policy is loaded. The module adds no permissive domain and one boolean,
`olivares_operate_runtime`, off by default: it lets the product run a conducted runtime that needs executable
memory, and is set with the Operate drop-in.

The network guard talks to NetworkManager over its own system-bus connection and is the only domain that
changes profiles, through NetworkManager's methods; it writes no keyfile. netrestore, admitted for the guard and the
repair console, alone removes NetworkManager's runtime shadow keyfile and asks for a reload; it reads the guard's
journal and writes only root's ledger in `/run/olivares-netrestore`. First boot's cloud-init host-owner drop-in has its
own type, so first boot writes no other file in `/etc`.

The portal's local API listens on `/run/olivares-portal-api/local.sock`. Its one admitted caller domain is
`olivares_cli_t`: only that domain may connect, and the portal reads `/proc/<pid>/cgroup` of no other domain, so a
peer in any other domain is refused. The module declares the domain and its entry type with `application_domain`.
An operator's SSH login (root, through sudo) runs in Fedora's default login domain, and the module
enters the CLI's domain from it through exactly one call of Fedora's interface for that domain, row CLI-16, written
inside `optional_policy`. That call is the only place the module names the login domain: the generator refuses any
other such term, a raw row of the call's rules, and the call with other arguments. A copy of the program with another
label stays in the login domain, and the portal refuses it. The portal's writable state is the operations leaf
`/var/lib/olivares-portal/operations` of the static `olivares-portal` account (never `/var/lib/private`); its parent
is searchable only, and the portal holds `/run/olivares-lifecycle/lifecycle.lock` read-only and shared.

## Files

- `access.tsv`: one row per rule, with its id, the rule and its reason; one `interface` row is the module's single
  Fedora interface call (its source and target are the two arguments, its perms column the interface's name). `contexts.tsv`: one row per file or port
  context, with how the module declares the type and the type Fedora gives the path or port without the module.
- `generate.py`: writes `olivares.te`, `olivares.fc` and `olivares.if` from the two tables. The three are committed and
  never edited by hand; `generate.py --check` compares them.
- `olivares-selinux.spec`: the noarch package, built with Fedora's SELinux policy Makefile and installed at priority 200
  by the `selinux-policy` RPM macros. On the first install it records the four ports in one `semanage import`:
  `port -m` for 8443, which Fedora holds as an exact record in `http_port_t`, and `port -a` for 8444, 9443 and 11434,
  which Fedora covers only by a range (its unreserved range, `pki_ca_port_t`'s 9443-9447). An upgrade adds any of the
  four that is missing. On erase it deletes them before the module leaves. A failed import is named on stderr and the
  scriptlet still exits 0.
- `denied.tsv`: accesses the compiled policy must not grant, at least one per domain. A row may name the one boolean
  state it is allowed in (`allowed_when`): the engine's executable memory only with `olivares_operate_runtime=true`.
- `compare_rules.py`: the hosted check that the compiled module holds exactly the rows, and that the installed policy
  grants every row and none of `denied.tsv` in the policy's defaults or with a boolean the module declares at true or
  at false; conditional rules count where their condition puts them in force.
- `manifest.tsv`: the accesses the enforcing image must show, each with its source line, domain, path, class,
  permissions, the label Fedora gives the path and the rows that hold it, and the accesses it must refuse.
  `test_manifest.py` holds it against the tables.
- `recipe-pins.json`: the image recipe's container digest and signed selinux-policy build, which the workflow must use
  (`test_workflow_pins.py`).

## Changing a rule

1. Edit the row in `access.tsv` (or `contexts.tsv`); its reason names what in the product needs it.
2. Run `python3 appliance/selinux/generate.py` and `python3 -m unittest discover -s appliance/selinux -p 'test_*.py'`.
3. Commit the tables and the three generated files together.

The generator refuses a permissive or unconfined term, a wildcard permission, an execution of `bin_t` or
`shell_exec_t`, and a module type nothing declares. The workflow `appliance-selinux` compiles the module in a Fedora 44
container, runs SELint, compares the compiled rules with the rows, builds and installs the package, and erases it.
