package v1

import (
	"github.com/gogf/gf/v2/frame/g"

	"multimo-backend/internal/model/entity"
)

type AICallsReq struct {
	g.Meta   `path:"/system/ai-calls" method:"get" tags:"System" summary:"AI 调用记录" dc:"按端点汇总 token、延迟、降级次数，并返回最近的调用"`
	Endpoint string `json:"endpoint" dc:"vlm | embedding | decision"`
	Status   string `json:"status"   dc:"ok | error | fallback"`
	Purpose  string `json:"purpose"`
	Limit    int    `json:"limit"    d:"50" v:"max:500"`
}
type AICallSummary struct {
	Endpoint         string  `json:"endpoint"`
	Model            string  `json:"model"`
	Calls            int     `json:"calls"`
	Ok               int     `json:"ok"`
	Fallback         int     `json:"fallback"`
	Error            int     `json:"error"`
	PromptTokens     int64   `json:"promptTokens"`
	CompletionTokens int64   `json:"completionTokens"`
	TotalTokens      int64   `json:"totalTokens"`
	AvgLatencyMs     float64 `json:"avgLatencyMs"`
	P95LatencyMs     float64 `json:"p95LatencyMs"`
}
type AICallsRes struct {
	Summary []AICallSummary  `json:"summary"`
	Recent  []*entity.AICall `json:"recent"`
}

type StatsReq struct {
	g.Meta `path:"/system/stats" method:"get" tags:"System" summary:"数据概况"`
}
type CountRow struct {
	Key   string `json:"key"`
	Count int    `json:"count"`
}
type StatsRes struct {
	AssetsByType   []CountRow `json:"assetsByType"`
	AssetsByStatus []CountRow `json:"assetsByStatus"`
	TasksByStatus  []CountRow `json:"tasksByStatus"`
	DocChunks      int        `json:"docChunks"`
	Keyframes      int        `json:"keyframes"`
}
