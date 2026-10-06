// Command vlocload is an open-loop load generator for vlocd.
//
// Requests are sent on a fixed schedule (constant arrival rate), not by a fixed number
// of clients waiting on each other, so a slow server does not slow the offered load
// down. Latency is measured from each request's scheduled send time, which counts any
// delay the generator itself adds instead of hiding it (no coordinated omission).
//
// Images are real 7-Scenes test frames read once into memory and replayed round robin.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type frame struct {
	path string
	data []byte
}

type sample struct {
	sched   time.Time
	lat     time.Duration
	status  int
	code    string
	core    float64
	service float64
	queue   float64
	sendErr bool
}

type Stats struct {
	OfferedRPS    float64        `json:"offered_rps"`
	DurationS     float64        `json:"duration_s"`
	Sent          int            `json:"sent"`
	OK            int            `json:"ok"`
	NoPose        int            `json:"ok_no_pose"`
	Shed          int            `json:"shed_503"`
	Timeout       int            `json:"timeout_504"`
	OtherErr      int            `json:"other_error"`
	ClientDropped int            `json:"client_dropped"`
	ThroughputRPS float64        `json:"throughput_ok_rps"`
	ShedRate      float64        `json:"shed_rate"`
	ErrorRate     float64        `json:"error_rate"`
	P50Ms         float64        `json:"p50_ms"`
	P95Ms         float64        `json:"p95_ms"`
	P99Ms         float64        `json:"p99_ms"`
	MaxMs         float64        `json:"max_ms"`
	ShedP50Ms     float64        `json:"shed_p50_ms"`
	CoreP50Ms     float64        `json:"core_p50_ms"`
	ServiceP50Ms  float64        `json:"server_service_p50_ms"`
	QueueP50Ms    float64        `json:"server_queue_p50_ms"`
	QueueP99Ms    float64        `json:"server_queue_p99_ms"`
	ClientOverP50 float64        `json:"client_minus_core_p50_ms"`
	ServerOverP50 float64        `json:"server_minus_core_p50_ms"`
	ServiceP99    float64        `json:"server_service_p99_ms"`
	ServiceMax    float64        `json:"server_service_max_ms"`
	LoadAvgStart  string         `json:"host_loadavg_start"`
	LoadAvgEnd    string         `json:"host_loadavg_end"`
	ErrorCodes    map[string]int `json:"error_codes,omitempty"`
}

func pct(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	i := int(math.Ceil(p/100*float64(len(s)))) - 1
	return s[max(0, min(i, len(s)-1))]
}

func round(x float64) float64 { return math.Round(x*1000) / 1000 }

func loadFrames(dir string, maxN int) ([]frame, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "seq-*", "frame-*.color.*"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return nil, fmt.Errorf("no frames under %s", dir)
	}
	// Spread the sample across sequences rather than taking the first maxN.
	step := max(1, len(paths)/maxN)
	var out []frame
	for i := 0; i < len(paths) && len(out) < maxN; i += step {
		b, err := os.ReadFile(paths[i])
		if err != nil {
			return nil, err
		}
		out = append(out, frame{paths[i], b})
	}
	return out, nil
}

type runner struct {
	url, query  string
	deadlineMs  string
	client      *http.Client
	frames      []frame
	maxOutstand int
}

