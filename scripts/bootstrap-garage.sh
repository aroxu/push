#!/bin/sh
# Idempotent first-boot bootstrap for Garage:
#   1. wait for the node to answer
#   2. assign it a storage layout (once)
#   3. create the bucket
#   4. create the access key and grant it read/write
#
# Re-running this is safe; every step tolerates "already exists".
set -eu

GARAGE="garage -c /etc/garage.toml"
BUCKET="${PUSH_S3_BUCKET:-push}"
KEY_NAME="${PUSH_S3_KEY_NAME:-push-app}"

echo "[bootstrap] waiting for garage to become reachable..."
i=0
until $GARAGE status >/dev/null 2>&1; do
	i=$((i + 1))
	if [ "$i" -gt 60 ]; then
		echo "[bootstrap] garage did not come up in time" >&2
		exit 1
	fi
	sleep 2
done

NODE_ID=$($GARAGE node id -q 2>/dev/null | cut -d@ -f1)
echo "[bootstrap] node id: $NODE_ID"

if ! $GARAGE layout show 2>/dev/null | grep -q "$NODE_ID"; then
	echo "[bootstrap] assigning storage layout"
	$GARAGE layout assign -z dc1 -c "${GARAGE_CAPACITY:-100G}" "$NODE_ID" || true
	VER=$($GARAGE layout show | grep -oE 'version [0-9]+' | head -n1 | awk '{print $2}')
	NEXT=$((${VER:-0} + 1))
	$GARAGE layout apply --version "$NEXT" || true
	sleep 3
fi

if ! $GARAGE bucket info "$BUCKET" >/dev/null 2>&1; then
	echo "[bootstrap] creating bucket $BUCKET"
	$GARAGE bucket create "$BUCKET"
fi

if ! $GARAGE key info "$KEY_NAME" >/dev/null 2>&1; then
	echo "[bootstrap] creating key $KEY_NAME"
	$GARAGE key create "$KEY_NAME"
fi

if [ -n "${PUSH_S3_ACCESS_KEY:-}" ] && [ -n "${PUSH_S3_SECRET_KEY:-}" ]; then
	echo "[bootstrap] importing pinned credentials from .env"
	$GARAGE key import -n "$KEY_NAME-pinned" --yes \
		"$PUSH_S3_ACCESS_KEY" "$PUSH_S3_SECRET_KEY" 2>/dev/null || true
	$GARAGE bucket allow --read --write --owner "$BUCKET" --key "$KEY_NAME-pinned" || true
fi

$GARAGE bucket allow --read --write --owner "$BUCKET" --key "$KEY_NAME" || true

echo "[bootstrap] done. Key material:"
$GARAGE key info "$KEY_NAME" --show-secret || true

