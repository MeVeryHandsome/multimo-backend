// Package keyframe picks representative frames: content-change sampling for
// videos, one frame for images and rasters. Each keyframe gets a
// multimodal (image) embedding used for de-duplication, image search and
// visual localisation.
package keyframe

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/frame/g"
	"golang.org/x/image/draw"

	"multimo-backend/internal/config"
	"multimo-backend/internal/model/entity"
	"multimo-backend/internal/pkg/geotiff"
	"multimo-backend/internal/pkg/llm"
	"multimo-backend/internal/pkg/media"
	"multimo-backend/internal/pkg/parser"
	"multimo-backend/internal/service/asset"
	"multimo-backend/internal/service/pipeline"
)

const Kind = "keyframes"

// dedupSimilarity: consecutive video keyframes whose image embeddings are
// at least this similar are treated as the same view.
const dedupSimilarity = 0.97

type Service struct {
	Assets     *asset.Service
	DB         gdb.DB
	Dispatcher *pipeline.Dispatcher
	AI         *llm.Gateway
	Video      config.Video
	Next       func(a *entity.Asset) []string
}

type frame struct {
	TS      float64
	FrameNo *int
	Path    string
	W, H    int
	Change  float64
	Reason  string
	Emb     []float32
}

func (s *Service) Handle(ctx context.Context, t *pipeline.Task, report pipeline.Reporter) error {
	a, err := s.Assets.Get(ctx, t.AssetID)
	if err != nil {
		return pipeline.Permanent(err)
	}
	if a.Type == parser.TypeDocument {
		return nil
	}
	dir := filepath.Join(s.Assets.Dir(a.Uid), "keyframes")
	os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	var frames []*frame
	if a.Type == parser.TypeVideo {
		frames, err = s.videoFrames(ctx, a, dir, report)
	} else {
		var f *frame
		f, err = s.stillFrame(ctx, a, dir)
		frames = []*frame{f}
	}
	if err != nil {
		return err
	}

	report(0.7, "image embeddings")
	model := ""
	if s.AI.VisionEnabled() && len(frames) > 0 {
		if err := s.embed(ctx, a, frames); err != nil {
			g.Log().Warningf(ctx, "asset %d: image embeddings unavailable, keeping all frames: %v", a.Id, err)
		} else {
			model = s.AI.VisionModel()
			if a.Type == parser.TypeVideo {
				frames = dedup(frames)
			}
		}
	}
	if err := s.save(ctx, a, frames, model); err != nil {
		return err
	}
	if s.Next != nil {
		for _, kind := range s.Next(a) {
			if err := s.Dispatcher.Enqueue(ctx, a.Id, kind); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) videoFrames(ctx context.Context, a *entity.Asset, dir string, report pipeline.Reporter) ([]*frame, error) {
	report(0.05, "detecting content changes")
	sigs, err := Signatures(ctx, a.StoragePath, s.Video.FFmpegThreads)
	if err != nil {
		return nil, err
	}
	picks := Select(sigs, Params{Threshold: s.Video.ChangeThreshold, MinGap: s.Video.MinKeyframeGapSecs,
		MaxGap: s.Video.MaxKeyframeGapSecs, MaxN: s.Video.MaxKeyframes})
	fps := 0.0
	if a.Fps != nil {
		fps = *a.Fps
	}
	frames := make([]*frame, 0, len(picks))
	for i, p := range picks {
		path := filepath.Join(dir, fmt.Sprintf("%04d.jpg", i))
		if err := media.ExtractFrame(ctx, a.StoragePath, p.Time, path, s.Video.KeyframeWidth, s.Video.FFmpegThreads); err != nil {
			return nil, err
		}
		w, h := jpegSize(path)
		f := &frame{TS: p.Time, Path: path, W: w, H: h, Change: p.Change, Reason: p.Reason}
		if fps > 0 {
			n := int(math.Round(p.Time * fps))
			f.FrameNo = &n
		}
		frames = append(frames, f)
		report(0.1+0.6*float64(i+1)/float64(len(picks)), fmt.Sprintf("extracted %d/%d keyframes", i+1, len(picks)))
	}
	return frames, nil
}

func (s *Service) stillFrame(ctx context.Context, a *entity.Asset, dir string) (*frame, error) {
	path := filepath.Join(dir, "0000.jpg")
	if a.Format == "tiff" {
		if t, err := geotiff.Open(a.StoragePath); err == nil {
			defer t.Close()
			ov, err := t.Overview(s.Video.KeyframeWidth)
			if err != nil {
				return nil, err
			}
			if err := writeJPEG(path, ov, 88); err != nil {
				return nil, err
			}
			w, h := jpegSize(path)
			return &frame{Path: path, W: w, H: h, Reason: "still"}, nil
		}
	}
	if err := media.ImageThumb(ctx, a.StoragePath, path, s.Video.KeyframeWidth); err != nil {
		return nil, err
	}
	w, h := jpegSize(path)
	return &frame{Path: path, W: w, H: h, Reason: "still"}, nil
}

// embed computes image embeddings on 512 px copies (the model downsamples
// anyway; smaller uploads are faster).
func (s *Service) embed(ctx context.Context, a *entity.Asset, frames []*frame) error {
	inputs := make([]llm.MMInput, len(frames))
	for i, f := range frames {
		img, err := SmallJPEG(f.Path, 512)
		if err != nil {
			return err
		}
		inputs[i] = llm.MMInput{Image: img}
	}
	res, err := s.AI.EmbedMultimodal(ctx, "keyframe-embedding", a.Id, inputs)
	if err != nil {
		return err
	}
	for i := range frames {
		frames[i].Emb = res.Vectors[i]
	}
	return nil
}

// dedup drops video keyframes that look the same as the last kept one.
func dedup(frames []*frame) []*frame {
	if len(frames) == 0 {
		return frames
	}
	kept := []*frame{frames[0]}
	for _, f := range frames[1:] {
		if llm.Dot(f.Emb, kept[len(kept)-1].Emb) >= dedupSimilarity {
			os.Remove(f.Path)
			continue
		}
		kept = append(kept, f)
	}
	return kept
}

func (s *Service) save(ctx context.Context, a *entity.Asset, frames []*frame, model string) error {
	return s.DB.Transaction(ctx, func(ctx context.Context, tx gdb.TX) error {
		if _, err := tx.Model("keyframes").Where("asset_id", a.Id).Delete(); err != nil {
			return err
		}
		for i, f := range frames {
			row := g.Map{"asset_id": a.Id, "idx": i, "ts_sec": f.TS, "path": s.Assets.Rel(f.Path),
				"width": f.W, "height": f.H, "scene_score": f.Change,
				"meta": fmt.Sprintf(`{"reason":%q}`, f.Reason)}
			if f.FrameNo != nil {
				row["frame_no"] = *f.FrameNo
			}
			if a.CapturedAt != nil {
				row["captured_at"] = a.CapturedAt.Time.Add(time.Duration(f.TS * float64(time.Second)))
			}
			// Photos with GPS (EXIF / container) carry it to their keyframe; videos
			// get per-frame positions from the geo step.
			if a.Lat != nil && a.Type != parser.TypeVideo && a.GeoSource != "geotiff" {
				row["lat"], row["lon"], row["geo_source"] = *a.Lat, *a.Lon, a.GeoSource
			}
			if f.Emb != nil {
				row["image_embedding"] = VectorLiteral(f.Emb)
				row["image_embedding_model"] = model
			}
			if _, err := tx.Model("keyframes").Data(row).Insert(); err != nil {
				return err
			}
		}
		return nil
	})
}

// VectorLiteral renders v in pgvector's text format.
func VectorLiteral(v []float32) string {
	var b strings.Builder
	b.Grow(len(v) * 10)
	b.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%.6g", x)
	}
	b.WriteByte(']')
	return b.String()
}

// SmallJPEG loads an image file and returns a JPEG no larger than side
// pixels on its longer edge.
func SmallJPEG(path string, side int) (*llm.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	src, _, err := image.Decode(f)
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	scale := math.Min(1, float64(side)/math.Max(float64(b.Dx()), float64(b.Dy())))
	w, h := max(1, int(float64(b.Dx())*scale)), max(1, int(float64(b.Dy())*scale))
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 85}); err != nil {
		return nil, err
	}
	return &llm.Image{Data: buf.Bytes(), MIME: "image/jpeg", Width: w, Height: h}, nil
}

func writeJPEG(path string, img image.Image, q int) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return jpeg.Encode(f, img, &jpeg.Options{Quality: q})
}

func jpegSize(path string) (int, int) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	cfg, err := jpeg.DecodeConfig(f)
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}
