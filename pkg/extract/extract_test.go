package extract

import (
	"strconv"
	"strings"
	"testing"

	"github.com/lisama569/clinical-note-extraction-go/pkg/segment"
	"github.com/lisama569/clinical-note-extraction-go/pkg/terminology"
)

func engine(t *testing.T, ver string) (*Engine, *terminology.Version) {
	t.Helper()
	v, err := terminology.NewRegistry().Snapshot(ver)
	if err != nil {
		t.Fatal(err)
	}
	return NewEngine(v), v
}

// runExtract 切分并抽取，打印原文片段、命中规则与判定依据。
func runExtract(t *testing.T, name, ver, text string) *Result {
	t.Helper()
	doc := (&segment.Splitter{}).Split(text)
	eng, _ := engine(t, ver)
	res := eng.Extract(text, doc)
	t.Logf("==== [%s] version=%s 原文 ====\n%s", name, ver, text)
	for _, f := range res.Findings {
		t.Logf("结论 %s 极性=%s 确定性=%d", f.ConceptID, f.Polarity, f.Certainty)
		for _, m := range f.Mentions {
			t.Logf("  原句=%q 片段=%q[%d:%d] 方式=%s 极性=%s",
				m.SentenceText, m.Term, m.Start, m.End, m.MatchedBy, m.Polarity)
			for _, ev := range m.Evidence {
				t.Logf("      规则/依据: %s", ev)
			}
		}
	}
	return res
}

func polarity(res *Result, id string) Polarity {
	for _, f := range res.Findings {
		if f.ConceptID == id {
			return f.Polarity
		}
	}
	return ""
}

func hasFinding(res *Result, id string) bool {
	for _, f := range res.Findings {
		if f.ConceptID == id {
			return true
		}
	}
	return false
}

// TestNegationDoesNotCrossClause 否定不得跨越子句边界波及后续肯定表述。
func TestNegationDoesNotCrossClause(t *testing.T) {
	res := runExtract(t, "否定不跨子句", "v2", "现病史：\n无胸痛；有咳嗽。\n")
	if polarity(res, "CHEST_PAIN") != PolarityNegative {
		t.Fatal("胸痛必须为 NEGATIVE")
	}
	if polarity(res, "COUGH") != PolarityPositive {
		t.Fatal("咳嗽在分号后的肯定子句，必须为 POSITIVE，否定不得跨子句")
	}
}

// TestNegationDoesNotCrossSentence 否定不得跨句作用。
func TestNegationDoesNotCrossSentence(t *testing.T) {
	res := runExtract(t, "否定不跨句", "v2", "现病史：\n否认发热。咳嗽明显。\n")
	if polarity(res, "FEVER") != PolarityNegative {
		t.Fatal("发热必须 NEGATIVE")
	}
	if polarity(res, "COUGH") != PolarityPositive {
		t.Fatal("另一句中的咳嗽必须 POSITIVE，否定不得跨句")
	}
}

// TestNegationForwardWindow 超过作用窗口的否定不得影响远处的概念。
func TestNegationForwardWindow(t *testing.T) {
	// “无”与“发热”之间用大量字填充，超过 ForwardWindow(24 rune)。
	long := strings.Repeat("腹部", 20) // 40 rune
	text := "现病史：\n无" + long + "发热。\n"
	res := runExtract(t, "否定越窗", "v2", text)
	if polarity(res, "FEVER") != PolarityPositive {
		t.Fatal("超出前向窗口的否定不得生效，发热应为 POSITIVE")
	}
}

// TestUncertainSemantics 不确定词作用域内判定为 UNCERTAIN。
func TestUncertainSemantics(t *testing.T) {
	res := runExtract(t, "不确定", "v2", "查体：\n疑似咳嗽。\n")
	if polarity(res, "COUGH") != PolarityUncertain {
		t.Fatal("疑似咳嗽必须为 UNCERTAIN")
	}
}

