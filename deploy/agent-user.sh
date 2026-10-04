#!/usr/bin/env bash
# Move the coop agents of one machine from root to a user with no privileges. Run it as root on
# an agent machine. It is safe to run again: each step looks first and changes only what is
# not there.
#
#   deploy/agent-user.sh [-n] [<coop binary>]
#
#   -n             show each step and change nothing
#   <coop binary>  the coop binary to install for all users (default: the one root has)
#   COOP_AGENT_USER=<name> names the user (default: agent)
#
# The steps:
#   1. Make the user, with a home directory and no password.
#   2. Let the SSH keys that can log in as root log in as the user.
#   3. Install coop in /usr/local/bin, for all users.
#   4. Give the user a copy of root's Claude Code.
#   5. Put ~/.local/bin on the user's PATH in every interactive shell (~/.bashrc).
#   6. Move root's coop credentials to the user. Root is then no agent on this machine.
#   7. Run `coop setup` and `coop doctor` as the user.
#
# The script does not log the user in to Claude Code. Do that one time by hand; the last lines
# of the output say how.
set -euo pipefail

dry=0
if [ "${1:-}" = "-n" ]; then
  dry=1
  shift
fi
bin=${1:-}
user=${COOP_AGENT_USER:-agent}

if [ "$(id -u)" -ne 0 ]; then
  echo "run as root: sudo $0 [-n] [<coop binary>]" >&2
  exit 2
fi
for cmd in useradd getent install runuser; do
  if ! command -v "$cmd" >/dev/null; then
    echo "missing: $cmd" >&2
    exit 2
  fi
done
if [ -n "$bin" ] && [ ! -f "$bin" ]; then
  echo "no such file: $bin" >&2
  exit 2
fi

step() { printf '\n== %s\n' "$*"; }

# run does one change, or only shows it with -n.
run() {
  if [ "$dry" = 1 ]; then
    printf 'would run:'
    printf ' %q' "$@"
    printf '\n'
  else
    "$@"
  fi
}

root_home=$(getent passwd root | cut -d: -f6)

step "user $user"
if id "$user" >/dev/null 2>&1; then
  echo "the user $user exists"
else
  run useradd --create-home --shell /bin/bash "$user"
fi
# With -n the user is not there yet, and getent then fails: show the path that useradd makes.
home=$(getent passwd "$user" | cut -d: -f6 || true)
home=${home:-/home/$user}

# own makes a directory of the user, with its parents.
own() {
  run install -d -m "$1" -o "$user" -g "$user" "$2"
}

step "ssh keys"
if [ -e "$home/.ssh/authorized_keys" ]; then
  echo "$user has ssh keys"
elif [ -s "$root_home/.ssh/authorized_keys" ]; then
  own 700 "$home/.ssh"
  run install -m 600 -o "$user" -g "$user" "$root_home/.ssh/authorized_keys" "$home/.ssh/authorized_keys"
  echo "the keys that log in as root log in as $user too"
else
  echo "root has no authorized_keys: $user gets none (use: sudo -iu $user)"
fi

step "coop for all users"
target=/usr/local/bin/coop
source=$bin
if [ -z "$source" ]; then
  for candidate in "$target" "$root_home/.local/bin/coop"; do
    if [ -x "$candidate" ]; then
      source=$(readlink -f "$candidate")
      break
    fi
  done
fi
if [ -z "$source" ]; then
  echo "no coop binary found: give one, for example $0 ./coop-linux-amd64" >&2
  exit 2
fi
if [ "$source" = "$target" ] || { [ -f "$target" ] && cmp -s "$source" "$target"; }; then
  echo "$target is $("$target" version)"
else
  run install -m 755 "$source" "$target"
  echo "installed $("$source" version) as $target"
fi

step "claude code for $user"
if [ -x "$home/.local/bin/claude" ]; then
  echo "$user has claude"
elif [ -x "$root_home/.local/bin/claude" ]; then
  # The native install is one file per version, with a link in ~/.local/bin.
  claude=$(readlink -f "$root_home/.local/bin/claude")
  version=$(basename "$claude")
  own 755 "$home/.local"
  own 755 "$home/.local/bin"
  own 755 "$home/.local/share"
  own 755 "$home/.local/share/claude"
  own 755 "$home/.local/share/claude/versions"
  run install -m 755 -o "$user" -g "$user" "$claude" "$home/.local/share/claude/versions/$version"
  run ln -s "$home/.local/share/claude/versions/$version" "$home/.local/bin/claude"
  run chown -h "$user:$user" "$home/.local/bin/claude"
  echo "copied claude $version from root"
else
  echo "root has no claude in ~/.local/bin: install Claude Code as $user before the agents start"
fi

step "path of $user"
# Claude Code and Herdr install into ~/.local/bin. A login shell has it on the PATH (~/.profile),
# but a terminal pane of Herdr starts a shell that is not a login shell: there, claude and herdr
# are not found. ~/.bashrc is what such a shell reads.
marker='# coop: ~/.local/bin on the PATH, also in a shell that is not a login shell'
if [ -f "$home/.bashrc" ] && grep -qF "$marker" "$home/.bashrc"; then
  echo "$home/.bashrc has ~/.local/bin on the PATH"
elif [ "$dry" = 1 ]; then
  echo "would add ~/.local/bin to the PATH in $home/.bashrc"
else
  # The words in single quotes go into the file as they are: the user's shell expands them.
  # shellcheck disable=SC2016
  printf '\n%s\n%s\n' "$marker" 'case ":$PATH:" in *":$HOME/.local/bin:"*) ;; *) PATH="$HOME/.local/bin:$PATH" ;; esac' >>"$home/.bashrc"
  chown "$user:$user" "$home/.bashrc"
  echo "added ~/.local/bin to the PATH in $home/.bashrc"
fi

step "coop credentials"
if [ -e "$home/.config/coop/env" ]; then
  echo "$user has coop credentials"
elif [ -f "$root_home/.config/coop/env" ]; then
  own 755 "$home/.config"
  own 700 "$home/.config/coop"
  run mv "$root_home/.config/coop/env" "$home/.config/coop/env"
  run chown "$user:$user" "$home/.config/coop/env"
  run chmod 600 "$home/.config/coop/env"
  echo "moved the credentials of root to $user: root is no agent on this machine now"
else
  echo "root has no coop credentials: as $user, run coop login <url> <machine token>"
fi

step "coop setup as $user"
# A login shell, so that ~/.local/bin is on the PATH.
run runuser -l "$user" -c 'coop setup'
run runuser -l "$user" -c 'coop doctor' || true

step "left to do"
cat <<TEXT
1. Log $user in to Claude Code, one time:
     sudo -iu $user claude        # then /login
2. Put the projects of the agents below $home (root's directories are closed to $user).
3. Start an agent as $user, not as root:
     ssh $user@$(hostname)        # or: sudo -iu $user
     cd <project> && coop claude <session>
An agent that starts in the home directory has the name "agent"; in a project it has the
name of the project directory; coop --agent <name> claude names it.
TEXT
