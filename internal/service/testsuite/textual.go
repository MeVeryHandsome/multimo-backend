package testsuite

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"time"

	"multimo-backend/internal/pkg/llm"
	"multimo-backend/internal/service/geo"
	"multimo-backend/internal/service/qa"
	"multimo-backend/internal/service/search"
)

// ---------------------------------------------------------------- retrieval

type QueryResult struct {
	ID        string   `json:"id"`
	Type      string   `json:"type"`
	Query     string   `json:"query"`
	Relevant  int      `json:"relevant"`
	Precision float64  `json:"precision"` // hits@10 / min(10,|R|)
	Recall    float64  `json:"recall"`    // hits@max(10,|R|) / |R|
	P5        float64  `json:"p5"`
	P10       float64  `json:"p10"`
	R5        float64  `json:"r5"`
	R10       float64  `json:"r10"`
	SetP      float64  `json:"setPrecision"`
	SetR      float64  `json:"setRecall"`
	Flagged   int      `json:"flagged"`
	LatencyMs int64    `json:"latencyMs"`
	Top       []string `json:"top"` // first 10 result paths, '*' marks relevant
	Error     string   `json:"error,omitempty"`
}

type TypeRow struct {
	Type      string  `json:"type"`
	N         int     `json:"n"`
	Precision float64 `json:"precision"`
	Recall    float64 `json:"recall"`
	SetP      float64 `json:"setPrecision"`
	SetR      float64 `json:"setRecall"`
}

type RetrievalSection struct {
	Queries   []QueryResult `json:"queries"`
	ByType    []TypeRow     `json:"byType"`
	Precision float64       `json:"precision"`
	Recall    float64       `json:"recall"`
	P5        float64       `json:"p5"`
	P10       float64       `json:"p10"`
	R5        float64       `json:"r5"`
	R10       float64       `json:"r10"`
	SetP      float64       `json:"setPrecision"`
	SetR      float64       `json:"setRecall"`
	Latency   Stats         `json:"latencyMs"`
}

type queryGT struct {
	ID          string   `json:"id"`
	Type        string   `json:"type"`
	Query       string   `json:"query"`
	Collections []string `json:"collections"`
	Relevant    []string `json:"relevant"`
}

func (s *Suite) retrieval(ctx context.Context, progress func(string)) (*RetrievalSection, error) {
	qs, err := readJSONL[queryGT](s, "queries.jsonl")
	if err != nil {
		return nil, err
	}
	out := make([]QueryResult, len(qs))
	var done int
	var mu sync.Mutex
	parallel(len(qs), s.Concurrency, func(i int) {
		out[i] = s.oneQuery(ctx, qs[i])
		mu.Lock()
		done++
		progress(fmt.Sprintf("retrieval %d/%d", done, len(qs)))
		mu.Unlock()
	})
	sec := &RetrievalSection{Queries: out}
	byType := map[string][]QueryResult{}
	var lat []float64
	for _, q := range out {
		byType[q.Type] = append(byType[q.Type], q)
		lat = append(lat, float64(q.LatencyMs))
	}
	mean := func(L []QueryResult, f func(QueryResult) float64) float64 {
		var s float64
		for _, x := range L {
			s += f(x)
		}
		return round(s/float64(max(len(L), 1)), 4)
	}
	sec.Precision = mean(out, func(q QueryResult) float64 { return q.Precision })
	sec.Recall = mean(out, func(q QueryResult) float64 { return q.Recall })
	sec.P5 = mean(out, func(q QueryResult) float64 { return q.P5 })
	sec.P10 = mean(out, func(q QueryResult) float64 { return q.P10 })
	sec.R5 = mean(out, func(q QueryResult) float64 { return q.R5 })
	sec.R10 = mean(out, func(q QueryResult) float64 { return q.R10 })
	sec.SetP = mean(out, func(q QueryResult) float64 { return q.SetP })
	sec.SetR = mean(out, func(q QueryResult) float64 { return q.SetR })
	sec.Latency = Summarise(lat)
	types := make([]string, 0, len(byType))
	for t := range byType {
		types = append(types, t)
	}
	sort.Strings(types)
	for _, t := range types {
		L := byType[t]
		sec.ByType = append(sec.ByType, TypeRow{Type: t, N: len(L),
			Precision: mean(L, func(q QueryResult) float64 { return q.Precision }),
			Recall:    mean(L, func(q QueryResult) float64 { return q.Recall }),
			SetP:      mean(L, func(q QueryResult) float64 { return q.SetP }),
			SetR:      mean(L, func(q QueryResult) float64 { return q.SetR })})
	}
	return sec, nil
}

