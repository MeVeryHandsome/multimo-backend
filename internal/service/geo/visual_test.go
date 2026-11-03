package geo

import (
	"encoding/json"
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/gogf/gf/v2/frame/g"

	"multimo-backend/internal/model/entity"
	"multimo-backend/internal/pkg/geotiff"
	"multimo-backend/internal/testutil"
)

func TestRotateReflectKeepsContentAndFillsCorners(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			v := uint8(0)
			if x < 32 {
				v = 255
			}
			src.Set(x, y, color.RGBA{v, v, v, 255})
		}
	}
	r180 := rotateReflect(src, 180)
	if r180.RGBAAt(5, 30).R > 10 || r180.RGBAAt(58, 30).R < 245 {
		t.Fatal("180 degree rotation did not swap halves")
	}
	r45 := rotateReflect(src, 45)
	for _, p := range [][2]int{{0, 0}, {63, 0}, {0, 63}, {63, 63}} {
		if r45.RGBAAt(p[0], p[1]).A != 255 {
			t.Fatal("corner left transparent")
		}
	}
	if len(QueryImages(src)) != queryRotations {
		t.Fatal("wrong number of query rotations")
	}
}

func TestTilesOnVisLocBasemap(t *testing.T) {
	f, err := geotiff.Open(testutil.Testdata(t, "geo/uav_visloc/01/satellite01.tif"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b, _ := f.LonLatBounds()
	centres := TileCentres(b, 300)
	if len(centres) < 600 || len(centres) > 900 {
		t.Fatalf("%d tile centres for 300 m tiles (Python prototype had 765)", len(centres))
	}
	img, err := TileImage(f, 29.76096029, 115.9747973, 300)
	if err != nil || img == nil || img.Bounds().Dx() != querySide {
		t.Fatalf("tile at 01_0001: %v %v", img, err)
	}
	if out, _ := TileImage(f, b.North, b.West, 300); out != nil {
		t.Fatal("tile overlapping the raster edge should be skipped")
	}
}

func TestRankTilesMergesScalesAndDedups(t *testing.T) {
	q := [][]float32{{1, 0}, {0, 1}}
	tiles := []Tile{
		{Lat: 30, Lon: 116, Scale: 300, Emb: []float32{0.9, 0.1}},
		{Lat: 30.0001, Lon: 116, Scale: 250, Emb: []float32{0.95, 0}}, // 11 m away: duplicate
		{Lat: 30.01, Lon: 116, Scale: 300, Emb: []float32{0.1, 0.8}},  // matched by the 2nd rotation
		{Lat: 30.02, Lon: 116, Scale: 400, Emb: []float32{0.2, 0.2}},
	}
	got := RankTiles(q, tiles)
	if len(got) != 3 || got[0].Scale != 250 || math.Abs(got[1].Score-0.8) > 1e-6 || got[2].Scale != 400 {
		t.Fatalf("got %+v", got)
	}
}

func TestSmoothTrackRejectsImpossibleJump(t *testing.T) {
	// Three photos 10 s apart. The middle one's best candidate is 5 km away;
	// its second candidate is on the route.
	at := func(lat float64, s float64) Candidate { return Candidate{Lat: lat, Lon: 116, Score: s} }
	cands := [][]Candidate{
		{at(30.000, 0.9)},
		{at(30.045, 0.95), at(30.001, 0.85)},
		{at(30.002, 0.9)},
	}
	path := SmoothTrack([]float64{0, 10, 20}, cands, 25)
	if path[1] != 1 {
		t.Fatalf("path = %v, expected the on-route candidate", path)
	}
}

func TestIsBasemapWithJSONNumbers(t *testing.T) {
	// Meta read back from jsonb carries json.Number values.
	a := &entity.Asset{Type: "remote_sensing", Meta: g.Map{"image": map[string]any{"geo": map[string]any{"bounds": map[string]any{
		"west": json.Number("115.970635"), "east": json.Number("115.996851"),
		"south": json.Number("29.702283"), "north": json.Number("29.774065")}}}}}
	if !IsBasemap(a) {
		t.Fatal("UAV-VisLoc satellite raster not recognised as a basemap")
	}
	a.Type = "image"
	if IsBasemap(a) {
		t.Fatal("non-raster recognised as basemap")
	}
}
