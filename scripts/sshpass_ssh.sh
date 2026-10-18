#!/bin/sh
set -eu

: "${PASSWORD:?PASSWORD is required for deployment}"
exec sshpass -p "$PASSWORD" ssh "$@"
