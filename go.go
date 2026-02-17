package gomoddepgraph

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/rhansen/gomoddepgraph/internal/command"
	"github.com/rhansen/gomoddepgraph/internal/logging"
	"golang.org/x/mod/modfile"
)

type jsonMetadata struct{ Path, Version, Dir, GoMod string }

// tempFilteredModClone makes a dummy copy of the named module in a temporary directory.  The copy
// doesn't have any source files—just go.mod and go.sum (if one existed in the original).  The
// temporary clone's go.mod has any directives that might affect the requirement graph or dependency
// resolution removed.  The name of the temporary directory is returned, along with a done callback
// that removes the temporary directory.
func tempFilteredModClone(ctx context.Context, mId ModuleId) (_ string, done func() error, retErr error) {
	done = func() error { return nil }
	defer func() {
		if retErr != nil {
			done()
			done = func() error { return nil }
		}
	}()
	if err := downloadModule(ctx, mId); err != nil {
		return "", done, err
	}
	md, err := lsModule(ctx, mId)
	if err != nil {
		return "", done, err
	}
	tmp, err := os.MkdirTemp(
		"", fmt.Sprintf("gomoddepgraph-%v-*", strings.ReplaceAll(mId.String(), "/", "_")))
	if err != nil {
		return "", done, err
	}
	done = func() error { return os.RemoveAll(tmp) }
	// md.GoMod might have been synthesized by $GOPROXY.  If so, no go.mod file will exist in md.Dir.
	// However, a go.sum file is needed in tmp for "go list -m all" and "go mod graph" to work in that
	// directory.  It is safe to write a copy of the synthesized go.mod to tmp even though the
	// original synthetic module doesn't have a go.mod because the synthesized go.mod does not have
	// any requirements.
	if err := copyFilteredGoMod(md.GoMod, tmp); err != nil {
		return "", done, err
	}
	// Copy go.sum if it exists.  The "go list -m" command complains if go.sum lacks any modules
	// required in go.mod, and for some reason "go mod download" re-downloads already downloaded
	// modules when building go.sum from scratch (as of Go v1.25).  Copying go.sum avoids that
	// redundant download work.
	if md.Dir == "" {
		return "", done, fmt.Errorf("missing contents of downloaded module: %v", mId)
	}
	if err := copyGoSum(md.Dir, tmp); err != nil {
		return "", done, err
	}
	return tmp, done, nil
}

func lsModule(ctx context.Context, mId ModuleId) (*jsonMetadata, error) {
	lsIter, done := goListM(ctx, "/", mId.String())
	ret := slices.Collect(lsIter)
	if err := done(); err != nil {
		return nil, err
	}
	return ret[0], nil
}

func goListM(ctx context.Context, wd string, args ...string) (iter.Seq[*jsonMetadata], func() error) {
	cmd := []string{"go", "list", "-json", "-m"}
	if slog.Default().Enabled(ctx, logging.LevelVerbose) {
		cmd = []string{"go", "list", "-x", "-json", "-m"}
	}
	cmd = append(cmd, args...)
	return command.DecodeJsonStream[*jsonMetadata](ctx, wd, cmd...)
}

var downloadConcurrencyLimiter = make(chan struct{}, 1)

func downloadModule(ctx context.Context, mId ModuleId) error {
	downloadConcurrencyLimiter <- struct{}{}
	defer func() { <-downloadConcurrencyLimiter }()
	slog.DebugContext(ctx, "downloading Go module", "mod", mId)
	cmd := []string{"go", "mod", "download"}
	if slog.Default().Enabled(ctx, logging.LevelVerbose) {
		cmd = append(cmd, "-x")
	}
	cmd = append(cmd, mId.String())
	return command.New(ctx, "/", cmd...).Run()
}

func copyFilteredGoMod(src, dstDir string) error {
	goMod, err := readGoMod(src)
	if err != nil {
		return err
	}
	dummyGoMod, err := filterGoMod(goMod)
	if err != nil {
		return err
	}
	dummyGoModData, err := dummyGoMod.Format()
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dstDir, "go.mod"), dummyGoModData, 0666)
}

func readGoMod(src string) (*modfile.File, error) {
	goModData, err := os.ReadFile(src)
	if err != nil {
		return nil, err
	}
	return modfile.ParseLax(src, goModData, nil)
}

func filterGoMod(src *modfile.File) (*modfile.File, error) {
	dst := &modfile.File{}
	if src == nil || src.Module == nil {
		return nil, fmt.Errorf("source go.mod lacks module directive")
	}
	if err := dst.AddModuleStmt(src.Module.Mod.Path); err != nil {
		return nil, err
	}
	if src.Go != nil {
		if err := dst.AddGoStmt(src.Go.Version); err != nil {
			return nil, err
		}
	}
	for _, req := range src.Require {
		dst.AddNewRequire(req.Mod.Path, req.Mod.Version, req.Indirect)
	}
	return dst, nil
}

func copyGoSum(srcDir, dstDir string) error {
	goSumFile := filepath.Join(srcDir, "go.sum")
	goSumData, err := os.ReadFile(goSumFile)
	if errors.Is(err, os.ErrNotExist) {
		goSumData = nil
	} else if err != nil {
		return fmt.Errorf("%s: %w", goSumFile, err)
	} else if goSumData == nil {
		goSumData = []byte{}
	}
	if goSumData != nil {
		if err := os.WriteFile(filepath.Join(dstDir, "go.sum"), goSumData, 0666); err != nil {
			return err
		}
	}
	return nil
}
