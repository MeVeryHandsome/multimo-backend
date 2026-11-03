package geo

import (
	"encoding/json"
	"os"
	"strings"
)

// Place is a named point from a GeoJSON gazetteer (e.g. Natural Earth).
type Place struct {
	Name string  `json:"name"`
	Lat  float64 `json:"lat"`
	Lon  float64 `json:"lon"`
	Pop  float64 `json:"pop"`
	Kind string  `json:"kind"`
}

type Gazetteer struct {
	byName map[string][]Place
}

// Property names are matched case-insensitively (Natural Earth's full
// files use upper case: NAME, NAME_ZH, POP_MAX).
var nameKeys = []string{"name", "name_zh", "name_en", "nameascii", "name_alt"}

// LoadGazetteer reads point features from GeoJSON files.
func LoadGazetteer(paths []string) (*Gazetteer, error) {
	g := &Gazetteer{byName: map[string][]Place{}}
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		var fc struct {
			Features []struct {
				Properties map[string]any `json:"properties"`
				Geometry   struct {
					Type        string    `json:"type"`
					Coordinates []float64 `json:"coordinates"`
				} `json:"geometry"`
			} `json:"features"`
		}
		if err := json.Unmarshal(raw, &fc); err != nil {
			continue // not a point collection we understand
		}
		for _, f := range fc.Features {
			if f.Geometry.Type != "Point" || len(f.Geometry.Coordinates) < 2 {
				continue
			}
			props := make(map[string]any, len(f.Properties))
			for k, v := range f.Properties {
				props[strings.ToLower(k)] = v
			}
			pop, _ := props["pop_max"].(float64)
			kind, _ := props["featurecla"].(string)
			if kind == "" {
				kind, _ = props["type"].(string)
			}
			base := Place{Lat: f.Geometry.Coordinates[1], Lon: f.Geometry.Coordinates[0], Pop: pop, Kind: kind}
			for _, k := range nameKeys {
				if v, ok := props[k].(string); ok && v != "" {
					pl := base
					pl.Name = v
					key := normName(v)
					g.byName[key] = append(g.byName[key], pl)
				}
			}
		}
	}
	return g, nil
}

// Size is the number of distinct names.
func (g *Gazetteer) Size() int { return len(g.byName) }

// Lookup finds the most populous place with this name, trying the name as
// given and without common Chinese administrative suffixes.
func (g *Gazetteer) Lookup(name string) (Place, bool) {
	for _, key := range variants(name) {
		if ps := g.byName[key]; len(ps) > 0 {
			best := ps[0]
			for _, p := range ps[1:] {
				if p.Pop > best.Pop {
					best = p
				}
			}
			return best, true
		}
	}
	return Place{}, false
}

func variants(name string) []string {
	n := normName(name)
	out := []string{n}
	for _, suf := range []string{"特别行政区", "自治区", "市", "省", "区", "县", " city"} {
		if t := strings.TrimSuffix(n, suf); t != n && t != "" {
			out = append(out, t)
		}
	}
	// "中国江西省九江市" -> try the last segment too
	if i := strings.LastIndex(n, "省"); i > 0 && i+len("省") < len(n) {
		out = append(out, variants(n[i+len("省"):])...)
	}
	return out
}

func normName(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
