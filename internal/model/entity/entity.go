// Package entity mirrors the database rows the services work with.
package entity

import (
	"github.com/gogf/gf/v2/frame/g"
	"github.com/gogf/gf/v2/os/gtime"
)

type Asset struct {
	Id           int64       `json:"id"           orm:"id"`
	Uid          string      `json:"uid"          orm:"uid"`
	Type         string      `json:"type"         orm:"type"`
	Format       string      `json:"format"       orm:"format"`
	Collection   string      `json:"collection"   orm:"collection"`
	Filename     string      `json:"filename"     orm:"filename"`
	StoragePath  string      `json:"-"            orm:"storage_path"`
	Source       string      `json:"source"       orm:"source"`
	SidecarPath  string      `json:"-"            orm:"sidecar_path"`
	SizeBytes    int64       `json:"sizeBytes"    orm:"size_bytes"`
	Md5          string      `json:"md5"          orm:"md5"`
	Status       string      `json:"status"       orm:"status"`
	Error        string      `json:"error,omitempty" orm:"error"`
	Meta         g.Map       `json:"meta"         orm:"meta"`
	Width        *int        `json:"width"        orm:"width"`
	Height       *int        `json:"height"       orm:"height"`
	DurationSec  *float64    `json:"durationSec"  orm:"duration_sec"`
	Fps          *float64    `json:"fps"          orm:"fps"`
	PageCount    *int        `json:"pageCount"    orm:"page_count"`
	CapturedAt   *gtime.Time `json:"capturedAt"   orm:"captured_at"`
	Lat          *float64    `json:"lat"          orm:"lat"`
	Lon          *float64    `json:"lon"          orm:"lon"`
	GeoSource    string      `json:"geoSource,omitempty" orm:"geo_source"`
	GeoAccuracyM *float64    `json:"geoAccuracyM" orm:"geo_accuracy_m"`
	CreatedAt    *gtime.Time `json:"createdAt"    orm:"created_at"`
	UpdatedAt    *gtime.Time `json:"updatedAt"    orm:"updated_at"`
}

type Task struct {
	Id          int64       `json:"id"          orm:"id"`
	AssetId     int64       `json:"assetId"     orm:"asset_id"`
	Kind        string      `json:"kind"        orm:"kind"`
	Status      string      `json:"status"      orm:"status"`
	Attempts    int         `json:"attempts"    orm:"attempts"`
	MaxAttempts int         `json:"maxAttempts" orm:"max_attempts"`
	Progress    float64     `json:"progress"    orm:"progress"`
	Message     string      `json:"message,omitempty" orm:"message"`
	Error       string      `json:"error,omitempty"   orm:"error"`
	StartedAt   *gtime.Time `json:"startedAt"   orm:"started_at"`
	FinishedAt  *gtime.Time `json:"finishedAt"  orm:"finished_at"`
	CreatedAt   *gtime.Time `json:"createdAt"   orm:"created_at"`
	UpdatedAt   *gtime.Time `json:"updatedAt"   orm:"updated_at"`
}

type AICall struct {
	Id               int64       `json:"id"               orm:"id"`
	Endpoint         string      `json:"endpoint"         orm:"endpoint"`
	Model            string      `json:"model"            orm:"model"`
	Purpose          string      `json:"purpose"          orm:"purpose"`
	Status           string      `json:"status"           orm:"status"`
	Attempts         int         `json:"attempts"         orm:"attempts"`
	LatencyMs        int         `json:"latencyMs"        orm:"latency_ms"`
	PromptTokens     int         `json:"promptTokens"     orm:"prompt_tokens"`
	CompletionTokens int         `json:"completionTokens" orm:"completion_tokens"`
	TotalTokens      int         `json:"totalTokens"      orm:"total_tokens"`
	InputSummary     string      `json:"inputSummary"     orm:"input_summary"`
	OutputSummary    string      `json:"outputSummary"    orm:"output_summary"`
	Error            string      `json:"error,omitempty"  orm:"error"`
	AssetId          *int64      `json:"assetId"          orm:"asset_id"`
	CreatedAt        *gtime.Time `json:"createdAt"        orm:"created_at"`
}
