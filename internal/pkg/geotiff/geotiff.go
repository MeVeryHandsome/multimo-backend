// Package geotiff reads georeferenced TIFF rasters: the GeoTIFF tags that map
// pixels to coordinates, and pixel regions of tiled or stripped images
// without decoding the whole file. Large satellite base maps (hundreds of MB,
// tens of thousands of pixels per side) are the main use; ffmpeg and
// image/tiff either cannot decode tiled layouts or need the whole raster in
// memory.
//
// Supported: classic (non-Big) TIFF, either byte order, 8-bit samples, 1-4
// samples per pixel, planar configuration 1, compression none / LZW /
// Deflate, without predictor.
package geotiff

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"math"
	"os"

	"golang.org/x/image/tiff/lzw"
)

const (
	tagImageWidth      = 256
	tagImageLength     = 257
	tagBitsPerSample   = 258
	tagCompression     = 259
	tagPhotometric     = 262
	tagStripOffsets    = 273
	tagSamplesPerPixel = 277
	tagRowsPerStrip    = 278
	tagStripByteCounts = 279
	tagPlanarConfig    = 284
	tagPredictor       = 317
	tagTileWidth       = 322
	tagTileLength      = 323
	tagTileOffsets     = 324
	tagTileByteCounts  = 325
	tagModelPixelScale = 33550
	tagModelTiepoint   = 33922
	tagModelTransform  = 34264
	tagGeoKeyDirectory = 34735

	geoKeyModelType      = 1024
	geoKeyGeographicType = 2048
	geoKeyProjectedCS    = 3072

	modelTypeProjected  = 1
	modelTypeGeographic = 2
)

// Georef describes how pixels map to model coordinates.
type Georef struct {
	ModelType  int     `json:"modelType"`      // 1 projected, 2 geographic
	EPSG       int     `json:"epsg,omitempty"` // GeographicType or ProjectedCSType key
	OriginX    float64 `json:"originX"`        // model X of pixel (0,0) corner
	OriginY    float64 `json:"originY"`        // model Y of pixel (0,0) corner
	PixelSizeX float64 `json:"pixelSizeX"`     // model units per pixel, X
	PixelSizeY float64 `json:"pixelSizeY"`     // model units per pixel, Y (positive: Y decreases downwards)
	Geographic bool    `json:"geographic"`     // true when model units are degrees lon/lat
}

// Bounds is a lon/lat box (only for geographic rasters).
type Bounds struct {
	West  float64 `json:"west"`
	South float64 `json:"south"`
	East  float64 `json:"east"`
	North float64 `json:"north"`
}

type File struct {
	f          *os.File
	order      binary.ByteOrder
	Width      int
	Height     int
	Samples    int
	Compressed int
	Tiled      bool
	TileW      int
	TileH      int
	offsets    []int64
	counts     []int64
	rowsStrip  int
	Georef     *Georef

	cacheIdx  int
	cacheData []byte
}

// Open reads the first IFD. The file stays open until Close.
func Open(path string) (*File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	t := &File{f: f, cacheIdx: -1}
	if err := t.readHeader(); err != nil {
		f.Close()
		return nil, err
	}
	return t, nil
}

func (t *File) Close() error { return t.f.Close() }

// IsTIFF reports whether the first bytes look like a TIFF header.
func IsTIFF(head []byte) bool {
	return len(head) >= 4 && (bytes.Equal(head[:4], []byte("II*\x00")) || bytes.Equal(head[:4], []byte("MM\x00*")))
}

type entry struct {
	tag, typ uint16
	count    uint32
	value    []byte // raw bytes (already fetched if stored at an offset)
}

