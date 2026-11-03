package parser

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"multimo-backend/internal/testutil"
)

func TestDetectAllTestdataFormats(t *testing.T) {
	cases := map[string]Detected{
		"video/visdrone_vid_val/mp4/uav0000086_00000_v.mp4":            {TypeVideo, "mp4"},
		"video/formats/M1004.wmv":                                      {TypeVideo, "wmv"},
		"video/formats/M1004.flv":                                      {TypeVideo, "flv"},
		"image/visdrone_det_val/images/0000001_02999_d_0000005.jpg":    {TypeImage, "jpeg"},
		"remote_sensing/dota_v2_val/images/P3044.png":                  {TypeImage, "png"},
		"remote_sensing/geotiff/cea.tif":                               {TypeRemoteSensing, "tiff"},
		"geo/uav_visloc/01/satellite01.tif":                            {TypeRemoteSensing, "tiff"},
		"docs/faa/Part_107_Summary.pdf":                                {TypeDocument, "pdf"},
		"docs/cn/uav_flight_regulation_2023.doc":                       {TypeDocument, "doc"},
		"docs/uk/pdra01_operations_manual_template.docx":               {TypeDocument, "docx"},
		"docs/geo_text/ne_10m_airports.geojson":                        {TypeDocument, "geojson"},
		"video/dji/palos_verdes/flight_record.csv":                     {TypeDocument, "csv"},
		"video/dji/palos_verdes/drone_palos_verdes_20250217174636.mp4": {TypeVideo, "mp4"},
	}
	for rel, want := range cases {
		got, err := Detect(testutil.Testdata(t, rel))
		if err != nil || got != want {
			t.Errorf("%s: got %+v err=%v, want %+v", rel, got, err, want)
		}
	}
}

func TestDetectTrustsContentOverExtension(t *testing.T) {
	src := testutil.Testdata(t, "docs/faa/Part_107_Summary.pdf")
	data, _ := os.ReadFile(src)
	fake := filepath.Join(t.TempDir(), "report.mp4")
	os.WriteFile(fake, data, 0o644)
	if d, err := Detect(fake); err != nil || d.Format != "pdf" {
		t.Fatalf("got %+v err=%v", d, err)
	}
	junk := filepath.Join(t.TempDir(), "x.exe")
	os.WriteFile(junk, []byte("MZ\x90\x00binary"), 0o644)
	if _, err := Detect(junk); err == nil {
		t.Fatal("unsupported file accepted")
	}
}

func TestParseImage(t *testing.T) {
	jpg, err := ParseImage(testutil.Testdata(t, "geo/uav_visloc/01/drone/01_0001.JPG"))
	if err != nil || jpg.Width != 3976 || jpg.Height != 2652 {
		t.Fatalf("visloc jpg: %+v err=%v", jpg, err)
	}
	if jpg.EXIF != nil && jpg.EXIF.Lat != nil {
		t.Fatal("UAV-VisLoc photos are expected to carry no GPS")
	}

	png, err := ParseImage(testutil.Testdata(t, "remote_sensing/dota_v2_val/images/P3044.png"))
	if err != nil || png.Width == 0 || png.Geo != nil {
		t.Fatalf("dota png: %+v err=%v", png, err)
	}

	start := time.Now()
	sat, err := ParseImage(testutil.Testdata(t, "geo/uav_visloc/01/satellite01.tif"))
	if err != nil || sat.Geo == nil || sat.Geo.Bounds == nil {
		t.Fatalf("satellite: %+v err=%v", sat, err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("parsing an 805 MB GeoTIFF header took %v", took)
	}
	if math.Abs(*sat.Geo.CenterLat-29.74) > 0.05 || !sat.Geo.Tiled {
		t.Fatalf("satellite geo: %+v", sat.Geo)
	}
}

func TestParseDocuments(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		rel, format string
		minPages    int
		mustContain string
	}{
		{"docs/faa/CFR-2024-title14-part107.pdf", "pdf", 10, "400 feet above ground level"},
		{"docs/cn/uav_flight_regulation_2023.doc", "doc", 1, "真高120米以上空域"},
		{"docs/uk/pdra01_operations_manual_template.docx", "docx", 1, "Maximum height not to exceed 120 metres"},
		{"docs/geo_text/ne_10m_airports.geojson", "geojson", 1, "iata_code=PEK"},
		{"video/dji/palos_verdes/flight_record.csv", "csv", 1, "OSD.latitude"},
	}
	for _, c := range cases {
		doc, err := ParseDocument(ctx, testutil.Testdata(t, c.rel), c.format)
		if err != nil {
			t.Errorf("%s: %v", c.rel, err)
			continue
		}
		if len(doc.Pages) < c.minPages {
			t.Errorf("%s: %d pages", c.rel, len(doc.Pages))
		}
		flat := strings.Join(strings.Fields(doc.Text()), " ")
		if !strings.Contains(flat, c.mustContain) && !strings.Contains(strings.ReplaceAll(doc.Text(), "\n", ""), c.mustContain) {
			t.Errorf("%s: text lacks %q", c.rel, c.mustContain)
		}
	}
}

func TestChunkDocument(t *testing.T) {
	doc, err := ParseDocument(context.Background(), testutil.Testdata(t, "docs/cn/uav_flight_regulation_2023.doc"), "doc")
	if err != nil {
		t.Fatal(err)
	}
	chunks := ChunkDocument(doc, 500, 80)
	if len(chunks) < 20 {
		t.Fatalf("only %d chunks", len(chunks))
	}
	for i, c := range chunks {
		if n := len([]rune(c.Text)); n > 500 || n == 0 {
			t.Fatalf("chunk %d has %d runes", i, n)
		}
		if c.Seq != i || c.Page != 1 {
			t.Fatalf("chunk %d: seq=%d page=%d", i, c.Seq, c.Page)
		}
	}
	// A clause should survive intact in at least one chunk thanks to overlap
	// and sentence-aware breaks.
	found := false
	for _, c := range chunks {
		if strings.Contains(strings.ReplaceAll(c.Text, "\n", ""), "应当在计划起飞1小时前向空中交通管理机构报告预计起飞时刻和准备情况") {
			found = true
		}
	}
	if !found {
		t.Fatal("clause split across chunks")
	}

	pdf, _ := ParseDocument(context.Background(), testutil.Testdata(t, "docs/faa/CFR-2024-title14-part107.pdf"), "pdf")
	pc := ChunkDocument(pdf, 800, 100)
	if pc[len(pc)-1].Page != len(pdf.Pages) && pc[len(pc)-1].Page > len(pdf.Pages) {
		t.Fatalf("page numbers out of range")
	}
}

func TestDecodeGB18030(t *testing.T) {
	gbk := []byte{0xce, 0xde, 0xc8, 0xcb, 0xbb, 0xfa} // 无人机 in GBK
	if got := decodeText(gbk); got != "无人机" {
		t.Fatalf("got %q", got)
	}
}
