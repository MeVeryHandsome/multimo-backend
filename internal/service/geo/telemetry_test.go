package geo

import (
	"math"
	"testing"
	"time"

	"multimo-backend/internal/pkg/media"
	"multimo-backend/internal/testutil"
)

func TestDJIFlightLogAlignment(t *testing.T) {
	pts, info, err := ParseFlightLogInfo(testutil.Testdata(t, "video/dji/palos_verdes/flight_record.csv"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Aircraft != "DJI Mini 4 Pro" {
		t.Fatalf("aircraft = %q", info.Aircraft)
	}
	if len(pts) < 4000 || math.Abs(pts[0].Lat-33.7332) > 0.001 {
		t.Fatalf("%d points, first %+v", len(pts), pts[0])
	}
	local, ok := media.FilenameTime("drone_palos_verdes_20250217174636.mp4")
	start, how := AlignVideo(local, ok, 465.7, pts)
	// Pacific Standard Time is UTC-8 in February: 17:46:36 local = 01:46:36 UTC.
	if want := time.Date(2025, 2, 18, 1, 46, 36, 0, time.UTC); !start.Equal(want) || how != "file timestamp, UTC-8" {
		t.Fatalf("start %v (%s), want %v", start, how, want)
	}
	tr := Track{Start: start, Pts: pts}
	if p := tr.At(-5); p.Lat != pts[0].Lat {
		t.Fatal("time before the log must clamp to the first sample")
	}
	mid := tr.At(200)
	if haversine(mid.Lat, mid.Lon, pts[0].Lat, pts[0].Lon) > 2000 || mid.AltM <= 0 {
		t.Fatalf("mid-flight point %+v", mid)
	}
}

func TestAlignVideoWithoutStamp(t *testing.T) {
	pts := []Point{{TS: time.Unix(1000, 0)}, {TS: time.Unix(2000, 0)}}
	start, how := AlignVideo(time.Time{}, false, 60, pts)
	if !start.Equal(pts[0].TS) || how == "" {
		t.Fatalf("got %v %s", start, how)
	}
}

func TestParseTimeUnits(t *testing.T) {
	for in, want := range map[string]int64{
		"1739843207.52":        1739843207,
		"1739843207520":        1739843207,
		"1739843207520000000":  1739843207,
		"2025-02-18T01:46:47Z": 1739843207,
	} {
		ts, ok := parseTime(in)
		if !ok || ts.Unix() != want {
			t.Errorf("parseTime(%q) = %v %v", in, ts, ok)
		}
	}
}
