package vloc

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"os"
	"sync/atomic"
	"time"
)

// Fake map file layout (little endian):
//
//	magic   [8]byte "VLOCFAKE"
//	version uint32  (1)
//	points  uint32
//	name    [32]byte, NUL padded
var fakeMagic = []byte("VLOCFAKE")

const fakeVersion = 1

// FakeEngine is a deterministic stand-in for the core. Each Localize busy-spins for
// Work to imitate a CPU-bound cgo call that holds an OS thread, then returns a pose
// derived from a hash of the image bytes, so the same frame always gives the same pose.
type FakeEngine struct {
	Work time.Duration
	// Loaded and Freed count map lifecycle events, for hot-swap tests.
	Loaded, Freed atomic.Int64
}

func (e *FakeEngine) Name() string { return "fake" }

// WriteFakeMap writes a fake map file.
func WriteFakeMap(path, name string, points uint32) error {
	var b bytes.Buffer
	b.Write(fakeMagic)
	binary.Write(&b, binary.LittleEndian, uint32(fakeVersion))
	binary.Write(&b, binary.LittleEndian, points)
	var n [32]byte
	copy(n[:], name)
	b.Write(n[:])
	b.Write(make([]byte, 64)) // body
	return os.WriteFile(path, b.Bytes(), 0o644)
}

func (e *FakeEngine) CheckHeader(head []byte) error {
	if len(head) < 48 {
		return errors.New("file too short for a map header")
	}
	if !bytes.Equal(head[:8], fakeMagic) {
		return fmt.Errorf("bad magic %q, want %q", head[:8], fakeMagic)
	}
	if v := binary.LittleEndian.Uint32(head[8:]); v != fakeVersion {
		return fmt.Errorf("unsupported map version %d, want %d", v, fakeVersion)
	}
	return nil
}

func (e *FakeEngine) CheckSize(head []byte, size int64) error {
	if size < 48+64 {
		return fmt.Errorf("file is %d bytes, too short for a fake map", size)
	}
	return nil
}

func (e *FakeEngine) Load(path string) (Map, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := e.CheckHeader(b); err != nil {
		return nil, err
	}
	m := &fakeMap{e: e, info: MapInfo{
		NumPoints:     int(binary.LittleEndian.Uint32(b[12:])),
		DescriptorDim: 128,
		Feature:       "fake",
		Name:          string(bytes.TrimRight(b[16:48], "\x00")),
	}}
	e.Loaded.Add(1)
	return m, nil
}

type fakeMap struct {
	e      *FakeEngine
	info   MapInfo
	closed atomic.Bool
	active atomic.Int64
}

func (m *fakeMap) Info() MapInfo { return m.info }

func (m *fakeMap) Close() {
	if m.active.Load() != 0 {
		panic("vloc: fake map closed with a Localize in flight")
	}
	if !m.closed.CompareAndSwap(false, true) {
		panic("vloc: fake map closed twice")
	}
	m.e.Freed.Add(1)
}

func spin(d time.Duration) {
	end := time.Now().Add(d)
	x := 1.0
	for time.Now().Before(end) {
		for i := 0; i < 1000; i++ {
			x = math.Sqrt(x + 1)
		}
	}
	_ = x
}

func (m *fakeMap) Localize(img []byte, k Intrinsics) (Result, error) {
	m.active.Add(1)
	defer m.active.Add(-1)
	if m.closed.Load() {
		panic("vloc: Localize on a freed fake map")
	}
	t0 := time.Now()
	h := fnv.New64a()
	h.Write(img)
	s := h.Sum64()
	spin(m.e.Work)
	f := func(shift uint) float64 { return float64((s>>shift)&0xffff)/65535*2 - 1 }
	qw, qx, qy, qz := 1+f(0)*0.2, f(16)*0.3, f(32)*0.3, f(48)*0.3
	n := math.Sqrt(qw*qw + qx*qx + qy*qy + qz*qz)
	ms := float64(time.Since(t0).Microseconds()) / 1000
	return Result{
		OK: true,
		Qw: qw / n, Qx: qx / n, Qy: qy / n, Qz: qz / n,
		Tx: f(8), Ty: f(24), Tz: f(40),
		NumKeypoints: 2000, NumMatches: 400, NumInliers: 150 + int(s%100),
		MsDecode: ms * 0.1, MsExtract: ms * 0.5, MsMatch: ms * 0.3, MsPose: ms * 0.1,
	}, nil
}
