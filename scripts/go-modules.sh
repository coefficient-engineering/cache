#!/usr/bin/env bash
# Prints Go module directories, one per line, relative to the repo root.
#   no args      every module in go.work
#   file args    the modules that own those files (deduplicated)
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

if [ "$#" -eq 0 ]; then
	go list -m -f '{{.Dir}}' | while IFS= read -r dir; do
		rel="${dir#"$PWD"}"
		echo ".${rel}"
	done
	exit 0
fi

for file in "$@"; do
	dir="$(dirname -- "$file")"
	while [ "$dir" != "." ] && [ ! -f "$dir/go.mod" ]; do
		dir="$(dirname -- "$dir")"
	done
	if [ "$dir" = "." ]; then echo "."; else echo "./$dir"; fi
done | sort -u
