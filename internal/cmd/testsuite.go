package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gogf/gf/v2/frame/g"
	"github.com/gogf/gf/v2/os/gcmd"

	"multimo-backend/internal/consts"
	"multimo-backend/internal/service/ingest"
	"multimo-backend/internal/service/testsuite"
)

// Testsuite runs the benchmark from the command line and writes the JSON,
// HTML and Markdown reports.
var Testsuite = gcmd.Command{
	Name:  "testsuite",
	Usage: "multimo testsuite [-only keyframe,qa] [-out dir] [-register] [-concurrency 4] [-strict] | -render dir",
	Brief: "按 testdata/benchmark 运行评测，输出 test_report.json / .html / .md",
	Arguments: []gcmd.Argument{
		{Name: "only", Short: "o", Brief: "只运行这些部分（逗号分隔）：formats,keyframe,detection,geo,retrieval,qa"},
		{Name: "out", Brief: "报告目录，默认 DATA_DIR/reports/<时间>"},
		{Name: "register", Short: "r", Orphan: true, Brief: "先登记 benchmark/corpus.json 中的素材，并在本进程内处理完队列"},
		{Name: "concurrency", Short: "c", Brief: "检索和问答的并发数，默认 4"},
		{Name: "strict", Orphan: true, Brief: "有核心指标未达标时以退出码 1 结束"},
		{Name: "render", Brief: "不运行评测，只按已保存的 test_report.json 重新生成 HTML 和 Markdown"},
	},
	Func: func(ctx context.Context, parser *gcmd.Parser) error {
		if dir := parser.GetOpt("render").String(); dir != "" {
			rep, err := testsuite.Load(dir)
			if err != nil {
				return err
			}
			if err := rep.Save(dir); err != nil {
				return err
			}
			fmt.Println(filepath.Join(dir, testsuite.HTMLFile))
			return nil
		}
		ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
		app, err := newApp(ctx)
		if err != nil {
			return err
		}
		if parser.GetOpt("register") != nil {
			if err := registerCorpus(ctx, app); err != nil {
				return err
			}
		}
		var only []string
		if v := parser.GetOpt("only").String(); v != "" {
			for _, s := range strings.Split(v, ",") {
				if s = strings.TrimSpace(s); s != "" {
					only = append(only, s)
				}
			}
		}
		out := parser.GetOpt("out").String()
		if out == "" {
			out = filepath.Join(app.ReportsDir(), testsuite.NewRunID(time.Now()))
		}
		suite := app.Suite(parser.GetOpt("concurrency").Int())
		rep := suite.Run(ctx, only, func(p string) { fmt.Fprintf(os.Stderr, "\r\033[K%s", p) })
		fmt.Fprintln(os.Stderr)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := rep.Save(out); err != nil {
			return err
		}
		printSummary(rep)
		fmt.Printf("\n报告：%s\n", filepath.Join(out, testsuite.HTMLFile))
		if parser.GetOpt("strict") != nil && !rep.AllPassed() {
			os.Exit(1)
		}
		return nil
	},
}

func printSummary(r *testsuite.Report) {
	for k, v := range r.Errors {
		fmt.Printf("ERROR %-10s %s\n", k, v)
	}
	for _, m := range r.Metrics {
		v := "参考  "
		if m.Primary {
			v = map[bool]string{true: "PASS  ", false: "FAIL  "}[m.Pass]
		}
		fmt.Printf("%s %-24s %-28s 目标 %s\n", v, m.Name, m.Display, m.Target)
	}
}

// Suite builds the benchmark runner on top of the app's services.
func (a *App) Suite(concurrency int) *testsuite.Suite {
	td, _ := filepath.Abs(a.Cfg.Storage.TestdataDir)
	return &testsuite.Suite{DB: a.DB, AI: a.AI, Search: a.Search, QA: a.QA, TestdataDir: td,
		Version: consts.Version, Concurrency: concurrency}
}

func (a *App) ReportsDir() string { return filepath.Join(a.Assets.DataDir, "reports") }

// registerCorpus registers the evaluation corpus and processes the queue in
// this process until nothing is pending (a running server shares the queue
// safely, tasks are claimed with SKIP LOCKED).
func registerCorpus(ctx context.Context, app *App) error {
	b, err := os.ReadFile(filepath.Join(app.Suite(0).TestdataDir, "benchmark", "corpus.json"))
	if err != nil {
		return fmt.Errorf("corpus.json: %w", err)
	}
	var corpus struct {
		Assets []ingest.RegisterItem `json:"assets"`
	}
	if err := json.Unmarshal(b, &corpus); err != nil {
		return fmt.Errorf("corpus.json: %w", err)
	}
	created, failed := 0, 0
	for _, r := range app.Ingest.Register(ctx, corpus.Assets) {
		if r.Error != "" {
			failed++
			fmt.Fprintf(os.Stderr, "register %s: %s\n", r.Path, r.Error)
		} else if r.Created {
			created++
		}
	}
	fmt.Fprintf(os.Stderr, "corpus: %d assets, %d new, %d failed\n", len(corpus.Assets), created, failed)

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { app.Dispatcher.Run(runCtx); close(done) }()
	defer func() { cancel(); <-done }()
	start := time.Now()
	for {
		counts := map[string]int{}
		rows, err := app.DB.GetAll(ctx, `SELECT status, count(*) AS n FROM tasks WHERE status IN ('pending','running') GROUP BY status`)
		if err != nil {
			return err
		}
		for _, r := range rows {
			counts[r["status"].String()] = r["n"].Int()
		}
		if counts["pending"]+counts["running"] == 0 {
			fmt.Fprintf(os.Stderr, "\r\033[Kqueue empty after %s\n", time.Since(start).Round(time.Second))
			return nil
		}
		fmt.Fprintf(os.Stderr, "\r\033[Kprocessing: %d pending, %d running (%s)", counts["pending"], counts["running"], time.Since(start).Round(time.Second))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
		g.Log().Debug(ctx, "waiting for queue")
	}
}
