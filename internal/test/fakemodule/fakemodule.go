// Package fakemodule makes it easy to create a fake [Go module proxy] populated with fake modules
// to facilitate testing.
//
// [Go module proxy]: https://go.dev/ref/mod#module-proxy
package fakemodule

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"iter"
	"log/slog"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	gmdg "github.com/rhansen/gomoddepgraph"
	"github.com/rhansen/gomoddepgraph/internal/command"
	"golang.org/x/mod/modfile"
	"golang.org/x/mod/sumdb/dirhash"
	"golang.org/x/mod/zip"
)

type config struct {
	gmdg.ModuleId
	synthetic bool
	goMod     *modfile.File
}

func (cfg *config) Check() error {
	if cfg.Path == "" {
		return fmt.Errorf("module path is the empty string")
	}
	if cfg.Version == "" {
		return fmt.Errorf("module version is the empty string")
	}
	if p1, p2 := cfg.Path, cfg.goMod.Module.Mod.Path; p1 != p2 {
		return fmt.Errorf("mismatched module paths: %+q and %+q", p1, p2)
	}
	if err := cfg.ModuleId.Check(); err != nil {
		return err
	}
	if cfg.synthetic {
		want := &modfile.File{}
		if err := want.AddModuleStmt(cfg.Path); err != nil {
			return err
		}
		wantData, err := want.Format()
		if err != nil {
			return err
		}
		gotData, err := cfg.goMod.Format()
		if err != nil {
			return err
		}
		if !bytes.Equal(gotData, wantData) {
			return fmt.Errorf("can't use a non-basic go.mod for a synthetic module")
		}
	}
	return nil
}

// An Option controls the creation of a fake module.
type Option func(*config) error

// Synthetic returns an option that causes the fake module to be synthetic (a synthesized go.mod to
// mimic a legacy non-module collection of packages) or not.
func Synthetic(synthetic bool) Option {
	return func(cfg *config) error {
		cfg.synthetic = synthetic
		return nil
	}
}

// Path returns an option that sets the fake module's path (e.g., "example.com/foo").
func Path(path string) Option {
	return func(cfg *config) error {
		if err := cfg.goMod.AddModuleStmt(path); err != nil {
			return err
		}
		cfg.Path = path
		return nil
	}
}

// Version returns an option that sets the fake module's version (e.g., "v1.2.3").
func Version(version string) Option {
	return func(cfg *config) error {
		// [modfile.File.Module.Mod.Version] is always the empty string because the go.mod "module"
		// directive only contains the module path <https://go.dev/ref/mod#go-mod-file-module>.
		cfg.Version = version
		return nil
	}
}

// ModuleId returns an option that sets the fake module's path and version.
func ModuleId(mId gmdg.ModuleId) Option {
	return func(cfg *config) error {
		if err := Path(mId.Path)(cfg); err != nil {
			return err
		}
		return Version(mId.Version)(cfg)
	}
}

// Go adds or removes a [go directive] to the generated go.mod.  By default the module has a [go
// directive] set to "1.26.0" (note the lack of "v" prefix).  Pass an empty string to remove the [go
// directive].
//
// [go directive]: https://go.dev/ref/mod#go-mod-file-go
func Go(ver string) Option {
	return func(cfg *config) error {
		if ver == "" {
			cfg.goMod.DropGoStmt()
		} else {
			return cfg.goMod.AddGoStmt(ver)
		}
		return nil
	}
}

// Id returns an option that sets the fake module's path and version.  The given string has the form
// path@version, e.g., "example.com/foo@v1.2.3".
func Id(pathVer string) Option {
	return ModuleId(gmdg.ParseModuleId(pathVer))
}

