package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"multimo-backend/internal/config"
)

type memRecorder struct {
	mu   sync.Mutex
	logs []CallLog
}

func (m *memRecorder) Record(_ context.Context, l CallLog) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.logs = append(m.logs, l)
}

func (m *memRecorder) last(t *testing.T) CallLog {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.logs) == 0 {
		t.Fatal("no call logged")
	}
	return m.logs[len(m.logs)-1]
}

const testKey = "sk-test-secret-123"

func newTestGateway(t *testing.T, handler http.HandlerFunc) (*Gateway, *memRecorder) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	ep := config.Endpoint{BaseURL: srv.URL, APIKey: testKey, Model: "m", Timeout: 2 * time.Second, MaxConcurrency: 2}
	cfg := &config.Config{
		VLM:       config.VLM{Endpoint: ep, EnableThinking: "false"},
		Embedding: config.Embedding{Endpoint: ep, Dim: 4},
		Decision:  config.Decision{Endpoint: ep, Path: "/decisions"},
		Vision:    config.VisionEmbedding{Endpoint: ep, Path: "/mm", Dim: 4},
		AI:        config.AIPolicy{MaxRetries: 3, RetryBase: time.Millisecond, FallbackEnabled: true, LogPayloadChars: 80},
	}
	rec := &memRecorder{}
	g := New(cfg, rec)
	g.sleep = func(context.Context, time.Duration) error { return nil }
	return g, rec
}

func chatOK(text string) string {
	b, _ := json.Marshal(map[string]any{
		"model":   "m",
		"choices": []any{map[string]any{"message": map[string]any{"content": text}}},
		"usage":   map[string]int{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
	})
	return string(b)
}

func TestChatRetriesOn5xxThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	g, rec := newTestGateway(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testKey {
			t.Errorf("missing auth header")
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"enable_thinking":false`) {
			t.Errorf("enable_thinking not sent: %s", body)
		}
		if calls.Add(1) <= 2 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		io.WriteString(w, chatOK("hello"))
	})
	resp, err := g.Chat(context.Background(), ChatRequest{Purpose: "t", Messages: []Message{{Text: "hi"}}})
	if err != nil || resp.Text != "hello" || resp.Fallback {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
	l := rec.last(t)
	if l.Attempts != 3 || l.Status != StatusOK || l.Usage.TotalTokens != 15 {
		t.Fatalf("log = %+v", l)
	}
}

func TestChatDoesNotRetry4xxAndFallsBack(t *testing.T) {
	var calls atomic.Int32
	g, rec := newTestGateway(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":"bad"}`)
	})
	resp, err := g.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Text: "hi"}},
		Fallback: func() (string, error) { return "local answer", nil },
	})
	if err != nil || !resp.Fallback || resp.Text != "local answer" {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("400 was retried: %d calls", calls.Load())
	}
	if l := rec.last(t); l.Status != StatusFallback || !strings.Contains(l.Error, "400") {
		t.Fatalf("log = %+v", l)
	}
}

func TestChatGivesUpAfterMaxRetries(t *testing.T) {
	var calls atomic.Int32
	g, rec := newTestGateway(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	_, err := g.Chat(context.Background(), ChatRequest{Messages: []Message{{Text: "hi"}}})
	if err == nil {
		t.Fatal("expected error without fallback")
	}
	if calls.Load() != 4 {
		t.Fatalf("calls = %d, want 1 + 3 retries", calls.Load())
	}
	if l := rec.last(t); l.Status != StatusError || l.Attempts != 4 {
		t.Fatalf("log = %+v", l)
	}
}

func TestChatJSONRetriesWhenReplyIsNotJSON(t *testing.T) {
	var calls atomic.Int32
	g, _ := newTestGateway(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			io.WriteString(w, chatOK("sorry, no json here"))
			return
		}
		io.WriteString(w, chatOK("```json\n{\"n\": 3}\n```"))
	})
	var out struct{ N int }
	if _, err := g.ChatJSON(context.Background(), ChatRequest{Messages: []Message{{Text: "x"}}}, &out); err != nil || out.N != 3 {
		t.Fatalf("out=%+v err=%v", out, err)
	}
}

func TestChatLogHidesImagesAndKey(t *testing.T) {
	g, rec := newTestGateway(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, chatOK("ok")) })
	img := Image{Data: make([]byte, 50_000), MIME: "image/jpeg", Width: 640, Height: 360}
	if _, err := g.Chat(context.Background(), ChatRequest{Messages: []Message{{Text: "describe", Images: []Image{img}}}}); err != nil {
		t.Fatal(err)
	}
	l := rec.last(t)
	if !strings.Contains(l.Input, "[image 640x360 image/jpeg 48KB]") || strings.Contains(l.Input, "base64") {
		t.Fatalf("input summary = %q", l.Input)
	}
	if strings.Contains(fmt.Sprintf("%+v", l), testKey) {
		t.Fatal("API key leaked into call log")
	}
}

