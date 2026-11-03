// Package search is hybrid retrieval over keyframes and document chunks:
// BM25 keywords, text embeddings, cross-modal (text to image) embeddings and
// hard space/time filters, fused with reciprocal rank fusion and re-ranked by
// the decision model.
package search

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/frame/g"

	"multimo-backend/internal/pkg/llm"
	"multimo-backend/internal/service/geo"
	"multimo-backend/internal/service/keyframe"
)

const (
	listDepth   = 100 // results taken from each retriever
	rrfK        = 60
	rerankDepth = 50
	relevantP   = 0.5 // decision probability above which a hit counts as relevant
)

type Service struct {
	DB  gdb.DB
	AI  *llm.Gateway
	Geo *geo.Service

	mu      sync.Mutex
	kfIndex *BM25
	chIndex *BM25
	version string
	checked time.Time
}

type Request struct {
	Query       string   `json:"query"`
	Collections []string `json:"collections"`
	Types       []string `json:"types"`
	AssetIDs    []int64  `json:"assetIds"`
	TopK        int      `json:"topK"`
	GroupBy     string   `json:"groupBy"` // asset (default) | unit
	NoRerank    bool     `json:"noRerank"`
	Filters     *Filters `json:"filters"` // explicit filters override parsed ones
}

type Signals struct {
	BM25Rank   int      `json:"bm25Rank,omitempty"`
	TextRank   int      `json:"textRank,omitempty"`
	ImageRank  int      `json:"imageRank,omitempty"`
	ObjectRank int      `json:"objectRank,omitempty"`
	RRF        float64  `json:"rrf"`
	Decision   *float64 `json:"decision,omitempty"`
}

type Result struct {
	Kind         string     `json:"kind"` // keyframe | chunk
	UnitID       int64      `json:"unitId"`
	AssetID      int64      `json:"assetId"`
	AssetType    string     `json:"assetType"`
	Collection   string     `json:"collection"`
	Filename     string     `json:"filename"`
	Path         string     `json:"path"` // registered path (testdata-relative) or filename
	Score        float64    `json:"score"`
	Relevant     bool       `json:"relevant"`
	Snippet      string     `json:"snippet"`
	TsSec        *float64   `json:"tsSec,omitempty"`
	Page         *int       `json:"page,omitempty"`
	Lat          *float64   `json:"lat,omitempty"`
	Lon          *float64   `json:"lon,omitempty"`
	CapturedAt   *time.Time `json:"capturedAt,omitempty"`
	ImageUrl     string     `json:"imageUrl,omitempty"`
	ThumbnailUrl string     `json:"thumbnailUrl,omitempty"`
	Signals      Signals    `json:"signals"`
}

type Response struct {
	Query    string           `json:"query"`
	Filters  Filters          `json:"filters"`
	Objects  *ObjectQuery     `json:"objects,omitempty"`
	Reranked bool             `json:"reranked"`
	Results  []*Result        `json:"results"`
	Timings  map[string]int64 `json:"timingsMs"`
}

type assetInfo struct {
	ID         int64
	Type       string
	Collection string
	Filename   string
	Path       string
	Thumb      bool
	Title      string
}

// ---------------------------------------------------------------- indexes

