// Package llm is the single way the backend talks to AI models.
//
// It wraps three endpoints behind one policy:
//
//   - VLM: OpenAI-compatible chat/completions, with image input and JSON mode
//   - Embedding: OpenAI-compatible embeddings
//   - Decision: the StartLux /decisions API (choice and score questions)
//
// Every call gets a per-endpoint concurrency limit, up to 1+MaxRetries attempts
// with exponential backoff on network errors, 429 and 5xx, a local fallback
// when all attempts fail, and one CallLog entry with token usage, latency and
// truncated input/output summaries. API keys never reach the log.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"multimo-backend/internal/config"
)

type Endpoint string

const (
	EndpointVLM       Endpoint = "vlm"
	EndpointEmbedding Endpoint = "embedding"
	EndpointDecision  Endpoint = "decision"
	EndpointVision    Endpoint = "vision_embedding"
)

const (
	StatusOK       = "ok"
	StatusError    = "error"
	StatusFallback = "fallback"
)

// ErrNotConfigured is returned (or triggers the fallback) when an endpoint
// has no URL, key or model.
var ErrNotConfigured = errors.New("endpoint not configured")

type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// CallLog is one AI call as seen by the gateway.
type CallLog struct {
	Endpoint Endpoint
	Model    string
	Purpose  string
	Status   string
	Attempts int
	Latency  time.Duration
	Usage    Usage
	Input    string
	Output   string
	Error    string
	AssetID  int64
}

// Recorder persists call logs. Implementations must be safe for concurrent use.
type Recorder interface {
	Record(ctx context.Context, l CallLog)
}

// HTTPError is a non-2xx response from a model endpoint.
type HTTPError struct {
	Status     int
	Body       string
	RetryAfter time.Duration
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("http %d: %s", e.Status, truncate(e.Body, 300))
}

type Options struct {
	MaxRetries      int
	RetryBase       time.Duration
	FallbackEnabled bool
	LogPayloadChars int
}

type endpoint struct {
	name     Endpoint
	baseURL  string
	path     string
	apiKey   string
	model    string
	timeout  time.Duration
	sem      chan struct{}
	extra    map[string]any // extra JSON fields merged into chat requests
	disabled bool
}

type Gateway struct {
	vlm, emb, dec *endpoint
	vis           *endpoint
	embDim        int
	visDim        int
	opts          Options
	rec           Recorder
	client        *http.Client
	sleep         func(context.Context, time.Duration) error // replaced in tests
}

// New builds a gateway from the process configuration.
func New(cfg *config.Config, rec Recorder) *Gateway {
	mk := func(name Endpoint, e config.Endpoint, path string) *endpoint {
		n := e.MaxConcurrency
		if n <= 0 {
			n = 1
		}
		return &endpoint{name: name, baseURL: strings.TrimRight(e.BaseURL, "/"), path: path, apiKey: e.APIKey,
			model: e.Model, timeout: e.Timeout, sem: make(chan struct{}, n), disabled: !e.Configured()}
	}
	g := &Gateway{
		vlm:    mk(EndpointVLM, cfg.VLM.Endpoint, "/chat/completions"),
		emb:    mk(EndpointEmbedding, cfg.Embedding.Endpoint, "/embeddings"),
		dec:    mk(EndpointDecision, cfg.Decision.Endpoint, cfg.Decision.Path),
		vis:    mk(EndpointVision, cfg.Vision.Endpoint, cfg.Vision.Path),
		embDim: cfg.Embedding.Dim,
		visDim: cfg.Vision.Dim,
		opts: Options{MaxRetries: cfg.AI.MaxRetries, RetryBase: cfg.AI.RetryBase,
			FallbackEnabled: cfg.AI.FallbackEnabled, LogPayloadChars: cfg.AI.LogPayloadChars},
		rec:    rec,
		client: &http.Client{},
		sleep:  sleepCtx,
	}
	switch strings.ToLower(cfg.VLM.EnableThinking) {
	case "true":
		g.vlm.extra = map[string]any{"enable_thinking": true}
	case "false":
		g.vlm.extra = map[string]any{"enable_thinking": false}
	}
	if rec == nil {
		g.rec = LogRecorder{}
	}
	return g
}

// Models reports the configured model names.
func (g *Gateway) Models() (vlm, embedding, decision string) {
	return g.vlm.model, g.emb.model, g.dec.model
}

