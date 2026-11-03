package parser

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// Document is extracted text, one entry per page where the format has pages
// (PDF); other formats are a single page.
type Document struct {
	Pages []string       `json:"-"`
	Title string         `json:"title,omitempty"`
	Meta  map[string]any `json:"meta,omitempty"`
}

func (d *Document) Text() string { return strings.Join(d.Pages, "\n") }

// ParseDocument extracts text. PDF uses pdftotext (poppler-utils), legacy
// .doc uses catdoc; DOCX, plain text, CSV and GeoJSON are read natively.
func ParseDocument(ctx context.Context, path, format string) (*Document, error) {
	switch format {
	case "pdf":
		return parsePDF(ctx, path)
	case "doc":
		out, err := runTool(ctx, "catdoc", "-d", "utf-8", "-w", path)
		if err != nil {
			return nil, err
		}
		text := cleanText(string(out))
		return &Document{Pages: []string{text}, Title: firstLine(text)}, nil
	case "docx":
		return parseDocx(path)
	case "geojson":
		return parseGeoJSON(path)
	case "json", "txt", "md", "csv":
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		text := cleanText(decodeText(raw))
		return &Document{Pages: []string{text}, Title: firstLine(text)}, nil
	}
	return nil, fmt.Errorf("%w: document format %q", ErrUnsupported, format)
}

func parsePDF(ctx context.Context, path string) (*Document, error) {
	out, err := runTool(ctx, "pdftotext", "-enc", "UTF-8", path, "-")
	if err != nil {
		return nil, err
	}
	pages := strings.Split(string(out), "\f")
	if n := len(pages); n > 1 && strings.TrimSpace(pages[n-1]) == "" {
		pages = pages[:n-1]
	}
	for i := range pages {
		pages[i] = cleanText(pages[i])
	}
	doc := &Document{Pages: pages, Meta: map[string]any{}}
	if info, err := runTool(ctx, "pdfinfo", path); err == nil {
		for _, line := range strings.Split(string(info), "\n") {
			k, v, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			v = strings.TrimSpace(v)
			switch strings.TrimSpace(k) {
			case "Title":
				doc.Title = v
			case "CreationDate", "Author", "Producer":
				doc.Meta[strings.ToLower(strings.TrimSpace(k))] = v
			}
		}
	}
	if doc.Title == "" {
		doc.Title = firstLine(strings.Join(pages, "\n"))
	}
	return doc, nil
}

func parseDocx(path string) (*Document, error) {
	z, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer z.Close()
	var body, core []byte
	for _, f := range z.File {
		switch f.Name {
		case "word/document.xml":
			body, err = readZip(f)
		case "docProps/core.xml":
			core, _ = readZip(f)
		}
		if err != nil {
			return nil, err
		}
	}
	if body == nil {
		return nil, fmt.Errorf("docx without word/document.xml")
	}
	text, err := docxText(body)
	if err != nil {
		return nil, err
	}
	doc := &Document{Pages: []string{cleanText(text)}, Meta: map[string]any{}}
	if core != nil {
		var c struct {
			Title   string `xml:"title"`
			Creator string `xml:"creator"`
			Created string `xml:"created"`
		}
		if xml.Unmarshal(core, &c) == nil {
			doc.Title = c.Title
			if c.Created != "" {
				doc.Meta["created"] = c.Created
			}
		}
	}
	if doc.Title == "" {
		doc.Title = firstLine(doc.Pages[0])
	}
	return doc, nil
}

// docxText walks WordprocessingML: text runs (w:t), tabs, breaks, and one
// newline per paragraph.
func docxText(body []byte) (string, error) {
	dec := xml.NewDecoder(bytes.NewReader(body))
	var b strings.Builder
	inText := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inText = true
			case "tab":
				b.WriteByte('\t')
			case "br", "cr":
				b.WriteByte('\n')
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				b.WriteByte('\n')
			}
		case xml.CharData:
			if inText {
				b.Write(t)
			}
		}
	}
	return b.String(), nil
}