// ensureIndexes rebuilds the BM25 indexes when keyframe or chunk text has
// changed (checked at most every 10 s).
func (s *Service) ensureIndexes(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.kfIndex != nil && time.Since(s.checked) < 10*time.Second {
		return nil
	}
	v, err := s.DB.GetValue(ctx, `SELECT concat_ws(':',
		(SELECT count(*) FROM keyframes WHERE search_text IS NOT NULL),
		(SELECT coalesce(sum(length(search_text)), 0) FROM keyframes),
		(SELECT count(*) FROM doc_chunks), (SELECT coalesce(max(id), 0) FROM doc_chunks),
		(SELECT count(*) FROM keyframes WHERE lat IS NOT NULL))`)
	if err != nil {
		return err
	}
	s.checked = time.Now()
	if v.String() == s.version && s.kfIndex != nil {
		return nil
	}
	start := time.Now()
	kf, err := s.DB.GetAll(ctx, `SELECT k.id, k.asset_id, coalesce(k.lat, a.lat) AS lat, coalesce(k.lon, a.lon) AS lon,
		coalesce(extract(epoch FROM coalesce(k.captured_at, a.captured_at))::bigint, 0) AS ts, k.search_text
		FROM keyframes k JOIN assets a ON a.id = k.asset_id WHERE k.search_text IS NOT NULL`)
	if err != nil {
		return err
	}
	units, texts := make([]Unit, len(kf)), make([]string, len(kf))
	for i, r := range kf {
		units[i] = Unit{Kind: "keyframe", ID: r["id"].Int64(), AssetID: r["asset_id"].Int64(),
			Lat: nullFloat(r["lat"]), Lon: nullFloat(r["lon"]), TS: r["ts"].Int64()}
		texts[i] = r["search_text"].String()
	}
	ch, err := s.DB.GetAll(ctx, `SELECT id, asset_id, text FROM doc_chunks`)
	if err != nil {
		return err
	}
	cu, ct := make([]Unit, len(ch)), make([]string, len(ch))
	for i, r := range ch {
		cu[i] = Unit{Kind: "chunk", ID: r["id"].Int64(), AssetID: r["asset_id"].Int64(), Lat: math.NaN(), Lon: math.NaN()}
		ct[i] = r["text"].String()
	}
	s.kfIndex, s.chIndex, s.version = NewBM25(units, texts), NewBM25(cu, ct), v.String()
	g.Log().Infof(ctx, "search index rebuilt: %d keyframes, %d chunks in %s", len(units), len(cu), time.Since(start).Round(time.Millisecond))
	return nil
}

func nullFloat(v interface {
	IsNil() bool
	Float64() float64
}) float64 {
	if v.IsNil() {
		return math.NaN()
	}
	return v.Float64()
}

// ---------------------------------------------------------------- search

func (s *Service) Search(ctx context.Context, req Request) (*Response, error) {
	t0 := time.Now()
	timings := map[string]int64{}
	mark := func(k string, since time.Time) { timings[k] = time.Since(since).Milliseconds() }
	if req.TopK <= 0 {
		req.TopK = 10
	}
	q := strings.TrimSpace(req.Query)
	if q == "" {
		return nil, fmt.Errorf("empty query")
	}

	// 1. filters
	ts := time.Now()
	f := ParseFilters(q)
	if req.Filters != nil {
		f = mergeFilters(f, *req.Filters)
	}
	if !f.Spatial() && MayNamePlace(q) {
		s.placeFilter(ctx, q, &f)
	}
	mark("understand", ts)

	if err := s.ensureIndexes(ctx); err != nil {
		return nil, err
	}
	assets, err := s.allowedAssets(ctx, req)
	if err != nil {
		return nil, err
	}
	hard := f.Spatial() || f.Temporal()
	keepKF := func(u Unit) bool { return assets[u.AssetID] != nil && passes(u, &f) }
	keepCh := func(u Unit) bool { return assets[u.AssetID] != nil && !hard }

	// 2. retrieval
	tr := time.Now()
	var textVec, imgVec []float32
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if r, err := s.AI.Embed(ctx, "search-query", 0, []string{q}); err == nil && !r.Fallback {
			textVec = r.Vectors[0]
		}
	}()
	go func() {
		defer wg.Done()
		if s.AI.VisionEnabled() {
			if r, err := s.AI.EmbedMultimodal(ctx, "search-query-image", 0, []llm.MMInput{{Text: q}}); err == nil {
				imgVec = r.Vectors[0]
			}
		}
	}()
	bmKF := s.kfIndex.Search(q, listDepth, keepKF)
	bmCh := s.chIndex.Search(q, listDepth, keepCh)
	wg.Wait()
	mark("embed+bm25", tr)

	tv := time.Now()
	var vecKF, vecCh, vecImg []Hit
	if textVec != nil {
		vecKF, _ = s.vectorKeyframes(ctx, "embedding", textVec, assets, &f)
		if !hard {
			vecCh, _ = s.vectorChunks(ctx, textVec, assets)
		}
	}
	if imgVec != nil {
		vecImg, _ = s.vectorKeyframes(ctx, "image_embedding", imgVec, assets, &f)
	}
	var objHits []Hit
	oq := ParseObjects(q)
	if oq != nil {
		objHits, _ = s.objectKeyframes(ctx, oq, assets, &f)
	}
	mark("vector", tv)

	// 3. fusion: keyframe and chunk lists are fused separately per signal so
	// a single strong document cannot crowd out images (and vice versa).
	fused := fuse(map[string][]Hit{"bm25": append(bmKF, bmCh...), "text": append(vecKF, vecCh...), "image": vecImg,
		"object": objHits})
	if len(fused) == 0 {
		return &Response{Query: q, Filters: f, Objects: oq, Results: []*Result{}, Timings: timings}, nil
	}

	results, err := s.hydrate(ctx, fused[:min(len(fused), 3*rerankDepth)], assets)
	if err != nil {
		return nil, err
	}

	// 4. decision re-rank (skipped when hard filters already define relevance)
	reranked := false
	tdec := time.Now()
	if !req.NoRerank && !hard {
		reranked = s.rerank(ctx, q, results)
	} else {
		maxRRF := results[0].Signals.RRF
		for _, r := range results {
			r.Score = r.Signals.RRF / maxRRF
			r.Relevant = hard
		}
	}
	mark("rerank", tdec)
	sort.SliceStable(results, func(i, j int) bool { return results[i].Score > results[j].Score })

	if req.GroupBy != "unit" {
		results = groupByAsset(results)
	}
	if len(results) > req.TopK {
		results = results[:req.TopK]
	}
	mark("total", t0)
	return &Response{Query: q, Filters: f, Objects: oq, Reranked: reranked, Results: results, Timings: timings}, nil
}