// EmbeddingDim is the vector size every Embed call returns.
func (g *Gateway) EmbeddingDim() int { return g.embDim }

// callMeta describes one logical call for logging.
type callMeta struct {
	purpose string
	assetID int64
	input   string
}

// attemptResult is what one HTTP attempt produced.
type attemptResult[T any] struct {
	value  T
	usage  Usage
	output string
}

// run executes call with the gateway policy. fallback may be nil.
func run[T any](ctx context.Context, g *Gateway, ep *endpoint, meta callMeta,
	call func(ctx context.Context) (attemptResult[T], error),
	fallback func() (T, string, error),
) (T, bool, error) {
	var zero T
	start := time.Now()
	log := CallLog{Endpoint: ep.name, Model: ep.model, Purpose: meta.purpose, AssetID: meta.assetID,
		Input: truncate(meta.input, g.opts.LogPayloadChars)}

	finish := func(status string, err error) {
		log.Status = status
		log.Latency = time.Since(start)
		if err != nil {
			log.Error = truncate(err.Error(), 500)
		}
		g.rec.Record(context.WithoutCancel(ctx), log)
	}
	useFallback := func(cause error) (T, bool, error) {
		// A cancelled caller (shutdown, client gone) gets an error, never a
		// substitute answer: the work will be redone, not faked.
		if ctx.Err() != nil {
			finish(StatusError, ctx.Err())
			return zero, false, ctx.Err()
		}
		if fallback == nil || !g.opts.FallbackEnabled {
			finish(StatusError, cause)
			return zero, false, cause
		}
		v, out, err := fallback()
		if err != nil {
			finish(StatusError, fmt.Errorf("%v; fallback: %w", cause, err))
			return zero, false, err
		}
		log.Output = truncate(out, g.opts.LogPayloadChars)
		finish(StatusFallback, cause)
		return v, true, nil
	}

	if ep.disabled {
		return useFallback(ErrNotConfigured)
	}

	select {
	case ep.sem <- struct{}{}:
		defer func() { <-ep.sem }()
	case <-ctx.Done():
		return useFallback(ctx.Err())
	}

	var lastErr error
	for attempt := 1; attempt <= 1+max(g.opts.MaxRetries, 0); attempt++ {
		log.Attempts = attempt
		actx, cancel := context.WithTimeout(ctx, ep.timeout)
		res, err := call(actx)
		cancel()
		if err == nil {
			log.Usage = res.usage
			log.Output = truncate(res.output, g.opts.LogPayloadChars)
			finish(StatusOK, nil)
			return res.value, false, nil
		}
		lastErr = err
		if ctx.Err() != nil || !retryable(err) || attempt > g.opts.MaxRetries {
			break
		}
		if serr := g.sleep(ctx, g.backoff(attempt, err)); serr != nil {
			lastErr = serr
			break
		}
	}
	return useFallback(lastErr)
}

func (g *Gateway) backoff(attempt int, err error) time.Duration {
	var he *HTTPError
	if errors.As(err, &he) && he.RetryAfter > 0 {
		return min(he.RetryAfter, 30*time.Second)
	}
	base := g.opts.RetryBase
	if base <= 0 {
		base = 800 * time.Millisecond
	}
	d := base << (attempt - 1)
	return d + time.Duration(rand.Int64N(int64(base/2)+1))
}

// retryable: network trouble, timeouts, 429 and 5xx. Other 4xx mean the
// request itself is wrong and repeating it would not help.
func retryable(err error) bool {
	var he *HTTPError
	if errors.As(err, &he) {
		// DashScope answers 400 when JSON-mode generation derails; a new
		// attempt usually succeeds.
		if he.Status == http.StatusBadRequest && strings.Contains(he.Body, "Model output became abnormal") {
			return true
		}
		return he.Status == http.StatusTooManyRequests || he.Status >= 500
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, errBadResponse)
}

var errBadResponse = errors.New("malformed response")

func (g *Gateway) postJSON(ctx context.Context, ep *endpoint, body any, out any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.baseURL+ep.path, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+ep.apiKey)
	resp, err := g.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		he := &HTTPError{Status: resp.StatusCode, Body: string(data)}
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil {
			he.RetryAfter = time.Duration(s) * time.Second
		}
		return he
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%w: %v: %s", errBadResponse, err, truncate(string(data), 200))
	}
	return nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func truncate(s string, n int) string {
	if n <= 0 {
		n = 200
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func sqrt(x float64) float64 { return math.Sqrt(x) }
