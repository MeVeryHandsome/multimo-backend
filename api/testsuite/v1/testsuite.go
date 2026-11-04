package v1

import (
	"github.com/gogf/gf/v2/frame/g"

	"multimo-backend/internal/service/testsuite"
)

type RunReq struct {
	g.Meta   `path:"/testsuite/run" method:"post" tags:"Testsuite" summary:"运行评测" dc:"在后台按 testdata/benchmark 运行评测（需先登记并处理完评测语料）。同一时间只运行一次，进度见 /testsuite/status"`
	Sections []string `json:"sections" v:"foreach|in:formats,keyframe,detection,geo,retrieval,qa" dc:"只运行这些部分，默认全部：formats keyframe detection geo retrieval qa"`
}
type RunRes struct {
	testsuite.Status
}

type StatusReq struct {
	g.Meta `path:"/testsuite/status" method:"get" tags:"Testsuite" summary:"评测进度" dc:"当前运行的进度和最近一次报告的指标"`
}
type StatusRes struct {
	testsuite.Status
}

type ListReq struct {
	g.Meta `path:"/testsuite/reports" method:"get" tags:"Testsuite" summary:"历史报告"`
}
type ListRes struct {
	List []testsuite.RunInfo `json:"list"`
}

type ReportReq struct {
	g.Meta `path:"/testsuite/reports/{id}" method:"get" tags:"Testsuite" summary:"报告（JSON）" dc:"id 为 latest 时返回最近一次"`
	Id     string `json:"id" in:"path" v:"required"`
}
type ReportRes struct {
	*testsuite.Report
}

type ReportFileReq struct {
	g.Meta `path:"/testsuite/reports/{id}/{file}" method:"get" tags:"Testsuite" summary:"报告文件" dc:"file 为 html、md 或 json，直接返回文件内容"`
	Id     string `json:"id"   in:"path" v:"required"`
	File   string `json:"file" in:"path" v:"required|in:html,md,json"`
}
type ReportFileRes struct{}