func mergeFilters(parsed, explicit Filters) Filters {
	out := parsed
	if explicit.Lat != nil {
		out.Lat, out.Lon, out.RadiusM = explicit.Lat, explicit.Lon, explicit.RadiusM
		out.note("point", "request")
	}
	for _, p := range []struct {
		dst **float64
		src *float64
	}{
		{&out.South, explicit.South}, {&out.North, explicit.North}, {&out.West, explicit.West}, {&out.East, explicit.East}} {
		if p.src != nil {
			*p.dst = p.src
		}
	}
	if explicit.From != nil || explicit.To != nil {
		out.From, out.To = explicit.From, explicit.To
		out.note("time", "request")
	}
	return out
}

func passes(u Unit, f *Filters) bool {
	if f.Spatial() {
		if math.IsNaN(u.Lat) {
			return false
		}
		if f.Lat != nil && geo.Haversine(*f.Lat, *f.Lon, u.Lat, u.Lon) > f.RadiusM {
			return false
		}
		if (f.South != nil && u.Lat < *f.South) || (f.North != nil && u.Lat > *f.North) ||
			(f.West != nil && u.Lon < *f.West) || (f.East != nil && u.Lon > *f.East) {
			return false
		}
	}
	if f.Temporal() {
		if u.TS == 0 || (f.From != nil && u.TS < f.From.Unix()) || (f.To != nil && u.TS > f.To.Unix()) {
			return false
		}
	}
	return true
}

func (s *Service) allowedAssets(ctx context.Context, req Request) (map[int64]*assetInfo, error) {
	m := s.DB.Model("assets").Ctx(ctx).Fields("id, type, collection, filename, meta->>'registeredPath' AS path, "+
		"meta->>'thumbnail' AS thumb, meta->>'title' AS title").Where("status", "ready")
	if len(req.Collections) > 0 {
		m = m.WhereIn("collection", req.Collections)
	}
	if len(req.Types) > 0 {
		m = m.WhereIn("type", req.Types)
	}
	if len(req.AssetIDs) > 0 {
		m = m.WhereIn("id", req.AssetIDs)
	}
	rows, err := m.All()
	if err != nil {
		return nil, err
	}
	out := make(map[int64]*assetInfo, len(rows))
	for _, r := range rows {
		p := r["path"].String()
		if p == "" {
			p = r["filename"].String()
		}
		out[r["id"].Int64()] = &assetInfo{ID: r["id"].Int64(), Type: r["type"].String(), Collection: r["collection"].String(),
			Filename: r["filename"].String(), Path: p, Thumb: r["thumb"].String() != "", Title: r["title"].String()}
	}
	return out, nil
}

