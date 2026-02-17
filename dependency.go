package gomoddepgraph

import (
	"fmt"
)

// A Dependency is a node in a [DependencyGraph].  It represents a Go module that satisfies one or
// more requirements in a [RequirementGraph].  Every type that implements [Dependency] is
// [comparable], with equality only semantically meaningful when compared with another [Dependency]
// from the same [DependencyGraph].
//
// [comparable]: https://go.dev/ref/spec#Comparison_operators
type Dependency interface {
	// Id returns the module's path and selected version.
	Id() ModuleId
	fmt.Stringer
}

// DependencyCompare is used to sort a collection of [Dependency] objects.  It returns the return
// value of [ModuleIdCompare] applied to the [ModuleId] identifiers returned from the
// [Dependency.Id] method on the given [Dependency] objects.
func DependencyCompare(a, b Dependency) int {
	return ModuleIdCompare(a.Id(), b.Id())
}

type dependency struct {
	ModuleId
}

var _ Dependency = dependency{}

func (d dependency) Id() ModuleId {
	return d.ModuleId
}
