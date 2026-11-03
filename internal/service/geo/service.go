// Package geo places assets and keyframes on the map:
//
//  1. GPS from metadata: flight log sidecar (per keyframe), EXIF, container tags
//  2. otherwise a coarse position from landmarks/place names the VLM read,
//     geocoded with the GeoJSON gazetteer
//  3. then, if a georeferenced basemap covers the area (or the asset names
//     one), visual matching against basemap tiles, smoothed over time-ordered
//     photos of the same collection
package geo

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/frame/g"
	"github.com/gogf/gf/v2/os/gtime"
	"github.com/gogf/gf/v2/util/gconv"

	"multimo-backend/internal/config"
	"multimo-backend/internal/model/entity"
	"multimo-backend/internal/pkg/geotiff"
	"multimo-backend/internal/pkg/llm"
	"multimo-backend/internal/pkg/media"
	"multimo-backend/internal/service/asset"
	"multimo-backend/internal/service/keyframe"
	"multimo-backend/internal/service/pipeline"
)

const (
	Kind        = "geo"
	BasemapKind = "basemap"

	gpsAccuracyM      = 5     // consumer GNSS
	landmarkAccuracyM = 10000 // city-level gazetteer hit
	minPlaceConf      = 0.5   // VLM place guesses below this are ignored
	minBasemapSideM   = 1000  // smaller georeferenced rasters are not basemaps
)

type Service struct {
	Assets     *asset.Service
	DB         gdb.DB
	Dispatcher *pipeline.Dispatcher
	AI         *llm.Gateway
	Geo        config.Geo

	mu    sync.Mutex
	tiles map[int64][]Tile // basemap asset id -> tiles
	gaz   *Gazetteer
	gazAt time.Time
}

// ---------------------------------------------------------------- helpers

// Bounds returns the lon/lat extent recorded at ingest for a georeferenced raster.
func Bounds(a *entity.Asset) (geotiff.Bounds, bool) {
	img, _ := a.Meta["image"].(map[string]any)
	gm, _ := img["geo"].(map[string]any)
	bm, _ := gm["bounds"].(map[string]any)
	if bm == nil {
		return geotiff.Bounds{}, false
	}
	// jsonb numbers arrive as json.Number through GoFrame; gconv handles both.
	f := func(k string) float64 { return gconv.Float64(bm[k]) }
	return geotiff.Bounds{West: f("west"), South: f("south"), East: f("east"), North: f("north")}, true
}

// IsBasemap reports whether an asset is a georeferenced raster big enough to
// match photos against.
func IsBasemap(a *entity.Asset) bool {
	b, ok := Bounds(a)
	if a.Type != "remote_sensing" || !ok {
		return false
	}
	return haversine(b.South, b.West, b.South, b.East) >= minBasemapSideM &&
		haversine(b.South, b.West, b.North, b.West) >= minBasemapSideM
}

func provided(a *entity.Asset, key string) string {
	p, _ := a.Meta["provided"].(map[string]any)
	v, _ := p[key].(string)
	return v
}

// ---------------------------------------------------------------- geo task

func (s *Service) Handle(ctx context.Context, t *pipeline.Task, report pipeline.Reporter) error {
	a, err := s.Assets.Get(ctx, t.AssetID)
	if err != nil {
		return pipeline.Permanent(err)
	}
	switch {
	case a.Type == "document" || IsBasemap(a) || a.GeoSource == "geotiff":
		return nil // documents have no position; georeferenced rasters already have an exact one
	case a.Type == "video" && a.SidecarPath != "":
		return s.fromTelemetry(ctx, a)
	case a.Lat != nil && (a.GeoSource == "exif" || a.GeoSource == "metadata"):
		_, err := s.DB.Exec(ctx, `UPDATE keyframes SET lat = ?, lon = ?, geo_source = ?, geo_accuracy_m = ?
			WHERE asset_id = ? AND lat IS NULL`, *a.Lat, *a.Lon, a.GeoSource, gpsAccuracyM, a.Id)
		return err
	}
	if err := s.fromLandmarks(ctx, a); err != nil {
		g.Log().Warningf(ctx, "asset %d landmark geocoding: %v", a.Id, err)
	}
	a, _ = s.Assets.Get(ctx, a.Id)
	return s.visual(ctx, a, report)
}