// GoMod returns an [Option] that sets the fake module's go.mod contents.  This is usually not
// necessary; a default go.mod is created using other [Option] values.
//
// For convenience, if the [modfile.File] contains a comment line of the form `//version:v1.2.3` (no
// space before "version" or after the colon) immediately above the [module] directive then that
// version will be used as the fake module's version (as with the [Option] returned from [Version]).
// For example:
//
//	//version:v1.2.3
//	module example.com/foo
//
// [module]: https://go.dev/ref/mod#go-mod-file-module
func GoMod(goMod *modfile.File) Option {
	return func(cfg *config) error {
		cfg.goMod = goMod
		cfg.Path = goMod.Module.Mod.Path
		// [modfile.File.Module.Mod.Version] is always the empty string because the go.mod "module"
		// directive only contains the module path <https://go.dev/ref/mod#go-mod-file-module>.
		// But the desired module version can be placed in a magic comment just before the module
		// directive.
		comments := goMod.Module.Syntax.Comments
		if len(comments.Before) > 0 {
			vc := comments.Before[len(comments.Before)-1].Token
			slog.Debug("last comment before module line", "comment", vc)
			const pfx = "//version:"
			if strings.HasPrefix(vc, pfx) {
				cfg.Version = vc[len(pfx):]
			}
		}
		return nil
	}
}

// GoModData returns an [Option] that sets the fake module's go.mod contents.  This is like [GoMod],
// except the argument is the unparsed go.mod contents.  The given contents are parsed and
// regenerated via [modfile.File.Format].
func GoModData(goModData []byte) Option {
	return func(cfg *config) error {
		goMod, err := modfile.Parse("go.mod", goModData, nil)
		if err != nil {
			return err
		}
		return GoMod(goMod)(cfg)
	}
}

// Require returns an [Option] that adds a [require] directive to the fake module's go.mod.  The
// pathVer argument has the form path@version, e.g., "example.com/foo@v1.2.3".
//
// [require]: https://go.dev/ref/mod#go-mod-file-require
func Require(pathVer string, indirect bool) Option {
	mId := gmdg.ParseModuleId(pathVer)
	return func(cfg *config) error {
		cfg.goMod.AddNewRequire(mId.Path, mId.Version, indirect)
		return nil
	}
}

