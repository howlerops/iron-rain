#!/bin/bash
# Drive a REAL agent turn through the local daemon, on a schedule, so the telemetry dashboard has
# something real to show.
#
# Why this exists: telemetry that only records a developer's own sporadic use produces a dashboard
# nobody can read a trend from — and one you cannot tell apart from a broken pipeline, which is
# exactly the confusion that hid an ingest endpoint rejecting every report for a day.
#
# Why it runs HERE and not in CI: opencode on this machine authenticates with an OAuth token that
# refreshes. A refreshing credential in a GitHub secret goes stale and cannot write the new one back,
# so a CI soak would work for a few days and then silently stop — the precise failure mode this is
# meant to detect. Running locally uses the credential that already exists and keeps working.
#
# It reuses cmd/turn-smoke rather than reimplementing a driver: that already connects over the real
# encrypted wire, creates a session on a real provider, sends a prompt, and watches turn.state to a
# verdict. A soak is that, repeatedly, with varied prompts.
#
# Usage:  scripts/telemetry-soak.sh [iterations]        (default 3)
# Install: scripts/telemetry-soak.sh --install-schedule  (nightly launchd agent)

set -uo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PAIRING="$HOME/.oculus/pairing.json"
LOG="$HOME/.oculus/logs/soak.log"
LABEL="com.howlerops.ironrain.soak"
PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"

log() { printf '%s %s\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$*" | tee -a "$LOG"; }

install_schedule() {
  mkdir -p "$(dirname "$PLIST")" "$(dirname "$LOG")"
  cat > "$PLIST" <<PLIST_EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>$LABEL</string>
  <key>ProgramArguments</key>
  <array><string>$REPO/scripts/telemetry-soak.sh</string><string>3</string></array>
  <key>StartCalendarInterval</key><dict><key>Hour</key><integer>3</integer><key>Minute</key><integer>17</integer></dict>
  <!-- The Mac is often asleep at 03:17; without this the run is skipped entirely rather than
       deferred, and the dashboard shows a gap that looks like a broken pipeline. -->
  <key>RunAtLoad</key><false/>
  <key>StandardOutPath</key><string>$LOG</string>
  <key>StandardErrorPath</key><string>$LOG</string>
</dict></plist>
PLIST_EOF
  launchctl unload "$PLIST" 2>/dev/null
  launchctl load "$PLIST" && log "scheduled: $LABEL nightly at 03:17 (log: $LOG)"
}

[ "${1:-}" = "--install-schedule" ] && { install_schedule; exit $?; }

ITERATIONS="${1:-3}"
mkdir -p "$(dirname "$LOG")"

if [ ! -f "$PAIRING" ]; then
  log "FAIL no $PAIRING — the daemon writes it at startup; is oculusd running?"
  exit 1
fi

# Read the credentials out of the daemon's own pairing file. They never appear in the process list:
# turn-smoke takes them as arguments, so they are passed through the environment and expanded by the
# shell at exec time rather than being echoed anywhere.
PUB=$(python3 -c "import json,sys;print(json.load(open('$PAIRING'))['pub'])")
SECRET=$(python3 -c "import json,sys;print(json.load(open('$PAIRING'))['secret'])")
WS=$(python3 -c "import json,sys;print(json.load(open('$PAIRING'))['ws'])")

# Varied prompts, because a soak that sends one identical trivial prompt measures one code path and
# reports a duration distribution with no width to it.
PROMPTS=(
  "Reply with exactly: OK — nothing else."
  "In one short sentence, what is a git rebase?"
  "List three common HTTP status codes, one per line, nothing else."
  "Reply with exactly the word: PONG"
  "Name one reason a WebSocket connection might drop. One sentence."
)

WORKDIR=$(mktemp -d "${TMPDIR:-/tmp}/ironrain-soak.XXXXXX")
trap 'rm -rf "$WORKDIR"' EXIT

pass=0; fail=0
log "soak: $ITERATIONS iteration(s) against $WS"
for i in $(seq 1 "$ITERATIONS"); do
  prompt="${PROMPTS[$(( (i - 1) % ${#PROMPTS[@]} ))]}"
  start=$(date +%s)
  if (cd "$REPO/daemon" && go run ./cmd/turn-smoke \
        -ws "$WS" -pub "$PUB" -secret "$SECRET" \
        -provider opencode -cwd "$WORKDIR" -prompt "$prompt" -timeout 240s) >>"$LOG" 2>&1; then
    pass=$((pass + 1)); log "  [$i/$ITERATIONS] pass in $(( $(date +%s) - start ))s — ${prompt:0:40}"
  else
    fail=$((fail + 1)); log "  [$i/$ITERATIONS] FAIL after $(( $(date +%s) - start ))s — ${prompt:0:40}"
  fi
done

log "soak: $pass passed, $fail failed"
# A failing turn is a real signal and should be visible in the exit code, but the run still counts as
# having produced telemetry — which is the other half of why this exists.
[ "$fail" -eq 0 ]
