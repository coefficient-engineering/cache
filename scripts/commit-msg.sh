#!/usr/bin/env bash
# Verifies a commit message file against Conventional Commits with cocogitto.
#
# cog (libgit2) panics when git config has no user.name, even though git itself
# falls back to the system account name. In that case, hand cog the identity git
# would use via a throwaway XDG config; ~/.gitconfig is still read.
set -euo pipefail
msg_file="$1"

if git config user.name >/dev/null; then
	exec cog verify --file "$msg_file"
fi

xdg="$(mktemp -d)"
trap 'rm -rf "$xdg"' EXIT
mkdir -p "$xdg/git"
name="$(git var GIT_AUTHOR_IDENT | sed -E 's/ <.*//')"
printf '[user]\n\tname = %s\n' "$name" >"$xdg/git/config"
XDG_CONFIG_HOME="$xdg" cog verify --file "$msg_file"
