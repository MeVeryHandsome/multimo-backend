package testsuite

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Report file names inside a run directory.
const (
	JSONFile     = "test_report.json"
	HTMLFile     = "test_report.html"
	MarkdownFile = "test_report.md"
)

//go:embed report.html.tmpl
var htmlTemplate string

var tmpl = template.Must(template.New("report").Funcs(template.FuncMap{
	"pct": pct,
	"f":   func(prec int, x float64) string { return fmt.Sprintf("%.*f", prec, x) },
	"verdict": func(pass bool) string {
		if pass {
			return "达标"
		}
		return "未达标"
	},
	"def": func(r *Report, k string) string { return r.Definitions[k] },
	"modelName": func(k string) string {
		return map[string]string{"vlm": "视觉模型", "detector": "目标检测", "embedding": "文本向量", "vision_embedding": "图像向量", "decision": "决策模型"}[k]
	},
	"time":  func(r *Report) string { return r.GeneratedAt.Local().Format("2006-01-02 15:04:05 MST") },
	"base":  filepath.Base,
	"hit":   func(p string) bool { return strings.HasPrefix(p, "*") },
	"strip": func(p string) string { return strings.TrimPrefix(p, "*") },
	"meters": func(p *float64) string {
		if p == nil {
			return "—"
		}
		return fmt.Sprintf("%.0f m", *p)
	},
	"grade": func(p *float64) string {
		if p == nil {
			return "—"
		}
		return fmt.Sprintf("%.2f", *p)
	},
	"primary": func(ms []Metric) (n [2]int) {
		for _, m := range ms {
			if m.Primary {
				n[1]++
				if m.Pass {
					n[0]++
				}
			}
		}
		return n
	},
	"models": func(m map[string]string) [][2]string {
		var out [][2]string
		for _, k := range []string{"vlm", "detector", "embedding", "vision_embedding", "decision"} {
			if v, ok := m[k]; ok {
				out = append(out, [2]string{k, v})
			}
		}
		return out
	},
}).Parse(htmlTemplate))

