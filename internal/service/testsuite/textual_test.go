package testsuite

import (
	"math"
	"testing"
)

func TestScoreRankingSmallRelevantSet(t *testing.T) {
	// One relevant item at rank 7: precision counts it as a full hit
	// because min(10, |R|) = 1, while P@10 stays at 0.1.
	paths := []string{"a", "b", "c", "d", "e", "f", "x", "g", "h", "i", "j"}
	flagged := []bool{true, true, false, false, false, false, true, false, false, false, false}
	var r QueryResult
	scoreRanking(&r, paths, flagged, map[string]bool{"x": true})
	if r.Precision != 1 || r.Recall != 1 || r.P10 != 0.1 || r.P5 != 0 || r.R5 != 0 {
		t.Fatalf("got %+v", r)
	}
	if r.Flagged != 3 || math.Abs(r.SetP-0.3333) > 1e-4 || r.SetR != 1 {
		t.Fatalf("set metrics %+v", r)
	}
	if len(r.Top) != 10 || r.Top[6] != "*x" {
		t.Fatalf("top %v", r.Top)
	}
}

func TestScoreRankingLargeRelevantSet(t *testing.T) {
	// 12 relevant items: recall looks at the first 12 results, precision at
	// the first 10.
	rel := map[string]bool{}
	var paths []string
	for i := 0; i < 12; i++ {
		p := string(rune('A' + i))
		rel[p] = true
		paths = append(paths, p)
	}
	paths = append([]string{"n1", "n2"}, paths...) // two misses on top
	var r QueryResult
	scoreRanking(&r, paths, make([]bool, len(paths)), rel)
	if r.Precision != 0.8 || r.Recall != round(10.0/12, 4) || r.R10 != round(8.0/12, 4) {
		t.Fatalf("got %+v", r)
	}
	if r.Flagged != 0 || r.SetP != 0 || r.SetR != 0 {
		t.Fatalf("set metrics %+v", r)
	}
}

func TestAnswerCoords(t *testing.T) {
	cases := []struct {
		in       string
		lat, lon float64
		ok       bool
	}{
		{"拍摄位置约在北纬 29.7735°、东经 115.9690° 附近 [1]", 29.7735, 115.969, true},
		{"位于北纬33.7390度，西经118.3890度。", 33.739, -118.389, true},
		{"南纬 12.5，东经 130.25", -12.5, 130.25, true},
		{"起飞点位于北纬33.73322°、经度-118.33468°（来源：telemetry）", 33.73322, -118.33468, true},
		{"纬度 33.7, 经度 -118.3", 33.7, -118.3, true},
		{"无法确定拍摄位置", 0, 0, false},
	}
	for _, c := range cases {
		lat, lon, ok := answerCoords(c.in)
		if ok != c.ok || math.Abs(lat-c.lat) > 1e-9 || math.Abs(lon-c.lon) > 1e-9 {
			t.Errorf("%q: got %v %v %v", c.in, lat, lon, ok)
		}
	}
}
