# Contributing

Thank you for looking. The agent is small on purpose: one binary, one
YAML file, outbound HTTPS only. Changes that keep it that way are welcome.

## Building and testing

```sh
make build            # CGO_ENABLED=0, -trimpath, version stamped
make test             # go test -race ./...
make lint             # vet, staticcheck, govulncheck, gosec
```

Go 1.26 or newer; the `toolchain` line in `go.mod` fetches the exact patch
release. Tests do not need root, network or systemd: what the agent reads
from `/proc`, `/sys` and the service manager is behind small hooks that
tests replace.

## Pull requests

- One change per PR, with a test that fails without it.
- `main` takes pull requests only, with CI green: `gofmt`, `go mod tidy`,
  vet, race tests, staticcheck, govulncheck, gosec (G304 and G204 are
  excluded on purpose: a /proc reader opens computed paths all day, and
  the only subprocess is systemctl with constant verbs).
- Comments explain a decision, not the code. A comment that restates the
  line below it is deleted in review.
- User-facing changes get a line in `CHANGELOG.md` under *Unreleased* and,
  where they change behaviour, a matching edit in `README.md`.

## Adding a module

Modules live in `internal/modules/<name>` and register themselves in
`init()`; see the README section *Contributing a module* for the shape and
the naming rules for metrics.

## Security

Do not open an issue for a vulnerability; see [SECURITY.md](SECURITY.md).
