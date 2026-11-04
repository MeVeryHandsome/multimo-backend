package testsuite

import (
	"math"
	"sort"
)

// Groups merge fine-grained classes whose boundaries are fuzzy in aerial
// imagery (car/van, pedestrian/people, bicycle/motor...).
var Groups = map[string]string{
	"car": "vehicle", "van": "vehicle", "truck": "vehicle", "bus": "vehicle",
	"small-vehicle": "vehicle", "large-vehicle": "vehicle",
	"pedestrian": "person", "people": "person",
	"bicycle": "two-wheeler", "motor": "two-wheeler",
	"tricycle": "tricycle", "awning-tricycle": "tricycle",
}

func sameClass(a, b string) bool { return a == b }

func sameGroup(a, b string) bool {
	ga, gb := Groups[a], Groups[b]
	if ga == "" {
		ga = a
	}
	if gb == "" {
		gb = b
	}
	return ga == gb
}

// Pred is a detection in ground-truth image pixels.
type Pred struct {
	Label string
	Box   Box
}

// Match counts for one image or frame.
type Match struct {
	TP, FP, FN, Ignored int
}

func (m *Match) Add(o Match) { m.TP += o.TP; m.FP += o.FP; m.FN += o.FN; m.Ignored += o.Ignored }

// Precision is TP/(TP+FP); ignored detections count for neither.
func (m Match) Precision() float64 { return ratio(m.TP, m.TP+m.FP) }

// Recall is TP/(TP+FN).
func (m Match) Recall() float64 { return ratio(m.TP, m.TP+m.FN) }

// MissRate is FN/(TP+FN).
func (m Match) MissRate() float64 { return ratio(m.FN, m.TP+m.FN) }

// F1 is 2TP/(2TP+FP+FN).
func (m Match) F1() float64 { return ratio(2*m.TP, 2*m.TP+m.FP+m.FN) }

func ratio(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

// IoU of two boxes.
func IoU(a, b Box) float64 {
	ix := math.Max(0, math.Min(a[2], b[2])-math.Max(a[0], b[0]))
	iy := math.Max(0, math.Min(a[3], b[3])-math.Max(a[1], b[1]))
	inter := ix * iy
	union := (a[2]-a[0])*(a[3]-a[1]) + (b[2]-b[0])*(b[3]-b[1]) - inter
	if union <= 0 {
		return 0
	}
	return inter / union
}

func inside(a, region Box) float64 {
	ix := math.Max(0, math.Min(a[2], region[2])-math.Max(a[0], region[0]))
	iy := math.Max(0, math.Min(a[3], region[3])-math.Max(a[1], region[1]))
	area := (a[2] - a[0]) * (a[3] - a[1])
	if area <= 0 {
		return 0
	}
	return ix * iy / area
}

// MatchBoxes pairs predictions with visible ground truth greedily by IoU
// (>= 0.5, compatible class). Unmatched predictions that overlap a
// non-visible object (IoU >= 0.5), lie mostly inside an ignored region, or
// have a label outside the dataset's taxonomy are ignored, following the
// VisDrone/COCO convention; the rest are false positives.
func MatchBoxes(preds []Pred, gt []GTObject, compatible func(a, b string) bool, inTaxonomy func(string) bool) Match {
	type pair struct {
		p, g int
		iou  float64
	}
	var pairs []pair
	for i, p := range preds {
		for j, o := range gt {
			if !o.Visible || o.Region || !compatible(p.Label, o.Label) {
				continue
			}
			if v := IoU(p.Box, o.Box); v >= 0.5 {
				pairs = append(pairs, pair{i, j, v})
			}
		}
	}
	sort.Slice(pairs, func(a, b int) bool { return pairs[a].iou > pairs[b].iou })
	usedP, usedG := map[int]bool{}, map[int]bool{}
	var m Match
	for _, pr := range pairs {
		if usedP[pr.p] || usedG[pr.g] {
			continue
		}
		usedP[pr.p], usedG[pr.g] = true, true
		m.TP++
	}
	for i, p := range preds {
		if usedP[i] {
			continue
		}
		if inTaxonomy != nil && !inTaxonomy(p.Label) {
			m.Ignored++
			continue
		}
		ignored := false
		for _, o := range gt {
			if (o.Region && inside(p.Box, o.Box) >= 0.5) || (!o.Visible && !o.Region && IoU(p.Box, o.Box) >= 0.5) {
				ignored = true
				break
			}
		}
		if ignored {
			m.Ignored++
		} else {
			m.FP++
		}
	}
	for j, o := range gt {
		if o.Visible && !o.Region && !usedG[j] {
			m.FN++
		}
	}
	return m
}

// Stats summarises a list of values.
type Stats struct {
	N      int     `json:"n"`
	Mean   float64 `json:"mean"`
	Median float64 `json:"median"`
	P90    float64 `json:"p90"`
	Max    float64 `json:"max"`
}

func Summarise(v []float64) Stats {
	if len(v) == 0 {
		return Stats{}
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	var sum float64
	for _, x := range s {
		sum += x
	}
	q := func(p float64) float64 { return s[min(len(s)-1, int(math.Ceil(p*float64(len(s))))-1)] }
	med := s[len(s)/2]
	if len(s)%2 == 0 {
		med = (s[len(s)/2-1] + s[len(s)/2]) / 2
	}
	return Stats{N: len(s), Mean: sum / float64(len(s)), Median: med, P90: q(0.9), Max: s[len(s)-1]}
}

func round(x float64, d int) float64 {
	p := math.Pow(10, float64(d))
	return math.Round(x*p) / p
}
