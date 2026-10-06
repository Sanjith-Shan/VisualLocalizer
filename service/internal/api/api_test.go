package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Sanjith-Shan/VisualLocalizer/service/internal/admit"
	"github.com/Sanjith-Shan/VisualLocalizer/service/internal/vloc"
)

func testImage(t testing.TB, w, h int, format string, seed int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y += 7 {
		for x := 0; x < w; x += 7 {
			img.Set(x, y, color.RGBA{uint8(x + seed), uint8(y), uint8(seed), 255})
		}
	}
	var b bytes.Buffer
	var err error
	if format == "png" {
		err = png.Encode(&b, img)
	} else {
		err = jpeg.Encode(&b, img, &jpeg.Options{Quality: 80})
	}
	if err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

type env struct {
	t   testing.TB
	s   *Server
	ts  *httptest.Server
	eng *vloc.FakeEngine
	dir string
}

func newEnv(t testing.TB, work time.Duration, pc admit.Config) *env {
	eng := &vloc.FakeEngine{Work: work}
	pool := admit.New(pc)
	dir := t.TempDir()
	s := New(Config{MapDir: dir, MaxImageBytes: 1 << 20}, Deps{Engine: eng, Pool: pool})
	ts := httptest.NewServer(s)
	t.Cleanup(func() { ts.Close(); pool.Close(); s.Maps.Close() })
	return &env{t: t, s: s, ts: ts, eng: eng, dir: dir}
}

func (e *env) fakeMap(name string, points uint32) []byte {
	p := filepath.Join(e.dir, "src-"+name)
	if err := vloc.WriteFakeMap(p, name, points); err != nil {
		e.t.Fatal(err)
	}
	b, _ := readFile(p)
	return b
}

func readFile(p string) ([]byte, error) {
	return func() ([]byte, error) { return osReadFile(p) }()
}

func (e *env) put(name string, body []byte) (*http.Response, []byte) {
	req, _ := http.NewRequest(http.MethodPut, e.ts.URL+"/v1/maps/"+name, bytes.NewReader(body))
	return e.do(req)
}

func (e *env) do(req *http.Request) (*http.Response, []byte) {
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

const k7 = "fx=525&fy=525&cx=320&cy=240"

func (e *env) localize(name, query string, img []byte, hdr map[string]string) (*http.Response, []byte) {
	req, _ := http.NewRequest(http.MethodPost, e.ts.URL+"/v1/maps/"+name+"/localize?"+query, bytes.NewReader(img))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return e.do(req)
}

func errCode(b []byte) string {
	var eb errorBody
	json.Unmarshal(b, &eb)
	return eb.Error.Code
}

func TestLocalizeHappyPathAndDeterminism(t *testing.T) {
	e := newEnv(t, 0, admit.Config{Workers: 2})
	if resp, b := e.put("heads", e.fakeMap("heads", 1234)); resp.StatusCode != http.StatusCreated {
		t.Fatalf("put: %d %s", resp.StatusCode, b)
	}
	img := testImage(t, 640, 480, "jpeg", 1)
	resp, b := e.localize("heads", k7, img, map[string]string{RequestIDHeader: "abc-123"})
	if resp.StatusCode != 200 {
		t.Fatalf("localize: %d %s", resp.StatusCode, b)
	}
	if resp.Header.Get(RequestIDHeader) != "abc-123" {
		t.Errorf("request id not echoed: %q", resp.Header.Get(RequestIDHeader))
	}
	var out LocalizeResponse
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || out.Pose == nil || out.RequestID != "abc-123" || out.ImageWidth != 640 || out.NumInliers == 0 {
		t.Fatalf("bad response: %s", b)
	}
	q := out.Pose.Q
	if n := q.W*q.W + q.X*q.X + q.Y*q.Y + q.Z*q.Z; n < 0.999 || n > 1.001 {
		t.Errorf("quaternion not unit: %v", n)
	}
	// Same frame, same pose; header intrinsics are equivalent to query intrinsics.
	_, b2 := e.localize("heads", "", img, map[string]string{IntrinsicsHeader: "525,525,320,240"})
	var out2 LocalizeResponse
	json.Unmarshal(b2, &out2)
	if out2.Pose == nil || *out2.Pose != *out.Pose {
		t.Errorf("pose not deterministic: %s vs %s", b, b2)
	}
	// PNG accepted too.
	if resp, b := e.localize("heads", k7, testImage(t, 640, 480, "png", 2), nil); resp.StatusCode != 200 {
		t.Errorf("png: %d %s", resp.StatusCode, b)
	}
}

func TestValidation(t *testing.T) {
	e := newEnv(t, 0, admit.Config{Workers: 1})
	e.put("heads", e.fakeMap("heads", 10))
	jpg := testImage(t, 640, 480, "jpeg", 1)
	gif := []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00;")
	cases := []struct {
		name   string
		mapN   string
		query  string
		body   []byte
		hdr    map[string]string
		status int
		code   string
	}{
		{"unknown map", "kitchen", k7, jpg, nil, 404, CodeMapNotFound},
		{"bad map name", "Bad.Name", k7, jpg, nil, 400, CodeBadName},
		{"empty body", "heads", k7, nil, nil, 400, CodeEmptyBody},
		{"gif rejected", "heads", k7, gif, nil, 415, CodeUnsupportedMedia},
		{"text rejected", "heads", k7, []byte("hello world"), nil, 415, CodeUnsupportedMedia},
		{"truncated jpeg", "heads", k7, jpg[:10], nil, 400, CodeBadImage},
		{"too large", "heads", k7, append(bytes.Clone(jpg), make([]byte, 1<<20)...), nil, 413, CodeImageTooLarge},
		{"tiny image", "heads", "fx=5&fy=5&cx=8&cy=8", testImage(t, 16, 16, "png", 0), nil, 422, CodeBadImageSize},
		{"huge dims", "heads", k7, testImage(t, 5000, 40, "png", 0), nil, 422, CodeBadImageSize},
		{"missing intrinsics", "heads", "", jpg, nil, 400, CodeMissingIntrinsics},
		{"partial intrinsics", "heads", "fx=525&fy=525&cx=320", jpg, nil, 400, CodeMissingIntrinsics},
		{"non numeric", "heads", "fx=abc&fy=525&cx=320&cy=240", jpg, nil, 400, CodeBadIntrinsics},
		{"negative focal", "heads", "fx=-525&fy=525&cx=320&cy=240", jpg, nil, 422, CodeBadIntrinsics},
		{"nan", "heads", "fx=NaN&fy=525&cx=320&cy=240", jpg, nil, 422, CodeBadIntrinsics},
		{"inf", "heads", "fx=525&fy=Inf&cx=320&cy=240", jpg, nil, 422, CodeBadIntrinsics},
		{"principal point outside", "heads", "fx=525&fy=525&cx=700&cy=240", jpg, nil, 422, CodeBadIntrinsics},
		{"header wrong arity", "heads", "", jpg, map[string]string{IntrinsicsHeader: "525,525,320"}, 400, CodeBadIntrinsics},
		{"bad deadline", "heads", k7, jpg, map[string]string{DeadlineHeader: "soon"}, 400, CodeBadDeadline},
		{"zero deadline", "heads", k7, jpg, map[string]string{DeadlineHeader: "0"}, 400, CodeBadDeadline},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, b := e.localize(c.mapN, c.query, c.body, c.hdr)
			if resp.StatusCode != c.status || errCode(b) != c.code {
				t.Errorf("got %d %s, want %d %s", resp.StatusCode, b, c.status, c.code)
			}
			var eb errorBody
			json.Unmarshal(b, &eb)
			if eb.RequestID == "" || eb.RequestID != resp.Header.Get(RequestIDHeader) {
				t.Errorf("error body request id %q vs header %q", eb.RequestID, resp.Header.Get(RequestIDHeader))
			}
		})
	}
}