// fromTelemetry aligns the flight log with the video and positions every keyframe.
func (s *Service) fromTelemetry(ctx context.Context, a *entity.Asset) error {
	pts, err := ParseFlightLog(a.SidecarPath)
	if err != nil {
		return pipeline.Permanent(fmt.Errorf("flight log: %w", err))
	}
	dur := 0.0
	if a.DurationSec != nil {
		dur = *a.DurationSec
	}
	local, ok := media.FilenameTime(a.Filename)
	if probe, _ := a.Meta["probe"].(map[string]any); probe != nil {
		if ct, _ := probe["creationTime"].(string); ct != "" {
			if t, err := time.Parse(time.RFC3339Nano, ct); err == nil {
				local, ok = t, true // container time is already UTC: offset 0 wins
			}
		}
	}
	start, how := AlignVideo(local, ok, dur, pts)
	tr := Track{Start: start, Pts: pts}

	return s.DB.Transaction(ctx, func(ctx context.Context, tx gdb.TX) error {
		if _, err := tx.Model("telemetry_points").Where("asset_id", a.Id).Delete(); err != nil {
			return err
		}
		rows := g.List{}
		for _, p := range pts {
			ts := p.TS.Sub(start).Seconds()
			if ts < -1 || (dur > 0 && ts > dur+1) {
				continue
			}
			rows = append(rows, g.Map{"asset_id": a.Id, "t_sec": ts, "ts": p.TS, "lat": p.Lat, "lon": p.Lon, "alt_m": p.AltM})
		}
		if len(rows) > 0 {
			if _, err := tx.Model("telemetry_points").Data(rows).Batch(500).Insert(); err != nil {
				return err
			}
		}
		var kfs []struct {
			Id    int64   `orm:"id"`
			TsSec float64 `orm:"ts_sec"`
		}
		if err := tx.Model("keyframes").Where("asset_id", a.Id).Scan(&kfs); err != nil {
			return err
		}
		for _, k := range kfs {
			p := tr.At(k.TsSec)
			if _, err := tx.Exec(`UPDATE keyframes SET lat = ?, lon = ?, geo_source = 'telemetry', geo_accuracy_m = ?,
				captured_at = ? WHERE id = ?`, p.Lat, p.Lon, gpsAccuracyM, start.Add(time.Duration(k.TsSec*float64(time.Second))), k.Id); err != nil {
				return err
			}
		}
		p0 := tr.At(0)
		meta := cloneMeta(a.Meta)
		meta["capturedAtSource"] = "flight log aligned by " + how
		maxAlt := 0.0
		for _, p := range pts {
			maxAlt = max(maxAlt, p.AltM)
		}
		meta["telemetry"] = g.Map{"points": len(pts), "start": start.Format(time.RFC3339), "alignment": how,
			"maxAltitudeM": maxAlt, "end": pts[len(pts)-1].TS.Format(time.RFC3339)}
		mj, _ := json.Marshal(meta)
		_, err := tx.Exec(`UPDATE assets SET lat = ?, lon = ?, geo_source = 'telemetry', geo_accuracy_m = ?,
			captured_at = ?, meta = ?::jsonb, updated_at = now() WHERE id = ?`, p0.Lat, p0.Lon, gpsAccuracyM, start, string(mj), a.Id)
		return err
	})
}

// fromLandmarks geocodes the most confident place name the VLM read from the
// keyframes (signs, landmarks). It only sets a coarse, city-level position.
func (s *Service) fromLandmarks(ctx context.Context, a *entity.Asset) error {
	rows, err := s.DB.GetAll(ctx, `SELECT meta->'analysis'->'place_guess' AS pg FROM keyframes WHERE asset_id = ?`, a.Id)
	if err != nil {
		return err
	}
	best, bestConf := "", 0.0
	for _, r := range rows {
		var pg struct {
			Name       string  `json:"name"`
			Confidence float64 `json:"confidence"`
		}
		if json.Unmarshal([]byte(r["pg"].String()), &pg) == nil && pg.Name != "" && pg.Confidence > bestConf {
			best, bestConf = pg.Name, pg.Confidence
		}
	}
	if best == "" || bestConf < minPlaceConf {
		return nil
	}
	gaz, err := s.gazetteer(ctx)
	if err != nil || gaz == nil {
		return err
	}
	pl, ok := gaz.Lookup(best)
	if !ok {
		return nil
	}
	meta := cloneMeta(a.Meta)
	meta["landmarkGeocode"] = g.Map{"query": best, "confidence": bestConf, "match": pl}
	mj, _ := json.Marshal(meta)
	_, err = s.DB.Exec(ctx, `UPDATE assets SET lat = ?, lon = ?, geo_source = 'landmark', geo_accuracy_m = ?,
		meta = ?::jsonb, updated_at = now() WHERE id = ?`, pl.Lat, pl.Lon, landmarkAccuracyM, string(mj), a.Id)
	if err == nil {
		_, err = s.DB.Exec(ctx, `UPDATE keyframes SET lat = ?, lon = ?, geo_source = 'landmark', geo_accuracy_m = ?
			WHERE asset_id = ? AND lat IS NULL`, pl.Lat, pl.Lon, landmarkAccuracyM, a.Id)
	}
	return err
}

