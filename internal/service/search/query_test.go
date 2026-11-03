package search

import (
	"testing"
	"time"
)

func TestParseFiltersBenchmarkQueries(t *testing.T) {
	f := ParseFilters("以北纬29.7253°、东经115.9748°为中心，半径300米内拍摄的航拍图像")
	if f.Lat == nil || *f.Lat != 29.7253 || *f.Lon != 115.9748 || f.RadiusM != 300 {
		t.Fatalf("circle: %+v", f)
	}
	f = ParseFilters("北纬33.73°、西经118.33°附近 2 公里内拍摄的视频")
	if f.Lat == nil || *f.Lat != 33.73 || *f.Lon != -118.33 || f.RadiusM != 2000 {
		t.Fatalf("west + km: %+v", f)
	}
	f = ParseFilters("纬度在北纬29.7212°到29.7373°之间拍摄的航拍图像")
	if f.South == nil || *f.South != 29.7212 || *f.North != 29.7373 || f.Lat != nil {
		t.Fatalf("lat range: %+v", f)
	}
	f = ParseFilters("东经115.9893°以东拍摄的航拍图像")
	if f.West == nil || *f.West != 115.9893 || f.East != nil {
		t.Fatalf("east of: %+v", f)
	}
	f = ParseFilters("2025年2月拍摄的无人机视频")
	if !f.From.Equal(time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)) || !f.To.Equal(time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("month: %v %v", f.From, f.To)
	}
	f = ParseFilters("2018年9月17日13:00到13:10之间拍摄的航拍图像")
	if !f.From.Equal(time.Date(2018, 9, 17, 13, 0, 0, 0, time.UTC)) || !f.To.Equal(time.Date(2018, 9, 17, 13, 10, 0, 0, time.UTC)) {
		t.Fatalf("time window: %v %v", f.From, f.To)
	}
	f = ParseFilters("申请远程驾驶员证书的年龄要求")
	if f.Spatial() || f.Temporal() {
		t.Fatalf("plain query got filters: %+v", f)
	}
	f = ParseFilters("2023年发布的无人机法规")
	if f.Temporal() {
		t.Fatal("a bare year about a regulation must not become a capture-time filter")
	}
	f = ParseFilters("33.73 N, 118.33 W 附近")
	if f.Lat == nil || *f.Lon != -118.33 || f.RadiusM != 5000 {
		t.Fatalf("english coords: %+v", f)
	}
}

func TestMayNamePlace(t *testing.T) {
	if !MayNamePlace("美国加利福尼亚州帕洛斯弗迪斯（Palos Verdes）一带拍摄的视频") {
		t.Fatal("place query not detected")
	}
	if MayNamePlace("有船只的遥感影像") {
		t.Fatal("false place cue")
	}
	for _, q := range []string{"中国无人驾驶航空器管制空域如何划设", "美国小型无人机最大飞行高度限制", "英国 PDRA01 运行手册模板"} {
		if MayNamePlace(q) {
			t.Errorf("%s: a jurisdiction is not a capture location", q)
		}
	}
}
