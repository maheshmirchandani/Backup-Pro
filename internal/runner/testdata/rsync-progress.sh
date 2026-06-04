#!/bin/sh
# fake-rsync: emits canned --progress lines for one file then exits 0.
# Lines mirror the shape rsync 3.x writes: filename, transferring %,
# final 100% line with the xfr#1 tail.
cat >/dev/null
printf 'sample.txt\n'
printf '       524,288  50%%   30.00MB/s    0:00:02\n'
printf '     1,048,576 100%%  500.00MB/s    0:00:00 (xfr#1, to-chk=0/1)\n'
printf 'sent 1,048,576 bytes  received 100 bytes  total 1,048,676\n'
exit 0
