#!/bin/sh
# fake-rsync: exits 0 with a single summary line on stdout.
# Used by RunT2Transfer happy-path tests. Reads (and discards) any
# stdin payload so the wrapper's --files-from goroutine completes.
cat >/dev/null
echo "sent 0 bytes  received 0 bytes  0.00 bytes/sec"
exit 0
