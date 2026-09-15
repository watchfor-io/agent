#!/bin/sh
# Installs or upgrades watchfor-agent from a GitHub release: verifies the
# release signature and checksum, creates the service user, writes the
# token, enables the unit. Re-running it on a host that already has the
# agent upgrades it in place: the token and agent.yml are kept, the service
# is restarted.
#
#   curl -fsSL https://raw.githubusercontent.com/watchfor-io/agent/main/packaging/install.sh | sudo sh -s -- --token <host-token>
#
# Options: --token <t>  --server <url>  --version <vX.Y.Z>
#          --skip-signature   rely on the sha256 checksum only (no minisign needed)
#          --auto-update      enable the daily update timer (installs newer signed
#                             releases the server reports; --no-auto-update removes it)
#          --yes              install missing tools (curl, minisign, …) without asking
#          --plain            no colours, spinners or screen clearing (also: NO_COLOR=1,
#                             or any output that is not a terminal)
#          --no-clear         keep what is on the terminal
#
# Missing tools: on a terminal, as root, the script offers to install them
# with the distribution's package manager and then carries on.
#
set -eu

REPO="${WATCHFOR_AGENT_REPO:-watchfor-io/agent}"
VERSION="${WATCHFOR_AGENT_VERSION:-latest}"
SERVER="${WATCHFOR_SERVER:-https://ingest.watchfor.io}"
TOKEN=""
SKIP_SIGNATURE=0
AUTO_UPDATE=""
CLEAR=1
ASSUME_YES=0
PLAIN=0
PUBKEY="${WATCHFOR_AGENT_PUBKEY:-RWTUApo01PH7RyjD76wN2Vu7l5sO7Ys5psNQE9I7QYdWVfWSf0CetQje}"
BIN="${WATCHFOR_AGENT_BIN:-/usr/local/bin/watchfor-agent}"
ETC=/etc/watchfor-agent
LIB=/var/lib/watchfor-agent
# From this version on the installed agent verifies releases itself (the
# release key is built into it), so an upgrade no longer needs minisign.
SELF_VERIFY_SINCE=0.3.0

while [ $# -gt 0 ]; do
  case "$1" in
    --token) TOKEN="$2"; shift 2 ;;
    --server) SERVER="$2"; shift 2 ;;
    --version) VERSION="$2"; shift 2 ;;
    --skip-signature) SKIP_SIGNATURE=1; shift ;;
    --auto-update) AUTO_UPDATE=1; shift ;;
    --no-auto-update) AUTO_UPDATE=0; shift ;;
    --no-clear) CLEAR=0; shift ;;
    --plain) PLAIN=1; CLEAR=0; shift ;;
    -y|--yes) ASSUME_YES=1; shift ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
done

# ── Terminal ─────────────────────────────────────────────────────────────
# Colours, spinners and the box only on a real terminal; a pipe, a CI log
# or NO_COLOR gets plain text with the same words.
TTY=0
[ -t 1 ] && [ "$PLAIN" = 0 ] && [ -z "${NO_COLOR:-}" ] && [ "${TERM:-dumb}" != dumb ] && TTY=1
UTF8=0
case "${LC_ALL:-${LC_CTYPE:-${LANG:-}}}" in *UTF-8*|*utf8*|*UTF8*|*utf-8*) UTF8=1 ;; esac
ESC=$(printf '\033')
if [ "$TTY" = 1 ]; then
  C_OFF="$ESC[0m"; C_BOLD="$ESC[1m"; C_DIM="$ESC[2m"
  C_RED="$ESC[1;31m"; C_GREEN="$ESC[1;32m"; C_CYAN="$ESC[36m"; C_BLUE="$ESC[1;34m"
  case "${TERM:-}${COLORTERM:-}" in
    *256color*|*truecolor*|*24bit*) G1="$ESC[1;38;5;33m"; G2="$ESC[1;38;5;39m"; G3="$ESC[1;38;5;45m"; G4="$ESC[1;38;5;51m" ;;
    *) G1=$C_BLUE; G2=$C_BLUE; G3=$C_CYAN; G4=$C_CYAN ;;
  esac
  HIDE="$ESC[?25l"; SHOW="$ESC[?25h"; CLR="$ESC[2K"
else
  C_OFF=""; C_BOLD=""; C_DIM=""; C_RED=""; C_GREEN=""; C_CYAN=""; C_BLUE=""
  G1=""; G2=""; G3=""; G4=""; HIDE=""; SHOW=""; CLR=""
