package testsuite

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"strings"

	"multimo-backend/internal/pkg/llm"
	"multimo-backend/internal/service/analyze"
)

// ---------------------------------------------------------------- formats

type FormatRow struct {
	Type   string `json:"type"`
	Format string `json:"format"`
	Ready  int    `json:"ready"`
	Failed int    `json:"failed"`
}

type FormatsSection struct {
	Rows    []FormatRow `json:"rows"`
	Types   int         `json:"types"`
	Formats int         `json:"formats"`
	Failed  []string    `json:"failedAssets,omitempty"`
}

func (s *Suite) formats(ctx context.Context) (*FormatsSection, error) {
	var rows []FormatRow
	if err := s.DB.GetScan(ctx, &rows, `SELECT type, format, count(*) FILTER (WHERE status = 'ready') AS ready,
		count(*) FILTER (WHERE status = 'failed') AS failed FROM assets GROUP BY type, format ORDER BY type, format`); err != nil {
		return nil, err
	}
	sec := &FormatsSection{Rows: rows}
	types := map[string]bool{}
	for _, r := range rows {
		if r.Ready > 0 {
			types[r.Type] = true
			sec.Formats++
		}
	}
	sec.Types = len(types)
	failed, err := s.DB.Model("assets").Ctx(ctx).Where("status", "failed").Array("filename")
	if err == nil {
		for _, f := range failed {
			sec.Failed = append(sec.Failed, f.String())
		}
	}
	return sec, nil
}

// ---------------------------------------------------------------- shared

type kfDet struct {
	ID      int64   `orm:"id"`
	FrameNo *int    `orm:"frame_no"`
	TsSec   float64 `orm:"ts_sec"`
	Width   int     `orm:"width"`
	Height  int     `orm:"height"`
	Source  string  `orm:"description_source"`
	Counts  string  `orm:"counts"`
}

// keyframeDetections returns an asset's keyframes and their boxes scaled to
// the original image size.
func (s *Suite) keyframeDetections(ctx context.Context, assetID int64, origW, origH int) ([]kfDet, map[int64][]Pred, error) {
	var kfs []kfDet
	if err := s.DB.Model("keyframes").Ctx(ctx).Fields("id, frame_no, ts_sec, width, height, description_source, "+
		"meta->'analysis'->>'counts' AS counts").Where("asset_id", assetID).OrderAsc("idx").Scan(&kfs); err != nil {
		return nil, nil, err
	}
	rows, err := s.DB.GetAll(ctx, `SELECT keyframe_id, label, x, y, w, h FROM detections WHERE asset_id = ?`, assetID)
	if err != nil {
		return nil, nil, err
	}
	size := map[int64][2]int{}
	for _, k := range kfs {
		size[k.ID] = [2]int{k.Width, k.Height}
	}
	preds := map[int64][]Pred{}
	for _, r := range rows {
		id := r["keyframe_id"].Int64()
		sz := size[id]
		if sz[0] == 0 {
			continue
		}
		sx, sy := float64(origW)/float64(sz[0]), float64(origH)/float64(sz[1])
		x, y, w, h := r["x"].Float64(), r["y"].Float64(), r["w"].Float64(), r["h"].Float64()
		preds[id] = append(preds[id], Pred{Label: r["label"].String(), Box: Box{x * sx, y * sy, (x + w) * sx, (y + h) * sy}})
	}
	return kfs, preds, nil
}

// ---------------------------------------------------------------- keyframe consistency

type KeyframeVideo struct {
	ID        string  `json:"id"`
	Keyframes int     `json:"keyframes"`
	Evaluated int     `json:"evaluated"`
	F1        float64 `json:"f1"`
	Precision float64 `json:"precision"`
	Recall    float64 `json:"recall"`
	Jaccard   float64 `json:"categoryJaccard"`
	Cosine    float64 `json:"semanticCosine"`
	Match     Match   `json:"match"`
}

type KeyframeSection struct {
	Videos    []KeyframeVideo `json:"videos"`
	Match     Match           `json:"match"`
	F1        float64         `json:"f1"`
	Jaccard   float64         `json:"categoryJaccard"`
	Cosine    float64         `json:"semanticCosine"`
	Keyframes int             `json:"keyframes"`
	Skipped   []string        `json:"skipped,omitempty"`
}

