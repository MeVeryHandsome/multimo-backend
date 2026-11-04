package v1

import (
	"github.com/gogf/gf/v2/frame/g"

	"multimo-backend/internal/service/search"
)

type SearchReq struct {
	g.Meta      `path:"/search" method:"post" tags:"Search" summary:"混合检索" dc:"关键词（BM25）+ 文本向量 + 以文搜图向量 + 时空过滤，RRF 融合后由 Decision 模型判断相关性。查询中的经纬度、半径、经纬度范围、日期时间和地名会自动解析为过滤条件"`
	Query       string          `json:"query"       v:"required#请输入检索内容" dc:"自然语言检索语句"`
	Collections []string        `json:"collections" dc:"限定集合"`
	Types       []string        `json:"types"       dc:"限定类型：video | image | remote_sensing | document"`
	AssetIds    []int64         `json:"assetIds"    dc:"限定素材（如在某个文档内检索）"`
	TopK        int             `json:"topK"        d:"10" v:"max:100"`
	GroupBy     string          `json:"groupBy"     d:"asset" v:"in:asset,unit" dc:"asset：每个素材只返回最相关的一条；unit：返回关键帧/文档片段"`
	NoRerank    bool            `json:"noRerank"    dc:"跳过 Decision 精排"`
	Filters     *search.Filters `json:"filters"     dc:"显式过滤条件，优先于从查询中解析出的条件"`
}

type SearchRes struct {
	*search.Response
}
