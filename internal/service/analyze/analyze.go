// Package analyze describes each keyframe with the VLM: a Chinese caption,
// scene tags and attributes, object boxes, landmarks, visible text and a
// place guess. It then embeds the searchable text.
package analyze

import (
	"context"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"math"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/frame/g"
	"github.com/gogf/gf/v2/os/gtime"

	"multimo-backend/internal/model/entity"
	"multimo-backend/internal/pkg/llm"
	"multimo-backend/internal/service/asset"
	"multimo-backend/internal/service/geo"
	"multimo-backend/internal/service/keyframe"
	"multimo-backend/internal/service/pipeline"
)

const Kind = "analyze"

type Service struct {
	Assets      *asset.Service
	DB          gdb.DB
	Dispatcher  *pipeline.Dispatcher
	AI          *llm.Gateway
	Concurrency int
	Next        func(a *entity.Asset) []string
}

// Result is the VLM's structured answer for one keyframe.
type Result struct {
	Description string         `json:"description"`
	Scene       []string       `json:"scene"`
	TimeOfDay   string         `json:"time_of_day"`
	Weather     string         `json:"weather"`
	View        string         `json:"view"`
	Altitude    string         `json:"altitude"`
	Objects     []Object       `json:"objects"`
	Counts      map[string]int `json:"counts"`
	Landmarks   []string       `json:"landmarks"`
	Texts       []string       `json:"texts"`
	PlaceGuess  struct {
		Name       string  `json:"name"`
		Confidence float64 `json:"confidence"`
	} `json:"place_guess"`
}

type Object struct {
	Label string    `json:"label"`
	BBox  []float64 `json:"bbox_2d"`
}

type kfRow struct {
	Id         int64       `orm:"id"`
	Idx        int         `orm:"idx"`
	TsSec      float64     `orm:"ts_sec"`
	Path       string      `orm:"path"`
	Width      int         `orm:"width"`
	Height     int         `orm:"height"`
	CapturedAt *gtime.Time `orm:"captured_at"`
	Meta       g.Map       `orm:"meta"`
}

func (s *Service) Handle(ctx context.Context, t *pipeline.Task, report pipeline.Reporter) error {
	a, err := s.Assets.Get(ctx, t.AssetID)
	if err != nil {
		return pipeline.Permanent(err)
	}
	var kfs []*kfRow
	if err := s.DB.Model("keyframes").Ctx(ctx).Where("asset_id", a.Id).OrderAsc("idx").Scan(&kfs); err != nil {
		return err
	}
	if len(kfs) == 0 {
		return s.next(ctx, a)
	}

	texts := make([]string, len(kfs))
	var done, fellBack int
	var mu sync.Mutex
	sem := make(chan struct{}, max(s.Concurrency, 1))
	var wg sync.WaitGroup
	errs := make([]error, len(kfs))
	for i, kf := range kfs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			text, fb, err := s.analyzeOne(ctx, a, kf)
			mu.Lock()
			defer mu.Unlock()
			texts[i], errs[i] = text, err
			done++
			if fb {
				fellBack++
			}
			report(0.9*float64(done)/float64(len(kfs)), fmt.Sprintf("analysed %d/%d keyframes", done, len(kfs)))
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err // database trouble; the task is retried
		}
	}

	report(0.95, "text embeddings")
	emb, err := s.AI.Embed(ctx, "keyframe-text-embedding", a.Id, texts)
	if err != nil {
		return err
	}
	for i, kf := range kfs {
		if _, err := s.DB.Exec(ctx, "UPDATE keyframes SET embedding = ?::vector, embedding_model = ? WHERE id = ?",
			keyframe.VectorLiteral(emb.Vectors[i]), emb.Model, kf.Id); err != nil {
			return err
		}
	}
	if fellBack > 0 {
		g.Log().Warningf(ctx, "asset %d: %d/%d keyframes described by the local fallback", a.Id, fellBack, len(kfs))
	}
	return s.next(ctx, a)
}

func (s *Service) next(ctx context.Context, a *entity.Asset) error {
	if s.Next == nil {
		return nil
	}
	for _, kind := range s.Next(a) {
		if err := s.Dispatcher.Enqueue(ctx, a.Id, kind); err != nil {
			return err
		}
	}
	return nil
}

