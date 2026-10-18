# Changelog

Releases are tagged `vX.Y.Z`; every tarball ships with a `checksums.txt`
signed by the WatchFor release key. Dates are the day the tag was pushed.

## Unreleased

- **Hosts with many interfaces no longer lose their batches.** A host with
  about 1300 interfaces (a Kubernetes node has one per pod) made a batch of
  over 10 000 samples; ingest refused it with 413 and the agent dropped it,
  every time. Requests now carry at most 5000 samples (256 KiB), a 413 halves
  the request and sends the halves, and the default interface list leaves
  out the CNI plumbing too: Calico (`cali*`, `vxlan.calico`, `tunl*`),
  Flannel, `cni*`, `kube-ipvs*`, Cilium, LXC/LXD (`lxc*`), Weave and libvirt's
  `vnet*`.
- **Catching up after an outage takes minutes, not hours.** The agent
  replayed four spooled batches per push, so a day offline at one batch a
  minute took about six hours to send. A replay request now merges
  consecutive spooled batches (up to 5000 samples), and the number of
  replay requests follows the per-host budget ingest allows at the
  interval in use.
- **A clock stepped backwards no longer throws away the newest batch.**
  Spool files were named by the clock: after a backward step a new batch
  sorted as the oldest, was trimmed at once when the spool was full — while
  the log said "batch spooled" — and replayed out of order. They are now
  numbered in sequence, continued across restarts; the batch just written
  is never trimmed; and when the disk is full the oldest batches make room
  for the newest instead of the new one being refused. Batches given up to
  make room are logged.
- **A dead hard mount can no longer hang `once`, `config init` or a
  stopping daemon.** A `statfs` stuck on a hard NFS/SMB/FUSE mount whose
  server is gone keeps its thread in the kernel, so the process never
  finishes exiting: `once` did its work and then stayed behind as a zombie
  (one more every minute under cron), and the installer waited for ever on
  `config init`. Commands that must end — and the daemon, for network and
  FUSE mounts — now ask through a helper process and leave one that does
  not answer behind; the mount is remembered in the state folder
  (`hung-mounts`) and not asked again while that helper is stuck.
- **Hosts that 0.8 stopped over a plan pause start again.** 0.8 counted
  ingest's `plan_forbidden` as a rejected token and, after two hours,
  stopped for good (exit 3) — and kept stopping on every start, even after
  the plan was restored. The rejected-token record now keeps the server's
  reason; a record written for a plan pause, or older than the token file,
  is removed at start; one without a reason (written by 0.8) needs one
  more rejection before it counts. `upgrade` starts a service that had
  stopped itself with exit 3, so the new release can look again, and
  `health` shows the record with a repair that removes a stale one. A 0.8
  host already stopped that way starts on `sudo systemctl restart
  watchfor-agent`, a re-run of the installer, or a reboot.
- **Reinstalling over a kept config works.** `--uninstall --keep-config`
  hands the kept token to root (its account is deleted, and the uid may go
  to the next account created); a reinstall then left it root's, the agent
  could not read it and restarted every five seconds — while the installer
  printed "running". The installer now gives the token back to the service
  account, and reports success only once the service has stayed up for a
  few seconds; otherwise it fails with the service's last log lines.
- **An `agent.yml` others can write is refused.** Whoever can change
  `agent.yml` can point `server.url` at their own server and receive the
  token with the next push, yet a world-writable file owned by another
  user loaded fine and `health` only warned. `run` and `once` now refuse a
  group- or world-writable `agent.yml`, and a folder around it or the token
  file that others may write (a sticky folder like `/tmp` is fine);
  `health` fails on those and on an `agent.yml` owned by another account,
  and repairs the installed file (0644, root).
- **A malformed token file is a config error, not an outage.** A token file
  with a second line or a stray control character failed every push with
  an invalid Authorization header, which the agent took for network
  trouble and retried into the spool; a FIFO in its place blocked the
  start. Both are now refused at load with the reason.
- **Cron honours what the server said.** Each `once` is a new process, so
  a `Retry-After`, the backoff after a rejected token or while the server
  fails, and a plan pause were forgotten a minute later and the server
  asked again. They are now kept in the state folder (`pause-state`), the
  next run waits with its batch in the spool, and such a run exits 0. At
  `log.level: info` a run that went well prints nothing (warnings and
  errors still do), and the cron file sets `MAILTO=""`.
