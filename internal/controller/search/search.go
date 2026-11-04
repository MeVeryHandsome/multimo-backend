package search

import (
	"context"

	"github.com/gogf/gf/v2/errors/gcode"
	"github.com/gogf/gf/v2/errors/gerror"

	v1 "multimo-backend/api/search/v1"
	svc "multimo-backend/internal/service/search"
)

type ControllerV1 struct {
	Svc *svc.Service
}

func (c *ControllerV1) Search(ctx context.Context, req *v1.SearchReq) (*v1.SearchRes, error) {
	res, err := c.Svc.Search(ctx, svc.Request{Query: req.Query, Collections: req.Collections, Types: req.Types, AssetIDs: req.AssetIds,
		TopK: req.TopK, GroupBy: req.GroupBy, NoRerank: req.NoRerank, Filters: req.Filters})
	if err != nil {
		return nil, gerror.WrapCode(gcode.CodeInternalError, err, "检索失败")
	}
	return &v1.SearchRes{Response: res}, nil
}
