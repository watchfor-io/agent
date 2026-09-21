#!/bin/sh
# Installs or upgrades watchfor-agent from a GitHub release: verifies the
# release signature and checksum, creates the service user, writes the
# token, enables the unit. Re-running it on a host that already has the
# agent upgrades it in place: the token and agent.yml are kept, the service
# is restarted.
#
#   curl --proto '=https' --tlsv1.2 -fsSL https://watchfor.io/agent/install.sh | sudo sh -s -- --token <host-token>
#
# Options: --token <t>  --server <url>  --version <vX.Y.Z>
#          --token -          ask for the token on the terminal (not echoed) — keeps it
#                             out of `ps` and shell history; WATCHFOR_TOKEN env works too
#          --auto-update      enable the daily update timer (installs newer signed
#                             releases the server reports; --no-auto-update removes it;
#                             with neither, an interactive run asks)
#          --reinstall        download and install again even if this version is present
#          --yes              answer the questions non-interactively: install missing
#                             tools, enable auto-update; never reinstall unless --reinstall
#          --plain            no colours, spinners or screen clearing (also: NO_COLOR=1,
#                             or any output that is not a terminal)
#          --no-clear         keep what is on the terminal
#          --uninstall        remove the agent: service, timer, cron entry, binary,
#                             state, config with the token, and the service user
#                             (--keep-config leaves /etc/watchfor-agent in place)
#
# Needs only curl, tar, sha256sum and the usual base tools — nothing for
# the verification: the tarball comes from GitHub (storage only), its
# checksums from watchfor.io (signature-verified there), and the downloaded
# agent checks the release signature itself with its built-in key.
# Missing base tools: on a terminal, as root, the script offers to install
# them with the distribution's package manager and then carries on.
#
set -eu

REPO="${WATCHFOR_AGENT_REPO:-watchfor-io/agent}"
# Where the release files (tarball, signature) are downloaded from — GitHub,
# storage only; a mirror works too, the checks below do not trust it.
DOWNLOAD="${WATCHFOR_AGENT_DOWNLOAD_URL:-https://github.com/$REPO/releases/download}"
VERSION="${WATCHFOR_AGENT_VERSION:-latest}"
usage() {
  cat <<'USAGE'
usage: install.sh [--token <token>|--token -] [--server <url>] [--version <x.y.z>]
                  [--auto-update|--no-auto-update] [--reinstall] [--uninstall [--keep-config]]
                  [--plain] [--no-clear] [-y|--yes]

  --token <token>     the host token from the dashboard; "-" asks on the terminal (not echoed)
  --server <url>      where the agent sends (default https://ingest.watchfor.io)
  --version <x.y.z>   install this release instead of the newest (0.7.0 or newer)
  --auto-update       install newer signed releases daily; --no-auto-update removes the timer
  --reinstall         download and install again even when this version is already there
  --uninstall         remove the agent; --keep-config leaves /etc/watchfor-agent in place
  --plain             no colours or progress; -y answers yes to every question
  WATCHFOR_TOKEN      the token, for automation; WATCHFOR_AGENT_VERSION pins a version
USAGE
}

# Every download the same way: HTTPS only, TLS 1.2 or newer, and a redirect
# may not leave HTTPS. Word-split on purpose; exported for the sh -c steps.
CURL="curl --proto =https --proto-redir =https --tlsv1.2"
export CURL
SERVER="${WATCHFOR_SERVER:-https://ingest.watchfor.io}"
# Where the verified release files are served from: /latest, /checksums/<tag>.txt
RELEASES="${WATCHFOR_RELEASES_URL:-https://watchfor.io/agent}"
TOKEN="${WATCHFOR_TOKEN:-}"
AUTO_UPDATE=""
REINSTALL=0
UNINSTALL=0
KEEP_CONFIG=0
CLEAR=1
ASSUME_YES=0
PLAIN=0
BIN="${WATCHFOR_AGENT_BIN:-/usr/local/bin/watchfor-agent}"
ETC=/etc/watchfor-agent
LIB=/var/lib/watchfor-agent

while [ $# -gt 0 ]; do
  case "$1" in
    --token) TOKEN="$2"; shift 2 ;;
    --server) SERVER="$2"; shift 2 ;;
    --version) VERSION="$2"; shift 2 ;;
    --auto-update) AUTO_UPDATE=1; shift ;;
    --no-auto-update) AUTO_UPDATE=0; shift ;;
    --reinstall) REINSTALL=1; shift ;;
    --uninstall) UNINSTALL=1; shift ;;
    --keep-config) KEEP_CONFIG=1; shift ;;
    --no-clear) CLEAR=0; shift ;;
    --plain) PLAIN=1; CLEAR=0; shift ;;
    -y|--yes) ASSUME_YES=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown option: $1 (try --help)" >&2; exit 2 ;;
  esac
