package keyframe

import (
	"context"
	"fmt"
	"os"

	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/errors/gcode"
	"github.com/gogf/gf/v2/errors/gerror"
	"github.com/gogf/gf/v2/frame/g"

	v1 "multimo-backend/api/keyframe/v1"
	"multimo-backend/internal/service/asset"
	"multimo-backend/internal/service/pipeline"
)

type ControllerV1 struct {
	DB         gdb.DB
	Assets     *asset.Service
	Dispatcher *pipeline.Dispatcher
}

const kfFields = `k.id, k.asset_id, k.idx, k.ts_sec, k.frame_no, k.width, k.height, k.description, k.description_source,
	k.tags, k.captured_at, k.lat, k.lon, k.geo_source, k.geo_accuracy_m, k.meta,
	(SELECT count(*) FROM detections d WHERE d.keyframe_id = k.id) AS detections`

func withURL(list []*v1.Keyframe) []*v1.Keyframe {
	for _, k := range list {
		k.ImageUrl = fmt.Sprintf("/api/v1/keyframes/%d/image", k.Id)
	}
	return list
}

func (c *ControllerV1) List(ctx context.Context, req *v1.ListReq) (*v1.ListRes, error) {
	res := &v1.ListRes{}
	err := c.DB.Model("keyframes k").Ctx(ctx).Fields(kfFields).Where("k.asset_id", req.Id).OrderAsc("k.idx").Scan(&res.List)
	withURL(res.List)
	return res, err
}

func (c *ControllerV1) Get(ctx context.Context, req *v1.GetReq) (*v1.GetRes, error) {
	res := &v1.GetRes{}
	if err := c.DB.Model("keyframes k").Ctx(ctx).Fields(kfFields).Where("k.id", req.Id).Scan(&res.Keyframe); err != nil {
		return nil, err
	}
	if res.Keyframe == nil {
		return nil, gerror.NewCode(gcode.CodeNotFound, "关键帧不存在")
	}
	withURL([]*v1.Keyframe{res.Keyframe})
	if gc, err := c.DB.GetValue(ctx, "SELECT geo_candidates FROM keyframes WHERE id = ?", req.Id); err == nil && !gc.IsEmpty() {
		res.Keyframe.Meta["geoCandidates"] = gc.Interface()
	}
	err := c.DB.Model("detections").Ctx(ctx).Where("keyframe_id", req.Id).OrderAsc("id").Scan(&res.Detections)
	return res, err
}

func (c *ControllerV1) Image(ctx context.Context, req *v1.ImageReq) (*v1.ImageRes, error) {
	p, err := c.DB.GetValue(ctx, "SELECT path FROM keyframes WHERE id = ?", req.Id)
	if err != nil {
		return nil, err
	}
	path := c.Assets.Abs(p.String())
	if p.IsEmpty() {
		return nil, gerror.NewCode(gcode.CodeNotFound, "关键帧不存在")
	}
	if _, err := os.Stat(path); err != nil {
		return nil, gerror.NewCode(gcode.CodeNotFound, "关键帧图片不存在")
	}
	r := g.RequestFromCtx(ctx)
	r.Response.Header().Set("Cache-Control", "public, max-age=86400")
	r.Response.ServeFile(path)
	return nil, nil
}

func (c *ControllerV1) Track(ctx context.Context, req *v1.TrackReq) (*v1.TrackRes, error) {
	res := &v1.TrackRes{}
	n, err := c.DB.Model("telemetry_points").Ctx(ctx).Where("asset_id", req.Id).Count()
	if err != nil {
		return nil, err
	}
	step := max(1, (n+1999)/2000)
	err = c.DB.GetScan(ctx, &res.Points, `SELECT t_sec, ts, lat, lon, alt_m FROM (
		SELECT *, row_number() OVER (ORDER BY t_sec) AS rn FROM telemetry_points WHERE asset_id = ?) p
		WHERE (rn - 1) % ? = 0 ORDER BY t_sec`, req.Id, step)
	return res, err
}

func (c *ControllerV1) Rerun(ctx context.Context, req *v1.RerunReq) (*v1.RerunRes, error) {
	m := c.DB.Model("assets").Ctx(ctx)
	if req.Collection != "" {
		m = m.Where("collection", req.Collection)
	}
	if req.Type != "" {
		m = m.Where("type", req.Type)
	}
	if len(req.AssetIds) > 0 {
		m = m.WhereIn("id", req.AssetIds)
	}
	ids, err := m.Array("id")
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if err := c.Dispatcher.Enqueue(ctx, id.Int64(), req.Stage); err != nil {
			return nil, err
		}
	}
	return &v1.RerunRes{Queued: len(ids)}, nil
}
