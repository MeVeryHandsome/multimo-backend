package search

import (
	"math"
	"regexp"
	"sort"
	"unicode/utf8"

	"multimo-backend/internal/service/geo"
)

// Positions estimated by visual matching are only good to a few hundred
// metres, so a hard boundary silently drops pictures taken just inside the
// area. A spatial filter therefore keeps units whose estimated position lies
// within its stored accuracy (capped at maxSpatialMargin) of the boundary.
// They are ranked after the units inside the boundary and are not marked
// relevant. Positions from telemetry or EXIF (a few metres) behave as before.
const maxSpatialMargin = 300.0

const metresPerDegLat = 110574.0

// spatialDepth is the signed distance in metres from a position to the
// boundary of the filter's area: positive inside, negative outside, NaN when
// the position is unknown.
func spatialDepth(lat, lon float64, f *Filters) float64 {
	if math.IsNaN(lat) || math.IsNaN(lon) {
		return math.NaN()
	}
	d := math.Inf(1)
	if f.Lat != nil {
		d = math.Min(d, f.RadiusM-geo.Haversine(*f.Lat, *f.Lon, lat, lon))
	}
	mLon := 111320 * math.Cos(lat*math.Pi/180)
	if f.South != nil {
		d = math.Min(d, (lat-*f.South)*metresPerDegLat)
	}
	if f.North != nil {
		d = math.Min(d, (*f.North-lat)*metresPerDegLat)
	}
	if f.West != nil {
		d = math.Min(d, (lon-*f.West)*mLon)
	}
	if f.East != nil {
		d = math.Min(d, (*f.East-lon)*mLon)
	}
	return d
}

// spatialMargin is how far outside the boundary a unit may lie.
func spatialMargin(acc float64) float64 {
	if math.IsNaN(acc) || acc <= 0 {
		return 0
	}
	return math.Min(acc, maxSpatialMargin)
}

// widen returns a copy of the filter grown by m metres on every side, used
// to prefilter in SQL before the exact per-unit check.
func (f Filters) widen(m float64) Filters {
	out := f
	if f.Lat != nil {
		out.RadiusM = f.RadiusM + m
	}
	dLat := m / metresPerDegLat
	// Longitude degrees per metre grow with latitude; use the band's most
	// poleward edge when known, otherwise assume 70 degrees (conservative).
	ref := 70.0
	if f.South != nil && f.North != nil {
		ref = math.Max(math.Abs(*f.South), math.Abs(*f.North))
	} else if f.Lat != nil {
		ref = math.Abs(*f.Lat)
	}
	dLon := m / (111320 * math.Cos(math.Min(ref+dLat, 85)*math.Pi/180))
	shift := func(p *float64, by float64) *float64 {
		if p == nil {
			return nil
		}
		v := *p + by
		return &v
	}
	out.South, out.North = shift(f.South, -dLat), shift(f.North, dLat)
	out.West, out.East = shift(f.West, -dLon), shift(f.East, dLon)
	return out
}

// reGenericWords are words that carry no content in a spatial query.
var reGenericWords = regexp.MustCompile(`为中心|以|半径|方圆|范围|以内|之内|内|附近|一带|周边|周围|拍摄|拍的|航拍|图像|图片|照片|影像|视频|画面|素材|截帧|镜头|的|在|于|有哪些|哪些|所有|全部|查找|搜索|列出|[\s\p{P}\p{S}]+`)

// pureSpatial reports whether the query asks for nothing but a place: after
// removing coordinates, radii, dates and filler words nothing is left. Such
// queries are ranked by distance instead of text similarity.
func pureSpatial(q string) bool {
	r := q
	for _, re := range []*regexp.Regexp{reLatLon, reLatLonEN, reRadius, reLatRange, reLonSide, reLatSide, reDateTime, reDay, reMonth, reYear} {
		r = re.ReplaceAllString(r, " ")
	}
	r = reGenericWords.ReplaceAllString(r, "")
	return utf8.RuneCountInString(r) <= 1
}

// spaceHits lists the keyframes that pass the spatial filter, deepest first.
func spaceHits(units map[int64]Unit, f *Filters, keep func(Unit) bool) []Hit {
	var out []Hit
	for _, u := range units {
		if keep(u) {
			out = append(out, Hit{Unit: u, Score: spatialDepth(u.Lat, u.Lon, f)})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Unit.ID < out[j].Unit.ID
	})
	return out
}
