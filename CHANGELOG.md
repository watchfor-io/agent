# Changelog

Releases are tagged `vX.Y.Z`; every tarball ships with a `checksums.txt`
signed by the WatchFor release key. Dates are the day the tag was pushed.

## Unreleased

- **A hung network mount no longer silences the whole agent.** `statfs` on
  a hard NFS/CIFS mount whose server has gone away blocks in the kernel,
  and the disk module asked every mount in turn inside the collection
  tick, so one dead mount stopped every metric from being reported. Each
  mount is now asked with a 2-second limit (a few at a time); one that
  does not answer is skipped, named in the module's error, and asked
  again after five minutes, while the other disks and I/O rates keep
  being reported. Stuck calls are never stacked: at most one per mount
  point and 16 in all.
- **The server can no longer stretch the push interval without limit.**
  The interval the server returns was applied as is, so a faulty or
  hostile answer could make an agent report once a year and look silent.
  It is now capped at one hour (the configured interval is still the
  floor), with a warning when the cap applies.
- **Process names can no longer spoof labels with invisible Unicode.**
  Control characters were already replaced, but bidi overrides and
  isolates (U+202A–202E, U+2066–2069, U+200E/200F, U+061C), zero-width
  characters (U+200B–200D, U+2060, U+FEFF) and the line/paragraph
  separators passed through, so a process could make its name read as
  another one on the screen or the dashboard. They are now shown as `�`.
- **Cloud metadata requests ignore `HTTP(S)_PROXY`.** With a proxy in the
  service environment, the instance-metadata requests (including the AWS
  IMDSv2 session token) went through the proxy. They now always go
  straight to the link-local address, with short connect and response
  timeouts.
- **Safer state and spool files when the agent runs as root.** The
  update hint was written through a fixed temp name, so a symlink planted
  in the state directory could make a root-run `run`/`once` overwrite
  another file, and a FIFO or symlink planted in the spool could hang the
  agent or make it upload a file it was pointed at. The hint is now
  written to a fresh `O_EXCL` temp file, fsynced and renamed into place;
  spool entries that are not plain, singly linked files are removed
  instead of read, and batches are read without following links or
  blocking.
- **Release signing is isolated from the build.** GoReleaser is pinned to
  an exact version (installed with `go install`, checked against the Go
  checksum database) and no longer holds the signing key: it uploads an
  unsigned draft, the binaries are reproduced from the tag on a fresh
  runner, a separate approval-gated job that only receives the verified
  `checksums.txt` and the key signs it, and the release is published only
  after the signature has been checked with the key built into the tagged
  agent. Build caches are off in all release jobs.
- SECURITY.md now describes accurately what happens to the server's
  newer-version hint: the opt-in root update unit acts on it, through the
  full signature chain.

## 0.8.2 — 2026-10-15

- **Built with Go 1.27.2.** 0.8.1 and earlier were compiled with Go 1.26.8,
  whose `net/http` and HTTP/2 code has known vulnerabilities (GO-2026-6611,
  GO-2026-6613, GO-2026-6617). The agent talks HTTPS to WatchFor and to the
  cloud metadata service, so every binary is rebuilt; behaviour is
  unchanged. Agents update themselves to it on their own schedule.
- Dependencies: `charmbracelet/x/ansi` 0.11.9 and its indirect updates.

## 0.8.1 — 2026-10-04

- **Fresh installs work again.** The installer writes the first agent.yml
  with `watchfor-agent config init -server … -token-file … -spool-dir …
  -out …`, but the flags after `init` were read by the `config` command
  first and rejected ("flag provided but not defined: -server"), so every
  install on a new host stopped at "could not write agent.yml". Upgrades
  were not affected. `config init` now gets its own flags, and
  `-config` before the subcommand still names the file it writes.

## 0.8.0 — 2026-09-21

- **The host's public addresses, from the cloud.** On a cloud instance the
  elastic/floating IPv4 never appears on the interface (the provider NATs
  it), so the agent asked its metadata service only for the instance size
  and the dashboard could show a public address only as "the one batches
  arrive from". With `facts.cloud_metadata` on the agent now also asks for
  the addresses the cloud assigned from the outside — IPv4 and IPv6 — on
  AWS, Google Compute Engine, Azure, Alibaba, Tencent, Scaleway,
  DigitalOcean, Hetzner, Vultr, Linode and OpenStack (Oracle Cloud does
  not publish them), and reports them as `public_addresses`. Only real
  public addresses get through: a metadata answer that is private,
  link-local or not an address at all is dropped. The screen shows them
  under "address".

## 0.7.1 — 2026-09-21

- The release's reproducibility check tripped over its own downloads: it
  saved the published tarball into the source tree before rebuilding the
  second binary, and Go stamps a dirty tree into the build. The 0.7.0
  binaries do reproduce; the check now builds first and writes nothing
  into the tree. No change to the agent itself.

## 0.7.0 — 2026-09-21

- **One screen for the whole agent.** `watchfor-agent` with no command opens
  it: live readings, every setting editable in place, the service and its
  log, updates. Arrow keys move, Enter chooses, Esc goes back. Long lists
  are filtered by typing `/`.
- **Health check** of the installation with safe one-step repairs, a
  comparison of the installed binary against the signed release, and a
  `health` command for scripts.
- **Reinstall and factory reset** from the screen, both asking first.
- **Processes to watch** are picked from what is running, shown by their
  full names even where the kernel cuts them at 15 characters, or typed in
  by hand.
- **Disks are listed as the tree they are**: encrypted and LVM volumes
  under the disk that holds them.
- **Config is watched.** A changed, valid `agent.yml` restarts the agent
  within half a minute (exit 75 for the unit); an invalid one is logged
  and ignored.
- `facts.cloud_metadata: false` switches the cloud instance-size lookup off
  everywhere, `log.level: debug` shows every push and lookup.
- The default interval is 60 s; the plan's floor still applies.
- Hardening after an audit: root repairs touch only the installer's paths
  and work on file descriptors; the update lock lives in root-only `/run`;
  state files are read without following links; every string from `/proc`,
  `/sys` or another process is cleaned before it is shown or sent; the
  metadata answer must look like an instance size; the installer pins HTTPS,
  redirects included, and the push client refuses redirects; the service unit gets
  `AF_NETLINK`, `StateDirectory` and more syscall filtering, the update
  unit a full hardening set.

## Before 0.7.0

Builds 0.1 to 0.6 were pre-release and are no longer published. A host
still running one moves to 0.7.0 with `watchfor-agent update`, or the
daily timer does it.
