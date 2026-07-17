#!/bin/sh
# Iron Rain daemon installer.
#   curl -fsSL https://howlerops.github.io/oculus/install.sh | sh
#
# Downloads a prebuilt `oculusd` binary (no Go or git needed) and installs it to
# ~/.local/bin. Set OCULUS_BIN to change the install dir.
set -eu

REPO="${OCULUS_REPO:-howlerops/oculus}"
BIN="${OCULUS_BIN:-$HOME/.local/bin}"

say() { printf '\033[33m→\033[0m %s\n' "$1"; }
die() { printf '\033[31mx\033[0m %s\n' "$1" >&2; exit 1; }

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
arch="$(uname -m)"
case "$arch" in
  x86_64|amd64) arch="amd64" ;;
  arm64|aarch64) arch="arm64" ;;
  *) die "unsupported architecture: $arch" ;;
esac
case "$os" in darwin|linux) : ;; *) die "unsupported OS: $os" ;; esac

asset="oculusd_${os}_${arch}.tar.gz"
url="https://github.com/$REPO/releases/latest/download/$asset"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

say "Downloading oculusd ($os/$arch)…"
if curl -fsSL "$url" -o "$tmp/$asset" 2>/dev/null; then
  tar -xzf "$tmp/$asset" -C "$tmp"
  mkdir -p "$BIN"
  install -m 0755 "$tmp/oculusd" "$BIN/oculusd"
  printf '\033[32m✓\033[0m installed %s\n' "$BIN/oculusd"
else
  # No prebuilt binary yet (or offline) → build from source if Go + git are available.
  say "No prebuilt binary found; building from source…"
  command -v git >/dev/null 2>&1 || die "no release binary and git not found"
  command -v go  >/dev/null 2>&1 || die "no release binary and Go not found (https://go.dev/dl/)"
  src="${OCULUS_SRC:-$HOME/.oculus/src}"
  if [ -d "$src/.git" ]; then git -C "$src" pull --ff-only --quiet; else
    mkdir -p "$(dirname "$src")"; git clone --depth 1 --quiet "https://github.com/$REPO" "$src"; fi
  ( cd "$src/daemon" && go build -o oculusd . )
  mkdir -p "$BIN"; install -m 0755 "$src/daemon/oculusd" "$BIN/oculusd"
  printf '\033[32m✓\033[0m built + installed %s\n' "$BIN/oculusd"
fi

case ":$PATH:" in
  *":$BIN:"*) : ;;
  *) printf '\033[33m!\033[0m add %s to your PATH:  echo '\''export PATH="%s:$PATH"'\'' >> ~/.zshrc\n' "$BIN" "$BIN" ;;
esac

cat <<'EOF'

Next:
  oculusd serve --secret <your-secret>

It auto-detects opencode, claude-code, and pi, then prints a pairing QR —
scan it from the Iron Rain iOS app and you're connected.
EOF