// TestNegationBeatsUncertainInScope 同作用域否定与不确定并存时按规则仲裁。
func TestNegationBeatsUncertainInScope(t *testing.T) {
	// “可能无发热”：否定词“无”离发热更近 -> NEGATIVE
	res := runExtract(t, "可能无", "v2", "查体：\n可能无发热。\n")
	if polarity(res, "FEVER") != PolarityNegative {
		t.Fatal("可能无发热 应由更近的否定词判定为 NEGATIVE")
	}
}

// TestCertaintyPrioritySameFragment 同一片段同一概念多命中时确定性优先，而非任取。
func TestCertaintyPrioritySameFragment(t *testing.T) {
	// 同一句先“疑似发热”（不确定），后“无发热”（确定否定）-> 最终 NEGATIVE/确定。
	res := runExtract(t, "确定性优先", "v2", "现病史：\n疑似发热，但最终无发热。\n")
	if polarity(res, "FEVER") != PolarityNegative {
		t.Fatalf("确定否定必须压过不确定表述，实际 %s", polarity(res, "FEVER"))
	}
	var f *Finding
	for i := range res.Findings {
		if res.Findings[i].ConceptID == "FEVER" {
			f = &res.Findings[i]
		}
	}
	if f == nil || f.Certainty != CertaintyDefinite {
		t.Fatal("最终确定性级别必须为 Definite")
	}
}

// TestAbbrevContextDisambiguation 同一缩写在不同上下文解析为不同概念。
func TestAbbrevContextDisambiguation(t *testing.T) {
	text := "现病史：\n神经科随访MS。心脏超声提示MS。\n"
	res := runExtract(t, "MS消歧", "v2", text)
	if polarity(res, "MULTIPLE_SCLEROSIS") != PolarityPositive {
		t.Fatal("神经科上下文的 MS 必须解析为 MULTIPLE_SCLEROSIS")
	}
	if polarity(res, "MITRAL_STENOSIS") != PolarityPositive {
		t.Fatal("心脏超声上下文的 MS 必须解析为 MITRAL_STENOSIS")
	}
	if hasFinding(res, "AMBIGUOUS:MS") {
		t.Fatal("两个上下文都有唯一线索，不应产生待定")
	}
}

// TestAbbrevMetabolicContextV2 v2 新增义项的上下文解析。
func TestAbbrevMetabolicContextV2(t *testing.T) {
	res := runExtract(t, "MS代谢", "v2", "既往史：\n代谢综合征随访，复查血脂，记录MS。\n")
	if polarity(res, "METABOLIC_SYNDROME") != PolarityPositive {
		t.Fatal("代谢/血脂上下文的 MS 必须解析为 METABOLIC_SYNDROME")
	}
}

// TestAbbrevAmbiguousExplicit 无上下文线索或多候选并列必须显式待定。
func TestAbbrevAmbiguousExplicit(t *testing.T) {
	// 同一句里“神经”和“二尖瓣”同时出现，两个候选同分 -> 待定。
	res := runExtract(t, "MS同分歧义", "v2", "诊断：\n神经与二尖瓣评估，考虑MS。\n")
	if polarity(res, "AMBIGUOUS:MS") != PolarityAmbiguous {
		t.Fatal("同分候选必须显式标记 AMBIGUOUS:MS")
	}

	// 完全无上下文
	res2 := runExtract(t, "MS无上下文", "v2", "诊断：\nMS\n")
	if polarity(res2, "AMBIGUOUS:MS") != PolarityAmbiguous {
		t.Fatal("无上下文的 MS 在 v2（三个义项）必须待定")
	}
}

// TestAbbrevVersionSensitive 同一缩写在不同版本下的可用性不同。
func TestAbbrevVersionSensitive(t *testing.T) {
	text := "既往史：\n血糖控制差，随访DM。\n"
	r1 := runExtract(t, "DM@v1", "v1", text)
	r2 := runExtract(t, "DM@v2", "v2", text)
	if polarity(r1, "DIABETES") != PolarityPositive {
		t.Fatal("v1 中 DM 缩写应命中 DIABETES")
	}
	if hasFinding(r2, "DIABETES") {
		t.Fatal("v2 已下线 DM 缩写，且句中无长触发词，不应命中 DIABETES")
	}
}