// analyzeOne describes a keyframe and stores the result. It returns the
// searchable text and whether the local fallback was used.
func (s *Service) analyzeOne(ctx context.Context, a *entity.Asset, kf *kfRow) (string, bool, error) {
	path := s.Assets.Abs(kf.Path)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false, err
	}
	temp := 0.1
	var res Result
	resp, err := s.AI.ChatJSON(ctx, llm.ChatRequest{
		Purpose: "keyframe-analysis", AssetID: a.Id, MaxTokens: 8192, Temperature: &temp,
		Messages: []llm.Message{{Text: buildPrompt(a.Type),
			Images: []llm.Image{{Data: data, MIME: "image/jpeg", Width: kf.Width, Height: kf.Height}}}},
		Fallback: func() (string, error) { return fallbackJSON(a, kf, path), nil },
	}, &res)
	fellBack := resp != nil && resp.Fallback
	if ctx.Err() != nil {
		return "", false, ctx.Err() // shutting down: leave the keyframe for the retried task
	}
	if err != nil {
		// Unparseable model output: keep going with the local description.
		g.Log().Warningf(ctx, "keyframe %d: %v; using fallback", kf.Id, err)
		json.Unmarshal([]byte(fallbackJSON(a, kf, path)), &res) //nolint:errcheck
		fellBack = true
	}

	dets := boxes(res.Objects, kf.Width, kf.Height)
	tags := tagsOf(&res)
	text := searchText(&res, tags)
	meta := g.Map{}
	for k, v := range kf.Meta {
		meta[k] = v
	}
	meta["analysis"] = g.Map{"scene": res.Scene, "time_of_day": res.TimeOfDay, "weather": res.Weather,
		"view": res.View, "altitude": res.Altitude, "counts": res.Counts, "landmarks": res.Landmarks,
		"texts": res.Texts, "place_guess": res.PlaceGuess, "boxes": len(dets)}
	meta["needsRerun"] = fellBack
	metaJSON, _ := json.Marshal(meta)
	source := "vlm"
	if fellBack {
		source = "fallback"
	}

	err = s.DB.Transaction(ctx, func(ctx context.Context, tx gdb.TX) error {
		if _, err := tx.Exec(`UPDATE keyframes SET description = ?, description_source = ?, tags = ?, search_text = ?,
			meta = ?::jsonb WHERE id = ?`, res.Description, source, pgTextArray(tags), text, string(metaJSON), kf.Id); err != nil {
			return err
		}
		if _, err := tx.Model("detections").Where("keyframe_id", kf.Id).Delete(); err != nil {
			return err
		}
		if len(dets) == 0 {
			return nil
		}
		rows := make(g.List, len(dets))
		for i, d := range dets {
			rows[i] = g.Map{"asset_id": a.Id, "keyframe_id": kf.Id, "label": d.Label, "label_zh": Labels[d.Label],
				"x": d.X, "y": d.Y, "w": d.W, "h": d.H, "source": source}
		}
		_, err := tx.Model("detections").Data(rows).Batch(200).Insert()
		return err
	})
	return text, fellBack, err
}

// Box is a detection in keyframe pixel coordinates.
type Box struct {
	Label      string
	X, Y, W, H float64
}

// boxes converts 0-1000 relative boxes to pixels, dropping unknown labels
// and degenerate boxes.
func boxes(objs []Object, w, h int) []Box {
	var out []Box
	for _, o := range objs {
		label := strings.ToLower(strings.TrimSpace(o.Label))
		if _, ok := Labels[label]; !ok || len(o.BBox) != 4 {
			continue
		}
		x1, y1 := clamp(o.BBox[0], 0, 1000)/1000*float64(w), clamp(o.BBox[1], 0, 1000)/1000*float64(h)
		x2, y2 := clamp(o.BBox[2], 0, 1000)/1000*float64(w), clamp(o.BBox[3], 0, 1000)/1000*float64(h)
		if x2 < x1 {
			x1, x2 = x2, x1
		}
		if y2 < y1 {
			y1, y2 = y2, y1
		}
		if x2-x1 < 1 || y2-y1 < 1 {
			continue
		}
		out = append(out, Box{Label: label, X: x1, Y: y1, W: x2 - x1, H: y2 - y1})
	}
	return out
}