func TestEmbedBatchesAndKeepsOrder(t *testing.T) {
	var calls atomic.Int32
	g, _ := newTestGateway(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req struct{ Input []string }
		json.NewDecoder(r.Body).Decode(&req)
		data := []any{}
		for i := len(req.Input) - 1; i >= 0; i-- { // reversed on purpose
			var n float32
			fmt.Sscanf(req.Input[i], "t%f", &n)
			data = append(data, map[string]any{"index": i, "embedding": []float32{n, 0, 0, 0}})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data, "usage": map[string]int{"prompt_tokens": 1, "total_tokens": 1}})
	})
	texts := make([]string, 25)
	for i := range texts {
		texts[i] = fmt.Sprintf("t%d", i)
	}
	res, err := g.Embed(context.Background(), "t", 0, texts)
	if err != nil || res.Fallback {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if calls.Load() != 3 {
		t.Fatalf("calls = %d, want 3 batches", calls.Load())
	}
	for i, v := range res.Vectors {
		if int(v[0]) != i {
			t.Fatalf("vector %d out of order: %v", i, v)
		}
	}
}

func TestEmbedFallbackIsAllOrNothing(t *testing.T) {
	var calls atomic.Int32
	g, _ := newTestGateway(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 { // first batch fine, then the endpoint dies
			data := []any{}
			for i := 0; i < embedBatch; i++ {
				data = append(data, map[string]any{"index": i, "embedding": []float32{1, 0, 0, 0}})
			}
			json.NewEncoder(w).Encode(map[string]any{"data": data})
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	texts := make([]string, 15)
	for i := range texts {
		texts[i] = fmt.Sprintf("text %d", i)
	}
	res, err := g.Embed(context.Background(), "t", 0, texts)
	if err != nil || !res.Fallback || res.Model != FallbackEmbeddingModel || len(res.Vectors) != 15 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	for i, v := range res.Vectors {
		want := HashEmbed(texts[i], 4)
		for j := range v {
			if v[j] != want[j] {
				t.Fatalf("vector %d is not a hash vector", i)
			}
		}
	}
}

func TestDecideParsesAnswers(t *testing.T) {
	g, rec := newTestGateway(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/decisions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		io.WriteString(w, `{"answers":{"q1":{"choice":"three","confidence":0.99,"probabilities":{"one":0.01,"three":0.99},"type":"choice"},
			"q2":{"score":1.5,"probabilities":{"0":0.1,"1":0.3,"2":0.6},"legend":{"0":"a","1":"b","2":"c"},"type":"score"}},
			"model":"m","usage":{"input_tokens":99,"output_tokens":1}}`)
	})
	resp, err := g.Decide(context.Background(), DecisionRequest{State: "s", Questions: map[string]DecisionQuestion{
		"q1": {Type: "choice", Instructions: "how many", Criteria: map[string]string{"one": "1", "three": "3"}},
		"q2": {Type: "score", Instructions: "grade", Criteria: []string{"a", "b", "c"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Answers["q1"].Choice != "three" || resp.Answers["q2"].Score != 1.5 || resp.Usage.TotalTokens != 100 {
		t.Fatalf("resp = %+v", resp)
	}
	if l := rec.last(t); l.Usage.PromptTokens != 99 || !strings.Contains(l.Output, "q1=three") {
		t.Fatalf("log = %+v", l)
	}
}

func TestDecideFallsBackToRules(t *testing.T) {
	g, rec := newTestGateway(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	resp, err := g.Decide(context.Background(), DecisionRequest{
		State: "画面中有一辆公交车停在路口",
		Questions: map[string]DecisionQuestion{"rel": {Type: "choice", Instructions: "这条证据和问题相关吗",
			Criteria: map[string]string{"yes": "相关：提到公交车", "no": "无关：飞机跑道"}}},
	})
	if err != nil || !resp.Fallback || resp.Answers["rel"].Choice != "yes" {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
	if rec.last(t).Status != StatusFallback {
		t.Fatal("fallback not logged")
	}
}

func TestUnconfiguredEndpointUsesFallbackWithoutHTTP(t *testing.T) {
	g, rec := newTestGateway(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected HTTP call") })
	g.emb.disabled = true
	res, err := g.Embed(context.Background(), "t", 0, []string{"a"})
	if err != nil || !res.Fallback {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if l := rec.last(t); l.Status != StatusFallback || !strings.Contains(l.Error, "not configured") {
		t.Fatalf("log = %+v", l)
	}
}

func TestConcurrencyLimit(t *testing.T) {
	var cur, peak atomic.Int32
	g, _ := newTestGateway(t, func(w http.ResponseWriter, r *http.Request) {
		n := cur.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		cur.Add(-1)
		io.WriteString(w, chatOK("ok"))
	})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); g.Chat(context.Background(), ChatRequest{Messages: []Message{{Text: "x"}}}) }()
	}
	wg.Wait()
	if peak.Load() > 2 {
		t.Fatalf("peak concurrency %d exceeds limit 2", peak.Load())
	}
}

func TestHashEmbedRanksLexicalOverlap(t *testing.T) {
	q := HashEmbed("路口有公交车", 256)
	near := HashEmbed("一辆公交车停在路口", 256)
	far := HashEmbed("机场跑道上的飞机", 256)
	dot := func(a, b []float32) (s float32) {
		for i := range a {
			s += a[i] * b[i]
		}
		return
	}
	if dot(q, near) <= dot(q, far) {
		t.Fatalf("hash embedding does not prefer overlapping text: near=%f far=%f", dot(q, near), dot(q, far))
	}
}

func TestExtractJSON(t *testing.T) {
	cases := map[string]string{
		`{"a":1}`:                           `{"a":1}`,
		"Here you go:\n```json\n[1,2]\n```": `[1,2]`,
		`prefix {"s":"has } brace"} suffix`: `{"s":"has } brace"}`,
		`no json`:                           ``,
		`{"broken": }`:                      ``,
	}
	for in, want := range cases {
		if got := extractJSON(in); got != want {
			t.Errorf("extractJSON(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestVisionEmbedBatchesAndNormalizes(t *testing.T) {
	var calls atomic.Int32
	g, rec := newTestGateway(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mm" {
			t.Errorf("path = %s", r.URL.Path)
		}
		calls.Add(1)
		var req struct {
			Model string
			Input struct{ Contents []map[string]string }
		}
		json.NewDecoder(r.Body).Decode(&req)
		emb := []any{}
		for i, c := range req.Input.Contents {
			if c["image"] != "" && !strings.HasPrefix(c["image"], "data:image/jpeg;base64,") {
				t.Errorf("image not sent as data URL: %.40s", c["image"])
			}
			emb = append(emb, map[string]any{"index": len(req.Input.Contents) - 1 - i, "embedding": []float32{3, 4, 0, float32(i)}})
		}
		json.NewEncoder(w).Encode(map[string]any{"output": map[string]any{"embeddings": emb},
			"usage": map[string]int{"input_tokens": 400, "total_tokens": 401}})
	})
	in := []MMInput{{Text: "a"}, {Image: &Image{Data: []byte{1, 2}, Width: 2, Height: 1}}, {Text: "c"}, {Text: "d"}, {Text: "e"}}
	res, err := g.EmbedMultimodal(context.Background(), "t", 0, in)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || len(res.Vectors) != 5 {
		t.Fatalf("calls=%d vectors=%d", calls.Load(), len(res.Vectors))
	}
	var n float32
	for _, x := range res.Vectors[0] {
		n += x * x
	}
	if n < 0.999 || n > 1.001 {
		t.Fatalf("vector not normalised: |v|^2 = %f", n)
	}
	if l := rec.last(t); l.Endpoint != EndpointVision || l.Usage.PromptTokens != 400 {
		t.Fatalf("log = %+v", l)
	}
}

func TestCancelledCallerGetsErrorNotFallback(t *testing.T) {
	started := make(chan struct{})
	g, rec := newTestGateway(t, func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select { // the server may not notice the client hanging up; don't block Close
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-started; cancel() }()
	_, err := g.Chat(ctx, ChatRequest{Messages: []Message{{Text: "x"}},
		Fallback: func() (string, error) { return "should not be used", nil }})
	if err == nil {
		t.Fatal("cancelled call returned a fallback answer")
	}
	if l := rec.last(t); l.Status != StatusError {
		t.Fatalf("log = %+v", l)
	}
}

func TestRepairTruncatedJSON(t *testing.T) {
	cut := `{"description": "停车场", "objects": [{"label": "car", "bbox_2d": [1, 2, 3, 4]}, {"label": "car", "bbox_2d": [5, 6`
	got := repairTruncatedJSON(cut)
	var v struct {
		Description string
		Objects     []struct{ Label string }
	}
	// The half-written second object may survive with a short bbox; the
	// analyzer drops boxes without four coordinates.
	if err := json.Unmarshal([]byte(got), &v); err != nil || v.Description != "停车场" || len(v.Objects) < 1 || v.Objects[0].Label != "car" {
		t.Fatalf("repaired = %q (%v)", got, err)
	}
	if repairTruncatedJSON("no json at all") != "" {
		t.Fatal("garbage repaired")
	}
}

func TestAbnormalJSONOutputIsRetried(t *testing.T) {
	var calls atomic.Int32
	g, _ := newTestGateway(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"message":"Model output became abnormal while generating a JSON response"}}`)
			return
		}
		io.WriteString(w, chatOK(`{"ok": true}`))
	})
	if _, err := g.Chat(context.Background(), ChatRequest{JSON: true, Messages: []Message{{Text: "x"}}}); err != nil || calls.Load() != 2 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}
