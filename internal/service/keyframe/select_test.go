package keyframe

import (
	"context"
	"testing"

	"multimo-backend/internal/pkg/media"
	"multimo-backend/internal/testutil"
)

func sig(t float64, v byte, stripe int) media.Signature {
	p := make([]byte, media.SigW*media.SigH)
	for i := range p {
		p[i] = v
		if (i%media.SigW)/8 == stripe {
			p[i] = 255 - v
		}
	}
	return media.Signature{Time: t, Pix: p}
}

func TestSelectRules(t *testing.T) {
	var sigs []media.Signature
	for i := 0; i < 80; i++ { // 40 s at 2 fps; content changes at 10 s, then static
		stripe := 0
		if i >= 20 {
			stripe = 4
		}
		sigs = append(sigs, sig(float64(i)/2, 40, stripe))
	}
	picks := Select(sigs, Params{Threshold: 0.08, MinGap: 2, MaxGap: 15, MaxN: 60})
	want := []struct {
		t      float64
		reason string
	}{{0, "first"}, {10, "change"}, {25, "coverage"}}
	if len(picks) != len(want) {
		t.Fatalf("picks = %+v", picks)
	}
	for i, w := range want {
		if picks[i].Time != w.t || picks[i].Reason != w.reason {
			t.Fatalf("pick %d = %+v, want %+v", i, picks[i], w)
		}
	}
}

func TestSelectCapsEvenly(t *testing.T) {
	var sigs []media.Signature
	for i := 0; i < 400; i++ { // every frame differs strongly
		sigs = append(sigs, sig(float64(i)/2, byte(i%2*200), i%8))
	}
	picks := Select(sigs, Params{Threshold: 0.01, MinGap: 2, MaxGap: 15, MaxN: 10})
	if len(picks) != 10 || picks[0].Time != 0 || picks[9].Time < 190 {
		t.Fatalf("picks = %+v", picks)
	}
}

func TestSelectRealVideos(t *testing.T) {
	cases := map[string][2]int{ // min, max keyframes
		"video/visdrone_vid_val/mp4/uav0000086_00000_v.mp4":            {3, 10},
		"video/uavdt/mp4/M1004.mp4":                                    {2, 7},
		"video/dji/palos_verdes/drone_palos_verdes_20250217174636.mp4": {30, 60},
	}
	for rel, rng := range cases {
		sigs, err := Signatures(context.Background(), testutil.Testdata(t, rel), 4)
		if err != nil {
			t.Fatal(err)
		}
		picks := Select(sigs, Params{Threshold: 0.08, MinGap: 2, MaxGap: 15, MaxN: 60})
		if len(picks) < rng[0] || len(picks) > rng[1] {
			t.Errorf("%s: %d keyframes, want %d..%d", rel, len(picks), rng[0], rng[1])
		}
	}
}
