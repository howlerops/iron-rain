#!/bin/sh
# Iron Rain installer.
#   curl -fsSL https://howlerops.github.io/iron-rain/install.sh | sh
#
# macOS: installs the daemon + the Iron Rain app, then launches the app (which starts the
# daemon for you — no terminal). Linux/headless: installs the daemon and starts it.
# Set OCULUS_BIN to change the daemon dir; OCULUS_NO_APP=1 to skip the GUI app.
set -eu

REPO="${OCULUS_REPO:-howlerops/iron-rain}"
BIN="${OCULUS_BIN:-$HOME/.local/bin}"
REL="https://github.com/$REPO/releases/latest/download"
# macOS launchd agent: keeps the daemon alive across reboots/crashes (set OCULUS_NO_AGENT=1 to skip).
AGENT_LABEL="com.howlerops.oculusd"
AGENT_PLIST="$HOME/Library/LaunchAgents/$AGENT_LABEL.plist"

say() { printf '\033[33m→\033[0m %s\n' "$1"; }
ok()  { printf '\033[32m✓\033[0m %s\n' "$1"; }
die() { printf '\033[31mx\033[0m %s\n' "$1" >&2; exit 1; }
daemon_up()  { curl -fsS -m 1 http://127.0.0.1:6000/healthz >/dev/null 2>&1; }
port_pids()  { lsof -tiTCP:6000 -sTCP:LISTEN 2>/dev/null | tr '\n' ' '; }

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
arch="$(uname -m)"
case "$arch" in x86_64|amd64) arch="amd64" ;; arm64|aarch64) arch="arm64" ;; *) die "unsupported arch: $arch" ;; esac
case "$os" in darwin|linux) : ;; *) die "unsupported OS: $os" ;; esac

tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT

# --- 1. The daemon (oculusd) ---
say "Downloading the Iron Rain daemon ($os/$arch)…"
if curl -fsSL "$REL/oculusd_${os}_${arch}.tar.gz" -o "$tmp/o.tgz" 2>/dev/null; then
  # Staged into the destination directory and RENAMED into place, never written over the existing
  # file. `install` opens the target and writes through it, which on macOS is enough to poison the
  # kernel's code-signing cache for that PATH when the old binary is still executing: every later
  # exec is SIGKILLed by AMFI even though `codesign -v` on the file passes. Observed exactly that
  # way — identical bytes ran fine from a different path and were killed from this one, and only
  # replacing the directory entry cleared it. A rename swaps the entry, so the running process keeps
  # its inode and the next exec sees a genuinely new file.
  tar -xzf "$tmp/o.tgz" -C "$tmp"; mkdir -p "$BIN"
  install -m 0755 "$tmp/oculusd" "$BIN/.oculusd.new" && mv -f "$BIN/.oculusd.new" "$BIN/oculusd"
  # Release binaries are now SIGNED in CI, so this is a repair path rather than the norm: it fixes a
  # binary whose signature didn't survive the trip (quarantine xattr, an older unsigned release).
  #
  # On Apple Silicon an unsigned or invalid Mach-O is SIGKILLed by AMFI at exec, before any of the
  # daemon's own code runs — which surfaces as a launchd crash-loop with no panic and nothing in any
  # log. So a signature that will not verify is a HARD FAILURE here, not a warning. This previously
  # printed a warning and installed the binary anyway, which handed the user something that could not
  # start and no way to find out why.
  if [ "$os" = "darwin" ]; then
    xattr -c "$BIN/oculusd" 2>/dev/null || true
    if ! codesign --verify --strict "$BIN/oculusd" 2>/dev/null; then
      say "signing the daemon for this machine…"
      codesign --force --sign - "$BIN/oculusd" 2>/dev/null || true
    fi
    if ! codesign --verify --strict "$BIN/oculusd" 2>/dev/null; then
      echo "error: $BIN/oculusd has no valid code signature, so macOS will refuse to run it." >&2
      echo "       (Apple Silicon kills unsigned binaries at exec — the daemon would crash-loop.)" >&2
      echo "       Try: codesign --force --sign - \"$BIN/oculusd\"" >&2
      exit 1
    fi
  fi
  ok "installed $BIN/oculusd"
else
  command -v go >/dev/null 2>&1 || die "no release binary and Go not found (https://go.dev/dl/)"
  command -v git >/dev/null 2>&1 || die "no release binary and git not found"
  say "building the daemon from source…"
  src="${OCULUS_SRC:-$HOME/.oculus/src}"
  if [ -d "$src/.git" ]; then git -C "$src" pull --ff-only --quiet
  else mkdir -p "$(dirname "$src")"; git clone --depth 1 --quiet "https://github.com/$REPO" "$src"; fi
  # Same staged rename as the download path above — a source build replaces the binary just as often
  # as a release does, and writing through a live one poisons the path identically.
  ( cd "$src/daemon" && go build -o oculusd . ); mkdir -p "$BIN"
  install -m 0755 "$src/daemon/oculusd" "$BIN/.oculusd.new" && mv -f "$BIN/.oculusd.new" "$BIN/oculusd"
  ok "built + installed $BIN/oculusd"
fi