func TestMapIngestion(t *testing.T) {
	e := newEnv(t, 0, admit.Config{Workers: 1})
	if resp, _ := e.do(must(http.NewRequest("GET", e.ts.URL+"/readyz", nil))); resp.StatusCode != 503 {
		t.Errorf("readyz with no maps: %d", resp.StatusCode)
	}
	good := e.fakeMap("heads", 10)
	bad := bytes.Clone(good)
	copy(bad, "NOTAMAP!")
	wrongVer := bytes.Clone(good)
	wrongVer[8] = 9
	for _, c := range []struct {
		name, mapN string
		body       []byte
		status     int
		code       string
	}{
		{"bad magic", "heads", bad, 422, CodeBadMapHeader},
		{"wrong version", "heads", wrongVer, 422, CodeBadMapHeader},
		{"short", "heads", good[:5], 422, CodeBadMapHeader},
		{"empty", "heads", nil, 400, CodeEmptyBody},
		{"bad name", "../etc", good, 404, ""}, // mux cleans the path; never reaches the handler
		{"bad name chars", "UPPER", good, 400, CodeBadName},
	} {
		resp, b := e.put(c.mapN, c.body)
		if resp.StatusCode != c.status || (c.code != "" && errCode(b) != c.code) {
			t.Errorf("%s: got %d %s, want %d %s", c.name, resp.StatusCode, b, c.status, c.code)
		}
	}
	if e.s.Maps.Len() != 0 {
		t.Fatal("a rejected upload was installed")
	}
	resp, b := e.put("heads", good)
	if resp.StatusCode != 201 {
		t.Fatalf("put: %d %s", resp.StatusCode, b)
	}
	resp, b = e.put("heads", e.fakeMap("heads", 20))
	var pr PutMapResponse
	json.Unmarshal(b, &pr)
	if resp.StatusCode != 200 || pr.PreviousVersion == 0 || pr.Map.NumPoints != 20 {
		t.Fatalf("replace: %d %s", resp.StatusCode, b)
	}
	// A bad upload must leave the serving map in place.
	e.put("heads", bad)
	_, b = e.do(must(http.NewRequest("GET", e.ts.URL+"/v1/maps", nil)))
	if !strings.Contains(string(b), `"num_points":20`) {
		t.Errorf("list after bad upload: %s", b)
	}
	if resp, _ := e.do(must(http.NewRequest("GET", e.ts.URL+"/readyz", nil))); resp.StatusCode != 200 {
		t.Errorf("readyz with a map: %d", resp.StatusCode)
	}
	if e.eng.Freed.Load() != 1 {
		t.Errorf("old map not freed after swap: freed=%d", e.eng.Freed.Load())
	}
	if resp, _ := e.do(must(http.NewRequest("DELETE", e.ts.URL+"/v1/maps/heads", nil))); resp.StatusCode != 204 {
		t.Errorf("delete: %d", resp.StatusCode)
	}
	if e.eng.Freed.Load() != 2 {
		t.Errorf("deleted map not freed: %d", e.eng.Freed.Load())
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// TestHotSwapUnderLoad swaps the map many times while requests run against it. The
// fake map panics if it is closed while a Localize is running or used after close, so
// a refcount bug fails loudly. Run with -race.
func TestHotSwapUnderLoad(t *testing.T) {
	e := newEnv(t, 2*time.Millisecond, admit.Config{Workers: 4, MaxQueue: 1000})
	e.put("heads", e.fakeMap("heads", 1))
	img := testImage(t, 640, 480, "jpeg", 3)
	stop := make(chan struct{})
	var ok, other atomic.Int64
	versions := sync.Map{}
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				resp, b := e.localize("heads", k7, img, map[string]string{DeadlineHeader: "10000"})
				if resp.StatusCode == 200 {
					var out LocalizeResponse
					json.Unmarshal(b, &out)
					versions.Store(out.MapVersion, true)
					ok.Add(1)
				} else {
					other.Add(1)
					t.Errorf("status %d during swap: %s", resp.StatusCode, b)
				}
			}
		}()
	}
	const swaps = 40
	for i := range swaps {
		if resp, b := e.put("heads", e.fakeMap("heads", uint32(i+2))); resp.StatusCode != 200 {
			t.Fatalf("swap %d: %d %s", i, resp.StatusCode, b)
		}
		time.Sleep(3 * time.Millisecond)
	}
	close(stop)
	wg.Wait()
	nv := 0
	versions.Range(func(_, _ any) bool { nv++; return true })
	t.Logf("requests ok=%d other=%d, distinct map versions served=%d", ok.Load(), other.Load(), nv)
	if loaded, freed := e.eng.Loaded.Load(), e.eng.Freed.Load(); loaded != swaps+1 || freed != swaps {
		t.Errorf("loaded=%d freed=%d, want %d and %d (every replaced map freed, current one live)", loaded, freed, swaps+1, swaps)
	}
	if nv < 2 {
		t.Errorf("requests saw only %d map versions; swaps did not interleave with load", nv)
	}
}

