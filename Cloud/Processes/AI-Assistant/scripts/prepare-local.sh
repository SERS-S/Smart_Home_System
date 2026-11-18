#!/bin/sh
set -eu
module_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$module_dir"
mkdir -p .cache/npm .cache/tmp .cache/node .cache/node-gyp .cache/xdg .runtime/home .runtime/state
export NPM_CONFIG_CACHE="$module_dir/.cache/npm"
export NPM_CONFIG_UPDATE_NOTIFIER=false NPM_CONFIG_AUDIT=false NPM_CONFIG_FUND=false
export TMPDIR="$module_dir/.cache/tmp"
export NODE_COMPILE_CACHE="$module_dir/.cache/node"
export npm_config_devdir="$module_dir/.cache/node-gyp"
export XDG_CACHE_HOME="$module_dir/.cache/xdg"
export OPENCLAW_HOME="$module_dir/.runtime/home"
export OPENCLAW_STATE_DIR="$module_dir/.runtime/state"
export OPENCLAW_CONFIG_PATH="$module_dir/openclaw/generated/openclaw.json"
export OPENCLAW_WORKSPACE_DIR="$module_dir/openclaw/workspace"
npm ci --prefix runtime/node --no-audit --no-fund
export PATH="$module_dir/runtime/node/node_modules/node/bin:$PATH"
# Dependencies are pinned by package-lock.json. No global prefix or daemon.
# Skip package lifecycle cleanup of shared locations; this is a fresh private prefix.
npm ci --prefix runtime --ignore-scripts --no-audit --no-fund
node runtime/node_modules/openclaw/openclaw.mjs --version