func (t *File) readHeader() error {
	var hdr [8]byte
	if _, err := io.ReadFull(t.f, hdr[:]); err != nil {
		return err
	}
	switch string(hdr[:2]) {
	case "II":
		t.order = binary.LittleEndian
	case "MM":
		t.order = binary.BigEndian
	default:
		return errors.New("not a TIFF file")
	}
	if magic := t.order.Uint16(hdr[2:]); magic == 43 {
		return errors.New("BigTIFF is not supported")
	} else if magic != 42 {
		return errors.New("not a TIFF file")
	}
	ifd := int64(t.order.Uint32(hdr[4:]))

	var n [2]byte
	if _, err := t.f.ReadAt(n[:], ifd); err != nil {
		return err
	}
	count := int(t.order.Uint16(n[:]))
	raw := make([]byte, count*12)
	if _, err := t.f.ReadAt(raw, ifd+2); err != nil {
		return err
	}
	entries := map[uint16]entry{}
	for i := 0; i < count; i++ {
		b := raw[i*12 : i*12+12]
		e := entry{tag: t.order.Uint16(b), typ: t.order.Uint16(b[2:]), count: t.order.Uint32(b[4:])}
		size := typeSize(e.typ) * int(e.count)
		if size <= 4 {
			e.value = append([]byte(nil), b[8:8+size]...)
		} else {
			e.value = make([]byte, size)
			if _, err := t.f.ReadAt(e.value, int64(t.order.Uint32(b[8:]))); err != nil {
				return fmt.Errorf("tag %d: %w", e.tag, err)
			}
		}
		entries[e.tag] = e
	}

	t.Width = int(t.uint(entries, tagImageWidth, 0))
	t.Height = int(t.uint(entries, tagImageLength, 0))
	t.Samples = int(t.uint(entries, tagSamplesPerPixel, 1))
	t.Compressed = int(t.uint(entries, tagCompression, 1))
	if bps := t.uints(entries[tagBitsPerSample]); len(bps) > 0 && bps[0] != 8 {
		return fmt.Errorf("unsupported bits per sample %d", bps[0])
	}
	if pc := t.uint(entries, tagPlanarConfig, 1); pc != 1 {
		return errors.New("planar configuration 2 is not supported")
	}
	if p := t.uint(entries, tagPredictor, 1); p != 1 {
		return fmt.Errorf("predictor %d is not supported", p)
	}
	switch t.Compressed {
	case 1, 5, 8, 32946:
	default:
		return fmt.Errorf("compression %d is not supported", t.Compressed)
	}
	if t.Samples < 1 || t.Samples > 4 {
		return fmt.Errorf("unsupported samples per pixel %d", t.Samples)
	}

	if _, ok := entries[tagTileWidth]; ok {
		t.Tiled = true
		t.TileW = int(t.uint(entries, tagTileWidth, 0))
		t.TileH = int(t.uint(entries, tagTileLength, 0))
		t.offsets = toInt64(t.uints(entries[tagTileOffsets]))
		t.counts = toInt64(t.uints(entries[tagTileByteCounts]))
	} else {
		t.rowsStrip = int(t.uint(entries, tagRowsPerStrip, uint64(t.Height)))
		t.TileW, t.TileH = t.Width, t.rowsStrip
		t.offsets = toInt64(t.uints(entries[tagStripOffsets]))
		t.counts = toInt64(t.uints(entries[tagStripByteCounts]))
	}
	if t.Width <= 0 || t.Height <= 0 || t.TileW <= 0 || t.TileH <= 0 || len(t.offsets) == 0 || len(t.offsets) != len(t.counts) {
		return errors.New("malformed TIFF layout")
	}
	t.Georef = t.parseGeoref(entries)
	return nil
}

func (t *File) parseGeoref(entries map[uint16]entry) *Georef {
	scale := t.doubles(entries[tagModelPixelScale])
	tie := t.doubles(entries[tagModelTiepoint])
	g := &Georef{}
	switch {
	case len(scale) >= 2 && len(tie) >= 6:
		g.PixelSizeX, g.PixelSizeY = scale[0], scale[1]
		g.OriginX = tie[3] - tie[0]*scale[0]
		g.OriginY = tie[4] + tie[1]*scale[1]
	case len(t.doubles(entries[tagModelTransform])) == 16:
		m := t.doubles(entries[tagModelTransform])
		if m[1] != 0 || m[4] != 0 {
			return nil // rotated rasters are out of scope
		}
		g.PixelSizeX, g.OriginX, g.PixelSizeY, g.OriginY = m[0], m[3], -m[5], m[7]
	default:
		return nil
	}
	keys := t.uints(entries[tagGeoKeyDirectory])
	for i := 4; i+3 < len(keys); i += 4 {
		id, loc, val := keys[i], keys[i+1], keys[i+3]
		if loc != 0 { // value stored elsewhere; the keys we need are inline shorts
			continue
		}
		switch id {
		case geoKeyModelType:
			g.ModelType = int(val)
		case geoKeyGeographicType:
			if g.EPSG == 0 {
				g.EPSG = int(val)
			}
		case geoKeyProjectedCS:
			g.EPSG = int(val)
		}
	}
	g.Geographic = g.ModelType == modelTypeGeographic
	return g
}