func (s *Suite) oneQuery(ctx context.Context, q queryGT) QueryResult {
	r := QueryResult{ID: q.ID, Type: q.Type, Query: q.Query, Relevant: len(q.Relevant)}
	rel := map[string]bool{}
	for _, p := range q.Relevant {
		rel[p] = true
	}
	start := time.Now()
	res, err := s.Search.Search(ctx, search.Request{Query: q.Query, Collections: q.Collections, TopK: max(50, len(rel)), GroupBy: "asset"})
	r.LatencyMs = time.Since(start).Milliseconds()
	if err != nil || len(rel) == 0 {
		if err != nil {
			r.Error = err.Error()
		}
		return r
	}
	paths := make([]string, len(res.Results))
	flagged := make([]bool, len(res.Results))
	for i, x := range res.Results {
		paths[i], flagged[i] = x.Path, x.Relevant
	}
	scoreRanking(&r, paths, flagged, rel)
	return r
}

// scoreRanking fills the ranking metrics of r from the ranked result paths,
// the system's own relevance flags and the ground-truth set.
func scoreRanking(r *QueryResult, paths []string, flagged []bool, rel map[string]bool) {
	R := len(rel)
	if R == 0 {
		return
	}
	hitsAt := func(k int) int {
		n := 0
		for i := 0; i < len(paths) && i < k; i++ {
			if rel[paths[i]] {
				n++
			}
		}
		return n
	}
	r.Precision = round(float64(hitsAt(10))/float64(min(10, R)), 4)
	r.Recall = round(float64(hitsAt(max(10, R)))/float64(R), 4)
	r.P5, r.P10 = round(float64(hitsAt(5))/5, 4), round(float64(hitsAt(10))/10, 4)
	r.R5, r.R10 = round(float64(hitsAt(5))/float64(R), 4), round(float64(hitsAt(10))/float64(R), 4)
	flaggedRel := 0
	r.Flagged, r.Top = 0, nil
	for i, p := range paths {
		if flagged[i] {
			r.Flagged++
			if rel[p] {
				flaggedRel++
			}
		}
		if i < 10 {
			mark := ""
			if rel[p] {
				mark = "*"
			}
			r.Top = append(r.Top, mark+p)
		}
	}
	r.SetP = round(ratio(flaggedRel, r.Flagged), 4)
	r.SetR = round(ratio(flaggedRel, R), 4)
}

// ---------------------------------------------------------------- QA

type QAItem struct {
	ID        string   `json:"id"`
	Category  string   `json:"category"`
	Question  string   `json:"question"`
	Reference string   `json:"reference"`
	Answer    string   `json:"answer"`
	Correct   bool     `json:"correct"`
	Grade     *float64 `json:"grade,omitempty"` // decision-model expected grade 0..2
	GeoErrorM *float64 `json:"geoErrorM,omitempty"`
	Method    string   `json:"method"` // judge | geo
	Intent    string   `json:"intent"`
	LatencyMs int64    `json:"latencyMs"`
	Error     string   `json:"error,omitempty"`
}

type CategoryRow struct {
	Category string  `json:"category"`
	N        int     `json:"n"`
	Correct  int     `json:"correct"`
	Accuracy float64 `json:"accuracy"`
}

type QASection struct {
	Items      []QAItem      `json:"items"`
	ByCategory []CategoryRow `json:"byCategory"`
	Correct    int           `json:"correct"`
	Total      int           `json:"total"`
	Accuracy   float64       `json:"accuracy"`
	MeanGrade  float64       `json:"meanGrade"`
	Latency    Stats         `json:"latencyMs"`
}

type qaGT struct {
	ID        string `json:"id"`
	Category  string `json:"category"`
	Question  string `json:"question"`
	Reference string `json:"reference"`
	Check     *struct {
		Type       string  `json:"type"`
		Lat        float64 `json:"lat"`
		Lon        float64 `json:"lon"`
		ToleranceM float64 `json:"tolerance_m"`
	} `json:"check"`
}

func (s *Suite) qa(ctx context.Context, progress func(string)) (*QASection, error) {
	items, err := readJSONL[qaGT](s, "qa_pairs.jsonl")
	if err != nil {
		return nil, err
	}
	out := make([]QAItem, len(items))
	var done int
	var mu sync.Mutex
	parallel(len(items), s.Concurrency, func(i int) {
		out[i] = s.oneQA(ctx, items[i])
		mu.Lock()
		done++
		progress(fmt.Sprintf("qa %d/%d", done, len(items)))
		mu.Unlock()
	})
	sec := &QASection{Items: out, Total: len(out)}
	byCat := map[string]*CategoryRow{}
	var order []string
	var grades, lat []float64
	for _, it := range out {
		c := byCat[it.Category]
		if c == nil {
			c = &CategoryRow{Category: it.Category}
			byCat[it.Category] = c
			order = append(order, it.Category)
		}
		c.N++
		if it.Correct {
			c.Correct++
			sec.Correct++
		}
		if it.Grade != nil {
			grades = append(grades, *it.Grade)
		}
		lat = append(lat, float64(it.LatencyMs))
	}
	for _, k := range order {
		c := byCat[k]
		c.Accuracy = round(ratio(c.Correct, c.N), 4)
		sec.ByCategory = append(sec.ByCategory, *c)
	}
	sec.Accuracy = round(ratio(sec.Correct, sec.Total), 4)
	sec.MeanGrade = round(Summarise(grades).Mean, 3)
	sec.Latency = Summarise(lat)
	return sec, nil
}

