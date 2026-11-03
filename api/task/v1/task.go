package v1

import (
	"github.com/gogf/gf/v2/frame/g"

	"multimo-backend/internal/model/entity"
)

type ListReq struct {
	g.Meta  `path:"/tasks" method:"get" tags:"Task" summary:"处理任务列表"`
	AssetId int64  `json:"assetId"`
	Kind    string `json:"kind"   dc:"ingest 等"`
	Status  string `json:"status" dc:"pending | running | done | failed"`
	Page    int    `json:"page"   d:"1"`
	Size    int    `json:"size"   d:"50" v:"max:500"`
}
type ListRes struct {
	List  []*entity.Task `json:"list"`
	Total int            `json:"total"`
}
