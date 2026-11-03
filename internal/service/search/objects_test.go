package search

import (
	"reflect"
	"testing"
)

func TestParseObjects(t *testing.T) {
	cases := []struct {
		q      string
		labels []string
		min    int
		absent bool
	}{
		{"街道上有公交车的航拍图像", []string{"bus"}, 0, false},
		{"行人密集的场景（20 人以上）", []string{"pedestrian"}, 20, false},
		{"出现带篷三轮车的路口或街道", []string{"awning-tricycle"}, 0, false},
		{"几乎看不到小汽车的画面", []string{"car", "small-vehicle"}, 0, true},
		{"停有大量车辆（20 辆以上）的遥感影像", []string{"car", "van", "truck", "bus", "small-vehicle", "large-vehicle"}, 20, false},
		{"有船只的遥感影像", []string{"ship"}, 0, false},
	}
	for _, c := range cases {
		oq := ParseObjects(c.q)
		if oq == nil || !reflect.DeepEqual(oq.Labels, c.labels) || oq.Min != c.min || oq.Absent != c.absent {
			t.Errorf("%s -> %+v", c.q, oq)
		}
	}
	if ParseObjects("申请远程驾驶员证书的年龄要求") != nil {
		t.Error("no object words expected")
	}
}

func TestScoreCounts(t *testing.T) {
	ped := &ObjectQuery{Labels: []string{"pedestrian"}, Min: 20}
	if _, ok := ped.scoreCounts(`{"pedestrian": 12}`); ok {
		t.Error("12 < 20 should fail")
	}
	if s, ok := ped.scoreCounts(`{"pedestrian": 30, "car": 2}`); !ok || s <= 0 {
		t.Error("30 >= 20 should pass")
	}
	none := &ObjectQuery{Labels: []string{"car"}, Absent: true}
	if _, ok := none.scoreCounts(`{"car": 40}`); ok {
		t.Error("40 cars is not 'almost none'")
	}
	if _, ok := none.scoreCounts(`{"pedestrian": 5}`); !ok {
		t.Error("no cars should pass the absence query")
	}
}
