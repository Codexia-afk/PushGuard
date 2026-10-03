#!/bin/sh
set -eu
PROJECT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$PROJECT_DIR"
if [ -x ./bin/pushguard ]; then
  exec ./bin/pushguard demo "$@"
fi
exec go run ./cmd/pushguard demo "$@"