fi
if [ "$UTF8" = 1 ]; then
  I_OK="✓"; I_BAD="✗"; I_DOWN="↓"; ARROW="→"; SPIN="⠋ ⠙ ⠹ ⠸ ⠼ ⠴ ⠦ ⠧ ⠇ ⠏"
  B_TL="┌"; B_TR="┐"; B_BL="└"; B_BR="┘"; B_H="─"; B_V="│"
else
  I_OK="OK"; I_BAD="x"; I_DOWN="v"; ARROW="->"; SPIN="- \\ | /"
  B_TL="+"; B_TR="+"; B_BL="+"; B_BR="+"; B_H="-"; B_V="|"
fi

ok()   { printf '%s%s%s %s\n' "$C_GREEN" "$I_OK" "$C_OFF" "$1"; }
note() { printf '%s%s%s\n' "$C_DIM" "$1" "$C_OFF"; }
fail() { printf '%s%s %s%s\n' "$C_RED" "$I_BAD" "$*" "$C_OFF" >&2; exit 1; }

TMP=$(mktemp -d)
LOG="$TMP/step.log"
STEP_PID=""
cleanup() { [ -z "$STEP_PID" ] || kill "$STEP_PID" 2>/dev/null || true; printf '%s' "$SHOW"; rm -rf "$TMP"; }
trap cleanup EXIT
trap 'cleanup; exit 130' INT TERM

# step <label> <command…>: run the command with its output in $LOG; on a
# terminal a spinner turns next to the label for as long as it really runs.
# The line is cleared afterwards so the caller can print the result.
step() {
  _label=$1; shift
  if [ "$TTY" = 1 ]; then
    printf '%s' "$HIDE"
    "$@" >"$LOG" 2>&1 &
    STEP_PID=$!
    _i=0
    while kill -0 "$STEP_PID" 2>/dev/null; do
      set -- $SPIN
      _i=$(( (_i + 1) % $# ))
      eval "_frame=\${$((_i + 1))}"
      printf '\r%s%s%s %s' "$C_CYAN" "$_frame" "$C_OFF" "$_label"
      sleep 0.1 2>/dev/null || sleep 1
    done
    if wait "$STEP_PID"; then _rc=0; else _rc=$?; fi
    STEP_PID=""
    printf '\r%s%s' "$CLR" "$SHOW"
  else
    if "$@" >"$LOG" 2>&1; then _rc=0; else _rc=$?; fi
  fi
  return $_rc
}
# failstep <message>: the step's own output, then the failure.
failstep() {
  if [ -s "$LOG" ]; then
    printf '%s' "$C_DIM" >&2; tail -n 8 "$LOG" | sed 's/^/    /' >&2; printf '%s' "$C_OFF" >&2
  fi
  fail "$@"
}

# ── Wordmark ─────────────────────────────────────────────────────────────
[ "$CLEAR" = 1 ] && [ "$TTY" = 1 ] && printf '\033[H\033[2J'
printf '\n'
printf '%s%s%s\n' "$G1" '   __      __         _          _        ___' "$C_OFF"
printf '%s%s%s\n' "$G2" '   \ \    / /  __ _  | |_   __  | |_     | __|  ___   _ _' "$C_OFF"
printf '%s%s%s\n' "$G3" '    \ \/\/ /  / _` | |  _| / _| | '"'"' \    | _|  / _ \ | '"'"'_|' "$C_OFF"
printf '%s%s%s\n' "$G4" '     \_/\_/   \__,_|  \__| \__| |_||_|   |_|   \___/ |_|' "$C_OFF"
printf '\n%sKnow before your customers do%s - and let your agents know too.\n' "$C_BOLD" "$C_OFF"
note "                     https://watchfor.io"
printf '\n'

# ── What is on this box ──────────────────────────────────────────────────
# os-release is read with sed, not sourced: sourcing it would set VERSION.
OS_RELEASE="${WATCHFOR_OS_RELEASE:-/etc/os-release}"
DISTRO_ID=$(sed -n 's/^ID=//p' "$OS_RELEASE" 2>/dev/null | tr -d '"')
DISTRO_LIKE=$(sed -n 's/^ID_LIKE=//p' "$OS_RELEASE" 2>/dev/null | tr -d '"')
DISTRO_NAME=$(sed -n 's/^PRETTY_NAME=//p' "$OS_RELEASE" 2>/dev/null | tr -d '"')
case " $DISTRO_ID $DISTRO_LIKE " in
  *alpine*) FAMILY=alpine ;;
  *arch*|*manjaro*) FAMILY=arch ;;
  *suse*|*sles*) FAMILY=suse ;;
  *rhel*|*centos*|*rocky*|*alma*) FAMILY=rhel ;;
  *fedora*) FAMILY=fedora ;;
  *debian*|*ubuntu*|*mint*|*pop*) FAMILY=debian ;;
  *) FAMILY="" ;;
