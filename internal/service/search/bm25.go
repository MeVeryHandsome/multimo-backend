package search

import (
	"math"
	"sort"

	"multimo-backend/internal/pkg/llm"
)

// BM25 is an in-memory inverted index. Chinese text is indexed as character
// unigrams plus bigrams (the usual dictionary-free approach for Chinese IR);
// Latin text as lowercase words. PostgreSQL's full-text search has no
// Chinese parser and no BM25 ranking, so scoring happens here.
type BM25 struct {
	k1, b    float64
	docs     []Unit
	lens     []float64
	avgLen   float64
	postings map[string][]posting
}

type posting struct {
	doc int32
	tf  float32
}

// Unit is one searchable item: a keyframe or a document chunk.
type Unit struct {
	Kind    string // keyframe | chunk
	ID      int64
	AssetID int64
	Lat     float64 // NaN when unknown
	Lon     float64
	TS      int64   // unix seconds, 0 when unknown
	Acc     float64 // position accuracy in metres, 0 when unknown
}

// NewBM25 indexes texts[i] as units[i].
func NewBM25(units []Unit, texts []string) *BM25 {
	ix := &BM25{k1: 1.2, b: 0.75, docs: units, lens: make([]float64, len(units)), postings: map[string][]posting{}}
	var total float64
	for i, t := range texts {
		tf := map[string]float32{}
		toks := llm.Tokenize(t)
		for _, tok := range toks {
			tf[tok]++
		}
		ix.lens[i] = float64(len(toks))
		total += ix.lens[i]
		for tok, n := range tf {
			ix.postings[tok] = append(ix.postings[tok], posting{doc: int32(i), tf: n})
		}
	}
	if len(units) > 0 {
		ix.avgLen = total / float64(len(units))
	}
	return ix
}

func (ix *BM25) Len() int { return len(ix.docs) }

// Hit is a scored unit.
type Hit struct {
	Unit  Unit
	Score float64
}

// Search returns the top k units for the query that pass keep (nil keeps all).
func (ix *BM25) Search(query string, k int, keep func(Unit) bool) []Hit {
	if ix == nil || len(ix.docs) == 0 {
		return nil
	}
	n := float64(len(ix.docs))
	scores := map[int32]float64{}
	seen := map[string]bool{}
	for _, tok := range llm.Tokenize(query) {
		if seen[tok] {
			continue
		}
		seen[tok] = true
		ps := ix.postings[tok]
		if len(ps) == 0 {
			continue
		}
		df := float64(len(ps))
		idf := math.Log(1 + (n-df+0.5)/(df+0.5))
		for _, p := range ps {
			tf := float64(p.tf)
			norm := tf * (ix.k1 + 1) / (tf + ix.k1*(1-ix.b+ix.b*ix.lens[p.doc]/ix.avgLen))
			scores[p.doc] += idf * norm
		}
	}
	hits := make([]Hit, 0, len(scores))
	for d, s := range scores {
		u := ix.docs[d]
		if keep != nil && !keep(u) {
			continue
		}
		hits = append(hits, Hit{Unit: u, Score: s})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Unit.ID < hits[j].Unit.ID
	})
	if len(hits) > k {
		hits = hits[:k]
	}
	return hits
}