# On an UPDATE, an old daemon is usually still running on :6000. The app (and this script)
# defer to whatever is listening there, so without this the freshly installed binary never
# takes over and newer app messages fail against the stale daemon (e.g. "unknown type: …").
# Stop it so the new binary is (re)started below / by the app.
if daemon_up; then
  say "stopping the running daemon so the update takes effect…"
  # If a launchd agent owns it, bootout first — otherwise KeepAlive instantly relaunches the OLD
  # binary and the update never takes hold. Section 1b re-loads it (new binary) below.
  if [ "$os" = "darwin" ]; then launchctl bootout "gui/$(id -u)/$AGENT_LABEL" 2>/dev/null || true; fi
  pkill -x oculusd 2>/dev/null || true
  for _ in 1 2 3 4 5; do daemon_up || break; sleep 1; done
  # pkill -x matches only a process named exactly "oculusd". If something else is still holding
  # :6000 (a renamed/stale binary, a squatter), free the port by PID — otherwise the new daemon
  # silently fails to bind and the app keeps talking to the old one.
  if daemon_up && [ -n "$(port_pids)" ]; then
    say "port 6000 still held by PID $(port_pids) — freeing it…"
    kill $(port_pids) 2>/dev/null || true
    for _ in 1 2 3 4 5; do daemon_up || break; sleep 1; done
  fi
fi

# --- 1b. Auto-start at login (macOS launchd agent) ---
# Without this the daemon only runs while the app is open (it's spawned as an app child), so a
# reboot leaves it down until you relaunch the app — and any session that can't be re-attached is
# lost. RunAtLoad starts it at login; KeepAlive restarts it if it crashes. The app defers to
# whatever already answers :6000, so the two never fight.
if [ "$os" = "darwin" ] && [ "${OCULUS_NO_AGENT:-0}" != "1" ]; then
  mkdir -p "$HOME/Library/LaunchAgents" "$HOME/.oculus"
  cat >"$AGENT_PLIST" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key><string>$AGENT_LABEL</string>
    <key>ProgramArguments</key>
    <array>
        <string>${SHELL:-/bin/zsh}</string>
        <string>-lc</string>
        <string>exec "$BIN/oculusd" serve --addr 0.0.0.0:6000</string>
    </array>
    <key>RunAtLoad</key><true/>
    <key>KeepAlive</key><true/>
    <key>ProcessType</key><string>Background</string>
    <key>StandardOutPath</key><string>$HOME/.oculus/oculusd.log</string>
    <key>StandardErrorPath</key><string>$HOME/.oculus/oculusd.log</string>
</dict>
</plist>
PLIST
  launchctl bootout "gui/$(id -u)/$AGENT_LABEL" 2>/dev/null || true
  if launchctl bootstrap "gui/$(id -u)" "$AGENT_PLIST" 2>/dev/null || launchctl load -w "$AGENT_PLIST" 2>/dev/null; then
    for _ in 1 2 3 4 5; do daemon_up && break; sleep 1; done
    ok "daemon set to start at login (auto-restarts on crash). Toggle it off in the app's ⋯ menu."
  else
    say "couldn't load the launch agent — the app will still start the daemon when open."
  fi
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
# On macOS the launchd agent (section 1b) already owns startup, so don't also nohup a second
# copy (that would just fail to bind :6000). Verify the agent's daemon is up and we're done.
if [ "$os" = "darwin" ] && [ "${OCULUS_NO_AGENT:-0}" != "1" ]; then
  for _ in 1 2 3 4 5; do daemon_up && break; sleep 1; done
  daemon_up && ok "daemon running via launchd (log: ~/.oculus/oculusd.log). Scan the pairing QR there." \
             || die "the daemon didn't come up. Last lines of ~/.oculus/oculusd.log:
$(tail -5 "$HOME/.oculus/oculusd.log" 2>/dev/null)"
  case ":$PATH:" in *":$BIN:"*) : ;; *) printf '\033[33m!\033[0m add %s to your PATH: echo '\''export PATH="%s:$PATH"'\'' >> ~/.zshrc\n' "$BIN" "$BIN" ;; esac
  exit 0
fi
if daemon_up; then
  # Still up after we tried to stop + free the port → a process we couldn't stop is squatting :6000.
  die "port 6000 is in use by another process (PID $(port_pids)) that couldn't be stopped. Stop it, then re-run."
fi
# --addr 0.0.0.0: bind all interfaces so the pairing QR carries the Mac's LAN IP (reachable from
# the phone on the same network), not ws://127.0.0.1. No --secret: the daemon persists a STABLE
# secret (~/.oculus/secret) so re-runs/updates keep already-paired clients authorized.
mkdir -p "$HOME/.oculus"
nohup "$BIN/oculusd" serve --addr 0.0.0.0:6000 >"$HOME/.oculus/oculusd.log" 2>&1 &
# Verify it actually came up + bound the port — a bind failure ("address already in use") would
# otherwise just land in the log and the script would falsely report success.
for _ in 1 2 3 4 5; do daemon_up && break; sleep 1; done
if daemon_up; then
  ok "started the daemon (log: ~/.oculus/oculusd.log). Scan the pairing QR it printed there."
else
  die "the daemon didn't come up. Last lines of ~/.oculus/oculusd.log:
$(tail -5 "$HOME/.oculus/oculusd.log" 2>/dev/null)"
fi

case ":$PATH:" in *":$BIN:"*) : ;; *) printf '\033[33m!\033[0m add %s to your PATH: echo '\''export PATH="%s:$PATH"'\'' >> ~/.zshrc\n' "$BIN" "$BIN" ;; esac