// gazetteer loads place names from every ingested GeoJSON document (cached
// for five minutes).
func (s *Service) gazetteer(ctx context.Context) (*Gazetteer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gaz != nil && time.Since(s.gazAt) < 5*time.Minute {
		return s.gaz, nil
	}
	vals, err := s.DB.Model("assets").Ctx(ctx).Where("format", "geojson").Where("status", "ready").Array("storage_path")
	if err != nil {
		return nil, err
	}
	paths := make([]string, len(vals))
	for i, v := range vals {
		paths[i] = v.String()
	}
	gz, err := LoadGazetteer(paths)
	if err != nil {
		return nil, err
	}
	s.gaz, s.gazAt = gz, time.Now()
	return gz, nil
}

// ---------------------------------------------------------------- visual matching

type kfGeo struct {
	Id         int64       `orm:"id"`
	AssetId    int64       `orm:"asset_id"`
	TsSec      float64     `orm:"ts_sec"`
	Path       string      `orm:"path"`
	CapturedAt *gtime.Time `orm:"captured_at"`
	Cands      string      `orm:"geo_candidates"`
}

func (s *Service) visual(ctx context.Context, a *entity.Asset, report pipeline.Reporter) error {
	if !s.AI.VisionEnabled() {
		return nil
	}
	maps, reason, err := s.basemapsFor(ctx, a)
	if err != nil || len(maps) == 0 {
		return err
	}
	if reason != "" { // a basemap is still being indexed
		return pipeline.Defer(30*time.Second, reason)
	}
	var tiles []Tile
	for _, m := range maps {
		ts, err := s.loadTiles(ctx, m)
		if err != nil {
			return err
		}
		tiles = append(tiles, ts...)
	}
	if len(tiles) == 0 {
		return nil
	}

	var kfs []*kfGeo
	if err := s.DB.Model("keyframes").Ctx(ctx).Where("asset_id", a.Id).OrderAsc("idx").Scan(&kfs); err != nil {
		return err
	}
	for i, k := range kfs {
		cands, err := s.candidates(ctx, a, k, tiles)
		if err != nil {
			return err
		}
		if len(cands) == 0 {
			continue
		}
		cj, _ := json.Marshal(cands)
		k.Cands = string(cj)
		top := cands[0]
		if _, err := s.DB.Exec(ctx, `UPDATE keyframes SET geo_candidates = ?::jsonb, lat = ?, lon = ?, geo_source = 'visual',
			geo_accuracy_m = ? WHERE id = ?`, string(cj), top.Lat, top.Lon, float64(top.Scale)/2, k.Id); err != nil {
			return err
		}
		report(float64(i+1)/float64(len(kfs)), fmt.Sprintf("matched %d/%d keyframes against basemap", i+1, len(kfs)))
	}
	if a.Type == "video" && len(kfs) > 1 {
		return s.smooth(ctx, kfs, true)
	}
	if err := s.setAssetFromKeyframe(ctx, a.Id, "visual"); err != nil {
		return err
	}
	return s.smoothCollectionIfDone(ctx, a)
}

// basemapsFor picks the basemaps to match against: the collection named in
// the asset's registration metadata (geo_reference), or basemaps covering
// its coarse position. A non-empty reason means one is not indexed yet.
func (s *Service) basemapsFor(ctx context.Context, a *entity.Asset) ([]*entity.Asset, string, error) {
	var all []*entity.Asset
	if err := s.DB.Model("assets").Ctx(ctx).Where("type", "remote_sensing").Where("format", "tiff").Scan(&all); err != nil {
		return nil, "", err
	}
	ref := provided(a, "geo_reference")
	var picked []*entity.Asset
	for _, m := range all {
		if !IsBasemap(m) {
			continue
		}
		b, _ := Bounds(m)
		switch {
		case ref != "" && m.Collection == ref:
			picked = append(picked, m)
		case ref == "" && a.Lat != nil && *a.Lat >= b.South-0.1 && *a.Lat <= b.North+0.1 &&
			*a.Lon >= b.West-0.1 && *a.Lon <= b.East+0.1:
			picked = append(picked, m)
		}
	}
	for _, m := range picked {
		v, err := s.DB.GetValue(ctx, `SELECT status FROM tasks WHERE asset_id = ? AND kind = ?`, m.Id, BasemapKind)
		if err != nil {
			return nil, "", err
		}
		if st := v.String(); st != "done" && st != "failed" {
			return picked, fmt.Sprintf("basemap %d (%s) not indexed yet", m.Id, m.Filename), nil
		}
	}
	return picked, "", nil
}

