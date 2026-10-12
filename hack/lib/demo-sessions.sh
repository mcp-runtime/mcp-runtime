#!/usr/bin/env bash
# Emit only sessions belonging to this demo's server and managed agents.
mcpruntime_demo_session_names() {
  local server="$1" globex="$2" techcorp="$3"
  jq -r --arg server "$server" --arg globex "$globex" --arg techcorp "$techcorp" '
    (.sessions // .) | .[] | select(.serverRef.name == $server) |
    select((.subject.agentID == $globex and $globex != "") or (.subject.agentID == $techcorp and $techcorp != "")) | .name'
}
