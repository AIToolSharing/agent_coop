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
# with `coop upgrade`.
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
  case $os in
    linux | darwin) ;;
    *)
      echo "this script is for Linux and macOS, not for $os: copy the binary by hand" >&2
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
  file=coop-$os-$arch

  dir=${COOP_BIN_DIR:-$HOME/.local/bin}
  mkdir -p "$dir"
  new=$dir/.coop-new.$$
  trap 'rm -f "$new"' EXIT
  echo "download $hub/dl/$file"
  if ! curl -fsSL -H "Authorization: Bearer $token" -o "$new" "$hub/dl/$file"; then
    echo "The hub did not give $file: the token is wrong, or the hub has no binary for this device." >&2
    exit 1
  fi
  chmod 755 "$new"
  # The file must run on this device before it takes the place of the old one.
  version=$("$new" version)
  mv -f "$new" "$dir/coop"
  echo "installed $version as $dir/coop"

  # The commands read nothing from the terminal: stdin is this script.
  "$dir/coop" login "$hub" "$token" </dev/null
  if command -v claude >/dev/null 2>&1; then
    "$dir/coop" setup </dev/null
  else
    echo "Claude Code is not on the PATH: install it, then run: coop setup"
  fi
  "$dir/coop" doctor </dev/null || true
  case ":$PATH:" in
    *":$dir:"*) ;;
    *) echo "$dir is not on the PATH: add it, for example in ~/.profile" ;;
  esac
}

main "$@"
