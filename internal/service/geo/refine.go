package geo

import (
	"context"
	"fmt"
	"image"
	"math"
	"os"
	"sync"

	"github.com/gogf/gf/v2/frame/g"

	"multimo-backend/internal/model/entity"
	"multimo-backend/internal/pkg/geotiff"
	"multimo-backend/internal/pkg/llm"
	"multimo-backend/internal/service/pipeline"
)

// RefineKind improves visually tracked positions with a local search.
// Basemap tiles are laid out with a stride of half their side (125-200 m),
// so even when track smoothing picks the right tile its centre can be about
// 100 m from where the photo was taken. The refinement compares the photo
// with crops at the chosen tile's scale on a grid around the tracked
// position (see refineStages) and moves to the best one.
//
// Measured on UAV-VisLoc 01 (82 photos): see docs/TEST_REPORT.md.
const RefineKind = "georefine"

// refineStages are the searches run one after another, each centred on the
// previous result: 7x7 positions 50 m apart. A second stage of 5x5
// positions 25 m apart was measured and gave no further gain (median 107 m
// vs 108 m, within 100 m 47.6% vs 48.8%), so it is not used.
var refineStages = []struct{ radiusM, stepM float64 }{{150, 50}}

// RefineOffsets returns the grid of positions searched around (lat, lon),
// the centre first.
func RefineOffsets(lat, lon, radiusM, stepM float64) [][2]float64 {
	mLat, mLon := metresPerDegree(lat)
	k := int(radiusM / stepM)
	out := [][2]float64{{lat, lon}}
	for i := -k; i <= k; i++ {
		for j := -k; j <= k; j++ {
			if i == 0 && j == 0 {
				continue
			}
			out = append(out, [2]float64{lat + float64(i)*stepM/mLat, lon + float64(j)*stepM/mLon})
		}
	}
	return out
}

type refineKF struct {
	Id     int64    `orm:"id"`
	Path   string   `orm:"path"`
	Lat    *float64 `orm:"lat"`
	Lon    *float64 `orm:"lon"`
	AccM   *float64 `orm:"geo_accuracy_m"`
	Source string   `orm:"geo_source"`
}

// HandleRefine refines the visually tracked keyframes of one asset.
func (s *Service) HandleRefine(ctx context.Context, t *pipeline.Task, report pipeline.Reporter) error {
	a, err := s.Assets.Get(ctx, t.AssetID)
	if err != nil {
		return pipeline.Permanent(err)
	}
	if !s.AI.VisionEnabled() {
		return nil
	}
	var kfs []*refineKF
	if err := s.DB.Model("keyframes").Ctx(ctx).Fields("id, path, lat, lon, geo_accuracy_m, geo_source").
		Where("asset_id", a.Id).Where("geo_source", "visual-track").WhereNotNull("lat").OrderAsc("idx").Scan(&kfs); err != nil {
		return err
	}
	if len(kfs) == 0 {
		return nil
	}
	maps, _, err := s.basemapsFor(ctx, a)
	if err != nil || len(maps) == 0 {
		return err
	}
	var rasters []*geotiff.File
	for _, m := range maps {
		f, err := geotiff.Open(m.StoragePath)
		if err != nil {
			return err
		}
		defer f.Close()
		rasters = append(rasters, f)
	}
	for i, k := range kfs {
		lat, lon, err := s.refineOne(ctx, a.Id, s.matchSource(a, k.Path), *k.Lat, *k.Lon, 2*val(k.AccM), rasters)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			g.Log().Warningf(ctx, "keyframe %d: refinement skipped: %v", k.Id, err)
			continue
		}
		if _, err := s.DB.Exec(ctx, `UPDATE keyframes SET lat = ?, lon = ?, geo_source = 'visual-refined' WHERE id = ?`,
			lat, lon, k.Id); err != nil {
			return err
		}
		report(float64(i+1)/float64(len(kfs)), fmt.Sprintf("refined %d/%d keyframes", i+1, len(kfs)))
	}
	return s.setAssetFromKeyframe(ctx, a.Id, "visual-refined")
}

