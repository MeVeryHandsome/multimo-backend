package testsuite

import "fmt"

// collectMetrics builds the summary table: primary metrics are the
// competition targets, the rest give context.
func collectMetrics(r *Report) []Metric {
	var m []Metric
	if f := r.Formats; f != nil {
		m = append(m, Metric{ID: "data_types", Name: "支持的数据类型", Value: float64(f.Types),
			Display: fmt.Sprintf("%d 种类型 / %d 种格式", f.Types, f.Formats), Target: "≥ 3 种", Pass: f.Types >= 3, Primary: true})
	}
	if k := r.Keyframe; k != nil {
		m = append(m,
			Metric{ID: "keyframe_consistency", Name: "关键帧自动标注一致性", Value: k.F1, Display: pct(k.F1),
				Target: "≥ 40%", Pass: k.F1 >= 0.40, Primary: true,
				Note: fmt.Sprintf("%d 个关键帧，IoU≥0.5 同大类 F1", k.Keyframes)},
			Metric{ID: "keyframe_semantic", Name: "关键帧类别语义相似度", Value: k.Cosine, Display: fmt.Sprintf("%.3f", k.Cosine),
				Target: "—", Pass: true, Note: fmt.Sprintf("类别集合 Jaccard %.3f", k.Jaccard)})
	}
	if d := r.Detection; d != nil {
		m = append(m,
			Metric{ID: "detection_precision", Name: "目标识别准确率", Value: d.Precision, Display: pct(d.Precision),
				Target: "≥ 85%", Pass: d.Precision >= 0.85, Primary: true,
				Note: fmt.Sprintf("%s类别一致；按大类 %s；不排除不可见目标时 %s", boxSourceNote(d, r.Models["detector"]), pct(d.GroupPrec), pct(d.RawPrecision))},
			Metric{ID: "detection_miss", Name: "目标识别漏检率", Value: d.MissRate, Display: pct(d.MissRate),
				Target: "≤ 5%", Pass: d.MissRate <= 0.05, Primary: true, Note: "按大类 " + pct(d.GroupMissRate)})
	}
	if g := r.Geo; g != nil {
		m = append(m,
			Metric{ID: "geo_visual", Name: "空间对齐精度（无元数据，视觉定位）", Value: g.Visual.Median,
				Display: fmt.Sprintf("中位误差 %.0f m", g.Visual.Median), Target: "< 100 m", Pass: g.Located > 0 && g.Visual.Median < 100,
				Primary: true, Note: fmt.Sprintf("<100 m 占 %s，<250 m 占 %s，%d/%d 张已定位", pct(g.Within100), pct(g.Within250), g.Located, g.Total)})
		if d := g.DJI; d != nil {
			m = append(m, Metric{ID: "geo_metadata", Name: "空间对齐（飞行记录元数据）", Value: d.MaxDeviationM,
				Display: fmt.Sprintf("%d/%d 帧已定位", d.FromTelemetry, d.Keyframes), Target: "流程验证",
				Pass: d.Keyframes > 0 && d.FromTelemetry == d.Keyframes,
				Note: fmt.Sprintf("对齐方式：%s；与独立插值最大偏差 %.2f m", d.Alignment, d.MaxDeviationM)})
		}
	}
	if q := r.Retrieval; q != nil {
		m = append(m,
			Metric{ID: "retrieval_precision", Name: "检索准确率", Value: q.Precision, Display: pct(q.Precision),
				Target: "≥ 85%", Pass: q.Precision >= 0.85, Primary: true, Note: fmt.Sprintf("P@10 %.3f，P@5 %.3f", q.P10, q.P5)},
			Metric{ID: "retrieval_recall", Name: "检索查全率", Value: q.Recall, Display: pct(q.Recall),
				Target: "≥ 80%", Pass: q.Recall >= 0.80, Primary: true, Note: fmt.Sprintf("R@10 %.3f，R@5 %.3f", q.R10, q.R5)},
			Metric{ID: "retrieval_set", Name: "检索（系统判为相关的结果）", Value: q.SetP,
				Display: fmt.Sprintf("精确率 %s / 查全率 %s", pct(q.SetP), pct(q.SetR)), Target: "—", Pass: true})
	}
	if a := r.QA; a != nil {
		m = append(m, Metric{ID: "qa_accuracy", Name: "智能问答准确率", Value: a.Accuracy,
			Display: fmt.Sprintf("%s（%d/%d）", pct(a.Accuracy), a.Correct, a.Total), Target: "≥ 80%",
			Pass: a.Accuracy >= 0.80, Primary: true, Note: fmt.Sprintf("平均评分 %.2f / 2", a.MeanGrade)})
	}
	return m
}

// boxSourceNote says who drew the boxes that were scored.
func boxSourceNote(d *DetectionSection, detector string) string {
	if n := d.BoxSources["detector"]; n > 0 {
		if n == d.Images {
			return "检测模型 " + detector + "；"
		}
		return fmt.Sprintf("检测模型 %s（%d/%d 张）；", detector, n, d.Images)
	}
	return "视觉模型画框；"
}
