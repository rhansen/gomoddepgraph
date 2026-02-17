// Package gomoddepgraph contains functions for examining the dependency graph of a Go module.
//
// # Quick Start
//
// (The following is also available as a package-level example.)
//
// Construct a [ModuleId] identifying the root node, perhaps via [ParseModuleId]:
//
//	rootId := gomoddepgraph.ParseModuleId("github.com/rhansen/gomoddepgraph@latest")
//
// Use [ResolveVersion] to resolve a [version query] to the actual version:
//
//	ctx := context.Background()
//	rootId, err := gomoddepgraph.ResolveVersion(ctx, rootId)
//	if err != nil {
//		return err
//	}
//
// Construct a [RequirementGraph] using the root module's identifier:
//
//	rg, err := gomodddepgraph.RequirementsGo(ctx, rootId)
//	if err != nil {
//		return err
//	}
//
// Resolve the [RequirementGraph] to a [DependencyGraph]:
//
//	dg, err := gomoddepgraph.ResolveGo(ctx, rg)
//	if err != nil {
//		return err
//	}
//
// You can use [AllDependencies] to get the selected set of [Dependency] objects:
//
//	selected := slices.Collect(gomoddepgraph.AllDependencies(dg))
//
// Or you can use [WalkDependencyGraph] to visit the nodes and edges of the [DependencyGraph]:
//
//	err := gomoddepgraph.WalkDependencyGraph(dg, dg.Root(),
//		func(m gomoddepgraph.Dependency) (bool, error) {
//			fmt.Printf("visited node %v\n", m)
//			return true, nil
//		},
//		func(p, m gomoddepgraph.Dependency, surprise bool) error {
//			fmt.Printf("visited edge %v -> %v (surprise: %v)\n", p, m, surprise)
//			return nil
//		})
//	if err != nil {
//		return err
//	}
//
// Or you can manually walk the graph:
//
//	seen := mapset.NewThreadUnsafeSet(dg.Root())
//	q := []gomoddepgraph.Dependency{dg.Root()}
//	for len(q) > 0 {
//		m := q[0]
//		q = q[1:]
//		fmt.Printf("manually visited node %v\n", m)
//		for d := range gomoddepgraph.Deps(dg, m) {
//			if seen.Add(d) {
//				q = append(q, d)
//			}
//		}
//	}
//
// # Introduction
//
// The requirements of a Go module are listed in its go.mod file.  Transitively, these form a
// requirement graph.  (The requirement graph is directed and almost always acyclic.)  Go resolves a
// subgraph of this requirement graph into a selection of dependencies that collectively satisfy the
// [main module]'s requirements and the selected modules' own requirements.  The selection set is
// what Go downloads and compiles when building and testing the [main module].
//
// It is easy to examine the requirement graph, but due to the particulars of Go's resolution
// algorithm it is more difficult to examine the relationships between the modules in the selection
// of resolved dependencies.
//
// The goal of this package is to provide a meaningful way to convert a selection of dependencies
// into a graph (directed, and frequently cyclic) that resembles the requirement graph so that the
// relationships between the selected dependencies can be examined.
//
// The original motivation for creating this package was to facilitate the creation of a minimal set
// of Debian packages, each containing one Go module, that can be used as build dependencies of
// other Debian packages.  See the section "Meshing the Go Resolver With Debian Package
// Dependencies" below.
//
// The rest of this package-level documentation is mostly a collection of things I learned about Go
// that I thought were non-obvious from Go's own documentation, but it also serves as background for
// why the API is designed the way it is.
//
// # Terminology
//
// This package's documentation uses the following terminology, intended to align with Go's own
// usage:
//
//   - The [main module] is the module that owns the current working directory when the go command
//     is invoked.  This module is the root of the requirement graph, so "root module" is a synonym.
//   - An immediate requirement is a {module path, module version} pair listed as a requirement in a
//     module's go.mod (regardless of whether the requirement has an `// indirect` comment or not).
//     The version half of the pair indicates the minimum acceptable version; any greater version is
//     also acceptable.  (A different major version number indicates incompatibility, but different
//     major versions also have different module paths due to a required [major version suffix] so
//     they appear to Go as unrelated modules.)
//   - A direct requirement is an immediate requirement that does not have an `// indirect` comment
//     in go.mod.
//   - An immediate indirect requirement is an immediate requirement that has an `// indirect`
//     comment in go.mod.
//   - A [direct dependency] is a module at a specific version that was selected to satisfy a direct
//     requirement.
//   - An [indirect dependency] is either: (1) a module at a specific version that was selected to
//     satisfy an immediate indirect requirement, or (2) a dependency (direct or indirect) of a
//     direct dependency.
//
// In addition, this documentation introduces the following terminology that does not appear in Go's
// documentation:
//
//   - A surprise dependency is a dependency that satisfies an immediate indirect requirement but is
//     not a dependency of a direct dependency.  See the "Surprise Dependencies" section below for
//     details.
//   - A synthetic module is collection of non-module packages (packages without a corresponding
//     go.mod) that is automatically converted to a Go module by a Go [module proxy].  Conversion is
//     performed by synthesizing a basic go.mod with no requirements (even if packages in the
//     synthetic module import outside packages).  This is a backwards compatibility feature that
//     allows a Go module to declare a requirement on code that was published before the Go module
//     functionality was added (in Go v1.11 to v1.15).
//   - A synthesized indirect requirement is an immediate indirect requirement added to support a
//     synthetic module.  (The synthetic module must be a direct requirement.)  The synthesized
//     indirect requirement ensures that Go selects a module that supplies a package that is
//     imported by the synthetic module.
//
// For more information about synthetic modules and synthesized indirect requirements, see the
// "Interoperability With Non-Modules" section below.
//
// # Go Dependency Resolver Behavior
//
// To resolve requirements to dependencies, Go (as of v1.25) does the following:
//
//  1. Construct a graph of module requirements.  Each node in the graph is a particular module at a
//     specific version.  The edges are the immediate requirements expressed in each module's go.mod
//     file.  An `// indirect` comment after a go.mod requirement has no effect on the graph
//     construction (those comments are ignored for this purpose).
//  2. [Prune some of the edges from the graph].  The pruning algorithm is not explained here.
//  3. Run the [Minimal Version Selection (MVS) algorithm] on the resulting pruned graph, rooted at
//     the [main module].
//
// The MVS algorithm is quite unique.  Most other systems, such as Debian's APT, select the newest
// available compatible version; MVS selects the oldest version that still satisfies every
// requirement in the transitive closure of [pruned] requirements.  While MVS has some nice
// properties, it complicates packaging in traditional package management systems such as Debian's
// APT.  In particular:
//
//   - A dependency module's own set of dependencies is undefined except in the context of a
//     particular [main module].  In other words, a dependency module's own dependencies might
//     change with a different dependent module.
//   - Dependency cycles are common.
//   - Overselection (unnecessary dependencies treated as necessary) is common.
//
// For example, suppose the following immediate requirements without any pruning:
//
//	executable x1 requires:
//	  - library y at v1.1.0 or newer
//	executable x2 requires:
//	  - library y at v1.1.0 or newer
//	  - library z at v1.12.9 or newer
//	library y at v1.1.0 requires:
//	  - library z at v1.0.0 or newer
//	library z at v1.0.0 requires:
//	  - library a
//	library z at v1.12.9 requires:
//	  - library b
//	library a requires library y at v1.0.0 or newer
//	library b has no requirements
//
// Go will build `x1` with modules `y@v1.1.0`, `z@v1.0.0` and `a` (even though `z@v1.12.9` is known
// to be available), but it will build executable `x2` with modules `y@v1.1.0`, `z@v1.12.9` and `b`.
//
// The above example requirement graph does not have a requirement cycle—note that library `a`
// requires `y@v1.0.0`, not `y@v1.1.0`.  However, the selected dependencies for `x1` do form a
// dependency cycle: `y@v1.1.0` to `z@v1.0.0` to `a` to `y@v1.1.0`.  Go permits module dependency
// cycles as long as the package imports do not form a cycle.
//
// MVS tends to overselect.  In the above example requirement graph, `a` is reachable from `x2` so
// MVS will select—and thus Go would download—module `a` when building `x2` even though it is not
// actually used to compile `x2`.  Similarly, overselection of immediate indirect requirements is
// common because MVS often satisfies a direct requirement with a version greater than strictly
// required, and the newer version has a different set of requirements than the version used to
// populate the set of immediate indirect requirements in go.mod.
//
// Overselection could be reduced by eliminating `// indirect` entries from go.mod and with other
// changes to the MVS algorithm, but:
//
//   - Permitting overselection avoids turning dependency resolution into an NP-complete problem.
//     (See [Russ Cox's excellent blog post] for details.)
//   - Permitting overselection ensures consistent dependency selection.  Ignoring some requirements
//     because they are deemed unnecessary risks different outcomes depending on which edges of the
//     requirement graph are traversed first or which metrics are minimized.
//   - Including an `// indirect` requirement for every module that contributes to the build reduces
//     the number of go.mod files downloaded and processed when building a module.
//   - `// indirect` requirements make it possible for a module to override a requirement's
//     requirement; i.e., to bump the version of an indirect dependency.
//   - `// indirect` requirements permit interoperability with legacy Go packages that are not
//     published in a module.
//
// That last point is explained in more detail in the next section.
//
// Besides MVS, a module's dependencies can change for other reasons:
//
//   - There are some go.mod directives that affect the requirement graph but only take effect when
//     the module is the [main module] (specifically, [replace] and [exclude]).
//   - Building a module as a dependency introduces another hop in the requirement graph
//     vs. building the module as the [main module].  This extra hop might affect the output of Go's
//     [graph pruning] algorithm.
//
// # Interoperability With Non-Modules
//
// Go modules were introduced in Go v1.11 and declared production-ready in Go v1.15.  Before
// modules, packages were simply published to a version control repository.  There was no formal way
// to express dependency version requirements, which caused problems.
//
// For interoperability with old code, a Go module can depend on a legacy non-module package.  Go
// accomplishes this by synthesizing a module whose module path matches the legacy package's version
// control repository root.  See [Compatibility with non-module repositories] for details.  The
// synthetic module contains all packages in the repository, except for packages belonging to any
// "real" modules that might exist in subdirectories of the repository.  The synthetic module is
// added as a requirement in the dependent's go.mod just like any other requirement.
//
// The synthesized go.mod does not list any requirements, even if a package in the synthetic module
// imports a package from outside the synthetic module.  No requirements are added to the
// synthesized go.mod because there is no way for Go to determine which specific versions of the
// dependencies are actually required by the synthetic module.  Instead, the responsibility for
// declaring the synthetic module's requirements is moved to the module that depends on the
// synthetic module.
//
// The dependent module adds the synthetic module's requirements to its own go.mod.  This package
// calls such requirements "synthesized indirect requirements", and the modules selected to satisfy
// them "synthesized indirect dependencies".  Go marks synthesized indirect requirements the same as
// normal (non-synthesized) immediate indirect requirements (with an `// indirect` comment).
// However, unlike normal `// indirect` requirements, it is not safe to assume that the synthesized
// requirement will appear as a direct requirement elsewhere in the requirement graph.
//
// Synthesized indirect dependencies are examples of "surprise" dependencies; see the "Surprise
// Dependencies" section below.
//
// # Surprise Dependencies
//
// A surprise dependency is a dependency that satisfies an immediate indirect requirement (a
// requirement in go.mod marked with the special `// indirect` comment) but the dependency is not
// also a dependency of a direct dependency.  By definition, an indirect requirement is not directly
// required by the module itself, so the fact that the dependency does not appear as a dependency of
// another dependency is surprising.
//
// Surprise dependencies can appear for several reasons, including:
//
//   - The surprise dependency is needed to satisfy a requirement of a direct requirement, but the
//     direct requirement's own requirements have been [pruned] from the requirement graph.
//   - The developer forgot to run `go mod tidy` after adding a direct requirement.  (The `go get`
//     command adds an `// indirect` comment to new requirements; `go mod tidy` removes that comment
//     if the requirement is direct.)
//   - One of the module's other dependencies is newer than the corresponding requirement (due to a
//     requirement for the newer version elsewhere in the requirement graph), and the older required
//     version depends on the surprise dependency but the newer selected version does not.
//   - A direct dependency is a synthetic module and the surprise dependency was selected to satisfy
//     a synthesized indirect requirement for that synthetic module.  See [Compatibility with
//     non-module repositories].
//   - The surprise dependency provides or is needed by a [tool].  (The `go get -tool` command marks
//     a tool's module as an indirect requirement and the `go mod tidy` command keeps it marked as
//     indirect.)
//
// # Go Dependency Resolver Interfaces
//
// Go provides three primary ways to collect module dependency information:
//
//   - Run `go mod graph`.  This prints the [pruned] graph of requirements that is used as input to
//     Go's [MVS algorithm].  [RequirementsGo] is built around this.
//   - Run `go list -m all`.  This prints the module's resolved dependencies; i.e., the output of
//     the [MVS algorithm] on the [pruned] graph of requirements.  [ResolveGo] is built around this.
//   - Parse go.mod directly.  This provides complete but low-level access to the requirements.
//     Package [golang.org/x/mod/modfile] makes this easier.  [RequirementsComplete] is built around
//     this.
//
// The outputs of the `go mod graph` and `go list -m all` commands are only perfectly complete when
// the [main module] is a final executable, not a library intended to be used as a dependency. This
// is due to (a) go.mod directives that are only active when the module is the [main module], (b)
// graph pruning, and (c) MVS.  That being said, running `go list -m all` in a dependency module is
// likely to output a similar selection compared to that dependency's contribution to the output of
// `go list -m all` in a dependent module.
//
// But there can be differences, which is why Go does not provide a straightforward way to list an
// arbitrary dependency's own dependencies.  There simply is no clearly defined answer except in the
// context of a particular [main module].  The root node of a [DependencyGraph] from this package is
// that context.
//
// # Package Query `all` vs. Module Query `all`
//
// There is a significant difference between `go list all` and `go list -m all`.  The former queries
// package dependencies; the latter queries module dependencies.  The latter is a superset of the
// modules containing the packages in the former.  In detail:
//
//   - Since Go v1.17, the `all` package pattern (e.g., `go list all`) matches only the Go packages
//     that are transitively imported by a Go package in the [main module].  Thus, dependencies of
//     tests of dependencies are not included (unless otherwise needed), and a subset of a
//     dependency module's packages (and their dependencies) might not match.
//
//   - Since Go v1.17, the `all` module pattern (e.g., `go list -m all`) matches the MVS selection
//     over the transitive closure of requirements, including dependencies of tests of dependencies,
//     except some modules are [pruned from the requirement graph] before MVS selection.  The
//     modules matching `all` is a superset of the modules actually needed to build and test the Go
//     packages in the [main module], but might differ from the MVS selection over the complete
//     (non-pruned) transitive closure.
//
// The `go list -m all` and `go mod graph` commands only examine go.mod.  The `go list all` command
// only examines imports.
//
// # Meshing the Go Resolver With Debian Package Dependencies
//
// To perfectly support Go's resolver, which can select different versions of the same module when
// building different executables, Debian would need to be able to publish multiple versions of the
// same module at the same time.  This could be done by doing one of the following:
//
//   - Embed the complete version in the Debian package name.  Using the example above, the module
//     `y@v1.12.9` would be packaged as `golang-y-v1.12.9-dev_1.12.9-1_all.deb`.  This approach
//     would require the Debian package for each executable to express the entire MVS set in its
//     Build-Depends.
//
//   - Provide multiple versions of a module in a single Debian package.  For example,
//     `golang-y-dev` could install `y@v1.0.0` to `$GOMODCACHE/y@v1.0.0` and `y@v1.1.0` to
//     `$GOMODCACHE/y@v1.1.0`.  This might be difficult for users to understand and tricky to
//     manage.  (What should the Debian package's version be?  Are there multiple orig tarballs?
//     How should the upstream repository's history be merged into the Salsa repository?)
//
// Instead of perfectly supporting Go's resolver, the Debian Go team only packages the newest
// version of a module (per major version) and forces every dependant module to use that one
// version.  Unfortunately, this means that the resulting compiled binaries are unlikely to 100%
// match what upstream has tested.  This is not expected to be a problem in practice.  In the rare
// case the divergence does matter, the MVS-selected versions can be vendorized.
//
// [major version suffix]: https://go.dev/ref/mod#major-version-suffixes
// [main module]: https://go.dev/ref/mod#glos-main-module
// [module proxy]: https://go.dev/ref/mod#module-proxy
// [Prune some of the edges from the graph]: https://go.dev/ref/mod#graph-pruning
// [pruned]: https://go.dev/ref/mod#graph-pruning
// [graph pruning]: https://go.dev/ref/mod#graph-pruning
// [Minimal Version Selection (MVS) algorithm]: https://go.dev/ref/mod#minimal-version-selection
// [MVS algorithm]: https://go.dev/ref/mod#minimal-version-selection
// [pruned from the requirement graph]: https://go.dev/ref/mod#graph-pruning
// [Russ Cox's excellent blog post]: https://research.swtch.com/vgo-mvs
// [direct dependency]: https://go.dev/ref/mod#glos-direct-dependency
// [indirect dependency]: https://go.dev/ref/mod#glos-indirect-dependency
// [replace]: https://go.dev/ref/mod#go-mod-file-replace
// [exclude]: https://go.dev/ref/mod#go-mod-file-exclude
// [version query]: https://go.dev/ref/mod#version-queries
// [Compatibility with non-module repositories]: https://go.dev/ref/mod#non-module-compat
// [tool]: https://go.dev/doc/modules/managing-dependencies#tools
package gomoddepgraph