func (s *Service) loadTiles(ctx context.Context, m *entity.Asset) ([]Tile, error) {
	s.mu.Lock()
	if s.tiles == nil {
		s.tiles = map[int64][]Tile{}
	}
	cached, ok := s.tiles[m.Id]
	s.mu.Unlock()
	if ok {
		return cached, nil
	}
	rows, err := s.DB.GetAll(ctx, `SELECT lat, lon, scale_m, embedding::text AS emb FROM basemap_tiles WHERE asset_id = ?`, m.Id)
	if err != nil {
		return nil, err
	}
	tiles := make([]Tile, 0, len(rows))
	for _, r := range rows {
		tiles = append(tiles, Tile{Lat: r["lat"].Float64(), Lon: r["lon"].Float64(), Scale: r["scale_m"].Int(),
			Emb: parseVector(r["emb"].String())})
	}
	s.mu.Lock()
	s.tiles[m.Id] = tiles
	s.mu.Unlock()
	return tiles, nil
}

func (s *Service) candidates(ctx context.Context, a *entity.Asset, k *kfGeo, tiles []Tile) ([]Candidate, error) {
	// Photos are matched from the original file (full resolution, no
	// re-encoding); video keyframes from the extracted frame.
	src := s.Assets.Abs(k.Path)
	if a.Type == "image" && (a.Format == "jpeg" || a.Format == "png") {
		src = a.StoragePath
	}
	f, err := os.Open(src)
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
	res, err := s.AI.EmbedMultimodal(ctx, "visual-localisation", a.Id, inputs)
	if err != nil {
		g.Log().Warningf(ctx, "keyframe %d: query embeddings failed, skipping visual match: %v", k.Id, err)
		return nil, nil
	}
	return RankTiles(res.Vectors, tiles), nil
}

func (s *Service) setAssetFromKeyframe(ctx context.Context, assetID int64, source string) error {
	_, err := s.DB.Exec(ctx, `UPDATE assets a SET lat = k.lat, lon = k.lon, geo_source = ?, geo_accuracy_m = k.geo_accuracy_m,
		updated_at = now() FROM keyframes k WHERE k.asset_id = a.id AND k.idx = 0 AND a.id = ? AND k.lat IS NOT NULL`, source, assetID)
	return err
}

// smoothCollectionIfDone runs track smoothing over all visually matched
// photos of the collection once none of them is still waiting for its geo task.
func (s *Service) smoothCollectionIfDone(ctx context.Context, a *entity.Asset) error {
	n, err := s.DB.GetValue(ctx, `SELECT count(*) FROM tasks t JOIN assets x ON x.id = t.asset_id
		WHERE x.collection = ? AND x.id <> ? AND t.kind IN ('ingest', 'keyframes', 'analyze', 'geo')
		AND t.status IN ('pending', 'running')`, a.Collection, a.Id)
	if err != nil || n.Int() > 0 {
		return err
	}
	var kfs []*kfGeo
	if err := s.DB.Model("keyframes k").Ctx(ctx).Fields("k.id, k.asset_id, k.ts_sec, k.path, k.captured_at, k.geo_candidates").
		InnerJoin("assets x", "x.id = k.asset_id").
		Where("x.collection", a.Collection).Where("k.idx", 0).WhereNotNull("k.geo_candidates").WhereNotNull("k.captured_at").
		OrderAsc("k.captured_at").Scan(&kfs); err != nil {
		return err
	}
	if len(kfs) < 2 {
		return nil
	}
	return s.smooth(ctx, kfs, false)
}

