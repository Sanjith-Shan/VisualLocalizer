package maps

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Sanjith-Shan/VisualLocalizer/service/internal/vloc"
)

type countMap struct {
	id     int
	freed  *atomic.Int64
	closed atomic.Bool
}

func (m *countMap) Localize([]byte, vloc.Intrinsics) (vloc.Result, error) {
	if m.closed.Load() {
		panic("use after free")
	}
	return vloc.Result{OK: true}, nil
}
func (m *countMap) Info() vloc.MapInfo { return vloc.MapInfo{} }
func (m *countMap) Close() {
	if !m.closed.CompareAndSwap(false, true) {
		panic("double free")
	}
	m.freed.Add(1)
}

func TestOldMapFreedAfterLastRelease(t *testing.T) {
	var freed atomic.Int64
	r := NewRegistry()
	r.Put("a", &countMap{id: 1, freed: &freed}, Meta{})
	h1, _ := r.Acquire("a")
	r.Put("a", &countMap{id: 2, freed: &freed}, Meta{})
	if freed.Load() != 0 {
		t.Fatal("old map freed while a request holds it")
	}
	h2, _ := r.Acquire("a")
	if h2.Map.(*countMap).id != 2 {
		t.Fatal("new requests must see the new map")
	}
	h1.Release()
	if freed.Load() != 1 {
		t.Fatal("old map not freed on last release")
	}
	h2.Release()
	r.Close()
	if freed.Load() != 2 {
		t.Fatal("Close did not free the current map")
	}
	if _, err := r.Acquire("a"); err != ErrNotFound {
		t.Fatal("acquire after close")
	}
}

func TestSwapStress(t *testing.T) {
	var freed atomic.Int64
	r := NewRegistry()
	r.Put("a", &countMap{freed: &freed}, Meta{})
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				h, err := r.Acquire("a")
				if err != nil {
					t.Error(err)
					return
				}
				h.Map.Localize(nil, vloc.Intrinsics{})
				h.Release()
			}
		}()
	}
	const swaps = 2000
	for i := range swaps {
		r.Put("a", &countMap{id: i, freed: &freed}, Meta{})
	}
	close(stop)
	wg.Wait()
	if freed.Load() != swaps {
		t.Fatalf("freed %d, want %d", freed.Load(), swaps)
	}
}
