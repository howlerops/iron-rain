#!/bin/sh
# Iron Rain installer.
#   curl -fsSL https://howlerops.github.io/oculus/install.sh | sh
#
# macOS: installs the daemon + the Iron Rain app, then launches the app (which starts the
# daemon for you — no terminal). Linux/headless: installs the daemon and starts it.
# Set OCULUS_BIN to change the daemon dir; OCULUS_NO_APP=1 to skip the GUI app.
set -eu

REPO="${OCULUS_REPO:-howlerops/oculus}"
BIN="${OCULUS_BIN:-$HOME/.local/bin}"
REL="https://github.com/$REPO/releases/latest/download"

say() { printf '\033[33m→\033[0m %s\n' "$1"; }
ok()  { printf '\033[32m✓\033[0m %s\n' "$1"; }
die() { printf '\033[31mx\033[0m %s\n' "$1" >&2; exit 1; }
daemon_up() { curl -fsS -m 1 http://127.0.0.1:6000/healthz >/dev/null 2>&1; }

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
arch="$(uname -m)"
case "$arch" in x86_64|amd64) arch="amd64" ;; arm64|aarch64) arch="arm64" ;; *) die "unsupported arch: $arch" ;; esac
case "$os" in darwin|linux) : ;; *) die "unsupported OS: $os" ;; esac

tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT

# --- 1. The daemon (oculusd) ---
say "Downloading the Iron Rain daemon ($os/$arch)…"
if curl -fsSL "$REL/oculusd_${os}_${arch}.tar.gz" -o "$tmp/o.tgz" 2>/dev/null; then
  tar -xzf "$tmp/o.tgz" -C "$tmp"; mkdir -p "$BIN"; install -m 0755 "$tmp/oculusd" "$BIN/oculusd"
  ok "installed $BIN/oculusd"
else
  command -v go >/dev/null 2>&1 || die "no release binary and Go not found (https://go.dev/dl/)"
  command -v git >/dev/null 2>&1 || die "no release binary and git not found"
  say "building the daemon from source…"
  src="${OCULUS_SRC:-$HOME/.oculus/src}"
  if [ -d "$src/.git" ]; then git -C "$src" pull --ff-only --quiet
  else mkdir -p "$(dirname "$src")"; git clone --depth 1 --quiet "https://github.com/$REPO" "$src"; fi
  ( cd "$src/daemon" && go build -o oculusd . ); mkdir -p "$BIN"; install -m 0755 "$src/daemon/oculusd" "$BIN/oculusd"
  ok "built + installed $BIN/oculusd"
fi

# On an UPDATE, an old daemon is usually still running on :6000. The app (and this script)
# defer to whatever is listening there, so without this the freshly installed binary never
# takes over and newer app messages fail against the stale daemon (e.g. "unknown type: …").
# Stop it so the new binary is (re)started below / by the app.
if daemon_up; then
  say "stopping the running daemon so the update takes effect…"
  pkill -x oculusd 2>/dev/null || true
  for _ in 1 2 3 4 5; do daemon_up || break; sleep 1; done
fi

# --- 2. The macOS app (which auto-starts the daemon) ---
if [ "$os" = "darwin" ] && [ "${OCULUS_NO_APP:-0}" != "1" ]; then
  say "Downloading the Iron Rain app…"
  if curl -fsSL "$REL/IronRain-macos.zip" -o "$tmp/app.zip" 2>/dev/null && ditto -x -k "$tmp/app.zip" "$tmp/app" 2>/dev/null; then
    osascript -e 'quit app "Iron Rain"' 2>/dev/null || true  # quit a running copy so we can replace + relaunch it
    sleep 1
    rm -rf "/Applications/Iron Rain.app"
    cp -R "$tmp/app/Iron Rain.app" "/Applications/Iron Rain.app"
    xattr -dr com.apple.quarantine "/Applications/Iron Rain.app" 2>/dev/null || true
    ok "installed /Applications/Iron Rain.app"
    open "/Applications/Iron Rain.app"
    ok "launched Iron Rain — it starts the daemon and shows a pairing QR. Done."
    exit 0
  fi
  say "app download unavailable — falling back to the daemon only."
fi

# --- 3. Headless / no app: start the daemon directly ---
if daemon_up; then
  ok "a daemon is already running on :6000."
else
  # --addr 0.0.0.0: bind all interfaces so the pairing QR carries the Mac's LAN IP (reachable from
  # the phone on the same network), not ws://127.0.0.1. No --secret: the daemon persists a STABLE
  # secret (~/.oculus/secret) so re-runs/updates keep already-paired clients authorized.
  mkdir -p "$HOME/.oculus"
  nohup "$BIN/oculusd" serve --addr 0.0.0.0:6000 >"$HOME/.oculus/oculusd.log" 2>&1 &
  sleep 1
  ok "started the daemon (log: ~/.oculus/oculusd.log). Scan the pairing QR it printed there."
fi

case ":$PATH:" in *":$BIN:"*) : ;; *) printf '\033[33m!\033[0m add %s to your PATH: echo '\''export PATH="%s:$PATH"'\'' >> ~/.zshrc\n' "$BIN" "$BIN" ;; esac
