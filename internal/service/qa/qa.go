// Package qa answers questions with retrieval-augmented generation:
// evidence from the assets a question names and from hybrid search, a
// decision-model pass that classifies the question and keeps the relevant
// evidence, and a VLM answer grounded in that evidence (with keyframe
// images for visual questions) that cites it.
package qa

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/frame/g"
	"github.com/gogf/gf/v2/util/gconv"

	"multimo-backend/internal/model/entity"
	"multimo-backend/internal/pkg/llm"
	"multimo-backend/internal/service/analyze"
	"multimo-backend/internal/service/asset"
	"multimo-backend/internal/service/keyframe"
	"multimo-backend/internal/service/search"
)

const (
	maxEvidence       = 12
	maxImages         = 3
	keepP             = 0.3 // evidence kept when the decision model gives at least this
	minEvidence       = 4   // ...but always the best few
	keyframesPerAsset = 6
)

type Service struct {
	DB     gdb.DB
	AI     *llm.Gateway
	Assets *asset.Service
	Search *search.Service
}

type Request struct {
	Question    string   `json:"question"`
	Collections []string `json:"collections"`
}

type Evidence struct {
	N         int      `json:"n"`
	Kind      string   `json:"kind"` // asset | keyframe | chunk
	AssetID   int64    `json:"assetId"`
	UnitID    int64    `json:"unitId,omitempty"`
	Filename  string   `json:"filename"`
	Page      *int     `json:"page,omitempty"`
	TsSec     *float64 `json:"tsSec,omitempty"`
	Text      string   `json:"text"`
	ImageUrl  string   `json:"imageUrl,omitempty"`
	Relevance *float64 `json:"relevance,omitempty"`
	Mentioned bool     `json:"mentioned,omitempty"`
	imagePath string
}

type Response struct {
	Question   string           `json:"question"`
	Answer     string           `json:"answer"`
	Citations  []int            `json:"citations"`
	Confidence float64          `json:"confidence"`
	Intent     string           `json:"intent"`
	Mentioned  []string         `json:"mentioned"`
	Evidence   []*Evidence      `json:"evidence"`
	Fallback   bool             `json:"fallback"`
	Timings    map[string]int64 `json:"timingsMs"`
}

var intents = map[string]string{
	"document": "询问法规、文档中的规定、数值或定义",
	"visual":   "询问某张图像、某段视频或遥感影像里有什么、有多少、是什么场景",
	"location": "询问拍摄位置、经纬度或地点",
	"metadata": "询问拍摄时间、机型、飞行高度等拍摄参数",
	"cross":    "需要把素材信息和法规文档结合起来判断",
}

