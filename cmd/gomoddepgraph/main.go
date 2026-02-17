package main

import (
	"bytes"
	"context"
	_ "embed"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"maps"
	"os"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/amterp/color"
	mapset "github.com/deckarep/golang-set/v2"
	gmdg "github.com/rhansen/gomoddepgraph"
	"github.com/rhansen/gomoddepgraph/internal/command"
	"github.com/rhansen/gomoddepgraph/internal/logging"
)

//go:embed gomoddepgraph.1.in
var man []byte

var (
	cyanf    = color.New(color.FgCyan).SprintfFunc()
	hicyanf  = color.New(color.FgHiCyan).SprintfFunc()
	hiblackf = color.New(color.FgHiBlack).SprintfFunc()
)

type getReqsFn = func(ctx context.Context, rootId gmdg.ModuleId) (gmdg.RequirementGraph, error)
type resolveDepsFn = func(ctx context.Context, rg gmdg.RequirementGraph) (gmdg.DependencyGraph, error)
type outputFn = func(ctx context.Context, sel gmdg.DependencyGraph) error

type config struct {
	mods        []string
	getReqs     *getReqsFn
	unify       bool
	resolveDeps *resolveDepsFn
	output      *outputFn
}

func ver() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok || bi.Main.Version == "(devel)" {
		return ""
	}
	return bi.Main.Version
}

func showMan(ctx context.Context) error {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return fmt.Errorf("failed to fetch Go build information")
	}
	date := ""
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.time":
			when, err := time.Parse(time.RFC3339, s.Value)
			if err != nil {
				return fmt.Errorf("failed to parse vcs.time %q: %w", s.Value, err)
			}
			date = when.Format(time.DateOnly)
		}
	}
	man := bytes.ReplaceAll(man, []byte("%DATE%"), []byte(date))
	man = bytes.ReplaceAll(man, []byte("%VERSION%"), []byte(ver()))
	cmd := command.New(ctx, ".", "man", "-l", "-")
	cmd.Stdin = bytes.NewBuffer(man)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("man failed: %w", err)
	}
	return nil
}

var allGetReqsFuncs = [...]getReqsFn{
	gmdg.RequirementsGo,
	getReqsComplete,
}

var allGetReqs = map[string]*getReqsFn{
	"go":       &allGetReqsFuncs[0],
	"complete": &allGetReqsFuncs[1],
}

func getReqsComplete(ctx context.Context, rootId gmdg.ModuleId) (gmdg.RequirementGraph, error) {
	rg, _, err := gmdg.RequirementsComplete(ctx, rootId)
	return rg, err
}

var allResolveDepsFuncs = [...]resolveDepsFn{
	gmdg.ResolveGo,
	gmdg.ResolveMvs,
	gmdg.ResolveSat,
}

var allResolveDeps = map[string]*resolveDepsFn{
	"go":  &allResolveDepsFuncs[0],
	"mvs": &allResolveDepsFuncs[1],
	"sat": &allResolveDepsFuncs[2],
}

var allOutputFuncs = [...]outputFn{
	outputTree,
	outputRaw,
	outputDot,
}

var allOutput = map[string]*outputFn{
	"tree": &allOutputFuncs[0],
	"raw":  &allOutputFuncs[1],
	"dot":  &allOutputFuncs[2],
}

