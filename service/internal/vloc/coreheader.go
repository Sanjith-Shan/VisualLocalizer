package vloc

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

// Core .vmap header (core/src/vmap.h, version 1, little endian):
//
//	char[4] "VMAP", u32 version, char[128] name, char[32] feature,
//	u32 dim, u32 num_points, u32 num_desc, u32 num_frames
const (
	coreHeaderLen = 4 + 4 + 128 + 32 + 16
	coreVersion   = 1
)

var errEmpty = errors.New("empty map file")

type coreHeader struct {
	name, feature                       string
	dim, points, descs, frames, version uint32
}

func parseCoreHeader(head []byte) (coreHeader, error) {
	if len(head) == 0 {
		return coreHeader{}, errEmpty
	}
	if len(head) < 4 || !bytes.Equal(head[:4], []byte("VMAP")) {
		return coreHeader{}, fmt.Errorf("bad magic %q, want \"VMAP\"", head[:min(4, len(head))])
	}
	if len(head) < coreHeaderLen {
		return coreHeader{}, fmt.Errorf("file is %d bytes, shorter than the %d byte header", len(head), coreHeaderLen)
	}
	le := binary.LittleEndian
	h := coreHeader{
		version: le.Uint32(head[4:]),
		name:    string(bytes.TrimRight(head[8:136], "\x00")),
		feature: string(bytes.TrimRight(head[136:168], "\x00")),
		dim:     le.Uint32(head[168:]), points: le.Uint32(head[172:]),
		descs: le.Uint32(head[176:]), frames: le.Uint32(head[180:]),
	}
	if h.version != coreVersion {
		return h, fmt.Errorf("unsupported map version %d, want %d", h.version, coreVersion)
	}
	if h.dim == 0 || h.dim > 1024 {
		return h, fmt.Errorf("descriptor dim %d out of range", h.dim)
	}
	if h.points == 0 || h.descs == 0 {
		return h, fmt.Errorf("map has %d points and %d descriptors", h.points, h.descs)
	}
	return h, nil
}

func checkCoreHeader(head []byte) error {
	_, err := parseCoreHeader(head)
	return err
}

// checkCoreSize rejects a file too short to hold the arrays its header declares (the
// observation list length is not in the header, so this is a lower bound).
func checkCoreSize(head []byte, size int64) error {
	h, err := parseCoreHeader(head)
	if err != nil {
		return err
	}
	p, d, f := int64(h.points), int64(h.descs), int64(h.frames)
	need := int64(coreHeaderLen) + p*12 + d*int64(h.dim) + d*4 + (p+1)*4 + f*48
	if size < need {
		return fmt.Errorf("file is %d bytes but its header declares at least %d (truncated upload?)", size, need)
	}
	return nil
}
