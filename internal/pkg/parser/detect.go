// Package parser identifies ingested files and extracts their metadata or
// text: video (via media), images and GeoTIFF rasters, and documents.
package parser

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"multimo-backend/internal/pkg/geotiff"
)

const (
	TypeVideo         = "video"
	TypeImage         = "image"
	TypeRemoteSensing = "remote_sensing"
	TypeDocument      = "document"
)

// ErrUnsupported means the file is not one of the accepted formats.
var ErrUnsupported = errors.New("unsupported file type")

type Detected struct {
	Type   string `json:"type"`
	Format string `json:"format"` // mp4, wmv, flv, mov, mkv, avi, jpeg, png, tiff, pdf, doc, docx, txt, csv, geojson, json, md
}

var byExt = map[string]Detected{
	".mp4": {TypeVideo, "mp4"}, ".m4v": {TypeVideo, "mp4"}, ".mov": {TypeVideo, "mov"},
	".wmv": {TypeVideo, "wmv"}, ".asf": {TypeVideo, "wmv"}, ".flv": {TypeVideo, "flv"},
	".mkv": {TypeVideo, "mkv"}, ".avi": {TypeVideo, "avi"}, ".ts": {TypeVideo, "ts"},
	".jpg": {TypeImage, "jpeg"}, ".jpeg": {TypeImage, "jpeg"}, ".png": {TypeImage, "png"},
	".tif": {TypeImage, "tiff"}, ".tiff": {TypeImage, "tiff"},
	".pdf": {TypeDocument, "pdf"}, ".doc": {TypeDocument, "doc"}, ".docx": {TypeDocument, "docx"},
	".txt": {TypeDocument, "txt"}, ".md": {TypeDocument, "md"}, ".csv": {TypeDocument, "csv"},
	".geojson": {TypeDocument, "geojson"}, ".json": {TypeDocument, "json"},
}

// Detect classifies a file from its leading bytes, using the extension only
// to break ties (text formats have no magic number). A TIFF carrying GeoTIFF
// tags is classified as remote_sensing.
func Detect(path string) (Detected, error) {
	f, err := os.Open(path)
	if err != nil {
		return Detected{}, err
	}
	defer f.Close()
	head := make([]byte, 64)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	ext := strings.ToLower(filepath.Ext(path))

	switch {
	case bytes.HasPrefix(head, []byte("%PDF")):
		return Detected{TypeDocument, "pdf"}, nil
	case bytes.HasPrefix(head, []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}):
		return Detected{TypeDocument, "doc"}, nil
	case bytes.HasPrefix(head, []byte("PK\x03\x04")):
		if isDocx(path) {
			return Detected{TypeDocument, "docx"}, nil
		}
		return Detected{}, fmt.Errorf("%w: zip archive", ErrUnsupported)
	case bytes.HasPrefix(head, []byte{0xFF, 0xD8, 0xFF}):
		return Detected{TypeImage, "jpeg"}, nil
	case bytes.HasPrefix(head, []byte("\x89PNG\r\n\x1a\n")):
		return Detected{TypeImage, "png"}, nil
	case geotiff.IsTIFF(head):
		if t, err := geotiff.Open(path); err == nil {
			geo := t.Georef != nil
			t.Close()
			if geo {
				return Detected{TypeRemoteSensing, "tiff"}, nil
			}
		}
		return Detected{TypeImage, "tiff"}, nil
	case bytes.HasPrefix(head, []byte{0x30, 0x26, 0xB2, 0x75, 0x8E, 0x66, 0xCF, 0x11}):
		return Detected{TypeVideo, "wmv"}, nil
	case bytes.HasPrefix(head, []byte("FLV")):
		return Detected{TypeVideo, "flv"}, nil
	case len(head) >= 12 && string(head[4:8]) == "ftyp":
		if brand := string(head[8:12]); brand == "qt  " {
			return Detected{TypeVideo, "mov"}, nil
		}
		return Detected{TypeVideo, "mp4"}, nil
	case bytes.HasPrefix(head, []byte{0x1A, 0x45, 0xDF, 0xA3}):
		return Detected{TypeVideo, "mkv"}, nil
	case len(head) >= 12 && string(head[:4]) == "RIFF" && string(head[8:12]) == "AVI ":
		return Detected{TypeVideo, "avi"}, nil
	}
	if d, ok := byExt[ext]; ok && d.Type == TypeDocument && looksText(head) {
		return d, nil
	}
	return Detected{}, fmt.Errorf("%w: %s", ErrUnsupported, ext)
}

func isDocx(path string) bool {
	z, err := zip.OpenReader(path)
	if err != nil {
		return false
	}
	defer z.Close()
	for _, f := range z.File {
		if f.Name == "word/document.xml" {
			return true
		}
	}
	return false
}

func looksText(head []byte) bool {
	return !bytes.ContainsRune(head, 0)
}