type keyframeGT struct {
	ID         string `json:"id"`
	Video      string `json:"video"`
	Annotation string `json:"annotation"`
	Format     string `json:"format"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
}

func (s *Suite) keyframe(ctx context.Context) (*KeyframeSection, error) {
	gts, err := readJSONL[keyframeGT](s, "keyframe_gt.jsonl")
	if err != nil {
		return nil, err
	}
	assets, err := s.assetsByPath(ctx)
	if err != nil {
		return nil, err
	}
	sec := &KeyframeSection{}
	type pairText struct{ gt, pred string }
	var texts []pairText
	var jac []float64
	var owner []int // video index per text pair
	for _, v := range gts {
		a := assets[v.Video]
		if a == nil {
			sec.Skipped = append(sec.Skipped, v.ID+": not ingested")
			continue
		}
		var frames map[int][]GTObject
		inTax := func(string) bool { return true }
		if v.Format == "uavdt" {
			frames, err = ParseUAVDT(s.path(v.Annotation))
			if err == nil {
				err = AddUAVDTIgnore(frames, s.path(v.Annotation))
			}
			inTax = func(l string) bool { return Groups[l] == "vehicle" } // UAVDT annotates vehicles only
		} else {
			frames, err = ParseVisDroneVID(s.path(v.Annotation))
		}
		if err != nil {
			return nil, err
		}
		kfs, preds, err := s.keyframeDetections(ctx, a.ID, v.Width, v.Height)
		if err != nil {
			return nil, err
		}
		kv := KeyframeVideo{ID: v.ID, Keyframes: len(kfs)}
		for _, k := range kfs {
			if k.FrameNo == nil || k.Source != "vlm" {
				continue
			}
			gt := frames[*k.FrameNo+1] // annotations count frames from 1
			kv.Evaluated++
			kv.Match.Add(MatchBoxes(preds[k.ID], gt, sameGroup, inTax))
			g, p := categorySets(gt, k.Counts, inTax)
			jac = append(jac, jaccard(g, p))
			texts = append(texts, pairText{setText(g), setText(p)})
			owner = append(owner, len(sec.Videos))
		}
		kv.F1, kv.Precision, kv.Recall = round(kv.Match.F1(), 4), round(kv.Match.Precision(), 4), round(kv.Match.Recall(), 4)
		sec.Match.Add(kv.Match)
		sec.Keyframes += kv.Evaluated
		sec.Videos = append(sec.Videos, kv)
	}
	sec.F1 = round(sec.Match.F1(), 4)

	// Semantic similarity of the category sets, via text embeddings.
	if len(texts) > 0 {
		in := make([]string, 0, 2*len(texts))
		for _, t := range texts {
			in = append(in, t.gt, t.pred)
		}
		res, err := s.AI.Embed(ctx, "testsuite-semantic", 0, in)
		if err == nil {
			perVideo := make([][]float64, len(sec.Videos))
			perJac := make([][]float64, len(sec.Videos))
			var all []float64
			for i := range texts {
				c := float64(llm.Dot(res.Vectors[2*i], res.Vectors[2*i+1])) / norm(res.Vectors[2*i]) / norm(res.Vectors[2*i+1])
				all = append(all, c)
				perVideo[owner[i]] = append(perVideo[owner[i]], c)
				perJac[owner[i]] = append(perJac[owner[i]], jac[i])
			}
			sec.Cosine = round(Summarise(all).Mean, 4)
			for i := range sec.Videos {
				sec.Videos[i].Cosine = round(Summarise(perVideo[i]).Mean, 4)
				sec.Videos[i].Jaccard = round(Summarise(perJac[i]).Mean, 4)
			}
		}
	}
	sec.Jaccard = round(Summarise(jac).Mean, 4)
	return sec, nil
}

// categorySets: visible ground-truth classes vs classes the VLM counted.
func categorySets(gt []GTObject, counts string, inTax func(string) bool) (map[string]bool, map[string]bool) {
	g := map[string]bool{}
	for _, o := range gt {
		if o.Visible && !o.Region {
			g[o.Label] = true
		}
	}
	p := map[string]bool{}
	var c map[string]float64
	json.Unmarshal([]byte(counts), &c) //nolint:errcheck
	for l, n := range c {
		if n > 0 && inTax(l) {
			p[l] = true
		}
	}
	return g, p
}

func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1
	}
	inter := 0
	for k := range a {
		if b[k] {
			inter++
		}
	}
	return float64(inter) / float64(len(a)+len(b)-inter)
}

func setText(m map[string]bool) string {
	var names []string
	for l := range m {
		if zh, ok := analyze.Labels[l]; ok {
			names = append(names, zh)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "画面中没有目标"
	}
	return "画面中有：" + strings.Join(names, "、")
}

func norm(v []float32) float64 {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	return math.Sqrt(s)
}

// ---------------------------------------------------------------- detection

type DetectionSection struct {
	Images        int        `json:"images"`
	Exact         Match      `json:"exact"`
	Grouped       Match      `json:"grouped"`
	RawPrecision  float64    `json:"rawPrecision"` // without the ignore protocol
	Precision     float64    `json:"precision"`
	MissRate      float64    `json:"missRate"`
	GroupPrec     float64    `json:"groupedPrecision"`
	GroupMissRate float64    `json:"groupedMissRate"`
	PerClass      []ClassRow `json:"perClass"`
	Skipped       []string   `json:"skipped,omitempty"`
}

type ClassRow struct {
	Label     string  `json:"label"`
	GT        int     `json:"gt"`
	Pred      int     `json:"pred"`
	Recall    float64 `json:"recall"`
	Precision float64 `json:"precision"`
}

type detectionGT struct {
	ID         string `json:"id"`
	Image      string `json:"image"`
	Annotation string `json:"annotation"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
}