func tagsOf(r *Result) []string {
	seen := map[string]bool{}
	var tags []string
	add := func(t string) {
		t = strings.TrimSpace(t)
		if t != "" && !seen[t] {
			seen[t] = true
			tags = append(tags, t)
		}
	}
	for _, t := range r.Scene {
		add(t)
	}
	for _, a := range []string{r.TimeOfDay, r.Weather, r.View, r.Altitude} {
		if zh, ok := attrZH[a]; ok {
			add(zh)
		}
	}
	for label, n := range r.Counts {
		if zh, ok := Labels[label]; ok && n > 0 {
			add(zh)
		}
	}
	sort.Strings(tags[min(len(r.Scene), len(tags)):])
	return tags
}

// searchText is what keyword search and text embeddings see.
func searchText(r *Result, tags []string) string {
	var b strings.Builder
	b.WriteString(r.Description)
	b.WriteString(" 标签：" + strings.Join(tags, "、"))
	if len(r.Counts) > 0 {
		labels := make([]string, 0, len(r.Counts))
		for l := range r.Counts {
			labels = append(labels, l)
		}
		sort.Strings(labels)
		var parts []string
		for _, l := range labels {
			if zh, ok := Labels[l]; ok && r.Counts[l] > 0 {
				parts = append(parts, fmt.Sprintf("%s%d", zh, r.Counts[l]))
			}
		}
		if len(parts) > 0 {
			b.WriteString(" 目标：" + strings.Join(parts, "、"))
		}
	}
	if len(r.Landmarks) > 0 {
		b.WriteString(" 地标：" + strings.Join(r.Landmarks, "、"))
	}
	if len(r.Texts) > 0 {
		b.WriteString(" 文字：" + strings.Join(r.Texts, "、"))
	}
	if r.PlaceGuess.Name != "" {
		b.WriteString(" 地点：" + r.PlaceGuess.Name)
	}
	return b.String()
}

// fallbackJSON builds a description from what is known locally: picture
// type, size, capture time, position and overall brightness.
func fallbackJSON(a *entity.Asset, kf *kfRow, path string) string {
	kind := map[string]string{"video": "无人机视频截帧", "remote_sensing": "遥感影像"}[a.Type]
	if kind == "" {
		kind = "无人机航拍照片"
	}
	parts := []string{fmt.Sprintf("%s（%d×%d）", kind, kf.Width, kf.Height)}
	tod := "unknown"
	if l, ok := meanLuma(path); ok {
		switch {
		case l < 55:
			parts = append(parts, "画面整体很暗，可能是夜间拍摄")
			tod = "night"
		case l > 170:
			parts = append(parts, "画面明亮")
			tod = "day"
		default:
			parts = append(parts, "画面亮度正常")
		}
	}
	if kf.CapturedAt != nil {
		parts = append(parts, "拍摄于"+kf.CapturedAt.Layout("2006-01-02 15:04"))
	}
	if a.Lat != nil {
		parts = append(parts, "位置约"+geo.FormatLatLonZH(*a.Lat, *a.Lon, 4))
	}
	if a.Type == "video" {
		parts = append(parts, fmt.Sprintf("位于视频第%.1f秒", kf.TsSec))
	}
	b, _ := json.Marshal(g.Map{"description": strings.Join(parts, "，") + "。（模型不可用，此为本地生成的描述）",
		"scene": []string{}, "time_of_day": tod, "weather": "unknown", "view": "unknown", "altitude": "unknown",
		"objects": []any{}, "counts": g.Map{}, "landmarks": []string{}, "texts": []string{},
		"place_guess": g.Map{"name": "", "confidence": 0}})
	return string(b)
}

func meanLuma(path string) (float64, bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	img, err := jpeg.Decode(f)
	if err != nil {
		return 0, false
	}
	b := img.Bounds()
	step := max(1, b.Dx()/64)
	var sum, n float64
	for y := b.Min.Y; y < b.Max.Y; y += step {
		for x := b.Min.X; x < b.Max.X; x += step {
			r, g, bb, _ := img.At(x, y).RGBA()
			sum += 0.299*float64(r>>8) + 0.587*float64(g>>8) + 0.114*float64(bb>>8)
			n++
		}
	}
	return sum / n, n > 0
}

func pgTextArray(v []string) string {
	parts := make([]string, len(v))
	for i, s := range v {
		parts[i] = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }
