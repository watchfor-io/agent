#!/bin/sh
# Installs watchfor-agent from a GitHub release: verifies the checksum,
# creates the service user, writes the token, enables the unit.
#
#   curl -fsSL https://raw.githubusercontent.com/watchfor-io/agent/main/packaging/install.sh | sh -s -- --token <host-token>
#
set -eu

REPO="${WATCHFOR_AGENT_REPO:-watchfor-io/agent}"
VERSION="${WATCHFOR_AGENT_VERSION:-latest}"
SERVER="${WATCHFOR_SERVER:-https://ingest.watchfor.io}"
TOKEN=""
BIN=/usr/local/bin/watchfor-agent
ETC=/etc/watchfor-agent
LIB=/var/lib/watchfor-agent

while [ $# -gt 0 ]; do
  case "$1" in
    --token) TOKEN="$2"; shift 2 ;;
    --server) SERVER="$2"; shift 2 ;;
    --version) VERSION="$2"; shift 2 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
done

[ "$(id -u)" -eq 0 ] || { echo "run as root (sudo)"; exit 1; }
command -v systemctl >/dev/null || { echo "systemd is required; for other init systems install the binary manually"; exit 1; }

case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "unsupported architecture: $(uname -m)"; exit 1 ;;
esac

if [ "$VERSION" = "latest" ]; then
  VERSION=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p')
  [ -n "$VERSION" ] || { echo "could not resolve the latest release"; exit 1; }
fi
TAG="$VERSION"
VER="${VERSION#v}"
BASE="https://github.com/$REPO/releases/download/$TAG"
TARBALL="watchfor-agent_${VER}_linux_${ARCH}.tar.gz"

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
echo "downloading $TARBALL"
curl -fsSL -o "$TMP/$TARBALL" "$BASE/$TARBALL"
curl -fsSL -o "$TMP/checksums.txt" "$BASE/checksums.txt"
if command -v minisign >/dev/null 2>&1 && curl -fsSL -o "$TMP/checksums.txt.minisig" "$BASE/checksums.txt.minisig" 2>/dev/null; then
  minisign -Vm "$TMP/checksums.txt" -P "${WATCHFOR_AGENT_PUBKEY:-RWQf6LRCGA9i53mlYecO4IzT51TGPpvWucNSCh1CBM0QTaLn73Y7GFO3}" >/dev/null \
    || { echo "signature verification failed"; exit 1; }
fi
(cd "$TMP" && grep " $TARBALL\$" checksums.txt | sha256sum -c --quiet) || { echo "checksum mismatch"; exit 1; }
tar -xzf "$TMP/$TARBALL" -C "$TMP" watchfor-agent
install -m 0755 "$TMP/watchfor-agent" "$BIN"

id -u watchfor-agent >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin --user-group watchfor-agent
install -d -m 0750 -o root -g watchfor-agent "$ETC"
install -d -m 0700 -o watchfor-agent -g watchfor-agent "$LIB"

if [ -n "$TOKEN" ]; then
  umask 077
  printf '%s\n' "$TOKEN" > "$ETC/token"
  chown watchfor-agent:watchfor-agent "$ETC/token"; chmod 0600 "$ETC/token"
  umask 022
fi
if [ ! -f "$ETC/agent.yml" ]; then
  cat > "$ETC/agent.yml" <<YML
server:
  url: $SERVER
  token_file: $ETC/token
interval: 15s
YML
fi

curl -fsSL -o /etc/systemd/system/watchfor-agent.service "https://raw.githubusercontent.com/$REPO/$TAG/packaging/watchfor-agent.service"
systemctl daemon-reload
systemctl enable --now watchfor-agent
echo "watchfor-agent $TAG installed; status: systemctl status watchfor-agent"
[ -n "$TOKEN" ] || echo "no --token given: put the host token in $ETC/token (chmod 600) and restart the service"
