package geo

import (
	"bytes"
	"image"
	"image/jpeg"
	"math"
	"sort"

	"golang.org/x/image/draw"

	"multimo-backend/internal/pkg/geotiff"
	"multimo-backend/internal/pkg/llm"
)

// Visual localisation, as validated on UAV-VisLoc region 01 (82 photos, no
// GPS, 0.3 m satellite base map):
//
//	single image, 4 rotations, 300 m tiles     : median 472 m, <100 m 28%
//	+ 12 rotations, tiles at 250/300/400 m     : top-30 recall (<100 m) 82%
//	+ track smoothing over time-ordered photos : median 128 m, <100 m 48%
//
// SIFT/ORB verification was tried and dropped: at the true location only
// 5-9 RANSAC inliers survive (different years and seasons between the drone
// photos and the satellite image).

const (
	querySide      = 512 // query and tile images are resampled to this square
	queryRotations = 12  // every 30 degrees
	candidatesPer  = 30  // candidates kept per image after merging scales
	dedupMetres    = 60  // candidates closer than this are the same place
)

// Candidate is a possible position for one image.
type Candidate struct {
	Lat   float64 `json:"lat"`
	Lon   float64 `json:"lon"`
	Score float64 `json:"score"`
	Scale int     `json:"scale"`
}

// QueryImages turns a photo into rotated square queries: centre square,
// resampled to 512 px, rotated in 30 degree steps with mirrored borders so no
// black corners appear.
func QueryImages(src image.Image) []image.Image {
	b := src.Bounds()
	s := min(b.Dx(), b.Dy())
	x0, y0 := b.Min.X+(b.Dx()-s)/2, b.Min.Y+(b.Dy()-s)/2
	sq := image.NewRGBA(image.Rect(0, 0, querySide, querySide))
	draw.ApproxBiLinear.Scale(sq, sq.Bounds(), src, image.Rect(x0, y0, x0+s, y0+s), draw.Src, nil)
	out := make([]image.Image, queryRotations)
	for i := range out {
		out[i] = rotateReflect(sq, float64(i)*360/queryRotations)
	}
	return out
}

// rotateReflect rotates a square image about its centre, sampling bilinearly
// and mirroring at the borders.
func rotateReflect(src *image.RGBA, deg float64) *image.RGBA {
	n := src.Bounds().Dx()
	dst := image.NewRGBA(image.Rect(0, 0, n, n))
	if deg == 0 {
		copy(dst.Pix, src.Pix)
		return dst
	}
	rad := deg * math.Pi / 180
	cos, sin := math.Cos(rad), math.Sin(rad)
	c := float64(n-1) / 2
	reflect := func(v float64) float64 {
		m := float64(n - 1)
		for v < 0 || v > m {
			if v < 0 {
				v = -v
			}
			if v > m {
				v = 2*m - v
			}
		}
		return v
	}
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			dx, dy := float64(x)-c, float64(y)-c
			sx := reflect(c + dx*cos + dy*sin)
			sy := reflect(c - dx*sin + dy*cos)
			x0, y0 := int(sx), int(sy)
			x1, y1 := min(x0+1, n-1), min(y0+1, n-1)
			fx, fy := sx-float64(x0), sy-float64(y0)
			for ch := 0; ch < 3; ch++ {
				p := func(xx, yy int) float64 { return float64(src.Pix[yy*src.Stride+xx*4+ch]) }
				v := (p(x0, y0)*(1-fx)+p(x1, y0)*fx)*(1-fy) + (p(x0, y1)*(1-fx)+p(x1, y1)*fx)*fy
				dst.Pix[y*dst.Stride+x*4+ch] = uint8(v + 0.5)
			}
			dst.Pix[y*dst.Stride+x*4+3] = 255
		}
	}
	return dst
}

// TileImage crops a square of side metres centred on (lat, lon) from a
// geographic raster and resamples it to 512x512. It returns nil when the
// square leaves the raster.
func TileImage(t *geotiff.File, lat, lon float64, sideM float64) (image.Image, error) {
	x, y, ok := t.LonLatToPixel(lon, lat)
	if !ok {
		return nil, nil
	}
	mLat, mLon := metresPerDegree(lat)
	hw := sideM / 2 / (t.Georef.PixelSizeX * mLon)
	hh := sideM / 2 / (t.Georef.PixelSizeY * mLat)
	r := image.Rect(int(x-hw), int(y-hh), int(x+hw), int(y+hh))
	if r.Min.X < 0 || r.Min.Y < 0 || r.Max.X > t.Width || r.Max.Y > t.Height {
		return nil, nil
	}
	region, err := t.ReadRegion(r)
	if err != nil {
		return nil, err
	}
	dst := image.NewRGBA(image.Rect(0, 0, querySide, querySide))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), region, region.Bounds(), draw.Src, nil)
	return dst, nil
}

