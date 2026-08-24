//go:build !embed_real_rsync

package rsync

import _ "embed"

// embeddedRsync is the placeholder shell script. This is the DEFAULT
// payload: plain `go build`, `go test`, `go run` and the existing
// `make build` all land here. It prints a banner and exits 0 without
// transferring anything, which the Task 12b-A regression test pins.
//
//go:embed bin/rsync.placeholder
var embeddedRsync []byte
