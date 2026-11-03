package analyze

import (
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"multimo-backend/internal/model/entity"
)

func TestBoxesConvertRelativeCoordinates(t *testing.T) {
	// The bus from VisDrone 0000249_02900_d_0000009 (960x540): model answered
	// [451,605,493,700]; ground truth is x=432 y=327 w=41 h=51.
	got := boxes([]Object{
		{Label: "bus", BBox: []float64{451, 605, 493, 700}},
		{Label: "Car ", BBox: []float64{10, 10, 20, 30}},
		{Label: "unicorn", BBox: []float64{1, 1, 2, 2}},      // unknown label
		{Label: "car", BBox: []float64{5, 5, 5, 400}},        // zero width
		{Label: "car", BBox: []float64{900, 900, 800, 1200}}, // swapped, clamped
	}, 960, 540)
	if len(got) != 3 {
		t.Fatalf("boxes = %+v", got)
	}
	b := got[0]
	if b.Label != "bus" || abs(b.X-433) > 1 || abs(b.Y-326.7) > 1 || abs(b.W-40.3) > 1 || abs(b.H-51.3) > 1 {
		t.Fatalf("bus box = %+v", b)
	}
	if got[1].Label != "car" {
		t.Fatalf("label not normalised: %q", got[1].Label)
	}
	if c := got[2]; c.X != 768 || c.Y+c.H != 540 {
		t.Fatalf("swapped/clamped box = %+v", c)
	}
}

func TestSearchTextAndTags(t *testing.T) {
	r := &Result{Description: "停车场里停满了车。", Scene: []string{"停车场", "城市"}, TimeOfDay: "night",
		View: "nadir", Counts: map[string]int{"car": 40, "bus": 2, "unicorn": 1}, Texts: []string{"万达广场"}}
	tags := tagsOf(r)
	text := searchText(r, tags)
	for _, want := range []string{"停车场", "夜间", "垂直俯视", "小汽车40", "公交车2", "文字：万达广场"} {
		if !strings.Contains(text, want) {
			t.Errorf("search text lacks %q: %s", want, text)
		}
	}
	if strings.Contains(text, "unicorn") {
		t.Error("unknown label leaked into search text")
	}
}

func TestFallbackDescribesDarkFrame(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dark.jpg")
	img := image.NewRGBA(image.Rect(0, 0, 64, 36))
	for i := range img.Pix {
		img.Pix[i] = 20
	}
	img.Set(0, 0, color.RGBA{255, 255, 255, 255})
	f, _ := os.Create(path)
	jpeg.Encode(f, img, nil)
	f.Close()
	lat, lon := 29.7, 115.9
	raw := fallbackJSON(&entity.Asset{Type: "video", Lat: &lat, Lon: &lon}, &kfRow{Width: 64, Height: 36, TsSec: 3}, path)
	var r Result
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatal(err)
	}
	if r.TimeOfDay != "night" || !strings.Contains(r.Description, "夜间") || !strings.Contains(r.Description, "第3.0秒") {
		t.Fatalf("fallback = %+v", r)
	}
}

func TestPGTextArrayEscapes(t *testing.T) {
	if got := pgTextArray([]string{`a"b`, `c\d`, "中文"}); got != `{"a\"b","c\\d","中文"}` {
		t.Fatalf("got %s", got)
	}
}

func TestPromptMentionsEveryLabel(t *testing.T) {
	p := buildPrompt("remote_sensing")
	for l := range Labels {
		if !strings.Contains(p, l) {
			t.Errorf("prompt lacks label %s", l)
		}
	}
	if !strings.Contains(p, "遥感影像") || strings.Contains(p, "{HINT}") {
		t.Error("hint not substituted")
	}
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