// smooth applies SmoothTrack to time-ordered keyframes and writes the result.
func (s *Service) smooth(ctx context.Context, kfs []*kfGeo, video bool) error {
	times := make([]float64, len(kfs))
	cands := make([][]Candidate, len(kfs))
	for i, k := range kfs {
		if video || k.CapturedAt == nil {
			times[i] = k.TsSec
		} else {
			times[i] = float64(k.CapturedAt.Time.UnixMilli()) / 1000
		}
		json.Unmarshal([]byte(k.Cands), &cands[i]) //nolint:errcheck
		if len(cands[i]) == 0 {
			return nil
		}
	}
	path := SmoothTrack(times, cands, s.Geo.MaxSpeedMPS)
	touched := map[int64]bool{}
	for i, k := range kfs {
		c := cands[i][path[i]]
		if _, err := s.DB.Exec(ctx, `UPDATE keyframes SET lat = ?, lon = ?, geo_source = 'visual-track', geo_accuracy_m = ?
			WHERE id = ?`, c.Lat, c.Lon, float64(c.Scale)/2, k.Id); err != nil {
			return err
		}
		touched[k.AssetId] = true
	}
	for id := range touched {
		if err := s.setAssetFromKeyframe(ctx, id, "visual-track"); err != nil {
			return err
		}
	}
	g.Log().Infof(ctx, "track smoothing over %d keyframes (%d assets)", len(kfs), len(touched))
	return nil
}

// ---------------------------------------------------------------- basemap indexing

// HandleBasemap cuts a georeferenced raster into square tiles at each
// configured scale and stores their image embeddings.
func (s *Service) HandleBasemap(ctx context.Context, t *pipeline.Task, report pipeline.Reporter) error {
	a, err := s.Assets.Get(ctx, t.AssetID)
	if err != nil {
		return pipeline.Permanent(err)
	}
	if !IsBasemap(a) || !s.AI.VisionEnabled() {
		return nil
	}
	f, err := geotiff.Open(a.StoragePath)
	if err != nil {
		return pipeline.Permanent(err)
	}
	defer f.Close()
	b, _ := f.LonLatBounds()

	type job struct {
		lat, lon float64
		scale    int
	}
	var jobs []job
	for _, sc := range s.Geo.TileScales {
		for _, c := range TileCentres(b, float64(sc)) {
			jobs = append(jobs, job{c[0], c[1], sc})
		}
	}
	if _, err := s.DB.Model("basemap_tiles").Ctx(ctx).Where("asset_id", a.Id).Delete(); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.tiles, a.Id)
	s.mu.Unlock()

	const chunk = 4
	var fmu sync.Mutex // the GeoTIFF reader is not safe for concurrent use
	var mu sync.Mutex
	var firstErr error
	done := 0
	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup
	for start := 0; start < len(jobs); start += chunk {
		part := jobs[start:min(start+chunk, len(jobs))]
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			inputs := make([]llm.MMInput, 0, len(part))
			kept := make([]job, 0, len(part))
			for _, j := range part {
				fmu.Lock()
				img, err := TileImage(f, j.lat, j.lon, float64(j.scale))
				fmu.Unlock()
				if err != nil || img == nil {
					continue
				}
				enc, err := EncodeJPEG(img)
				if err != nil {
					continue
				}
				inputs = append(inputs, llm.MMInput{Image: enc})
				kept = append(kept, j)
			}
			if len(inputs) == 0 {
				return
			}
			res, err := s.AI.EmbedMultimodal(ctx, "basemap-tiles", a.Id, inputs)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			rows := make(g.List, len(kept))
			for i, j := range kept {
				rows[i] = g.Map{"asset_id": a.Id, "scale_m": j.scale, "lat": j.lat, "lon": j.lon,
					"embedding": keyframe.VectorLiteral(res.Vectors[i]), "embedding_model": res.Model}
			}
			if _, err := s.DB.Model("basemap_tiles").Ctx(ctx).Data(rows).Insert(); err != nil && firstErr == nil {
				firstErr = err
			}
			done += len(part)
			report(float64(done)/float64(len(jobs)), fmt.Sprintf("indexed %d/%d basemap tiles", done, len(jobs)))
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	g.Log().Infof(ctx, "basemap %d: %d tiles at scales %v", a.Id, len(jobs), s.Geo.TileScales)
	return nil
}

// ---------------------------------------------------------------- misc

func parseVector(s string) []float32 {
	s = strings.Trim(s, "[]")
	parts := strings.Split(s, ",")
	v := make([]float32, len(parts))
	for i, p := range parts {
		f, _ := strconv.ParseFloat(strings.TrimSpace(p), 32)
		v[i] = float32(f)
	}
	return v
}

func cloneMeta(m g.Map) g.Map {
	out := g.Map{}
	for k, v := range m {
		out[k] = v
	}
	return out
}
