#!/bin/sh
set -eu

mkdir -p /app/data
chown es:es /app/data

exec su-exec es /app/email-soft "$@"
