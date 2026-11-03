package search

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Filters are hard constraints extracted from a query or given explicitly.
type Filters struct {
	Lat     *float64 `json:"lat,omitempty"`
	Lon     *float64 `json:"lon,omitempty"`
	RadiusM float64  `json:"radiusM,omitempty"`
	// Box limits; nil sides are open.
	South *float64   `json:"south,omitempty"`
	North *float64   `json:"north,omitempty"`
	West  *float64   `json:"west,omitempty"`
	East  *float64   `json:"east,omitempty"`
	From  *time.Time `json:"from,omitempty"`
	To    *time.Time `json:"to,omitempty"`
	Place string     `json:"place,omitempty"`
	// Source names which parser produced each filter, for transparency.
	Source map[string]string `json:"source,omitempty"`
}

// Spatial reports whether any location constraint is set.
func (f *Filters) Spatial() bool {
	return f.Lat != nil || f.South != nil || f.North != nil || f.West != nil || f.East != nil
}

// Temporal reports whether a time constraint is set.
func (f *Filters) Temporal() bool { return f.From != nil || f.To != nil }

func (f *Filters) note(key, src string) {
	if f.Source == nil {
		f.Source = map[string]string{}
	}
	f.Source[key] = src
}

const num = `(\d+(?:\.\d+)?)`

var (
	reLatLon   = regexp.MustCompile(`(北纬|南纬)\s*` + num + `\s*°?\s*[，,、/\s]*\s*(东经|西经)\s*` + num + `\s*°?`)
	reLatLonEN = regexp.MustCompile(`(?i)` + num + `\s*°?\s*([NS])[\s,，]+` + num + `\s*°?\s*([EW])\b`)
	reRadius   = regexp.MustCompile(`(?:半径|方圆)\s*` + num + `\s*(米|m|公里|千米|km)|` + num + `\s*(米|m|公里|千米|km)\s*(?:以内|之内|范围内|内)`)
	reLatRange = regexp.MustCompile(`纬度\s*(?:在|介于)?\s*(?:北纬)?\s*` + num + `\s*°?\s*(?:到|至|和|-|~|—)\s*(?:北纬)?\s*` + num + `\s*°?`)
	reLonSide  = regexp.MustCompile(`(东经|西经)\s*` + num + `\s*°?\s*(以东|以西)`)
	reLatSide  = regexp.MustCompile(`(北纬|南纬)\s*` + num + `\s*°?\s*(以北|以南)`)
	reDateTime = regexp.MustCompile(`(\d{4})\s*年\s*(\d{1,2})\s*月\s*(\d{1,2})\s*日?\s*(\d{1,2})[:：](\d{2})\s*(?:到|至|-|~|—)\s*(\d{1,2})[:：](\d{2})`)
	reDay      = regexp.MustCompile(`(\d{4})\s*年\s*(\d{1,2})\s*月\s*(\d{1,2})\s*日`)
	reMonth    = regexp.MustCompile(`(\d{4})\s*年\s*(\d{1,2})\s*月`)
	reYear     = regexp.MustCompile(`(\d{4})\s*年`)
	reNear     = regexp.MustCompile(`附近|一带|周边|周围`)
)

