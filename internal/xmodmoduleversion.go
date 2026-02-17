package internal

import "golang.org/x/mod/module"

// An XModModuleVersion is a [module.Version].  It exists only to give the
// [github.com/rhansen/gomoddepgraph.ModuleId.XModModuleVersion] [embedded field] a name other than
// "Version" so that the [module.Version.Version] field can be [promoted] to
// [github.com/rhansen/gomoddepgraph.ModuleId.Version].
//
// [embedded field]: https://go.dev/ref/spec#Struct_types
// [promoted]: https://go.dev/ref/spec#Struct_types
type XModModuleVersion = module.Version