func (r *runner) one(f frame, sched time.Time) sample {
	s := sample{sched: sched}
	req, _ := http.NewRequest(http.MethodPost, r.url+"?"+r.query, bytes.NewReader(f.data))
	req.Header.Set("Content-Type", "image/jpeg")
	req.Header.Set("X-Deadline-Ms", r.deadlineMs)
	resp, err := r.client.Do(req)
	if err != nil {
		s.sendErr = true
		s.lat = time.Since(sched)
		return s
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	s.lat = time.Since(sched)
	s.status = resp.StatusCode
	if resp.StatusCode == 200 {
		var out struct {
			OK      bool `json:"ok"`
			Timings struct {
				Core, Service, Queue float64
			} `json:"timings_ms"`
		}
		json.Unmarshal(body, &out)
		s.core, s.service, s.queue = out.Timings.Core, out.Timings.Service, out.Timings.Queue
		if !out.OK {
			s.code = "NO_POSE"
		}
	} else {
		var eb struct {
			Error struct{ Code string } `json:"error"`
		}
		json.Unmarshal(body, &eb)
		s.code = eb.Error.Code
	}
	return s
}

// run offers rate req/s for warmup+dur and returns stats over the post-warmup window.
func (r *runner) run(rate float64, warmup, dur time.Duration) Stats {
	total := warmup + dur
	n := int(rate * total.Seconds())
	interval := time.Duration(float64(time.Second) / rate)
	samples := make([]sample, 0, n)
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, r.maxOutstand)
	dropped := 0
	start := time.Now().Add(50 * time.Millisecond)
	measureFrom := start.Add(warmup)
	for i := 0; i < n; i++ {
		sched := start.Add(time.Duration(i) * interval)
		if d := time.Until(sched); d > 0 {
			time.Sleep(d)
		}
		select {
		case sem <- struct{}{}:
		default:
			if !sched.Before(measureFrom) {
				dropped++
			}
			continue
		}
		wg.Add(1)
		go func(f frame) {
			defer wg.Done()
			s := r.one(f, sched)
			<-sem
			if s.sched.Before(measureFrom) {
				return
			}
			mu.Lock()
			samples = append(samples, s)
			mu.Unlock()
		}(r.frames[i%len(r.frames)])
	}
	wg.Wait()

	st := Stats{OfferedRPS: rate, DurationS: dur.Seconds(), ClientDropped: dropped, ErrorCodes: map[string]int{}}
	var okLat, shedLat, core, svc, queue, cOver, sOver []float64
	for _, s := range samples {
		st.Sent++
		l := float64(s.lat.Microseconds()) / 1000
		switch {
		case s.sendErr:
			st.OtherErr++
			st.ErrorCodes["TRANSPORT"]++
		case s.status == 200:
			st.OK++
			if s.code == "NO_POSE" {
				st.NoPose++
			}
			okLat = append(okLat, l)
			core = append(core, s.core)
			svc = append(svc, s.service)
			queue = append(queue, s.queue)
			cOver = append(cOver, l-s.core)
			sOver = append(sOver, s.service-s.core)
		case s.status == 503:
			st.Shed++
			shedLat = append(shedLat, l)
		case s.status == 504:
			st.Timeout++
		default:
			st.OtherErr++
			st.ErrorCodes[strconv.Itoa(s.status)+" "+s.code]++
		}
	}
	all := float64(st.Sent + st.ClientDropped)
	if all > 0 {
		st.ShedRate = round(float64(st.Shed+st.Timeout) / all)
		st.ErrorRate = round(float64(st.OtherErr+st.ClientDropped) / all)
	}
	st.ThroughputRPS = round(float64(st.OK) / dur.Seconds())
	st.P50Ms, st.P95Ms, st.P99Ms, st.MaxMs = round(pct(okLat, 50)), round(pct(okLat, 95)), round(pct(okLat, 99)), round(pct(okLat, 100))
	st.ShedP50Ms = round(pct(shedLat, 50))
	st.CoreP50Ms, st.ServiceP50Ms = round(pct(core, 50)), round(pct(svc, 50))
	st.ServiceP99, st.ServiceMax = round(pct(svc, 99)), round(pct(svc, 100))
	st.QueueP50Ms, st.QueueP99Ms = round(pct(queue, 50)), round(pct(queue, 99))
	st.ClientOverP50, st.ServerOverP50 = round(pct(cOver, 50)), round(pct(sOver, 50))
	if len(st.ErrorCodes) == 0 {
		st.ErrorCodes = nil
	}
	return st
}

// loadAvg records the host's load average so a noisy run can be recognized later
// (the benchmark host is shared with other work).
func loadAvg() string {
	b, err := exec.Command("sysctl", "-n", "vm.loadavg").Output()
	if err != nil {
		b, err = os.ReadFile("/proc/loadavg")
		if err != nil {
			return ""
		}
	}
	return strings.Trim(strings.TrimSpace(string(b)), "{} ")
}

