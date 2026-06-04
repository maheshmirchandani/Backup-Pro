#!/bin/sh
# fake-rsync: writes one stderr diagnostic and exits 23 (rsync's
# "Partial transfer due to error" exit code).
cat >/dev/null
echo "rsync: failed to set times on some file: Operation not permitted (1)" >&2
exit 23
