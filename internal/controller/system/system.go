package system

import (
	"context"
	"time"

	"github.com/gogf/gf/v2/frame/g"

	v1 "multimo-backend/api/system/v1"
	"multimo-backend/internal/config"
	"multimo-backend/internal/consts"
)

type ControllerV1 struct{}

func NewV1() *ControllerV1 { return &ControllerV1{} }

func (c *ControllerV1) Health(ctx context.Context, _ *v1.HealthReq) (*v1.HealthRes, error) {
	cfg := config.Get()
	res := &v1.HealthRes{
		Status:   "ok",
		Version:  consts.Version,
		Database: "up",
		Models: v1.ModelInfo{
			VLM:          cfg.VLM.Model,
			Embedding:    cfg.Embedding.Model,
			EmbeddingDim: cfg.Embedding.Dim,
			Decision:     cfg.Decision.Model,
			Vision:       cfg.Vision.Model,
		},
		Missing: cfg.Missing(),
		Time:    time.Now().Format(time.RFC3339),
	}
	if cfg.Detector.Enabled() {
		res.Models.Detector = cfg.Detector.Model
	}

	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if master, err := g.DB().Master(); err != nil || master.PingContext(pingCtx) != nil {
		res.Database = "down"
	}
	if res.Database != "up" || len(res.Missing) > 0 {
		res.Status = "degraded"
	}
	return res, nil
}
