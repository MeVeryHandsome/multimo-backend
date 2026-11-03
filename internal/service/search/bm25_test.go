package search

import (
	"math"
	"testing"
)

func TestBM25RanksByRelevance(t *testing.T) {
	units := []Unit{{Kind: "keyframe", ID: 1}, {Kind: "keyframe", ID: 2}, {Kind: "keyframe", ID: 3}, {Kind: "chunk", ID: 4}}
	texts := []string{
		"城市十字路口，一辆绿色公交车在等红灯，周围有小汽车。",
		"停车场里停满了小汽车。",
		"港口停靠着大量船只。",
		"The altitude of the small unmanned aircraft cannot be higher than 400 feet above ground level.",
	}
	ix := NewBM25(units, texts)
	if got := ix.Search("公交车", 3, nil); len(got) == 0 || got[0].Unit.ID != 1 {
		t.Fatalf("公交车 -> %+v", got)
	}
	if got := ix.Search("停满汽车的停车场", 3, nil); got[0].Unit.ID != 2 {
		t.Fatalf("停车场 -> %+v", got)
	}
	if got := ix.Search("maximum altitude 400 feet", 3, nil); len(got) != 1 || got[0].Unit.ID != 4 {
		t.Fatalf("english -> %+v", got)
	}
	keepChunks := func(u Unit) bool { return u.Kind == "chunk" }
	if got := ix.Search("公交车", 3, keepChunks); len(got) != 0 {
		t.Fatalf("filter ignored: %+v", got)
	}
	if got := ix.Search("飞机", 3, nil); len(got) != 0 {
		t.Fatalf("unrelated query matched: %+v", got)
	}
}

func TestBM25EmptyIndex(t *testing.T) {
	var ix *BM25
	if ix.Search("x", 5, nil) != nil {
		t.Fatal("nil index should return nothing")
	}
	_ = math.NaN()
}
