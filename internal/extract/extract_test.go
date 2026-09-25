package extract

import (
	"testing"

	"github.com/lisama569/clinical-note-extraction-go/internal/segment"
	"github.com/lisama569/clinical-note-extraction-go/internal/terminology"
)

func testDict() *terminology.Dictionary {
	return &terminology.Dictionary{
		Version: "v1",
		Terms: []terminology.Term{
			{Surface: "胸痛", ConceptID: "C_CHEST_PAIN"},
			{Surface: "发热", ConceptID: "C_FEVER"},
			{Surface: "肺炎", ConceptID: "C_PNEUMONIA"},
		},
		Modifiers: []terminology.Modifier{
			{Surface: "未见", Kind: terminology.Negation},
			{Surface: "无", Kind: terminology.Negation},
			{Surface: "否认", Kind: terminology.Negation},
			{Surface: "疑似", Kind: terminology.Uncertainty},
			{Surface: "可能", Kind: terminology.Uncertainty},
		},
		Abbrev: map[string][]terminology.AbbrevCandidate{
			"PT": {
				{ConceptID: "C_PHYSICAL_THERAPY", Sections: []string{"治疗"}, Keywords: []string{"康复"}},
				{ConceptID: "C_PROTHROMBIN_TIME", Sections: []string{"检验"}, Keywords: []string{"凝血"}},
			},
			"DM": {{ConceptID: "C_DIABETES"}},
		},
		SectionSet: map[string]bool{"主诉": true, "现病史": true, "检验": true, "治疗": true},
	}
}

func extractOne(t *testing.T, text string) ([]Mention, []Conclusion) {
	t.Helper()
	d := testDict()
	segs := segment.Split(text, d.SectionSet)
	ms, cs := Extract(segs, d)
	for _, m := range ms {
		t.Logf("原文片段=%q 命中=%q 概念=%s 极性=%s 判定依据=%s", m.Segment.Text, m.Surface, m.ConceptID, m.Polarity, m.Rationale)
	}
	for _, c := range cs {
		t.Logf("结论 概念=%s 极性=%s 依据=%s", c.ConceptID, c.Polarity, c.Rationale)
	}
	return ms, cs
}

func polarityOfConcept(ms []Mention, concept string) Polarity {
	for _, m := range ms {
		if m.ConceptID == concept {
			return m.Polarity
		}
	}
	return ""
}

func TestNegationScopeSameClause(t *testing.T) {
	ms, _ := extractOne(t, "现病史：无胸痛，伴发热。")
	if p := polarityOfConcept(ms, "C_CHEST_PAIN"); p != Negated {
		t.Errorf("胸痛应被否定，得到 %s", p)
	}
	if p := polarityOfConcept(ms, "C_FEVER"); p != Affirmed {
		t.Errorf("否定不得波及另一分句的发热，得到 %s", p)
	}
}

func TestNegationDoesNotReachBack(t *testing.T) {
	ms, _ := extractOne(t, "现病史：胸痛，无发热。")
	if p := polarityOfConcept(ms, "C_CHEST_PAIN"); p != Affirmed {
		t.Errorf("否定词之后的概念才被否定，胸痛应为肯定，得到 %s", p)
	}
	if p := polarityOfConcept(ms, "C_FEVER"); p != Negated {
		t.Errorf("发热应被否定，得到 %s", p)
	}
}

func TestResetWordEndsScope(t *testing.T) {
	ms, _ := extractOne(t, "现病史：无胸痛但伴发热。")
	if p := polarityOfConcept(ms, "C_CHEST_PAIN"); p != Negated {
		t.Errorf("胸痛应被否定，得到 %s", p)
	}
	if p := polarityOfConcept(ms, "C_FEVER"); p != Affirmed {
		t.Errorf("转折词切断否定作用域，发热应为肯定，得到 %s", p)
	}
}

func TestUncertaintyAndNegationPrecedence(t *testing.T) {
	ms, _ := extractOne(t, "现病史：疑似肺炎。")
	if p := polarityOfConcept(ms, "C_PNEUMONIA"); p != Uncertain {
		t.Errorf("疑似应判为不确定，得到 %s", p)
	}
	ms, _ = extractOne(t, "现病史：未见明确肺炎。")
	if p := polarityOfConcept(ms, "C_PNEUMONIA"); p != Negated {
		t.Errorf("同一作用域内否定优先于不确定，得到 %s", p)
	}
}

func TestCertaintyPriorityAcrossMentions(t *testing.T) {
	_, cs := extractOne(t, "主诉：胸痛。现病史：无胸痛。")
	if len(cs) != 1 || cs[0].Polarity != Affirmed {
		t.Fatalf("同一概念多次提及应按确定性优先级取肯定: %+v", cs)
	}
}

func TestAbbrevDisambiguation(t *testing.T) {
	// 小节线索
	ms, _ := extractOne(t, "治疗：PT每日一次，康复训练。")
	if p := polarityOfConcept(ms, "C_PHYSICAL_THERAPY"); p != Affirmed {
		t.Errorf("PT 在治疗小节应解析为物理治疗: %+v", ms)
	}
	// 关键词线索
	ms, _ = extractOne(t, "检验：PT延长，凝血功能异常。")
	if p := polarityOfConcept(ms, "C_PROTHROMBIN_TIME"); p != Affirmed {
		t.Errorf("PT 在凝血上下文应解析为凝血酶原时间: %+v", ms)
	}
	// 无法判定 → 待定
	ms, _ = extractOne(t, "现病史：PT待查。")
	found := false
	for _, m := range ms {
		if m.Polarity == Pending {
			found = true
		}
	}
	if !found {
		t.Errorf("无线索的 PT 必须显式标记待定: %+v", ms)
	}
	// 唯一候选直接解析
	ms, _ = extractOne(t, "现病史：DM病史5年。")
	if p := polarityOfConcept(ms, "C_DIABETES"); p != Affirmed {
		t.Errorf("DM 应解析为糖尿病: %+v", ms)
	}
}

func TestExtractDeterministic(t *testing.T) {
	text := "主诉：胸痛。现病史：无发热，疑似肺炎。检验：PT延长，凝血异常。"
	d := testDict()
	a1, c1 := Extract(segment.Split(text, d.SectionSet), d)
	a2, c2 := Extract(segment.Split(text, d.SectionSet), d)
	if len(a1) != len(a2) || len(c1) != len(c2) {
		t.Fatal("重复抽取结果不一致")
	}
	for i := range a1 {
		x, y := a1[i], a2[i]
		if x.ConceptID != y.ConceptID || x.Polarity != y.Polarity || x.Rationale != y.Rationale ||
			x.Surface != y.Surface || x.Segment.Text != y.Segment.Text || x.Segment.Start != y.Segment.Start {
			t.Fatalf("提及不一致: %+v vs %+v", x, y)
		}
	}
	for i := range c1 {
		if c1[i] != c2[i] {
			t.Fatalf("结论不一致: %+v vs %+v", c1[i], c2[i])
		}
	}
}
