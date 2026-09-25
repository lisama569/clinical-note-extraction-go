package segment

import (
	"strings"
	"testing"
)

// logDoc 打印原文片段与切分/异常依据，保证测试失败时可解释。
func logDoc(t *testing.T, name, text string, doc *Document) {
	t.Helper()
	t.Logf("---- [%s] 原文 ----\n%q", name, text)
	for _, is := range doc.Issues {
		t.Logf("文档问题: %s @[%d:%d] %s", is.Kind, is.Start, is.End, is.Message)
	}
	for _, sec := range doc.Sections {
		h := sec.Header
		if h == "" {
			h = "<匿名>"
		}
		t.Logf("小节#%d 标题=%q 标题区间[%d:%d]", sec.Index, h, sec.HeaderStart, sec.HeaderEnd)
		for _, is := range sec.Issues {
			t.Logf("  小节问题: %s %s", is.Kind, is.Message)
		}
		for _, s := range sec.Sentences {
			t.Logf("  句子[%d:%d] 归一=%q 原文=%q", s.Start, s.End, s.Text, text[s.Start:s.End])
		}
	}
}

func issueKinds(doc *Document) map[IssueKind]bool {
	m := map[IssueKind]bool{}
	for _, is := range doc.Issues {
		m[is.Kind] = true
	}
	return m
}

// TestSectionAndSentenceBasic 覆盖：分节、跨行合并、句子边界与字节偏移正确性。
func TestSectionAndSentenceBasic(t *testing.T) {
	text := "主诉：\n发热3天\n现病史：\n患者3天前出现发热，\n咳嗽持续2天。无胸痛。\n"
	doc := (&Splitter{}).Split(text)
	logDoc(t, "basic", text, doc)

	if len(doc.Sections) != 2 {
		t.Fatalf("期望2个小节，实际%d", len(doc.Sections))
	}
	if doc.Sections[0].Header != "主诉" || doc.Sections[1].Header != "现病史" {
		t.Fatalf("小节标题错误: %q %q", doc.Sections[0].Header, doc.Sections[1].Header)
	}
	// 主诉跨行/无结束符的内容不得丢弃
	if got := doc.Sections[0].Sentences[0].Text; got != "发热3天" {
		t.Fatalf("主诉句子错误: %q", got)
	}
	// 跨行内容合并为一个句子，换行折叠成空格
	sents := doc.Sections[1].Sentences
	if len(sents) != 2 {
		t.Fatalf("现病史期望2句，实际%d: %+v", len(sents), sents)
	}
	if sents[0].Text != "患者3天前出现发热， 咳嗽持续2天。" {
		t.Fatalf("跨行合并错误: %q", sents[0].Text)
	}
	// 偏移必须精确回溯原文
	if text[sents[1].Start:sents[1].End] != "无胸痛。" {
		t.Fatalf("偏移回溯错误: %q", text[sents[1].Start:sents[1].End])
	}
}

// TestNoHeaderContentPreserved 缺小节标题时内容不得静默丢弃。
func TestNoHeaderContentPreserved(t *testing.T) {
	text := "开头没有标题的一行\n现病史：\n咳嗽。\n"
	doc := (&Splitter{}).Split(text)
	logDoc(t, "no-header", text, doc)

	var noHeader bool
	for _, sec := range doc.Sections {
		for _, is := range sec.Issues {
			if is.Kind == IssueNoHeader {
				noHeader = true
			}
		}
	}
	if !noHeader {
		t.Fatal("缺少标题的正文必须以 NO_HEADER 显式标记")
	}
	if doc.Sections[0].Header != "" || doc.Sections[0].Sentences[0].Text != "开头没有标题的一行" {
		t.Fatalf("匿名小节内容未保留: %+v", doc.Sections[0])
	}
}

// TestDuplicateHeaderKept 重复小节必须分别保留并标记。
func TestDuplicateHeaderKept(t *testing.T) {
	text := "现病史：\n发热。\n现病史：\n咳嗽。\n"
	doc := (&Splitter{}).Split(text)
	logDoc(t, "dup-header", text, doc)

	var dup int
	var hpi int
	for _, sec := range doc.Sections {
		if sec.Header == "现病史" {
			hpi++
		}
		for _, is := range sec.Issues {
			if is.Kind == IssueDuplicateHeader {
				dup++
			}
		}
	}
	if hpi != 2 {
		t.Fatalf("重复标题应生成两个小节，实际%d", hpi)
	}
	if dup != 1 {
		t.Fatalf("第二次出现应标记1次 DUPLICATE_HEADER，实际%d", dup)
	}
}

// TestTruncatedLineDetected 缺尾换行且无结束符必须标记截断。
func TestTruncatedLineDetected(t *testing.T) {
	text := "主诉\n突发头痛"
	doc := (&Splitter{}).Split(text)
	logDoc(t, "truncated", text, doc)
	if !issueKinds(doc)[IssueTruncatedLine] {
		t.Fatal("无尾换行无结束符的末行必须标记 TRUNCATED_LINE")
	}
}

// TestProperEndingNotTruncated 正常以结束符+换行结尾不应误报截断。
func TestProperEndingNotTruncated(t *testing.T) {
	text := "现病史：\n咳嗽。\n"
	doc := (&Splitter{}).Split(text)
	if issueKinds(doc)[IssueTruncatedLine] {
		t.Fatal("正常结尾不应标记 TRUNCATED_LINE")
	}
}

// TestMalformedInputs 畸形文本：空、纯空白、含 NUL，原因必须可区分。
func TestMalformedInputs(t *testing.T) {
	cases := map[string]struct {
		text   string
		expect IssueKind
	}{
		"空串":   {"", IssueEmptyText},
		"纯空白":  {"  \n\t \r\n", IssueEmptyText},
		"含NUL": {"abc\x00def\n", IssueBinaryNUL},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			doc := (&Splitter{}).Split(tc.text)
			logDoc(t, name, tc.text, doc)
			if len(doc.Issues) == 0 || doc.Issues[0].Kind != tc.expect {
				t.Fatalf("期望问题 %s，实际 %+v", tc.expect, doc.Issues)
			}
			if tc.expect == IssueBinaryNUL && len(doc.Sections) != 0 {
				t.Fatal("二进制输入必须整体拒绝，不产出任何小节")
			}
		})
	}
}

// TestDeterministicRepeatedSplit 同一文本重复切分结果必须逐字节一致。
func TestDeterministicRepeatedSplit(t *testing.T) {
	text := "无标题首行\n现病史：\n发热，\n咳嗽。无胸痛。\n现病史：\n头痛"
	sp := &Splitter{}
	first := canonical(sp.Split(text))
	for i := 0; i < 20; i++ {
		if got := canonical(sp.Split(text)); got != first {
			t.Fatalf("第%d次切分结果与首次不一致", i+1)
		}
	}
}

// canonical 将文档压成稳定字符串用于逐字节比较。
func canonical(doc *Document) string {
	var b strings.Builder
	for _, is := range doc.Issues {
		b.WriteString(string(is.Kind) + "@" + itos(is.Start) + ":" + itos(is.End) + ";")
	}
	for _, sec := range doc.Sections {
		b.WriteString("SEC[" + sec.Header + "]")
		for _, is := range sec.Issues {
			b.WriteString(string(is.Kind) + ";")
		}
		for _, s := range sec.Sentences {
			b.WriteString("(" + itos(s.Start) + ":" + itos(s.End) + "=" + s.Text + ")")
		}
	}
	return b.String()
}

func itos(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	n := len(b)
	for i > 0 {
		n--
		b[n] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		n--
		b[n] = '-'
	}
	return string(b[n:])
}
