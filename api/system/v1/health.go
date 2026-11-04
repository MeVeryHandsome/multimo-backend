package v1

import "github.com/gogf/gf/v2/frame/g"

type HealthReq struct {
	g.Meta `path:"/system/health" method:"get" tags:"System" summary:"服务健康检查" dc:"返回服务版本、数据库连通性以及当前配置的模型名称（不含密钥）"`
}

type ModelInfo struct {
	VLM          string `json:"vlm"          dc:"视觉模型"`
	Embedding    string `json:"embedding"    dc:"向量模型"`
	EmbeddingDim int    `json:"embeddingDim" dc:"向量维度"`
	Decision     string `json:"decision"     dc:"决策模型"`
	Vision       string `json:"vision"       dc:"图像向量模型"`
	Detector     string `json:"detector"     dc:"目标检测模型（未配置时为空，由 VLM 画框）"`
}

type HealthRes struct {
	Status   string    `json:"status"   dc:"ok | degraded"`
	Version  string    `json:"version"`
	Database string    `json:"database" dc:"up | down"`
	Models   ModelInfo `json:"models"`
	Missing  []string  `json:"missing"  dc:"未设置的必需环境变量"`
	Time     string    `json:"time"`
}
