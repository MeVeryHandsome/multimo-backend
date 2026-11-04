package testsuite

import (
	"context"
	"math"
	"path/filepath"

	"multimo-backend/internal/pkg/media"
	"multimo-backend/internal/service/geo"
)

type GeoItem struct {
	Image  string   `json:"image"`
	Source string   `json:"source"`
	ErrorM *float64 `json:"errorM"`
}

type DJICheck struct {
	Keyframes     int     `json:"keyframes"`
	FromTelemetry int     `json:"fromTelemetry"`
	MaxDeviationM float64 `json:"maxDeviationM"` // stored position vs independent interpolation
	Alignment     string  `json:"alignment"`
}

type GeoSection struct {
	Visual    Stats     `json:"visual"`
	Within100 float64   `json:"within100"`
	Within250 float64   `json:"within250"`
	Located   int       `json:"located"`
	Total     int       `json:"total"`
	Items     []GeoItem `json:"items"`
	DJI       *DJICheck `json:"dji,omitempty"`
}

type geoGT struct {
	ID        string  `json:"id"`
	Image     string  `json:"image"`
	Video     string  `json:"video"`
	Telemetry string  `json:"telemetry"`
	Lat       float64 `json:"lat"`
	Lon       float64 `json:"lon"`
}

func (s *Suite) geo(ctx context.Context) (*GeoSection, error) {
	gts, err := readJSONL[geoGT](s, "geo_gt.jsonl")
	if err != nil {
		return nil, err
	}
	assets, err := s.assetsByPath(ctx)
	if err != nil {
		return nil, err
	}
	sec := &GeoSection{}
	var errs []float64
	for _, g := range gts {
		if g.Image == "" {
			if g.Video != "" {
				sec.DJI, err = s.djiCheck(ctx, assets[g.Video], g)
				if err != nil {
					return nil, err
				}
			}
			continue
		}
		sec.Total++
		it := GeoItem{Image: filepath.Base(g.Image)}
		if a := assets[g.Image]; a != nil && a.Lat != nil {
			e := geo.Haversine(g.Lat, g.Lon, *a.Lat, *a.Lon)
			it.ErrorM, it.Source = &e, a.GeoSource
			errs = append(errs, e)
			sec.Located++
		}
		sec.Items = append(sec.Items, it)
	}
	sec.Visual = Summarise(errs)
	within := func(m float64) float64 {
		n := 0
		for _, e := range errs {
			if e < m {
				n++
			}
		}
		return ratio(n, sec.Total) // unlocated images count as misses
	}
	sec.Within100, sec.Within250 = round(within(100), 4), round(within(250), 4)
	return sec, nil
}

// djiCheck verifies that the stored keyframe positions equal an independent
// interpolation of the flight log (the log is the only ground truth here).
func (s *Suite) djiCheck(ctx context.Context, a *assetRow, g geoGT) (*DJICheck, error) {
	if a == nil {
		return nil, nil
	}
	rows, err := s.DB.GetAll(ctx, `SELECT ts_sec, lat, lon, geo_source FROM keyframes WHERE asset_id = ?`, a.ID)
	if err != nil {
		return nil, err
	}
	c := &DJICheck{Keyframes: len(rows)}
	pts, err := geo.ParseFlightLog(s.path(g.Telemetry))
	if err != nil {
		return c, nil
	}
	dur, _ := s.DB.GetValue(ctx, "SELECT duration_sec FROM assets WHERE id = ?", a.ID)
	local, ok := media.FilenameTime(a.Filename)
	start, how := geo.AlignVideo(local, ok, dur.Float64(), pts)
	c.Alignment = how
	tr := geo.Track{Start: start, Pts: pts}
	for _, r := range rows {
		if r["geo_source"].String() != "telemetry" || r["lat"].IsNil() {
			continue
		}
		c.FromTelemetry++
		p := tr.At(r["ts_sec"].Float64())
		c.MaxDeviationM = math.Max(c.MaxDeviationM, geo.Haversine(p.Lat, p.Lon, r["lat"].Float64(), r["lon"].Float64()))
	}
	c.MaxDeviationM = round(c.MaxDeviationM, 2)
	return c, nil
}
