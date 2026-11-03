package search

import (
	"encoding/json"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Object words in queries, mapped to detection labels. Longer phrases are
// listed so they win over their substrings (带篷三轮车 before 三轮车).
var objectWords = []struct {
	word   string
	labels []string
}{
	{"带篷三轮车", []string{"awning-tricycle"}}, {"三轮车", []string{"tricycle", "awning-tricycle"}},
	{"公交车", []string{"bus"}}, {"大巴", []string{"bus"}}, {"巴士", []string{"bus"}},
	{"面包车", []string{"van"}}, {"卡车", []string{"truck", "large-vehicle"}}, {"货车", []string{"truck", "large-vehicle"}},
	{"大型车辆", []string{"large-vehicle", "truck", "bus"}}, {"小汽车", []string{"car", "small-vehicle"}},
	{"轿车", []string{"car", "small-vehicle"}}, {"汽车", []string{"car", "small-vehicle", "van"}},
	{"车辆", []string{"car", "van", "truck", "bus", "small-vehicle", "large-vehicle"}},
	{"摩托车", []string{"motor"}}, {"电动车", []string{"motor"}}, {"自行车", []string{"bicycle"}}, {"骑车", []string{"bicycle"}},
	{"行人", []string{"pedestrian"}}, {"人群", []string{"people", "pedestrian"}}, {"非行走人员", []string{"people"}},
	{"船只", []string{"ship"}}, {"船", []string{"ship"}}, {"港口", []string{"harbor"}}, {"码头", []string{"harbor"}},
	{"桥梁", []string{"bridge"}}, {"桥", []string{"bridge"}}, {"游泳池", []string{"swimming-pool"}},
	{"网球场", []string{"tennis-court"}}, {"篮球场", []string{"basketball-court"}}, {"棒球场", []string{"baseball-diamond"}},
	{"足球场", []string{"soccer-ball-field"}}, {"田径场", []string{"ground-track-field"}}, {"操场", []string{"ground-track-field"}},
	{"储油罐", []string{"storage-tank"}}, {"环岛", []string{"roundabout"}}, {"机场", []string{"airport"}},
	{"飞机", []string{"plane"}}, {"直升机", []string{"helicopter"}}, {"停机坪", []string{"helipad"}},
	{"集装箱起重机", []string{"container-crane"}},
}

var (
	reAtLeast = regexp.MustCompile(`(\d+)\s*(?:辆|人|艘|个|架|座|处)?\s*(?:以上|多|及以上|或更多)`)
	reAbsent  = regexp.MustCompile(`没有|无|几乎看不到|看不到|很少|极少`)
)

// ObjectQuery is the structured object intent of a query.
type ObjectQuery struct {
	Labels []string `json:"labels"`
	Min    int      `json:"min,omitempty"` // at least this many in total (0: any)
	Absent bool     `json:"absent,omitempty"`
}

// ParseObjects finds object words, a count threshold ("20 人以上") and
// negation ("几乎看不到小汽车").
func ParseObjects(q string) *ObjectQuery {
	rest := q
	seen := map[string]bool{}
	var labels []string
	firstAt := -1
	for _, w := range objectWords {
		i := strings.Index(rest, w.word)
		if i < 0 {
			continue
		}
		if firstAt < 0 || i < firstAt {
			firstAt = i
		}
		rest = strings.ReplaceAll(rest, w.word, " ")
		for _, l := range w.labels {
			if !seen[l] {
				seen[l] = true
				labels = append(labels, l)
			}
		}
	}
	if len(labels) == 0 {
		return nil
	}
	oq := &ObjectQuery{Labels: labels}
	if m := reAtLeast.FindStringSubmatch(q); m != nil {
		oq.Min, _ = strconv.Atoi(m[1])
	}
	if loc := reAbsent.FindStringIndex(q); loc != nil && loc[0] <= firstAt {
		oq.Absent = true
	}
	return oq
}

// scoreCounts ranks a keyframe by the VLM's per-class counts. It returns
// false when the keyframe does not meet the query's constraint.
func (oq *ObjectQuery) scoreCounts(raw string) (float64, bool) {
	var counts map[string]float64
	if raw == "" || json.Unmarshal([]byte(raw), &counts) != nil {
		counts = nil
	}
	total := 0.0
	for _, l := range oq.Labels {
		total += counts[l]
	}
	switch {
	case oq.Absent:
		if total > 2 {
			return 0, false
		}
		return 1 / (1 + total), true
	case total <= 0 || (oq.Min > 0 && total < float64(oq.Min)):
		return 0, false
	}
	return math.Log1p(total), true
}

// rankByObjects orders candidate keyframes by object counts.
func rankByObjects(oq *ObjectQuery, rows []objRow) []Hit {
	var hits []Hit
	for _, r := range rows {
		if s, ok := oq.scoreCounts(r.counts); ok {
			hits = append(hits, Hit{Unit: Unit{Kind: "keyframe", ID: r.id, AssetID: r.assetID}, Score: s})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > listDepth {
		hits = hits[:listDepth]
	}
	return hits
}

type objRow struct {
	id, assetID int64
	counts      string
}
