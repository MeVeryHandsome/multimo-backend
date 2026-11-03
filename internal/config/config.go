// Package config loads runtime settings from environment variables.
//
// Every value that differs between machines (endpoints, keys, model names,
// concurrency limits) comes from the environment. Nothing secret is read from
// manifest/config/config.yaml, which only holds GoFrame server/logger options.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Endpoint struct {
	BaseURL string
	APIKey  string
	Model   string
	Timeout time.Duration
	// MaxConcurrency caps in-flight requests to this endpoint.
	MaxConcurrency int
}

// Configured reports whether the endpoint has enough settings to be called.
func (e Endpoint) Configured() bool {
	return e.BaseURL != "" && e.APIKey != "" && e.Model != ""
}

type VLM struct {
	Endpoint
	// EnableThinking is sent as enable_thinking ("true"/"false"); empty omits
	// the parameter for servers that do not know it. Thinking off keeps
	// per-frame description at a few seconds on qwen3.8-max.
	EnableThinking string
}

type Embedding struct {
	Endpoint
	Dim int
}

// VisionEmbedding is Aliyun's multimodal embedding (DashScope native API):
// images and text share one vector space.
type VisionEmbedding struct {
	Endpoint
	Path string
	Dim  int
}

type Geo struct {
	// MaxSpeedMPS bounds how far a drone can move between two photos when
	// smoothing a visually located track.
	MaxSpeedMPS float64
	// TileScales are the basemap tile sides (metres) indexed for matching.
	TileScales []int
}

type Decision struct {
	Endpoint
	// Path is appended to BaseURL; the StartLux decision API lives at /decisions.
	Path string
}

type AIPolicy struct {
	MaxRetries      int
	RetryBase       time.Duration
	FallbackEnabled bool
	// LogPayloadChars truncates prompt/response summaries in AI call logs.
	LogPayloadChars int
}

type Database struct {
	Host     string
	Port     int
	User     string
	Password string
	Name     string
	SSLMode  string
}

type Storage struct {
	DataDir     string
	TestdataDir string
	MaxUploadMB int
}

type Video struct {
	WorkerPoolSize int
	FFmpegThreads  int
	// ChangeThreshold is the mean absolute difference (0..1) between 64x36
	// grey signatures that makes a frame a new keyframe. Continuous drone
	// footage has no cuts, so ffmpeg scene scores stay near 0.01; content
	// change works instead.
	ChangeThreshold    float64
	MaxKeyframes       int
	MinKeyframeGapSecs float64
	MaxKeyframeGapSecs float64
	// KeyframeWidth is the stored keyframe width in pixels.
	KeyframeWidth int
}

type Config struct {
	HTTPAddr  string
	Database  Database
	Storage   Storage
	VLM       VLM
	Embedding Embedding
	Decision  Decision
	Vision    VisionEmbedding
	Geo       Geo
	AI        AIPolicy
	Video     Video
}

var (
	once sync.Once
	cfg  *Config
)

// Get returns the process-wide configuration, loading it on first use.
func Get() *Config {
	once.Do(func() { cfg = Load() })
	return cfg
}

