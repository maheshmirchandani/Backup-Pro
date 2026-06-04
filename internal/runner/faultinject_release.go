//go:build !faultinject

// DO NOT DELETE: release binary won't link without this.
//
// This file is the no-op stub for the faultinject build tag. The runner
// phase code calls faultinject.Hook unconditionally at instrumented sites;
// without this stub, release builds would not compile. The release-gate
// symbol scan (`make verify-release`, invariant #35) greps `go tool nm` for
// `(^|[._/])faultinject` and fails if any faultinject symbol leaks into
// the release binary. The bodies below are intentionally minimal so the
// linker can dead-code-eliminate everything that isn't a callable entry
// point.
//
// Import surface intentionally limited to "context" and "errors". Adding
// any extra import here risks dragging code with the substring
// "faultinject" into the release binary through indirect references.

package runner

import (
	"context"
	"errors"
)

type Action string

const (
	ActionCorrupt          Action = "corrupt"
	ActionKill             Action = "kill"
	ActionMutateSource     Action = "mutate-source"
	ActionUnmount          Action = "unmount"
	ActionDiskFull         Action = "disk-full"
	ActionPermissionDenied Action = "permission-denied"
)

type Point string

const (
	PointT1PreRsync  Point = "T1-pre"
	PointT1Progress  Point = "T1"
	PointT1Post      Point = "T1-post"
	PointT2PreHash   Point = "T2-pre"
	PointT2PerFile   Point = "T2"
	PointT3PreUnlink Point = "T3-pre"
	PointT3PerFile   Point = "T3"
)

type Fault struct {
	Action     Action
	Phase      string
	File       string
	AfterPct   int
	AfterCount int
}

type HookArgs struct {
	Phase       string
	CurrentFile string
	FilesDone   int
	FilesTotal  int
	BytesDone   int64
	BytesTotal  int64
	DestRoot    string
	SourceRoot  string
}

var (
	ErrFaultinjectStripped = errors.New("faultinject stripped from release build")
	ErrFaultKill           = errors.New("faultinject: kill fired")
	ErrFaultDiskFull       = errors.New("faultinject: disk-full simulated")
)

func Parse(specs []string) ([]Fault, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	return nil, ErrFaultinjectStripped
}

func Activate(_ []Fault) {}

func Hook(_ context.Context, _ Point, _ HookArgs) error { return nil }
