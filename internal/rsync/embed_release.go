//go:build embed_real_rsync

package rsync

import _ "embed"

// embeddedRsync is the real universal2 GNU rsync 3.4.1 produced by
// scripts/build-rsync.sh. The file is gitignored and absent from a fresh
// checkout, so this file only compiles after `make build-rsync` has run.
// A missing file surfaces at compile time as
// "pattern bin/rsync.universal2: no matching files found", which is the
// intended failure: louder than shipping a placeholder by accident.
//
//go:embed bin/rsync.universal2
var embeddedRsync []byte