// Add is a low-level function that creates a new fake module in the given proxy directory.
// dirHashes maps dependency modules to their directory hashes as returned from [dirhash.HashDir].
// goModHashes maps dependency modules to their go.mod hashes as returned from
// [dirhash.DefaultHash].  The new module's hashes are added to dirHashes and goModHashes.
//
// By default the module has a [go directive] set to "1.26.0".  Use [Go] to remove or replace the
// directive, or [GoMod] or [GoModData] to replace the entire go.mod.
//
// See [FakeGoProxy.Add] for a more ergonomic interface.
//
// [go directive]: https://go.dev/ref/mod#go-mod-file-go
func Add(ctx context.Context, proxyDir string, dirHashes, goModHashes map[gmdg.ModuleId]string, opts ...Option) (retErr error) {
	cfg := &config{goMod: &modfile.File{}}
	if err := cfg.goMod.AddGoStmt("1.26.0"); err != nil {
		return err
	}
	for _, opt := range opts {
		if err := opt(cfg); err != nil {
			return err
		}
	}
	if err := cfg.Check(); err != nil {
		return err
	}
	slog.DebugContext(ctx, "creating fake module", "mod", cfg.ModuleId)
	// Create the $GOPROXY/<modpath>/@v directory.
	vd := filepath.Join(proxyDir, cfg.Path, "@v")
	if err := os.MkdirAll(vd, 0777); err != nil {
		return err
	}
	// Create/update the $GOPROXY/<modpath>/@v/list file.
	if err := fileAppend(filepath.Join(vd, "list"), []byte(cfg.Version+"\n")); err != nil {
		return err
	}
	// Create the $GOPROXY/<modpath>/@v/<modversion>.info file.
	vb := filepath.Join(vd, cfg.Version)
	if err := fileSaveJson(vb+".info", &struct {
		Version string
		Time    time.Time
	}{cfg.Version, time.Now()}); err != nil {
		return err
	}
	// Create the $GOPROXY/<modpath>/@v/<modversion>.zip file.
	zipdir, err := os.MkdirTemp("", "")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.RemoveAll(zipdir); retErr == nil {
			retErr = err
		}
	}()
	// Create pkg.go.
	pkgSrc := "package pkg\n\nimport (\n"
	for _, req := range cfg.goMod.Require {
		if req.Indirect || strings.HasPrefix(req.Mod.Path, "test.test/") {
			continue
		}
		pkgSrc += fmt.Sprintf("\t_ \"%s\"\n", req.Mod.Path)
	}
	pkgSrc += ")\n"
	if err := fileSave(filepath.Join(zipdir, "pkg.go"), []byte(pkgSrc)); err != nil {
		return err
	}
	// Create pkg_test.go.
	pkgTestSrc := "package pkg_test\n\nimport (\n"
	for _, req := range cfg.goMod.Require {
		if req.Indirect || !strings.HasPrefix(req.Mod.Path, "test.test/") {
			continue
		}
		pkgTestSrc += fmt.Sprintf("\t_ \"%s\"\n", req.Mod.Path)
	}
	pkgTestSrc += ")\n"
	if err := fileSave(filepath.Join(zipdir, "pkg_test.go"), []byte(pkgTestSrc)); err != nil {
		return err
	}
	// Format the *.go files.
	if err := command.New(ctx, zipdir, "gofmt", "-w", "-e", ".").Run(); err != nil {
		return err
	}
	// Create go.mod.
	goModData, err := cfg.goMod.Format()
	if err != nil {
		return err
	}
	if !cfg.synthetic {
		if err := fileSave(filepath.Join(zipdir, "go.mod"), goModData); err != nil {
			return err
		}
	}
	// Create the $GOPROXY/<modpath>/@v/<modversion>.mod file.
	if err := fileSave(vb+".mod", goModData); err != nil {
		return err
	}
	if !cfg.synthetic {
		// Create go.sum.
		sums := ""
		for _, req := range cfg.goMod.Require {
			d := gmdg.ModuleId{req.Mod}
			if dirHashes[d] == "" || goModHashes[d] == "" {
				return fmt.Errorf("unable to build %v go.sum: hashes for dependency %v not found", cfg, d)
			}
			sums += fmt.Sprintf("%s %s %s\n", d.Path, d.Version, dirHashes[d])
			sums += fmt.Sprintf("%s %s/go.mod %s\n", d.Path, d.Version, goModHashes[d])
		}
		slog.DebugContext(ctx, "Sums:", "sums", sums)
		if err := fileSave(filepath.Join(zipdir, "go.sum"), []byte(sums)); err != nil {
			return err
		}
	}
	// Hash the module directory.
	dh, err := dirhash.HashDir(zipdir, cfg.String(), dirhash.DefaultHash)
	if err != nil {
		return err
	}
	dirHashes[cfg.ModuleId] = dh
	slog.DebugContext(ctx, "dir hash", "hash", dh)
	// Hash go.mod.  This is needed even for synthetic modules.
	mh, err := dirhash.DefaultHash([]string{"go.mod"}, func(fn string) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewBuffer(append([]byte(nil), goModData...))), nil
	})
	if err != nil {
		return err
	}
	goModHashes[cfg.ModuleId] = mh
	slog.DebugContext(ctx, "go.mod hash", "hash", mh)
	// Create the actual zip.
	zipf, err := os.Create(vb + ".zip")
	if err != nil {
		return err
	}
	defer func() {
		if err := zipf.Close(); retErr == nil {
			retErr = err
		}
	}()
	if err := zip.CreateFromDir(zipf, cfg.XModModuleVersion, zipdir); err != nil {
		return err
	}
	return nil
}

// A FakeGoProxy is a pair of temporary directories intended for use as a [Go module proxy] and
// [module cache], with convenience methods to make them easier to use in tests.
//
// This is meant for testable example functions; test functions should use [TestFakeGoProxy]
// instead.
//
// [Go module proxy]: https://go.dev/ref/mod#module-proxy
// [module cache]: https://go.dev/ref/mod#module-cache
type FakeGoProxy struct {
	modCacheDir string
	proxyDir    string
	dirHashes   map[gmdg.ModuleId]string
	goModHashes map[gmdg.ModuleId]string
}

