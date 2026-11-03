package ingest

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"multimo-backend/internal/model/entity"
	"multimo-backend/internal/pkg/workerpool"
	"multimo-backend/internal/service/asset"
	"multimo-backend/internal/service/pipeline"
	"multimo-backend/internal/testutil"
)

func setup(t *testing.T) (*Service, *pipeline.Dispatcher, context.CancelFunc) {
	t.Helper()
	db := testutil.DB(t)
	testutil.Testdata(t, "docs/faa/Part_107_Summary.pdf") // skip when testdata is absent
	root := testutil.TestdataRoot()
	assets := &asset.Service{DB: db, DataDir: t.TempDir()}
	d := pipeline.New(db, workerpool.New(4))
	d.SetTiming(50*time.Millisecond, 10*time.Millisecond)
	s := &Service{Assets: assets, DB: db, Dispatcher: d, TestdataDir: root, FFmpegThreads: 1}
	d.Register(Kind, s.Handle)
	ctx, cancel := context.WithCancel(context.Background())
	go d.Run(ctx)
	return s, d, cancel
}

func waitReady(t *testing.T, s *Service, ids []int64) []*entity.Asset {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	out := make([]*entity.Asset, len(ids))
	for i, id := range ids {
		for {
			a, err := s.Assets.Get(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			if a.Status == "ready" || a.Status == "failed" {
				out[i] = a
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("asset %d stuck in %s", id, a.Status)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	return out
}

func TestRegisterAndIngestEveryType(t *testing.T) {
	s, _, cancel := setup(t)
	defer cancel()
	ctx := context.Background()

	items := []RegisterItem{
		{Path: "video/formats/M1004.wmv", Collection: "formats"},
		{Path: "video/dji/palos_verdes/drone_palos_verdes_20250217174636.mp4", Collection: "dji",
			Sidecar: "video/dji/palos_verdes/flight_record.csv"},
		{Path: "geo/uav_visloc/01/drone/01_0001.JPG", Collection: "visloc",
			Metadata: map[string]any{"time": "2018-09-17T12:44:21"}},
		{Path: "remote_sensing/dota_v2_val/images/P3044.png", Collection: "dota", Type: "remote_sensing"},
		{Path: "geo/uav_visloc/01/satellite01.tif", Collection: "basemap"},
		{Path: "docs/cn/uav_flight_regulation_2023.doc", Collection: "docs"},
		{Path: "docs/faa/CFR-2024-title14-part107.pdf", Collection: "docs"},
		{Path: "docs/uk/pdra01_operations_manual_template.docx", Collection: "docs"},
		{Path: "docs/geo_text/ne_10m_airports.geojson", Collection: "docs"},
	}
	res := s.Register(ctx, items)
	ids := make([]int64, len(res))
	for i, r := range res {
		if r.Error != "" || !r.Created {
			t.Fatalf("register %s: %+v", items[i].Path, r)
		}
		ids[i] = r.AssetId
	}
	got := waitReady(t, s, ids)
	for i, a := range got {
		if a.Status != "ready" {
			t.Errorf("%s: status %s error %q", items[i].Path, a.Status, a.Error)
		}
		if a.Type != "document" {
			thumb, _ := a.Meta["thumbnail"].(string)
			if st, err := os.Stat(s.Assets.Abs(thumb)); thumb == "" || err != nil || st.Size() < 1000 {
				t.Errorf("%s: thumbnail %q missing (%v)", items[i].Path, thumb, err)
			}
		}
	}
	wmv, dji, jpg, png, sat, doc := got[0], got[1], got[2], got[3], got[4], got[5]

	if wmv.Format != "wmv" || wmv.Width == nil || *wmv.Width != 1024 || wmv.DurationSec == nil || *wmv.DurationSec < 10 {
		t.Errorf("wmv: %+v", wmv)
	}
	if dji.CapturedAt == nil || dji.CapturedAt.Layout("2006-01-02") != "2025-02-17" || dji.SidecarPath == "" {
		t.Errorf("dji: captured %v sidecar %q", dji.CapturedAt, dji.SidecarPath)
	}
	if jpg.CapturedAt == nil || jpg.CapturedAt.Layout("2006-01-02 15:04") != "2018-09-17 12:44" || jpg.Lat != nil {
		t.Errorf("visloc jpg: captured %v lat %v", jpg.CapturedAt, jpg.Lat)
	}
	if png.Type != "remote_sensing" || png.Width == nil {
		t.Errorf("dota png: %+v", png)
	}
	if sat.GeoSource != "geotiff" || sat.Lat == nil || math.Abs(*sat.Lat-29.74) > 0.05 {
		t.Errorf("satellite geo: source %q lat %v", sat.GeoSource, sat.Lat)
	}
	n, _ := s.DB.Model("doc_chunks").Where("asset_id", doc.Id).Count()
	if n < 20 || doc.PageCount == nil || *doc.PageCount != 1 {
		t.Errorf("cn doc: %d chunks, pages %v", n, doc.PageCount)
	}
	pdfChunks, _ := s.DB.Model("doc_chunks").Where("asset_id", got[6].Id).Count()
	if got[6].PageCount == nil || *got[6].PageCount < 10 || pdfChunks < *got[6].PageCount {
		t.Errorf("pdf: pages %v chunks %d", got[6].PageCount, pdfChunks)
	}

	// Registering the same file again is a no-op.
	again := s.Register(ctx, items[:1])
	if again[0].Created || again[0].AssetId != ids[0] {
		t.Errorf("duplicate register: %+v", again[0])
	}
}

func TestRegisterRejectsPathsOutsideTestdata(t *testing.T) {
	s, _, cancel := setup(t)
	defer cancel()
	for _, p := range []string{"../.env", "/etc/passwd", "docs/../../.env", "docs", "nope.pdf"} {
		r := s.Register(context.Background(), []RegisterItem{{Path: p}})
		if r[0].Error == "" {
			t.Errorf("%s accepted", p)
		}
	}
}

func TestUnsupportedUploadIsRejected(t *testing.T) {
	s, _, cancel := setup(t)
	defer cancel()
	tmp := filepath.Join(t.TempDir(), "x.bin")
	os.WriteFile(tmp, []byte("MZ\x90\x00 not a media file"), 0o644)
	if _, _, err := s.Upload(context.Background(), UploadInput{TmpPath: tmp, Filename: "x.bin"}); err == nil {
		t.Fatal("unsupported upload accepted")
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatal("rejected upload left its temp file behind")
	}
}
