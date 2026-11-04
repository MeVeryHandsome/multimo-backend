package llm

import (
	"context"
	"encoding/base64"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Detection is one box from the detector, in pixels of the image sent.
type Detection struct {
	Label string     `json:"label"`
	Score float64    `json:"score"`
	Box   [4]float64 `json:"bbox"` // x1, y1, x2, y2
}

// DetectResult is the detector's answer for one image.
type DetectResult struct {
	Objects []Detection
	Model   string
	Width   int
	Height  int
}

type detectWire struct {
	Model   string      `json:"model"`
	Width   int         `json:"width"`
	Height  int         `json:"height"`
	Objects []Detection `json:"objects"`
}

// DetectorEnabled reports whether a detector service is configured.
func (g *Gateway) DetectorEnabled() bool { return g.det != nil && !g.det.disabled }

// DetectorModel is the configured detector name (for logs and reports).
func (g *Gateway) DetectorModel() string {
	if g.det == nil {
		return ""
	}
	return g.det.model
}

// DetectsType reports whether keyframes of an asset type go to the detector.
func (g *Gateway) DetectsType(assetType string) bool {
	return g.DetectorEnabled() && slices.Contains(g.detTypes, assetType)
}

// Detect runs the object detector on one image. There is no local
// substitute: on failure the caller falls back to boxes from the VLM.
func (g *Gateway) Detect(ctx context.Context, purpose string, assetID int64, img Image) (*DetectResult, error) {
	mime := img.MIME
	if mime == "" {
		mime = "image/jpeg"
	}
	body := map[string]any{"image": "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(img.Data)}
	if g.detConf > 0 {
		body["conf"] = g.detConf
	}
	meta := callMeta{purpose: purpose, assetID: assetID, input: fmt.Sprintf("[image %dx%d %dKB]", img.Width, img.Height, len(img.Data)/1024)}
	res, _, err := run(ctx, g, g.det, meta, func(ctx context.Context) (attemptResult[*DetectResult], error) {
		var w detectWire
		if err := g.postJSON(ctx, g.det, body, &w); err != nil {
			return attemptResult[*DetectResult]{}, err
		}
		if w.Width <= 0 || w.Height <= 0 {
			return attemptResult[*DetectResult]{}, fmt.Errorf("%w: no image size in detector response", errBadResponse)
		}
		model := w.Model
		if model == "" {
			model = g.det.model
		}
		return attemptResult[*DetectResult]{value: &DetectResult{Objects: w.Objects, Model: model, Width: w.Width, Height: w.Height},
			output: summarizeDetections(w.Objects)}, nil
	}, nil)
	return res, err
}

// summarizeDetections renders "23 objects: car 15, pedestrian 8" for the call log.
func summarizeDetections(objs []Detection) string {
	counts := map[string]int{}
	for _, o := range objs {
		counts[o.Label]++
	}
	labels := make([]string, 0, len(counts))
	for l := range counts {
		labels = append(labels, l)
	}
	sort.Slice(labels, func(i, j int) bool { return counts[labels[i]] > counts[labels[j]] })
	parts := make([]string, len(labels))
	for i, l := range labels {
		parts[i] = fmt.Sprintf("%s %d", l, counts[l])
	}
	return fmt.Sprintf("%d objects: %s", len(objs), strings.Join(parts, ", "))
}
