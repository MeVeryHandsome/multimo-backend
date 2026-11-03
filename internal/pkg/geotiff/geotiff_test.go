package geotiff

import (
	"image"
	"math"
	"testing"
	"time"

	"multimo-backend/internal/testutil"
)

// UAV-VisLoc region 01 base map: 9774 x 26762, WGS84, tiled 256, uncompressed.
func TestVisLocSatellite(t *testing.T) {
	path := testutil.Testdata(t, "geo/uav_visloc/01/satellite01.tif")
	f, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if f.Width != 9774 || f.Height != 26762 || !f.Tiled || f.TileW != 256 || f.Samples != 3 {
		t.Fatalf("layout = %dx%d tiled=%v tile=%d samples=%d", f.Width, f.Height, f.Tiled, f.TileW, f.Samples)
	}
	b, ok := f.LonLatBounds()
	if !ok || f.Georef.EPSG != 4326 {
		t.Fatalf("not geographic WGS84: %+v", f.Georef)
	}
	if math.Abs(b.West-115.970635) > 1e-6 || math.Abs(b.North-29.774065) > 1e-6 {
		t.Fatalf("bounds = %+v", b)
	}
	// First drone photo of the flight (01_0001, from 01.csv) must fall inside.
	x, y, _ := f.LonLatToPixel(115.9747973, 29.76096029)
	if x < 0 || y < 0 || x > float64(f.Width) || y > float64(f.Height) {
		t.Fatalf("01_0001 maps outside the raster: (%f, %f)", x, y)
	}
	if res := f.GroundResolution(); res < 0.2 || res > 0.4 {
		t.Fatalf("ground resolution %.3f m/px, expected ~0.26", res)
	}

	start := time.Now()
	ov, err := f.Overview(512)
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("overview took %v", took)
	}
	if ov.Bounds().Dy() != 512 {
		t.Fatalf("overview size %v", ov.Bounds())
	}
	assertNotBlank(t, ov)

	crop, err := f.ReadRegion(image.Rect(int(x)-128, int(y)-128, int(x)+128, int(y)+128))
	if err != nil {
		t.Fatal(err)
	}
	assertNotBlank(t, crop)
}

func TestProjectedSample(t *testing.T) {
	f, err := Open(testutil.Testdata(t, "remote_sensing/geotiff/cea.tif"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if f.Georef == nil || f.Georef.Geographic {
		t.Fatalf("cea.tif should be projected: %+v", f.Georef)
	}
	if _, ok := f.LonLatBounds(); ok {
		t.Fatal("projected raster must not report lon/lat bounds")
	}
	ov, err := f.Overview(128)
	if err != nil {
		t.Fatal(err)
	}
	assertNotBlank(t, ov)
}

func assertNotBlank(t *testing.T, img *image.RGBA) {
	t.Helper()
	var lo, hi uint8 = 255, 0
	for i := 0; i < len(img.Pix); i += 4 {
		lo, hi = min(lo, img.Pix[i]), max(hi, img.Pix[i])
	}
	if hi-lo < 20 {
		t.Fatalf("image looks blank (red range %d..%d)", lo, hi)
	}
}