- **A redirect no longer drops the batch.** A `3xx` (a proxy, a moved URL)
  dropped the batch; it is now spooled and retried, never followed, and the
  log says where it pointed. Error bodies from the server are cleaned of
  control characters and cut to 200 bytes before they reach the log.
- **`processes.watch` with `cmdline:` sees the whole command line.** Only
  the first 256 bytes were matched, so a JVM's main class after a long
  classpath was never found. Matching now reads up to 4 KB; nothing more is
  sent.
- **Container processes no longer show up as the agent's user.** A
  container without a user namespace runs its own uid 999 as host uid 999,
  and `useradd --system` on the host also hands out 999 first — so on a
  host running, for example, mailcow, `ps` and the agent's process list
  named SOGo's ~20 `sogod` workers `watchfor-agent`, as if the agent had
  started them (it never runs anything but `systemctl`). The process list
  now reports a process in another container with its bare uid and a new
  `container` label (`docker:3f2a9c1b7d4e`, `podman:…`, `kubernetes:…`,
  `lxc:<name>` …), and marks the agent's own process with `self="true"`.
  Watches with `user:` no longer count container processes as that host
  user's.
- **`health` finds a shared uid and fixes it.** When anything but the
  agent runs with its uid it names the processes and the containers and
  says what it means: to the kernel they are one user, so they can read
  the agent's token and spool, and the unit's `RemoveIPC=` removes their
  shared memory whenever the agent stops. `health -fix` (root) moves the
  account and its group to the first id in 60578–61183 that no account,
  group or process uses, and hands the agent's files over. New installs
  pick their uid from that band right away; existing installs keep theirs
  until you run the fix.
- **A host paused by the plan is no longer treated as a dead token.**
  When the organization was over its host limit, WatchFor's
  `plan_forbidden` answer counted like a revoked token: after about two
  hours the agent stopped for good (exit 3), every later start — even
  `systemctl restart` — stopped at once, and the log blamed a removed host
  or a rotated token. A paused host now asks again after 1, 5, 15 and then
  every 30 minutes, drops its batches meanwhile, and resumes by itself.
  A token that really was rejected still stops the agent, but a start by
  hand or a boot always tries once more.
- **A rotated token takes effect.** Re-running the installer with a new
  `--token` wrote the file but left the agent running on the old token
  until it was rejected for two hours. The agent now watches its token
  file and restarts on a new token within 30 seconds, and the installer
  restarts it too.
- **The agent cannot pile up or wedge.** `once` (cron) holds a lock in
  the state folder, so a run that hangs on a dead network or mount no
  longer has another stacked on it every minute; it finishes within 40
  seconds, is ended at 55, and exits 1 when its batch was not accepted.
  Only one agent runs per state folder: with the service and the cron
  entry both installed the cron run steps aside, and a second `run`
  refuses with the reason. A daemon whose collection round is stuck ends
  itself with the goroutine stacks in the journal, and systemd starts a
  fresh one. While the server keeps failing the agent backs off — doubling
  up to five minutes, jittered — instead of asking every tick. Every
  command the agent runs (`systemctl`, `journalctl`, `useradd` …) has a
  deadline and is always reaped.
- **`health` says more.** "WatchFor accepts this host" shows the last
  push's answer (accepted, token rejected, paused by the plan, no answer);
  the reachability check uses `server.ca_file` like the agent does; the
  service and a cron entry both installed is pointed out (and fixed by
  removing the cron entry); a unit edited locally or left over from an
  earlier release is named; and after exit 3 there is no "start it again"
  that would only exit 3 again.
- **Upgrades bring the unit along.** `upgrade` swapped only the binary,
  so changes to the systemd unit never reached upgraded hosts. The new
  binary now replaces the service unit and cron file when they are an
  earlier release's, untouched; an edited unit is left alone.
- **Agent footprint in every batch:** `agent.rss_bytes`, `agent.cpu_pct`
  and `agent.spool_batches`.
- **Installer:** the whole script runs from a function called on its last
  line, so a download cut short runs nothing; the release tag from
  watchfor.io must be exactly `vX.Y.Z`; a leftover cron entry is removed
  when the service is installed; `--uninstall` and `watchfor-agent
  uninstall` say so, and exit non-zero, when `userdel` refuses (a process
  still runs as the account) instead of reporting success.
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
