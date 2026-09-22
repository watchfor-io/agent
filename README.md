# watchfor-agent

[![ci](https://github.com/watchfor-io/agent/actions/workflows/ci.yml/badge.svg)](https://github.com/watchfor-io/agent/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/watchfor-io/agent?display_name=tag)](https://github.com/watchfor-io/agent/releases/latest)
[![license](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

Host monitoring agent for [WatchFor](https://watchfor.io). One static
binary, one YAML file, outbound HTTPS only. It reads `/proc` and `/sys`,
turns them into a batch of samples once a minute (or as often as you
like, down to every 5 seconds) and pushes the batch
to WatchFor — no inbound ports, no credentials outside the host, nothing
executed from configuration.

Linux only for now (amd64, arm64). Windows is planned; macOS is not.

What it is for, in plain words: [watchfor.io/server-monitoring](https://watchfor.io/server-monitoring).
About the agent itself — what it collects, how it is secured, how to verify a
release: [watchfor.io/agent](https://watchfor.io/agent). Dashboard-side docs:
[watchfor.io/docs/hosts](https://watchfor.io/docs/hosts).

## Install

```sh
curl --proto '=https' --tlsv1.2 -fsSL https://watchfor.io/agent/install.sh | sudo sh -s -- --token <host-token>
```

The script downloads the release for your architecture, verifies it,
creates the unprivileged `watchfor-agent` user, writes the token to
`/etc/watchfor-agent/token` with mode 0600 and enables the systemd unit.
The host token comes from the WatchFor dashboard, one per host.

It needs no packages for the verification. The tarball comes from GitHub
(storage only); the checksums it is checked against come from watchfor.io,
which serves a release's `checksums.txt` only after verifying the release
signature on its side — so a file swapped on GitHub cannot pass. The
downloaded agent then verifies the release signature itself with the key
built into it (`watchfor-agent verify`). The script itself is served by
watchfor.io from the newest release, covered by the same signed checksums.

It needs root, systemd, `curl`, `tar`, `sha256sum` and `useradd` — all
checked before anything is touched. A missing tool does
not end the script halfway through: on a terminal, run with sudo, it
shows the one install command for your distribution and offers to run it
(`--yes` skips the question); otherwise it prints that command and stops.
Package installs run unattended (no debconf, needrestart or conffile
prompts, bounded lock waits) with the package manager's last line shown
next to the spinner, and Ctrl-C stops everything at once, leaving the
step's output in a file it names on exit (`/tmp/watchfor-install.XXXXXX`).

On a terminal the script also asks whether to enable daily auto-update
(see [Upgrade](#upgrade)); `--auto-update` / `--no-auto-update` decide
without asking. Re-running it on a host that already has this version
offers a reinstall and lets you switch auto-update on or off — it is the
one tool for installing, upgrading and reconfiguring the agent
(`--reinstall` forces a fresh download non-interactively).

Prefer to keep the token out of `ps` and your shell history: pass
`--token -` and the script asks for it on the terminal without echoing
it — prefer that on a shared machine, since a token given on the command
line shows in `ps` and in sudo's log; `WATCHFOR_TOKEN=…` in the
environment works for automation.
Progress, colours and the summary box appear only on a terminal; `--plain`
(or `NO_COLOR=1`) keeps the output to plain lines for logs and CI. The release signature is always verified; there is no switch to skip it. Once an
agent is installed, the same command upgrades it: the new release is
downloaded, verified and swapped in, and the service restarted; token and
`agent.yml` stay.

Prefer to look before you run? Download the tarball from the releases page,
put the binary anywhere, and run it by hand:

```sh
watchfor-agent check          # collect once, print the batch, send nothing
watchfor-agent once           # collect once, push once, exit — for cron
watchfor-agent run            # daemon, what the systemd unit runs
```

`check` works without a token and without a config file, so the first
thing you can do with the binary is see exactly what it would report.

### Verify a release

Every release ships `checksums.txt` and its signature (`checksums.txt.minisig`,
made in CI with the WatchFor release key). The agent checks both with the
key built into it:

```sh
# checksums.txt, checksums.txt.minisig and the tarball, all from the release page
tar -xzf watchfor-agent_0.7.0_linux_amd64.tar.gz watchfor-agent
./watchfor-agent verify -checksums checksums.txt watchfor-agent_0.7.0_linux_amd64.tar.gz
```

The signature is in minisign format, so `minisign -Vm checksums.txt -P
RWTUApo01PH7RyjD76wN2Vu7l5sO7Ys5psNQE9I7QYdWVfWSf0CetQje` checks it from
the outside too. Binaries are static, built from the tag by GoReleaser
with `CGO_ENABLED=0 -trimpath`, so a build from the same tag on your own
machine produces the same binary.

### Install by hand

No script, no surprises — four steps:

```sh
tar -xzf watchfor-agent_<version>_linux_amd64.tar.gz
sudo install -m 0755 watchfor-agent /usr/local/bin/watchfor-agent
sudo useradd --system --no-create-home --shell /usr/sbin/nologin --user-group watchfor-agent
sudo install -d -m 0750 -o root -g watchfor-agent /etc/watchfor-agent
sudo install -d -m 0700 -o watchfor-agent -g watchfor-agent /var/lib/watchfor-agent
```

Write the token and the config (the token file must be 0600 and owned by
the service user):

```sh
sudo sh -c 'umask 077; printf "%s\n" "<host-token>" > /etc/watchfor-agent/token'
sudo chown watchfor-agent:watchfor-agent /etc/watchfor-agent/token
sudo cp packaging/agent.example.yml /etc/watchfor-agent/agent.yml   # then edit
sudo -u watchfor-agent watchfor-agent check -config /etc/watchfor-agent/agent.yml
```

Then either the service or the cron job below.

### As a systemd service

```sh
sudo cp packaging/watchfor-agent.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now watchfor-agent
systemctl status watchfor-agent
journalctl -u watchfor-agent -f
```

### From cron (no daemon)

`once` collects, pushes one batch and exits. Cron's floor is a minute; the
shortest interval your plan allows applies on top of that.

```sh
sudo cp packaging/watchfor-agent.cron /etc/cron.d/watchfor-agent
```

```cron
* * * * * watchfor-agent /usr/local/bin/watchfor-agent once -config /etc/watchfor-agent/agent.yml
```

### Uninstall

```sh
sudo watchfor-agent uninstall          # shows what it found and asks; -yes to skip, -keep-config to leave agent.yml
```

Without the binary, the installer does the same:

```sh
curl --proto '=https' --tlsv1.2 -fsSL https://watchfor.io/agent/install.sh | sudo sh -s -- --uninstall
```

Both stop and remove the service and the update timer, the cron entry, the
state directory, the config directory (the token file is overwritten before
it is deleted), the `watchfor-agent` system user and the binary. Only
directories called `watchfor-agent` are removed recursively: a `spool.dir`
under another name is reported and left to you.

Remove the host in the dashboard as well; that revokes the token and deletes
its history.

## Upgrade

The server tells a running agent when a newer release exists (it rides on
the answer to every batch, so the agent never talks to anything but
`server.url`). The agent logs it once — `update available … sudo
watchfor-agent upgrade` — and the host page in the dashboard shows the
same next to the version. Then, on the server:

```sh
sudo watchfor-agent upgrade            # to the version the server reported
sudo watchfor-agent upgrade -check     # only report; exit 10 if one is available
sudo watchfor-agent upgrade -version 0.7.0
```

`upgrade` asks watchfor.io which release is current (or takes the version
the server reported to the running agent), downloads the files from
github.com/watchfor-io/agent over HTTPS (only watchfor.io and GitHub's
release hosts are accepted, redirects included), verifies `checksums.txt`
against the release key built into the binary —
the same key as above — checks the archive's sha256 against that signed
file, runs the new binary once to confirm it reports the expected version,
swaps it in with an atomic rename and restarts the service. It refuses to
downgrade unless told so (`-allow-downgrade`), and nothing is replaced
unless every check passed. Re-running the install script does the same
job with the same checks and keeps the token and `agent.yml`.

Hosts that should keep themselves current can opt in to a daily timer:

```sh
curl --proto '=https' --tlsv1.2 -fsSL https://watchfor.io/agent/install.sh | sudo sh -s -- --auto-update        # --no-auto-update removes it again
```

The timer (`watchfor-agent-update.timer`, once a day with up to six hours
of jitter) runs `watchfor-agent upgrade -if-available`, which acts only on
the version the server reported and never polls GitHub on its own. The
daemon itself stays unprivileged: the swap is done by the root-run
one-shot unit, not by the running agent.

The agent manages that timer itself, so you can switch it at any time
without the installer:

```sh
sudo watchfor-agent auto-update on       # installs + enables the timer, writes updates.auto: true
sudo watchfor-agent auto-update off      # removes the timer, writes updates.auto: false
watchfor-agent auto-update status
```

The setting lives in `agent.yml` as `updates.auto`; when it is `false`
the timer's run does nothing even if the timer is still there. Other
settings can be changed the same way, validated before they are written:

```sh
sudo watchfor-agent config set interval 30s
sudo watchfor-agent config set host.tags.env prod
watchfor-agent config get interval
watchfor-agent config keys
```

`config set` changes the one line when the key already has one of its own
and keeps every comment; a key it has to add goes through the YAML
encoder, which does not keep blank lines or comment alignment. A file
that is not there is not invented — `config init` writes one. `-config`
may stand before or after the word (`config get interval -config x`
and `config -config x get interval` are the same). It
refuses a value the agent would not start with (an interval below 5 s, an
invalid host name). Restart the service afterwards for anything but
`updates.auto`.

## The screen

`watchfor-agent` with no command opens one screen: what the machine is
doing right now on the left half of the job, everything that can be
changed on the right. It is the same binary, no extra install, and it
needs no token to look around.

```sh
watchfor-agent                          # the screen, reading /etc/watchfor-agent/agent.yml
watchfor-agent -config ./agent.yml      # a file somewhere else
sudo watchfor-agent                     # the same, with the keys that need root
```

The menu is three groups:

- **Overview** — System (machine, agent, service, where it sends),
  Live (CPU, memory, load, disks, network, busiest processes, sampled
  every two seconds from the real collectors, not from the server), and
  The batch (the exact samples the next push carries).
- **Settings** — server, host name and tags, interval, disks,
  interfaces, processes, logging, updates, facts, and the file itself.
  Processes to watch are ticked from the biggest ones running, shown
  by their full names even where the kernel cuts them at 15
  characters, or typed in by hand for a daemon the list does not
  offer or one that is down at the moment.
  Every pane reads the same in view and edit mode: Enter starts editing
  the pane in front of you, Space changes a value, Esc leaves it.
- **Maintenance** — Update, Health check, Service & log, Reinstall &
  reset.

There is nothing to learn: `↑↓` move, `Enter` opens what the cursor is
on, `Esc` goes back, `q` quits. Inside a section `Tab` steps between the
things it holds and comes round to the first one after the last, and the
arrow keys do the same once a list has no more options in that
direction. In a maintenance section Enter moves the
cursor into the pane, where each thing the agent can do is a row of its
own: choose it with Enter, or leave with Esc. A row that cannot run says
why on the row itself (`needs root — run: sudo systemctl start
watchfor-agent`, `it is already running`), and anything that replaces a
file or stops the agent asks first, with the safe answer under the
cursor.

Long lists — mounts, interfaces, processes — filter as you type after
`/`.

**Health check** answers, without a shell, what someone would otherwise
check by hand: is `agent.yml` there and does it parse, is the binary
writable by others, does the service user exist, is the token file
private, is the spool writable, is the service running and enabled, is
the update timer what the config says, and can the host reach WatchFor
over TLS. Each answer that can be repaired safely says how, and *Repair
what can be repaired* applies all of them (chmod, chown, mkdir,
`systemctl enable --now`).

Repairs run as root, so they are deliberately narrow: they touch only the
installer's own paths (`/etc/watchfor-agent/agent.yml`, `…/token`,
`/var/lib/watchfor-agent`, `/usr/local/bin/watchfor-agent`), work on open
file descriptors rather than paths, refuse symlinks, and refuse a
directory that anyone but root can write. A token file or spool that
`agent.yml` points at somewhere else still works, but the check only
reports it — on a machine with many users, a path an operator can be
talked into is a path an attacker can prepare. *Compare with the signed release* downloads
the release WatchFor signed and hashes the binary on disk against the one
inside it — the same verification `upgrade` does, applied to what is
already installed.

The same checks run without the screen, for a cron job, an Ansible task
or a quick look over SSH:

```sh
watchfor-agent health            # one line per check; exit 1 when something failed
watchfor-agent check             # the batch that would be sent; a -config path that is missing is an error
sudo watchfor-agent health -fix  # apply every safe repair, then check again
watchfor-agent health -deep -q   # also compare the binary with the signed release; print only problems
```

**Reinstall & reset** is the way back to a known state: *Reinstall the
binary* installs this version again over the one on disk (signature and
checksum verified first, service restarted), and *Reset agent.yml to
defaults* writes a fresh config with this machine's mounts and
interfaces, keeping the server address and token file and leaving the old
file as `agent.yml.bak`. Both ask before they act.

Changes are written the moment you confirm them, so a running agent picks
them up within half a minute (see [Configuration](#configuration)).

## Configuration

`/etc/watchfor-agent/agent.yml`. The installer writes it with
`watchfor-agent config init`: every key explained in a comment, and the
mounts, disks and interfaces found on the machine listed inside, so
choosing what to watch is editing a list, not guessing names. Every key
with its default is in
[`packaging/agent.example.yml`](packaging/agent.example.yml).

```yaml
server:
  url: https://ingest.watchfor.io
  token_file: /etc/watchfor-agent/token     # or token: env:WATCHFOR_TOKEN

host:
  name: web-01                              # default: hostname
  tags: { env: prod, role: web }

interval: 60s                               # the plan sets the floor; the server adjusts a running agent

spool:
  dir: /var/lib/watchfor-agent              # batches wait here while WatchFor is unreachable
  max_mb: 64

log:
  level: info                               # debug | info | warn | error

facts:
  cloud_metadata: true                      # false: never ask a cloud metadata service

modules:
  system: {}
  disk: {}
  network: {}
  processes:
    top: 10
```

### Choosing what to watch

Nothing is required: with `{}` a collector watches everything sensible.
Narrow it when a box has dozens of mounts or interfaces and one or two
matter:

```yaml
modules:
  disk:
    mounts: [/, /data]                      # only these (default: every real filesystem, minus removable
                                            # media, /tmp, snaps and FUSE mounts — list one to watch it)
    devices: [nvme0n1]                      # I/O rates for these whole disks only (dm-0, dm-1… are
                                            # the encrypted or LVM volumes stacked on one)
    ignore_fs: [nfs, fuse.sshfs]            # skip filesystem types on top of the built-in pseudo-fs list
    io: false                               # no I/O rates at all
  network:
    interfaces: [eth0]                      # only these (default: all but lo, veth*, docker*, br-*, virbr*)
  processes:
    top: 5                                  # top CPU and memory consumers per batch
    watch:                                  # named processes: running or not, CPU, memory — alertable
      - name: nginx                         # comm, the 15-char kernel name
      - cmdline: postgres -D                # substring of the command line
        user: postgres
  system:
    per_core: true                          # one cpu.usage_pct series per core
```

A running agent watches `agent.yml` (one `stat()` every 30 seconds) and
restarts itself on a change that validates — so `configure`, an editor or
Ansible take effect within half a minute, without a restart by hand. A change the agent would not accept is
logged and ignored; the running config stays. Batches keep flowing: the
spool holds the one or two seconds of the restart.

`watchfor-agent config detect` prints what the collectors see — CPU,
memory, hardware, every mount with its size, whole disks, interfaces — so
you can copy names from it. Disks are drawn as the tree they are, so an
encrypted machine's `dm-0` and `dm-1` read as what they are rather than
as two more disks:

```
disks   nvme0n1                            238 GB  SSD
        └─ dm-0 · dm_crypt-0               235 GB  encrypted
           └─ dm-1 · ubuntu--vg-ubuntu--lv 235 GB  LVM
```

`watchfor-agent config keys` lists the keys
`config set` can change in place (`sudo watchfor-agent config set
interval 30s`); the file is validated before it is written, and the
service picks it up on restart.

### Logging

The agent logs to the journal (`journalctl -u watchfor-agent`) at
`log.level: info`: start, interval changes, failed sends, module errors,
an available update. Set `debug` when you want to see what it does:
every push (samples, bytes, whether facts went along), every spooled or
replayed batch, the host facts it collected, which cloud it recognised and
whether it asked the metadata service. `sudo watchfor-agent config set
log.level debug` and a restart; back to `info` the same way.

### What the agent talks to

Outbound HTTPS only; nothing listens.

- `ingest.watchfor.io` (or `server.url`): the metric batches, once per
  `interval`.
- `watchfor.io/agent/latest` and `github.com/watchfor-io/agent` release
  files: only during `watchfor-agent upgrade` (manual or the opt-in daily
  timer), never from the running collector.
- The cloud's metadata service (link-local, e.g. `169.254.169.254`): once
  an hour with the facts, **only** when the firmware says the machine is on
  that cloud, and only two kinds of path — the instance size where the
  firmware does not carry it (Google Compute Engine, Azure, Oracle Cloud,
  Alibaba Cloud, Tencent Cloud, Scaleway; AWS Nitro puts the type in the
  firmware) and the public addresses the cloud assigned to the instance
  (AWS, Google Compute Engine, Azure, Alibaba Cloud, Tencent Cloud,
  Scaleway, DigitalOcean, Hetzner, Vultr, Linode, OpenStack; Oracle Cloud
  does not publish them) — never credentials. Only real public addresses
  are kept: a private, link-local or malformed answer is dropped.
  `facts.cloud_metadata: false` switches both lookups off entirely; a
  blocked metadata endpoint just times out after 0.7 s and the facts go
  without the size and the addresses.

Rules that hold for the whole file:


- Unknown keys are an error. A typo fails at start, it does not silently
  monitor nothing.
- The token belongs in a file that must be mode 0600, or in an
  environment variable (`token: env:NAME`). A literal `token:` in
  agent.yml is accepted but the health check warns about it, and the
  screen never shows it back. The token comes from a file that must be mode
  0600, or from an environment variable. A world-readable token file is a
  start-up error.
- `server.url` must be `https://` (plain `http://` is allowed for
  `localhost` only). `ca_file` pins a CA instead of the system store.
- `interval` has a floor of 5 seconds. The server answers each batch with the
  shortest interval the account allows; when that is longer, the agent stretches
  to it (logged once) and drops back as soon as the server allows it again.

## Modules

| Module | Samples | Source |
| --- | --- | --- |
| `system` | `cpu.usage_pct`, `cpu.user_pct`, `cpu.system_pct`, `cpu.iowait_pct`, `cpu.steal_pct`, `cpu.idle_pct` (per core with `per_core: true`), `cpu.count`, `load.1/5/15`, `mem.total/used/available/free/cached/buffers_bytes`, `mem.used_pct`, `swap.total/used_bytes`, `swap.used_pct`, `sys.uptime_s`, `sys.procs_running/blocked`, `sys.context_switches_per_s`, `sys.forks_per_s` | `/proc/stat`, `/proc/meminfo`, `/proc/loadavg`, `/proc/uptime` |
| `disk` | per mount `{mount, device, fs}`: `disk.total/used/free_bytes`, `disk.used_pct`, `disk.inodes_used_pct`; per whole disk `{device}`: `diskio.read/write_bytes_per_s`, `diskio.read/write_ops_per_s`, `diskio.util_pct`, `diskio.await_ms`, `diskio.in_progress` | `statfs(2)`, `/proc/self/mounts`, `/proc/diskstats` |
| `network` | per interface `{if}`: `net.rx/tx_bytes_per_s`, `net.rx/tx_packets_per_s`, `net.rx/tx_errors_per_s`, `net.rx/tx_dropped_per_s`; `net.sockets_used`, `net.tcp_in_use`, `net.tcp_time_wait`, `net.tcp_orphan`, `net.udp_in_use` | `/proc/net/dev`, `/proc/net/sockstat` |
| `processes` | `proc.total/running/sleeping/blocked/zombie/stopped`, `proc.threads`; top N `{pid, name, user}`: `proc.cpu_pct`, `proc.rss_bytes`; per watch `{watch}`: `procwatch.count`, `procwatch.cpu_pct`, `procwatch.rss_bytes`, `procwatch.uptime_s` | `/proc/<pid>/{stat,statm,status,cmdline}` |

Rates need two samples, so the first tick after start reports gauges only;
`once` takes two samples one second apart for the same reason.

Host facts (OS, kernel, CPU model and count, hardware, memory, virtualization, boot
time, routable addresses per interface) travel with the first batch and
then once an hour.

`hardware` is the machine as the firmware describes it — the server model
on metal (`Dell Inc. PowerEdge R640`), the instance type on a cloud. The
provider comes from the DMI tables; where those do not carry the size, the
agent asks that provider's metadata service once an hour, link-local and
with a sub-second timeout, and nothing else: AWS (IMDSv2), Google Compute
Engine, Azure, Oracle Cloud, Alibaba Cloud, Tencent Cloud and Scaleway.
`virtualization` names the provider when known (`ec2`, `gce`, `azure`,
`oci`, `alibaba`, `tencent`, `scaleway`, `digitalocean`, `hetzner`,
`vultr`, `linode`, `openstack`), else the hypervisor (`kvm`, `vmware`,
`hyperv`, `xen`, `virtualbox`) or the container runtime (`docker`, `lxc`,
`kubernetes`). ARM CPUs, which report no model name, are named from the
implementer and part numbers (`ARM Neoverse-N1`, or `AWS Graviton2
(Neoverse-N1)` on EC2).

## The systemd unit

`packaging/watchfor-agent.service` runs the agent as the `watchfor-agent`
user with `ProtectSystem=strict`, an empty capability set and a memory cap.
Everything the four modules need is readable by an unprivileged user on a
standard kernel. Two things are not:

- per-process I/O counters (`/proc/<pid>/io`) — not collected yet;
- some `/sys` entries and container runtime sockets — future modules.

When a module needs them, grant the capability in a drop-in
(`sudo systemctl edit watchfor-agent`), not in the unit file itself — an
upgrade rewrites the unit and would undo the edit:

```ini
[Service]
CapabilityBoundingSet=CAP_DAC_READ_SEARCH
AmbientCapabilities=CAP_DAC_READ_SEARCH
```

That grants read access everywhere without running as root.

Exit status 3 means the server has rejected the token repeatedly for over
an hour (one rejection is spooled and retried with a growing backoff);
the unit does not
restart on it, because restarting would only repeat the rejection.

## When the server is unreachable

Batches are written to `/var/lib/watchfor-agent` (up to `spool.max_mb`,
oldest dropped first) and replayed after the next successful push. `once`
uses the same directory, so a cron-driven host catches up on its next run.

## Wire format

`POST /v1/agent/metrics`, gzip-compressed JSON, `Authorization: Bearer
<token>`. `watchfor-agent check` prints the exact document:

```json
{
  "v": 1,
  "agent": "0.7.0",
  "host": { "id": "3f9a…", "name": "web-01", "tags": {"env": "prod"},
            "os": "Ubuntu 24.04.1 LTS", "kernel": "6.8.0-45-generic", "arch": "amd64" },
  "facts": { "cpu_model": "AMD EPYC 7B13", "cpu_cores": 4, "mem_total": 8329273344,
             "hardware": "Amazon EC2 t3.medium", "virtualization": "ec2", "boot_time": 1757600000,
             "addresses": { "eth0": ["10.0.4.7", "2001:db8::7"] } },
  "samples": [
    { "t": 1757836800, "m": "cpu.usage_pct", "v": 12.4 },
    { "t": 1757836800, "m": "disk.used_pct", "v": 71.2, "l": { "mount": "/", "device": "/dev/vda1", "fs": "ext4" } }
  ]
}
```

`host.id` is a hash of `/etc/machine-id`, not the id itself: stable across
reinstalls of the agent, unlinkable to anything else that uses the machine id.

## Building from source

```sh
git clone https://github.com/watchfor-io/agent.git && cd agent
make build          # CGO_ENABLED=0, -trimpath, version from git describe
make test           # go test -race
make lint           # vet, staticcheck, govulncheck, gosec
./watchfor-agent version
```

Go 1.26 or newer (the toolchain line in `go.mod` fetches the exact patch
release). The agent is a Linux program: on any other system the tree
still builds and vets, but the binary only says so and exits. Outside the standard library the agent itself uses
`gopkg.in/yaml.v3` and `golang.org/x/crypto`; the screen adds the Charm
libraries (bubbletea, huh, lipgloss). Cross-compile with
`GOARCH=arm64 make build`.
Releases are built by GoReleaser from a tag (`git tag v0.1.0 && git push
--tags` runs `.github/workflows/release.yml`), reproducibly, with a signed
`checksums.txt` next to the tarballs. A second job rebuilds the release from
the tag on a fresh runner and fails if a byte differs, and every asset
carries a GitHub build-provenance attestation:

```sh
gh attestation verify watchfor-agent_0.7.0_linux_amd64.tar.gz --repo watchfor-io/agent
```

### Exit codes

| Code | Meaning |
| --- | --- |
| 0 | `once`/`check` finished; `run` stopped on SIGINT/SIGTERM |
| 1 | configuration or runtime error (see the log) |
| 2 | the command line made no sense |
| 3 | the server has rejected the token repeatedly for over an hour — revoked, rotated, or the plan has no server monitoring; a single rejection is spooled and retried; the unit does not restart on 3 |
| 10 | `upgrade -check`: a newer release is available |
| 1 (health) | `health`: at least one check failed |
| 75 | `agent.yml` changed and validated; the daemon asks to be started again on the new file, which the unit does at once |

## Contributing a module

How changes are made and reviewed is in [CONTRIBUTING.md](CONTRIBUTING.md).

A module is one package under `internal/modules/` that registers a factory
in `init()` and implements `Collect(ctx, *metric.Batch) error`. Its
configuration is the YAML subtree under `modules.<name>`, decoded strictly.
Keep the parsing in `internal/procfs` if it reads a kernel file, ship a
fixture from a real machine with the test, and never shell out.

## License

Apache-2.0.