// sqlFilter renders the space/time filters for keyframe queries.
func sqlFilter(f *Filters) (string, []any) {
	var conds []string
	var args []any
	lat, lon := "coalesce(k.lat, a.lat)", "coalesce(k.lon, a.lon)"
	if f.Lat != nil {
		conds = append(conds, fmt.Sprintf(`%s IS NOT NULL AND 2 * 6371008.8 * asin(sqrt(power(sin(radians(%s - ?) / 2), 2) +
			cos(radians(?)) * cos(radians(%s)) * power(sin(radians(%s - ?) / 2), 2))) <= ?`, lat, lat, lat, lon))
		args = append(args, *f.Lat, *f.Lat, *f.Lon, f.RadiusM)
	}
	for _, b := range []struct {
		v   *float64
		sql string
	}{{f.South, lat + " >= ?"}, {f.North, lat + " <= ?"}, {f.West, lon + " >= ?"}, {f.East, lon + " <= ?"}} {
		if b.v != nil {
			conds = append(conds, b.sql)
			args = append(args, *b.v)
		}
	}
	ts := "coalesce(k.captured_at, a.captured_at)"
	if f.From != nil {
		conds = append(conds, ts+" >= ?")
		args = append(args, *f.From)
	}
	if f.To != nil {
		conds = append(conds, ts+" <= ?")
		args = append(args, *f.To)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " AND " + strings.Join(conds, " AND "), args
}

func ids(assets map[int64]*assetInfo) []int64 {
	out := make([]int64, 0, len(assets))
	for id := range assets {
		out = append(out, id)
	}
	return out
}

// vectorKeyframes ranks keyframes by cosine similarity on the given column.
// The "+ 0" keeps the planner off the HNSW index so filtering is exact
// (fine at this scale; switch to iterative index scans for large corpora).
func (s *Service) vectorKeyframes(ctx context.Context, col string, vec []float32, assets map[int64]*assetInfo, f *Filters) ([]Hit, error) {
	if len(assets) == 0 {
		return nil, nil
	}
	where, args := sqlFilter(f)
	lit := keyframe.VectorLiteral(vec)
	all := append([]any{lit, ids(assets)}, args...)
	all = append(all, lit, listDepth)
	rows, err := s.DB.GetAll(ctx, fmt.Sprintf(`SELECT k.id, k.asset_id, 1 - (k.%[1]s <=> ?::vector) AS sim
		FROM keyframes k JOIN assets a ON a.id = k.asset_id
		WHERE k.%[1]s IS NOT NULL AND k.asset_id IN (?)%[2]s
		ORDER BY (k.%[1]s <=> ?::vector) + 0 LIMIT ?`, col, where), all...)
	if err != nil {
		g.Log().Warningf(ctx, "vector search on %s: %v", col, err)
		return nil, err
	}
	hits := make([]Hit, len(rows))
	for i, r := range rows {
		hits[i] = Hit{Unit: Unit{Kind: "keyframe", ID: r["id"].Int64(), AssetID: r["asset_id"].Int64()}, Score: r["sim"].Float64()}
	}
	return hits, nil
}

func (s *Service) vectorChunks(ctx context.Context, vec []float32, assets map[int64]*assetInfo) ([]Hit, error) {
	if len(assets) == 0 {
		return nil, nil
	}
	lit := keyframe.VectorLiteral(vec)
	rows, err := s.DB.GetAll(ctx, `SELECT c.id, c.asset_id, 1 - (c.embedding <=> ?::vector) AS sim FROM doc_chunks c
		WHERE c.embedding IS NOT NULL AND c.asset_id IN (?) ORDER BY (c.embedding <=> ?::vector) + 0 LIMIT ?`,
		lit, ids(assets), lit, listDepth)
	if err != nil {
		return nil, err
	}
	hits := make([]Hit, len(rows))
	for i, r := range rows {
		hits[i] = Hit{Unit: Unit{Kind: "chunk", ID: r["id"].Int64(), AssetID: r["asset_id"].Int64()}, Score: r["sim"].Float64()}
	}
	return hits, nil
}

type fusedHit struct {
	unit    Unit
	rrf     float64
	signals Signals
}

// fuse applies reciprocal rank fusion; ranks are computed within each
// (signal, kind) list.
func fuse(lists map[string][]Hit) []fusedHit {
	acc := map[string]*fusedHit{}
	for signal, hits := range lists {
		rank := map[string]int{}
		for _, h := range hits {
			rank[h.Unit.Kind]++
			r := rank[h.Unit.Kind]
			key := fmt.Sprintf("%s:%d", h.Unit.Kind, h.Unit.ID)
			fh := acc[key]
			if fh == nil {
				fh = &fusedHit{unit: h.Unit}
				acc[key] = fh
			}
			fh.rrf += 1.0 / float64(rrfK+r)
			switch signal {
			case "bm25":
				fh.signals.BM25Rank = r
			case "text":
				fh.signals.TextRank = r
			case "image":
				fh.signals.ImageRank = r
			case "object":
				fh.signals.ObjectRank = r
			}
		}
	}
	out := make([]fusedHit, 0, len(acc))
	for _, fh := range acc {
		fh.signals.RRF = fh.rrf
		out = append(out, *fh)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].rrf != out[j].rrf {
			return out[i].rrf > out[j].rrf
		}
		return out[i].unit.ID < out[j].unit.ID
	})
	return out
}

