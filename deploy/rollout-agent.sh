#!/usr/bin/env bash
# One agent machine in a rollout: install the binary, set the user agent up, check the result.
# deploy/rollout.sh copies this script to the host and runs it there as root.
#
#   rollout-agent.sh <version>      (the output of `coop version` that the host must have)
#
# The script is next to agent-user.sh and the binary `coop`.
set -euo pipefail

want=${1:?usage: rollout-agent.sh <version>}
user=${COOP_AGENT_USER:-agent}
cd "$(dirname "$0")"

# agent-user.sh says much. Show it only when it fails.
if ! bash agent-user.sh ./coop >agent-user.log 2>&1; then
  cat agent-user.log
  exit 1
fi
have=$(/usr/local/bin/coop version)
if [ "$have" != "$want" ]; then
  echo "the host has $have, not $want"
  exit 1
fi
# doctor ends with an error when a check fails. Its notes (--) are not errors.
if ! out=$(runuser -l "$user" -c 'coop doctor' 2>&1); then
  echo "$out"
  exit 1
fi
echo "$have"
echo "$out" | grep -v '^--' || true