esac

# ── Preflight ────────────────────────────────────────────────────────────
# The agent reads /proc and /sys and runs as a systemd service: Linux on
# amd64 or arm64. Anything else is told so here, before any other check
# could produce a confusing "missing: sha256sum useradd".
problem() { printf '%s%s %s%s\n\n' "$C_RED" "$I_BAD" "$1" "$C_OFF" >&2; }
OS=$(uname -s 2>/dev/null || echo unknown)
case "$OS" in
  Linux) ;;
  Darwin)
    problem "macOS is not supported"
    cat >&2 <<'MSG'
  watchfor-agent monitors Linux servers: it reads /proc and /sys and runs
  as a systemd service. Releases are built for Linux amd64 and arm64;
  Windows is planned, macOS is not. Install it on the Linux host you
  want to watch: https://watchfor.io/docs/hosts

MSG
    exit 1 ;;
  *)
    problem "$OS is not supported"
    cat >&2 <<'MSG'
  watchfor-agent monitors Linux servers: it reads /proc and /sys and runs
  as a systemd service. Releases are built for Linux amd64 and arm64.
  https://watchfor.io/docs/hosts

MSG
    exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *)
    problem "unsupported architecture: $(uname -m)"
    printf '  Releases are built for linux/amd64 and linux/arm64. For another\n  architecture build from source: https://github.com/%s#building-from-source\n\n' "$REPO" >&2
    exit 1 ;;
esac
CURRENT=""
[ -x "$BIN" ] && CURRENT=$("$BIN" version 2>/dev/null | awk '{print $2}')

# ver_ge A B: true when release A is at least B (MAJOR.MINOR.PATCH; a
# "-dev" or "+build" suffix is ignored).
ver_ge() {
  a=$(printf '%s' "$1" | sed 's/^v//; s/[-+].*//'); b=$(printf '%s' "$2" | sed 's/^v//; s/[-+].*//')
  [ "$(printf '%s\n%s\n' "$a" "$b" | sort -t. -k1,1n -k2,2n -k3,3n | head -n1)" = "$b" ]
}

# The release signature is checked before anything is installed. That is
# minisign's job — unless an agent that verifies releases itself is
# already here; then it does the download and the check with its built-in key.
VERIFY_BY=minisign
if [ "$SKIP_SIGNATURE" = 1 ]; then
  VERIFY_BY=none
elif ! command -v minisign >/dev/null 2>&1 && [ -n "$CURRENT" ] && ver_ge "$CURRENT" "$SELF_VERIFY_SINCE"; then
  VERIFY_BY=agent
fi

# ── Required tools ───────────────────────────────────────────────────────
# Everything the script needs, checked up front: one clear list beats a
# "command not found" halfway through.
missing=""
for tool in curl tar sha256sum mktemp install useradd id grep sed uname; do
  command -v "$tool" >/dev/null 2>&1 || missing="$missing $tool"
done
[ "$VERIFY_BY" != minisign ] || command -v minisign >/dev/null 2>&1 || missing="$missing minisign"

# pkgs_for <family>: the packages that provide the missing tools there.
pkgs_for() {
  out=""
  for t in $missing; do
    case "$t" in
      sha256sum|mktemp|install|id|uname) p=coreutils ;;
      useradd) case "$1" in debian) p=passwd ;; fedora|rhel) p=shadow-utils ;; *) p=shadow ;; esac ;;
      *) p=$t ;;
    esac
    case " $out " in *" $p "*) ;; *) out="$out $p" ;; esac
  done
  printf '%s' "${out# }"
}
# cmd_for <family>: the one install command for that family.
cmd_for() {
  case "$1" in
    debian) printf 'apt-get install -y %s' "$(pkgs_for debian)" ;;
    fedora) printf 'dnf install -y %s' "$(pkgs_for fedora)" ;;
    rhel)   case "$missing" in
              *minisign*) printf 'dnf install -y epel-release && dnf install -y %s' "$(pkgs_for rhel)" ;;
              *) printf 'dnf install -y %s' "$(pkgs_for rhel)" ;;
            esac ;;
    suse)   printf 'zypper install -y %s' "$(pkgs_for suse)" ;;
    arch)   printf 'pacman -S --noconfirm %s' "$(pkgs_for arch)" ;;
    alpine) printf 'apk add %s' "$(pkgs_for alpine)" ;;
  esac
}
# ask <question>: y/n from the terminal even when the script itself arrives
# on stdin (curl | sh). No terminal → no.
ask() {
  [ "$ASSUME_YES" = 1 ] && return 0
  ( : < /dev/tty ) 2>/dev/null || return 1
  printf '%s [Y/n] ' "$1"
  read -r answer < /dev/tty || answer=n
  case "$answer" in ""|y|Y|yes|YES|Yes) return 0 ;; *) return 1 ;; esac
}