// hydrate loads display fields for fused hits.
func (s *Service) hydrate(ctx context.Context, hits []fusedHit, assets map[int64]*assetInfo) ([]*Result, error) {
	var kfIDs, chIDs []int64
	for _, h := range hits {
		if h.unit.Kind == "keyframe" {
			kfIDs = append(kfIDs, h.unit.ID)
		} else {
			chIDs = append(chIDs, h.unit.ID)
		}
	}
	kfRows := map[int64]gdb.Record{}
	if len(kfIDs) > 0 {
		rows, err := s.DB.GetAll(ctx, `SELECT k.id, k.ts_sec, coalesce(k.lat, a.lat) AS lat, coalesce(k.lon, a.lon) AS lon,
			coalesce(k.captured_at, a.captured_at) AS captured_at, k.search_text
			FROM keyframes k JOIN assets a ON a.id = k.asset_id WHERE k.id IN (?)`, kfIDs)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			kfRows[r["id"].Int64()] = r
		}
	}
	chRows := map[int64]gdb.Record{}
	if len(chIDs) > 0 {
		rows, err := s.DB.GetAll(ctx, `SELECT id, page, text FROM doc_chunks WHERE id IN (?)`, chIDs)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			chRows[r["id"].Int64()] = r
		}
	}
	out := make([]*Result, 0, len(hits))
	for _, h := range hits {
		a := assets[h.unit.AssetID]
		if a == nil {
			continue
		}
		r := &Result{Kind: h.unit.Kind, UnitID: h.unit.ID, AssetID: a.ID, AssetType: a.Type, Collection: a.Collection,
			Filename: a.Filename, Path: a.Path, Signals: h.signals}
		if a.Thumb {
			r.ThumbnailUrl = fmt.Sprintf("/api/v1/assets/%d/thumbnail", a.ID)
		}
		if h.unit.Kind == "keyframe" {
			row := kfRows[h.unit.ID]
			if row == nil {
				continue
			}
			t := row["ts_sec"].Float64()
			r.TsSec = &t
			if !row["lat"].IsNil() {
				la, lo := row["lat"].Float64(), row["lon"].Float64()
				r.Lat, r.Lon = &la, &lo
			}
			if !row["captured_at"].IsNil() {
				ct := row["captured_at"].Time()
				r.CapturedAt = &ct
			}
			r.Snippet = row["search_text"].String()
			r.ImageUrl = fmt.Sprintf("/api/v1/keyframes/%d/image", h.unit.ID)
		} else {
			row := chRows[h.unit.ID]
			if row == nil {
				continue
			}
			p := row["page"].Int()
			r.Page = &p
			r.Snippet = row["text"].String()
		}
		out = append(out, r)
	}
	return out, nil
}

