#!/bin/sh
# live-e2e: two real claude sessions set up, join and ping-pong through one huddle channel, and
# the channel's own events prove it happened — then an opencode agent, configured by
# `huddle harness install opencode` in the same temp home, joins the same channel and
# ping-pongs with the Claude session too. Not part of CI: it needs `claude`, `opencode` and
# huddle-router on 127.0.0.1 (the opencode leg skips with a clear line when opencode is away).
# sh scripts/live-e2e.sh (KEEP=1 keeps the temp dir and its logs).
#
# Everything runs in one mktemp home: the built binary stands in for the release download
# (AGENTS_HUDDLE_HOME), the plugin loads from a copy, and every claude run gets its own
# HOME/CLAUDE_CONFIG_DIR/XDG_* so the real ~/.claude, ~/.config/huddle and ~/.agents are never
# touched. A run once rewrote the real ~/.claude; the isolation is the point.

set -eu

ROOT=$(cd "$(dirname "$0")/.." && pwd)
ROUTER=${HUDDLE_LIVE_E2E_ROUTER:-http://127.0.0.1:18786}
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

step_skip() {
	printf 'step %s: %s ... skip: %s\n' "$1" "$2" "$3"
	RESULTS="$RESULTS skip:$1-$2"
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

# oc runs one headless opencode turn in that project, capped at CAP seconds, its stream at
# $T/log/oc-<tag>.jsonl. The huddle MCP server opencode spawns inherits HUDDLE_SESSION=oc,
# which names the credential file the oc join wrote.
oc() {
	proj=$1
	prompt=$2
	tag=$3
	log="$T/log/oc-$tag.jsonl"
	(
		cd "$T/$proj" || exit 1
		env_iso env HUDDLE_SESSION=oc perl -e 'alarm shift; exec @ARGV' "$CAP" \
			opencode run "$prompt" --format json >"$log" 2>"$log.err"
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
	health=$(curl -s --max-time 5 "$ROUTER/huddle-router/health" || true)
	case $health in
	*'"ok":true'*) ;;
	*) why="the router at $ROUTER does not answer huddle-router/health with ok:true" ;;
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

# ── step 9: opencode leg — provider config, then the harness install ─────────
if ! command -v opencode >/dev/null 2>&1; then
	step_skip 9 oc-leg 'opencode is not on PATH'
	finish
fi
OCJSON="$T/xdg/config/opencode/opencode.json"
mkdir -p "$T/xdg/config/opencode" "$T/proj-oc"
cat >"$OCJSON" <<EOF
{
  "\$schema": "https://opencode.ai/config.json",
  "provider": {
    "zai": {
      "npm": "@ai-sdk/anthropic",
      "name": "zai router",
      "options": { "baseURL": "$ROUTER/v1", "apiKey": "dummy" },
      "models": { "$MODEL": {} }
    }
  },
  "model": "zai/$MODEL"
}
EOF
if ! (
	cd "$T/proj-oc" || exit 1
	git init -q 2>>"$T/log/git.err" || exit 1
	git -c user.name=t -c user.email=t@example.test -c commit.gpgsign=false \
		commit --allow-empty -qm init 2>>"$T/log/git.err" || exit 1
); then
	step_fail 9 oc-install "git init or commit failed in proj-oc (see $T/log/git.err)"
	finish
fi
cp "$OCJSON" "$T/log/oc-before.json"
if hcli harness install opencode >"$T/log/oc-install.log" 2>"$T/log/oc-install.err" \
	&& grep -q '"mcp"' "$OCJSON" \
	&& grep -q 'huddle","mcp"' "$OCJSON" \
	&& grep -q '"timeout":120000' "$OCJSON" \
	&& grep -q 'opencode-hook.js' "$OCJSON" \
	&& [ -f "$T/xdg/config/huddle/harness/opencode-hook.js" ]; then
	step_pass 9 oc-install
else
	step_fail 9 oc-install "the install did not wire the huddle MCP server into $OCJSON (stderr $T/log/oc-install.err)"
	finish
fi

# ── step 10: the opencode agent answers through the router ───────────────────
oc proj-oc 'Reply with exactly: ok' model
if [ -s "$T/log/oc-model.jsonl" ] \
	&& grep -q '"type":"text"' "$T/log/oc-model.jsonl" \
	&& grep -q '"reason":"stop"' "$T/log/oc-model.jsonl"; then
	step_pass 10 oc-model
else
	step_fail 10 oc-model "opencode produced no answer (log $T/log/oc-model.jsonl, stderr $T/log/oc-model.jsonl.err)"
	finish
fi

# ── step 11: the opencode agent joins proj-a's channel as oc ─────────────────
OCJOIN=$(hcli token create --print-join-command 2>"$T/log/oc-token.err" || true)
OCLINE=$(printf '%s' "$OCJOIN" | grep -o '/huddle:join 127\.0\.0\.1:[0-9]* --token [^ "\\]*' | tail -1 | sed 's/[^A-Za-z0-9._:/-]*$//') || true
OCHOSTPORT=$(printf '%s' "$OCLINE" | sed -n 's#^/huddle:join \([0-9.]*:[0-9]*\) .*#\1#p')
OCTOKEN=$(printf '%s' "$OCLINE" | sed -n 's#.* --token \([A-Za-z0-9.]*\)$#\1#p')
if [ -z "$OCHOSTPORT" ] || [ -z "$OCTOKEN" ]; then
	step_fail 11 oc-join "no join line in token create output (stderr $T/log/oc-token.err)"
	finish
fi
if (
	cd "$T/proj-oc" || exit 1
	env_iso env HUDDLE_SESSION=oc "$T/hb/0.0.1/huddle" join "$OCHOSTPORT" --token "$OCTOKEN" --as oc \
		>"$T/log/oc-join.log" 2>&1
) && [ -f "$T/xdg/config/huddle/sessions/oc.json" ]; then
	step_pass 11 oc-join
else
	step_fail 11 oc-join "the cli join failed (log $T/log/oc-join.log)"
	finish
fi

# ── step 12: ping-pong, the opencode agent and the Claude session live at once
oc proj-oc "Use the huddle send tool to send exactly 'ping' to the session proj-a. Then use the huddle wait tool with timeout 90 seconds. Until a message from proj-a whose text is exactly 'pong' arrives, ignore every other event and call the huddle wait tool again. When the pong arrives, print exactly: pong received" run &
pid_oc=$!
sleep 5
(
	cc proj-a "Use the huddle wait tool to wait (timeout 180 seconds) for a message from the session oc. Until a message whose text is exactly 'ping' arrives from oc, ignore join notices and every other event and call the huddle wait tool again. When it arrives, use the huddle reply or send tool to answer oc with exactly 'pong'. Then stop." \
		--resume "$SID_A" >"$T/log/sid-a3.txt" 2>&1
) &
pid_a2=$!
st_oc=0
st_a2=0
wait "$pid_oc" || st_oc=$?
wait "$pid_a2" || st_a2=$?
if [ "$st_oc" -lt 128 ] && [ "$st_a2" -lt 128 ]; then
	step_pass 12 oc-pingpong
else
	step_fail 12 oc-pingpong "a session hit the ${CAP}s cap (oc=$st_oc claude=$st_a2)"
fi

# ── step 13: the channel itself holds oc's ping and proj-a's pong ────────────
EVFILE2="$T/log/events-oc.json"
hcli events --after 0 --limit 500 >"$EVFILE2" 2>"$T/log/events-oc.err" || true
ocpings=$(json_array_count "$EVFILE2" oc proj-a ping)
ocpongs=$(json_array_count "$EVFILE2" proj-a oc pong)
if [ "$ocpings" = 1 ] && [ "$ocpongs" = 1 ]; then
	step_pass 13 oc-events
	echo "message: oc -> proj-a: ping"
	echo "message: proj-a -> oc: pong"
else
	step_fail 13 oc-events "ping=$ocpings pong=$ocpongs (events $EVFILE2, stderr $T/log/events-oc.err)"
fi

# ── step 14: harness remove restores the temp opencode.json ──────────────────
LEDGER="$T/xdg/config/huddle/harness-ledger.json"
if hcli harness remove opencode >"$T/log/oc-remove.log" 2>"$T/log/oc-remove.err" \
	&& cmp -s "$OCJSON" "$T/log/oc-before.json" \
	&& [ ! -e "$T/xdg/config/huddle/harness/opencode-hook.js" ] \
	&& ! grep -q '"harness":"opencode"' "$LEDGER" 2>/dev/null; then
	step_pass 14 oc-remove
else
	step_fail 14 oc-remove "$OCJSON did not come back byte for byte, or the shim or its ledger entry stayed (stderr $T/log/oc-remove.err)"
fi

finish