func (s *Service) Answer(ctx context.Context, req Request) (*Response, error) {
	t0 := time.Now()
	q := strings.TrimSpace(req.Question)
	if q == "" {
		return nil, fmt.Errorf("empty question")
	}
	res := &Response{Question: q, Timings: map[string]int64{}}
	mark := func(k string, since time.Time) { res.Timings[k] = time.Since(since).Milliseconds() }

	// 1. evidence: assets named in the question, then hybrid search
	t := time.Now()
	named, err := s.mentionedAssets(ctx, q, req.Collections)
	if err != nil {
		return nil, err
	}
	var ev []*Evidence
	for _, a := range named {
		res.Mentioned = append(res.Mentioned, a.Filename)
		items, err := s.assetEvidence(ctx, a)
		if err != nil {
			return nil, err
		}
		ev = append(ev, items...)
	}
	mark("mentions", t)

	// 2. decision model: what kind of question is this? It decides where to search.
	t = time.Now()
	intent := s.classify(ctx, q, len(named) > 0)
	res.Intent = intent
	mark("intent", t)

	t = time.Now()
	seen := map[string]bool{}
	for _, e := range ev {
		seen[fmt.Sprintf("%s:%d", e.Kind, e.UnitID)] = true
	}
	addSearchIn := func(query string, types []string, assetIDs []int64, k int) {
		sr, err := s.Search.Search(ctx, search.Request{Query: query, Collections: req.Collections, Types: types,
			AssetIDs: assetIDs, TopK: k, GroupBy: "unit", NoRerank: true})
		if err != nil {
			return
		}
		for _, r := range sr.Results {
			key := fmt.Sprintf("%s:%d", r.Kind, r.UnitID)
			if seen[key] {
				continue
			}
			seen[key] = true
			ev = append(ev, &Evidence{Kind: r.Kind, AssetID: r.AssetID, UnitID: r.UnitID, Filename: r.Filename, Page: r.Page,
				TsSec: r.TsSec, Text: trimRunes(r.Snippet, 600), ImageUrl: r.ImageUrl, Mentioned: len(assetIDs) > 0})
		}
	}
	addSearch := func(query string, types []string, k int) { addSearchIn(query, types, nil, k) }
	// Regulations are in Chinese and English: also search with the question
	// rewritten as keywords in both languages (BM25 cannot cross languages).
	docQueries := func() []string {
		if intent != "document" && intent != "cross" {
			return nil
		}
		return s.rewrite(ctx, q)
	}()
	// A document named in the question is searched on its own first.
	for _, a := range named {
		if a.Type != "document" {
			continue
		}
		for _, query := range append([]string{q}, docQueries...) {
			addSearchIn(query, nil, []int64{a.Id}, 3)
		}
	}
	switch intent {
	case "document":
		addSearch(q, []string{"document"}, 10)
		for _, rq := range docQueries {
			addSearch(rq, []string{"document"}, 6)
		}
	case "cross":
		addSearch(q, []string{"document"}, 6)
		for _, rq := range docQueries {
			addSearch(rq, []string{"document"}, 5)
		}
		addSearch(q, nil, 6)
	default:
		if len(named) > 0 {
			addSearch(q, nil, 4)
		} else {
			addSearch(q, nil, 12)
		}
	}
	mark("retrieve", t)

	// 3. decision model: which evidence helps?
	t = time.Now()
	ev = s.judge(ctx, q, intent, ev)
	mark("judge", t)

	for i, e := range ev {
		e.N = i + 1
	}
	res.Evidence = ev

	// 3. answer
	t = time.Now()
	if err := s.generate(ctx, q, intent, ev, res); err != nil {
		return nil, err
	}
	mark("generate", t)
	mark("total", t0)
	return res, nil
}

// ---------------------------------------------------------------- mentions

var reToken = regexp.MustCompile(`[A-Za-z0-9][A-Za-z0-9_\-.]{3,}`)
var reDigits = regexp.MustCompile(`^[0-9.]+$`)

// mentionedAssets finds assets whose file names contain a token from the
// question (uav0000086_00000_v, P6519, 01_0101.JPG, drone_palos_verdes,
// "Palos Verdes"...). Pure numbers are ignored so years do not match.
func (s *Service) mentionedAssets(ctx context.Context, q string, collections []string) ([]*entity.Asset, error) {
	var out []*entity.Asset
	seen := map[int64]bool{}
	for _, tok := range MentionTokens(q) {
		m := s.DB.Model("assets").Ctx(ctx).WhereLike("lower(filename)", "%"+strings.ToLower(tok)+"%").Where("status", "ready")
		if len(collections) > 0 {
			m = m.WhereIn("collection", collections)
		}
		var found []*entity.Asset
		if err := m.OrderAsc("id").Limit(4).Scan(&found); err != nil {
			return nil, err
		}
		if len(found) > 3 {
			continue // too generic a token
		}
		for _, a := range found {
			if !seen[a.Id] {
				seen[a.Id] = true
				out = append(out, a)
			}
		}
	}
	return out, nil
}

var reMultiWord = regexp.MustCompile(`[A-Za-z]{3,}(?:\s+[A-Za-z]{3,})+`)

// MentionTokens lists the file-name-like tokens of a question: identifiers
// such as uav0000086_00000_v or 01_0101.JPG, and multi-word names joined
// with underscores ("Palos Verdes" -> palos_verdes). Numbers and generic
// words are dropped.
func MentionTokens(q string) []string {
	raw := reToken.FindAllString(q, -1)
	for _, w := range reMultiWord.FindAllString(q, -1) {
		raw = append(raw, strings.ToLower(strings.Join(strings.Fields(w), "_")))
	}
	var out []string
	for _, tok := range raw {
		tok = strings.TrimRight(tok, ".,")
		if reDigits.MatchString(tok) || stop[strings.ToLower(tok)] {
			continue
		}
		out = append(out, tok)
	}
	return out
}

