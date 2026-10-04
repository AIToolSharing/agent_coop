#!/usr/bin/env bash
# Put this version of coop on the hub, on each agent machine and on this machine.
#
#   deploy/rollout.sh [-n] <hub host> [<agent host>...]
#
#   <hub host>    the SSH host or alias of the hub
#   <agent host>  the SSH host or alias of an agent machine; the hub host can be one too
#   -n            show each step and change nothing
#
# Example:  deploy/rollout.sh ghonovps vps contabo_vps ghonovps
#
# Run it in the repository, on the commit to roll out. The login on each host must be root,
# and each host must be Linux. The steps:
#   1. Build: the release binaries, and the binary of this machine (make release, make install).
#   2. Copy the binary and the scripts to ~/.cache/coop-rollout on each host, all at one time.
#   3. Hub: deploy/install.sh. It restarts the hub. The agents connect again by themselves.
#   4. Agent machines, all at one time (sshp): deploy/rollout-agent.sh. It installs the binary,
#      runs `coop setup` as the user agent, which writes the hooks of this version, and runs
#      `coop doctor`.
#   5. This machine: coop setup and coop doctor.
# Steps 3 and 4 check that the host has the version that step 1 built. The script stops at
# the first step that fails. It is safe to run again.
#
# Written for bash 3.2 (macOS) and later.
set -euo pipefail

usage() {
  sed -n '2,11p' "$0" | sed 's/^# \{0,1\}//' >&2
  exit 2
}

dry=0
while getopts 'nh' opt; do
  case $opt in
    n) dry=1 ;;
    *) usage ;;
  esac
done
shift $((OPTIND - 1))
[ $# -ge 1 ] || usage
hub=$1
shift
agents=("$@")

for cmd in make ssh scp sshp; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "rollout: missing: $cmd" >&2
    exit 2
  fi
done
cd "$(dirname "$0")/.."

# The directory on each host. It is in the home directory of root, so no other user of the
# host can change a file between the copy and the run.
dir='.cache/coop-rollout'

step() { printf '\n== %s\n' "$*"; }

# run does one command, or only shows it with -n.
run() {
  if [ "$dry" = 1 ]; then
    printf 'would run:'
    printf ' %q' "$@"
    printf '\n'
  else
    "$@"
  fi
}

# Each host one time: the hub can be an agent machine too.
hosts=("$hub")
for h in ${agents[@]+"${agents[@]}"}; do
  case " ${hosts[*]} " in
    *" $h "*) ;;
    *) hosts+=("$h") ;;
  esac
done

step "build"
run make release install
if [ "$dry" = 1 ]; then
  want='<the version that make builds>'
else
  want=$(dist/coop version)
fi
echo "version: $want"

# copy puts the binary for the host's processor and the scripts on one host.
copy() {
  local host=$1 platform bin
  platform=$(ssh -o BatchMode=yes "$host" "uname -sm && install -d -m 700 $dir") || return 1
  case $platform in
    'Linux x86_64') bin=dist/coop-linux-amd64 ;;
    'Linux aarch64' | 'Linux arm64') bin=dist/coop-linux-arm64 ;;
    *)
      echo "rollout: $host is $platform: only Linux on x86_64 or arm64 has a release binary" >&2
      return 1
      ;;
  esac
  scp -q -o BatchMode=yes "$bin" "$host:$dir/coop" &&
    scp -q -o BatchMode=yes deploy/agent-user.sh deploy/rollout-agent.sh deploy/install.sh deploy/coop.service "$host:$dir/"
}

step "copy to ${hosts[*]}"
if [ "$dry" = 1 ]; then
  echo "would copy the binary and the scripts to ~/$dir on each host"
else
  pids=()
  for h in "${hosts[@]}"; do
    copy "$h" &
    pids+=($!)
  done
  failed=0
  for i in "${!pids[@]}"; do
    if ! wait "${pids[$i]}"; then
      echo "rollout: the copy to ${hosts[$i]} failed" >&2
      failed=1
    fi
  done
  [ "$failed" = 0 ] || exit 1
  echo "copied"
fi

step "hub on $hub"
# install.sh installs the binary and starts the hub again. The step ends well only when the
# service is active and the binary is the new one.
run ssh -o BatchMode=yes "$hub" "cd $dir && ./install.sh ./coop >install.log 2>&1 || { cat install.log; exit 1; }; systemctl is-active --quiet coop && test \"\$(/usr/local/bin/coop version)\" = '$want' && echo 'the hub is active with $want'"

if [ ${#agents[@]} -gt 0 ]; then
  step "agent machines: ${agents[*]}"
  list=$(mktemp)
  trap 'rm -f "$list"' EXIT
  printf '%s\n' "${agents[@]}" | sort -u >"$list"
  run sshp -f "$list" -g -c off -e "bash $dir/rollout-agent.sh '$want'"
fi

step "this machine"
run dist/coop setup
run dist/coop doctor

step "done"
echo "$want is on the hub ($hub), on ${#agents[@]} agent machines and on this machine"
