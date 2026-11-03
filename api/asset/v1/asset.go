package v1

import (
	"github.com/gogf/gf/v2/frame/g"
	"github.com/gogf/gf/v2/net/ghttp"

	"multimo-backend/internal/model/entity"
)

// AssetItem is an asset as returned by the API.
type AssetItem struct {
	*entity.Asset
	ThumbnailUrl string `json:"thumbnailUrl,omitempty" dc:"缩略图地址"`
}

type UploadReq struct {
	g.Meta     `path:"/assets" method:"post" mime:"multipart/form-data" tags:"Asset" summary:"上传文件" dc:"支持 MP4/WMV/FLV/MOV/MKV/AVI、JPEG/PNG/TIFF(GeoTIFF)、PDF/DOC/DOCX/TXT/CSV/GeoJSON。同一集合内内容相同的文件只保存一次"`
	File       *ghttp.UploadFile `json:"file"       type:"file" v:"required#请选择文件" dc:"要上传的文件"`
	Sidecar    *ghttp.UploadFile `json:"sidecar"    type:"file" dc:"配套文件，如 DJI 飞行记录 CSV"`
	Collection string            `json:"collection" d:"default" dc:"集合名"`
	Type       string            `json:"type"       v:"in:image,remote_sensing#type 只能是 image 或 remote_sensing" dc:"把图像标为遥感影像（或反之）"`
}
type UploadRes struct {
	Asset   *AssetItem `json:"asset"`
	Created bool       `json:"created" dc:"false 表示已存在相同内容的文件"`
}

type RegisterItem struct {
	Path       string         `json:"path"       v:"required" dc:"TESTDATA_DIR 下的相对路径"`
	Collection string         `json:"collection" dc:"集合名，默认 default"`
	Type       string         `json:"type"       dc:"image 或 remote_sensing，用于覆盖自动识别"`
	Sidecar    string         `json:"sidecar"    dc:"配套文件路径（TESTDATA_DIR 下）"`
	Metadata   map[string]any `json:"metadata"   dc:"附加元数据，目前支持 time（拍摄时间）"`
}
type RegisterReq struct {
	g.Meta `path:"/assets/register" method:"post" tags:"Asset" summary:"登记服务器上的文件" dc:"登记 TESTDATA_DIR 中已有的文件，不复制文件本身，用于批量导入测试数据"`
	Items  []RegisterItem `json:"items" v:"required|min-length:1" dc:"最多 1000 项"`
}
type RegisterResult struct {
	Path    string `json:"path"`
	AssetId int64  `json:"assetId,omitempty"`
	Created bool   `json:"created"`
	Error   string `json:"error,omitempty"`
}
type RegisterRes struct {
	Results []RegisterResult `json:"results"`
	Created int              `json:"created"`
	Failed  int              `json:"failed"`
}

type ListReq struct {
	g.Meta     `path:"/assets" method:"get" tags:"Asset" summary:"素材列表"`
	Type       string `json:"type"       dc:"video | image | remote_sensing | document"`
	Collection string `json:"collection"`
	Status     string `json:"status"     dc:"pending | processing | ready | failed"`
	Keyword    string `json:"keyword"    dc:"按文件名模糊匹配"`
	Page       int    `json:"page"       d:"1"`
	Size       int    `json:"size"       d:"20" v:"max:200"`
}
type ListRes struct {
	List  []*AssetItem `json:"list"`
	Total int          `json:"total"`
	Page  int          `json:"page"`
	Size  int          `json:"size"`
}

type GetReq struct {
	g.Meta `path:"/assets/{id}" method:"get" tags:"Asset" summary:"素材详情"`
	Id     int64 `json:"id" in:"path" v:"required"`
}
type AssetCounts struct {
	Keyframes  int `json:"keyframes"`
	Detections int `json:"detections"`
	Chunks     int `json:"chunks"`
}
type GetRes struct {
	Asset  *AssetItem     `json:"asset"`
	Tasks  []*entity.Task `json:"tasks"`
	Counts AssetCounts    `json:"counts"`
}

type DeleteReq struct {
	g.Meta `path:"/assets/{id}" method:"delete" tags:"Asset" summary:"删除素材" dc:"删除记录及派生文件；登记的原始文件保留"`
	Id     int64 `json:"id" in:"path" v:"required"`
}
type DeleteRes struct{}

type ReprocessReq struct {
	g.Meta `path:"/assets/{id}/reprocess" method:"post" tags:"Asset" summary:"重新处理素材"`
	Id     int64 `json:"id" in:"path" v:"required"`
}
type ReprocessRes struct{}

type ThumbnailReq struct {
	g.Meta `path:"/assets/{id}/thumbnail" method:"get" tags:"Asset" summary:"缩略图" dc:"返回 JPEG"`
	Id     int64 `json:"id" in:"path" v:"required"`
}
type ThumbnailRes struct{}