var (
	reLat       = regexp.MustCompile(`(北纬|南纬)\s*(\d+(?:\.\d+)?)`)
	reLon       = regexp.MustCompile(`(东经|西经)\s*(\d+(?:\.\d+)?)`)
	reLatSigned = regexp.MustCompile(`纬度\s*[:：为是]?\s*(-?\d+(?:\.\d+)?)`)
	reLonSigned = regexp.MustCompile(`经度\s*[:：为是]?\s*(-?\d+(?:\.\d+)?)`)
)

// answerCoords pulls the first latitude/longitude pair from an answer,
// written with hemisphere words (北纬 29.77°、东经 115.97°) or as signed
// decimals after 纬度/经度.
func answerCoords(s string) (float64, float64, bool) {
	lat, okLat := 0.0, false
	if m := reLat.FindStringSubmatch(s); m != nil {
		lat, _ = strconv.ParseFloat(m[2], 64)
		if m[1] == "南纬" {
			lat = -lat
		}
		okLat = true
	} else if m := reLatSigned.FindStringSubmatch(s); m != nil {
		lat, _ = strconv.ParseFloat(m[1], 64)
		okLat = true
	}
	lon, okLon := 0.0, false
	if m := reLon.FindStringSubmatch(s); m != nil {
		lon, _ = strconv.ParseFloat(m[2], 64)
		if m[1] == "西经" {
			lon = -lon
		}
		okLon = true
	} else if m := reLonSigned.FindStringSubmatch(s); m != nil {
		lon, _ = strconv.ParseFloat(m[1], 64)
		okLon = true
	}
	return lat, lon, okLat && okLon
}

func (s *Suite) oneQA(ctx context.Context, x qaGT) QAItem {
	it := QAItem{ID: x.ID, Category: x.Category, Question: x.Question, Reference: x.Reference}
	start := time.Now()
	res, err := s.QA.Answer(ctx, qa.Request{Question: x.Question})
	it.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		it.Error = err.Error()
		return it
	}
	it.Answer, it.Intent = res.Answer, res.Intent
	if x.Check != nil && x.Check.Type == "geo" {
		it.Method = "geo"
		if lat, lon, ok := answerCoords(res.Answer); ok {
			e := round(geo.Haversine(x.Check.Lat, x.Check.Lon, lat, lon), 1)
			it.GeoErrorM = &e
			it.Correct = e <= x.Check.ToleranceM
		}
		return it
	}
	it.Method = "judge"
	g, err := s.grade(ctx, x, res.Answer)
	if err != nil {
		it.Error = "judge: " + err.Error()
		return it
	}
	it.Grade = &g
	it.Correct = g >= 1.5
	return it
}

// judgeRubric is the grading instruction for the decision model. Reference
// counts come from human annotations of dense scenes, so "约" counts are
// compared with a tolerance; extra detail only counts against an answer when
// it contradicts the reference.
const judgeRubric = "对照参考答案判断系统回答是否正确回答了问题。规则：1. 只评判问题实际询问的内容，措辞和详略不影响评分；" +
	"2. 参考答案中标为“约”的数量来自人工标注，系统给出的数量与之相差不超过 25% 视为一致；" +
	"3. 回答中超出问题范围的额外细节，只有与参考答案明显矛盾时才扣分；" +
	"4. 回答无法确定、答非所问或核心事实与参考答案不符，记为错误。"

// grade asks the decision model how well the answer matches the reference.
func (s *Suite) grade(ctx context.Context, x qaGT, answer string) (float64, error) {
	resp, err := s.AI.Decide(ctx, llm.DecisionRequest{Purpose: "testsuite-qa-judge",
		State: fmt.Sprintf("问题：%s\n参考答案：%s\n系统回答：%s", x.Question, x.Reference, answer),
		Questions: map[string]llm.DecisionQuestion{"grade": {Type: "score",
			Instructions: judgeRubric,
			Criteria:     []string{"错误或未回答", "部分正确：问题所问的核心事实不完整或有偏差", "正确：问题所问的核心事实与参考答案一致"}}},
		Fallback: func() (map[string]llm.DecisionAnswer, error) { return nil, fmt.Errorf("no local judge") },
	})
	if err != nil {
		return 0, err
	}
	if resp.Fallback {
		return 0, fmt.Errorf("decision model unavailable")
	}
	return round(resp.Answers["grade"].Score, 3), nil
}

func parallel(n, workers int, fn func(i int)) {
	sem := make(chan struct{}, max(workers, 1))
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			fn(i)
		}(i)
	}
	wg.Wait()
}