// Load reads configuration from the environment. Exported for tests.
func Load() *Config {
	vlm := Endpoint{
		BaseURL:        strings.TrimRight(env("VLM_BASE_URL", ""), "/"),
		APIKey:         env("VLM_API_KEY", ""),
		Model:          env("VLM_MODEL", "qwen3.8-max"),
		Timeout:        time.Duration(envInt("VLM_TIMEOUT_SEC", 120)) * time.Second,
		MaxConcurrency: envInt("VLM_MAX_CONCURRENCY", 4),
	}
	return &Config{
		HTTPAddr: env("HTTP_ADDR", ":8000"),
		Database: Database{
			Host:     env("POSTGRES_HOST", "127.0.0.1"),
			Port:     envInt("POSTGRES_PORT", 5432),
			User:     env("POSTGRES_USER", "multimo"),
			Password: env("POSTGRES_PASSWORD", ""),
			Name:     env("POSTGRES_DB", "multimo"),
			SSLMode:  env("POSTGRES_SSLMODE", "disable"),
		},
		Storage: Storage{
			DataDir:     env("DATA_DIR", "./data"),
			TestdataDir: env("TESTDATA_DIR", "../testdata"),
			MaxUploadMB: envInt("MAX_UPLOAD_MB", 4096),
		},
		VLM: VLM{Endpoint: vlm, EnableThinking: env("VLM_ENABLE_THINKING", "false")},
		Embedding: Embedding{
			Endpoint: Endpoint{
				// Embeddings default to the VLM endpoint: Aliyun serves both.
				BaseURL:        strings.TrimRight(env("EMBEDDING_BASE_URL", vlm.BaseURL), "/"),
				APIKey:         env("EMBEDDING_API_KEY", vlm.APIKey),
				Model:          env("EMBEDDING_MODEL", "text-embedding-v4"),
				Timeout:        time.Duration(envInt("EMBEDDING_TIMEOUT_SEC", 30)) * time.Second,
				MaxConcurrency: envInt("EMBEDDING_MAX_CONCURRENCY", 8),
			},
			Dim: envInt("EMBEDDING_DIM", 1024),
		},
		Decision: Decision{
			Endpoint: Endpoint{
				BaseURL:        strings.TrimRight(env("DECISION_BASE_URL", "https://api.bamboonode.dev/v1"), "/"),
				APIKey:         env("DECISION_API_KEY", ""),
				Model:          env("DECISION_MODEL", "startlux-decision-27b-autothink"),
				Timeout:        time.Duration(envInt("DECISION_TIMEOUT_SEC", 90)) * time.Second,
				MaxConcurrency: envInt("DECISION_MAX_CONCURRENCY", 4),
			},
			Path: env("DECISION_PATH", "/decisions"),
		},
		Vision: VisionEmbedding{
			Endpoint: Endpoint{
				BaseURL:        strings.TrimRight(env("VISION_EMBEDDING_BASE_URL", strings.TrimSuffix(vlm.BaseURL, "/compatible-mode/v1")), "/"),
				APIKey:         env("VISION_EMBEDDING_API_KEY", vlm.APIKey),
				Model:          env("VISION_EMBEDDING_MODEL", "tongyi-embedding-vision-plus-2026-03-06"),
				Timeout:        time.Duration(envInt("VISION_EMBEDDING_TIMEOUT_SEC", 60)) * time.Second,
				MaxConcurrency: envInt("VISION_EMBEDDING_MAX_CONCURRENCY", 6),
			},
			Path: env("VISION_EMBEDDING_PATH", "/api/v1/services/embeddings/multimodal-embedding/multimodal-embedding"),
			Dim:  envInt("VISION_EMBEDDING_DIM", 1152),
		},
		Geo: Geo{
			MaxSpeedMPS: envFloat("GEO_MAX_SPEED_MPS", 25),
			TileScales:  envInts("GEO_TILE_SCALES", []int{250, 300, 400}),
		},
		AI: AIPolicy{
			MaxRetries:      envInt("AI_MAX_RETRIES", 3),
			RetryBase:       time.Duration(envInt("AI_RETRY_BASE_MS", 800)) * time.Millisecond,
			FallbackEnabled: envBool("AI_FALLBACK_ENABLED", true),
			LogPayloadChars: envInt("AI_LOG_PAYLOAD_CHARS", 200),
		},
		Video: Video{
			WorkerPoolSize:     envInt("WORKER_POOL_SIZE", 4),
			FFmpegThreads:      envInt("FFMPEG_THREADS", 2),
			ChangeThreshold:    envFloat("KEYFRAME_CHANGE_THRESHOLD", 0.08),
			MaxKeyframes:       envInt("KEYFRAME_MAX_PER_VIDEO", 60),
			MinKeyframeGapSecs: envFloat("KEYFRAME_MIN_INTERVAL_SEC", 2),
			MaxKeyframeGapSecs: envFloat("KEYFRAME_MAX_INTERVAL_SEC", 15),
			KeyframeWidth:      envInt("KEYFRAME_WIDTH", 1280),
		},
	}
}

// Missing lists required variables that are unset, for startup diagnostics.
func (c *Config) Missing() []string {
	out := []string{}
	check := func(name, v string) {
		if v == "" {
			out = append(out, name)
		}
	}
	check("POSTGRES_PASSWORD", c.Database.Password)
	check("VLM_BASE_URL", c.VLM.BaseURL)
	check("VLM_API_KEY", c.VLM.APIKey)
	check("DECISION_API_KEY", c.Decision.APIKey)
	return out
}

func (d Database) String() string {
	return fmt.Sprintf("%s@%s:%d/%s", d.User, d.Host, d.Port, d.Name)
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func envInt(key string, def int) int {
	if v, err := strconv.Atoi(env(key, "")); err == nil {
		return v
	}
	return def
}

func envFloat(key string, def float64) float64 {
	if v, err := strconv.ParseFloat(env(key, ""), 64); err == nil {
		return v
	}
	return def
}

func envInts(key string, def []int) []int {
	raw := env(key, "")
	if raw == "" {
		return def
	}
	var out []int
	for _, p := range strings.Split(raw, ",") {
		if v, err := strconv.Atoi(strings.TrimSpace(p)); err == nil && v > 0 {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return def
	}
	return out
}

func envBool(key string, def bool) bool {
	if v, err := strconv.ParseBool(env(key, "")); err == nil {
		return v
	}
	return def
}
