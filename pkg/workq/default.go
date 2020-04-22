package workq

// A default registry that other packages can easily register their types
// against.
var GlobalRegistry Registry

// Register a job and handler with the default registry.
func Register(jobType string, h interface{}) {
	GlobalRegistry.Register(jobType, h)
}
