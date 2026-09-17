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

# Under launchd, stdout is ALREADY redirected to $LOG, so teeing there too wrote every line twice.
# Tee only when stdout is a terminal (an interactive run, where you want both).
log() {
  line="$(date '+%Y-%m-%d %H:%M:%S') $*"
  if [ -t 1 ]; then printf '%s\n' "$line" | tee -a "$LOG"; else printf '%s\n' "$line"; fi
}

install_schedule() {
  mkdir -p "$(dirname "$PLIST")" "$(dirname "$LOG")"
  cat > "$PLIST" <<PLIST_EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>$LABEL</string>
  <!-- Through a LOGIN SHELL, not the script directly.
       launchd runs jobs with a minimal PATH that does not include the Go toolchain, so invoking the
       script directly made every scheduled run die on "go: command not found" — silently, while
       the job still showed as installed. Verified: the first night's run failed 3/3 that way.
       A login shell sources the same profile an interactive run would.
       (No backticks in this comment: the heredoc below is UNQUOTED so $LABEL and $REPO expand, which
       means a backtick here would run a command at install time and paste its output into the
       plist. That happened.) -->
  <key>ProgramArguments</key>
  <array>
    <string>${SHELL:-/bin/zsh}</string><string>-lc</string>
    <!-- The path is passed as \$0 rather than interpolated into the shell string, so an apostrophe
         anywhere in it cannot break the quoting. -->
    <string>exec "\$0" 3</string>
    <string>$REPO/scripts/telemetry-soak.sh</string>
  </array>
  <key>StartCalendarInterval</key><dict><key>Hour</key><integer>3</integer><key>Minute</key><integer>17</integer></dict>
  <!-- The Mac is often asleep at 03:17; without this the run is skipped entirely rather than
       deferred, and the dashboard shows a gap that looks like a broken pipeline. -->
  <key>RunAtLoad</key><false/>
  <key>StandardOutPath</key><string>$LOG</string>
  <key>StandardErrorPath</key><string>$LOG</string>
</dict></plist>
PLIST_EOF
  launchctl unload "$PLIST" 2>/dev/null
  launchctl load "$PLIST" 2>/dev/null
  # `launchctl load` exits 0 in every failure mode — malformed plist, missing path, already loaded,
  # previously disabled. Verified on this machine. So confirm the job is actually registered rather
  # than trusting the status, or --install-schedule reports success while nothing is scheduled.
  if launchctl list 2>/dev/null | grep -q "$LABEL"; then
    log "scheduled: $LABEL nightly at 03:17 (log: $LOG)"
  else
    log "FAILED to schedule $LABEL — launchctl reported success but the job is not registered."
    log "  Try: launchctl bootstrap gui/\$(id -u) \"$PLIST\""
    return 1
  fi
}

[ "${1:-}" = "--install-schedule" ] && { install_schedule; exit $?; }

case "${1:-}" in
  -h|--help)
    sed -n '2,20p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
    exit 0
    ;;
esac

ITERATIONS="${1:-3}"
mkdir -p "$(dirname "$LOG")"

# A soak that drives nothing must not report health. `telemetry-soak.sh nonsense` previously ran
# zero turns and exited 0 with "0 passed, 0 failed", which is indistinguishable from success.
case "$ITERATIONS" in
  ''|*[!0-9]*|0) log "FAIL iteration count must be a positive integer, got: ${ITERATIONS:-(empty)}"; exit 2 ;;
esac

if [ ! -f "$PAIRING" ]; then
  log "FAIL no $PAIRING — the daemon writes it at startup; is oculusd running?"
  exit 1
fi

# Read the credentials out of the daemon's own pairing file.
#
# NOTE, because the comment here used to claim the opposite: turn-smoke takes -secret as a command
# line FLAG, so the pairing secret IS visible in the process list to any local process for the life
# of the turn. That is the same secret already sitting in ~/.oculus/pairing.json mode 0600, so on a
# single-user Mac it changes little — but "never appears in the process list" was simply untrue and
# is the kind of claim that stops the next person checking.
# Read all three in one parse and fail loudly if any is missing. The file is rewritten on every
# daemon start, so a run that lands mid-write previously got empty strings and failed later with an
# opaque handshake error instead of saying what was wrong.
CREDS=$(python3 - "$PAIRING" <<'PYEOF' 2>/dev/null
import json, sys
try:
    d = json.load(open(sys.argv[1]))
    pub, secret, ws = d["pub"], d["secret"], d["ws"]
except Exception:
    sys.exit(1)
if not (pub and secret and ws):
    sys.exit(1)
print(pub); print(secret); print(ws)
PYEOF
) || { log "FAIL $PAIRING is unreadable or incomplete (daemon restarting?) — nothing to drive"; exit 1; }
PUB=$(printf '%s' "$CREDS" | sed -n 1p)
SECRET=$(printf '%s' "$CREDS" | sed -n 2p)
WS=$(printf '%s' "$CREDS" | sed -n 3p)

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
for ((i = 1; i <= ITERATIONS; i++)); do
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
