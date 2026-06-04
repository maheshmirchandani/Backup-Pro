#!/bin/sh
# fake-rsync: replaces itself with `sleep` so the wrapper's SIGKILL on
# ctx cancel terminates the actual sleep process (not just the shell
# wrapper). Used by the mid-transfer cancellation test.
cat >/dev/null
exec sleep 30
