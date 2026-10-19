#!/bin/sh
# live-e2e: two real claude sessions set up, join and ping-pong through one huddle channel, and
# the channel's own events prove it happened. Not part of CI: it needs `claude` and the zai
# router on 127.0.0.1. sh scripts/live-e2e.sh (KEEP=1 keeps the temp dir and its logs).
#
# Everything runs in one mktemp home: the built binary stands in for the release download
# (AGENTS_HUDDLE_HOME), the plugin loads from a copy, and every claude run gets its own
# HOME/CLAUDE_CONFIG_DIR/XDG_* so the real ~/.claude, ~/.config/huddle and ~/.agents are never
# touched. A run once rewrote the real ~/.claude; the isolation is the point.

set -eu

ROOT=$(cd "$(dirname "$0")/.." && pwd)
ROUTER=${HUDDLE_LIVE_E2E_ROUTER:-http://127.0.0.1:18787}
MODEL=glm-5.3-flash
CAP=240
KEEP=${KEEP:-0}
PLUGIN_TOOLS=mcp__plugin_huddle_huddle_

T=
HAVE_JQ=0
command -v jq >/dev/null 2>&1 && HAVE_JQ=1

RESULTS=
FAILED=0

step_pass() {
	printf 'step %s: %s ... ok\n' "$1" "$2"
	RESULTS="$RESULTS ok:$1-$2"
}

step_fail() {
	printf 'step %s: %s ... FAILED: %s\n' "$1" "$2" "$3"
	RESULTS="$RESULTS FAILED:$1-$2"
	FAILED=$((FAILED + 1))
}

finish() {
	echo 'summary:'
	lines=$(printf '%s' "$RESULTS" | tr ' ' '\n' | grep -v '^$' || true)
	for r in $lines; do
		echo "  $r"
	done
	if [ "$FAILED" -eq 0 ]; then
		echo 'live e2e: ok'
		exit 0
	fi
	echo "live e2e: $FAILED step(s) failed"
	exit 1
}

# env_iso runs a command with the temp homes and without every variable that could point a
# child claude at this session, the real home or a proxy.
env_iso() {
	env -u HTTPS_PROXY -u HTTP_PROXY -u ALL_PROXY -u https_proxy -u http_proxy -u all_proxy \
		-u NODE_EXTRA_CA_CERTS -u HUDDLE_URL -u HUDDLE_CHANNEL -u HUDDLE_HOME -u HUDDLE_SESSION \
		-u HUDDLE_SESSIONS_DIR -u HUDDLE_TOKEN -u HUDDLE_PORT -u HUDDLE_AUTOSTART \
		-u HUDDLE_CONTEXT -u HUDDLE_LISTEN -u HUDDLE_DOWNLOAD_BASE -u RADAR_HOME \
		-u CLAUDECODE -u CLAUDE_CODE_ENTRYPOINT -u CLAUDE_CODE_SESSION_ID -u CLAUDE_PID \
		-u CLAUDE_CODE_MESSAGING_SOCKET -u CLAUDE_CODE_MESSAGING_TOKEN \
		-u CLAUDE_CODE_CHILD_SESSION -u CLAUDE_CODE_SESSION_ATTENDED \
		HOME="$T/home" CLAUDE_CONFIG_DIR="$T/cfg" \
		XDG_CONFIG_HOME="$T/xdg/config" XDG_STATE_HOME="$T/xdg/state" XDG_DATA_HOME="$T/xdg/data" \
		AGENTS_HUDDLE_HOME="$T/hb" \
		ANTHROPIC_BASE_URL="$ROUTER" ANTHROPIC_API_KEY=dummy \
		MCP_TIMEOUT=30000 MCP_TOOL_TIMEOUT=200000 \
		"$@"
}

# cc <proj> <prompt> [extra claude args...]: one headless claude turn in that project, capped at
# CAP seconds, its stream at $T/log/<n>-<proj>.jsonl. Prints the session id from the init line.
# The caller numbers the call in CC_N first: cc runs in a command substitution, so a counter
# bumped inside it would never reach the script.
CC_N=0
cc() {
	proj=$1
	prompt=$2
	shift 2
	log="$T/log/$CC_N-$proj.jsonl"
	(
		cd "$T/$proj" || exit 1
		env_iso perl -e 'alarm shift; exec @ARGV' "$CAP" \
			claude -p --model "$MODEL" --plugin-dir "$T/plugin" --dangerously-skip-permissions \
			--output-format stream-json --verbose "$prompt" "$@" >"$log" 2>"$log.err"
	)
	status=$?
	if [ "$status" -ge 128 ]; then
		echo "cc $proj: ended on signal ($status), see $log.err" >&2
	fi
	sid=''
	if [ "$HAVE_JQ" = 1 ]; then
		sid=$(jq -r 'select(.type=="system" and .subtype=="init") | .session_id' "$log" 2>/dev/null | head -1 || true)
	else
		sid=$(python3 -c '
import json, sys
try:
    lines = open(sys.argv[1]).readlines()
except OSError:
    lines = []
for line in lines:
    line = line.strip()
    if not line:
        continue
    try:
        item = json.loads(line)
    except ValueError:
        continue
    if item.get("type") == "system" and item.get("subtype") == "init":
        print(item.get("session_id", ""))
        break
' "$log" 2>/dev/null || true)
	fi
	printf '%s' "$sid"
}

# hcli runs the temp huddle binary the way proj-a's session would: from proj-a, temp homes.
hcli() {
	(
		cd "$T/proj-a" || exit 1
		env_iso "$T/hb/0.0.1/huddle" "$@"
	)
}

cleanup() {
	status=$?
	if [ -n "$T" ] && [ -x "$T/hb/0.0.1/huddle" ] && [ -d "$T/proj-a" ]; then
		hcli down >/dev/null 2>&1 || true
	fi
	if [ -n "$T" ]; then
		if [ "$KEEP" = 1 ]; then
			echo "keep: $T"
		else
			rm -rf "$T"
		fi
	fi
	exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# json_array_count <file> <from> <to> <msg>: how many events carry that one message.
json_array_count() {
	if [ "$HAVE_JQ" = 1 ]; then
		jq --arg f "$2" --arg t "$3" --arg m "$4" \
			'[.[] | select(.from==$f and .to==$t and .msg==$m)] | length' "$1" 2>/dev/null || echo 0
	else
		python3 -c '
import json, sys
try:
    events = json.load(open(sys.argv[1]))
except (OSError, ValueError):
    print(0)
    raise SystemExit
wanted = sys.argv[2:]
found = [e for e in events if [e.get("from"), e.get("to"), e.get("msg")] == wanted]
print(len(found))
' "$1" "$2" "$3" "$4"
	fi
}

json_member() { # <file> <key>: one top-level member, raw
	if [ "$HAVE_JQ" = 1 ]; then
		jq -r --arg k "$2" '.[$k] // ""' "$1" 2>/dev/null || echo ''
	else
		python3 -c '
import json, sys
try:
    doc = json.load(open(sys.argv[1]))
except (OSError, ValueError):
    raise SystemExit
print(doc.get(sys.argv[2], ""))
' "$1" "$2"
	fi
}

echo "huddle live e2e: two real claude sessions on $MODEL through $ROUTER"

# ── step 1: preflight ────────────────────────────────────────────────────────
why=''
claude --version >/dev/null 2>&1 || why='claude is not on PATH'
if [ -z "$why" ]; then
	health=$(curl -s --max-time 5 "$ROUTER/zai-router/health" || true)
	case $health in
	*'"ok":true'*) ;;
	*) why="the router at $ROUTER does not answer zai-router/health with ok:true" ;;
	esac
fi
if [ -z "$why" ]; then
	go version >/dev/null 2>&1 || why='go is not on PATH'
fi
if [ -n "$why" ]; then
	printf 'step 1: preflight ... FAILED: %s\n' "$why"
	exit 2
fi
step_pass 1 preflight

# ── step 2: temp home, binary, plugin ────────────────────────────────────────
T=$(mktemp -d)
mkdir -p "$T/home" "$T/cfg" "$T/xdg/config" "$T/xdg/state" "$T/xdg/data" "$T/log" "$T/hb/0.0.1"
if (cd "$ROOT" && go build -o "$T/hb/0.0.1/huddle" ./cmd/huddle) 2>"$T/log/build.err"; then
	:
else
	step_fail 2 build "go build failed: $(tail -3 "$T/log/build.err")"
	finish
fi
cp -R "$ROOT/plugin" "$T/plugin"
if [ ! -x "$T/hb/0.0.1/huddle" ] || [ ! -f "$T/plugin/dist/run" ]; then
	step_fail 2 build 'the binary or the plugin copy is missing'
	finish
fi
step_pass 2 build

# ── step 3: two projects ─────────────────────────────────────────────────────
ok3=1
for p in proj-a proj-b; do
	mkdir -p "$T/$p"
	git -C "$T/$p" init -q 2>>"$T/log/git.err" || ok3=0
	git -C "$T/$p" -c user.name=t -c user.email=t@example.test -c commit.gpgsign=false \
		commit --allow-empty -qm init 2>>"$T/log/git.err" || ok3=0
done
if [ "$ok3" = 1 ]; then
	step_pass 3 projects
else
	step_fail 3 projects "git init or commit failed (see $T/log/git.err)"
	finish
fi

# ── step 4: session A sets up and prints the join line ───────────────────────
CC_N=$((CC_N + 1))
SID_A=$(cc proj-a "/huddle:setup")
LOG_A="$T/log/$CC_N-proj-a.jsonl"
JOINLINE=$(grep -o '/huddle:join 127\.0\.0\.1:[0-9]* --token [^ "\\]*' "$LOG_A" 2>/dev/null | tail -1 | sed 's/[^A-Za-z0-9._:/-]*$//') || true
if [ -n "$JOINLINE" ] && [ -n "$SID_A" ]; then
	step_pass 4 setup-a
else
	step_fail 4 setup-a "no join line or session id in $LOG_A"
	finish
fi

# ── step 5: session B joins with that line ───────────────────────────────────
CC_N=$((CC_N + 1))
SID_B=$(cc proj-b "$JOINLINE")
LOG_B="$T/log/$CC_N-proj-b.jsonl"
if [ -n "$SID_B" ]; then
	step_pass 5 join-b
else
	step_fail 5 join-b "session B produced no session id (log $LOG_B, stderr $LOG_B.err)"
	finish
fi

# ── step 6: a joined session holds the huddle MCP tools ──────────────────────
CC_N=$((CC_N + 1))
SID_B2=$(cc proj-b "Call the huddle status tool and print its first line." --resume "$SID_B")
LOG_B2="$T/log/$CC_N-proj-b.jsonl"
if grep -q "\"name\":\"${PLUGIN_TOOLS}" "$LOG_B2" 2>/dev/null; then
	step_pass 6 tools-b
else
	step_fail 6 tools-b "no ${PLUGIN_TOOLS}* tool_use in $LOG_B2"
	finish
fi

# ── step 7: ping-pong, both sessions live at once ────────────────────────────
CC_N=$((CC_N + 1))
(
	cc proj-b "Use the huddle wait tool to wait (timeout 180 seconds) for a message. When the message 'ping' arrives, use the huddle reply or send tool to answer its sender with exactly 'pong'. Then stop." \
		--resume "$SID_B" >"$T/log/sid-b3.txt" 2>&1
) &
pid_b=$!
sleep 5
CC_N=$((CC_N + 1))
(
	cc proj-a "Use the huddle send tool to send exactly 'ping' to the session proj-b. Then use the huddle wait tool (timeout 180 seconds) until proj-b answers, and print the answer." \
		--resume "$SID_A" >"$T/log/sid-a2.txt" 2>&1
) &
pid_a=$!
st_b=0
st_a=0
wait "$pid_b" || st_b=$?
wait "$pid_a" || st_a=$?
if [ "$st_b" -lt 128 ] && [ "$st_a" -lt 128 ]; then
	step_pass 7 pingpong
else
	step_fail 7 pingpong "a session hit the ${CAP}s cap (b=$st_b a=$st_a)"
fi

# ── step 8: the channel itself holds ping and pong ───────────────────────────
EVFILE="$T/log/events.json"
DIGESTFILE="$T/log/digest.txt"
hcli events --after 0 --limit 500 >"$EVFILE" 2>"$T/log/events.err" || true
hcli digest --since 1h >"$DIGESTFILE" 2>/dev/null || true
pings=$(json_array_count "$EVFILE" proj-a proj-b ping)
pongs=$(json_array_count "$EVFILE" proj-b proj-a pong)
cfg="$T/proj-a/.agents/huddle/huddle.json"
CHANNEL=$(json_member "$cfg" channel)
if [ "$pings" = 1 ] && [ "$pongs" = 1 ] && [ -n "$CHANNEL" ]; then
	step_pass 8 channel
	echo "channel: $CHANNEL"
	echo "message: proj-a -> proj-b: ping"
	echo "message: proj-b -> proj-a: pong"
else
	step_fail 8 channel "ping=$pings pong=$pongs channel='$CHANNEL' (events $EVFILE, stderr $T/log/events.err)"
fi

finish
