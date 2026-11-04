package testsuite

import (
	"math"
	"testing"

	"multimo-backend/internal/testutil"
)

func TestMatchBoxesIgnoreProtocol(t *testing.T) {
	gt := []GTObject{
		{Label: "car", Box: Box{0, 0, 40, 40}, Visible: true},
		{Label: "bus", Box: Box{100, 100, 160, 160}, Visible: true},
		{Label: "pedestrian", Box: Box{200, 200, 210, 215}, Visible: false}, // tiny: not counted
		{Label: "ignored", Box: Box{300, 300, 400, 400}, Region: true},
	}
	preds := []Pred{
		{Label: "car", Box: Box{2, 2, 40, 41}},              // TP
		{Label: "van", Box: Box{100, 100, 158, 160}},        // wrong class (exact), right group
		{Label: "pedestrian", Box: Box{200, 200, 210, 214}}, // matches a hidden object: ignored
		{Label: "car", Box: Box{310, 310, 330, 330}},        // inside ignored region
		{Label: "truck", Box: Box{500, 500, 540, 540}},      // nothing there: FP
	}
	exact := MatchBoxes(preds, gt, sameClass, nil)
	if exact != (Match{TP: 1, FP: 2, FN: 1, Ignored: 2}) {
		t.Fatalf("exact = %+v", exact)
	}
	group := MatchBoxes(preds, gt, sameGroup, nil)
	if group != (Match{TP: 2, FP: 1, FN: 0, Ignored: 2}) {
		t.Fatalf("group = %+v", group)
	}
	vehiclesOnly := func(l string) bool { return Groups[l] == "vehicle" }
	if m := MatchBoxes(preds, gt, sameGroup, vehiclesOnly); m.Ignored != 2 || m.FP != 1 {
		t.Fatalf("taxonomy filter = %+v", m)
	}
	if math.Abs(group.Precision()-2.0/3) > 1e-9 || group.MissRate() != 0 {
		t.Fatalf("precision %f miss %f", group.Precision(), group.MissRate())
	}
}

func TestParseAnnotations(t *testing.T) {
	det, err := ParseVisDroneDET(testutil.Testdata(t, "image/visdrone_det_val/annotations/0000249_02900_d_0000009.txt"))
	if err != nil {
		t.Fatal(err)
	}
	buses := 0
	for _, o := range det {
		if o.Label == "bus" && o.Visible {
			buses++
		}
	}
	if buses != 2 { // the third bus is 9x10 px: not visible
		t.Fatalf("visible buses = %d", buses)
	}
	vid, err := ParseVisDroneVID(testutil.Testdata(t, "video/visdrone_vid_val/annotations/uav0000305_00000_v.txt"))
	if err != nil || len(vid[1]) == 0 {
		t.Fatalf("frame 1: %d objects, err %v", len(vid[1]), err)
	}
	ua, err := ParseUAVDT(testutil.Testdata(t, "video/uavdt/GT/M1004_gt_whole.txt"))
	if err != nil || len(ua[1]) == 0 {
		t.Fatalf("uavdt frame 1: %d objects, err %v", len(ua[1]), err)
	}
	n := len(ua[1])
	if err := AddUAVDTIgnore(ua, testutil.Testdata(t, "video/uavdt/GT/M1004_gt_whole.txt")); err != nil {
		t.Fatal(err)
	}
	// M1004 frame 1 has three ignore regions, the first at x=450 y=0 177x166.
	if len(ua[1]) != n+3 || !ua[1][n].Region || ua[1][n].Box != (Box{450, 0, 627, 166}) {
		t.Fatalf("uavdt ignore region not added: %+v", ua[1][n:])
	}
}

func TestSummarise(t *testing.T) {
	s := Summarise([]float64{5, 1, 3, 2, 4})
	if s.Median != 3 || s.Mean != 3 || s.Max != 5 || s.P90 != 5 {
		t.Fatalf("%+v", s)
	}
}
