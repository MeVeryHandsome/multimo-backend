package media

import (
	"context"
	"image/jpeg"
	"math"
	"os"
	"path/filepath"
	"testing"

	"multimo-backend/internal/testutil"
)

func TestProbeFormats(t *testing.T) {
	cases := []struct {
		rel           string
		width, height int
		fps           float64
		minDur        float64
	}{
		{"video/visdrone_vid_val/mp4/uav0000339_00001_v.mp4", 1904, 1070, 25, 10},
		{"video/formats/uav0000339_00001_v.wmv", 1904, 1070, 25, 10},
		{"video/formats/uav0000339_00001_v.flv", 1904, 1070, 25, 10},
		{"video/dji/palos_verdes/drone_palos_verdes_20250217174636.mp4", 1920, 1080, 29.97, 400},
	}
	for _, c := range cases {
		t.Run(filepath.Ext(c.rel), func(t *testing.T) {
			p, err := Probe(context.Background(), testutil.Testdata(t, c.rel))
			if err != nil {
				t.Fatal(err)
			}
			if p.Width != c.width || p.Height != c.height || math.Abs(p.FPS-c.fps) > 0.05 || p.Duration < c.minDur {
				t.Fatalf("%s: %dx%d %.2ffps %.1fs", c.rel, p.Width, p.Height, p.FPS, p.Duration)
			}
		})
	}
}

func TestExtractFrame(t *testing.T) {
	src := testutil.Testdata(t, "video/uavdt/mp4/M1004.mp4")
	dst := filepath.Join(t.TempDir(), "f.jpg")
	if err := ExtractFrame(context.Background(), src, 3.5, dst, 640, 1); err != nil {
		t.Fatal(err)
	}
	f, _ := os.Open(dst)
	defer f.Close()
	cfg, err := jpeg.DecodeConfig(f)
	if err != nil || cfg.Width != 640 {
		t.Fatalf("frame %dx%d err=%v", cfg.Width, cfg.Height, err)
	}
}

func TestParseISO6709(t *testing.T) {
	lat, lon, alt, ok := ParseISO6709("+33.7332-118.3347+012.000/")
	if !ok || lat != 33.7332 || lon != -118.3347 || alt == nil || *alt != 12 {
		t.Fatalf("got %v %v %v %v", lat, lon, alt, ok)
	}
	if _, _, _, ok := ParseISO6709("garbage"); ok {
		t.Fatal("garbage accepted")
	}
}

func TestFilenameTime(t *testing.T) {
	ts, ok := FilenameTime("drone_palos_verdes_20250217174636.mp4")
	if !ok || ts.Format("2006-01-02 15:04:05") != "2025-02-17 17:46:36" {
		t.Fatalf("got %v %v", ts, ok)
	}
	if ts, ok := FilenameTime("DJI_20240802_183101_0025_D.MP4"); !ok || ts.Hour() != 18 {
		t.Fatalf("underscore form: %v %v", ts, ok)
	}
	if _, ok := FilenameTime("uav0000339_00001_v.mp4"); ok {
		t.Fatal("false positive")
	}
}