// TileCentres lays out tile centres over a raster's bounds with a stride of
// half the tile side, keeping whole tiles inside.
func TileCentres(b geotiff.Bounds, sideM float64) [][2]float64 {
	midLat := (b.North + b.South) / 2
	mLat, mLon := metresPerDegree(midLat)
	halfLat, halfLon := sideM/2/mLat, sideM/2/mLon
	stepLat, stepLon := sideM/2/mLat, sideM/2/mLon
	var out [][2]float64
	for lat := b.North - halfLat; lat >= b.South+halfLat; lat -= stepLat {
		for lon := b.West + halfLon; lon <= b.East-halfLon; lon += stepLon {
			out = append(out, [2]float64{lat, lon})
		}
	}
	return out
}

// Tile is an indexed basemap square.
type Tile struct {
	Lat, Lon float64
	Scale    int
	Emb      []float32
}

// RankTiles scores every tile by its best similarity over all query
// rotations, keeps the top candidatesPer per scale, merges scales and drops
// near-duplicates.
func RankTiles(queries [][]float32, tiles []Tile) []Candidate {
	byScale := map[int][]Candidate{}
	for _, t := range tiles {
		best := float32(-1)
		for _, q := range queries {
			if s := llm.Dot(q, t.Emb); s > best {
				best = s
			}
		}
		byScale[t.Scale] = append(byScale[t.Scale], Candidate{Lat: t.Lat, Lon: t.Lon, Score: float64(best), Scale: t.Scale})
	}
	var pool []Candidate
	for _, cs := range byScale {
		sort.Slice(cs, func(i, j int) bool { return cs[i].Score > cs[j].Score })
		pool = append(pool, cs[:min(len(cs), candidatesPer)]...)
	}
	sort.Slice(pool, func(i, j int) bool { return pool[i].Score > pool[j].Score })
	var keep []Candidate
	for _, c := range pool {
		dup := false
		for _, k := range keep {
			if haversine(c.Lat, c.Lon, k.Lat, k.Lon) <= dedupMetres {
				dup = true
				break
			}
		}
		if !dup {
			keep = append(keep, c)
			if len(keep) == candidatesPer {
				break
			}
		}
	}
	return keep
}

// SmoothTrack picks one candidate per image (images in time order, times in
// seconds) maximising similarity while penalising moves faster than vmax.
// Returns the chosen index for each image.
func SmoothTrack(times []float64, cands [][]Candidate, vmax float64) []int {
	const weight, slack = 20.0, 150.0
	n := len(cands)
	if n == 0 {
		return nil
	}
	score := make([][]float64, n)
	back := make([][]int, n)
	score[0] = make([]float64, len(cands[0]))
	for j, c := range cands[0] {
		score[0][j] = c.Score * weight
	}
	for t := 1; t < n; t++ {
		allow := vmax*math.Max(times[t]-times[t-1], 1) + slack
		score[t] = make([]float64, len(cands[t]))
		back[t] = make([]int, len(cands[t]))
		for j, c := range cands[t] {
			best, bi := math.Inf(-1), 0
			for i, p := range cands[t-1] {
				d := haversine(p.Lat, p.Lon, c.Lat, c.Lon)
				v := score[t-1][i]
				if d > allow {
					v -= (d - allow) / 100
				}
				if v > best {
					best, bi = v, i
				}
			}
			score[t][j], back[t][j] = best+c.Score*weight, bi
		}
	}
	path := make([]int, n)
	for j := range score[n-1] {
		if score[n-1][j] > score[n-1][path[n-1]] {
			path[n-1] = j
		}
	}
	for t := n - 1; t > 0; t-- {
		path[t-1] = back[t][path[t]]
	}
	return path
}

// EncodeJPEG is a small helper for embedding inputs.
func EncodeJPEG(img image.Image) (*llm.Image, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		return nil, err
	}
	b := img.Bounds()
	return &llm.Image{Data: buf.Bytes(), MIME: "image/jpeg", Width: b.Dx(), Height: b.Dy()}, nil
}
