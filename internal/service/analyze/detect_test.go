package analyze

import (
	"strings"
	"testing"

	"multimo-backend/internal/pkg/llm"
)

func TestDetectorBoxesScaleToKeyframe(t *testing.T) {
	r := &llm.DetectResult{Width: 2560, Height: 1440, Objects: []llm.Detection{
		{Label: "car", Score: 0.9, Box: [4]float64{256, 144, 512, 288}},
		{Label: "others", Score: 0.9, Box: [4]float64{0, 0, 100, 100}},      // not in the vocabulary
		{Label: "bus", Score: 0.8, Box: [4]float64{2550, 1430, 2700, 1500}}, // clamped to the frame
	}}
	got := detectorBoxes(r, 1280, 720)
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	if b := got[0]; b.X != 128 || b.Y != 72 || b.W != 128 || b.H != 72 || b.Score != 0.9 {
		t.Fatalf("scaled box %+v", b)
	}
	if b := got[1]; b.X+b.W > 1280 || b.Y+b.H > 720 {
		t.Fatalf("box not clamped %+v", b)
	}
}

func TestMergeCountsPrefersDetector(t *testing.T) {
	got := mergeCounts(map[string]int{"car": 40, "ship": 3, "van": 2}, map[string]int{"car": 72, "motor": 19})
	if got["car"] != 72 || got["motor"] != 19 || got["ship"] != 3 || got["van"] != 0 {
		t.Fatalf("got %v", got) // van: the detector saw none, so the VLM's guess is dropped
	}
}

func TestPromptWithDetections(t *testing.T) {
	p := buildPromptWithDetections("image", map[string]int{"car": 72, "motor": 19})
	if strings.Contains(p, "bbox_2d") || strings.Contains(p, `"objects"`) {
		t.Fatal("the VLM must not be asked for boxes when the detector drew them")
	}
	if !strings.Contains(p, "小汽车（car）72") || !strings.Contains(p, "摩托车（motor）19") || !strings.Contains(p, "ship") {
		t.Fatalf("detector counts or the remaining vocabulary missing:\n%s", p)
	}
	if strings.Contains(buildPromptWithDetections("video", nil), "检测结果：没有检测到目标") == false {
		t.Fatal("empty detection result should be stated")
	}
}
