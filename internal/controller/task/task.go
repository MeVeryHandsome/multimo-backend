package task

import (
	"context"

	"github.com/gogf/gf/v2/database/gdb"

	v1 "multimo-backend/api/task/v1"
)

type ControllerV1 struct {
	DB gdb.DB
}

func (c *ControllerV1) List(ctx context.Context, req *v1.ListReq) (*v1.ListRes, error) {
	m := c.DB.Model("tasks").Ctx(ctx)
	if req.AssetId > 0 {
		m = m.Where("asset_id", req.AssetId)
	}
	if req.Kind != "" {
		m = m.Where("kind", req.Kind)
	}
	if req.Status != "" {
		m = m.Where("status", req.Status)
	}
	res := &v1.ListRes{}
	var err error
	if res.Total, err = m.Count(); err != nil {
		return nil, err
	}
	err = m.OrderDesc("id").Page(max(req.Page, 1), max(req.Size, 1)).Scan(&res.List)
	return res, err
}
