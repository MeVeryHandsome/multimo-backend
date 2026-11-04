package testsuite

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/gogf/gf/v2/errors/gcode"
	"github.com/gogf/gf/v2/errors/gerror"
	"github.com/gogf/gf/v2/frame/g"

	v1 "multimo-backend/api/testsuite/v1"
	svc "multimo-backend/internal/service/testsuite"
)

type ControllerV1 struct {
	Runner *svc.Runner
}

func (c *ControllerV1) Run(ctx context.Context, req *v1.RunReq) (*v1.RunRes, error) {
	st, err := c.Runner.Start(req.Sections)
	if errors.Is(err, svc.ErrRunning) {
		return nil, gerror.NewCode(gcode.CodeOperationFailed, err.Error())
	}
	return &v1.RunRes{Status: st}, err
}

func (c *ControllerV1) Status(ctx context.Context, req *v1.StatusReq) (*v1.StatusRes, error) {
	return &v1.StatusRes{Status: c.Runner.Status()}, nil
}

func (c *ControllerV1) List(ctx context.Context, req *v1.ListReq) (*v1.ListRes, error) {
	return &v1.ListRes{List: c.Runner.List()}, nil
}

func (c *ControllerV1) Report(ctx context.Context, req *v1.ReportReq) (*v1.ReportRes, error) {
	dir, err := c.Runner.RunDir(req.Id)
	if err != nil {
		return nil, gerror.NewCode(gcode.CodeNotFound, "没有这份报告")
	}
	rep, err := svc.Load(dir)
	if err != nil {
		return nil, gerror.WrapCode(gcode.CodeInternalError, err, "读取报告失败")
	}
	return &v1.ReportRes{Report: rep}, nil
}

func (c *ControllerV1) ReportFile(ctx context.Context, req *v1.ReportFileReq) (*v1.ReportFileRes, error) {
	dir, err := c.Runner.RunDir(req.Id)
	if err != nil {
		return nil, gerror.NewCode(gcode.CodeNotFound, "没有这份报告")
	}
	name, mime := svc.HTMLFile, "text/html; charset=utf-8"
	switch req.File {
	case "md":
		name, mime = svc.MarkdownFile, "text/markdown; charset=utf-8"
	case "json":
		name, mime = svc.JSONFile, "application/json"
	}
	r := g.RequestFromCtx(ctx)
	r.Response.Header().Set("Content-Type", mime)
	r.Response.ServeFile(filepath.Join(dir, name))
	return nil, nil
}
