# Security

## Reporting

Please report vulnerabilities privately through GitHub's
[private vulnerability reporting](https://github.com/watchfor-io/agent/security/advisories/new)
on this repository, or by email to hello@watchfor.io with "security" in the
subject. You will get an answer within three working days. Please do not open
a public issue for anything that could be exploited before a fix is out.

Supported: the latest release. Fixes ship as a new release, never as a
rewritten one.

## What the agent does, and does not do

- Outbound HTTPS to one URL, `server.url`, with a bearer token. Nothing listens.
- Reads `/proc`, `/sys` and mount usage. Runs nothing from configuration and
  never accepts commands from the server — the server's whole answer to a
  batch is how many samples it kept and the shortest interval the account
  allows.
- The token comes from a file that must be mode 0600 or from an environment
  variable; it is never written to logs or to the spool.
- The systemd unit runs as an unprivileged user with `ProtectSystem=strict`,
  an empty capability set, `NoNewPrivileges`, a memory cap and a task cap.
- `host.id` is a hash of `/etc/machine-id`, not the id itself.
- Releases are static binaries built from the tag by GoReleaser; the
  `checksums.txt` is signed (Ed25519, minisign format) in CI. The public
  key is compiled into the agent (`internal/update/minisign.go`) and
  printed in the README; nothing on a customer's machine needs the
  minisign tool.
- The one-line installer (`https://watchfor.io/agent/install.sh`) needs no
  verification tools on the machine and has no skip switch. The script and a release's
  `checksums.txt` are served by watchfor.io only after the release
  signature verified there; the tarball comes from GitHub and is checked
  against those checksums, so a file replaced on GitHub alone cannot pass
  and a forged watchfor.io alone has no tarball to serve. The downloaded
  agent then verifies the release signature itself with its built-in key
  (`watchfor-agent verify`). A first install therefore rests on TLS to
  watchfor.io, which is also what delivered the install command; from then
  on, the installed agent's key does the checking and no server is trusted.
- `watchfor-agent upgrade` installs only a release whose `checksums.txt`
  carries a valid signature by that key (the key is compiled into the
  binary), whose archive matches the signed checksum, and whose binary
  reports the expected version; it talks HTTPS to GitHub's release hosts
  only and refuses redirects elsewhere. The server can announce a newer
  version, but cannot make an agent install anything unsigned — and the
  running daemon never replaces itself; the swap is a separate root-run
  one-shot (manual, or the opt-in daily timer).