func val(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

// refineOne runs the refinement stages for one photo at the given tile
// side and returns the best matching position.
func (s *Service) refineOne(ctx context.Context, assetID int64, photo string, lat, lon, sideM float64, rasters []*geotiff.File) (float64, float64, error) {
	if sideM <= 0 {
		return lat, lon, fmt.Errorf("no tile scale")
	}
	queries, err := s.queryVectors(ctx, assetID, photo)
	if err != nil {
		return lat, lon, err
	}
	pos := [2]float64{lat, lon}
	for _, st := range refineStages {
		if pos, err = s.searchGrid(ctx, assetID, queries, pos, st.radiusM, st.stepM, sideM, rasters); err != nil {
			return lat, lon, err
		}
	}
	return pos[0], pos[1], nil
}

// searchGrid embeds crops on the offset grid around centre and returns the
// position whose crop matches the photo best (the centre on ties).
func (s *Service) searchGrid(ctx context.Context, assetID int64, queries [][]float32, centre [2]float64,
	radiusM, stepM, sideM float64, rasters []*geotiff.File) ([2]float64, error) {
	pts := RefineOffsets(centre[0], centre[1], radiusM, stepM)
	type crop struct {
		pos [2]float64
		img llm.MMInput
	}
	var crops []crop
	for _, p := range pts {
		for _, r := range rasters {
			img, err := TileImage(r, p[0], p[1], sideM)
			if err != nil || img == nil {
				continue
			}
			enc, err := EncodeJPEG(img)
			if err != nil {
				continue
			}
			crops = append(crops, crop{p, llm.MMInput{Image: enc}})
			break
		}
	}
	if len(crops) == 0 || crops[0].pos != pts[0] {
		return centre, fmt.Errorf("position outside the basemap")
	}
	// Embed in parallel batches of four.
	sims := make([]float64, len(crops))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	sem := make(chan struct{}, 4)
	for start := 0; start < len(crops); start += 4 {
		part := crops[start:min(start+4, len(crops))]
		inputs := make([]llm.MMInput, len(part))
		for i, c := range part {
			inputs[i] = c.img
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(start int) {
			defer wg.Done()
			defer func() { <-sem }()
			res, err := s.AI.EmbedMultimodal(ctx, "visual-refine", assetID, inputs)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			for i, v := range res.Vectors {
				best := math.Inf(-1)
				for _, q := range queries {
					best = math.Max(best, float64(llm.Dot(q, v)))
				}
				sims[start+i] = best
			}
		}(start)
	}
	wg.Wait()
	if firstErr != nil {
		return centre, firstErr
	}
	best := 0
	for i := range sims {
		if sims[i] > sims[best] {
			best = i
		}
	}
	return crops[best].pos, nil
}

// matchSource is the image a keyframe is matched from: the original file
// for photos (full resolution, no re-encoding), the extracted frame otherwise.
func (s *Service) matchSource(a *entity.Asset, kfPath string) string {
	if a.Type == "image" && (a.Format == "jpeg" || a.Format == "png") {
		return a.StoragePath
	}
	return s.Assets.Abs(kfPath)
}

// queryVectors embeds the rotated query squares of a photo.
func (s *Service) queryVectors(ctx context.Context, assetID int64, path string) ([][]float32, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	img, _, err := image.Decode(f)
	f.Close()
	if err != nil {
		return nil, err
	}
	qs := QueryImages(img)
	inputs := make([]llm.MMInput, len(qs))
	for i, q := range qs {
		j, err := EncodeJPEG(q)
		if err != nil {
			return nil, err
		}
		inputs[i] = llm.MMInput{Image: j}
	}
	res, err := s.AI.EmbedMultimodal(ctx, "visual-localisation", assetID, inputs)
	if err != nil {
		return nil, err
	}
	return res.Vectors, nil
}
