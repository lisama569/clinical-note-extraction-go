package segment

import (
	"strings"
	"testing"
)

var testSections = map[string]bool{"主诉": true, "现病史": true, "查体": true}

func hasIssue(s Segment, c IssueCode) bool {
	for _, is := range s.Issues {
		if is.Code == c {
			return true
		}
	}
	return false
}

func TestSplitBasicSectionsAndOffsets(t *testing.T) {
	text := "主诉：胸痛3天。现病史：患者昨日出现胸痛，伴大汗。查体：心肺听诊未闻及异常！"
	segs := Split(text, testSections)
	if len(segs) != 3 {
		t.Fatalf("期望 3 个片段，得到 %d: %+v", len(segs), segs)
	}
	for _, s := range segs {
		t.Logf("片段 section=%q [%d,%d) text=%q issues=%v", s.Section, s.Start, s.End, s.Text, s.Issues)
		if text[s.Start:s.End] != s.Text {
			t.Errorf("偏移与原文不一致: %q vs %q", s.Text, text[s.Start:s.End])
		}
	}
	if segs[0].Section != "主诉" || segs[1].Section != "现病史" || segs[2].Section != "查体" {
		t.Errorf("小节归属错误: %q %q %q", segs[0].Section, segs[1].Section, segs[2].Section)
	}
}

func TestSplitMissingHeader(t *testing.T) {
	text := "患者自行来院。主诉：头晕。"
	segs := Split(text, testSections)
	if len(segs) != 2 {
		t.Fatalf("期望 2 个片段，得到 %d", len(segs))
	}
	t.Logf("首片段 section=%q text=%q issues=%v", segs[0].Section, segs[0].Text, segs[0].Issues)
	if segs[0].Section != PreambleSection || !hasIssue(segs[0], IssueMissingHeader) {
		t.Errorf("无标题正文应归入 _preamble 并标记 missing_section_header: %+v", segs[0])
	}
}

func TestSplitDuplicateSection(t *testing.T) {
	text := "主诉：胸痛。现病史：无发热。主诉：补述头痛。"
	segs := Split(text, testSections)
	last := segs[len(segs)-1]
	t.Logf("重复小节课片段 text=%q issues=%v", last.Text, last.Issues)
	if last.Section != "主诉" || !hasIssue(last, IssueDuplicateSection) {
		t.Errorf("重复小节应保留并标记 duplicate_section: %+v", last)
	}
	if !strings.Contains(last.Text, "补述头痛") {
		t.Errorf("重复小节内容不得被丢弃: %q", last.Text)
	}
}

func TestSplitCrossLineAndTruncation(t *testing.T) {
	text := "现病史：患者三天前\n开始出现胸闷，\n休息后缓解。查体：未见明显异常"
	segs := Split(text, testSections)
	if len(segs) != 2 {
		t.Fatalf("期望 2 个片段，得到 %d: %+v", len(segs), segs)
	}
	t.Logf("跨行片段 text=%q issues=%v", segs[0].Text, segs[0].Issues)
	if !strings.Contains(segs[0].Text, "\n") {
		t.Errorf("跨行内容应保留在同一片段: %q", segs[0].Text)
	}
	t.Logf("截断片段 text=%q issues=%v", segs[1].Text, segs[1].Issues)
	if !hasIssue(segs[1], IssueTruncated) {
		t.Errorf("句末无终止符应标记 truncated_sentence: %+v", segs[1])
	}
}

func TestSplitDeterministic(t *testing.T) {
	text := "主诉：胸痛。主诉：头痛。现病史：无发热\n伴咳嗽"
	a, b := Split(text, testSections), Split(text, testSections)
	if len(a) != len(b) {
		t.Fatal("重复处理长度不一致")
	}
	for i := range a {
		if a[i].Text != b[i].Text || a[i].Start != b[i].Start || a[i].End != b[i].End ||
			a[i].Section != b[i].Section || len(a[i].Issues) != len(b[i].Issues) {
			t.Fatalf("重复处理结果不一致: %+v vs %+v", a[i], b[i])
		}
	}
}

func TestSplitEmpty(t *testing.T) {
	if segs := Split("  \n\t ", testSections); len(segs) != 0 {
		t.Errorf("纯空白文本应无片段: %+v", segs)
	}
}
