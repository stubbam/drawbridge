#!/bin/sh
# Starts a fresh Drawbridge with the fake backend for the end-to-end tests:
# serve.sh BINARY STATE_DIR PORT
set -eu
bin=$1 dir=$2 port=$3
rm -rf "$dir"
mkdir -p "$dir"
(umask 077 && head -c 32 /dev/urandom >"$dir/secret.key")
exec "$bin" serve --backend fake --listen "127.0.0.1:$port" --db "$dir/drawbridge.db" \
	--secret-key "$dir/secret.key" --control "$dir/control.sock" --drift-interval 1h
