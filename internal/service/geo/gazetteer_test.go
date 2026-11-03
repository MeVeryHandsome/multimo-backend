package geo

import (
	"testing"

	"multimo-backend/internal/testutil"
)

func TestGazetteerNaturalEarth(t *testing.T) {
	gz, err := LoadGazetteer([]string{
		testutil.Testdata(t, "docs/geo_text/ne_10m_populated_places.geojson"),
		testutil.Testdata(t, "docs/geo_text/ne_10m_airports.geojson"),
	})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][2]float64{ // name -> approx lat, lon
		"九江":                  {29.7, 116.0},
		"九江市":                 {29.7, 116.0},
		"江西省九江市":              {29.7, 116.0},
		"Beijing":             {39.9, 116.4},
		"北京":                  {39.9, 116.4},
		"Los Angeles":         {34.0, -118.2},
		"rancho palos verdes": {0, 0}, // not in a 10m populated-places set: expect a miss or a sane hit
	}
	for name, want := range cases {
		p, ok := gz.Lookup(name)
		if want == [2]float64{0, 0} {
			continue
		}
		if !ok || haversine(p.Lat, p.Lon, want[0], want[1]) > 60000 {
			t.Errorf("%s -> %+v (ok=%v)", name, p, ok)
		}
	}
	if _, ok := gz.Lookup("一个不存在的地名"); ok {
		t.Error("unknown name matched")
	}
}
