// Package ingest accepts files (upload or in-place registration) and runs the
// first processing step: metadata, thumbnails and document text.
package ingest

import (
	"context"
	"errors"
	"fmt"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/frame/g"

	"multimo-backend/internal/model/entity"
	"multimo-backend/internal/pkg/geotiff"
	"multimo-backend/internal/pkg/media"
	"multimo-backend/internal/pkg/parser"
	"multimo-backend/internal/service/asset"
	"multimo-backend/internal/service/pipeline"
)

// Kind is the task kind handled by this package.
const Kind = "ingest"

const (
	thumbWidth   = 640
	chunkRunes   = 500
	chunkOverlap = 80
)

type Service struct {
	Assets        *asset.Service
	DB            gdb.DB
	Dispatcher    *pipeline.Dispatcher
	TestdataDir   string // absolute; registration is restricted to this tree
	FFmpegThreads int
	// Next lists the task kinds to run once an asset is ingested.
	Next func(a *entity.Asset) []string
}

// ---------------------------------------------------------------- intake

type UploadInput struct {
	TmpPath     string // uploaded file, will be moved
	Filename    string
	Collection  string
	Type        string
	SidecarTmp  string // optional
	SidecarName string
}

// Upload stores an uploaded file under DATA_DIR/assets/<uid>/ and queues it.
func (s *Service) Upload(ctx context.Context, in UploadInput) (*entity.Asset, bool, error) {
	a, created, err := s.Assets.Create(ctx, asset.CreateInput{Path: in.TmpPath, Filename: in.Filename,
		Collection: in.Collection, Type: in.Type, Source: "upload"})
	if err != nil || !created {
		os.Remove(in.TmpPath)
		if in.SidecarTmp != "" {
			os.Remove(in.SidecarTmp)
		}
		return a, created, err
	}
	dir := s.Assets.Dir(a.Uid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, false, err
	}
	dst := filepath.Join(dir, "original"+strings.ToLower(filepath.Ext(in.Filename)))
	if err := moveFile(in.TmpPath, dst); err != nil {
		return nil, false, err
	}
	upd := g.Map{"storage_path": dst}
	if in.SidecarTmp != "" {
		sc := filepath.Join(dir, "sidecar"+strings.ToLower(filepath.Ext(in.SidecarName)))
		if err := moveFile(in.SidecarTmp, sc); err != nil {
			return nil, false, err
		}
		upd["sidecar_path"] = sc
	}
	if err := s.Assets.Update(ctx, a.Id, upd); err != nil {
		return nil, false, err
	}
	if err := s.Dispatcher.Enqueue(ctx, a.Id, Kind); err != nil {
		return nil, false, err
	}
	a, err = s.Assets.Get(ctx, a.Id)
	return a, true, err
}

type RegisterItem struct {
	Path       string         `json:"path"       v:"required" dc:"TESTDATA_DIR 下的相对路径（或其中的绝对路径）"`
	Collection string         `json:"collection" dc:"集合名，默认 default"`
	Type       string         `json:"type"       dc:"覆盖自动识别的类型（仅限 image/remote_sensing 互换）"`
	Sidecar    string         `json:"sidecar"    dc:"配套文件路径，如 DJI 飞行记录 CSV"`
	Metadata   map[string]any `json:"metadata"   dc:"附加元数据，如 {\"time\": \"2018-09-17T12:44:21\"}"`
}

type RegisterResult struct {
	Path    string `json:"path"`
	AssetId int64  `json:"assetId,omitempty"`
	Created bool   `json:"created"`
	Error   string `json:"error,omitempty"`
}

// Register records files that already sit under TESTDATA_DIR without copying
// them, and queues ingestion. Items are independent: one bad path does not
// stop the rest.
func (s *Service) Register(ctx context.Context, items []RegisterItem) []RegisterResult {
	out := make([]RegisterResult, len(items))
	for i, it := range items {
		out[i].Path = it.Path
		p, err := s.resolve(it.Path)
		if err != nil {
			out[i].Error = err.Error()
			continue
		}
		sidecar := ""
		if it.Sidecar != "" {
			if sidecar, err = s.resolve(it.Sidecar); err != nil {
				out[i].Error = "sidecar: " + err.Error()
				continue
			}
		}
		meta := map[string]any{"registeredPath": it.Path}
		if len(it.Metadata) > 0 {
			meta["provided"] = it.Metadata
		}
		a, created, err := s.Assets.Create(ctx, asset.CreateInput{Path: p, Filename: filepath.Base(p),
			Collection: it.Collection, Type: it.Type, Source: "register", SidecarPath: sidecar, Meta: meta})
		if err != nil {
			out[i].Error = err.Error()
			continue
		}
		out[i].AssetId, out[i].Created = a.Id, created
		if created {
			if err := s.Dispatcher.Enqueue(ctx, a.Id, Kind); err != nil {
				out[i].Error = err.Error()
			}
		}
	}
	return out
}

