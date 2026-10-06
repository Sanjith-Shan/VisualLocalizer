//go:build !vloc

package vloc

// NewCore returns nil: the binary was built without the vloc tag.
func NewCore() Engine { return nil }
