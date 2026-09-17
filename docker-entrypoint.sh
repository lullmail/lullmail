#!/bin/sh
# Prepare the configured data directory, then drop privileges and exec the
# server. DATA_DIR may point anywhere; it must be absolute and dedicated —
# chown -R on an arbitrary host tree is never acceptable (audit OPS-06).
set -eu

data_dir="${DATA_DIR:-/app/data}"
case "$data_dir" in
  /*) ;;
  *) echo "DATA_DIR must be an absolute path (got '$data_dir')" >&2; exit 1 ;;
esac

mkdir -p -- "$data_dir"
data_dir="$(readlink -f -- "$data_dir")"
case "$data_dir" in
  /|/app|/bin|/etc|/usr|/var|/home|/root|/tmp)
    echo "DATA_DIR must be a dedicated data directory, not '$data_dir'" >&2
    exit 1 ;;
esac

# The dropped-privilege user needs to own the directory itself. Existing
# files inside a moved volume must already be user-owned; a recursive
# chown of an arbitrary tree on every boot is not something this image
# does silently.
chown lull:lull "$data_dir"
chmod 0700 "$data_dir"

export DATA_DIR="$data_dir"
exec su-exec lull /app/lullmail "$@"
