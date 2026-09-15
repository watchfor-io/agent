# watchfor-agent

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

Prefer to look before you run? Download the tarball from the releases page,
put the binary anywhere, and run it by hand:

```sh
watchfor-agent check          # collect once, print the batch, send nothing
watchfor-agent once           # collect once, push once, exit — for cron
watchfor-agent run            # daemon, what the systemd unit runs
```

`check` works without a token and without a config file, so the first
thing you can do with the binary is see exactly what it would report.

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
- `interval` has a floor of 5 seconds.

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
time) travel with the first batch and then once an hour.

## Running as a service

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
             "virtualization": "kvm", "boot_time": 1757600000 },
  "samples": [
    { "t": 1757836800, "m": "cpu.usage_pct", "v": 12.4 },
    { "t": 1757836800, "m": "disk.used_pct", "v": 71.2, "l": { "mount": "/", "device": "/dev/vda1", "fs": "ext4" } }
  ]
}
```

`host.id` is a hash of `/etc/machine-id`, not the id itself: stable across
reinstalls of the agent, unlinkable to anything else that uses the machine id.

## Building

```sh
make build          # CGO_ENABLED=0, -trimpath, version from git describe
make test
make lint           # vet, staticcheck, govulncheck
```

Go 1.24 or newer. The only dependency outside the standard library is
`gopkg.in/yaml.v3`. Releases are built by GoReleaser from a tag, reproducibly,
with a `checksums.txt` next to the tarballs.

## Contributing a module

A module is one package under `internal/modules/` that registers a factory
in `init()` and implements `Collect(ctx, *metric.Batch) error`. Its
configuration is the YAML subtree under `modules.<name>`, decoded strictly.
Keep the parsing in `internal/procfs` if it reads a kernel file, ship a
fixture from a real machine with the test, and never shell out.

## License

Apache-2.0.
