package llm

import (
	"context"
	"fmt"
	"hash/fnv"
	"math"
	"strings"
	"unicode"
)

// FallbackEmbeddingModel names vectors made by HashEmbed. Store it with each
// vector: hash vectors are only comparable with other hash vectors.
const FallbackEmbeddingModel = "local-hash-v1"

// embedBatch is the largest input list Aliyun text-embedding-v4 accepts.
const embedBatch = 10

type EmbedResult struct {
	Vectors  [][]float32
	Model    string
	Usage    Usage
	Fallback bool
}

type embedWire struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Model string `json:"model"`
	Usage struct {
		PromptTokens int `json:"prompt_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

// Embed returns one vector per text, in order. If the endpoint fails after
// all retries, every vector in the result comes from HashEmbed and
// Result.Model is FallbackEmbeddingModel; a result never mixes the two.
func (g *Gateway) Embed(ctx context.Context, purpose string, assetID int64, texts []string) (*EmbedResult, error) {
	out := &EmbedResult{Vectors: make([][]float32, len(texts)), Model: g.emb.model}
	for start := 0; start < len(texts); start += embedBatch {
		batch := texts[start:min(start+embedBatch, len(texts))]
		res, fellBack, err := g.embedOnce(ctx, purpose, assetID, batch)
		if err != nil {
			return nil, err
		}
		if fellBack {
			return g.hashAll(texts), nil
		}
		copy(out.Vectors[start:], res.Vectors)
		out.Usage.PromptTokens += res.Usage.PromptTokens
		out.Usage.TotalTokens += res.Usage.TotalTokens
	}
	return out, nil
}

func (g *Gateway) hashAll(texts []string) *EmbedResult {
	r := &EmbedResult{Vectors: make([][]float32, len(texts)), Model: FallbackEmbeddingModel, Fallback: true}
	for i, t := range texts {
		r.Vectors[i] = HashEmbed(t, g.embDim)
	}
	return r
}

func (g *Gateway) embedOnce(ctx context.Context, purpose string, assetID int64, batch []string) (*EmbedResult, bool, error) {
	body := map[string]any{"model": g.emb.model, "input": batch, "dimensions": g.embDim, "encoding_format": "float"}
	input := fmt.Sprintf("%d texts: %s", len(batch), strings.Join(batch, " | "))
	return run(ctx, g, g.emb, callMeta{purpose: purpose, assetID: assetID, input: input},
		func(ctx context.Context) (attemptResult[*EmbedResult], error) {
			var w embedWire
			if err := g.postJSON(ctx, g.emb, body, &w); err != nil {
				return attemptResult[*EmbedResult]{}, err
			}
			if len(w.Data) != len(batch) {
				return attemptResult[*EmbedResult]{}, fmt.Errorf("%w: %d vectors for %d texts", errBadResponse, len(w.Data), len(batch))
			}
			vecs := make([][]float32, len(batch))
			for _, d := range w.Data {
				if d.Index < 0 || d.Index >= len(batch) || len(d.Embedding) != g.embDim {
					return attemptResult[*EmbedResult]{}, fmt.Errorf("%w: bad vector index %d or dim %d", errBadResponse, d.Index, len(d.Embedding))
				}
				vecs[d.Index] = d.Embedding
			}
			u := Usage{PromptTokens: w.Usage.PromptTokens, TotalTokens: w.Usage.TotalTokens}
			return attemptResult[*EmbedResult]{value: &EmbedResult{Vectors: vecs, Model: g.emb.model, Usage: u},
				usage: u, output: fmt.Sprintf("%d vectors x %d", len(vecs), g.embDim)}, nil
		},
		func() (*EmbedResult, string, error) {
			return nil, "hash fallback", nil // Embed replaces the whole result with hash vectors
		})
}

// HashEmbed is the local fallback: a feature-hashed bag of character n-grams
// (CJK unigrams/bigrams, lowercase word tokens), L2-normalised. It captures
// lexical overlap only, so retrieval quality is far below a real model.
func HashEmbed(text string, dim int) []float32 {
	v := make([]float32, dim)
	add := func(tok string, w float32) {
		h := fnv.New64a()
		h.Write([]byte(tok))
		x := h.Sum64()
		sign := float32(1)
		if x>>63 == 1 {
			sign = -1
		}
		v[x%uint64(dim)] += sign * w
	}
	for _, tok := range Tokenize(text) {
		add(tok, 1)
	}
	var n float64
	for _, x := range v {
		n += float64(x * x)
	}
	if n > 0 {
		inv := float32(1 / math.Sqrt(n))
		for i := range v {
			v[i] *= inv
		}
	}
	return v
}

// Tokenize splits text into lowercase Latin words/numbers and CJK unigrams
// plus bigrams. It is also used by the keyword fallback for decisions.
func Tokenize(text string) []string {
	var toks []string
	var word []rune
	var prevCJK rune
	flush := func() {
		if len(word) > 0 {
			toks = append(toks, strings.ToLower(string(word)))
			word = word[:0]
		}
	}
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Han, r):
			flush()
			toks = append(toks, string(r))
			if prevCJK != 0 {
				toks = append(toks, string([]rune{prevCJK, r}))
			}
			prevCJK = r
			continue
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			word = append(word, r)
		default:
			flush()
		}
		prevCJK = 0
	}
	flush()
	return toks
}
