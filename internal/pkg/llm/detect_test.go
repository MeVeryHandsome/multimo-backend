package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"multimo-backend/internal/config"
)

func newDetectGateway(t *testing.T, key string, handler http.HandlerFunc) (*Gateway, *memRecorder) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cfg := &config.Config{
		Detector: config.Detector{Endpoint: config.Endpoint{BaseURL: srv.URL, APIKey: key, Model: "visdrone-test",
			Timeout: 2 * time.Second, MaxConcurrency: 1}, Conf: 0.57, Types: []string{"video", "image"}},
		AI: config.AIPolicy{MaxRetries: 2, RetryBase: time.Millisecond, FallbackEnabled: true, LogPayloadChars: 80},
	}
	rec := &memRecorder{}
	g := New(cfg, rec)
	g.sleep = func(context.Context, time.Duration) error { return nil }
	return g, rec
}

func TestDetectRetriesAndParses(t *testing.T) {
	var calls atomic.Int32
	g, rec := newDetectGateway(t, "", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/detect" {
			t.Errorf("path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "" {
			t.Errorf("no key configured, but Authorization was sent")
		}
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		json.Unmarshal(body, &req) //nolint:errcheck
		if req["conf"] != 0.57 || !strings.HasPrefix(req["image"].(string), "data:image/jpeg;base64,") {
			t.Errorf("request %v", req)
		}
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(`{"model":"visdrone-yolov9e","width":1920,"height":1080,"objects":[
			{"label":"car","score":0.9,"bbox":[10,20,110,80]},{"label":"car","score":0.8,"bbox":[0,0,5,5]},
			{"label":"pedestrian","score":0.7,"bbox":[1,2,3,4]}]}`)) //nolint:errcheck
	})
	if !g.DetectsType("image") || g.DetectsType("remote_sensing") || g.DetectorModel() != "visdrone-test" {
		t.Fatal("detector type routing")
	}
	res, err := g.Detect(context.Background(), "test", 1, Image{Data: []byte{1, 2, 3}, Width: 1920, Height: 1080})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || len(res.Objects) != 3 || res.Model != "visdrone-yolov9e" || res.Width != 1920 {
		t.Fatalf("calls %d, result %+v", calls.Load(), res)
	}
	if res.Objects[0].Box != [4]float64{10, 20, 110, 80} {
		t.Fatalf("box %v", res.Objects[0].Box)
	}
	l := rec.last(t)
	if l.Endpoint != EndpointDetector || l.Attempts != 2 || l.Output != "3 objects: car 2, pedestrian 1" {
		t.Fatalf("log %+v", l)
	}
}

func TestDetectSendsKeyAndFailsWithoutFallback(t *testing.T) {
	g, _ := newDetectGateway(t, "secret", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("auth %q", r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusUnauthorized)
	})
	if _, err := g.Detect(context.Background(), "test", 1, Image{Data: []byte{1}}); err == nil {
		t.Fatal("a 401 must surface as an error (the caller falls back to VLM boxes)")
	}
}

func TestDetectorDisabledWithoutURL(t *testing.T) {
	g := New(&config.Config{Detector: config.Detector{Types: []string{"image"}}}, &memRecorder{})
	if g.DetectorEnabled() || g.DetectsType("image") {
		t.Fatal("no URL means no detector")
	}
}
