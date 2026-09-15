# watchfor-agent

[![ci](https://github.com/watchfor-io/agent/actions/workflows/ci.yml/badge.svg)](https://github.com/watchfor-io/agent/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/watchfor-io/agent?display_name=tag)](https://github.com/watchfor-io/agent/releases/latest)
[![license](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

Host monitoring agent for [WatchFor](https://watchfor.io). One static
binary, one YAML file, outbound HTTPS only. It reads `/proc` and `/sys`,
turns them into a batch of samples every few seconds and pushes the batch
to WatchFor — no inbound ports, no credentials outside the host, nothing
executed from configuration.

Linux only for now (amd64, arm64). Windows is planned; macOS is not.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/watchfor-io/agent/main/packaging/install.sh \
  | sudo sh -s -- --token <host-token>
```

The script downloads the release for your architecture, verifies the
checksum (and the minisign signature when `minisign` is installed), creates
the unprivileged `watchfor-agent` user, writes the token to
`/etc/watchfor-agent/token` with mode 0600 and enables the systemd unit.
The host token comes from the WatchFor dashboard, one per host.

It needs root, systemd, `curl`, `tar`, `sha256sum`, `useradd` and
`minisign` — all checked before anything is touched. A missing tool does
not end the script halfway through: on a terminal, run with sudo, it
shows the one install command for your distribution and offers to run it
(`--yes` skips the question); otherwise it prints that command and stops.
Progress, colours and the summary box appear only on a terminal; `--plain`
(or `NO_COLOR=1`) keeps the output to plain lines for logs and CI. The release signature is verified by default. Once an
agent 0.3.0 or newer is installed, upgrades no longer need minisign: the
installed agent verifies the release with the key built into it. On a box
where you would rather not install minisign at all, `--skip-signature`
trusts the sha256 checksum alone:

```sh
curl -fsSL https://raw.githubusercontent.com/watchfor-io/agent/main/packaging/install.sh \
  | sudo sh -s -- --token <host-token> --skip-signature
```

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

Every release ships `checksums.txt` and its minisign signature.

```sh
sha256sum -c --ignore-missing checksums.txt
minisign -Vm checksums.txt -P RWTUApo01PH7RyjD76wN2Vu7l5sO7Ys5psNQE9I7QYdWVfWSf0CetQje
```

The install script does both (the signature when `minisign` is on the
machine). Binaries are static, built from the tag by GoReleaser with
`CGO_ENABLED=0 -trimpath`, so a build from the same tag on your own machine
produces the same binary.

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
sudo systemctl disable --now watchfor-agent
sudo rm -f /etc/systemd/system/watchfor-agent.service /etc/cron.d/watchfor-agent /usr/local/bin/watchfor-agent
sudo rm -rf /etc/watchfor-agent /var/lib/watchfor-agent
sudo userdel watchfor-agent
```

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
sudo watchfor-agent upgrade -version 0.3.0
```

`upgrade` downloads the release from github.com/watchfor-io/agent over
HTTPS (only GitHub's release hosts are accepted, redirects included),
verifies `checksums.txt` against the minisign key built into the binary —
the same key as above — checks the archive's sha256 against that signed
file, runs the new binary once to confirm it reports the expected version,
swaps it in with an atomic rename and restarts the service. It refuses to
downgrade unless told so (`-allow-downgrade`), and nothing is replaced
unless every check passed. Re-running the install script does the same
job with the same checks and keeps the token and `agent.yml`.

Hosts that should keep themselves current can opt in to a daily timer:

```sh
curl -fsSL https://raw.githubusercontent.com/watchfor-io/agent/main/packaging/install.sh \
  | sudo sh -s -- --auto-update        # --no-auto-update removes it again
```

The timer (`watchfor-agent-update.timer`, once a day with up to six hours
of jitter) runs `watchfor-agent upgrade -if-available`, which acts only on
the version the server reported and never polls GitHub on its own. The
daemon itself stays unprivileged: the swap is done by the root-run
one-shot unit, not by the running agent.

## Configuration

`/etc/watchfor-agent/agent.yml` — every key with its default is in
[`packaging/agent.example.yml`](packaging/agent.example.yml). The short
version:

```yaml
server:
  url: https://ingest.watchfor.io
  token_file: /etc/watchfor-agent/token     # or token: env:WATCHFOR_TOKEN

host:
  name: web-01                              # default: hostname
  tags: { env: prod, role: web }

interval: 15s

modules:
  system: {}
  disk: {}
  network: {}
  processes:
    top: 10
    watch:
      - name: nginx
      - cmdline: postgres -D
        user: postgres
```

Rules that hold for the whole file:

- Unknown keys are an error. A typo fails at start, it does not silently
  monitor nothing.
- Secrets are never inline. The token comes from a file that must be mode
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

Host facts (OS, kernel, CPU model and count, memory, virtualization, boot
time, routable addresses per interface) travel with the first batch and
then once an hour.

## The systemd unit

`packaging/watchfor-agent.service` runs the agent as the `watchfor-agent`
user with `ProtectSystem=strict`, an empty capability set and a memory cap.
Everything the four modules need is readable by an unprivileged user on a
standard kernel. Two things are not:

- per-process I/O counters (`/proc/<pid>/io`) — not collected yet;
- some `/sys` entries and container runtime sockets — future modules.

When a module needs them, uncomment `AmbientCapabilities=CAP_DAC_READ_SEARCH`
in the unit. That grants read access everywhere without running as root.

Exit status 3 means the server rejected the token; the unit does not
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
  "agent": "0.1.0",
  "host": { "id": "3f9a…", "name": "web-01", "tags": {"env": "prod"},
            "os": "Ubuntu 24.04.1 LTS", "kernel": "6.8.0-45-generic", "arch": "amd64" },
  "facts": { "cpu_model": "AMD EPYC 7B13", "cpu_cores": 4, "mem_total": 8329273344,
             "virtualization": "kvm", "boot_time": 1757600000,
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
make lint           # vet, staticcheck, govulncheck
./watchfor-agent version
```

Go 1.24 or newer, nothing else: the only dependency outside the standard
library is `gopkg.in/yaml.v3`. Cross-compile with `GOARCH=arm64 make build`.
Releases are built by GoReleaser from a tag (`git tag v0.1.0 && git push
--tags` runs `.github/workflows/release.yml`), reproducibly, with a signed
`checksums.txt` next to the tarballs.

### Exit codes

| Code | Meaning |
| --- | --- |
| 0 | `once`/`check` finished; `run` stopped on SIGINT/SIGTERM |
| 1 | configuration or runtime error (see the log) |
| 3 | the server rejected the token — revoked, rotated, or the plan has no server monitoring; the unit does not restart on it |

## Contributing a module

A module is one package under `internal/modules/` that registers a factory
in `init()` and implements `Collect(ctx, *metric.Batch) error`. Its
configuration is the YAML subtree under `modules.<name>`, decoded strictly.
Keep the parsing in `internal/procfs` if it reads a kernel file, ship a
fixture from a real machine with the test, and never shell out.

## License

Apache-2.0.
