package parser

import (
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"time"

	"github.com/rwcarlsen/goexif/exif"
	_ "golang.org/x/image/tiff"

	"multimo-backend/internal/pkg/geotiff"
)

type EXIF struct {
	Time  *time.Time `json:"time,omitempty"`
	Lat   *float64   `json:"lat,omitempty"`
	Lon   *float64   `json:"lon,omitempty"`
	Make  string     `json:"make,omitempty"`
	Model string     `json:"model,omitempty"`
}

type Geo struct {
	Georef      geotiff.Georef  `json:"georef"`
	Bounds      *geotiff.Bounds `json:"bounds,omitempty"`
	CenterLat   *float64        `json:"centerLat,omitempty"`
	CenterLon   *float64        `json:"centerLon,omitempty"`
	GroundResM  float64         `json:"groundResM"`
	Tiled       bool            `json:"tiled"`
	TileSize    int             `json:"tileSize,omitempty"`
	Compression int             `json:"compression"`
}

type ImageInfo struct {
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Format string `json:"format"`
	EXIF   *EXIF  `json:"exif,omitempty"`
	Geo    *Geo   `json:"geo,omitempty"`
}

// ParseImage reads dimensions, EXIF (time, GPS, camera) and GeoTIFF
// georeferencing. Pixel data is not decoded.
func ParseImage(path string) (*ImageInfo, error) {
	d, err := Detect(path)
	if err != nil {
		return nil, err
	}
	info := &ImageInfo{Format: d.Format}

	if d.Format == "tiff" {
		if t, err := geotiff.Open(path); err == nil {
			info.Width, info.Height = t.Width, t.Height
			if t.Georef != nil {
				g := &Geo{Georef: *t.Georef, GroundResM: t.GroundResolution(), Tiled: t.Tiled, Compression: t.Compressed}
				if t.Tiled {
					g.TileSize = t.TileW
				}
				if b, ok := t.LonLatBounds(); ok {
					g.Bounds = &b
					lat, lon := (b.North+b.South)/2, (b.West+b.East)/2
					g.CenterLat, g.CenterLon = &lat, &lon
				}
				info.Geo = g
			}
			t.Close()
		}
	}
	if info.Width == 0 {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		cfg, _, err := image.DecodeConfig(f)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("decode %s header: %w", d.Format, err)
		}
		info.Width, info.Height = cfg.Width, cfg.Height
	}
	// goexif reads a whole TIFF into memory; large rasters carry georeferencing
	// in GeoTIFF tags anyway, so EXIF is only read from JPEGs and small TIFFs.
	if d.Format == "jpeg" || (d.Format == "tiff" && fileSize(path) < 64<<20) {
		info.EXIF = readEXIF(path)
	}
	return info, nil
}

func fileSize(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.Size()
}

func readEXIF(path string) *EXIF {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	x, err := exif.Decode(f)
	if err != nil {
		return nil
	}
	e := &EXIF{}
	if t, err := x.DateTime(); err == nil && t.Year() > 1971 {
		e.Time = &t
	}
	if lat, lon, err := x.LatLong(); err == nil && (lat != 0 || lon != 0) {
		e.Lat, e.Lon = &lat, &lon
	}
	if tag, err := x.Get(exif.Make); err == nil {
		e.Make, _ = tag.StringVal()
	}
	if tag, err := x.Get(exif.Model); err == nil {
		e.Model, _ = tag.StringVal()
	}
	if e.Time == nil && e.Lat == nil && e.Make == "" && e.Model == "" {
		return nil
	}
	return e
}
