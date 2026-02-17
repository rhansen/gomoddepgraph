package gomoddepgraph

import (
	"fmt"
)

// A Requirement is a node in a [RequirementGraph].  It represents the path and version that would
// be in a go.mod [require directive].  Every type that implements [Requirement] is [comparable],
// with equality only semantically meaningful when compared with another [Requirement] from the same
// [RequirementGraph].
//
// [require directive]: https://go.dev/ref/mod#go-mod-file-require
// [comparable]: https://go.dev/ref/spec#Comparison_operators
type Requirement interface {
	// Id returns the required module's path and minimum acceptable version.
	Id() ModuleId
	fmt.Stringer
}

// RequirementCompare is used to sort a collection of [Requirement] objects.  It returns the return
// value of [ModuleIdCompare] applied to the [ModuleId] identifiers returned from the
// [Requirement.Id] method on the given [Requirement] objects.
func RequirementCompare(a, b Requirement) int {
	return ModuleIdCompare(a.Id(), b.Id())
}

type requirement struct {
	ModuleId
}

var _ Requirement = requirement{}

func (r requirement) Id() ModuleId {
	return r.ModuleId
}
