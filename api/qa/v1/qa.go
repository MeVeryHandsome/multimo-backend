package v1

import (
	"github.com/gogf/gf/v2/frame/g"

	"multimo-backend/internal/service/qa"
)

type AskReq struct {
	g.Meta      `path:"/qa" method:"post" tags:"QA" summary:"智能问答" dc:"RAG：问题中点名的素材 + 混合检索的关键帧和文档片段作为证据；Decision 模型判断问题类型并筛选证据；VLM 根据证据（视觉类问题附关键帧图片）生成带引用的答案"`
	Question    string   `json:"question"    v:"required#请输入问题"`
	Collections []string `json:"collections" dc:"限定检索范围"`
}

type AskRes struct {
	*qa.Response
}