func (s *Suite) detection(ctx context.Context) (*DetectionSection, error) {
	gts, err := readJSONL[detectionGT](s, "detection_gt.jsonl")
	if err != nil {
		return nil, err
	}
	assets, err := s.assetsByPath(ctx)
	if err != nil {
		return nil, err
	}
	sec := &DetectionSection{}
	gtPer, predPer, perClass := map[string]int{}, map[string]int{}, map[string]Match{}
	var raw Match
	for _, d := range gts {
		a := assets[d.Image]
		if a == nil {
			sec.Skipped = append(sec.Skipped, d.ID+": not ingested")
			continue
		}
		gt, err := ParseVisDroneDET(s.path(d.Annotation))
		if err != nil {
			return nil, err
		}
		kfs, preds, err := s.keyframeDetections(ctx, a.ID, d.Width, d.Height)
		if err != nil {
			return nil, err
		}
		if len(kfs) == 0 || kfs[0].Source != "vlm" {
			sec.Skipped = append(sec.Skipped, d.ID+": no VLM analysis")
			continue
		}
		p := preds[kfs[0].ID]
		sec.Images++
		sec.Exact.Add(MatchBoxes(p, gt, sameClass, nil))
		sec.Grouped.Add(MatchBoxes(p, gt, sameGroup, nil))
		// Without the ignore protocol: every unmatched prediction is an error.
		var visible []GTObject
		for _, o := range gt {
			if o.Visible && !o.Region {
				visible = append(visible, o)
				gtPer[o.Label]++
			}
		}
		raw.Add(MatchBoxes(p, visible, sameClass, nil))
		for _, x := range p {
			predPer[x.Label]++
		}
		// Per class: that class's predictions against that class's visible
		// objects, keeping hidden objects and regions for the ignore rule.
		for label := range visdroneLabelSet {
			var pl []Pred
			for _, x := range p {
				if x.Label == label {
					pl = append(pl, x)
				}
			}
			var gl []GTObject
			for _, o := range gt {
				if o.Label == label || o.Region || !o.Visible {
					gl = append(gl, o)
				}
			}
			m := perClass[label]
			m.Add(MatchBoxes(pl, gl, sameClass, nil))
			perClass[label] = m
		}
	}
	sec.RawPrecision = round(raw.Precision(), 4)
	sec.Precision, sec.MissRate = round(sec.Exact.Precision(), 4), round(sec.Exact.MissRate(), 4)
	sec.GroupPrec, sec.GroupMissRate = round(sec.Grouped.Precision(), 4), round(sec.Grouped.MissRate(), 4)
	for label := range visdroneLabelSet {
		sec.PerClass = append(sec.PerClass, ClassRow{Label: label, GT: gtPer[label], Pred: predPer[label],
			Recall: round(perClass[label].Recall(), 3), Precision: round(perClass[label].Precision(), 3)})
	}
	sort.Slice(sec.PerClass, func(i, j int) bool { return sec.PerClass[i].GT > sec.PerClass[j].GT })
	return sec, nil
}

var visdroneLabelSet = func() map[string]bool {
	m := map[string]bool{}
	for _, l := range visdroneClasses {
		m[l] = true
	}
	return m
}()