func main() {
	home, _ := os.UserHomeDir()
	var (
		base    = flag.String("url", "http://127.0.0.1:8080", "server base URL")
		mapName = flag.String("map", "heads", "map to localize against")
		data    = flag.String("data", filepath.Join(home, "Data/7scenes"), "7-Scenes root")
		scene   = flag.String("scene", "heads", "scene whose test frames are replayed")
		maxImg  = flag.Int("images", 300, "frames to preload")
		rates   = flag.String("rates", "5", "comma separated offered rates (req/s); one run per rate")
		dur     = flag.Duration("duration", 20*time.Second, "measured window per rate")
		warmup  = flag.Duration("warmup", 3*time.Second, "unmeasured lead-in per rate")
		pause   = flag.Duration("pause", 2*time.Second, "idle gap between rates")
		dl      = flag.Int("deadline-ms", 1000, "X-Deadline-Ms sent with each request")
		maxOut  = flag.Int("max-outstanding", 20000, "client-side cap on in-flight requests (excess counted as client_dropped)")
		intr    = flag.String("intrinsics", "fx=525&fy=525&cx=320&cy=240", "intrinsics query")
		out     = flag.String("out", "", "results JSON path (e.g. ../results/service/sweep.json)")
		label   = flag.String("label", "", "free text stored in the results")
	)
	flag.Parse()

	frames, err := loadFrames(filepath.Join(*data, *scene, "test"), *maxImg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var bytesTotal int
	for _, f := range frames {
		bytesTotal += len(f.data)
	}
	tr := &http.Transport{MaxIdleConns: 4096, MaxIdleConnsPerHost: 4096, IdleConnTimeout: 90 * time.Second}
	r := &runner{
		url: strings.TrimRight(*base, "/") + "/v1/maps/" + *mapName + "/localize", query: *intr,
		deadlineMs: strconv.Itoa(*dl), client: &http.Client{Transport: tr, Timeout: 60 * time.Second},
		frames: frames, maxOutstand: *maxOut,
	}
	var serverInfo json.RawMessage
	if resp, err := http.Get(strings.TrimRight(*base, "/") + "/readyz"); err == nil {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if json.Valid(b) {
			serverInfo = b
		}
	}
	fmt.Fprintf(os.Stderr, "%d frames (%.0f KB avg) from %s/%s/test\n", len(frames), float64(bytesTotal)/float64(len(frames))/1024, *data, *scene)
	fmt.Fprintf(os.Stderr, "%8s %8s %7s %7s %8s %8s %8s %8s %8s %8s %6s\n", "offered", "ok/s", "shed", "err", "p50", "p95", "p99", "core50", "over50", "srv99", "load")
	var results []Stats
	for i, f := range strings.Split(*rates, ",") {
		rate, err := strconv.ParseFloat(strings.TrimSpace(f), 64)
		if err != nil || rate <= 0 {
			fmt.Fprintln(os.Stderr, "bad rate", f)
			os.Exit(2)
		}
		if i > 0 {
			time.Sleep(*pause)
		}
		la0 := loadAvg()
		st := r.run(rate, *warmup, *dur)
		st.LoadAvgStart, st.LoadAvgEnd = la0, loadAvg()
		results = append(results, st)
		fmt.Fprintf(os.Stderr, "%8.1f %8.1f %6.1f%% %6.1f%% %8.1f %8.1f %8.1f %8.1f %8.1f %8.1f %6s\n", st.OfferedRPS, st.ThroughputRPS,
			100*st.ShedRate, 100*st.ErrorRate, st.P50Ms, st.P95Ms, st.P99Ms, st.CoreP50Ms, st.ClientOverP50, st.ServiceP99, strings.Fields(st.LoadAvgStart + " -")[0])
	}
	if *out != "" {
		doc := map[string]any{
			"tool": "vlocload", "time": time.Now().UTC().Format(time.RFC3339), "label": *label,
			"command": strings.Join(os.Args, " "), "url": *base, "map": *mapName, "scene": *scene,
			"frames": len(frames), "deadline_ms": *dl, "warmup_s": warmup.Seconds(), "server": serverInfo,
			"latency_note": "latency_ms fields are client-side, from scheduled send time to full response, over 200 responses only",
			"runs":         results,
		}
		os.MkdirAll(filepath.Dir(*out), 0o755)
		b, _ := json.MarshalIndent(doc, "", "  ")
		if err := os.WriteFile(*out, b, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "wrote", *out)
	}
}
