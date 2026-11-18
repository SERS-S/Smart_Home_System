#!/bin/sh
set -eu
module_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$module_dir"
mkdir -p .cache/go-build .cache/go-mod .cache/tmp bin
export GOCACHE="$module_dir/.cache/go-build"
export GOMODCACHE="$module_dir/.cache/go-mod"
export GOTMPDIR="$module_dir/.cache/tmp"
export GOTELEMETRY=off
# Only a local foreground build. This does not deploy or alter build.sh/systemd.
go run ./cmd/build "$@"