done
# --version 0.7.0 and --version v0.7.0 both name the tag v0.7.0.
case "$VERSION" in "" | latest | v*) ;; *) VERSION="v$VERSION" ;; esac

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
KEPT_LOG="" # made only when a step leaves output worth keeping: root's own fresh file, never a path someone else prepared
keep_log() { [ -s "$LOG" ] || return 0; [ -n "$KEPT_LOG" ] || KEPT_LOG=$(mktemp /tmp/watchfor-install.XXXXXX) || return 0; cp "$LOG" "$KEPT_LOG" 2>/dev/null || true; }
STEP_PID=""
STEP_LABEL=""
# Every step runs in its own process group (setsid) with stdin closed, so
# nothing it starts can wait for a keypress, and one kill reaches all of it.
HAVE_SETSID=0; command -v setsid >/dev/null 2>&1 && HAVE_SETSID=1
kill_step() {
  [ -n "$STEP_PID" ] || return 0
  if [ "$HAVE_SETSID" = 1 ]; then kill -TERM -- -"$STEP_PID" 2>/dev/null || true; else kill -TERM "$STEP_PID" 2>/dev/null || true; fi
  _n=0; while kill -0 "$STEP_PID" 2>/dev/null && [ $_n -lt 20 ]; do sleep 0.1 2>/dev/null || sleep 1; _n=$((_n + 1)); done
  if kill -0 "$STEP_PID" 2>/dev/null; then
    if [ "$HAVE_SETSID" = 1 ]; then kill -KILL -- -"$STEP_PID" 2>/dev/null || true; else kill -KILL "$STEP_PID" 2>/dev/null || true; fi
  fi
  STEP_PID=""
}
ECHO_OFF=0
cleanup() {
  kill_step
  # Only touch the terminal if the token prompt turned echo off: a tty
  # ioctl from a background process group would stop the script instead.
  [ "$ECHO_OFF" = 0 ] || stty echo 2>/dev/null < /dev/tty || true
  printf '%s' "$SHOW"
  rm -rf "$TMP"
  [ -z "${BIN:-}" ] || rm -f "$BIN.new"
}
trap cleanup EXIT
interrupted() {
  trap - INT TERM
  printf '\r%s' "$CLR"
  kill_step
  keep_log
  printf '%s%s interrupted%s' "$C_RED" "$I_BAD" "$C_OFF" >&2
  [ -z "$STEP_LABEL" ] || printf ' while %s' "$STEP_LABEL" >&2
  printf '\n' >&2
  [ -n "$KEPT_LOG" ] && printf '  the step'"'"'s output is in %s\n' "$KEPT_LOG" >&2
  case "$STEP_LABEL" in *installing*|*package*) printf '  a package install was cut short: run  dpkg --configure -a  (Debian) or re-run the package manager before trying again\n' >&2 ;; esac
  exit 130
}
trap interrupted INT TERM

