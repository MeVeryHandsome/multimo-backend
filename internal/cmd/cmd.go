package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/frame/g"
	"github.com/gogf/gf/v2/net/ghttp"
	"github.com/gogf/gf/v2/os/gcmd"

	"multimo-backend/internal/config"
	"multimo-backend/internal/consts"
	assetctl "multimo-backend/internal/controller/asset"
	kfctl "multimo-backend/internal/controller/keyframe"
	qactl "multimo-backend/internal/controller/qa"
	searchctl "multimo-backend/internal/controller/search"
	"multimo-backend/internal/controller/system"
	taskctl "multimo-backend/internal/controller/task"
	"multimo-backend/internal/model/entity"
	"multimo-backend/internal/pkg/llm"
	"multimo-backend/internal/pkg/migrate"
	"multimo-backend/internal/pkg/workerpool"
	"multimo-backend/internal/service/analyze"
	"multimo-backend/internal/service/asset"
	"multimo-backend/internal/service/geo"
	"multimo-backend/internal/service/ingest"
	"multimo-backend/internal/service/keyframe"
	"multimo-backend/internal/service/pipeline"
	"multimo-backend/internal/service/qa"
	"multimo-backend/internal/service/search"
	"multimo-backend/manifest"
)

var Main = gcmd.Command{
	Name:  consts.AppName,
	Usage: "multimo [ai-check]",
	Brief: "无人机视频智能分析服务。不带子命令时启动 HTTP 服务和处理队列",
	Func: func(ctx context.Context, parser *gcmd.Parser) error {
		return runHTTP(ctx)
	},
}

func init() {
	if err := Main.AddCommand(&AICheck); err != nil {
		panic(err)
	}
}

// App holds the long-lived services shared by HTTP handlers and workers.
type App struct {
	Cfg        *config.Config
	DB         gdb.DB
	AI         *llm.Gateway
	Assets     *asset.Service
	Ingest     *ingest.Service
	Dispatcher *pipeline.Dispatcher
	Search     *search.Service
	QA         *qa.Service
}

func newApp(ctx context.Context) (*App, error) {
	cfg := config.Get()
	if err := setupDatabase(cfg.Database); err != nil {
		return nil, err
	}
	db := g.DB()
	if err := migrate.WaitAndUp(ctx, db, manifest.Migrations,
		migrate.Vars{"EMBEDDING_DIM": cfg.Embedding.Dim, "VISION_EMBEDDING_DIM": cfg.Vision.Dim}, 90*time.Second); err != nil {
		return nil, fmt.Errorf("database: %w", err)
	}
	dataDir, err := filepath.Abs(cfg.Storage.DataDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("DATA_DIR: %w", err)
	}
	testdata, _ := filepath.Abs(cfg.Storage.TestdataDir)

	app := &App{Cfg: cfg, DB: db}
	app.AI = llm.New(cfg, llm.DBRecorder{DB: db})
	app.Assets = &asset.Service{DB: db, DataDir: dataDir}
	app.Dispatcher = pipeline.New(db, workerpool.New(cfg.Video.WorkerPoolSize))
	app.Ingest = &ingest.Service{Assets: app.Assets, DB: db, Dispatcher: app.Dispatcher,
		TestdataDir: testdata, FFmpegThreads: cfg.Video.FFmpegThreads}
	video := cfg.Video
	kf := &keyframe.Service{Assets: app.Assets, DB: db, Dispatcher: app.Dispatcher, AI: app.AI, Video: video}
	an := &analyze.Service{Assets: app.Assets, DB: db, Dispatcher: app.Dispatcher, AI: app.AI,
		Concurrency: cfg.VLM.MaxConcurrency}
	gs := &geo.Service{Assets: app.Assets, DB: db, Dispatcher: app.Dispatcher, AI: app.AI, Geo: cfg.Geo}

	// ingest -> keyframes -> analyze -> geo; basemaps also get a tile index.
	app.Search = &search.Service{DB: db, AI: app.AI, Geo: gs}
	app.QA = &qa.Service{DB: db, AI: app.AI, Assets: app.Assets, Search: app.Search}
	app.Ingest.Next = func(a *entity.Asset) []string {
		if a.Type == "document" {
			return []string{search.IndexKind}
		}
		return []string{keyframe.Kind}
	}
	kf.Next = func(a *entity.Asset) []string {
		if geo.IsBasemap(a) {
			return []string{analyze.Kind, geo.BasemapKind}
		}
		return []string{analyze.Kind}
	}
	an.Next = func(a *entity.Asset) []string { return []string{geo.Kind} }

	app.Dispatcher.Register(ingest.Kind, app.Ingest.Handle)
	app.Dispatcher.Register(keyframe.Kind, kf.Handle)
	app.Dispatcher.Register(analyze.Kind, an.Handle)
	app.Dispatcher.Register(geo.Kind, gs.Handle)
	app.Dispatcher.Register(geo.BasemapKind, gs.HandleBasemap)
	app.Dispatcher.Register(search.IndexKind, app.Search.HandleIndex)
	return app, nil
}

func runHTTP(ctx context.Context) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	app, err := newApp(ctx)
	if err != nil {
		return err
	}
	cfg := app.Cfg
	if missing := cfg.Missing(); len(missing) > 0 {
		g.Log().Warningf(ctx, "missing env vars, related features will degrade: %v", missing)
	}
	if n, err := app.Dispatcher.RecoverStale(ctx); err == nil && n > 0 {
		g.Log().Infof(ctx, "requeued %d tasks interrupted by the previous run", n)
	}
	workersDone := make(chan struct{})
	go func() {
		app.Dispatcher.Run(ctx)
		close(workersDone)
	}()

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
		group.Bind(
			system.NewV1(),
			&assetctl.ControllerV1{Assets: app.Assets, Ingest: app.Ingest, Dispatcher: app.Dispatcher},
			&taskctl.ControllerV1{DB: app.DB},
			&kfctl.ControllerV1{DB: app.DB, Assets: app.Assets, Dispatcher: app.Dispatcher},
			&searchctl.ControllerV1{Svc: app.Search},
			&qactl.ControllerV1{Svc: app.QA},
		)
	})
	g.Log().Infof(ctx, "multimo %s listening on %s, db %s, data %s, workers %d",
		consts.Version, cfg.HTTPAddr, cfg.Database, app.Assets.DataDir, cfg.Video.WorkerPoolSize)
	if err := s.Start(); err != nil {
		return err
	}
	<-ctx.Done()
	g.Log().Info(context.Background(), "shutting down: waiting for running tasks")
	s.Shutdown()
	<-workersDone
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
