#!/bin/sh
# Re-seeds a Lull Mail DEMO database: empties it, then loads the made-up mail.
# Prints one SQL script on stdout; pipe it to psql against the demo database.
#
#   tools/demo/reset.sh | psql "$DEMO_DATABASE_URL" -v ON_ERROR_STOP=1
#   tools/demo/reset.sh | docker exec -i <demo-postgres-container> psql -U lull -d lullmail -v ON_ERROR_STOP=1
#
# The demo owner must already exist: start the app once with LULL_DEMO=1
# before the first run. Every timestamp in the seed is relative to now(), so
# each run produces a mailbox that looks like today's.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
echo "BEGIN;"
cat "$here/reset-prelude.sql"
# The seed's psql meta-command is redundant under -v ON_ERROR_STOP=1 and
# not valid inside a piped transaction on every client; drop it.
grep -v '^\\set ' "$here/../../dashboard/e2e/seed.sql"
echo "COMMIT;"