# step <label> <command…>: run the command with its output in $LOG; on a
# terminal a spinner turns next to the label for as long as it really runs.
# The line is cleared afterwards so the caller can print the result.
step() {
  _label=$1; shift
  STEP_LABEL=$_label
  : > "$LOG"
  if [ "$TTY" = 1 ]; then
    printf '%s' "$HIDE"
    if [ "$HAVE_SETSID" = 1 ]; then setsid "$@" >"$LOG" 2>&1 < /dev/null & else "$@" >"$LOG" 2>&1 < /dev/null & fi
    STEP_PID=$!
    _i=0; _cols=$(stty size 2>/dev/null < /dev/tty | awk '{print $2}')
    _w=$(( ${_cols:-0} - ${#_label} - 8 )); [ "$_w" -gt 10 ] 2>/dev/null || _w=60
    while kill -0 "$STEP_PID" 2>/dev/null; do
      set -- $SPIN
      _i=$(( (_i + 1) % $# ))
      eval "_frame=\${$((_i + 1))}"
      # The step's latest output line rides along, so a slow package
      # manager ("Waiting for cache lock…") never looks like a hang.
      _last=$(tail -n 1 "$LOG" 2>/dev/null | tr -d '\r' | tr -cd '[:print:]' | cut -c1-"$_w")
      printf '\r%s%s%s%s %s%s' "$CLR" "$C_CYAN" "$_frame" "$C_OFF" "$_label" "${_last:+ $C_DIM— $_last$C_OFF}"
      sleep 0.1 2>/dev/null || sleep 1
    done
    if wait "$STEP_PID"; then _rc=0; else _rc=$?; fi
    STEP_PID=""
    printf '\r%s%s' "$CLR" "$SHOW"
  else
    if "$@" >"$LOG" 2>&1 < /dev/null; then _rc=0; else _rc=$?; fi
    STEP_PID=""
  fi
  STEP_LABEL=""
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

# ── Required tools ───────────────────────────────────────────────────────
# Everything the script needs, checked up front: one clear list beats a
# "command not found" halfway through.
missing=""
for tool in curl tar sha256sum mktemp install useradd id grep sed uname; do
  command -v "$tool" >/dev/null 2>&1 || missing="$missing $tool"
done

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
# cmd_for <family>: the install command as shown to the person.
cmd_for() {
  case "$1" in
    debian) printf 'apt-get install -y %s' "$(pkgs_for debian)" ;;
    fedora) printf '%s install -y %s' "$(dnf_or_yum)" "$(pkgs_for fedora)" ;;
    rhel)   printf '%s install -y %s' "$(dnf_or_yum)" "$(pkgs_for rhel)" ;;
    suse)   printf 'zypper install -y %s' "$(pkgs_for suse)" ;;
    arch)   printf 'pacman -S --noconfirm %s' "$(pkgs_for arch)" ;;
    alpine) printf 'apk add %s' "$(pkgs_for alpine)" ;;
  esac
}
dnf_or_yum() { command -v dnf >/dev/null 2>&1 && printf dnf || printf yum; }
# install_pkgs <family>: the same install, made unattended. Package
# managers love to stop and ask — debconf and needrestart dialogs on
# Debian, conffile questions, pagers, lock waits — and every one of those
# would sit invisibly behind the spinner. So: no frontend, no restart
# prompts, keep existing config files, bounded lock waits, quiet output.
install_pkgs() {
  _p=$(pkgs_for "$1")
  case "$1" in
    debian)
      export DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a NEEDRESTART_SUSPEND=1 APT_LISTCHANGES_FRONTEND=none UCF_FORCE_CONFFOLD=1
      # A fresh box may have no package lists yet.
      ls /var/lib/apt/lists/*Packages >/dev/null 2>&1 || step "refreshing the package lists" apt-get -q -o DPkg::Lock::Timeout=180 update || return 1
      # shellcheck disable=SC2086
      step "installing$missing" apt-get -q -y -o DPkg::Lock::Timeout=180 -o Dpkg::Options::=--force-confold -o Dpkg::Options::=--force-confdef install $_p ;;
    fedora|rhel)
      _m=$(dnf_or_yum)
      # shellcheck disable=SC2086
      step "installing$missing" "$_m" -y -q --setopt=install_weak_deps=False install $_p ;;
    suse)
      export ZYPP_LOCK_TIMEOUT=180
      # shellcheck disable=SC2086
      step "installing$missing" zypper --non-interactive --quiet install -y --no-recommends $_p ;;
    arch)
      # shellcheck disable=SC2086
      step "installing$missing" pacman -Sy --noconfirm --needed --noprogressbar $_p ;;
    alpine)
      # shellcheck disable=SC2086
      step "installing$missing" apk add --no-cache --no-progress -q $_p ;;
    *) return 1 ;;
  esac
}
# ask <question> [default y|n] [answer-under---yes y|n]: y/n from the
# terminal even when the script itself arrives on stdin (curl | sh). Enter
# takes the default; no terminal → the default; --yes → the third argument.
ask() {
  _def=${2:-y}; _auto=${3:-$_def}
  if [ "$ASSUME_YES" = 1 ]; then [ "$_auto" = y ]; return; fi
  ( : < /dev/tty ) 2>/dev/null || { [ "$_def" = y ]; return; }
  if [ "$_def" = y ]; then printf '%s [Y/n] ' "$1"; else printf '%s [y/N] ' "$1"; fi
  read -r answer < /dev/tty || answer=""
  case "$answer" in
    "") [ "$_def" = y ] ;;
    y|Y|yes|YES|Yes) return 0 ;;
    *) return 1 ;;
  esac
}
# interactive: a terminal to ask on (and no --yes).
interactive() { [ "$ASSUME_YES" != 1 ] && ( : < /dev/tty ) 2>/dev/null; }

# ── Uninstall ────────────────────────────────────────────────────────────
# Everything the installer put here, in reverse: service and timer, cron
# entry, state, config with the token (overwritten before it is deleted),
# the service user, the binary. The agent does the work (`uninstall` reads
# the paths agent.yml names); without a working binary the same steps run
# from here with the default paths. Nothing is removed without a yes.
if [ "$UNINSTALL" = 1 ]; then
  [ "$(id -u)" -eq 0 ] || fail "uninstall needs root: re-run with sudo"
  present=""
  [ -e /etc/systemd/system/watchfor-agent.service ] && present="$present /etc/systemd/system/watchfor-agent.service"
  [ -e /etc/systemd/system/watchfor-agent-update.timer ] && present="$present /etc/systemd/system/watchfor-agent-update.timer"
  [ -e /etc/cron.d/watchfor-agent ] && present="$present /etc/cron.d/watchfor-agent"
  [ -e "$LIB" ] && present="$present $LIB"
  [ -e "$ETC" ] && [ "$KEEP_CONFIG" = 0 ] && present="$present $ETC"
  [ -e "$BIN" ] && present="$present $BIN"
  id -u watchfor-agent >/dev/null 2>&1 && present="$present user:watchfor-agent"
  if [ -z "$present" ]; then ok "nothing of watchfor-agent is on this machine"; exit 0; fi
  printf '%sThis removes watchfor-agent from this machine:%s\n' "$C_BOLD" "$C_OFF"
  for p in $present; do printf '  %s\n' "$p"; done
  [ "$KEEP_CONFIG" = 0 ] || note "  $ETC is kept (--keep-config)"
  printf '\n'
  ask "  Remove all of the above?" n y || {
    if interactive; then note "nothing removed"; exit 0; fi
    fail "nothing removed: no terminal to ask on — add -y to remove without asking"
  }
  printf '\n'
  if [ -n "$CURRENT" ]; then
    keep=""; [ "$KEEP_CONFIG" = 0 ] || keep="-keep-config"
    if out=$("$BIN" uninstall -yes $keep 2>&1); then
      printf '%s\n' "$out" | grep -v '^This removes\|^  \|^watchfor-agent removed' | sed "s/^/  /"
      ok "removed watchfor-agent $CURRENT"
    else
      printf '%s\n' "$out" >&2; fail "uninstall failed"
    fi
  else
    # no working binary to ask: the same steps by hand, default paths
    systemctl disable -q --now watchfor-agent 2>/dev/null || true
    systemctl disable -q --now watchfor-agent-update.timer 2>/dev/null || true
    for f in /etc/systemd/system/watchfor-agent.service /etc/systemd/system/watchfor-agent-update.service \
             /etc/systemd/system/watchfor-agent-update.timer /etc/cron.d/watchfor-agent /run/lock/watchfor-agent-upgrade.lock; do
      [ -e "$f" ] || continue
      rm -f "$f" && note "  removed $f"
    done
    systemctl daemon-reload 2>/dev/null || true
    systemctl reset-failed watchfor-agent 2>/dev/null || true
    if [ -e "$LIB" ]; then rm -rf "$LIB" && note "  removed $LIB"; fi
    if [ "$KEEP_CONFIG" = 1 ]; then
      note "  kept $ETC"
    elif [ -e "$ETC" ]; then
      # zero the token before the unlink so it does not linger in freed blocks
      [ -f "$ETC/token" ] && dd if=/dev/zero of="$ETC/token" bs=1 count="$(wc -c < "$ETC/token")" conv=notrunc 2>/dev/null
      rm -rf "$ETC" && note "  removed $ETC (token overwritten first)"
    fi
    if id -u watchfor-agent >/dev/null 2>&1; then userdel watchfor-agent 2>/dev/null && note "  removed system user watchfor-agent"; fi
    # a kept config must not belong to a uid that no longer exists
    [ "$KEEP_CONFIG" = 0 ] || [ ! -d "$ETC" ] || chown -R root:root "$ETC" 2>/dev/null || true
    if [ -e "$BIN" ]; then rm -f "$BIN" && note "  removed $BIN"; fi
    ok "removed watchfor-agent${CURRENT:+ $CURRENT}"
  fi
  printf '\n'
  note "Remove the host in WatchFor as well (Hosts → the host → Remove): that"
  note "revokes its token and drops its history."
  printf '\n'
  exit 0
fi

if ! command -v systemctl >/dev/null 2>&1; then
  problem "systemd not found on ${DISTRO_NAME:-this system}"
  cat >&2 <<MSG
  This script installs watchfor-agent as a systemd service. Without
  systemd, install the binary by hand and run it from cron instead —
  both are a few lines: https://github.com/$REPO#install-by-hand

MSG
  exit 1
fi

if [ -n "$missing" ]; then
  printf '%s%s missing:%s%s\n\n' "$C_RED" "$I_BAD" "$C_OFF" "$missing" >&2
  case "$missing" in " "*" "*) what=them ;; *) what=it ;; esac
  cmd=""; [ -z "$FAMILY" ] || cmd=$(cmd_for "$FAMILY")
  if [ -n "$cmd" ] && [ "$(id -u)" -eq 0 ]; then
    printf '  Install %s with:  %s%s%s   %s%s%s\n\n' "$what" "$C_GREEN" "$cmd" "$C_OFF" "$C_DIM" "$DISTRO_NAME" "$C_OFF" >&2
    if ! interactive && [ "$ASSUME_YES" != 1 ]; then
      fail "no terminal to ask on: install $what first ($cmd) or add -y, then run the same command again"
    fi
    if ask "  Do you want me to install $what now?" y y; then
      printf '\n'
      install_pkgs "$FAMILY" || failstep "package installation failed — install $what by hand and run the same command again"
      hash -r 2>/dev/null || true
      still=""
      for t in $missing; do command -v "$t" >/dev/null 2>&1 || still="$still $t"; done
      [ -z "$still" ] || fail "still missing after installation:$still — install $what by hand and run the same command again"
      ok "installed$missing"
      printf '\n'
    else
      printf '\n  Install %s, then run the same command again.\n' "$what" >&2
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
    exit 1
  fi
fi
[ "$(id -u)" -eq 0 ] || fail "run as root (sudo)"
if [ "$TOKEN" = "-" ]; then
  ( : < /dev/tty ) 2>/dev/null || fail "--token - needs a terminal to ask on; pass --token <token> or set WATCHFOR_TOKEN"
  printf 'Host token (from the dashboard, not echoed): '
  stty -echo < /dev/tty 2>/dev/null && ECHO_OFF=1
  read -r TOKEN < /dev/tty || TOKEN=""
  stty echo < /dev/tty 2>/dev/null || true
  ECHO_OFF=0
  printf '\n'
  [ -n "$TOKEN" ] || fail "no token entered"
fi

# ── The release ──────────────────────────────────────────────────────────
if [ "$VERSION" = "latest" ]; then
  # watchfor.io names the newest release it has verified.
  step "looking up the latest release" sh -c '$CURL -fsSL --connect-timeout 15 --max-time 30 "$1/latest" | tr -d "[:space:]" > "$2" && grep -q "^v[0-9]" "$2"' sh "$RELEASES" "$TMP/tag" \
    || failstep "could not resolve the latest release from $RELEASES/latest"
  VERSION=$(cat "$TMP/tag")
fi
TAG="$VERSION"
VER="${VERSION#v}"
case "$VER" in
  0.[0-6].*) fail "this installer needs watchfor-agent 0.7.0 or newer (asked for $VER): older releases carry their own install.sh on the release page" ;;
esac
BASE="$DOWNLOAD/$TAG"
TARBALL="watchfor-agent_${VER}_linux_${ARCH}.tar.gz"

# An existing install turns this into an upgrade: same checks, then the
# binary is swapped and the service restarted. Token and config stay.
if [ "$CURRENT" = "$VER" ] && [ ! -f /etc/systemd/system/watchfor-agent.service ]; then
  note "watchfor-agent $VER is installed but its service unit is missing: fetching the release again"
  REINSTALL=1
fi
if [ "$CURRENT" = "$VER" ]; then
  ok "watchfor-agent $C_BOLD$VER$C_OFF is installed and current"
  if [ "$REINSTALL" != 1 ] && interactive && ask "  Reinstall it anyway (fresh download of the same version)?" n n; then
    REINSTALL=1
  fi
elif [ -n "$CURRENT" ]; then
  if [ "$(printf '%s\n%s\n' "$CURRENT" "$VER" | sort -V | head -n 1)" = "$CURRENT" ]; then
    ok "upgrade: watchfor-agent $C_BOLD$CURRENT $ARROW $VER$C_OFF for linux/$ARCH"
  else
    ok "downgrade: watchfor-agent $C_BOLD$CURRENT $ARROW $VER$C_OFF for linux/$ARCH"
  fi
else
  ok "install: watchfor-agent $C_BOLD$VER$C_OFF for linux/$ARCH"
fi

# Verification, one step (steps live in their own process group, so
# everything arrives as arguments): the sha256 of the tarball against the
# checksums watchfor.io served, then the release signature over those
# checksums, checked by the downloaded agent with its built-in key. The
# binary is staged next to its final place first — /tmp may be noexec.
# A reason is left for the caller when it fails.
# What carries the trust here is the first check: the tarball's sha256
# against checksums.txt fetched from watchfor.io, which serves only a
# checksums file it verified against the release signature — and it runs
# before the new binary is executed. The binary's own signature check
# afterwards is belt and braces (its key is compiled in), not the anchor.
VERIFY_SCRIPT='
tmp=$1; base=$2; tarball=$3; bin=$4
(cd "$tmp" && grep " $tarball\$" checksums.txt | sha256sum -c --quiet) \
  || { echo "checksum mismatch: $tarball does not match the checksums watchfor.io serves" > "$tmp/reason"; exit 1; }
$CURL -fsSL --connect-timeout 15 --max-time 60 -o "$tmp/checksums.txt.minisig" "$base/checksums.txt.minisig" \
  || { echo "could not download the release signature ($base/checksums.txt.minisig)" > "$tmp/reason"; exit 1; }
tar -xzf "$tmp/$tarball" -C "$tmp" watchfor-agent packaging/watchfor-agent.service \
  || { echo "could not extract $tarball" > "$tmp/reason"; exit 1; }
install -m 0755 "$tmp/watchfor-agent" "$bin.new" \
  || { echo "could not stage the binary next to $bin" > "$tmp/reason"; exit 1; }
"$bin.new" verify -q -checksums "$tmp/checksums.txt" -sig "$tmp/checksums.txt.minisig" "$tmp/$tarball" \
  || { rm -f "$bin.new"; echo "signature verification failed: the release is not signed by the WatchFor key" > "$tmp/reason"; exit 1; }
'

if [ "$CURRENT" != "$VER" ] || [ "$REINSTALL" = 1 ]; then
  # The checksums come from watchfor.io, which serves them only after
  # verifying the release signature: a file swapped on GitHub cannot pass.
  step "fetching the release checksums from watchfor.io" $CURL -fsSL --connect-timeout 15 --max-time 60 -o "$TMP/checksums.txt" "$RELEASES/checksums/$TAG.txt" \
    || failstep "could not download $RELEASES/checksums/$TAG.txt"
  if [ "$TTY" = 1 ]; then
    # curl draws its own progress bar on a terminal; both lines are then
    # replaced by the one-line result.
    printf '%s%s%s downloading %s\n' "$C_CYAN" "$I_DOWN" "$C_OFF" "$TARBALL"
    if $CURL -fL -# --connect-timeout 15 --max-time 600 -o "$TMP/$TARBALL" "$BASE/$TARBALL"; then
      # curl ends its bar with a newline: clear that line, the bar, the label.
      printf '\r%s\033[1A\r%s\033[1A\r%s' "$CLR" "$CLR" "$CLR"
    else
      printf '\n'; fail "could not download $BASE/$TARBALL"
    fi
  else
    $CURL -fsSL --connect-timeout 15 --max-time 600 -o "$TMP/$TARBALL" "$BASE/$TARBALL" || fail "could not download $BASE/$TARBALL"
  fi
  size=$(wc -c < "$TMP/$TARBALL" | awk '{printf "%.1f MB", $1/1048576}')
  ok "downloaded $TARBALL ${C_DIM}($size)$C_OFF"
  step "verifying the sha256 and the release signature" sh -c "$VERIFY_SCRIPT" sh "$TMP" "$BASE" "$TARBALL" "$BIN" \
    || fail "$(cat "$TMP/reason" 2>/dev/null || echo "verification failed")"
  ok "verified sha256 (checksums from watchfor.io) and release signature ${C_DIM}(agent built-in key)$C_OFF"
  verify_state="sha256 via watchfor.io + signature (agent key)"
  # The verified, staged binary takes its place; the unit that shipped in
  # the same tarball is installed further down.
  step "installing $BIN" mv -f "$BIN.new" "$BIN" || failstep "could not install $BIN"
  ok "installed $BIN"
fi

# ── User, directories, token, config ─────────────────────────────────────
step "creating the service user and directories" sh -c '
  id -u watchfor-agent >/dev/null 2>&1 || useradd --system --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin --user-group watchfor-agent
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
  # The agent writes the config: every key explained, and the mounts,
  # disks and interfaces it found listed inside, so choosing what to
  # watch is editing a list.
  step "writing $ETC/agent.yml (detecting mounts, disks, interfaces)" "$BIN" config init -server "$SERVER" -token-file "$ETC/token" -spool-dir "$LIB" -out "$ETC/agent.yml" \
    || failstep "could not write $ETC/agent.yml"
  ok "config written: $ETC/agent.yml ${C_DIM}(what this machine has is listed inside)$C_OFF"
else
  ok "config kept: $ETC/agent.yml"
fi

# ── auto-update: ask when no flag decided it ─────────────────────────────
if [ -z "$AUTO_UPDATE" ]; then
  if systemctl is-enabled -q watchfor-agent-update.timer 2>/dev/null; then
    if interactive; then
      if ask "  Auto-update is on (a newer signed release the server reports is installed daily). Keep it on?" y y; then AUTO_UPDATE=1; else AUTO_UPDATE=0; fi
    fi
  elif interactive || [ "$ASSUME_YES" = 1 ]; then
    if ask "  Enable daily auto-update? It installs newer signed releases the server reports and restarts the agent." y n; then AUTO_UPDATE=1; else AUTO_UPDATE=0; fi
  fi
fi

# ── systemd ──────────────────────────────────────────────────────────────
step "installing the systemd service" sh -c '
  unit=/etc/systemd/system/watchfor-agent.service
  # the unit ships in the verified tarball; an unchanged install keeps its own
  [ ! -f "$1/packaging/watchfor-agent.service" ] || install -m 0644 "$1/packaging/watchfor-agent.service" "$unit"
  [ -f "$unit" ] || { echo "no unit file: $unit" >&2; exit 1; }
  systemctl daemon-reload
  systemctl enable -q --now watchfor-agent
  [ -z "$2" ] || [ "$2" = "$3" ] || systemctl try-restart watchfor-agent' sh "$TMP" "$CURRENT" "$VER" \
  || failstep "could not enable the systemd service"
# Auto-update: the agent owns the timer and the updates.auto line in agent.yml.
case "$AUTO_UPDATE" in
  1|0)
    if [ "$AUTO_UPDATE" = 1 ]; then _au=on; else _au=off; fi
    step "turning auto-update $_au" "$BIN" auto-update -config "$ETC/agent.yml" "$_au" || failstep "could not turn auto-update $_au" ;;
esac
if [ -n "$CURRENT" ] && [ "$CURRENT" != "$VER" ]; then
  ok "service restarted on watchfor-agent $VER"
  service_state="watchfor-agent, restarted, runs as user watchfor-agent"
elif [ "$REINSTALL" = 1 ]; then
  systemctl try-restart watchfor-agent
  ok "reinstalled watchfor-agent $VER and restarted the service"
  service_state="reinstalled and restarted, runs as user watchfor-agent"
else
  ok "service enabled and running"
  service_state="watchfor-agent, running as user watchfor-agent"
fi
if systemctl is-enabled -q watchfor-agent-update.timer 2>/dev/null; then
  ok "auto-update on: a newer signed release the server reports is installed daily"
  update_state="on, daily (watchfor-agent-update.timer)"
else
  update_state="off: sudo watchfor-agent upgrade, or --auto-update"
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
[ -z "${verify_state:-}" ] || line "verified" "$verify_state"
line "auto-update" "$update_state"
line "status" "systemctl status watchfor-agent"
line "logs" "journalctl -u watchfor-agent -f"
[ -n "$TOKEN" ] || [ -s "$ETC/token" ] || line "token" "missing: $ETC/token (chmod 600), then restart"
printf '%s%s%s%s%s\n\n' "$C_DIM" "$B_BL" "$(rule $((W + 16)))" "$B_BR" "$C_OFF"
