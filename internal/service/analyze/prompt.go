package analyze

import (
	"fmt"
	"slices"
	"strings"
)

// Labels is the detection vocabulary: VisDrone classes for drone footage and
// DOTA classes for remote sensing.
var Labels = map[string]string{
	"pedestrian": "行人", "people": "人员（非站立）", "bicycle": "自行车", "car": "小汽车", "van": "面包车",
	"truck": "卡车", "tricycle": "三轮车", "awning-tricycle": "带篷三轮车", "bus": "公交车", "motor": "摩托车",
	"plane": "飞机", "ship": "船只", "storage-tank": "储油罐", "baseball-diamond": "棒球场",
	"tennis-court": "网球场", "basketball-court": "篮球场", "ground-track-field": "田径场", "harbor": "港口",
	"bridge": "桥梁", "large-vehicle": "大型车辆", "small-vehicle": "小型车辆", "helicopter": "直升机",
	"roundabout": "环岛", "soccer-ball-field": "足球场", "swimming-pool": "游泳池",
	"container-crane": "集装箱起重机", "airport": "机场", "helipad": "直升机停机坪",
}

var attrZH = map[string]string{
	"day": "白天", "night": "夜间", "dusk": "黄昏", "clear": "晴", "cloudy": "多云", "fog": "雾天",
	"rain": "雨天", "snow": "雪天", "nadir": "垂直俯视", "oblique": "斜视", "side": "侧视", "front": "前视",
	"low": "低空", "medium": "中空", "high": "高空",
}

// hintFor tells the model what kind of picture it is looking at.
func hintFor(assetType string) string {
	switch assetType {
	case "video":
		return "无人机航拍视频的截帧"
	case "remote_sensing":
		return "卫星或航空遥感影像"
	default:
		return "无人机航拍照片"
	}
}

// Measured on VisDrone-DET (99 visible objects, 1280 px): precision 0.89,
// recall 0.80 at IoU 0.5; 65 s and ~4100 output tokens for a dense frame.
const promptTemplate = `这是一张{HINT}。请仔细观察后，只返回一个 JSON 对象，字段如下：

{
  "description": "一到两句中文，客观描述场景、主要目标和布局，不要猜测看不见的内容",
  "scene": ["场景标签，中文，2~6 个，如 城市道路、十字路口、停车场、居民区、港口、机场、河流、农田"],
  "time_of_day": "day | night | dusk | unknown",
  "weather": "clear | cloudy | fog | rain | snow | unknown",
  "view": "nadir（垂直俯视） | oblique（斜视） | side（侧视） | front（前视） | unknown",
  "altitude": "low | medium | high | unknown（相对拍摄高度）",
  "objects": [{"label": "类别", "bbox_2d": [x1, y1, x2, y2]}],
  "counts": {"类别": 数量},
  "landmarks": ["可辨认的地标或特征建筑，中文；没有则为空数组"],
  "texts": ["画面中能读出的文字，如招牌、路牌；没有则为空数组"],
  "place_guess": {"name": "能判断的城市或地区名，判断不了就填空字符串", "confidence": 0.0}
}

目标检测要求：
- label 只能取以下英文类别：{LABELS}。
- pedestrian 是站立或行走的人，people 是坐着、骑车等非站立的人；van 是面包车，car 是轿车和 SUV。
- 无人机照片中的车辆用 car/van/truck/bus；遥感影像中的车辆用 small-vehicle/large-vehicle。
- 逐个框出所有清晰可见的目标，包括较小的目标，最多 120 个；被严重遮挡、看不清的不要框。
- bbox_2d 使用相对坐标，取值 0~1000：x 为横向位置 ×1000/图像宽度，y 为纵向位置 ×1000/图像高度。
- counts 统计画面中每个类别的总数（可以超过框的数量）。`

// labelOrder lists the detection vocabulary in prompt order.
var labelOrder = []string{"pedestrian", "people", "bicycle", "car", "van", "truck", "tricycle", "awning-tricycle", "bus",
	"motor", "plane", "ship", "storage-tank", "baseball-diamond", "tennis-court", "basketball-court",
	"ground-track-field", "harbor", "bridge", "large-vehicle", "small-vehicle", "helicopter", "roundabout",
	"soccer-ball-field", "swimming-pool", "container-crane", "airport", "helipad"}

func buildPrompt(assetType string) string {
	return strings.NewReplacer("{HINT}", hintFor(assetType), "{LABELS}", strings.Join(labelOrder, ", ")).Replace(promptTemplate)
}

// DetectorLabels are the classes the VisDrone detector reports. When the
// detector handles a keyframe, these counts come from it.
var DetectorLabels = []string{"pedestrian", "people", "bicycle", "car", "van", "truck", "tricycle",
	"awning-tricycle", "bus", "motor"}

// buildPromptWithDetections asks the VLM for the description and attributes
// only: the boxes come from the detector, whose counts it is given so the
// caption and counts agree with the boxes.
func buildPromptWithDetections(assetType string, counts map[string]int) string {
	p := buildPrompt(assetType)
	p = strings.Replace(p, "  \"objects\": [{\"label\": \"类别\", \"bbox_2d\": [x1, y1, x2, y2]}],\n", "", 1)
	if i := strings.Index(p, "目标检测要求："); i >= 0 {
		p = p[:i]
	}
	var found []string
	for _, l := range DetectorLabels {
		if counts[l] > 0 {
			found = append(found, fmt.Sprintf("%s（%s）%d", Labels[l], l, counts[l]))
		}
	}
	summary := "没有检测到目标"
	if len(found) > 0 {
		summary = strings.Join(found, "、")
	}
	other := make([]string, 0, len(Labels))
	for _, l := range labelOrder {
		if !slices.Contains(DetectorLabels, l) {
			other = append(other, l)
		}
	}
	return p + "目标框已由专用检测模型给出，不需要输出 objects。检测结果：" + summary + "。\n" +
		"- description 中提到这些类别的数量时以检测结果为准，不要自己重新数。\n" +
		"- counts 中 " + strings.Join(DetectorLabels, "/") + " 直接照抄检测结果；画面中其他类别的目标按以下英文类别补充：" +
		strings.Join(other, ", ") + "。"
}
