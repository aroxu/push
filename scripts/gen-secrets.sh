#!/usr/bin/env bash
# Prints a ready-to-paste secret block for .env.
set -euo pipefail

rand_hex() { openssl rand -hex "$1"; }
rand_b64() { openssl rand -base64 24 | tr -d '/+=' | cut -c1-32; }

cat <<EOF
# --- generated $(date -u +%Y-%m-%dT%H:%M:%SZ) ---
GARAGE_RPC_SECRET=$(rand_hex 32)
PUSH_S3_ACCESS_KEY=GK$(rand_hex 12)
PUSH_S3_SECRET_KEY=$(rand_hex 32)

# Dozzle password hash (requires docker):
#   docker run --rm amir20/dozzle:latest generate --name admin --password '$(rand_b64)'
EOF