// rerank asks the decision model whether each of the top candidates meets
// the query. Returns false if the model was unavailable.
func (s *Service) rerank(ctx context.Context, q string, results []*Result) bool {
	n := min(len(results), rerankDepth)
	qs := make(map[string]llm.DecisionQuestion, n)
	for i := 0; i < n; i++ {
		qs[fmt.Sprintf("c%d", i)] = llm.DecisionQuestion{Type: "choice",
			Instructions: "候选内容：" + candidateText(results[i]) + "\n这条候选内容是否满足检索需求？",
			Criteria:     map[string]string{"yes": "满足：内容与检索需求相符", "no": "不满足：与检索需求无关或不符"}}
	}
	resp, err := s.AI.Decide(ctx, llm.DecisionRequest{Purpose: "search-rerank", State: "检索需求：" + q, Questions: qs})
	if err != nil {
		return false
	}
	maxRRF := results[0].Signals.RRF
	for i, r := range results {
		norm := r.Signals.RRF / maxRRF
		if i >= n {
			r.Score = 0.2 * norm // below every judged candidate
			continue
		}
		a := resp.Answers[fmt.Sprintf("c%d", i)]
		p := a.Probabilities["yes"]
		r.Signals.Decision = &p
		r.Score = 0.8*p + 0.2*norm
		r.Relevant = p >= relevantP
	}
	return !resp.Fallback
}

func candidateText(r *Result) string {
	snip := []rune(r.Snippet)
	if len(snip) > 300 {
		snip = snip[:300]
	}
	kind := map[string]string{"video": "视频截帧", "image": "航拍图像", "remote_sensing": "遥感影像", "document": "文档"}[r.AssetType]
	head := fmt.Sprintf("[%s %s]", kind, r.Filename)
	if r.TsSec != nil && r.AssetType == "video" {
		head = fmt.Sprintf("[%s %s 第%.1f秒]", kind, r.Filename, *r.TsSec)
	}
	if r.Page != nil {
		head = fmt.Sprintf("[%s %s 第%d页]", kind, r.Filename, *r.Page)
	}
	return head + " " + string(snip)
}

// groupByAsset keeps the best unit per asset.
func groupByAsset(results []*Result) []*Result {
	seen := map[int64]bool{}
	out := results[:0]
	for _, r := range results {
		if seen[r.AssetID] {
			continue
		}
		seen[r.AssetID] = true
		out = append(out, r)
	}
	return out
}

// objectKeyframes ranks keyframes by the VLM's per-class counts.
func (s *Service) objectKeyframes(ctx context.Context, oq *ObjectQuery, assets map[int64]*assetInfo, f *Filters) ([]Hit, error) {
	if len(assets) == 0 {
		return nil, nil
	}
	where, args := sqlFilter(f)
	rows, err := s.DB.GetAll(ctx, `SELECT k.id, k.asset_id, k.meta->'analysis'->>'counts' AS counts
		FROM keyframes k JOIN assets a ON a.id = k.asset_id
		WHERE k.description_source = 'vlm' AND k.asset_id IN (?)`+where, append([]any{ids(assets)}, args...)...)
	if err != nil {
		return nil, err
	}
	in := make([]objRow, len(rows))
	for i, r := range rows {
		in[i] = objRow{id: r["id"].Int64(), assetID: r["asset_id"].Int64(), counts: r["counts"].String()}
	}
	return rankByObjects(oq, in), nil
}