if [ -n "$missing" ]; then
  printf '%s%s missing:%s%s\n\n' "$C_RED" "$I_BAD" "$C_OFF" "$missing" >&2
  case "$missing" in *minisign*)
    cat >&2 <<'WHY'
  Every release ships checksums.txt signed with the WatchFor release key,
  and this script verifies that signature before it installs anything.
  The check needs the minisign tool; without it the download would be
  trusted on TLS alone.

WHY
  esac
  case "$missing" in " minisign") what=it ;; *) what=them ;; esac
  cmd=""; [ -z "$FAMILY" ] || cmd=$(cmd_for "$FAMILY")
  if [ -n "$cmd" ] && [ "$(id -u)" -eq 0 ]; then
    printf '  Install %s with:  %s%s%s   %s%s%s\n\n' "$what" "$C_GREEN" "$cmd" "$C_OFF" "$C_DIM" "$DISTRO_NAME" "$C_OFF" >&2
    if ask "  Do you want me to install $what now?"; then
      printf '\n'
      case "$FAMILY" in debian)
        # A fresh box may have no package lists yet.
        ls /var/lib/apt/lists/*Packages >/dev/null 2>&1 || step "refreshing the package lists" apt-get update -qq || failstep "apt-get update failed"
        ;;
      esac
      step "installing$missing" sh -c "$cmd" || failstep "package installation failed — install $what by hand and run the same command again"
      hash -r 2>/dev/null || true
      still=""
      for t in $missing; do command -v "$t" >/dev/null 2>&1 || still="$still $t"; done
      [ -z "$still" ] || fail "still missing after installation:$still — install $what by hand and run the same command again"
      ok "installed$missing"
      printf '\n'
    else
      printf '\n  Install %s, then run the same command again.\n' "$what" >&2
      case "$missing" in *minisign*)
        printf '  To skip the signature and rely on the sha256 checksum alone, add %s--skip-signature%s.\n' "$C_DIM" "$C_OFF" >&2 ;;
      esac
      exit 1
    fi
  else
    if [ -n "$cmd" ]; then
      printf '  Install %s, then run the same command again:\n\n' "$what" >&2
      printf '    %s%s%s   %s%s%s\n\n' "$C_GREEN" "$cmd" "$C_OFF" "$C_DIM" "$DISTRO_NAME" "$C_OFF" >&2
      [ "$(id -u)" -eq 0 ] || printf '  (run this script with sudo and it can install %s for you)\n\n' "$what" >&2
    else
      printf '  Install %s with your package manager, then run the same command again:\n\n' "$what" >&2
      for f in debian fedora rhel suse arch alpine; do
        case "$f" in debian) l="Debian / Ubuntu" ;; fedora) l="Fedora" ;; rhel) l="RHEL / Alma / Rocky" ;; suse) l="openSUSE" ;; arch) l="Arch" ;; alpine) l="Alpine" ;; esac
        printf '    %-20s %s%s%s\n' "$l" "$C_GREEN" "$(cmd_for "$f")" "$C_OFF" >&2
      done
      printf '\n' >&2
    fi
    case "$missing" in *minisign*)
      printf '  To skip the signature and rely on the sha256 checksum alone, add %s--skip-signature%s.\n' "$C_DIM" "$C_OFF" >&2 ;;
    esac
    exit 1
  fi
fi
[ "$(id -u)" -eq 0 ] || fail "run as root (sudo)"
if ! command -v systemctl >/dev/null 2>&1; then
  problem "systemd not found on ${DISTRO_NAME:-this system}"
  cat >&2 <<MSG
  This script installs watchfor-agent as a systemd service. Without
  systemd, install the binary by hand and run it from cron instead —
  both are a few lines: https://github.com/$REPO#install-by-hand

MSG
  exit 1
fi

# ── The release ──────────────────────────────────────────────────────────
if [ "$VERSION" = "latest" ]; then
  step "looking up the latest release" sh -c 'curl -fsSL "https://api.github.com/repos/$1/releases/latest" | sed -n "s/.*\"tag_name\": *\"\([^\"]*\)\".*/\1/p" > "$2"' sh "$REPO" "$TMP/tag" \
    || failstep "could not resolve the latest release from api.github.com"
  VERSION=$(cat "$TMP/tag")
  [ -n "$VERSION" ] || fail "could not resolve the latest release"