// LonLatBounds returns the raster extent for geographic rasters.
func (t *File) LonLatBounds() (Bounds, bool) {
	g := t.Georef
	if g == nil || !g.Geographic {
		return Bounds{}, false
	}
	return Bounds{West: g.OriginX, North: g.OriginY,
		East: g.OriginX + float64(t.Width)*g.PixelSizeX, South: g.OriginY - float64(t.Height)*g.PixelSizeY}, true
}

// PixelToLonLat maps a pixel position (may be fractional) to lon/lat.
func (t *File) PixelToLonLat(x, y float64) (lon, lat float64, ok bool) {
	g := t.Georef
	if g == nil || !g.Geographic {
		return 0, 0, false
	}
	return g.OriginX + x*g.PixelSizeX, g.OriginY - y*g.PixelSizeY, true
}

// LonLatToPixel is the inverse of PixelToLonLat.
func (t *File) LonLatToPixel(lon, lat float64) (x, y float64, ok bool) {
	g := t.Georef
	if g == nil || !g.Geographic || g.PixelSizeX == 0 || g.PixelSizeY == 0 {
		return 0, 0, false
	}
	return (lon - g.OriginX) / g.PixelSizeX, (g.OriginY - lat) / g.PixelSizeY, true
}

// GroundResolution is the approximate metres per pixel at the raster centre.
func (t *File) GroundResolution() float64 {
	g := t.Georef
	if g == nil {
		return 0
	}
	if !g.Geographic {
		return g.PixelSizeX
	}
	lat := g.OriginY - float64(t.Height)/2*g.PixelSizeY
	return g.PixelSizeX * 111320 * math.Cos(lat*math.Pi/180)
}

// ReadRegion returns the pixels of r (clipped to the image) as RGBA.
func (t *File) ReadRegion(r image.Rectangle) (*image.RGBA, error) {
	r = r.Intersect(image.Rect(0, 0, t.Width, t.Height))
	out := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	if r.Empty() {
		return out, nil
	}
	tilesAcross := (t.Width + t.TileW - 1) / t.TileW
	for ty := r.Min.Y / t.TileH; ty*t.TileH < r.Max.Y; ty++ {
		for tx := r.Min.X / t.TileW; tx*t.TileW < r.Max.X; tx++ {
			idx := ty*tilesAcross + tx
			if !t.Tiled {
				idx = ty
			}
			data, err := t.chunk(idx)
			if err != nil {
				return nil, err
			}
			x0, y0 := tx*t.TileW, ty*t.TileH
			for y := max(r.Min.Y, y0); y < min(r.Max.Y, y0+t.TileH); y++ {
				for x := max(r.Min.X, x0); x < min(r.Max.X, x0+t.TileW); x++ {
					off := ((y-y0)*t.TileW + (x - x0)) * t.Samples
					if off+t.Samples > len(data) {
						continue
					}
					out.SetRGBA(x-r.Min.X, y-r.Min.Y, t.pixel(data[off:off+t.Samples]))
				}
			}
		}
	}
	return out, nil
}

