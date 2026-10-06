// Package vloc is the Go view of the localization core's C ABI (core/include/vloc.h).
//
// Two engines implement it. The cgo engine (build tag vloc) links libvloc. The fake
// engine is deterministic and pure Go, for tests and for load testing the service
// layer without the core.
package vloc

import (
	"errors"
	"fmt"
	"math"
)

// Intrinsics are pinhole camera parameters in pixels.
type Intrinsics struct {
	Fx, Fy, Cx, Cy float64
}

// Validate rejects intrinsics that cannot describe a camera for an image of w x h.
func (k Intrinsics) Validate(w, h int) error {
	for _, v := range []float64{k.Fx, k.Fy, k.Cx, k.Cy} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return errors.New("intrinsics must be finite")
		}
	}
	if k.Fx <= 0 || k.Fy <= 0 {
		return errors.New("fx and fy must be positive")
	}
	if k.Fx > 100*float64(max(w, h)) || k.Fy > 100*float64(max(w, h)) {
		return errors.New("focal length is implausibly large for the image size")
	}
	if k.Cx <= 0 || k.Cx >= float64(w) || k.Cy <= 0 || k.Cy >= float64(h) {
		return fmt.Errorf("principal point (%g, %g) must lie inside the %dx%d image", k.Cx, k.Cy, w, h)
	}
	return nil
}

// Result mirrors vloc_result.
type Result struct {
	OK                  bool
	Qw, Qx, Qy, Qz      float64
	Tx, Ty, Tz          float64
	NumKeypoints        int
	NumMatches          int
	NumInliers          int
	MsDecode, MsExtract float64
	MsMatch, MsPose     float64
	Err                 string
}

// CoreMs is the core's own wall time summed over its stages.
func (r Result) CoreMs() float64 { return r.MsDecode + r.MsExtract + r.MsMatch + r.MsPose }

// MapInfo mirrors vloc_map_info.
type MapInfo struct {
	NumPoints     int    `json:"num_points"`
	DescriptorDim int    `json:"descriptor_dim"`
	Feature       string `json:"feature"`
	Name          string `json:"name"`
}

// Map is one loaded map. Localize must be safe for concurrent use. Close frees it and
// must be called exactly once, after every Localize on it has returned.
type Map interface {
	Localize(img []byte, k Intrinsics) (Result, error)
	Info() MapInfo
	Close()
}

// Engine loads maps from files.
type Engine interface {
	Name() string
	// CheckHeader validates the first bytes of a map file before the whole file is
	// loaded, so a wrong upload fails fast with a clear reason.
	CheckHeader(head []byte) error
	// CheckSize validates the full file size against the header, after upload.
	CheckSize(head []byte, size int64) error
	Load(path string) (Map, error)
}

// HeaderLen is how many leading bytes CheckHeader wants to see.
const HeaderLen = 184

// Available reports whether the cgo engine was compiled in (build tag vloc).
var Available = false
