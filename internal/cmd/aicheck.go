package cmd

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png"
	"os"
	"time"

	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/frame/g"
	"github.com/gogf/gf/v2/os/gcmd"

	"multimo-backend/internal/config"
	"multimo-backend/internal/pkg/llm"
)

// AICheck calls each model endpoint once and prints what came back, so an
// operator can confirm keys, model names and network access in one command.
var AICheck = gcmd.Command{
	Name:  "ai-check",
	Usage: "multimo ai-check [-image path.jpg]",
	Brief: "逐个调用 VLM、Embedding、Decision 端点，检查配置是否可用",
	Arguments: []gcmd.Argument{
		{Name: "image", Short: "i", Brief: "用于视觉测试的 JPEG/PNG（默认生成一张测试图）"},
	},
	Func: func(ctx context.Context, parser *gcmd.Parser) error {
		cfg := config.Get()
		var rec llm.Recorder = llm.LogRecorder{}
		if db, err := newAppQuiet(ctx); err == nil {
			rec = llm.DBRecorder{DB: db}
		}
		gw := llm.New(cfg, rec)
		vlm, emb, dec := gw.Models()
		ok := true
		check := func(name, model string, f func() (string, error)) {
			start := time.Now()
			out, err := f()
			status := "OK  "
			if err != nil {
				status, ok = "FAIL", false
				out = err.Error()
			}
			fmt.Printf("%s %-9s %-34s %6dms  %s\n", status, name, model, time.Since(start).Milliseconds(), out)
		}

		check("vlm", vlm, func() (string, error) {
			img, mime, err := checkImage(parser.GetOpt("image").String())
			if err != nil {
				return "", err
			}
			w, h := 0, 0
			if cfg, _, err := image.DecodeConfig(bytes.NewReader(img)); err == nil {
				w, h = cfg.Width, cfg.Height
			}
			var out struct {
				Scene string `json:"scene"`
			}
			resp, err := gw.ChatJSON(ctx, llm.ChatRequest{Purpose: "ai-check", MaxTokens: 200, Messages: []llm.Message{{
				Text:   `用一句话描述这张图，返回 JSON：{"scene": "..."}`,
				Images: []llm.Image{{Data: img, MIME: mime, Width: w, Height: h}},
			}}}, &out)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("%s (tokens %d)", out.Scene, resp.Usage.TotalTokens), nil
		})
		check("embedding", emb, func() (string, error) {
			res, err := gw.Embed(ctx, "ai-check", 0, []string{"无人机航拍的十字路口", "停车场里的汽车"})
			if err != nil {
				return "", err
			}
			if res.Fallback {
				return "", fmt.Errorf("fell back to %s", res.Model)
			}
			return fmt.Sprintf("%d vectors x %d dims", len(res.Vectors), len(res.Vectors[0])), nil
		})
		check("vision", gw.VisionModel(), func() (string, error) {
			img, mime, err := checkImage(parser.GetOpt("image").String())
			if err != nil {
				return "", err
			}
			res, err := gw.EmbedMultimodal(ctx, "ai-check", 0, []llm.MMInput{
				{Image: &llm.Image{Data: img, MIME: mime}}, {Text: "无人机航拍的停车场"}, {Text: "海上的帆船"}})
			if err != nil {
				return "", err
			}
			v := res.Vectors
			return fmt.Sprintf("%d dims; image~停车场 %.3f, image~帆船 %.3f", len(v[0]), llm.Dot(v[0], v[1]), llm.Dot(v[0], v[2])), nil
		})
		check("decision", dec, func() (string, error) {
			res, err := gw.Decide(ctx, llm.DecisionRequest{Purpose: "ai-check",
				State: "无人机画面中，一辆公交车停在路口，周围有三辆小汽车。",
				Questions: map[string]llm.DecisionQuestion{"bus": {Type: "choice",
					Instructions: "画面中是否有公交车？", Criteria: map[string]string{"yes": "有公交车", "no": "没有公交车"}}},
			})
			if err != nil {
				return "", err
			}
			if res.Fallback {
				return "", fmt.Errorf("fell back to local rules")
			}
			a := res.Answers["bus"]
			return fmt.Sprintf("choice=%s confidence=%.3f", a.Choice, a.Confidence), nil
		})
		if !ok {
			return fmt.Errorf("one or more endpoints failed")
		}
		return nil
	},
}

// newAppQuiet connects to the database (if reachable) so ai-check calls are
// recorded in ai_call_log like any other call.
func newAppQuiet(ctx context.Context) (gdb.DB, error) {
	cfg := config.Get()
	if cfg.Database.Password == "" {
		return nil, fmt.Errorf("no database configured")
	}
	if err := setupDatabase(cfg.Database); err != nil {
		return nil, err
	}
	pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	master, err := g.DB().Master()
	if err != nil {
		return nil, err
	}
	if err := master.PingContext(pctx); err != nil {
		return nil, err
	}
	return g.DB(), nil
}

func checkImage(path string) ([]byte, string, error) {
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, "", err
		}
		mime := "image/jpeg"
		if len(b) > 4 && string(b[1:4]) == "PNG" {
			mime = "image/png"
		}
		return b, mime, nil
	}
	// A plain synthetic picture: grey road with a red rectangle.
	img := image.NewRGBA(image.Rect(0, 0, 320, 180))
	for y := 0; y < 180; y++ {
		for x := 0; x < 320; x++ {
			c := color.RGBA{90, 140, 70, 255}
			if y > 70 && y < 110 {
				c = color.RGBA{110, 110, 110, 255}
			}
			if x > 140 && x < 180 && y > 78 && y < 102 {
				c = color.RGBA{200, 30, 30, 255}
			}
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90})
	return buf.Bytes(), "image/jpeg", err
}