// parseGeoJSON renders each feature as one line of searchable text with its
// name(s), selected properties and coordinates.
func parseGeoJSON(path string) (*Document, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var fc struct {
		Name     string `json:"name"`
		Features []struct {
			Properties map[string]any `json:"properties"`
			Geometry   struct {
				Type        string          `json:"type"`
				Coordinates json.RawMessage `json:"coordinates"`
			} `json:"geometry"`
		} `json:"features"`
	}
	if err := json.Unmarshal(raw, &fc); err != nil {
		return nil, fmt.Errorf("geojson: %w", err)
	}
	nameKeys := []string{"name", "name_zh", "name_en", "nameascii"}
	infoKeys := []string{"featurecla", "type", "adm0name", "adm1name", "sov0name", "iata_code", "gps_code", "pop_max", "location", "abbrev"}
	lines := make([]string, 0, len(fc.Features))
	for _, f := range fc.Features {
		props := make(map[string]any, len(f.Properties))
		for k, v := range f.Properties {
			props[strings.ToLower(k)] = v // Natural Earth full files use upper case
		}
		var parts []string
		seen := map[string]bool{}
		for _, k := range nameKeys {
			if v, ok := props[k].(string); ok && v != "" && !seen[v] {
				parts = append(parts, v)
				seen[v] = true
			}
		}
		for _, k := range infoKeys {
			if v, ok := props[k]; ok && v != nil && fmt.Sprint(v) != "" {
				parts = append(parts, k+"="+fmt.Sprint(v))
			}
		}
		if f.Geometry.Type == "Point" {
			var c []float64
			if json.Unmarshal(f.Geometry.Coordinates, &c) == nil && len(c) >= 2 {
				parts = append(parts, fmt.Sprintf("lat=%.5f lon=%.5f", c[1], c[0]))
			}
		}
		if len(parts) > 0 {
			lines = append(lines, strings.Join(parts, " | "))
		}
	}
	return &Document{Pages: []string{strings.Join(lines, "\n")}, Title: fc.Name,
		Meta: map[string]any{"features": len(fc.Features)}}, nil
}

// Chunk is a retrieval unit of document text.
type Chunk struct {
	Seq  int    `json:"seq"`
	Page int    `json:"page"` // 1-based
	Text string `json:"text"`
}

// ChunkDocument splits pages into chunks of about size runes with overlap,
// breaking at paragraph or sentence ends when one is near. A chunk never
// spans two pages, so its page number is exact.
func ChunkDocument(doc *Document, size, overlap int) []Chunk {
	var out []Chunk
	for p, page := range doc.Pages {
		r := []rune(strings.TrimSpace(page))
		for start := 0; start < len(r); {
			end := min(start+size, len(r))
			if end < len(r) {
				end = breakPoint(r, start+size*2/3, end)
			}
			text := strings.TrimSpace(string(r[start:end]))
			if text != "" {
				out = append(out, Chunk{Seq: len(out), Page: p + 1, Text: text})
			}
			if end >= len(r) {
				break
			}
			start = max(end-overlap, start+1)
		}
	}
	return out
}

// breakPoint finds the last paragraph, sentence or line end in r[lo:hi].
func breakPoint(r []rune, lo, hi int) int {
	for _, set := range []string{"\n\n", "。！？.!?；;", "\n，, "} {
		for i := hi - 1; i >= lo; i-- {
			if set == "\n\n" {
				if i > 0 && r[i] == '\n' && r[i-1] == '\n' {
					return i + 1
				}
				continue
			}
			if strings.ContainsRune(set, r[i]) {
				return i + 1
			}
		}
	}
	return hi
}

func readZip(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, 64<<20))
}

// decodeText returns raw as UTF-8, converting from GB18030 when it is not
// valid UTF-8 (common for Chinese text files).
func decodeText(raw []byte) string {
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
	if utf8.Valid(raw) {
		return string(raw)
	}
	if out, err := simplifiedchinese.GB18030.NewDecoder().Bytes(raw); err == nil {
		return string(out)
	}
	return strings.ToValidUTF8(string(raw), "")
}

// cleanText normalises line endings, drops control characters and collapses
// runs of blank lines and spaces.
func cleanText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	lines := strings.Split(s, "\n")
	var out []string
	blank := 0
	for _, l := range lines {
		l = strings.Join(strings.Fields(l), " ")
		if l == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); len([]rune(l)) >= 4 && !strings.HasPrefix(l, "[") {
			r := []rune(l)
			return string(r[:min(len(r), 80)])
		}
	}
	return ""
}

func runTool(ctx context.Context, name string, args ...string) ([]byte, error) {
	if _, err := exec.LookPath(name); err != nil {
		return nil, fmt.Errorf("%s not installed: %w", name, err)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}