// resolve maps a user path to an absolute path inside TestdataDir, rejecting
// anything that escapes it (.., absolute paths elsewhere, symlinks out).
func (s *Service) resolve(p string) (string, error) {
	if s.TestdataDir == "" {
		return "", errors.New("TESTDATA_DIR is not configured")
	}
	root, err := filepath.EvalSymlinks(s.TestdataDir)
	if err != nil {
		return "", fmt.Errorf("TESTDATA_DIR: %w", err)
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(s.TestdataDir, p)
	}
	real, err := filepath.EvalSymlinks(filepath.Clean(p))
	if err != nil {
		return "", fmt.Errorf("file not found: %s", filepath.Base(p))
	}
	if rel, err := filepath.Rel(root, real); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("path is outside TESTDATA_DIR")
	}
	st, err := os.Stat(real)
	if err != nil || st.IsDir() {
		return "", errors.New("not a regular file")
	}
	return real, nil
}

// ---------------------------------------------------------------- processing

// Handle is the pipeline handler for Kind.
func (s *Service) Handle(ctx context.Context, t *pipeline.Task, report pipeline.Reporter) error {
	a, err := s.Assets.Get(ctx, t.AssetID)
	if errors.Is(err, asset.ErrNotFound) {
		return pipeline.Permanent(err)
	}
	if err != nil {
		return err
	}
	s.Assets.Update(ctx, a.Id, g.Map{"status": "processing", "error": nil}) //nolint:errcheck

	upd := g.Map{}
	meta := g.Map{}
	for k, v := range a.Meta {
		meta[k] = v
	}
	switch a.Type {
	case parser.TypeVideo:
		err = s.video(ctx, a, upd, meta)
	case parser.TypeImage, parser.TypeRemoteSensing:
		err = s.image(ctx, a, upd, meta)
	case parser.TypeDocument:
		report(0.3, "extracting text")
		err = s.document(ctx, a, upd, meta)
	default:
		err = pipeline.Permanent(fmt.Errorf("unknown asset type %q", a.Type))
	}
	if errors.Is(err, parser.ErrUnsupported) {
		err = pipeline.Permanent(err)
	}
	if err != nil {
		if pipeline.IsPermanent(err) || t.Attempts >= t.MaxAttempts {
			s.Assets.Update(context.WithoutCancel(ctx), a.Id, g.Map{"status": "failed", "error": err.Error()}) //nolint:errcheck
		}
		return err
	}
	applyProvided(meta, upd)
	upd["meta"] = meta
	upd["status"] = "ready"
	upd["error"] = nil
	if err := s.Assets.Update(ctx, a.Id, upd); err != nil {
		return err
	}
	if s.Next != nil {
		for _, kind := range s.Next(a) {
			if err := s.Dispatcher.Enqueue(ctx, a.Id, kind); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) video(ctx context.Context, a *entity.Asset, upd, meta g.Map) error {
	p, err := media.Probe(ctx, a.StoragePath)
	if err != nil {
		return err
	}
	upd["width"], upd["height"], upd["duration_sec"], upd["fps"] = p.Width, p.Height, p.Duration, p.FPS
	meta["probe"] = p
	switch {
	case p.CreationTime != nil:
		upd["captured_at"] = p.CreationTime.UTC()
		meta["capturedAtSource"] = "container metadata"
	default:
		if ts, ok := media.FilenameTime(a.Filename); ok {
			upd["captured_at"] = ts
			meta["capturedAtSource"] = "filename (device local time, zone unknown)"
		}
	}
	if p.Lat != nil {
		upd["lat"], upd["lon"], upd["geo_source"] = *p.Lat, *p.Lon, "metadata"
	}
	thumb := filepath.Join(s.Assets.Dir(a.Uid), "thumb.jpg")
	ts := min(max(p.Duration*0.1, 0), 5)
	if err := media.ExtractFrame(ctx, a.StoragePath, ts, thumb, thumbWidth, s.FFmpegThreads); err != nil {
		g.Log().Warningf(ctx, "asset %d thumbnail: %v", a.Id, err)
	} else {
		meta["thumbnail"] = s.Assets.Rel(thumb)
	}
	return nil
}

func (s *Service) image(ctx context.Context, a *entity.Asset, upd, meta g.Map) error {
	info, err := parser.ParseImage(a.StoragePath)
	if err != nil {
		return err
	}
	upd["width"], upd["height"] = info.Width, info.Height
	meta["image"] = info
	if e := info.EXIF; e != nil {
		if e.Time != nil {
			upd["captured_at"] = *e.Time
			meta["capturedAtSource"] = "exif"
		}
		if e.Lat != nil {
			upd["lat"], upd["lon"], upd["geo_source"] = *e.Lat, *e.Lon, "exif"
		}
	}
	if geo := info.Geo; geo != nil && geo.CenterLat != nil {
		upd["lat"], upd["lon"], upd["geo_source"] = *geo.CenterLat, *geo.CenterLon, "geotiff"
		// The raster covers an area; report half its diagonal as the extent.
		b := geo.Bounds
		upd["geo_accuracy_m"] = haversine(b.South, b.West, b.North, b.East) / 2
	}

	thumb := filepath.Join(s.Assets.Dir(a.Uid), "thumb.jpg")
	if err := s.imageThumb(ctx, a, info, thumb); err != nil {
		g.Log().Warningf(ctx, "asset %d thumbnail: %v", a.Id, err)
	} else {
		meta["thumbnail"] = s.Assets.Rel(thumb)
	}
	return nil
}

// imageThumb uses the GeoTIFF reader for TIFFs (ffmpeg renders tiled rasters
// black and reads the whole file) and ffmpeg for everything else.
func (s *Service) imageThumb(ctx context.Context, a *entity.Asset, info *parser.ImageInfo, dst string) error {
	if info.Format == "tiff" {
		if t, err := geotiff.Open(a.StoragePath); err == nil {
			defer t.Close()
			ov, err := t.Overview(thumbWidth)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			f, err := os.Create(dst)
			if err != nil {
				return err
			}
			defer f.Close()
			return jpeg.Encode(f, ov, &jpeg.Options{Quality: 82})
		}
	}
	return media.ImageThumb(ctx, a.StoragePath, dst, thumbWidth)
}

func (s *Service) document(ctx context.Context, a *entity.Asset, upd, meta g.Map) error {
	doc, err := parser.ParseDocument(ctx, a.StoragePath, a.Format)
	if err != nil {
		return err
	}
	chunks := parser.ChunkDocument(doc, chunkRunes, chunkOverlap)
	err = s.DB.Transaction(ctx, func(ctx context.Context, tx gdb.TX) error {
		if _, err := tx.Model("doc_chunks").Where("asset_id", a.Id).Delete(); err != nil {
			return err
		}
		if len(chunks) == 0 {
			return nil
		}
		rows := make(g.List, len(chunks))
		for i, c := range chunks {
			rows[i] = g.Map{"asset_id": a.Id, "seq": c.Seq, "page": c.Page, "text": c.Text}
		}
		_, err := tx.Model("doc_chunks").Data(rows).Batch(200).Insert()
		return err
	})
	if err != nil {
		return err
	}
	upd["page_count"] = len(doc.Pages)
	meta["title"] = doc.Title
	meta["chunks"] = len(chunks)
	meta["chars"] = len([]rune(doc.Text()))
	if len(doc.Meta) > 0 {
		meta["document"] = doc.Meta
	}
	return nil
}

// applyProvided lets metadata supplied at registration (e.g. capture time
// from a dataset's CSV) fill fields the file itself did not carry.
func applyProvided(meta, upd g.Map) {
	p, ok := meta["provided"].(map[string]any)
	if !ok {
		return
	}
	if s, ok := p["time"].(string); ok {
		for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"} {
			if t, err := time.Parse(layout, s); err == nil {
				upd["captured_at"] = t
				meta["capturedAtSource"] = "provided at registration"
				break
			}
		}
	}
}

func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := out.ReadFrom(in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Remove(src)
}
