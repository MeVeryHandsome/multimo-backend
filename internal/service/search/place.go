package search

import (
	"context"

	"multimo-backend/internal/pkg/llm"
)

const placePrompt = `从下面的检索语句中找出其中提到的地理位置（国家、城市、地区、地标）。只返回 JSON：
{"place": "中文地名，没有就填空字符串", "place_en": "英文地名", "capture_location": 这个地点是否指画面的拍摄地点（true/false；如果地点只是法规的适用范围、话题背景，填 false）,
 "lat": 纬度或 null, "lon": 经度或 null, "radius_km": 地点范围的大致半径（公里）}
lat/lon 给出该地点中心的大致坐标（十进制度，南纬和西经为负）；不知道就填 null。
检索语句：`

// placeFilter turns a place name in the query into a circle filter. The
// gazetteer is tried first; otherwise the model's own coordinate estimate
// is used and labelled as such.
func (s *Service) placeFilter(ctx context.Context, q string, f *Filters) {
	var out struct {
		Place    string   `json:"place"`
		PlaceEN  string   `json:"place_en"`
		Capture  bool     `json:"capture_location"`
		Lat      *float64 `json:"lat"`
		Lon      *float64 `json:"lon"`
		RadiusKm float64  `json:"radius_km"`
	}
	zero := 0.0
	if _, err := s.AI.ChatJSON(ctx, llm.ChatRequest{Purpose: "search-place", MaxTokens: 200, Temperature: &zero,
		Messages: []llm.Message{{Text: placePrompt + q}}}, &out); err != nil || (out.Place == "" && out.PlaceEN == "") || !out.Capture {
		return
	}
	radius := out.RadiusKm * 1000
	if radius <= 0 || radius > 50000 {
		radius = 10000
	}
	if s.Geo != nil {
		if gz, err := s.Geo.Gazetteer(ctx); err == nil && gz != nil {
			for _, name := range []string{out.Place, out.PlaceEN} {
				if name == "" {
					continue
				}
				if p, ok := gz.Lookup(name); ok {
					f.Lat, f.Lon, f.RadiusM, f.Place = &p.Lat, &p.Lon, radius, name
					f.note("point", "gazetteer")
					f.note("radius", "llm")
					return
				}
			}
		}
	}
	if out.Lat != nil && out.Lon != nil {
		f.Lat, f.Lon, f.RadiusM = out.Lat, out.Lon, radius
		f.Place = out.Place
		if f.Place == "" {
			f.Place = out.PlaceEN
		}
		f.note("point", "llm-estimate")
		f.note("radius", "llm")
	}
}
