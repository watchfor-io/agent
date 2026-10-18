# Changelog

Releases are tagged `vX.Y.Z`; every tarball ships with a `checksums.txt`
signed by the WatchFor release key. Dates are the day the tag was pushed.

## Unreleased

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
