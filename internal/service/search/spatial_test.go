package search

import (
	"math"
	"testing"
)

func TestSpatialDepth(t *testing.T) {
	lat, lon := 29.73, 115.98
	f := Filters{Lat: &lat, Lon: &lon, RadiusM: 300}
	if d := spatialDepth(lat, lon, &f); math.Abs(d-300) > 1e-6 {
		t.Fatalf("centre depth %v", d)
	}
	// 0.0045 deg of latitude is ~498 m: outside by ~198 m.
	if d := spatialDepth(lat+0.0045, lon, &f); d > -190 || d < -205 {
		t.Fatalf("outside depth %v", d)
	}
	south, east := 29.72, 115.99
	b := Filters{South: &south, East: &east}
	if d := spatialDepth(29.721, 115.98, &b); d < 100 || d > 115 {
		t.Fatalf("band depth %v", d) // limited by the southern edge, ~110 m
	}
	if !math.IsNaN(spatialDepth(math.NaN(), lon, &b)) {
		t.Fatal("unknown position should give NaN")
	}
}

func TestPassesWithMargin(t *testing.T) {
	lat, lon := 29.73, 115.98
	f := Filters{Lat: &lat, Lon: &lon, RadiusM: 300}
	u := Unit{Lat: lat + 0.0045, Lon: lon} // ~200 m outside
	if passes(u, &f) {
		t.Fatal("exact position (no accuracy) must not pass")
	}
	u.Acc = 250
	if !passes(u, &f) {
		t.Fatal("within its accuracy margin should pass")
	}
	u.Acc = 10000 // capped at maxSpatialMargin
	u.Lat = lat + 0.01
	if passes(u, &f) {
		t.Fatal("margin must be capped")
	}
}

func TestWidenCoversMargin(t *testing.T) {
	west := 115.9893
	f := Filters{West: &west}
	w := f.widen(maxSpatialMargin)
	// A point 300 m west of the line at 29.7 N must be inside the widened box.
	lon := west - 300/(111320*math.Cos(29.7*math.Pi/180))
	if lon < *w.West {
		t.Fatalf("widened west %v does not cover %v", *w.West, lon)
	}
}

func TestPureSpatial(t *testing.T) {
	for q, want := range map[string]bool{
		"以北纬29.7253°、东经115.9748°为中心，半径300米内拍摄的航拍图像": true,
		"北纬33.73°、西经118.33°附近 2 公里内拍摄的视频":           true,
		"北纬29.73°、东经115.98° 500米内的停车场":              false,
		"美国加利福尼亚州帕洛斯弗迪斯（Palos Verdes）一带拍摄的视频":       false,
	} {
		if got := pureSpatial(q); got != want {
			t.Errorf("%s: got %v", q, got)
		}
	}
}
