# Changelog

Releases are tagged `vX.Y.Z`; every tarball ships with a `checksums.txt`
signed by the WatchFor release key. Dates are the day the tag was pushed.

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
