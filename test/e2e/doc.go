// Package e2e holds the end-to-end test scaffolding for FlashBackup.
//
// Tests in this directory exercise the flashbackup CLI against a freshly
// mounted DMG (via internal/testutil) using committed fixture trees
// under test/fixtures. The package itself is not test-only by build
// tag; instead each *_test.go file gates itself with
// testutil.RequireE2E + testutil.RequireMacOS so a plain
// `go test ./...` on any platform skips cleanly.
//
// API surface:
//
//	SetupUSB(t, sizeMB)                  - mount fresh APFS DMG + init.
//	SeedSource(t, fixtureName)           - copy a test/fixtures/<name>
//	                                       tree into a host tempdir.
//	SeedProfile(t, usb, name, src, ...)  - write profiles.json directly.
//	RunBackup(t, profile, usb, args...)  - exec `flashbackup backup`.
//	RunVerify / RunStatus / RunProfiles
//	RunInit                              - other CLI subcommands.
//	BuildBinary, BuildFaultinjectBinary  - cached binary builds.
//
//	AssertManifestExists(t, usb, runID)
//	AssertRunsNDJSONHasFinishedLine(t, usb)
//	AssertVerifySummaryExists(t, usb, runID)
//	FixtureTreeSHA256(t, fixtureRoot)    - canonical hash recipe; must
//	                                       match the MANIFEST.txt line.
//
// Design notes:
//
//   - Mountpoints come from internal/testutil.MountTempVolume which
//     uses a unique nanosecond-suffixed volname per attach
//     ("/Volumes/Flashbackup<unixnano>"). NEVER a fixed path under
//     /Volumes/<name>; that would clobber a real USB volume on a
//     developer's machine if one was attached.
//
//   - The flashbackup binary is built ONCE per test process via
//     sync.Once (see binary_cache.go). The build cost is ~1-2 s; per-
//     test invocations are pure exec time.
//
//   - SeedProfile bypasses the `flashbackup profiles new` editor flow
//     because that flow needs a real EDITOR to be wired (or the test-
//     only editor override seam, which is package-internal). Writing
//     profiles.json directly via internal/profiles.Store.Upsert
//     reuses the validation surface the real subcommand also runs.
//
//   - Fixture trees come from test/fixtures/{tiny,realistic,
//     pathological}. The tiny and realistic trees are checked in
//     verbatim; pathological is materialized at test time by
//     test/fixtures/pathological/mkfixtures.sh because git cannot
//     reliably round-trip its members (NFC/NFD twins, control bytes,
//     sparse files, immutable bits).
package e2e
