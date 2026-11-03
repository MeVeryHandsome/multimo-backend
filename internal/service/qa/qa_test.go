package qa

import (
	"reflect"
	"strings"
	"testing"
)

func TestMentionTokens(t *testing.T) {
	cases := map[string][]string{
		"视频 uav0000086_00000_v 中最多的目标类型是什么？":         {"uav0000086_00000_v"},
		"航拍图像 01_0101.JPG 是在什么位置拍摄的？":                {"01_0101.JPG"},
		"Palos Verdes 这段无人机视频是用哪款无人机拍摄的？":            {"Palos", "Verdes", "palos_verdes"},
		"按美国 Part 107 的高度限制，drone_palos_verdes 是否超限": {"drone_palos_verdes"},
		"2025年2月拍摄的视频":                               nil,
	}
	for q, want := range cases {
		if got := MentionTokens(q); !reflect.DeepEqual(got, want) {
			t.Errorf("%s -> %v, want %v", q, got, want)
		}
	}
}

func TestFallbackAnswerUsesBestEvidence(t *testing.T) {
	p := 0.9
	ev := []*Evidence{{N: 1, Text: "无关"}, {N: 2, Text: "地速不得超过87节", Relevance: &p}}
	if got := fallbackAnswer(ev); !strings.Contains(got, "87节") || !strings.Contains(got, `"citations":[2]`) {
		t.Fatalf("fallback = %s", got)
	}
}
