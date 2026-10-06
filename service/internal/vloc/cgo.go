//go:build vloc

package vloc

/*
#cgo CFLAGS: -I${SRCDIR}/../../../core/include
#cgo LDFLAGS: -L${SRCDIR}/../../../core/build -lvloc -Wl,-rpath,${SRCDIR}/../../../core/build
#include <stdlib.h>
#include "vloc.h"
*/
import "C"

import (
	"errors"
	"os"
	"runtime"
	"unsafe"
)

// CgoEngine links libvloc (core/build/libvloc.dylib, see docs/CORE_READY.md).
type CgoEngine struct{}

func (CgoEngine) Name() string { return "cgo" }

func (CgoEngine) CheckHeader(head []byte) error { return checkCoreHeader(head) }

func (CgoEngine) CheckSize(head []byte, size int64) error { return checkCoreSize(head, size) }

func (CgoEngine) Load(path string) (Map, error) {
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))
	var errbuf [512]C.char
	var m *C.vloc_map
	if rc := C.vloc_map_load(cpath, &m, &errbuf[0], C.size_t(len(errbuf))); rc != 0 || m == nil {
		msg := C.GoString(&errbuf[0])
		if msg == "" {
			msg = "vloc_map_load failed"
		}
		if _, err := os.Stat(path); err != nil {
			return nil, err
		}
		return nil, errors.New(msg)
	}
	var ci C.vloc_map_info
	C.vloc_map_get_info(m, &ci)
	return &cgoMap{m: m, info: MapInfo{
		NumPoints:     int(ci.num_points),
		DescriptorDim: int(ci.descriptor_dim),
		Feature:       C.GoString(&ci.feature[0]),
		Name:          C.GoString(&ci.name[0]),
	}}, nil
}

type cgoMap struct {
	m    *C.vloc_map
	info MapInfo
}

func (c *cgoMap) Info() MapInfo { return c.info }

func (c *cgoMap) Close() { C.vloc_map_free(c.m); c.m = nil }

func (c *cgoMap) Localize(img []byte, k Intrinsics) (Result, error) {
	if len(img) == 0 {
		return Result{}, errors.New("empty image")
	}
	// The image is copied into C memory once: cgo forbids C keeping Go pointers, and a
	// single malloc+memcpy of a ~100 KB JPEG costs microseconds next to the core's work.
	buf := C.CBytes(img)
	defer C.free(buf)
	ck := C.vloc_intrinsics{fx: C.double(k.Fx), fy: C.double(k.Fy), cx: C.double(k.Cx), cy: C.double(k.Cy)}
	var out C.vloc_result
	rc := C.vloc_localize(c.m, (*C.uint8_t)(buf), C.size_t(len(img)), &ck, &out)
	runtime.KeepAlive(c)
	if rc != 0 {
		msg := C.GoString(&out.err[0])
		if msg == "" {
			msg = "vloc_localize failed"
		}
		return Result{}, errors.New(msg)
	}
	return Result{
		OK: out.ok == 1,
		Qw: float64(out.qw), Qx: float64(out.qx), Qy: float64(out.qy), Qz: float64(out.qz),
		Tx: float64(out.tx), Ty: float64(out.ty), Tz: float64(out.tz),
		NumKeypoints: int(out.num_keypoints), NumMatches: int(out.num_matches), NumInliers: int(out.num_inliers),
		MsDecode: float64(out.ms_decode), MsExtract: float64(out.ms_extract),
		MsMatch: float64(out.ms_match), MsPose: float64(out.ms_pose),
		Err: C.GoString(&out.err[0]),
	}, nil
}