// HTML renders the self-contained report page.
func (r *Report) HTML() ([]byte, error) {
	var b bytes.Buffer
	if err := tmpl.Execute(&b, r); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// Save writes the JSON, HTML and Markdown reports into dir.
func (r *Report) Save(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	js, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	page, err := r.HTML()
	if err != nil {
		return err
	}
	for name, data := range map[string][]byte{JSONFile: js, HTMLFile: page, MarkdownFile: []byte(r.Markdown())} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// Load reads a saved JSON report.
func Load(dir string) (*Report, error) {
	b, err := os.ReadFile(filepath.Join(dir, JSONFile))
	if err != nil {
		return nil, err
	}
	var r Report
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// Markdown renders the summary and the per-section tables (per-item lists
// are limited to failures, the full detail lives in the JSON/HTML).
func (r *Report) Markdown() string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	cell := func(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "|", "\\|"), "\n", " ") }
	n := 0
	passed := 0
	for _, m := range r.Metrics {
		if m.Primary {
			n++
			if m.Pass {
				passed++
			}
		}
	}
	w("# Multimo 评测报告\n\n")
	w("生成时间：%s；版本：%s；耗时 %.0f 秒；核心指标 %d/%d 达标。\n\n", r.GeneratedAt.Local().Format("2006-01-02 15:04 MST"), r.Version, r.DurationSec, passed, n)
	w("| 指标 | 结果 | 目标 | 结论 | 说明 |\n|---|---|---|---|---|\n")
	for _, m := range r.Metrics {
		v := "参考"
		if m.Primary {
			v = map[bool]string{true: "达标", false: "**未达标**"}[m.Pass]
		}
		w("| %s | %s | %s | %s | %s |\n", cell(m.Name), cell(m.Display), m.Target, v, cell(m.Note))
	}
	w("\n模型：")
	var ms []string
	for _, kv := range [][2]string{{"VLM", r.Models["vlm"]}, {"Detector", r.Models["detector"]}, {"Embedding", r.Models["embedding"]}, {"Vision embedding", r.Models["vision_embedding"]}, {"Decision", r.Models["decision"]}} {
		if kv[1] != "" {
			ms = append(ms, fmt.Sprintf("%s `%s`", kv[0], kv[1]))
		}
	}
	w("%s\n", strings.Join(ms, "，"))
	if len(r.Errors) > 0 {
		w("\n**运行错误**\n\n")
		keys := make([]string, 0, len(r.Errors))
		for k := range r.Errors {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			w("- %s：%s\n", k, cell(r.Errors[k]))
		}
	}

	if f := r.Formats; f != nil {
		w("\n## 数据类型\n\n| 类型 | 格式 | 解析成功 | 失败 |\n|---|---|---|---|\n")
		for _, x := range f.Rows {
			w("| %s | %s | %d | %d |\n", x.Type, x.Format, x.Ready, x.Failed)
		}
	}
	if k := r.Keyframe; k != nil {
		w("\n## 关键帧自动标注一致性\n\n%s\n\n", r.Definitions["keyframe_consistency"])
		w("| 视频 | 关键帧 | 已评测 | 准确率 | 召回率 | F1 | 类别 Jaccard | 语义余弦 |\n|---|---|---|---|---|---|---|---|\n")
		for _, v := range k.Videos {
			w("| %s | %d | %d | %s | %s | %s | %.3f | %.3f |\n", v.ID, v.Keyframes, v.Evaluated, pct(v.Precision), pct(v.Recall), pct(v.F1), v.Jaccard, v.Cosine)
		}
		w("| **合计** | %d | | %s | %s | **%s** | %.3f | %.3f |\n", k.Keyframes, pct(k.Match.Precision()), pct(k.Match.Recall()), pct(k.F1), k.Jaccard, k.Cosine)
	}
	if d := r.Detection; d != nil {
		w("\n## 目标识别\n\n%s\n\n", r.Definitions["detection"])
		w("| 口径 | TP | FP | FN | 忽略 | 准确率 | 漏检率 |\n|---|---|---|---|---|---|---|\n")
		for _, x := range []struct {
			name string
			m    Match
		}{{"类别一致", d.Exact}, {"同大类", d.Grouped}} {
			w("| %s | %d | %d | %d | %d | %s | %s |\n", x.name, x.m.TP, x.m.FP, x.m.FN, x.m.Ignored, pct(x.m.Precision()), pct(x.m.MissRate()))
		}
		w("\n| 类别 | 标注数 | 预测数 | 准确率 | 召回率 |\n|---|---|---|---|---|\n")
		for _, c := range d.PerClass {
			w("| %s | %d | %d | %s | %s |\n", c.Label, c.GT, c.Pred, pct(c.Precision), pct(c.Recall))
		}
	}
	if g := r.Geo; g != nil {
		w("\n## 空间对齐\n\n%s\n\n", r.Definitions["geo"])
		s := g.Visual
		w("| 已定位 | 中位误差 | 平均误差 | P90 | 最大 | <100 m | <250 m |\n|---|---|---|---|---|---|---|\n")
		w("| %d/%d | %.0f m | %.0f m | %.0f m | %.0f m | %s | %s |\n", g.Located, g.Total, s.Median, s.Mean, s.P90, s.Max, pct(g.Within100), pct(g.Within250))
		if d := g.DJI; d != nil {
			w("\nDJI 视频：%d 个关键帧，%d 个由飞行记录定位（%s），与独立插值的最大偏差 %.2f m。\n", d.Keyframes, d.FromTelemetry, d.Alignment, d.MaxDeviationM)
		}
	}
	if q := r.Retrieval; q != nil {
		w("\n## 检索\n\n%s\n\n%s\n\n", r.Definitions["retrieval_precision"], r.Definitions["retrieval_recall"])
		w("| 类型 | 数量 | 准确率 | 查全率 | 判为相关的精确率 | 判为相关的查全率 |\n|---|---|---|---|---|---|\n")
		for _, t := range q.ByType {
			w("| %s | %d | %s | %s | %s | %s |\n", t.Type, t.N, pct(t.Precision), pct(t.Recall), pct(t.SetP), pct(t.SetR))
		}
		w("| **全部** | %d | **%s** | **%s** | %s | %s |\n", len(q.Queries), pct(q.Precision), pct(q.Recall), pct(q.SetP), pct(q.SetR))
		w("\nP@5 %.3f，P@10 %.3f，R@5 %.3f，R@10 %.3f；延迟中位 %.0f ms，P90 %.0f ms。\n", q.P5, q.P10, q.R5, q.R10, q.Latency.Median, q.Latency.P90)
		var weak []QueryResult
		for _, x := range q.Queries {
			if x.Precision < 1 || x.Recall < 1 {
				weak = append(weak, x)
			}
		}
		if len(weak) > 0 {
			w("\n未满分的 query：\n\n| ID | 类型 | Query | 相关数 | 准确率 | 查全率 |\n|---|---|---|---|---|---|\n")
			for _, x := range weak {
				w("| %s | %s | %s | %d | %s | %s |\n", x.ID, x.Type, cell(x.Query), x.Relevant, pct(x.Precision), pct(x.Recall))
			}
		}
	}
	if a := r.QA; a != nil {
		w("\n## 智能问答\n\n%s\n\n", r.Definitions["qa"])
		w("| 类别 | 数量 | 正确 | 准确率 |\n|---|---|---|---|\n")
		for _, c := range a.ByCategory {
			w("| %s | %d | %d | %s |\n", c.Category, c.N, c.Correct, pct(c.Accuracy))
		}
		w("| **全部** | %d | %d | **%s** |\n", a.Total, a.Correct, pct(a.Accuracy))
		var wrong []QAItem
		for _, x := range a.Items {
			if !x.Correct {
				wrong = append(wrong, x)
			}
		}
		if len(wrong) > 0 {
			w("\n判为错误的问题：\n\n| ID | 类别 | 问题 | 参考答案 | 系统回答 | 判定 |\n|---|---|---|---|---|---|\n")
			for _, x := range wrong {
				how := "评分 " + fmtPtr(x.Grade, "%.2f")
				if x.Method == "geo" {
					how = "误差 " + fmtPtr(x.GeoErrorM, "%.0f m")
				}
				if x.Error != "" {
					how = "错误：" + x.Error
				}
				w("| %s | %s | %s | %s | %s | %s |\n", x.ID, x.Category, cell(x.Question), cell(clip(x.Reference, 120)), cell(clip(x.Answer, 160)), cell(how))
			}
		}
	}
	return b.String()
}

func fmtPtr(p *float64, format string) string {
	if p == nil {
		return "—"
	}
	return fmt.Sprintf(format, *p)
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
