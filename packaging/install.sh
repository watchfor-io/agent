#!/bin/sh
# Installs watchfor-agent from a GitHub release: verifies the checksum,
# creates the service user, writes the token, enables the unit.
#
#   curl -fsSL https://raw.githubusercontent.com/watchfor-io/agent/main/packaging/install.sh | sh -s -- --token <host-token>
#
# Options: --token <t>  --server <url>  --version <vX.Y.Z>
#          --skip-signature   rely on the sha256 checksum only (no minisign needed)
#
set -eu

REPO="${WATCHFOR_AGENT_REPO:-watchfor-io/agent}"
VERSION="${WATCHFOR_AGENT_VERSION:-latest}"
SERVER="${WATCHFOR_SERVER:-https://ingest.watchfor.io}"
TOKEN=""
SKIP_SIGNATURE=0
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ] && [ "${TERM:-dumb}" != dumb ]; then
  C_BLUE=$(printf '\033[1;34m'); C_GREEN=$(printf '\033[1;32m'); C_RED=$(printf '\033[1;31m'); C_DIM=$(printf '\033[2m'); C_OFF=$(printf '\033[0m')
else
  C_BLUE=""; C_GREEN=""; C_RED=""; C_DIM=""; C_OFF=""
fi
fail() { printf '%s%s%s\n' "$C_RED" "$*" "$C_OFF" >&2; exit 1; }
PUBKEY="${WATCHFOR_AGENT_PUBKEY:-RWTUApo01PH7RyjD76wN2Vu7l5sO7Ys5psNQE9I7QYdWVfWSf0CetQje}"
BIN=/usr/local/bin/watchfor-agent
ETC=/etc/watchfor-agent
LIB=/var/lib/watchfor-agent

while [ $# -gt 0 ]; do
  case "$1" in
    --token) TOKEN="$2"; shift 2 ;;
    --server) SERVER="$2"; shift 2 ;;
    --version) VERSION="$2"; shift 2 ;;
    --skip-signature) SKIP_SIGNATURE=1; shift ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
done

printf '%s' "$C_BLUE"
cat <<'WORDMARK'

   __      __         _          _        ___
   \ \    / /  __ _  | |_   __  | |_     | __|  ___   _ _
    \ \/\/ /  / _` | |  _| / _| | ' \    | _|  / _ \ | '_|
     \_/\_/   \__,_|  \__| \__| |_||_|   |_|   \___/ |_|
WORDMARK
printf '%s\n' "$C_OFF"
printf '%s\n' "Know before your customers do - and let your agents know too."
printf '%s%s%s\n\n' "$C_DIM" "                     https://watchfor.io" "$C_OFF"

# Everything the script needs, checked up front: a clear list beats a
# "command not found" halfway through. The release signature is verified by
# default, so minisign is required unless --skip-signature says otherwise.
missing=""
for tool in curl tar sha256sum mktemp install useradd id grep sed uname; do
  command -v "$tool" >/dev/null 2>&1 || missing="$missing $tool"
done
[ "$SKIP_SIGNATURE" = 1 ] || command -v minisign >/dev/null 2>&1 || missing="$missing minisign"
if [ -n "$missing" ]; then
  printf '%smissing required tools:%s%s\n' "$C_RED" "$missing" "$C_OFF" >&2
  echo "install them and run again — Debian/Ubuntu: apt-get install -y curl tar coreutils passwd minisign" >&2
  echo "                              RHEL/Alma/Rocky: dnf install -y curl tar coreutils shadow-utils minisign" >&2
  echo "                              openSUSE: zypper install -y curl tar coreutils shadow minisign" >&2
  case "$missing" in *minisign*)
    echo "minisign verifies the release signature; to rely on the sha256 checksum only, add --skip-signature" >&2 ;;
  esac
  exit 1
fi
[ "$(id -u)" -eq 0 ] || fail "run as root (sudo)"
command -v systemctl >/dev/null || fail "systemd is required; for other init systems install the binary by hand (see README: Install by hand)"

case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) fail "unsupported architecture: $(uname -m)" ;;
esac

if [ "$VERSION" = "latest" ]; then
  VERSION=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p')
  [ -n "$VERSION" ] || fail "could not resolve the latest release"
fi
TAG="$VERSION"
VER="${VERSION#v}"
BASE="https://github.com/$REPO/releases/download/$TAG"
TARBALL="watchfor-agent_${VER}_linux_${ARCH}.tar.gz"
printf '%sinstalling%s watchfor-agent %s for linux/%s from github.com/%s\n' "$C_DIM" "$C_OFF" "$TAG" "$ARCH" "$REPO"

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
printf '%sdownloading%s %s\n' "$C_DIM" "$C_OFF" "$TARBALL"
curl -fsSL -o "$TMP/$TARBALL" "$BASE/$TARBALL"
curl -fsSL -o "$TMP/checksums.txt" "$BASE/checksums.txt"
if [ "$SKIP_SIGNATURE" = 1 ]; then
  echo "signature check skipped (--skip-signature): trusting the sha256 checksum only"
else
  curl -fsSL -o "$TMP/checksums.txt.minisig" "$BASE/checksums.txt.minisig" \
    || fail "could not download the release signature ($BASE/checksums.txt.minisig)"
  minisign -Vm "$TMP/checksums.txt" -P "$PUBKEY" >/dev/null \
    || fail "signature verification failed: checksums.txt is not signed by the WatchFor release key"
fi
(cd "$TMP" && grep " $TARBALL\$" checksums.txt | sha256sum -c --quiet) || fail "checksum mismatch"
printf '%sverified%s signature and sha256\n' "$C_GREEN" "$C_OFF"
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
systemctl enable -q --now watchfor-agent
printf '%sinstalled%s %s as a service (user watchfor-agent, config %s/agent.yml)\n' "$C_GREEN" "$C_OFF" "$("$BIN" version)" "$ETC"
printf '%sstatus:%s systemctl status watchfor-agent   %slogs:%s journalctl -u watchfor-agent -f\n' "$C_DIM" "$C_OFF" "$C_DIM" "$C_OFF"
[ -n "$TOKEN" ] || echo "no --token given: put the host token in $ETC/token (chmod 600) and restart the service"
