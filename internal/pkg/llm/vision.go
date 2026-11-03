package llm

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
)

// visionBatch is how many inputs go in one multimodal-embedding request.
// Measured: 4 images take ~4.4 s in one call vs ~2 s each separately.
const visionBatch = 4

// MMInput is one item for the multimodal embedding: an image or a text.
type MMInput struct {
	Image *Image
	Text  string
}

type mmWire struct {
	Output struct {
		Embeddings []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
			Type      string    `json:"type"`
		} `json:"embeddings"`
	} `json:"output"`
	Usage struct {
		InputTokens int `json:"input_tokens"`
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// VisionEnabled reports whether the multimodal embedding endpoint is set up.
func (g *Gateway) VisionEnabled() bool { return g.vis != nil && !g.vis.disabled }

// VisionDim is the size of multimodal embedding vectors.
func (g *Gateway) VisionDim() int { return g.visDim }

// VisionModel is the configured multimodal embedding model name.
func (g *Gateway) VisionModel() string { return g.vis.model }

// EmbedMultimodal embeds images and/or texts into the shared image-text
// space (Aliyun DashScope native multimodal-embedding API). There is no
// meaningful local substitute for these vectors, so failures are returned to
// the caller, which skips the dependent step (dedup, visual localisation).
func (g *Gateway) EmbedMultimodal(ctx context.Context, purpose string, assetID int64, inputs []MMInput) (*EmbedResult, error) {
	out := &EmbedResult{Vectors: make([][]float32, len(inputs)), Model: g.vis.model}
	for start := 0; start < len(inputs); start += visionBatch {
		batch := inputs[start:min(start+visionBatch, len(inputs))]
		res, _, err := g.visionOnce(ctx, purpose, assetID, batch)
		if err != nil {
			return nil, err
		}
		copy(out.Vectors[start:], res.Vectors)
		out.Usage.PromptTokens += res.Usage.PromptTokens
		out.Usage.TotalTokens += res.Usage.TotalTokens
	}
	return out, nil
}

func (g *Gateway) visionOnce(ctx context.Context, purpose string, assetID int64, batch []MMInput) (*EmbedResult, bool, error) {
	contents := make([]map[string]string, len(batch))
	var summary []string
	for i, in := range batch {
		if in.Image != nil {
			mime := in.Image.MIME
			if mime == "" {
				mime = "image/jpeg"
			}
			contents[i] = map[string]string{"image": "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(in.Image.Data)}
			summary = append(summary, fmt.Sprintf("[image %dx%d %dKB]", in.Image.Width, in.Image.Height, len(in.Image.Data)/1024))
		} else {
			contents[i] = map[string]string{"text": in.Text}
			summary = append(summary, in.Text)
		}
	}
	body := map[string]any{"model": g.vis.model, "input": map[string]any{"contents": contents}}
	return run(ctx, g, g.vis, callMeta{purpose: purpose, assetID: assetID, input: strings.Join(summary, " | ")},
		func(ctx context.Context) (attemptResult[*EmbedResult], error) {
			var w mmWire
			if err := g.postJSON(ctx, g.vis, body, &w); err != nil {
				return attemptResult[*EmbedResult]{}, err
			}
			if len(w.Output.Embeddings) != len(batch) {
				return attemptResult[*EmbedResult]{}, fmt.Errorf("%w: %d vectors for %d inputs (%s %s)",
					errBadResponse, len(w.Output.Embeddings), len(batch), w.Code, w.Message)
			}
			vecs := make([][]float32, len(batch))
			for _, e := range w.Output.Embeddings {
				if e.Index < 0 || e.Index >= len(batch) || len(e.Embedding) != g.visDim {
					return attemptResult[*EmbedResult]{}, fmt.Errorf("%w: bad vector index %d or dim %d", errBadResponse, e.Index, len(e.Embedding))
				}
				vecs[e.Index] = normalize32(e.Embedding)
			}
			u := Usage{PromptTokens: w.Usage.InputTokens, TotalTokens: w.Usage.TotalTokens}
			return attemptResult[*EmbedResult]{value: &EmbedResult{Vectors: vecs, Model: g.vis.model, Usage: u},
				usage: u, output: fmt.Sprintf("%d vectors x %d", len(vecs), g.visDim)}, nil
		}, nil)
}

// normalize32 scales v to unit length so dot products are cosine similarities.
func normalize32(v []float32) []float32 {
	var n float64
	for _, x := range v {
		n += float64(x) * float64(x)
	}
	if n == 0 {
		return v
	}
	inv := float32(1 / sqrt(n))
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x * inv
	}
	return out
}

// Dot is the dot product of two equal-length vectors.
func Dot(a, b []float32) float32 {
	var s float32
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}
