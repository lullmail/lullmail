#!/bin/sh
set -eu

mkdir -p /app/data
chown lull:lull /app/data

exec su-exec lull /app/lullmail "$@"
