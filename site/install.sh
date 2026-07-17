#!/bin/sh
# Iron Rain daemon installer.
#   curl -fsSL https://howlerops.github.io/oculus/install.sh | sh
#
# Builds the daemon (`oculusd`) from source and installs it to ~/.local/bin. Requires
# git + Go (>=1.22). Set OCULUS_BIN to change the install dir, OCULUS_SRC for the checkout.
set -eu

REPO="${OCULUS_REPO:-https://github.com/howlerops/oculus}"
SRC="${OCULUS_SRC:-$HOME/.oculus/src}"
BIN="${OCULUS_BIN:-$HOME/.local/bin}"

say() { printf '\033[33m→\033[0m %s\n' "$1"; }
die() { printf '\033[31mx\033[0m %s\n' "$1" >&2; exit 1; }

command -v git >/dev/null 2>&1 || die "git is required (xcode-select --install)"
command -v go  >/dev/null 2>&1 || die "Go is required — install from https://go.dev/dl/ or 'brew install go'"

say "Fetching the Iron Rain daemon source…"
if [ -d "$SRC/.git" ]; then
  git -C "$SRC" pull --ff-only --quiet
else
  mkdir -p "$(dirname "$SRC")"
  git clone --depth 1 --quiet "$REPO" "$SRC"
fi

say "Building oculusd…"
( cd "$SRC/daemon" && go build -o oculusd . )

mkdir -p "$BIN"
cp "$SRC/daemon/oculusd" "$BIN/oculusd"
printf '\033[32m✓\033[0m installed %s\n' "$BIN/oculusd"

case ":$PATH:" in
  *":$BIN:"*) : ;;
  *) printf '\033[33m!\033[0m add %s to your PATH, e.g.  echo '\''export PATH="%s:$PATH"'\'' >> ~/.zshrc\n' "$BIN" "$BIN" ;;
esac

cat <<'EOF'

Next:
  oculusd serve --secret <your-secret>

It auto-detects opencode, claude-code, and pi, then prints a pairing QR —
scan it from the Iron Rain iOS app and you're connected.
EOF