fi
TAG="$VERSION"
VER="${VERSION#v}"
BASE="https://github.com/$REPO/releases/download/$TAG"
TARBALL="watchfor-agent_${VER}_linux_${ARCH}.tar.gz"

# An existing install turns this into an upgrade: same checks, then the
# binary is swapped and the service restarted. Token and config stay.
if [ "$CURRENT" = "$VER" ]; then
  ok "watchfor-agent $C_BOLD$VER$C_OFF is already installed — nothing to download"
elif [ -n "$CURRENT" ]; then
  ok "upgrade: watchfor-agent $C_BOLD$CURRENT $ARROW $VER$C_OFF for linux/$ARCH"
else
  ok "install: watchfor-agent $C_BOLD$VER$C_OFF for linux/$ARCH"
fi

verify_release() {
  if [ "$VERIFY_BY" = minisign ]; then
    curl -fsSL -o "$TMP/checksums.txt.minisig" "$BASE/checksums.txt.minisig" \
      || { echo "could not download the release signature ($BASE/checksums.txt.minisig)" > "$TMP/reason"; return 1; }
    minisign -Vm "$TMP/checksums.txt" -P "$PUBKEY" \
      || { echo "signature verification failed: checksums.txt is not signed by the WatchFor release key" > "$TMP/reason"; return 1; }
  fi
  (cd "$TMP" && grep " $TARBALL\$" checksums.txt | sha256sum -c --quiet) \
    || { echo "checksum mismatch: $TARBALL does not match the signed checksums.txt" > "$TMP/reason"; return 1; }
}

if [ "$CURRENT" != "$VER" ]; then
  if [ "$VERIFY_BY" = agent ]; then
    note "  minisign is not installed; the installed agent ($CURRENT) verifies the release with its built-in key"
    step "downloading and verifying watchfor-agent $VER" "$BIN" upgrade -version "$VER" -no-restart \
      || failstep "upgrade failed; nothing was replaced"
    ok "verified signature and sha256, installed $BIN"
  else
    if [ "$TTY" = 1 ]; then
      # curl draws its own progress bar on a terminal; both lines are then
      # replaced by the one-line result.
      printf '%s%s%s downloading %s\n' "$C_CYAN" "$I_DOWN" "$C_OFF" "$TARBALL"
      if curl -fL -# -o "$TMP/$TARBALL" "$BASE/$TARBALL"; then
        # curl ends its bar with a newline: clear that line, the bar, the label.
        printf '\r%s\033[1A\r%s\033[1A\r%s' "$CLR" "$CLR" "$CLR"
      else
        printf '\n'; fail "could not download $BASE/$TARBALL"
      fi
    else
      curl -fsSL -o "$TMP/$TARBALL" "$BASE/$TARBALL" || fail "could not download $BASE/$TARBALL"
    fi
    size=$(wc -c < "$TMP/$TARBALL" | awk '{printf "%.1f MB", $1/1048576}')
    ok "downloaded $TARBALL ${C_DIM}($size)$C_OFF"
    step "fetching the signed checksums" curl -fsSL -o "$TMP/checksums.txt" "$BASE/checksums.txt" \
      || failstep "could not download $BASE/checksums.txt"
    if [ "$VERIFY_BY" = none ]; then
      step "verifying the sha256 checksum" verify_release || fail "$(cat "$TMP/reason" 2>/dev/null || echo "verification failed")"
      ok "verified sha256 ${C_DIM}(signature check skipped: --skip-signature)$C_OFF"
    else
      step "verifying the release signature and checksum" verify_release || fail "$(cat "$TMP/reason" 2>/dev/null || echo "verification failed")"
      ok "verified signature and sha256"
    fi
    step "installing $BIN" sh -c 'tar -xzf "$1" -C "$2" watchfor-agent && install -m 0755 "$2/watchfor-agent" "$3"' sh "$TMP/$TARBALL" "$TMP" "$BIN" \
      || failstep "could not install $BIN"
    ok "installed $BIN"
  fi
