package llm

import (
	"context"

	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/frame/g"
)

// LogRecorder writes call logs to the process log only.
type LogRecorder struct{}

func (LogRecorder) Record(ctx context.Context, l CallLog) {
	line := "ai %s model=%s purpose=%s status=%s attempts=%d latency=%dms tokens=%d/%d/%d"
	args := []any{l.Endpoint, l.Model, l.Purpose, l.Status, l.Attempts, l.Latency.Milliseconds(),
		l.Usage.PromptTokens, l.Usage.CompletionTokens, l.Usage.TotalTokens}
	if l.Error != "" {
		g.Log().Warningf(ctx, line+" error=%q", append(args, l.Error)...)
		return
	}
	g.Log().Infof(ctx, line, args...)
}

// DBRecorder stores call logs in ai_call_log and also writes the log line.
type DBRecorder struct {
	DB gdb.DB
}

func (r DBRecorder) Record(ctx context.Context, l CallLog) {
	LogRecorder{}.Record(ctx, l)
	var assetID any
	if l.AssetID > 0 {
		assetID = l.AssetID
	}
	_, err := r.DB.Model("ai_call_log").Ctx(ctx).Insert(g.Map{
		"endpoint":          string(l.Endpoint),
		"model":             l.Model,
		"purpose":           l.Purpose,
		"status":            l.Status,
		"attempts":          l.Attempts,
		"latency_ms":        l.Latency.Milliseconds(),
		"prompt_tokens":     l.Usage.PromptTokens,
		"completion_tokens": l.Usage.CompletionTokens,
		"total_tokens":      l.Usage.TotalTokens,
		"input_summary":     l.Input,
		"output_summary":    l.Output,
		"error":             l.Error,
		"asset_id":          assetID,
	})
	if err != nil {
		g.Log().Warningf(ctx, "record ai call: %v", err)
	}
}