func outputTree(ctx context.Context, dg gmdg.DependencyGraph) error {
	surpriseMsg := hicyanf(" (surprise indirect)")
	surpriseSeenMsg := cyanf(" (surprise indirect)")
	seenMsg := hiblackf(" (repeat)")
	seen := mapset.NewSet[gmdg.Dependency]()
	var visit func(m gmdg.Dependency, surprise bool, indent int) error
	visit = func(m gmdg.Dependency, surprise bool, indent int) error {
		wasSeen := !seen.Add(m)
		fmt.Print(strings.Repeat("  ", indent))
		switch {
		case !wasSeen && !surprise:
			fmt.Print(m)
		case !wasSeen && surprise:
			fmt.Printf("%v%s", m, surpriseMsg)
		case wasSeen && !surprise:
			fmt.Printf("%s%s", hiblackf("%v", m), seenMsg)
		case wasSeen && surprise:
			fmt.Printf("%s%s%s", hiblackf("%v", m), seenMsg, surpriseSeenMsg)
		}
		fmt.Print("\n")
		if !wasSeen {
			deps := maps.Collect(gmdg.Deps(dg, m))
			for _, d := range slices.SortedFunc(maps.Keys(deps), gmdg.DependencyCompare) {
				if err := visit(d, deps[d], indent+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return visit(dg.Root(), false, 0)
}

func outputRaw(ctx context.Context, dg gmdg.DependencyGraph) error {
	for _, dep := range slices.SortedFunc(gmdg.AllDependencies(dg), gmdg.DependencyCompare) {
		fmt.Printf("%v\n", dep)
	}
	return nil
}

func outputDot(ctx context.Context, dg gmdg.DependencyGraph) error {
	printEdge := func(from, to gmdg.Dependency, surprise bool) {
		attrs := []string{}
		if surprise {
			attrs = append(attrs, "class=\"surprise\"", "style=\"dashed\"")
		}
		fmt.Printf("  %q -> %q [%s];\n", from, to, strings.Join(attrs, ","))
	}
	visited := mapset.NewSet[gmdg.Dependency]()
	var visit func(m gmdg.Dependency) error
	visit = func(m gmdg.Dependency) error {
		if !visited.Add(m) {
			return nil
		}
		attrs := []string{fmt.Sprintf("URL=\"https://pkg.go/dev/%v\"", m)}
		if m == dg.Root() {
			attrs = append(attrs, "fillcolor=\"black\"", "fontcolor=\"white\"")
		}
		fmt.Printf("  %q [%s];\n", m, strings.Join(attrs, ","))
		ds := maps.Collect(gmdg.Deps(dg, m))
		for _, d := range slices.SortedFunc(maps.Keys(ds), gmdg.DependencyCompare) {
			printEdge(m, d, ds[d])
			if err := visit(d); err != nil {
				return err
			}
		}
		return nil
	}
	fmt.Print("digraph {\n")
	fmt.Print("  outputorder= \"edgesfirst\";\n")
	fmt.Print("  overlap = prism;\n")
	fmt.Print("  overlap_scaling = -10;\n")
	fmt.Print("  node [style=filled,fillcolor=\"white\",shape=box];\n")
	if err := visit(dg.Root()); err != nil {
		return err
	}
	fmt.Print("}\n")
	return nil
}

func run(ctx context.Context, cfg *config, mod string) error {
	mId := gmdg.ParseModuleId(mod)
	if err := mId.Check(); err != nil {
		if mId, err = gmdg.ResolveVersion(ctx, mId); err != nil {
			return err
		}
	}
	rg, err := (*cfg.getReqs)(ctx, mId)
	if err != nil {
		return err
	}
	if cfg.unify {
		rg, err = gmdg.UnifyRequirements(ctx, rg)
		if err != nil {
			return err
		}
	}
	dg, err := (*cfg.resolveDeps)(ctx, rg)
	if err != nil {
		return err
	}
	return (*cfg.output)(ctx, dg)
}

var slogLevel = func() *slog.LevelVar {
	lvl := &slog.LevelVar{}
	lvl.Set(logging.LevelInfo)
	h := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})
	slog.SetDefault(slog.New(h))
	return lvl
}()

func choiceFlag[T any](p *T, name string, choices map[string]T, dflt string, post func(string) error, usage string) {
	cstr := strings.Join(slices.Sorted(maps.Keys(choices)), ", ")
	var ok bool
	if *p, ok = choices[dflt]; !ok {
		panic(fmt.Errorf("invalid default for %v option: %v", dflt, name))
	}
	usage += fmt.Sprintf(" (one of: %v; default: %v)", cstr, dflt)
	flag.Func(name, usage, func(arg string) error {
		if arg == "" {
			arg = dflt
		}
		v, ok := choices[arg]
		if !ok {
			return fmt.Errorf("expected one of: %v", cstr)
		}
		*p = v
		if post != nil {
			return post(arg)
		}
		return nil
	})
}

func parseFlags(ctx context.Context) *config {
	cfg := &config{}

	bumpLogLevel := func(lower bool) {
		slog.Debug("log level pre-change", "level", slogLevel.Level())
		slogLevel.Set(logging.BumpLevel(slogLevel.Level(), lower))
		slog.Debug("log level post-change", "level", slogLevel.Level())
	}
	setLogLevel := func(arg string) error {
		lvl, err := logging.StringToLevel(arg)
		if err != nil {
			return err
		}
		slogLevel.Set(lvl)
		return nil
	}
	flag.BoolFunc("v", "Increase log verbosity.", func(arg string) error {
		switch arg {
		case "", "true":
			bumpLogLevel(true)
		default:
			return setLogLevel(arg)
		}
		return nil
	})
	flag.BoolFunc("q", "Decrease log verbosity.", func(arg string) error {
		switch arg {
		case "", "true":
			bumpLogLevel(false)
		default:
			return setLogLevel(arg)
		}
		return nil
	})

	colorChoices := map[string]bool{
		"auto":   color.NoColor,
		"never":  true,
		"always": false,
	}
	choiceFlag(&color.NoColor, "color", colorChoices, "auto", nil,
		"Output colors according to `mode`.")
	choiceFlag(&cfg.getReqs, "requirements", allGetReqs, "go",
		func(_ string) error {
			if cfg.getReqs != allGetReqs["go"] && cfg.resolveDeps == allResolveDeps["go"] {
				cfg.resolveDeps = allResolveDeps["mvs"]
			}
			return nil
		},
		"Generate the requirement graph using the algorithm indicated by `mode`.  Implies '--resolver=mvs' if given a non-'go' mode and the resolver is currently 'go'.")
	flag.BoolFunc("u",
		"Unify requirement versions before resolving.  Implies '--resolver=mvs' if the resolver is currently 'go'.",
		func(_ string) error {
			cfg.unify = true
			if cfg.resolveDeps == allResolveDeps["go"] {
				cfg.resolveDeps = allResolveDeps["mvs"]
			}
			return nil
		})
	choiceFlag(&cfg.resolveDeps, "resolver", allResolveDeps, "go", nil,
		"Resolve dependencies using the algorithm indicated by `mode`.")
	choiceFlag(&cfg.output, "format", allOutput, "tree", nil,
		"Print dependencies according to `mode`.")
	flag.BoolFunc("man", "Show the usage manual and exit.", func(_ string) error {
		if err := showMan(ctx); err != nil {
			log.Fatal(err)
		}
		os.Exit(0)
		return nil
	})
	help := func(string) error {
		// Pet peeve: Help output should be written to standard output, not standard error, when the
		// user explicitly requests the help.  This makes it easier for them to pipe the help output to
		// a pager.
		flag.CommandLine.SetOutput(os.Stdout)
		flag.Usage()
		os.Exit(0)
		return nil
	}
	helpUsage := "Print usage information and exit."
	flag.BoolFunc("h", helpUsage, help)
	flag.BoolFunc("help", helpUsage, help)
	flag.BoolFunc("version", "Print the version and exit.", func(string) error {
		v := ver()
		if v == "" {
			log.Fatal("the Go build information is unavalable; try passing the \"-buildvcs=true\" build option to go")
		}
		fmt.Printf("%s\n", v)
		os.Exit(0)
		return nil
	})
	flag.Parse()
	if cfg.resolveDeps == allResolveDeps["go"] {
		if cfg.getReqs != allGetReqs["go"] {
			log.Fatal("the go dependency resolver requires the go requirements collector")
		}
		if cfg.unify {
			log.Fatal("the -u option cannot be used in combination with the go resolver")
		}
	}
	cfg.mods = flag.Args()
	if len(cfg.mods) != 1 {
		log.Fatal("exactly one root module is required")
	}
	return cfg
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := parseFlags(ctx)
	for _, mod := range cfg.mods {
		if err := run(ctx, cfg, mod); err != nil {
			slog.ErrorContext(ctx, "failed", "error", err)
			os.Exit(1)
		}
	}
}