fi

# ── User, directories, token, config ─────────────────────────────────────
step "creating the service user and directories" sh -c '
  id -u watchfor-agent >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin --user-group watchfor-agent
  install -d -m 0750 -o root -g watchfor-agent "$1"
  install -d -m 0700 -o watchfor-agent -g watchfor-agent "$2"' sh "$ETC" "$LIB" \
  || failstep "could not create the watchfor-agent user or its directories"
ok "service user watchfor-agent, $ETC, $LIB"

if [ -n "$TOKEN" ]; then
  umask 077
  printf '%s\n' "$TOKEN" > "$ETC/token"
  chown watchfor-agent:watchfor-agent "$ETC/token"; chmod 0600 "$ETC/token"
  umask 022
  ok "token written to $ETC/token ${C_DIM}(mode 0600)$C_OFF"
fi
if [ ! -f "$ETC/agent.yml" ]; then
  cat > "$ETC/agent.yml" <<YML
server:
  url: $SERVER
  token_file: $ETC/token
interval: 15s
YML
  ok "config written: $ETC/agent.yml"
else
  ok "config kept: $ETC/agent.yml"
fi

# ── systemd ──────────────────────────────────────────────────────────────
RAW="https://raw.githubusercontent.com/$REPO/$TAG/packaging"
step "installing the systemd service" sh -c '
  curl -fsSL -o /etc/systemd/system/watchfor-agent.service "$1/watchfor-agent.service"
  case "$2" in
    1) curl -fsSL -o /etc/systemd/system/watchfor-agent-update.service "$1/watchfor-agent-update.service"
       curl -fsSL -o /etc/systemd/system/watchfor-agent-update.timer "$1/watchfor-agent-update.timer" ;;
    0) systemctl disable -q --now watchfor-agent-update.timer 2>/dev/null || true
       rm -f /etc/systemd/system/watchfor-agent-update.timer /etc/systemd/system/watchfor-agent-update.service ;;
  esac
  systemctl daemon-reload
  systemctl enable -q --now watchfor-agent
  [ "$2" != 1 ] || systemctl enable -q --now watchfor-agent-update.timer
  [ -z "$3" ] || [ "$3" = "$4" ] || systemctl try-restart watchfor-agent' sh "$RAW" "$AUTO_UPDATE" "$CURRENT" "$VER" \
  || failstep "could not enable the systemd service"
if [ -n "$CURRENT" ] && [ "$CURRENT" != "$VER" ]; then
  ok "service restarted on watchfor-agent $VER"
  service_state="watchfor-agent, restarted, runs as user watchfor-agent"
else
  ok "service enabled and running"
  service_state="watchfor-agent, running as user watchfor-agent"
fi
if systemctl is-enabled -q watchfor-agent-update.timer 2>/dev/null; then
  ok "auto-update on: a newer signed release the server reports is installed daily"
  update_state="on, daily (watchfor-agent-update.timer)"
else
  update_state="off: sudo watchfor-agent upgrade, or re-run with --auto-update"
fi

# ── Summary ──────────────────────────────────────────────────────────────
W=58
rule() { _n=$1; while [ "$_n" -gt 0 ]; do printf '%s' "$B_H"; _n=$((_n - 1)); done; }
line() { printf '%s%s%s  %s%-12s%s %-*.*s %s%s%s\n' "$C_DIM" "$B_V" "$C_OFF" "$C_DIM" "$1" "$C_OFF" "$W" "$W" "$2" "$C_DIM" "$B_V" "$C_OFF"; }
title=" $("$BIN" version) "
tlen=${#title}
printf '\n%s%s%s%s%s%s%s' "$C_DIM" "$B_TL" "$B_H" "$C_OFF" "$C_BOLD$C_GREEN" "$title" "$C_OFF"
printf '%s%s%s%s\n' "$C_DIM" "$(rule $((W + 15 - tlen)))" "$B_TR" "$C_OFF"
line "service" "$service_state"
line "config" "$ETC/agent.yml"
line "auto-update" "$update_state"
line "status" "systemctl status watchfor-agent"
line "logs" "journalctl -u watchfor-agent -f"
[ -n "$TOKEN" ] || [ -s "$ETC/token" ] || line "token" "missing: $ETC/token (chmod 600), then restart"
printf '%s%s%s%s%s\n\n' "$C_DIM" "$B_BL" "$(rule $((W + 16)))" "$B_BR" "$C_OFF"