var stop = map[string]bool{"part": true, "faa": true, "uav": true, "drone": true, "video": true, "image": true,
	"jpg": true, "mp4": true, "png": true, "remote": true, "dji": false}

// ---------------------------------------------------------------- evidence

func (s *Service) assetEvidence(ctx context.Context, a *entity.Asset) ([]*Evidence, error) {
	out := []*Evidence{{Kind: "asset", AssetID: a.Id, Filename: a.Filename, Text: assetSummary(a), Mentioned: true}}
	var kfs []struct {
		Id          int64    `orm:"id"`
		Idx         int      `orm:"idx"`
		TsSec       float64  `orm:"ts_sec"`
		Path        string   `orm:"path"`
		SearchText  string   `orm:"search_text"`
		Lat         *float64 `orm:"lat"`
		Lon         *float64 `orm:"lon"`
		GeoSource   string   `orm:"geo_source"`
		GeoAccuracy *float64 `orm:"geo_accuracy_m"`
		Counts      string   `orm:"counts"`
	}
	if err := s.DB.Model("keyframes").Ctx(ctx).Fields("id, idx, ts_sec, path, search_text, lat, lon, geo_source, "+
		"geo_accuracy_m, meta->'analysis'->>'counts' AS counts").Where("asset_id", a.Id).OrderAsc("idx").Scan(&kfs); err != nil {
		return nil, err
	}
	// Spread the cited keyframes over the whole video.
	step := max(1, int(math.Ceil(float64(len(kfs))/keyframesPerAsset)))
	maxCount := map[string]float64{}
	for i, k := range kfs {
		var counts map[string]float64
		json.Unmarshal([]byte(k.Counts), &counts) //nolint:errcheck
		for l, n := range counts {
			maxCount[l] = math.Max(maxCount[l], n)
		}
		if i%step != 0 {
			continue
		}
		text := k.SearchText
		if a.Type == "video" {
			text = fmt.Sprintf("第%.1f秒：%s", k.TsSec, text)
		}
		if k.Lat != nil {
			text += fmt.Sprintf("（该帧位置：北纬%.5f°、东经%.5f°，来源 %s，误差约 %.0f 米）", *k.Lat, *k.Lon, k.GeoSource, val(k.GeoAccuracy))
		}
		ts := k.TsSec
		out = append(out, &Evidence{Kind: "keyframe", AssetID: a.Id, UnitID: k.Id, Filename: a.Filename, TsSec: &ts,
			Text: trimRunes(text, 600), ImageUrl: fmt.Sprintf("/api/v1/keyframes/%d/image", k.Id), Mentioned: true,
			imagePath: s.Assets.Abs(k.Path)})
	}
	if len(kfs) > 1 && len(maxCount) > 0 {
		labels := make([]string, 0, len(maxCount))
		for l := range maxCount {
			labels = append(labels, l)
		}
		sort.Slice(labels, func(i, j int) bool { return maxCount[labels[i]] > maxCount[labels[j]] })
		var parts []string
		for _, l := range labels {
			if zh, ok := analyze.Labels[l]; ok {
				parts = append(parts, fmt.Sprintf("%s 最多 %.0f", zh, maxCount[l]))
			}
		}
		out[0].Text += "。各关键帧中目标数量的最大值：" + strings.Join(parts, "、")
	}
	return out, nil
}

