// Package asset stores and looks up ingested files.
package asset

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/frame/g"

	"multimo-backend/internal/model/entity"
	"multimo-backend/internal/pkg/parser"
)

var ErrNotFound = errors.New("asset not found")

type Service struct {
	DB      gdb.DB
	DataDir string
}

type CreateInput struct {
	Path        string // file on disk (already in place)
	Filename    string // original name shown to users
	Collection  string
	Type        string // optional override of the detected type
	Source      string // upload | register
	SidecarPath string
	Meta        map[string]any
}

// Create records a file. The same content in the same collection is stored
// once: the existing asset is returned with created=false.
func (s *Service) Create(ctx context.Context, in CreateInput) (a *entity.Asset, created bool, err error) {
	det, err := parser.Detect(in.Path)
	if err != nil {
		return nil, false, err
	}
	typ := det.Type
	if in.Type != "" {
		if !validOverride(det.Type, in.Type) {
			return nil, false, fmt.Errorf("type %q does not fit a %s file", in.Type, det.Format)
		}
		typ = in.Type
	}
	sum, size, err := md5File(in.Path)
	if err != nil {
		return nil, false, err
	}
	if in.Collection == "" {
		in.Collection = "default"
	}
	if in.Source == "" {
		in.Source = "upload"
	}
	if in.Meta == nil {
		in.Meta = map[string]any{}
	}
	var sidecar any
	if in.SidecarPath != "" {
		sidecar = in.SidecarPath
	}
	metaJSON, err := json.Marshal(in.Meta)
	if err != nil {
		return nil, false, err
	}
	rec, err := s.DB.GetOne(ctx, `
		INSERT INTO assets (uid, type, format, collection, filename, storage_path, source, sidecar_path, size_bytes, md5, meta)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb)
		ON CONFLICT (collection, md5) DO NOTHING
		RETURNING id`, NewUID(), typ, det.Format, in.Collection, in.Filename, in.Path, in.Source, sidecar, size, sum, string(metaJSON))
	if err != nil {
		return nil, false, err
	}
	if rec.IsEmpty() {
		existing, err := s.byMD5(ctx, in.Collection, sum)
		return existing, false, err
	}
	a, err = s.Get(ctx, rec["id"].Int64())
	return a, true, err
}

// validOverride allows re-labelling only within compatible kinds, e.g. a PNG
// declared as remote_sensing (DOTA tiles), never a PDF as video.
func validOverride(detected, want string) bool {
	if detected == want {
		return true
	}
	img := map[string]bool{parser.TypeImage: true, parser.TypeRemoteSensing: true}
	return img[detected] && img[want]
}

func (s *Service) Get(ctx context.Context, id int64) (*entity.Asset, error) {
	var a *entity.Asset
	if err := s.DB.Model("assets").Ctx(ctx).Where("id", id).Scan(&a); err != nil {
		return nil, err
	}
	if a == nil {
		return nil, ErrNotFound
	}
	return a, nil
}

func (s *Service) byMD5(ctx context.Context, collection, sum string) (*entity.Asset, error) {
	var a *entity.Asset
	err := s.DB.Model("assets").Ctx(ctx).Where("collection", collection).Where("md5", sum).Scan(&a)
	if err == nil && a == nil {
		err = ErrNotFound
	}
	return a, err
}

type ListFilter struct {
	Type       string
	Collection string
	Status     string
	Keyword    string
	Page, Size int
}

func (s *Service) List(ctx context.Context, f ListFilter) ([]*entity.Asset, int, error) {
	m := s.DB.Model("assets").Ctx(ctx)
	if f.Type != "" {
		m = m.Where("type", f.Type)
	}
	if f.Collection != "" {
		m = m.Where("collection", f.Collection)
	}
	if f.Status != "" {
		m = m.Where("status", f.Status)
	}
	if f.Keyword != "" {
		m = m.WhereLike("filename", "%"+f.Keyword+"%")
	}
	total, err := m.Count()
	if err != nil {
		return nil, 0, err
	}
	var list []*entity.Asset
	err = m.OrderDesc("id").Page(max(f.Page, 1), min(max(f.Size, 1), 200)).Scan(&list)
	return list, total, err
}

// Delete removes the asset rows and every derived file. Originals that were
// registered in place (testdata) are left on disk.
func (s *Service) Delete(ctx context.Context, id int64) error {
	a, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if _, err := s.DB.Model("assets").Ctx(ctx).Where("id", id).Delete(); err != nil {
		return err
	}
	return os.RemoveAll(s.Dir(a.Uid))
}

// Update writes the given columns. A "meta" value is stored as JSON.
func (s *Service) Update(ctx context.Context, id int64, data g.Map) error {
	if m, ok := data["meta"]; ok {
		b, err := json.Marshal(m)
		if err != nil {
			return err
		}
		data["meta"] = string(b)
	}
	data["updated_at"] = gdb.Raw("now()")
	_, err := s.DB.Model("assets").Ctx(ctx).Where("id", id).Data(data).Update()
	return err
}

// Dir is where an asset's uploaded original and derived files live.
func (s *Service) Dir(uid string) string { return filepath.Join(s.DataDir, "assets", uid) }

// Abs resolves a path stored relative to DATA_DIR.
func (s *Service) Abs(rel string) string {
	if filepath.IsAbs(rel) {
		return rel
	}
	return filepath.Join(s.DataDir, rel)
}

// Rel makes p relative to DATA_DIR for storage in meta.
func (s *Service) Rel(p string) string {
	if r, err := filepath.Rel(s.DataDir, p); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return p
}

// NewUID returns a 16-character random identifier used in paths and URLs.
func NewUID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func md5File(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := md5.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
