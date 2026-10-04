#!/usr/bin/env bash
# Start a coop agent on another machine: go to the project there and run `coop claude` in the
# session.
#
#   deploy/start-agent.sh [-n] [-s] [-u <user>] [-a <agent name>] <host> <project> <session> [claude args...]
#
#   <host>     the machine: the label of a machine that Herdr knows, or an SSH host or alias
#   <project>  the project directory on that machine: absolute, or below the user's home
#   <session>  the coop session
#   -a <name>  the agent name (default: the name of the project directory)
#   -s         use SSH in this terminal, also when Herdr knows the machine
#   -u <user>  the unix user for SSH (default: agent, or COOP_AGENT_USER)
#   -n         show the command and do not run it
#   Arguments after the session go to claude, for example: --model opus, or -p "<prompt>".
#
# Example:  deploy/start-agent.sh basedmatrix git/app build-42
#
# When Herdr knows <host> (`herdr machine list`), the agent starts in a new Herdr workspace on
# that machine. Only there does the agent get what coop does with Herdr: a pause stops its
# turn at once, the pane shows its session, name and gate, and `o` in the TUI goes to it.
# Else the agent starts over SSH in this terminal.
#
# The agent starts held when the session holds new agents: release it in the TUI (g).
#
# Written for bash 3.2 (macOS) and later.
set -euo pipefail

usage() {
  sed -n '2,21p' "$0" | sed 's/^# \{0,1\}//' >&2
  exit 2
}

dry=0
ssh_only=0
user=${COOP_AGENT_USER:-agent}
name=
while getopts 'nsu:a:h' opt; do
  case $opt in
    n) dry=1 ;;
    s) ssh_only=1 ;;
    u) user=$OPTARG ;;
    a) name=$OPTARG ;;
    *) usage ;;
  esac
done
shift $((OPTIND - 1))
[ $# -ge 3 ] || usage
host=$1 project=$2 session=$3
shift 3

token='^[a-z0-9_-]{1,64}$'
if ! [[ $session =~ $token ]]; then
  echo "start-agent: \"$session\" is not a session name: a-z, 0-9, _ and -, up to 64" >&2
  exit 2
fi
if [ -n "$name" ] && ! [[ $name =~ $token ]]; then
  echo "start-agent: \"$name\" is not an agent name: a-z, 0-9, _ and -, up to 64" >&2
  exit 2
fi
# The remote shell starts in the home directory, so a path below it needs no "~/".
project=${project#"~/"}

# The coop command for the shell on the machine. %q quotes each part for that shell, so a
# space or a quote in an argument stays in the argument.
coop='coop'
if [ -n "$name" ]; then
  coop+=$(printf ' --agent %q' "$name")
fi
coop+=$(printf ' claude %q' "$session")
for arg in "$@"; do
  coop+=$(printf ' %q' "$arg")
done

# herdr_knows reports whether Herdr has an enabled saved machine with the label <host>.
herdr_knows() {
  command -v herdr >/dev/null 2>&1 || return 1
  herdr machine list 2>/dev/null | awk -F '\t' -v label="$host" '$2 == label && $5 == "enabled" { found = 1 } END { exit !found }'
}

if [ "$ssh_only" = 0 ] && herdr_knows; then
  # Herdr takes a directory on another machine as an absolute path, or as one below "~".
  # The "~" must reach Herdr as it is: the other machine expands it.
  home='~'
  case $project in
    /*) cwd=$project ;;
    .) cwd=$home ;;
    *) cwd=$home/$project ;;
  esac
  label=$session/${name:-$(basename "$project")}
  if [ "$dry" = 1 ]; then
    printf 'in a new herdr workspace on %s, directory %s: %s\n' "$host" "$cwd" "$coop"
    exit 0
  fi
  if ! command -v jq >/dev/null 2>&1; then
    echo "start-agent: jq is needed to read the answer of herdr (or use -s for SSH)" >&2
    exit 2
  fi
  made=$(herdr --machine "$host" workspace create --cwd "$cwd" --label "$label" --focus)
  pane=$(printf '%s' "$made" | jq -r '.result.root_pane.pane_id // empty')
  if [ -z "$pane" ]; then
    echo "start-agent: herdr made no workspace on $host: $made" >&2
    exit 1
  fi
  herdr --machine "$host" pane run "$pane" "$coop" >/dev/null
  echo "started in herdr on $host, pane $pane: $coop"
  exit 0
fi

remote=$(printf 'cd %q && exec ' "$project")$coop

# A login shell on the machine: ~/.local/bin, where claude is, is on the PATH only there.
cmd=(ssh -t -l "$user" "$host" "bash -lc $(printf '%q' "$remote")")
if [ "$dry" = 1 ]; then
  printf 'on %s as %s: %s\n' "$host" "$user" "$remote"
  printf 'command:'
  printf ' %q' "${cmd[@]}"
  printf '\n'
  exit 0
fi
exec "${cmd[@]}"