// NewFakeGoProxy creates a new [FakeGoProxy].  The returned done callback must be called when done
// using the [FakeGoProxy].
func NewFakeGoProxy() (_ *FakeGoProxy, done func() error, retErr error) {
	dones := []func() error(nil)
	done = func() error {
		var retErr error
		for _, fn := range slices.Backward(dones) {
			if err := fn(); retErr == nil {
				retErr = err
			}
		}
		return retErr
	}
	cleanup := func() {
		if err := done(); retErr == nil {
			retErr = err
		}
		done = func() error { return nil }
	}
	defer func() { cleanup() }()
	modCacheDir, modCacheDirDone, err := absTempDir("fakemodule-modCache-")
	if err != nil {
		return nil, done, err
	}
	dones = append(dones, modCacheDirDone)
	proxyDir, proxyDirDone, err := absTempDir("fakemodule-proxyDir-")
	if err != nil {
		return nil, done, err
	}
	dones = append(dones, proxyDirDone)
	gp := &FakeGoProxy{
		modCacheDir: modCacheDir,
		proxyDir:    proxyDir,
		dirHashes:   map[gmdg.ModuleId]string{},
		goModHashes: map[gmdg.ModuleId]string{},
	}
	// Go marks many of the files in the GOMODCACHE directory as read-only, which [os.RemoveAll] fails
	// to delete.
	dones = append(dones, func() error {
		ctx := gp.WithEnv(context.Background())
		return command.New(ctx, "", "go", "clean", "-modcache").Run()
	})
	cleanup = func() {}
	return gp, done, nil
}

// Dir returns the [FakeGoProxy]'s [Go module proxy] directory.
//
// [Go module proxy]: https://go.dev/ref/mod#goproxy-protocol
func (gp *FakeGoProxy) Dir() string {
	return gp.proxyDir
}

// CacheDir returns the [FakeGoProxy]'s [module cache] directory.
//
// [module cache]: https://go.dev/ref/mod#module-cache
func (gp *FakeGoProxy) CacheDir() string {
	return gp.modCacheDir
}

// Env returns [Go environment variable] {name, value} pairs that tell the `go`
// command to:
//
//   - use [FakeGoProxy.Dir] (via `GOPROXY`)
//   - use [FakeGoProxy.CacheDir] (via `GOMODCACHE`)
//   - disable checksum verification (via `GOSUMDB`)
//   - disable VCS downloads (via `GOVCS`)
//
// [Go environment variable]: https://go.dev/ref/mod#environment-variables
func (gp *FakeGoProxy) Env() iter.Seq2[string, string] {
	return maps.All(map[string]string{
		"GOMODCACHE": gp.modCacheDir,
		"GOPROXY":    "file://" + gp.proxyDir,
		"GOSUMDB":    "off",
		"GOVCS":      "*:off",
	})
}

// Environ returns the given environment variables (usually from [os.Environ]) augmented with the
// environment variables from [FakeGoProxy.Env].
func (gp *FakeGoProxy) Environ(env []string) []string {
	for k, v := range gp.Env() {
		env = append(env, k+"="+v)
	}
	return env
}

// Setenv uses [testing.T.Setenv] to temporarily set the environment variables from
// [FakeGoProxy.Env] in the current process.
func (gp *FakeGoProxy) Setenv(t *testing.T) {
	t.Helper()
	for k, v := range gp.Env() {
		t.Setenv(k, v)
	}
}

// WithEnv adds the environment variables from [FakeGoProxy.Env] to the context with key
// [command.EnvKey] for use with [command.New] and friends.
func (gp *FakeGoProxy) WithEnv(ctx context.Context) context.Context {
	return context.WithValue(ctx, command.EnvKey, gp.Environ(os.Environ()))
}

// Add creates a new fake module in the [FakeGoProxy].  It is a convenience wrapper around [Add],
// with the same semantics.
func (gp *FakeGoProxy) Add(ctx context.Context, opts ...Option) error {
	return Add(ctx, gp.proxyDir, gp.dirHashes, gp.goModHashes, opts...)
}

