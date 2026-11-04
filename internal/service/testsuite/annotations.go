package testsuite

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// Box is x1, y1, x2, y2 in image pixels.
type Box [4]float64

// GTObject is one ground-truth annotation.
type GTObject struct {
	Label   string
	Box     Box
	Visible bool // big enough and not heavily occluded: counted
	Region  bool // "ignored region": detections inside are not penalised
}

// The visibility rule matches scripts/lib/build_benchmark.py, which built
// the retrieval and QA references from the same annotations.
const minVisibleArea = 600

var visdroneClasses = map[int]string{1: "pedestrian", 2: "people", 3: "bicycle", 4: "car", 5: "van", 6: "truck",
	7: "tricycle", 8: "awning-tricycle", 9: "bus", 10: "motor"}

var uavdtClasses = map[int]string{1: "car", 2: "truck", 3: "bus"}

// ParseVisDroneDET reads "x,y,w,h,score,category,truncation,occlusion".
func ParseVisDroneDET(path string) ([]GTObject, error) {
	var out []GTObject
	err := eachCSVLine(path, func(v []int) {
		if len(v) < 8 {
			return
		}
		out = append(out, visdroneObject(v[0], v[1], v[2], v[3], v[5], v[7]))
	})
	return out, err
}

// ParseVisDroneVID reads "frame,target,x,y,w,h,score,category,truncation,occlusion"
// into objects per frame (1-based frame index).
func ParseVisDroneVID(path string) (map[int][]GTObject, error) {
	out := map[int][]GTObject{}
	err := eachCSVLine(path, func(v []int) {
		if len(v) < 10 {
			return
		}
		out[v[0]] = append(out[v[0]], visdroneObject(v[2], v[3], v[4], v[5], v[7], v[9]))
	})
	return out, err
}

func visdroneObject(x, y, w, h, cat, occ int) GTObject {
	b := Box{float64(x), float64(y), float64(x + w), float64(y + h)}
	label, ok := visdroneClasses[cat]
	if !ok { // 0 ignored region, 11 others
		return GTObject{Label: "ignored", Box: b, Region: true}
	}
	return GTObject{Label: label, Box: b, Visible: w*h >= minVisibleArea && occ < 2}
}

// ParseUAVDT reads UAVDT *_gt_whole.txt:
// "frame,target,x,y,w,h,out-of-view,occlusion,category" (1 car, 2 truck, 3 bus).
func ParseUAVDT(path string) (map[int][]GTObject, error) {
	out := map[int][]GTObject{}
	err := eachCSVLine(path, func(v []int) {
		if len(v) < 9 {
			return
		}
		label, ok := uavdtClasses[v[8]]
		if !ok {
			return
		}
		b := Box{float64(v[2]), float64(v[3]), float64(v[2] + v[4]), float64(v[3] + v[5])}
		out[v[0]] = append(out[v[0]], GTObject{Label: label, Box: b, Visible: v[4]*v[5] >= minVisibleArea && v[7] < 3})
	})
	return out, err
}

// AddUAVDTIgnore adds the ignore regions of a UAVDT sequence (the
// *_gt_ignore.txt file next to *_gt_whole.txt: areas whose vehicles are not
// annotated) to the parsed frames. A missing file is not an error.
func AddUAVDTIgnore(frames map[int][]GTObject, wholePath string) error {
	path := strings.Replace(wholePath, "_gt_whole.txt", "_gt_ignore.txt", 1)
	if path == wholePath {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	return eachCSVLine(path, func(v []int) {
		if len(v) < 6 {
			return
		}
		b := Box{float64(v[2]), float64(v[3]), float64(v[2] + v[4]), float64(v[3] + v[5])}
		frames[v[0]] = append(frames[v[0]], GTObject{Label: "ignored", Box: b, Region: true})
	})
}

func eachCSVLine(path string, fn func([]int)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		parts := strings.Split(strings.TrimRight(strings.TrimSpace(sc.Text()), ","), ",")
		v := make([]int, 0, len(parts))
		for _, p := range parts {
			n, err := strconv.Atoi(strings.TrimSpace(p))
			if err != nil {
				break
			}
			v = append(v, n)
		}
		fn(v)
	}
	return sc.Err()
}
