package filesystem

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
)

func TestInspect_RootIsAPFS(t *testing.T) {
	requireMacOS(t)
	info, err := Inspect(context.Background(), "/")
	if err != nil {
		t.Fatalf("Inspect /: %v", err)
	}
	// Modern macOS (13+) always mounts the system volume as APFS.
	if info.Type != TypeAPFS {
		t.Errorf("root mount Type = %v (raw=%q), want %v", info.Type, info.TypeRaw, TypeAPFS)
	}
	if info.Mountpoint != "/" {
		t.Errorf("Mountpoint = %q, want '/'", info.Mountpoint)
	}
	if info.TypeRaw == "" {
		t.Errorf("TypeRaw should not be empty")
	}
}

func TestInspect_NonexistentPath(t *testing.T) {
	requireMacOS(t)
	_, err := Inspect(context.Background(), "/nonexistent/no/way/should/this/exist")
	if err == nil {
		t.Fatal("expected error on nonexistent path")
	}
}

func TestInspect_CancelledContext(t *testing.T) {
	requireMacOS(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Inspect(ctx, "/")
	if err == nil {
		t.Fatal("expected cancelled context error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected error wrapping context.Canceled, got %v", err)
	}
}

func TestInspect_NonDarwinStub(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("non-darwin stub test; skipping on darwin")
	}
	_, err := Inspect(context.Background(), "/")
	if err == nil {
		t.Fatal("expected unsupported-platform error on non-darwin")
	}
	if !strings.Contains(err.Error(), "unsupported platform") {
		t.Errorf("expected 'unsupported platform' in error, got: %v", err)
	}
}

func TestValidate_NilInfo(t *testing.T) {
	if err := Validate(nil); err == nil {
		t.Fatal("expected error on nil info")
	}
}

func TestValidate_APFSAccepted(t *testing.T) {
	info := &Info{Type: TypeAPFS, TypeRaw: "apfs", Mountpoint: "/Volumes/USB"}
	if err := Validate(info); err != nil {
		t.Errorf("APFS should be accepted: %v", err)
	}
}

func TestValidate_HFSPlusAccepted(t *testing.T) {
	info := &Info{Type: TypeHFSPlus, TypeRaw: "hfs", Mountpoint: "/Volumes/USB"}
	if err := Validate(info); err != nil {
		t.Errorf("HFS+ should be accepted: %v", err)
	}
}

func TestValidate_ExFATRejected(t *testing.T) {
	info := &Info{Type: TypeExFAT, TypeRaw: "exfat", Mountpoint: "/Volumes/USB"}
	err := Validate(info)
	if err == nil {
		t.Fatal("expected exFAT rejection")
	}
	if !errors.Is(err, ErrFilesystemUnsupported) {
		t.Errorf("expected wrapping ErrFilesystemUnsupported, got %v", err)
	}
	var ue *UnsupportedError
	if !errors.As(err, &ue) {
		t.Errorf("expected *UnsupportedError, got %T", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "diskutil eraseDisk APFS") {
		t.Errorf("error should include reformat recipe: %v", msg)
	}
	if !strings.Contains(msg, "FLASHBKP") {
		t.Errorf("error should include FLASHBKP volume name in recipe: %v", msg)
	}
	if !strings.Contains(msg, "ALL DATA WILL BE LOST") {
		t.Errorf("error should warn about data loss: %v", msg)
	}
	if !strings.Contains(msg, "/Volumes/USB") {
		t.Errorf("error should include the mountpoint: %v", msg)
	}
	if !strings.Contains(msg, "exfat") {
		t.Errorf("error should name the detected type: %v", msg)
	}
}

func TestValidate_MSDOSRejected(t *testing.T) {
	info := &Info{Type: TypeMSDOS, TypeRaw: "msdos", Mountpoint: "/Volumes/USB"}
	err := Validate(info)
	if err == nil {
		t.Fatal("expected msdos rejection")
	}
	if !errors.Is(err, ErrFilesystemUnsupported) {
		t.Errorf("expected wrapping ErrFilesystemUnsupported, got %v", err)
	}
}

func TestValidate_UnknownRejected(t *testing.T) {
	info := &Info{Type: TypeUnknown, TypeRaw: "weirdfs", Mountpoint: "/x"}
	err := Validate(info)
	if err == nil {
		t.Fatal("expected unknown rejection")
	}
	if !errors.Is(err, ErrFilesystemUnsupported) {
		t.Errorf("expected wrapping ErrFilesystemUnsupported, got %v", err)
	}
	if !strings.Contains(err.Error(), "weirdfs") {
		t.Errorf("error should include raw type 'weirdfs': %v", err)
	}
}

func TestValidate_NoExecRejected(t *testing.T) {
	info := &Info{
		Type:       TypeAPFS,
		TypeRaw:    "apfs",
		Mountpoint: "/Volumes/USB",
		Flags:      MountFlags{NoExec: true},
	}
	err := Validate(info)
	if err == nil {
		t.Fatal("expected noexec rejection")
	}
	if !errors.Is(err, ErrFilesystemNoExec) {
		t.Errorf("expected wrapping ErrFilesystemNoExec, got %v", err)
	}
	if !strings.Contains(err.Error(), "mount -uw -o exec") {
		t.Errorf("error should include remount recipe: %v", err)
	}
}

func TestValidate_ReadOnlyAlone_AcceptedForAPFS(t *testing.T) {
	// ReadOnly is surfaced but not refused by Validate (callers do write checks).
	info := &Info{
		Type:       TypeAPFS,
		TypeRaw:    "apfs",
		Mountpoint: "/Volumes/USB",
		Flags:      MountFlags{ReadOnly: true},
	}
	if err := Validate(info); err != nil {
		t.Errorf("APFS+ReadOnly should pass type/exec gate (write check is elsewhere): %v", err)
	}
}

func TestValidate_NoSUIDAndNoDev_Accepted(t *testing.T) {
	info := &Info{
		Type:       TypeAPFS,
		TypeRaw:    "apfs",
		Mountpoint: "/Volumes/USB",
		Flags:      MountFlags{NoSUID: true, NoDev: true},
	}
	if err := Validate(info); err != nil {
		t.Errorf("nosuid/nodev should be harmless: %v", err)
	}
}

func TestParseType(t *testing.T) {
	cases := []struct {
		raw  string
		want FilesystemType
	}{
		{"apfs", TypeAPFS},
		{"hfs", TypeHFSPlus},
		{"exfat", TypeExFAT},
		{"msdos", TypeMSDOS},
		{"ntfs", TypeUnknown},
		{"", TypeUnknown},
		{"APFS", TypeUnknown}, // statfs reports lowercase; we don't fold here
	}
	for _, tc := range cases {
		if got := parseType(tc.raw); got != tc.want {
			t.Errorf("parseType(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

func TestUnsupportedError_NilInfoSafe(t *testing.T) {
	// Defence-in-depth: UnsupportedError should not panic if constructed
	// with a nil Info (shouldn't happen via Validate, but cheap to check).
	e := &UnsupportedError{Info: nil}
	msg := e.Error()
	if msg == "" {
		t.Error("Error() should produce a non-empty string even with nil Info")
	}
	if !errors.Is(e, ErrFilesystemUnsupported) {
		t.Error("UnsupportedError should unwrap to ErrFilesystemUnsupported")
	}
}

func requireMacOS(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skipf("filesystem statfs inspection is macOS-only; runtime.GOOS=%s", runtime.GOOS)
	}
}