// AddAll is a convenience method to make it easier to add many modules at a time.
func (gp *FakeGoProxy) AddAll(ctx context.Context, optss ...[]Option) error {
	for _, opts := range optss {
		if err := gp.Add(ctx, opts...); err != nil {
			return err
		}
	}
	return nil
}

// AddFromDir reads all *.mod files in the given directory and creates a fake module for each.  See
// [GoMod] for how to specify the module version.
func (gp *FakeGoProxy) AddFromDir(ctx context.Context, dataDir string) (retErr error) {
	r, err := os.OpenRoot(dataDir)
	if err != nil {
		return err
	}
	defer func() {
		if err := r.Close(); retErr == nil {
			retErr = err
		}
	}()
	if smf, err := r.Open("synthmods.txt"); err == nil {
		defer func() {
			if err := smf.Close(); retErr == nil {
				retErr = err
			}
		}()
		scn := bufio.NewScanner(smf)
		for scn.Scan() {
			if err := gp.Add(ctx, ModuleId(gmdg.ParseModuleId(scn.Text())), Synthetic(true)); err != nil {
				return err
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	ents, err := fs.ReadDir(r.FS(), ".")
	for _, e := range ents {
		if e.IsDir() || path.Ext(e.Name()) != ".mod" {
			continue
		}
		goModData, err := r.ReadFile(e.Name())
		if err != nil {
			return err
		}
		if err := gp.Add(ctx, GoModData(goModData)); err != nil {
			return err
		}
	}
	return nil
}

// A TestFakeGoProxy is like [FakeGoProxy] but with a more ergonomic interface meant for unit tests.
type TestFakeGoProxy struct {
	FakeGoProxy
	t *testing.T
}

func NewTestFakeGoProxy(t *testing.T) *TestFakeGoProxy {
	t.Helper()
	gp, done, err := NewFakeGoProxy()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := done(); err != nil {
			t.Fatal(err)
		}
	})
	return &TestFakeGoProxy{
		FakeGoProxy: *gp,
		t:           t,
	}
}

func (gp *TestFakeGoProxy) Add(opts ...Option) *TestFakeGoProxy {
	gp.t.Helper()
	if err := gp.FakeGoProxy.Add(gp.t.Context(), opts...); err != nil {
		gp.t.Fatal(err)
	}
	return gp
}

func (gp *TestFakeGoProxy) AddAll(optss ...[]Option) *TestFakeGoProxy {
	gp.t.Helper()
	if err := gp.FakeGoProxy.AddAll(gp.t.Context(), optss...); err != nil {
		gp.t.Fatal(err)
	}
	return gp
}

func (gp *TestFakeGoProxy) AddFromDir(dataDir string) *TestFakeGoProxy {
	gp.t.Helper()
	if err := gp.FakeGoProxy.AddFromDir(gp.t.Context(), dataDir); err != nil {
		gp.t.Fatal(err)
	}
	return gp
}

func (gp *TestFakeGoProxy) Context() context.Context {
	return gp.WithEnv(gp.t.Context())
}

func fileAppend(fn string, data []byte) (retErr error) {
	f, err := os.OpenFile(fn, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0666)
	if err != nil {
		return err
	}
	defer func() {
		if err := f.Close(); retErr == nil {
			retErr = err
		}
	}()
	_, err = f.Write(data)
	return err
}

func fileSave(fn string, data []byte) error {
	return os.WriteFile(fn, data, 0666)
}

func fileSaveJson(fn string, data any) (retErr error) {
	f, err := os.Create(fn)
	if err != nil {
		return err
	}
	defer func() {
		if err := f.Close(); retErr == nil {
			retErr = err
		}
	}()
	return json.NewEncoder(f).Encode(data)
}

func absTempDir(pattern string) (_ string, done func() error, _ error) {
	done = func() error { return nil }
	d, err := os.MkdirTemp("", pattern)
	if err != nil {
		return "", done, err
	}
	if !filepath.IsAbs(d) {
		panic(fmt.Errorf("temporary directory is not absolute: %v", d))
	}
	return d, func() error { return os.RemoveAll(d) }, nil
}
