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
  `checksums.txt` is signed with minisign. The public key is in
  `packaging/install.sh` and in the README.