func assetSummary(a *entity.Asset) string {
	kind := map[string]string{"video": "视频", "image": "航拍图像", "remote_sensing": "遥感影像", "document": "文档"}[a.Type]
	parts := []string{fmt.Sprintf("%s %s（集合 %s，格式 %s）", kind, a.Filename, a.Collection, a.Format)}
	if a.Width != nil {
		parts = append(parts, fmt.Sprintf("分辨率 %d×%d", *a.Width, *a.Height))
	}
	if a.DurationSec != nil {
		parts = append(parts, fmt.Sprintf("时长 %.1f 秒", *a.DurationSec))
	}
	if a.CapturedAt != nil {
		src, _ := a.Meta["capturedAtSource"].(string)
		parts = append(parts, fmt.Sprintf("拍摄时间 %s UTC（来源：%s）", a.CapturedAt.Layout("2006-01-02 15:04:05"), src))
	}
	if a.Lat != nil {
		parts = append(parts, fmt.Sprintf("位置 北纬%.5f°、经度%.5f°（来源：%s，误差约 %.0f 米）", *a.Lat, *a.Lon, a.GeoSource, val(a.GeoAccuracyM)))
	}
	if tel, ok := a.Meta["telemetry"].(map[string]any); ok {
		alt := gconv.Float64(tel["maxAltitudeM"])
		parts = append(parts, fmt.Sprintf("飞行记录：机型 %v，相对起飞点最大高度 %.1f 米（%.0f 英尺），记录时间 %v 至 %v（UTC），%v 个 GPS 点",
			tel["aircraft"], alt, alt/0.3048, tel["start"], tel["end"], tel["points"]))
	}
	if t, ok := a.Meta["title"].(string); ok && t != "" {
		parts = append(parts, "标题："+t)
	}
	return strings.Join(parts, "；")
}

// ---------------------------------------------------------------- judging

const rewritePrompt = `把下面的问题改写成两条用于在法规文档中检索相关条款的关键词查询：一条英文（用法规原文可能出现的术语，如 maximum height、groundspeed、visibility、anti-collision lighting），一条中文。
只保留要查找的内容本身，不要包含文档或法规的名称（如 PDRA01、Part 107、条例名称），每条不超过 8 个词。只返回 JSON：{"en": "...", "zh": "..."}
问题：`

// rewrite turns a question into English and Chinese keyword queries.
func (s *Service) rewrite(ctx context.Context, q string) []string {
	var out struct {
		EN string `json:"en"`
		ZH string `json:"zh"`
	}
	zero := 0.0
	if _, err := s.AI.ChatJSON(ctx, llm.ChatRequest{Purpose: "qa-rewrite", MaxTokens: 200, Temperature: &zero,
		Messages: []llm.Message{{Text: rewritePrompt + q}}}, &out); err != nil {
		return nil
	}
	var qs []string
	for _, x := range []string{out.EN, out.ZH} {
		if x = strings.TrimSpace(x); x != "" && x != q {
			qs = append(qs, x)
		}
	}
	return qs
}

// classify asks the decision model which kind of question this is.
func (s *Service) classify(ctx context.Context, q string, named bool) string {
	state := "问题：" + q
	if named {
		state += "\n（问题中点名了具体的素材文件）"
	}
	resp, err := s.AI.Decide(ctx, llm.DecisionRequest{Purpose: "qa-intent", State: state,
		Questions: map[string]llm.DecisionQuestion{"intent": {Type: "choice", Instructions: "这个问题属于哪一类？", Criteria: intents}}})
	if err != nil || resp.Answers["intent"].Choice == "" {
		if named {
			return "visual"
		}
		return "document"
	}
	return resp.Answers["intent"].Choice
}

// judge scores evidence relevance with the decision model; it keeps every
// mentioned-asset summary and the most relevant of the rest.
func (s *Service) judge(ctx context.Context, q, intent string, ev []*Evidence) []*Evidence {
	n := min(len(ev), 40)
	qs := make(map[string]llm.DecisionQuestion, n)
	for i := 0; i < n; i++ {
		qs[fmt.Sprintf("e%d", i)] = llm.DecisionQuestion{Type: "choice",
			Instructions: "证据：" + trimRunes(ev[i].Text, 400) + "\n这条证据对回答问题是否有用？",
			Criteria:     map[string]string{"yes": "有用：包含回答问题所需的信息", "no": "无用：与问题无关"}}
	}
	if n > 0 {
		if resp, err := s.AI.Decide(ctx, llm.DecisionRequest{Purpose: "qa-judge", State: "问题：" + q, Questions: qs}); err == nil {
			for i := 0; i < n; i++ {
				p := resp.Answers[fmt.Sprintf("e%d", i)].Probabilities["yes"]
				ev[i].Relevance = &p
			}
		}
	}
	var kept, rest []*Evidence
	for _, e := range ev {
		if e.Kind == "asset" {
			kept = append(kept, e)
		} else {
			rest = append(rest, e)
		}
	}
	sort.SliceStable(rest, func(i, j int) bool { return rel(rest[i]) > rel(rest[j]) })
	// Keep evidence the model finds useful, but never fewer than minEvidence
	// items: a strict judge must not leave the answer with nothing.
	for i, e := range rest {
		if len(kept) >= maxEvidence {
			break
		}
		useful := e.Relevance == nil || *e.Relevance >= keepP || (e.Mentioned && intent == "visual")
		if useful || i < minEvidence {
			kept = append(kept, e)
		}
	}
	return kept
}