// ParseFilters extracts explicit coordinates, radii, latitude/longitude
// bounds and dates. Times without a zone are read as UTC, which is how
// capture times without a zone are stored.
func ParseFilters(q string) Filters {
	var f Filters
	if m := reLatLon.FindStringSubmatch(q); m != nil {
		lat, _ := strconv.ParseFloat(m[2], 64)
		lon, _ := strconv.ParseFloat(m[4], 64)
		if m[1] == "南纬" {
			lat = -lat
		}
		if m[3] == "西经" {
			lon = -lon
		}
		f.Lat, f.Lon = &lat, &lon
		f.note("point", "regex")
	} else if m := reLatLonEN.FindStringSubmatch(q); m != nil {
		lat, _ := strconv.ParseFloat(m[1], 64)
		lon, _ := strconv.ParseFloat(m[3], 64)
		if strings.EqualFold(m[2], "S") {
			lat = -lat
		}
		if strings.EqualFold(m[4], "W") {
			lon = -lon
		}
		f.Lat, f.Lon = &lat, &lon
		f.note("point", "regex")
	}
	if f.Lat != nil {
		if m := reRadius.FindStringSubmatch(q); m != nil {
			v, unit := m[1], m[2]
			if v == "" {
				v, unit = m[3], m[4]
			}
			r, _ := strconv.ParseFloat(v, 64)
			if unit == "公里" || unit == "千米" || strings.EqualFold(unit, "km") {
				r *= 1000
			}
			f.RadiusM = r
			f.note("radius", "regex")
		} else if reNear.MatchString(q) {
			f.RadiusM = 5000
			f.note("radius", "default for 附近")
		} else {
			f.RadiusM = 1000
			f.note("radius", "default")
		}
	}
	if m := reLatRange.FindStringSubmatch(q); m != nil {
		a, _ := strconv.ParseFloat(m[1], 64)
		b, _ := strconv.ParseFloat(m[2], 64)
		lo, hi := min(a, b), max(a, b)
		f.South, f.North = &lo, &hi
		f.note("latRange", "regex")
	}
	if m := reLonSide.FindStringSubmatch(q); m != nil {
		v, _ := strconv.ParseFloat(m[2], 64)
		if m[1] == "西经" {
			v = -v
		}
		if m[3] == "以东" {
			f.West = &v
		} else {
			f.East = &v
		}
		f.note("lonBound", "regex")
	}
	if m := reLatSide.FindStringSubmatch(q); m != nil {
		v, _ := strconv.ParseFloat(m[2], 64)
		if m[1] == "南纬" {
			v = -v
		}
		if m[3] == "以北" {
			f.South = &v
		} else {
			f.North = &v
		}
		f.note("latBound", "regex")
	}

	atoi := func(s string) int { n, _ := strconv.Atoi(s); return n }
	switch {
	case reDateTime.MatchString(q):
		m := reDateTime.FindStringSubmatch(q)
		day := time.Date(atoi(m[1]), time.Month(atoi(m[2])), atoi(m[3]), 0, 0, 0, 0, time.UTC)
		from := day.Add(time.Duration(atoi(m[4]))*time.Hour + time.Duration(atoi(m[5]))*time.Minute)
		to := day.Add(time.Duration(atoi(m[6]))*time.Hour + time.Duration(atoi(m[7]))*time.Minute)
		f.From, f.To = &from, &to
	case reDay.MatchString(q):
		m := reDay.FindStringSubmatch(q)
		from := time.Date(atoi(m[1]), time.Month(atoi(m[2])), atoi(m[3]), 0, 0, 0, 0, time.UTC)
		to := from.AddDate(0, 0, 1)
		f.From, f.To = &from, &to
	case reMonth.MatchString(q):
		m := reMonth.FindStringSubmatch(q)
		from := time.Date(atoi(m[1]), time.Month(atoi(m[2])), 1, 0, 0, 0, 0, time.UTC)
		to := from.AddDate(0, 1, 0)
		f.From, f.To = &from, &to
	case reYear.MatchString(q) && strings.Contains(q, "拍"):
		// Only "2018年拍摄的…": a bare year usually names a regulation.
		m := reYear.FindStringSubmatch(q)
		from := time.Date(atoi(m[1]), 1, 1, 0, 0, 0, 0, time.UTC)
		to := from.AddDate(1, 0, 0)
		f.From, f.To = &from, &to
	}
	if f.Temporal() {
		f.note("time", "regex")
	}
	return f
}

var rePlaceCue = regexp.MustCompile(`附近|一带|周边|地区|区域|城市|省|市|州|县|国|位于|拍摄于|在.{1,16}(拍|的)|[A-Z][a-z]+ [A-Z][a-z]+`)

var reVisualCue = regexp.MustCompile(`拍|航拍|视频|图像|影像|画面|照片|截帧|镜头`)

// MayNamePlace is a cheap test for whether asking the language model for a
// capture-location filter is worthwhile: the query must mention a place and
// be about pictures. "美国小型无人机的高度限制" names a jurisdiction, not
// where something was filmed.
func MayNamePlace(q string) bool { return rePlaceCue.MatchString(q) && reVisualCue.MatchString(q) }
