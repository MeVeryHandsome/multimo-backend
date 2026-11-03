package geo

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Point is one flight log sample.
type Point struct {
	TS   time.Time
	Lat  float64
	Lon  float64
	AltM float64
}

// ParseFlightLog reads a CSV flight record. It understands DJI exports
// (timestamps_ns in epoch seconds, OSD.latitude, OSD.longitude,
// OSD.height [ft]) and plain logs with time/lat/lon/alt columns.
func ParseFlightLog(path string) ([]Point, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	head, err := r.Read()
	if err != nil {
		return nil, err
	}
	col := func(names ...string) int {
		for _, n := range names {
			for i, h := range head {
				if strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(h, "\ufeff")), n) {
					return i
				}
			}
		}
		return -1
	}
	ti := col("timestamps_ns", "timestamp", "time", "datetime", "date")
	la := col("OSD.latitude", "latitude", "lat")
	lo := col("OSD.longitude", "longitude", "lon", "lng")
	altFt := col("OSD.height [ft]")
	altM := col("OSD.height [m]", "altitude", "alt", "height")
	if ti < 0 || la < 0 || lo < 0 {
		return nil, errors.New("flight log needs time, latitude and longitude columns")
	}
	var pts []Point
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(rec) <= max(ti, la, lo) {
			continue
		}
		ts, ok := parseTime(rec[ti])
		lat, e1 := strconv.ParseFloat(strings.TrimSpace(rec[la]), 64)
		lon, e2 := strconv.ParseFloat(strings.TrimSpace(rec[lo]), 64)
		if !ok || e1 != nil || e2 != nil || math.Abs(lat) < 0.01 && math.Abs(lon) < 0.01 || math.Abs(lat) > 90 || math.Abs(lon) > 180 {
			continue // no GPS fix yet
		}
		p := Point{TS: ts, Lat: lat, Lon: lon}
		if altFt >= 0 && altFt < len(rec) {
			if v, err := strconv.ParseFloat(rec[altFt], 64); err == nil {
				p.AltM = v * 0.3048
			}
		} else if altM >= 0 && altM < len(rec) {
			p.AltM, _ = strconv.ParseFloat(rec[altM], 64)
		}
		pts = append(pts, p)
	}
	if len(pts) == 0 {
		return nil, errors.New("flight log has no valid GPS samples")
	}
	sort.Slice(pts, func(i, j int) bool { return pts[i].TS.Before(pts[j].TS) })
	return pts, nil
}

func parseTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		switch {
		case v > 1e17: // nanoseconds
			return time.Unix(0, int64(v)).UTC(), true
		case v > 1e14: // microseconds
			return time.UnixMicro(int64(v)).UTC(), true
		case v > 1e11: // milliseconds
			return time.UnixMilli(int64(v)).UTC(), true
		case v > 1e8: // seconds (DJI "timestamps_ns" holds seconds)
			sec, frac := math.Modf(v)
			return time.Unix(int64(sec), int64(frac*1e9)).UTC(), true
		}
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.000", "2006-01-02 15:04:05", "2006/01/02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// AlignVideo finds the UTC instant of video t=0.
//
// Cameras name files by local wall-clock time with no zone. The log is UTC.
// We try every whole-hour offset and keep the one that puts the recording
// start closest to the log while still overlapping it. Without a usable
// stamp the video is assumed to start with the log.
func AlignVideo(localStart time.Time, haveStamp bool, duration float64, pts []Point) (time.Time, string) {
	first, last := pts[0].TS, pts[len(pts)-1].TS
	if !haveStamp {
		return first, "flight log start (no timestamp in file)"
	}
	best, bestGap, bestH := time.Time{}, math.MaxFloat64, 0
	for h := -12; h <= 14; h++ {
		start := localStart.Add(-time.Duration(h) * time.Hour)
		end := start.Add(time.Duration(duration * float64(time.Second)))
		if end.Before(first) || start.After(last) {
			continue // no overlap with the log
		}
		if gap := math.Abs(start.Sub(first).Seconds()); gap < bestGap {
			best, bestGap, bestH = start, gap, h
		}
	}
	if best.IsZero() {
		return first, "flight log start (file timestamp does not overlap the log)"
	}
	return best, fmt.Sprintf("file timestamp, UTC%+d", bestH)
}

// Track is a flight log on the video timeline.
type Track struct {
	Start time.Time
	Pts   []Point
}

// At interpolates the position at video time t (seconds); times outside the
// log clamp to its ends.
func (tr Track) At(t float64) Point {
	ts := tr.Start.Add(time.Duration(t * float64(time.Second)))
	p := tr.Pts
	i := sort.Search(len(p), func(i int) bool { return !p[i].TS.Before(ts) })
	switch {
	case i == 0:
		return p[0]
	case i == len(p):
		return p[len(p)-1]
	}
	a, b := p[i-1], p[i]
	span := b.TS.Sub(a.TS).Seconds()
	if span <= 0 {
		return b
	}
	f := ts.Sub(a.TS).Seconds() / span
	return Point{TS: ts, Lat: a.Lat + f*(b.Lat-a.Lat), Lon: a.Lon + f*(b.Lon-a.Lon), AltM: a.AltM + f*(b.AltM-a.AltM)}
}