// TestShedding drives a 1-worker server well past capacity with a tight deadline.
// Admission must reject with 503 + Retry-After instead of queueing, and the requests it
// admits must finish within their deadline.
func TestShedding(t *testing.T) {
	work := 20 * time.Millisecond
	e := newEnv(t, work, admit.Config{Workers: 1, MaxQueue: 100, InitSvc: work})
	e.put("heads", e.fakeMap("heads", 1))
	img := testImage(t, 640, 480, "jpeg", 4)
	const n = 40
	var wg sync.WaitGroup
	var mu sync.Mutex
	var okLat []time.Duration
	counts := map[int]int{}
	retry := true
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t0 := time.Now()
			resp, b := e.localize("heads", k7, img, map[string]string{DeadlineHeader: "100"})
			d := time.Since(t0)
			mu.Lock()
			defer mu.Unlock()
			counts[resp.StatusCode]++
			switch resp.StatusCode {
			case 200:
				okLat = append(okLat, d)
			case 503:
				if resp.Header.Get("Retry-After") == "" || errCode(b) != CodeOverloaded {
					retry = false
				}
			}
		}()
	}
	wg.Wait()
	t.Logf("status counts %v", counts)
	if counts[503] == 0 {
		t.Fatalf("no shedding at 40 concurrent on 1 worker with a 100 ms deadline: %v", counts)
	}
	if counts[200] == 0 {
		t.Fatalf("everything shed: %v", counts)
	}
	if !retry {
		t.Error("503 without Retry-After or OVERLOADED code")
	}
	for _, d := range okLat {
		if d > 250*time.Millisecond { // deadline 100 ms plus generous slack for -race
			t.Errorf("admitted request took %v, deadline was 100ms", d)
		}
	}
	_, mb := e.do(must(http.NewRequest("GET", e.ts.URL+"/metrics", nil)))
	if !strings.Contains(string(mb), "vloc_shed_total{reason=") {
		t.Error("shed counter missing from /metrics")
	}
}

func TestMetricsExposed(t *testing.T) {
	e := newEnv(t, 0, admit.Config{Workers: 1})
	e.put("heads", e.fakeMap("heads", 1))
	e.localize("heads", k7, testImage(t, 640, 480, "png", 0), nil)
	e.localize("heads", "", testImage(t, 640, 480, "png", 0), nil)
	_, b := e.do(must(http.NewRequest("GET", e.ts.URL+"/metrics", nil)))
	for _, want := range []string{
		`vloc_http_request_duration_seconds_count{outcome="ok",route="POST /v1/maps/{name}/localize"} 1`,
		`vloc_http_request_duration_seconds_count{outcome="client_error",route="POST /v1/maps/{name}/localize"} 1`,
		`vloc_stage_duration_seconds_count{stage="extract"} 1`,
		`vloc_stage_duration_seconds_count{stage="overhead"} 1`,
		`vloc_map_events_total{event="loaded",map="heads"} 1`,
		"vloc_queue_depth 0", "vloc_inflight 0", "vloc_maps_loaded 1",
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("metrics missing %q", want)
		}
	}
}

var _ = fmt.Sprint
