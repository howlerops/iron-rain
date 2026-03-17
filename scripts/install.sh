#!/usr/bin/env bash
set -euo pipefail

# Iron Rain installer
# Usage: curl -fsSL https://raw.githubusercontent.com/howlerops/iron-rain/main/scripts/install.sh | bash

PACKAGE="@howlerops/iron-rain-cli"

echo "  ___                    ____       _       "
echo " |_ _|_ __ ___  _ __   |  _ \\ __ _(_)_ __  "
echo "  | || '__/ _ \\| '_ \\  | |_) / _\` | | '_ \\ "
echo "  | || | | (_) | | | | |  _ < (_| | | | | |"
echo " |___|_|  \\___/|_| |_| |_| \\_\\__,_|_|_| |_|"
echo ""
echo "Multi-model orchestration for terminal-based coding"
echo ""

# Detect OS and architecture
OS="$(uname -s)"
ARCH="$(uname -m)"

echo "Detected: $OS $ARCH"
echo ""

# Check if Bun is installed
if command -v bun &> /dev/null; then
  BUN_VERSION=$(bun --version 2>/dev/null || echo "unknown")
  echo "Found Bun: v$BUN_VERSION"
else
  echo "Bun is not installed. Installing Bun..."
  echo ""

  case "$OS" in
    Linux|Darwin)
      curl -fsSL https://bun.sh/install | bash
      # Source the updated profile to get bun in PATH
      export BUN_INSTALL="$HOME/.bun"
      export PATH="$BUN_INSTALL/bin:$PATH"
      ;;
    *)
      echo "Error: Unsupported OS: $OS"
      echo "Please install Bun manually: https://bun.sh"
      exit 1
      ;;
  esac

  if ! command -v bun &> /dev/null; then
    echo "Error: Bun installation failed or not in PATH."
    echo "Try adding ~/.bun/bin to your PATH and running this script again."
    exit 1
  fi

  echo "Bun installed successfully: v$(bun --version)"
fi

echo ""
echo "Installing $PACKAGE..."
bun add -g "$PACKAGE"

echo ""

# Verify installation
if command -v iron-rain &> /dev/null; then
  echo "Installation successful!"
  echo ""
  iron-rain version
  echo ""
  echo "Run 'iron-rain' to get started."
else
  echo "Warning: iron-rain command not found in PATH."
  echo "You may need to add ~/.bun/bin to your PATH:"
  echo "  export PATH=\"\$HOME/.bun/bin:\$PATH\""
  echo ""
  echo "Then run: iron-rain"
fi