// Overview returns a downsampled copy whose longer side is at most maxDim
// pixels (nearest-neighbour). For uncompressed rasters only the sampled pixel
// rows are read, so even very large files take well under a second.
func (t *File) Overview(maxDim int) (*image.RGBA, error) {
	scale := math.Max(float64(t.Width), float64(t.Height)) / float64(maxDim)
	if scale < 1 {
		scale = 1
	}
	ow, oh := max(1, int(float64(t.Width)/scale)), max(1, int(float64(t.Height)/scale))
	out := image.NewRGBA(image.Rect(0, 0, ow, oh))
	tilesAcross := (t.Width + t.TileW - 1) / t.TileW
	rowBuf := make([]byte, t.TileW*t.Samples)
	for oy := 0; oy < oh; oy++ {
		sy := min(t.Height-1, int((float64(oy)+0.5)*scale))
		ty := sy / t.TileH
		for ox := 0; ox < ow; ox++ {
			sx := min(t.Width-1, int((float64(ox)+0.5)*scale))
			tx := sx / t.TileW
			idx := ty*tilesAcross + tx
			if !t.Tiled {
				idx = ty
			}
			var px []byte
			if t.Compressed == 1 {
				// Read just this pixel row of the tile (cached while ox stays in the tile).
				key := idx*t.TileH + (sy - ty*t.TileH)
				if t.cacheIdx != key {
					off := t.offsets[idx] + int64((sy-ty*t.TileH)*t.TileW*t.Samples)
					n, err := t.f.ReadAt(rowBuf, off)
					if err != nil && !(errors.Is(err, io.EOF) && n > 0) {
						return nil, err
					}
					t.cacheIdx, t.cacheData = key, rowBuf
				}
				o := (sx - tx*t.TileW) * t.Samples
				px = t.cacheData[o : o+t.Samples]
			} else {
				data, err := t.chunk(idx)
				if err != nil {
					return nil, err
				}
				o := ((sy-ty*t.TileH)*t.TileW + (sx - tx*t.TileW)) * t.Samples
				if o+t.Samples > len(data) {
					continue
				}
				px = data[o : o+t.Samples]
			}
			out.SetRGBA(ox, oy, t.pixel(px))
		}
	}
	t.cacheIdx, t.cacheData = -1, nil
	return out, nil
}

func (t *File) pixel(p []byte) color.RGBA {
	switch t.Samples {
	case 1:
		return color.RGBA{p[0], p[0], p[0], 255}
	case 2:
		return color.RGBA{p[0], p[0], p[0], p[1]}
	case 3:
		return color.RGBA{p[0], p[1], p[2], 255}
	default:
		return color.RGBA{p[0], p[1], p[2], p[3]}
	}
}

// chunk returns the decoded bytes of tile/strip idx (one-entry cache).
func (t *File) chunk(idx int) ([]byte, error) {
	if idx < 0 || idx >= len(t.offsets) {
		return nil, fmt.Errorf("tile %d out of range", idx)
	}
	if t.cacheIdx == -(idx + 2) {
		return t.cacheData, nil
	}
	raw := make([]byte, t.counts[idx])
	if _, err := t.f.ReadAt(raw, t.offsets[idx]); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	var data []byte
	switch t.Compressed {
	case 1:
		data = raw
	case 5:
		r := lzw.NewReader(bytes.NewReader(raw), lzw.MSB, 8)
		d, err := io.ReadAll(r)
		r.Close()
		if err != nil && len(d) == 0 {
			return nil, err
		}
		data = d
	case 8, 32946:
		r, err := zlib.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		d, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			return nil, err
		}
		data = d
	}
	t.cacheIdx, t.cacheData = -(idx + 2), data
	return data, nil
}

func typeSize(typ uint16) int {
	switch typ {
	case 1, 2, 6, 7: // BYTE ASCII SBYTE UNDEFINED
		return 1
	case 3, 8: // SHORT SSHORT
		return 2
	case 4, 9, 11: // LONG SLONG FLOAT
		return 4
	case 5, 10, 12: // RATIONAL SRATIONAL DOUBLE
		return 8
	}
	return 1
}

func (t *File) uints(e entry) []uint64 {
	var out []uint64
	switch e.typ {
	case 1:
		for _, b := range e.value {
			out = append(out, uint64(b))
		}
	case 3:
		for i := 0; i+2 <= len(e.value); i += 2 {
			out = append(out, uint64(t.order.Uint16(e.value[i:])))
		}
	case 4:
		for i := 0; i+4 <= len(e.value); i += 4 {
			out = append(out, uint64(t.order.Uint32(e.value[i:])))
		}
	}
	return out
}

func (t *File) uint(entries map[uint16]entry, tag uint16, def uint64) uint64 {
	if v := t.uints(entries[tag]); len(v) > 0 {
		return v[0]
	}
	return def
}

func (t *File) doubles(e entry) []float64 {
	if e.typ != 12 {
		return nil
	}
	out := make([]float64, 0, len(e.value)/8)
	for i := 0; i+8 <= len(e.value); i += 8 {
		out = append(out, math.Float64frombits(t.order.Uint64(e.value[i:])))
	}
	return out
}

func toInt64(v []uint64) []int64 {
	out := make([]int64, len(v))
	for i, x := range v {
		out[i] = int64(x)
	}
	return out
}