func rel(e *Evidence) float64 {
	v := 0.0
	if e.Relevance != nil {
		v = *e.Relevance
	}
	if e.Mentioned {
		v += 0.5 // evidence about the asset the question names comes first
	}
	return v
}

// ---------------------------------------------------------------- generation

const answerPrompt = `你是无人机影像分析系统的问答助手。只根据下面编号的证据回答问题，不要使用证据以外的知识，也不要编造数字。
- 用中文简洁作答，直接给出结论和关键数字/条件；需要判断（如是否超限）时说明依据。
- 引用证据时在句末写 [编号]。证据不足以回答时，明确说"根据现有资料无法确定"，并说明缺少什么。
- 问位置时给出经纬度和证据中的定位来源与误差。
- 附带的图片是证据中提到的关键帧，可以直接观察图片回答画面内容。
只返回 JSON：{"answer": "答案", "citations": [引用的证据编号], "confidence": 0到1之间的数}

问题：%s

证据：
%s`

func (s *Service) generate(ctx context.Context, q, intent string, ev []*Evidence, res *Response) error {
	var b strings.Builder
	for _, e := range ev {
		head := map[string]string{"asset": "素材信息", "keyframe": "关键帧", "chunk": "文档片段"}[e.Kind]
		loc := e.Filename
		if e.Page != nil {
			loc += fmt.Sprintf(" 第%d页", *e.Page)
		}
		fmt.Fprintf(&b, "[%d] %s｜%s：%s\n", e.N, head, loc, e.Text)
	}
	var images []llm.Image
	if intent == "visual" || intent == "cross" {
		for _, e := range ev {
			if e.imagePath == "" || len(images) >= maxImages {
				continue
			}
			if img, err := keyframe.SmallJPEG(e.imagePath, 1024); err == nil {
				images = append(images, *img)
			}
		}
	}
	if len(images) == 0 {
		for _, e := range ev { // still show the named picture for location/metadata questions
			if e.Mentioned && e.imagePath != "" {
				if img, err := keyframe.SmallJPEG(e.imagePath, 1024); err == nil {
					images = append(images, *img)
				}
				break
			}
		}
	}
	temp := 0.1
	var out struct {
		Answer     string  `json:"answer"`
		Citations  []int   `json:"citations"`
		Confidence float64 `json:"confidence"`
	}
	resp, err := s.AI.ChatJSON(ctx, llm.ChatRequest{Purpose: "qa-answer", MaxTokens: 1200, Temperature: &temp,
		Messages: []llm.Message{{Text: fmt.Sprintf(answerPrompt, q, b.String()), Images: images}},
		Fallback: func() (string, error) { return fallbackAnswer(ev), nil },
	}, &out)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		json.Unmarshal([]byte(fallbackAnswer(ev)), &out) //nolint:errcheck
		res.Fallback = true
	}
	if resp != nil && resp.Fallback {
		res.Fallback = true
	}
	res.Answer, res.Citations, res.Confidence = out.Answer, out.Citations, out.Confidence
	return nil
}

// fallbackAnswer returns the most relevant evidence verbatim when the VLM is
// unavailable.
func fallbackAnswer(ev []*Evidence) string {
	if len(ev) == 0 {
		b, _ := json.Marshal(g.Map{"answer": "模型不可用，且没有检索到相关资料。", "citations": []int{}, "confidence": 0})
		return string(b)
	}
	best := ev[0]
	for _, e := range ev {
		if rel(e) > rel(best) {
			best = e
		}
	}
	b, _ := json.Marshal(g.Map{"answer": "模型暂不可用，以下是最相关的资料原文：" + trimRunes(best.Text, 300),
		"citations": []int{best.N}, "confidence": 0.2})
	return string(b)
}

func trimRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func val(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}