// TestLongTriggerSurvivesAbbrevRemoval 缩写下线后长触发词仍可命中。
func TestLongTriggerSurvivesAbbrevRemoval(t *testing.T) {
	res := runExtract(t, "长触发词", "v2", "既往史：\n患者有明确糖尿病病史。\n")
	if polarity(res, "DIABETES") != PolarityPositive {
		t.Fatal("缩写下线不影响长触发词“糖尿病”命中")
	}
}

// TestCovidAbbrevVersionDifference COVID 缩写仅 v2 收录。
func TestCovidAbbrevVersionDifference(t *testing.T) {
	text := "辅助检查：\n发热待查，COVID核酸阳性。\n"
	r1 := runExtract(t, "COVID@v1", "v1", text)
	r2 := runExtract(t, "COVID@v2", "v2", text)
	if hasFinding(r1, "COVID19") {
		t.Fatal("v1 未收录 COVID 缩写，该句不应命中（仅长词 COVID-19 才命中）")
	}
	if polarity(r2, "COVID19") != PolarityPositive {
		t.Fatal("v2 收录 COVID 缩写，应命中 COVID19")
	}
}

// TestOffsetMappingAcrossNewline 跨行命中的字节偏移必须正确回溯原文。
func TestOffsetMappingAcrossNewline(t *testing.T) {
	text := "现病史：\n无明显\n胸痛。\n"
	res := runExtract(t, "跨行偏移", "v2", text)
	var f *Finding
	for i := range res.Findings {
		if res.Findings[i].ConceptID == "CHEST_PAIN" {
			f = &res.Findings[i]
		}
	}
	if f == nil {
		t.Fatal("应命中胸痛")
	}
	m := f.Mentions[0]
	if text[m.Start:m.End] != "胸痛" {
		t.Fatalf("偏移回溯错误: %q [%d:%d]", text[m.Start:m.End], m.Start, m.End)
	}
}

// TestDeterministicExtract 重复抽取必须完全一致。
func TestDeterministicExtract(t *testing.T) {
	text := "现病史：\n无胸痛；有咳嗽。神经科随访MS。\n"
	doc := (&segment.Splitter{}).Split(text)
	eng, _ := engine(t, "v2")
	first := findingSignature(eng.Extract(text, doc))
	for i := 0; i < 20; i++ {
		if got := findingSignature(eng.Extract(text, doc)); got != first {
			t.Fatalf("第%d次抽取结果不一致", i+1)
		}
	}
}

func findingSignature(res *Result) string {
	var b strings.Builder
	for _, f := range res.Findings {
		b.WriteString(f.ConceptID + ":" + string(f.Polarity) + ":" +
			string(rune('0'+f.Certainty)) + "|")
		for _, m := range f.Mentions {
			b.WriteString(m.Term + "@" + strconv.Itoa(m.Start) + "-" + strconv.Itoa(m.End) +
				":" + string(m.Polarity) + ";")
		}
	}
	return b.String()
}

// TestNegationEnumerationBoundary 顿号并列枚举中，否定只作用于被否定项。
func TestNegationEnumerationBoundary(t *testing.T) {
	res := runExtract(t, "枚举边界", "v2", "现病史：\n发热、咳嗽，无胸痛。\n")
	if polarity(res, "CHEST_PAIN") != PolarityNegative {
		t.Fatal("胸痛必须 NEGATIVE")
	}
	if polarity(res, "FEVER") != PolarityPositive {
		t.Fatal("发热在顿号另一侧，必须 POSITIVE，否定不得越过顿号")
	}
	if polarity(res, "COUGH") != PolarityPositive {
		t.Fatal("咳嗽在顿号另一侧，必须 POSITIVE，否定不得越过逗号")
	}
}
