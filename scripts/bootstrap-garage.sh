#!/bin/sh
# Idempotent first-boot bootstrap for Garage:
#   1. discover the node id and connect over RPC
#   2. assign the node a storage layout (once)
#   3. create the bucket
#   4. import/create the access key and grant it read/write
#
# Re-running this is safe; every step tolerates "already exists".
set -eu

CONF="/etc/garage.toml"
BUCKET="${PUSH_S3_BUCKET:-push}"
KEY_NAME="${PUSH_S3_KEY_NAME:-push-app}"
HOST="${GARAGE_HOST:-garage}"
PORT="${GARAGE_RPC_PORT:-3901}"

# Garage requires <full-node-id>@<host>:<port> for remote RPC. The node id is
# derived from the RPC secret, so we read it out of the running node itself.
echo "[bootstrap] resolving garage node id at $HOST:$PORT ..."
NODE_ID=""
i=0
while [ -z "$NODE_ID" ]; do
	i=$((i + 1))
	if [ "$i" -gt 60 ]; then
		echo "[bootstrap] could not resolve garage node id in time" >&2
		exit 1
	fi
	# node id is printed as <id>@<addr>; keep only the id part.
	NODE_ID=$(GARAGE_RPC_HOST="$HOST:$PORT" garage -c "$CONF" node id -q 2>/dev/null | cut -d@ -f1 || true)
	[ -z "$NODE_ID" ] && sleep 2
done

export GARAGE_RPC_HOST="${NODE_ID}@${HOST}:${PORT}"
GARAGE="garage -c $CONF"
echo "[bootstrap] node id: $NODE_ID"

echo "[bootstrap] waiting for garage to answer RPC ..."
i=0
until $GARAGE status >/dev/null 2>&1; do
	i=$((i + 1))
	if [ "$i" -gt 60 ]; then
		echo "[bootstrap] garage did not come up in time" >&2
		$GARAGE status || true
		exit 1
	fi
	sleep 2
done

# An unconfigured cluster reports "No nodes currently have a role".
if ! $GARAGE layout show 2>/dev/null | grep -q "$NODE_ID"; then
	echo "[bootstrap] assigning storage layout"
	$GARAGE layout assign -z dc1 -c "${GARAGE_CAPACITY:-100G}" "$NODE_ID"
	# Apply the staged change; the target version is current + 1.
	VER=$($GARAGE layout show 2>/dev/null | grep -oE 'layout version: [0-9]+' | grep -oE '[0-9]+' | head -n1)
	[ -z "$VER" ] && VER=0
	$GARAGE layout apply --version "$((VER + 1))"
	echo "[bootstrap] waiting for layout to settle ..."
	sleep 8
else
	echo "[bootstrap] layout already assigned"
fi

if ! $GARAGE bucket info "$BUCKET" >/dev/null 2>&1; then
	echo "[bootstrap] creating bucket $BUCKET"
	$GARAGE bucket create "$BUCKET"
fi

# Prefer the credentials pinned in .env so the app config stays declarative.
if [ -n "${PUSH_S3_ACCESS_KEY:-}" ] && [ -n "${PUSH_S3_SECRET_KEY:-}" ]; then
	if ! $GARAGE key info "$PUSH_S3_ACCESS_KEY" >/dev/null 2>&1; then
		echo "[bootstrap] importing pinned credentials from .env"
		$GARAGE key import -n "$KEY_NAME" "$PUSH_S3_ACCESS_KEY" "$PUSH_S3_SECRET_KEY" --yes
	else
		echo "[bootstrap] pinned key already present"
	fi
	$GARAGE bucket allow --read --write --owner "$BUCKET" --key "$PUSH_S3_ACCESS_KEY"
else
	if ! $GARAGE key info "$KEY_NAME" >/dev/null 2>&1; then
		$GARAGE key create "$KEY_NAME"
	fi
	$GARAGE bucket allow --read --write --owner "$BUCKET" --key "$KEY_NAME"
	echo "[bootstrap] generated key (copy these into .env):"
	$GARAGE key info "$KEY_NAME" --show-secret
fi

echo "[bootstrap] done."
