// Package maps holds the loaded maps and swaps them atomically.
//
// Each loaded map sits in a handle with a reference count. The registry owns one
// reference; every request that acquires the map owns another. A hot swap publishes
// the new handle with one atomic store and drops the registry's reference on the old
// one, so requests already running finish on the old map, new requests see the new
// map, and the old map is freed by whichever release brings its count to zero.
package maps

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Sanjith-Shan/VisualLocalizer/service/internal/vloc"
)

// Handle is a reference-counted map. Call Release exactly once per Acquire.
type Handle struct {
	Map      vloc.Map
	Name     string
	Version  int64
	Path     string
	Size     int64
	LoadedAt time.Time
	LoadMs   float64

	refs   atomic.Int64
	onFree func(*Handle)
}

// tryRef takes a reference unless the handle has already dropped to zero.
func (h *Handle) tryRef() bool {
	for {
		n := h.refs.Load()
		if n <= 0 {
			return false
		}
		if h.refs.CompareAndSwap(n, n+1) {
			return true
		}
	}
}

// Release drops one reference and frees the map when it was the last.
func (h *Handle) Release() {
	switch n := h.refs.Add(-1); {
	case n == 0:
		h.Map.Close()
		if h.onFree != nil {
			h.onFree(h)
		}
	case n < 0:
		panic("maps: handle released more times than acquired")
	}
}

// Refs is the current reference count, for tests and introspection.
func (h *Handle) Refs() int64 { return h.refs.Load() }

type slot struct{ cur atomic.Pointer[Handle] }

// Registry maps names to handles.
type Registry struct {
	mu      sync.RWMutex
	slots   map[string]*slot
	version atomic.Int64
	// OnFree is called after a map is freed (after its last request finished).
	OnFree func(*Handle)
}

func NewRegistry() *Registry { return &Registry{slots: map[string]*slot{}} }

var ErrNotFound = errors.New("map not found")

// Acquire returns the current handle for name with a reference taken.
func (r *Registry) Acquire(name string) (*Handle, error) {
	r.mu.RLock()
	s := r.slots[name]
	r.mu.RUnlock()
	if s == nil {
		return nil, ErrNotFound
	}
	for {
		h := s.cur.Load()
		if h == nil {
			return nil, ErrNotFound
		}
		if h.tryRef() {
			return h, nil
		}
		// Lost a race with a swap that freed h: the slot already holds the
		// replacement (or nil after a delete), so look again.
	}
}

// Put installs m under name and returns the previous handle's version (0 if none).
// The previous map is freed once its in-flight requests release it.
// Meta describes where a map came from.
type Meta struct {
	Path   string
	Size   int64
	LoadMs float64
}

func (r *Registry) Put(name string, m vloc.Map, meta Meta) (*Handle, int64) {
	h := &Handle{Map: m, Name: name, Version: r.version.Add(1), Path: meta.Path, Size: meta.Size,
		LoadedAt: time.Now(), LoadMs: meta.LoadMs, onFree: r.OnFree}
	h.refs.Store(1)
	r.mu.Lock()
	s := r.slots[name]
	if s == nil {
		s = &slot{}
		r.slots[name] = s
	}
	r.mu.Unlock()
	old := s.cur.Swap(h)
	var oldV int64
	if old != nil {
		oldV = old.Version
		old.Release()
	}
	return h, oldV
}

// Delete removes name. Its map is freed once in-flight requests finish.
func (r *Registry) Delete(name string) bool {
	r.mu.Lock()
	s := r.slots[name]
	delete(r.slots, name)
	r.mu.Unlock()
	if s == nil {
		return false
	}
	if old := s.cur.Swap(nil); old != nil {
		old.Release()
		return true
	}
	return false
}

// Info is a snapshot row for GET /v1/maps.
type Info struct {
	Name     string       `json:"name"`
	Version  int64        `json:"version"`
	Bytes    int64        `json:"bytes"`
	LoadedAt time.Time    `json:"loaded_at"`
	LoadMs   float64      `json:"load_ms"`
	InUse    int64        `json:"in_use"`
	Map      vloc.MapInfo `json:"map"`
}

// List returns every loaded map sorted by name.
func (r *Registry) List() []Info {
	r.mu.RLock()
	names := make([]string, 0, len(r.slots))
	for n := range r.slots {
		names = append(names, n)
	}
	r.mu.RUnlock()
	sort.Strings(names)
	out := make([]Info, 0, len(names))
	for _, n := range names {
		h, err := r.Acquire(n)
		if err != nil {
			continue
		}
		out = append(out, Info{Name: n, Version: h.Version, Bytes: h.Size, LoadedAt: h.LoadedAt,
			LoadMs: h.LoadMs, InUse: h.Refs() - 2, Map: h.Map.Info()})
		h.Release()
	}
	return out
}

// Len is the number of loaded maps.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.slots)
}

// Close drops the registry's reference on every map.
func (r *Registry) Close() {
	r.mu.RLock()
	names := make([]string, 0, len(r.slots))
	for n := range r.slots {
		names = append(names, n)
	}
	r.mu.RUnlock()
	for _, n := range names {
		r.Delete(n)
	}
}
