package cmd

import (
	"context"
	"strconv"

	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/frame/g"
	"github.com/gogf/gf/v2/net/ghttp"
	"github.com/gogf/gf/v2/os/gcmd"

	"multimo-backend/internal/config"
	"multimo-backend/internal/consts"
	"multimo-backend/internal/controller/system"
)

var Main = gcmd.Command{
	Name:  consts.AppName,
	Usage: "multimo [command]",
	Brief: "无人机视频智能分析服务。不带子命令时启动 HTTP 服务",
	Func: func(ctx context.Context, parser *gcmd.Parser) error {
		return runHTTP(ctx)
	},
}

func runHTTP(ctx context.Context) error {
	cfg := config.Get()
	if err := setupDatabase(cfg.Database); err != nil {
		return err
	}
	if missing := cfg.Missing(); len(missing) > 0 {
		g.Log().Warningf(ctx, "missing env vars, related features will degrade: %v", missing)
	}

	s := g.Server()
	s.SetAddr(cfg.HTTPAddr)
	s.SetClientMaxBodySize(int64(cfg.Storage.MaxUploadMB) << 20)
	s.SetOpenApiPath("/api.json")
	s.SetSwaggerPath("/swagger")
	oai := s.GetOpenApi()
	oai.Info.Title = "Multimo API"
	oai.Info.Description = "无人机视频智能分析系统接口"
	oai.Info.Version = consts.Version

	s.Group("/api/v1", func(group *ghttp.RouterGroup) {
		group.Middleware(ghttp.MiddlewareCORS, ghttp.MiddlewareHandlerResponse)
		group.Bind(system.NewV1())
	})
	g.Log().Infof(ctx, "multimo %s listening on %s, db %s", consts.Version, cfg.HTTPAddr, cfg.Database)
	s.Run()
	return nil
}

// setupDatabase registers the default DB node from environment variables, so
// credentials never need to live in config.yaml.
func setupDatabase(d config.Database) error {
	return gdb.SetConfig(gdb.Config{
		gdb.DefaultGroupName: gdb.ConfigGroup{{
			Type:             "pgsql",
			Host:             d.Host,
			Port:             strconv.Itoa(d.Port),
			User:             d.User,
			Pass:             d.Password,
			Name:             d.Name,
			Extra:            "sslmode=" + d.SSLMode,
			Role:             "master",
			Weight:           100,
			MaxIdleConnCount: 4,
			MaxOpenConnCount: 32,
		}},
	})
}
