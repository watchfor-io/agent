# Changelog

Releases are tagged `vX.Y.Z`; every tarball ships with a `checksums.txt`
signed by the WatchFor release key. Dates are the day the tag was pushed.

## Unreleased — 0.8.0

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
