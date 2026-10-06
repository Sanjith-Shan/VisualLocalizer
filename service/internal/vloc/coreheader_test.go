package vloc

import (
	"encoding/binary"
	"strings"
	"testing"
)

func coreHead(version, dim, points, descs, frames uint32) []byte {
	b := make([]byte, coreHeaderLen)
	copy(b, "VMAP")
	le := binary.LittleEndian
	le.PutUint32(b[4:], version)
	copy(b[8:], "heads")
	copy(b[136:], "sift")
	le.PutUint32(b[168:], dim)
	le.PutUint32(b[172:], points)
	le.PutUint32(b[176:], descs)
	le.PutUint32(b[180:], frames)
	return b
}

func TestCoreHeader(t *testing.T) {
	good := coreHead(1, 128, 10, 20, 2)
	if err := checkCoreHeader(good); err != nil {
		t.Fatal(err)
	}
	need := int64(coreHeaderLen + 10*12 + 20*128 + 20*4 + 11*4 + 2*48)
	if err := checkCoreSize(good, need); err != nil {
		t.Fatal(err)
	}
	if err := checkCoreSize(good, need-1); err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("truncated file accepted: %v", err)
	}
	for name, h := range map[string][]byte{
		"magic":   append([]byte("XMAP"), good[4:]...),
		"version": coreHead(2, 128, 10, 20, 2),
		"dim":     coreHead(1, 0, 10, 20, 2),
		"empty":   coreHead(1, 128, 0, 0, 0),
		"short":   good[:100],
		"nothing": nil,
	} {
		if err := checkCoreHeader(h); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
