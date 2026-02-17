package gomoddepgraph

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/rhansen/gomoddepgraph/internal"
	"github.com/rhansen/gomoddepgraph/internal/command"
	"github.com/rhansen/gomoddepgraph/internal/logging"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

// A ModuleId identifies a specific version of a specific module, or a module requirement (path and
// minimum acceptable version).  Some uses of [ModuleId] allow the [ModuleId.Version] field to be
// "latest" or empty (equivalent to "latest") or any other [version query] accepted by Go; these can
// be resolved to a specific version by the [ResolveVersion] function.
//
// [version query]: https://go.dev/ref/mod#version-queries
type ModuleId struct {
	internal.XModModuleVersion
}

// NewModuleId constructs a new [ModuleId] from its path and version components.
func NewModuleId(path, ver string) ModuleId {
	return ModuleId{module.Version{Path: path, Version: ver}}
}

// ParseModuleId breaks a "path[@version]" string into its path and version components.
func ParseModuleId(pathVer string) ModuleId {
	parts := append(strings.SplitN(pathVer, "@", 2), "")
	return NewModuleId(parts[0], parts[1])
}

// Check asserts that the path and version are valid, and the version is canonical (not the empty
// string or a [version query]).  A [ModuleId] that passes this check is assumed to have a resolved
// (fully-specified) [ModuleId.Version] field.
//
// [version query]: https://go.dev/ref/mod#version-queries
func (mId ModuleId) Check() error {
	got := mId.Version
	if err := module.Check(mId.Path, got); err != nil {
		return err
	}
	if got == "" {
		return errors.New("version is the empty string")
	}
	if want := semver.Canonical(got) + semver.Build(got); got != want {
		return fmt.Errorf("version is non-canonical; got %v, want %v", got, want)
	}
	return nil
}

// ModuleIdCompare returns [strings.Compare] using each [ModuleId]'s [ModuleId.Path] if the two
// paths differ, otherwise it returns [semver.Compare] using each [ModuleId]'s [ModuleId.Version].
func ModuleIdCompare(a, b ModuleId) int {
	if cmp := strings.Compare(a.Path, b.Path); cmp != 0 {
		return cmp
	}
	return semver.Compare(a.Version, b.Version)
}

// ResolveVersion resolves "latest" and other such [version query] strings to the actual version.
// If the [ModuleId.Version] field is empty, "latest" is assumed.
//
// [version query]: https://go.dev/ref/mod#version-queries
func ResolveVersion(ctx context.Context, mId ModuleId) (ModuleId, error) {
	if mId.Version == "" {
		mId.Version = "latest"
	}
	cmd := []string{"go", "list", "-json", "-m"}
	if slog.Default().Enabled(ctx, logging.LevelVerbose) {
		cmd = []string{"go", "list", "-x", "-json", "-m"}
	}
	cmd = append(cmd, mId.String())
	lsIter, finished := command.DecodeJsonStream[struct{ Path, Version string }](ctx, "/", cmd...)
	ls := slices.Collect(lsIter)
	if err := finished(); err != nil {
		return ModuleId{}, err
	}
	if len(ls) != 1 {
		return ModuleId{}, fmt.Errorf("got %v results, want 1", len(ls))
	}
	if ls[0].Path != mId.Path {
		return ModuleId{}, fmt.Errorf("got path %v, want %v", ls[0].Path, mId.Path)
	}
	mId.Version = ls[0].Version
	return mId, nil
}
