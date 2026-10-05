#!/bin/sh
# Install coop on this device, from its hub. Run it one time, as the user that runs the agents:
#
#   curl -fsSL <hub>/install.sh | sh -s -- <hub> <machine token>
#
#   <hub>            the address of the hub, for example http://hub.example:8090
#   <machine token>  from the host of the hub: coop admin token add <machine>
#
# The script puts coop into ~/.local/bin (COOP_BIN_DIR names another directory), stores the
# address and the token (coop login), connects coop to Claude Code (coop setup) and checks the
# result (coop doctor). It needs no root and it is safe to run again. A later version comes
# with `coop upgrade`. It runs on Linux, on macOS, and on Windows in Git Bash.
#
# The hub serves this file at /install.sh. All the work is in main, which runs on the last
# line: a download that stops in the middle runs nothing.
set -eu

main() {
  usage='usage: curl -fsSL <hub>/install.sh | sh -s -- <hub> <machine token>'
  hub=${1:?$usage}
  token=${2:?$usage}
  hub=${hub%/}

  if [ "$(id -u)" = 0 ]; then
    echo "Do not run agents as root. Run this line as the user that runs the agents." >&2
    echo "To make such a user: useradd --create-home --shell /bin/bash agent" >&2
    exit 2
  fi

  os=$(uname -s | tr '[:upper:]' '[:lower:]')
  exe=
  case $os in
    linux | darwin) ;;
    mingw* | msys* | cygwin*)
      # Git Bash on Windows: the shell in which Claude Code runs the hooks of coop there.
      os=windows
      exe=.exe
      ;;
    *)
      echo "this script is for Linux, macOS and Git Bash on Windows, not for $os: copy the binary by hand" >&2
      exit 1
      ;;
  esac
  case $(uname -m) in
    x86_64 | amd64) arch=amd64 ;;
    aarch64 | arm64) arch=arm64 ;;
    *)
      echo "coop has no binary for the processor $(uname -m)" >&2
      exit 1
      ;;
  esac
  file=coop-$os-$arch$exe
  coop=${COOP_BIN_DIR:-$HOME/.local/bin}/coop$exe

  dir=$(dirname "$coop")
  mkdir -p "$dir"
  new=$dir/.coop-new.$$$exe
  trap 'rm -f "$new"' EXIT
  echo "download $hub/dl/$file"
  if ! curl -fsSL -H "Authorization: Bearer $token" -o "$new" "$hub/dl/$file"; then
    echo "No download of $file from $hub. The line of curl above gives the cause:" >&2
    echo "401 is a wrong token, 404 is a hub that has no binary for this device." >&2
    exit 1
  fi
  chmod 755 "$new"
  # The file must run on this device before it takes the place of the old one.
  version=$("$new" version)
  if [ -n "$exe" ] && [ -e "$coop" ]; then
    # Windows does not replace a program that runs, but it lets the program get a new name.
    # The old file goes at a later install, when nothing runs it.
    rm -f "$coop".old* 2>/dev/null || true
    mv -f "$coop" "$coop.old.$$"
  fi
  mv -f "$new" "$coop"
  echo "installed $version as $coop"

  # The commands read nothing from the terminal: stdin is this script.
  "$coop" login "$hub" "$token" </dev/null
  if command -v claude >/dev/null 2>&1; then
    "$coop" setup </dev/null
  else
    echo "Claude Code is not on the PATH: install it, then run: coop setup"
  fi
  "$coop" doctor </dev/null || true
  case ":$PATH:" in
    *":$dir:"*) ;;
    *) echo "$dir is not on the PATH: add it, for example in ~/.profile" ;;
  esac
}

main "$@"
