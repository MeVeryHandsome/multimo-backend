package system

import (
	"context"

	"github.com/gogf/gf/v2/frame/g"

	v1 "multimo-backend/api/system/v1"
)

func (c *ControllerV1) AICalls(ctx context.Context, req *v1.AICallsReq) (*v1.AICallsRes, error) {
	db := g.DB()
	where, args := "1=1", []any{}
	for col, val := range map[string]string{"endpoint": req.Endpoint, "status": req.Status, "purpose": req.Purpose} {
		if val != "" {
			where += " AND " + col + " = ?"
			args = append(args, val)
		}
	}
	res := &v1.AICallsRes{}
	err := db.GetScan(ctx, &res.Summary, `
		SELECT endpoint, model, count(*) AS calls,
		       count(*) FILTER (WHERE status = 'ok') AS ok,
		       count(*) FILTER (WHERE status = 'fallback') AS fallback,
		       count(*) FILTER (WHERE status = 'error') AS error,
		       coalesce(sum(prompt_tokens), 0) AS prompt_tokens,
		       coalesce(sum(completion_tokens), 0) AS completion_tokens,
		       coalesce(sum(total_tokens), 0) AS total_tokens,
		       round(avg(latency_ms)::numeric, 1) AS avg_latency_ms,
		       percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms) AS p95_latency_ms
		FROM ai_call_log WHERE `+where+`
		GROUP BY endpoint, model ORDER BY endpoint`, args...)
	if err != nil {
		return nil, err
	}
	err = db.Model("ai_call_log").Ctx(ctx).Where(where, args...).OrderDesc("id").Limit(max(req.Limit, 1)).Scan(&res.Recent)
	return res, err
}

func (c *ControllerV1) Stats(ctx context.Context, _ *v1.StatsReq) (*v1.StatsRes, error) {
	db := g.DB()
	res := &v1.StatsRes{}
	for dst, sql := range map[*[]v1.CountRow]string{
		&res.AssetsByType:       "SELECT type AS key, count(*) AS count FROM assets GROUP BY type ORDER BY type",
		&res.AssetsByStatus:     "SELECT status AS key, count(*) AS count FROM assets GROUP BY status ORDER BY status",
		&res.TasksByStatus:      "SELECT status AS key, count(*) AS count FROM tasks GROUP BY status ORDER BY status",
		&res.AssetsByCollection: "SELECT collection AS key, count(*) AS count FROM assets GROUP BY collection ORDER BY collection",
	} {
		if err := db.GetScan(ctx, dst, sql); err != nil {
			return nil, err
		}
	}
	res.DocChunks, _ = db.Model("doc_chunks").Ctx(ctx).Count()
	res.Keyframes, _ = db.Model("keyframes").Ctx(ctx).Count()
	return res, nil
}
