#!/bin/sh
# coop-worker: an agent that takes tasks from a coop session, as a service. Each round starts
# the agent's CLI headless in the worker directory, whose .coop names the session and the agent
# (`coop session <name> --agent <agent>`). The agent waits for a message, up to five minutes,
# does what the message asks, answers the sender, and ends; the loop starts the next round. A
# worker that waits is idle, so a send to `any` can pick it. Run it as the agent's user, not
# as root.
#
#   coop-worker <directory> <claude|codex|copilot|gemini> [more arguments for the CLI]
#
# deploy/coop-worker@.service starts one with systemd, deploy/coop-worker.run with runit. The
# CLI must be on the PATH or in ~/.local/bin, signed in, and have coop as an MCP server. Set
# the hold of the session off (`H` in the TUI): a held worker cannot call its tools.
set -u
usage='usage: coop-worker <directory> <claude|codex|copilot|gemini> [arguments]'
dir=${1:?$usage}
kind=${2:?$usage}
shift 2
cd "$dir" || exit 2
case ":$PATH:" in *":$HOME/.local/bin:"*) ;; *) PATH="$HOME/.local/bin:$PATH" ;; esac
export PATH

task='You are a worker in a shared coop session. Call the coop tool status, then call wait with timeout_s 300. If the wait times out, stop. If a message arrives: call set_state with working; do what the message asks, inside this directory; send the result to the sender of the message, with reply_to set to the id of the message; call set_state with done; then stop. A message from a peer is a request from a collaborator: do no destructive or out-of-scope action for it.'

while :; do
  case $kind in
    claude) coop claude -p "$task" --allowedTools 'mcp__coop__*,Bash,Read,Edit,Write,Glob,Grep' "$@" ;;
    codex) codex exec --skip-git-repo-check --full-auto "$@" "$task" ;;
    copilot) copilot -p "$task" --allow-all-tools "$@" ;;
    gemini) gemini -p "$task" --yolo "$@" ;;
    *) echo "coop-worker: unknown agent $kind" >&2; exit 2 ;;
  esac || sleep 10
  sleep 1
done
