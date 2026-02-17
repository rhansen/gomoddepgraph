[![Go Reference](https://pkg.go.dev/badge/github.com/rhansen/gomoddepgraph.svg)](https://pkg.go.dev/github.com/rhansen/gomoddepgraph)

# gomoddepgraph

Go library and command-line utility to examine the dependency graph for a Go module.

[Library API reference documentation](https://pkg.go.dev/github.com/rhansen/gomoddepgraph)

## Features

Dependency resolution implementations:

  * A wrapper around Go's own dependency resolver ([Minimal Version Selection
    (MVS)](https://go.dev/ref/mod#minimal-version-selection) as of Go v1.25).
  * A reimplementation of MVS to support alternative requirement collection modes.
  * A Boolean satisfiability problem (SAT) solver.

Module requirements loader implementations:

  * A wrapper around Go's own requirements loader (which produces a
    [pruned](https://go.dev/ref/mod#graph-pruning) transitive closure).
  * Complete (non-pruned) transitive closure.

Optional "unification" of requirement versions to reduce the size of a requirement graph to
speed up dependency resolution for complex modules.

## Command-Line Utility

To view the manual:

```sh
go run github.com/rhansen/gomoddepgraph/cmd/gomoddepgraph@latest --man
```

Example:

```console
$ go run github.com/rhansen/gomoddepgraph/cmd/gomoddepgraph@latest golang.org/x/net@v0.49.0
golang.org/x/net@v0.49.0
  golang.org/x/crypto@v0.47.0
    golang.org/x/net@v0.49.0 (repeat)
    golang.org/x/sys@v0.40.0
    golang.org/x/term@v0.39.0
      golang.org/x/sys@v0.40.0 (repeat)
  golang.org/x/sys@v0.40.0 (repeat)
  golang.org/x/term@v0.39.0 (repeat)
  golang.org/x/text@v0.33.0
    golang.org/x/mod@v0.31.0 (surprise indirect)
    golang.org/x/sync@v0.19.0 (surprise indirect)
    golang.org/x/tools@v0.40.0
```

## Copyright and License

Copyright © 2026 Richard Hansen &lt;[rhansen@rhansen.org](mailto:rhansen@rhansen.org)&gt; and
contributors.

Licensed under the [MIT/Expat license](LICENSE).
