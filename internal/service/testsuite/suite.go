// Package testsuite measures Multimo against the competition targets on the
// benchmark in testdata/benchmark: keyframe annotation consistency, object
// detection, localisation accuracy, retrieval precision/recall, QA accuracy
// and data-type coverage. Every reference comes from dataset annotations,
// GPS ground truth or document text, never from Multimo's own output.
package testsuite

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/frame/g"

	"multimo-backend/internal/pkg/llm"
	"multimo-backend/internal/service/qa"
	"multimo-backend/internal/service/search"
)

type Suite struct {
	DB          gdb.DB
	AI          *llm.Gateway
	Search      *search.Service
	QA          *qa.Service
	TestdataDir string
	Version     string
	Concurrency int
}

// Sections that can be run on their own.
var AllSections = []string{"formats", "keyframe", "detection", "geo", "retrieval", "qa"}

// Metric is one line of the summary table.
type Metric struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Value   float64 `json:"value"`
	Display string  `json:"display"`
	Target  string  `json:"target"`
	Pass    bool    `json:"pass"`
	Primary bool    `json:"primary"` // a competition target (others are supporting figures)
	Note    string  `json:"note,omitempty"`
}

type Report struct {
	GeneratedAt time.Time         `json:"generatedAt"`
	DurationSec float64           `json:"durationSec"`
	Version     string            `json:"version"`
	Models      map[string]string `json:"models"`
	Sections    []string          `json:"sections"`
	Metrics     []Metric          `json:"metrics"`
	Formats     *FormatsSection   `json:"formats,omitempty"`
	Keyframe    *KeyframeSection  `json:"keyframe,omitempty"`
	Detection   *DetectionSection `json:"detection,omitempty"`
	Geo         *GeoSection       `json:"geo,omitempty"`
	Retrieval   *RetrievalSection `json:"retrieval,omitempty"`
	QA          *QASection        `json:"qa,omitempty"`
	Definitions map[string]string `json:"definitions"`
	Errors      map[string]string `json:"errors,omitempty"`
}

// AllPassed reports whether every primary metric met its target.
func (r *Report) AllPassed() bool {
	for _, m := range r.Metrics {
		if m.Primary && !m.Pass {
			return false
		}
	}
	return true
}

// Run executes the selected sections (all when only is empty). A failing
// section is recorded in Errors and the others still run.
func (s *Suite) Run(ctx context.Context, only []string, progress func(string)) *Report {
	if progress == nil {
		progress = func(string) {}
	}
	if s.Concurrency <= 0 {
		s.Concurrency = 4
	}
	start := time.Now()
	vlm, emb, dec := s.AI.Models()
	r := &Report{GeneratedAt: start.UTC(), Version: s.Version, Errors: map[string]string{},
		Models:      map[string]string{"vlm": vlm, "embedding": emb, "vision_embedding": s.AI.VisionModel(), "decision": dec},
		Definitions: definitions}
	want := map[string]bool{}
	for _, x := range only {
		want[x] = true
	}
	for _, sec := range AllSections {
		if len(want) > 0 && !want[sec] {
			continue
		}
		progress("running " + sec)
		r.Sections = append(r.Sections, sec)
		var err error
		switch sec {
		case "formats":
			r.Formats, err = s.formats(ctx)
		case "keyframe":
			r.Keyframe, err = s.keyframe(ctx)
		case "detection":
			r.Detection, err = s.detection(ctx)
		case "geo":
			r.Geo, err = s.geo(ctx)
		case "retrieval":
			r.Retrieval, err = s.retrieval(ctx, progress)
		case "qa":
			r.QA, err = s.qa(ctx, progress)
		}
		if err != nil {
			r.Errors[sec] = err.Error()
			g.Log().Warningf(ctx, "testsuite %s: %v", sec, err)
		}
	}
	r.Metrics = collectMetrics(r)
	r.DurationSec = round(time.Since(start).Seconds(), 1)
	return r
}

