package api

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Sanjith-Shan/VisualLocalizer/service/internal/maps"
	"github.com/Sanjith-Shan/VisualLocalizer/service/internal/vloc"
)

// MapPath is where an ingested map named name is stored.
func (s *Server) MapPath(name string) string { return filepath.Join(s.cfg.MapDir, name+".vmap") }

// PutMapResponse is the body of a successful PUT /v1/maps/{name}.
type PutMapResponse struct {
	Name            string       `json:"name"`
	Version         int64        `json:"version"`
	PreviousVersion int64        `json:"previous_version,omitempty"`
	Bytes           int64        `json:"bytes"`
	LoadMs          float64      `json:"load_ms"`
	Map             vloc.MapInfo `json:"map"`
}

// LoadFile loads a map from disk and installs it under name. Used at startup and by PUT.
func (s *Server) LoadFile(name, path string) (*maps.Handle, int64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, 0, err
	}
	t0 := time.Now()
	m, err := s.engine.Load(path)
	if err != nil {
		s.m.MapEvents.WithLabelValues(name, "load_failed").Inc()
		return nil, 0, err
	}
	loadMs := ms(time.Since(t0))
	h, prev := s.Maps.Put(name, m, maps.Meta{Path: path, Size: fi.Size(), LoadMs: loadMs})
	ev := "loaded"
	if prev != 0 {
		ev = "swapped"
	}
	s.m.MapEvents.WithLabelValues(name, ev).Inc()
	s.log.Info("map "+ev, "map", name, "version", h.Version, "previous_version", prev, "bytes", fi.Size(),
		"load_ms", loadMs, "points", m.Info().NumPoints)
	return h, prev, nil
}

func (s *Server) putMap(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	annotate(w, slog.String("map", name))
	if !nameRe.MatchString(name) {
		writeErr(w, r, http.StatusBadRequest, CodeBadName, "map name must match "+nameRe.String())
		return
	}
	if s.cfg.MapDir == "" {
		writeErr(w, r, http.StatusInternalServerError, CodeInternal, "server has no map directory configured")
		return
	}
	reject := func(status int, code, msg string) {
		s.m.MapEvents.WithLabelValues(name, "rejected").Inc()
		writeErr(w, r, status, code, msg)
	}
	if r.ContentLength > s.cfg.MaxMapBytes {
		reject(http.StatusRequestEntityTooLarge, CodeMapTooLarge, fmt.Sprintf("map is %d bytes, limit %d", r.ContentLength, s.cfg.MaxMapBytes))
		return
	}
	mu, _ := s.ingestMu.LoadOrStore(name, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()

	body := http.MaxBytesReader(w, r.Body, s.cfg.MaxMapBytes)
	head := make([]byte, vloc.HeaderLen)
	n, err := io.ReadFull(body, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		reject(http.StatusBadRequest, CodeBadMapHeader, "reading map: "+err.Error())
		return
	}
	head = head[:n]
	if n == 0 {
		reject(http.StatusBadRequest, CodeEmptyBody, "request body must be a map file")
		return
	}
	if err := s.engine.CheckHeader(head); err != nil {
		reject(http.StatusUnprocessableEntity, CodeBadMapHeader, err.Error())
		return
	}
	if err := os.MkdirAll(s.cfg.MapDir, 0o755); err != nil {
		writeErr(w, r, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	tmp, err := os.CreateTemp(s.cfg.MapDir, "."+name+".upload-*")
	if err != nil {
		writeErr(w, r, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	defer os.Remove(tmp.Name()) // no-op after the rename below
	size, err := tmp.Write(head)
	if err == nil {
		var rest int64
		rest, err = io.Copy(tmp, body)
		size += int(rest)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			reject(http.StatusRequestEntityTooLarge, CodeMapTooLarge, fmt.Sprintf("map exceeds limit %d bytes", s.cfg.MaxMapBytes))
		} else {
			reject(http.StatusBadRequest, CodeBadMapHeader, "upload failed: "+err.Error())
		}
		return
	}
	// Load from the temp path first: a map that fails to load never replaces the
	// serving one, on disk or in memory.
	h, prev, err := s.LoadFile(name, tmp.Name())
	if err != nil {
		writeErr(w, r, http.StatusUnprocessableEntity, CodeMapLoadFailed, err.Error())
		return
	}
	// Renaming over a file the old map may still have open or mapped is safe on POSIX:
	// the old inode lives until it is closed.
	if err := os.Rename(tmp.Name(), s.MapPath(name)); err != nil {
		s.log.Error("map rename failed; the new map serves but will not survive a restart", "map", name, "err", err)
	}
	status := http.StatusCreated
	if prev != 0 {
		status = http.StatusOK
	}
	annotate(w, slog.Int64("version", h.Version), slog.Int64("previous_version", prev))
	writeJSON(w, status, PutMapResponse{Name: name, Version: h.Version, PreviousVersion: prev,
		Bytes: int64(size), LoadMs: h.LoadMs, Map: h.Map.Info()})
}
