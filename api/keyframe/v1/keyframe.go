package v1

import (
	"github.com/gogf/gf/v2/frame/g"
	"github.com/gogf/gf/v2/os/gtime"
)

type Keyframe struct {
	Id                int64       `json:"id"                orm:"id"`
	AssetId           int64       `json:"assetId"           orm:"asset_id"`
	Idx               int         `json:"idx"               orm:"idx"`
	TsSec             float64     `json:"tsSec"             orm:"ts_sec"`
	FrameNo           *int        `json:"frameNo"           orm:"frame_no"`
	Width             int         `json:"width"             orm:"width"`
	Height            int         `json:"height"            orm:"height"`
	Description       string      `json:"description"       orm:"description"`
	DescriptionSource string      `json:"descriptionSource" orm:"description_source"`
	Tags              []string    `json:"tags"              orm:"tags"`
	CapturedAt        *gtime.Time `json:"capturedAt"        orm:"captured_at"`
	Lat               *float64    `json:"lat"               orm:"lat"`
	Lon               *float64    `json:"lon"               orm:"lon"`
	GeoSource         string      `json:"geoSource"         orm:"geo_source"`
	GeoAccuracyM      *float64    `json:"geoAccuracyM"      orm:"geo_accuracy_m"`
	Meta              g.Map       `json:"meta"              orm:"meta"`
	Detections        int         `json:"detections"        orm:"detections"`
	ImageUrl          string      `json:"imageUrl"`
}

type Detection struct {
	Id      int64    `json:"id"      orm:"id"`
	Label   string   `json:"label"   orm:"label"`
	LabelZh string   `json:"labelZh" orm:"label_zh"`
	Score   *float64 `json:"score"   orm:"score"`
	X       float64  `json:"x"       orm:"x"`
	Y       float64  `json:"y"       orm:"y"`
	W       float64  `json:"w"       orm:"w"`
	H       float64  `json:"h"       orm:"h"`
	Source  string   `json:"source"  orm:"source"`
}

type ListReq struct {
	g.Meta `path:"/assets/{id}/keyframes" method:"get" tags:"Keyframe" summary:"素材的关键帧" dc:"视频为按内容变化抽取的关键帧；图片和遥感影像各有一帧"`
	Id     int64 `json:"id" in:"path" v:"required"`
}
type ListRes struct {
	List []*Keyframe `json:"list"`
}

type GetReq struct {
	g.Meta `path:"/keyframes/{id}" method:"get" tags:"Keyframe" summary:"关键帧详情" dc:"含描述、标签、检测框（关键帧像素坐标）、定位候选"`
	Id     int64 `json:"id" in:"path" v:"required"`
}
type GetRes struct {
	Keyframe   *Keyframe    `json:"keyframe"`
	Detections []*Detection `json:"detections"`
}

type ImageReq struct {
	g.Meta `path:"/keyframes/{id}/image" method:"get" tags:"Keyframe" summary:"关键帧图片" dc:"返回 JPEG"`
	Id     int64 `json:"id" in:"path" v:"required"`
}
type ImageRes struct{}

type TrackPoint struct {
	TSec float64     `json:"tSec"  orm:"t_sec"`
	Ts   *gtime.Time `json:"ts"    orm:"ts"`
	Lat  float64     `json:"lat"   orm:"lat"`
	Lon  float64     `json:"lon"   orm:"lon"`
	AltM *float64    `json:"altM"  orm:"alt_m"`
}
type TrackReq struct {
	g.Meta `path:"/assets/{id}/track" method:"get" tags:"Keyframe" summary:"飞行轨迹" dc:"来自配套飞行记录、已对齐到视频时间轴的 GPS 点；最多返回 2000 个（均匀抽样）"`
	Id     int64 `json:"id" in:"path" v:"required"`
}
type TrackRes struct {
	Points []*TrackPoint `json:"points"`
}

type RerunReq struct {
	g.Meta     `path:"/pipeline/rerun" method:"post" tags:"Keyframe" summary:"批量重跑处理阶段" dc:"对符合条件的素材重新排队某个阶段，后续阶段会自动接着运行"`
	Stage      string  `json:"stage"      v:"required|in:ingest,keyframes,analyze,geo,basemap,index" dc:"ingest | keyframes | analyze | geo | basemap | index"`
	Collection string  `json:"collection"`
	Type       string  `json:"type"`
	AssetIds   []int64 `json:"assetIds"`
}
type RerunRes struct {
	Queued int `json:"queued"`
}