var definitions = map[string]string{
	"keyframe_consistency": "关键帧自动标注与人工标注的一致性：在 6 段带逐帧标注的视频（VisDrone-VID 4 段、UAVDT 2 段）的每个关键帧上，把 VLM 检测框换算回原始帧坐标，与该帧的可见标注按 IoU≥0.5、同大类贪心匹配，取 F1 = 2TP/(2TP+FP+FN)（所有关键帧合计）。UAVDT 只标注车辆，其他类别的检测不计。语义一致性另以类别集合的 Jaccard 和文本向量余弦给出。",
	"detection":            "VisDrone-DET 验证集 100 张图（每 5 张取 1 张）。预测与可见标注（面积≥600 像素且非重度遮挡）按 IoU≥0.5、类别一致贪心匹配。与不可见标注或忽略区域匹配的预测不计入正确也不计入误报（VisDrone/COCO 惯例）。准确率 = TP/(TP+FP)，漏检率 = FN/(TP+FN)。",
	"geo":                  "UAV-VisLoc 01 区 82 张照片（无 EXIF GPS，只提供拍摄时间和参考底图），系统通过底图视觉匹配和航迹平滑推算的坐标与 CSV 真值的 haversine 距离。达标条件：中位误差 < 100 m。DJI 视频的定位来自配套飞行记录，真值即飞行记录本身，只作为流程验证。",
	"retrieval_precision":  "每条 query 取按素材聚合后的前 10 个结果，准确率 = 前 10 中相关结果数 ÷ min(10, 相关结果总数)；即相关结果只有 1~2 个时，只要它们出现在前 10 名，准确率就是 100%。50 条取平均。",
	"retrieval_recall":     "每条 query 查全率 = 前 max(10, 相关结果总数) 名中的相关结果数 ÷ 相关结果总数；相关结果多于 10 个时，允许系统返回与相关数一样多的结果。50 条取平均。",
	"qa":                   "50 个问答对。坐标类（带 check）按回答中经纬度与真值的距离判定（≤ 容差）；其余由 Decision 模型对照参考答案按 0 错误 / 1 部分正确 / 2 正确打分，期望分 ≥ 1.5 记为正确，部分正确按错误计。评分规则：只评判问题实际询问的内容；参考答案中标为“约”的人工计数允许 ±25% 偏差；超出问题范围的额外细节只有与参考答案矛盾时才扣分。",
	"formats":              "评测语料中各文件格式均成功解析（状态 ready）的数量；达标条件：至少 3 种数据类型。",
}

// ---------------------------------------------------------------- shared helpers

type assetRow struct {
	ID        int64    `orm:"id"`
	Type      string   `orm:"type"`
	Format    string   `orm:"format"`
	Status    string   `orm:"status"`
	Filename  string   `orm:"filename"`
	Path      string   `orm:"path"`
	Width     *int     `orm:"width"`
	Height    *int     `orm:"height"`
	Lat       *float64 `orm:"lat"`
	Lon       *float64 `orm:"lon"`
	GeoSource string   `orm:"geo_source"`
}

// assetsByPath maps testdata-relative registered paths to assets.
func (s *Suite) assetsByPath(ctx context.Context) (map[string]*assetRow, error) {
	var rows []*assetRow
	if err := s.DB.Model("assets").Ctx(ctx).Fields("id, type, format, status, filename, meta->>'registeredPath' AS path, " +
		"width, height, lat, lon, geo_source").Scan(&rows); err != nil {
		return nil, err
	}
	out := make(map[string]*assetRow, len(rows))
	for _, r := range rows {
		if r.Path != "" {
			out[r.Path] = r
		}
	}
	return out, nil
}

// readJSONL decodes one JSON object per line from testdata/benchmark/name.
func readJSONL[T any](s *Suite, name string) ([]T, error) {
	f, err := os.Open(filepath.Join(s.TestdataDir, "benchmark", name))
	if err != nil {
		return nil, fmt.Errorf("benchmark file %s: %w (run scripts/lib/build_benchmark.py)", name, err)
	}
	defer f.Close()
	var out []T
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var v T
		if err := json.Unmarshal(sc.Bytes(), &v); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		out = append(out, v)
	}
	return out, sc.Err()
}

func (s *Suite) path(rel string) string { return filepath.Join(s.TestdataDir, rel) }

func pct(x float64) string { return fmt.Sprintf("%.1f%%", 100*x) }
