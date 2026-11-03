package asset

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gogf/gf/v2/errors/gcode"
	"github.com/gogf/gf/v2/errors/gerror"
	"github.com/gogf/gf/v2/frame/g"

	v1 "multimo-backend/api/asset/v1"
	"multimo-backend/internal/model/entity"
	assetsvc "multimo-backend/internal/service/asset"
	"multimo-backend/internal/service/ingest"
	"multimo-backend/internal/service/pipeline"
)

type ControllerV1 struct {
	Assets     *assetsvc.Service
	Ingest     *ingest.Service
	Dispatcher *pipeline.Dispatcher
}

func item(a *entity.Asset) *v1.AssetItem {
	if a == nil {
		return nil
	}
	it := &v1.AssetItem{Asset: a}
	if t, _ := a.Meta["thumbnail"].(string); t != "" {
		it.ThumbnailUrl = fmt.Sprintf("/api/v1/assets/%d/thumbnail", a.Id)
	}
	return it
}

func notFound(err error) error {
	if errors.Is(err, assetsvc.ErrNotFound) {
		return gerror.NewCode(gcode.CodeNotFound, "素材不存在")
	}
	return err
}

func (c *ControllerV1) Upload(ctx context.Context, req *v1.UploadReq) (*v1.UploadRes, error) {
	tmpDir := filepath.Join(c.Assets.DataDir, "tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return nil, err
	}
	name, err := req.File.Save(tmpDir, true)
	if err != nil {
		return nil, err
	}
	in := ingest.UploadInput{TmpPath: filepath.Join(tmpDir, name), Filename: filepath.Base(req.File.Filename),
		Collection: req.Collection, Type: req.Type}
	if req.Sidecar != nil {
		sc, err := req.Sidecar.Save(tmpDir, true)
		if err != nil {
			os.Remove(in.TmpPath)
			return nil, err
		}
		in.SidecarTmp, in.SidecarName = filepath.Join(tmpDir, sc), req.Sidecar.Filename
	}
	a, created, err := c.Ingest.Upload(ctx, in)
	if err != nil {
		return nil, gerror.WrapCode(gcode.CodeInvalidParameter, err, "无法接收该文件")
	}
	return &v1.UploadRes{Asset: item(a), Created: created}, nil
}

func (c *ControllerV1) Register(ctx context.Context, req *v1.RegisterReq) (*v1.RegisterRes, error) {
	if len(req.Items) > 1000 {
		return nil, gerror.NewCode(gcode.CodeInvalidParameter, "一次最多登记 1000 项")
	}
	items := make([]ingest.RegisterItem, len(req.Items))
	for i, it := range req.Items {
		items[i] = ingest.RegisterItem{Path: it.Path, Collection: it.Collection, Type: it.Type,
			Sidecar: it.Sidecar, Metadata: it.Metadata}
	}
	res := &v1.RegisterRes{}
	for _, r := range c.Ingest.Register(ctx, items) {
		res.Results = append(res.Results, v1.RegisterResult(r))
		if r.Created {
			res.Created++
		}
		if r.Error != "" {
			res.Failed++
		}
	}
	return res, nil
}

func (c *ControllerV1) List(ctx context.Context, req *v1.ListReq) (*v1.ListRes, error) {
	list, total, err := c.Assets.List(ctx, assetsvc.ListFilter{Type: req.Type, Collection: req.Collection,
		Status: req.Status, Keyword: req.Keyword, Page: req.Page, Size: req.Size})
	if err != nil {
		return nil, err
	}
	res := &v1.ListRes{Total: total, Page: req.Page, Size: req.Size, List: make([]*v1.AssetItem, len(list))}
	for i, a := range list {
		res.List[i] = item(a)
	}
	return res, nil
}

func (c *ControllerV1) Get(ctx context.Context, req *v1.GetReq) (*v1.GetRes, error) {
	a, err := c.Assets.Get(ctx, req.Id)
	if err != nil {
		return nil, notFound(err)
	}
	res := &v1.GetRes{Asset: item(a)}
	if err := c.Assets.DB.Model("tasks").Ctx(ctx).Where("asset_id", a.Id).OrderAsc("id").Scan(&res.Tasks); err != nil {
		return nil, err
	}
	db := c.Assets.DB
	res.Counts.Keyframes, _ = db.Model("keyframes").Ctx(ctx).Where("asset_id", a.Id).Count()
	res.Counts.Detections, _ = db.Model("detections").Ctx(ctx).Where("asset_id", a.Id).Count()
	res.Counts.Chunks, _ = db.Model("doc_chunks").Ctx(ctx).Where("asset_id", a.Id).Count()
	return res, nil
}

func (c *ControllerV1) Delete(ctx context.Context, req *v1.DeleteReq) (*v1.DeleteRes, error) {
	return &v1.DeleteRes{}, notFound(c.Assets.Delete(ctx, req.Id))
}

func (c *ControllerV1) Reprocess(ctx context.Context, req *v1.ReprocessReq) (*v1.ReprocessRes, error) {
	if _, err := c.Assets.Get(ctx, req.Id); err != nil {
		return nil, notFound(err)
	}
	if err := c.Assets.Update(ctx, req.Id, g.Map{"status": "pending", "error": nil}); err != nil {
		return nil, err
	}
	return &v1.ReprocessRes{}, c.Dispatcher.Enqueue(ctx, req.Id, ingest.Kind)
}

func (c *ControllerV1) Thumbnail(ctx context.Context, req *v1.ThumbnailReq) (*v1.ThumbnailRes, error) {
	a, err := c.Assets.Get(ctx, req.Id)
	if err != nil {
		return nil, notFound(err)
	}
	rel, _ := a.Meta["thumbnail"].(string)
	path := c.Assets.Abs(rel)
	if rel == "" || !fileExists(path) {
		return nil, gerror.NewCode(gcode.CodeNotFound, "缩略图尚未生成")
	}
	r := g.RequestFromCtx(ctx)
	r.Response.Header().Set("Cache-Control", "public, max-age=86400")
	r.Response.ServeFile(path)
	return nil, nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
