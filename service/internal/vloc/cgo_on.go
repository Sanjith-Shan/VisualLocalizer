//go:build vloc

package vloc

func init() { Available = true }

// NewCore returns the cgo engine.
func NewCore() Engine { return CgoEngine{} }
