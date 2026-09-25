package pipeline

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/lisama569/clinical-note-extraction-go/internal/extract"
	"github.com/lisama569/clinical-note-extraction-go/internal/segment"
	"github.com/lisama569/clinical-note-extraction-go/internal/terminology"
)

// dictV1：DM 只有一个候选；不含“胸闷”词条。
func dictV1() *terminology.Dictionary {
	return &terminology.Dictionary{
		Version: "v1",
		Terms: []terminology.Term{
			{Surface: "胸痛", ConceptID: "C_CHEST_PAIN"},
			{Surface: "发热", ConceptID: "C_FEVER"},
		},
		Modifiers: []terminology.Modifier{
			{Surface: "无", Kind: terminology.Negation},
			{Surface: "疑似", Kind: terminology.Uncertainty},
		},
		Abbrev:     map[string][]terminology.AbbrevCandidate{"DM": {{ConceptID: "C_DIABETES"}}},
		SectionSet: map[string]bool{"主诉": true, "现病史": true},
	}
}

// dictV2：DM 新增候选（变为歧义）；新增“胸闷”词条。
func dictV2() *terminology.Dictionary {
	d := dictV1()
	d.Version = "v2"
	d.Terms = append(append([]terminology.Term{}, d.Terms...),
		terminology.Term{Surface: "胸闷", ConceptID: "C_CHEST_TIGHTNESS"})
	d.Abbrev = map[string][]terminology.AbbrevCandidate{
		"DM": {{ConceptID: "C_DIABETES"}, {ConceptID: "C_DERMATOMYOSITIS", Keywords: []string{"肌无力"}}},
	}
	return d
}

const docText = "主诉：胸闷。现病史：DM病史5年，无发热。"

func conclusionOf(r Result, concept string) extract.Polarity {
	for _, c := range r.Conclusions {
		if c.ConceptID == concept {
			return c.Polarity
		}
	}
	return ""
}

func hasPending(r Result) bool {
	for _, m := range r.Mentions {
		if m.Polarity == extract.Pending {
			return true
		}
	}
	return false
}

func TestVersionSwitchAndRollback(t *testing.T) {
	reg := terminology.NewRegistry(dictV1())
	reg.Register(dictV2())
	p := New(reg)

	r1 := p.Process("doc1", docText)
	t.Logf("v1 结果: version=%s conclusions=%+v", r1.TermVersion, r1.Conclusions)
	if r1.TermVersion != "v1" {
		t.Fatalf("应使用 v1，得到 %s", r1.TermVersion)
	}
	if conclusionOf(r1, "C_DIABETES") != extract.Affirmed {
		t.Errorf("v1 下 DM 应唯一解析为糖尿病")
	}
	if conclusionOf(r1, "C_CHEST_TIGHTNESS") != "" {
		t.Errorf("v1 不含胸闷词条，不应出现该结论")
	}

	reg.Switch("v2")
	r2 := p.Process("doc1", docText)
	t.Logf("v2 结果: version=%s conclusions=%+v mentions=%+v", r2.TermVersion, r2.Conclusions, r2.Mentions)
	if r2.TermVersion != "v2" {
		t.Fatalf("应使用 v2，得到 %s", r2.TermVersion)
	}
	if !hasPending(r2) {
		t.Errorf("v2 下 DM 变为歧义缩写，应出现待定提及")
	}
	if conclusionOf(r2, "C_CHEST_TIGHTNESS") != extract.Affirmed {
		t.Errorf("v2 新增胸闷词条，应得出肯定结论")
	}

	// 回退到 v1：同一文本的结论必须与首次 v1 结果完全一致。
	reg.Switch("v1")
	r3 := p.Process("doc1", docText)
	j1, _ := json.Marshal(r1)
	j3, _ := json.Marshal(r3)
	if string(j1) != string(j3) {
		t.Errorf("术语回退后历史结论被改变:\n%s\nvs\n%s", j1, j3)
	}
	// 升级前的历史结果对象本身不受影响（不可变快照）。
	if conclusionOf(r1, "C_DIABETES") != extract.Affirmed {
		t.Errorf("历史结果对象被后续版本切换污染")
	}
}

func TestConcurrentProcessingWithVersionSwitch(t *testing.T) {
	reg := terminology.NewRegistry(dictV1())
	reg.Register(dictV2())
	p := New(reg)

	var processors sync.WaitGroup
	stop := make(chan struct{})
	var switcher sync.WaitGroup
	switcher.Add(1)
	go func() { // 持续在 v1/v2 间切换，直到处理结束
		defer switcher.Done()
		for {
			select {
			case <-stop:
				return
			default:
				reg.Switch("v2")
				reg.Switch("v1")
			}
		}
	}()

	errs := make(chan string, 256)
	for g := 0; g < 8; g++ {
		processors.Add(1)
		go func(g int) {
			defer processors.Done()
			for i := 0; i < 200; i++ {
				r := p.Process("doc", docText)
				switch r.TermVersion {
				case "v1":
					// v1 整体判定：DM 唯一解析，且无胸闷词条。
					if conclusionOf(r, "C_DIABETES") != extract.Affirmed || hasPending(r) {
						errs <- "v1 结果混入 v2 判定"
					}
					if conclusionOf(r, "C_CHEST_TIGHTNESS") != "" {
						errs <- "v1 结果出现 v2 才有的胸闷结论"
					}
				case "v2":
					// v2 整体判定：DM 待定，胸闷肯定。
					if !hasPending(r) || conclusionOf(r, "C_CHEST_TIGHTNESS") != extract.Affirmed {
						errs <- "v2 结果混入 v1 判定"
					}
				default:
					errs <- "未知术语版本: " + r.TermVersion
				}
			}
		}(g)
	}
	processors.Wait() // 全部处理完成后再停止版本切换
	close(stop)
	switcher.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

func TestRepeatProcessingIdentical(t *testing.T) {
	p := New(terminology.NewRegistry(dictV1()))
	first, _ := json.Marshal(p.Process("d", docText))
	for i := 0; i < 50; i++ {
		cur, _ := json.Marshal(p.Process("d", docText))
		if string(first) != string(cur) {
			t.Fatalf("第 %d 次重复处理结果不一致:\n%s\nvs\n%s", i, first, cur)
		}
	}
}

func TestMalformedText(t *testing.T) {
	p := New(terminology.NewRegistry(dictV1()))
	cases := []struct {
		name string
		text string
		want segment.IssueCode
	}{
		{"无小节标题", "患者胸痛三天。", segment.IssueMissingHeader},
		{"句末截断", "主诉：胸痛", segment.IssueTruncated},
		{"重复小节", "主诉：胸痛。主诉：发热。", segment.IssueDuplicateSection},
	}
	for _, tc := range cases {
		r := p.Process("m", tc.text)
		found := false
		for _, s := range r.Segments {
			for _, is := range s.Issues {
				if is.Code == tc.want {
					found = true
				}
			}
			t.Logf("用例=%s 片段=%q issues=%v", tc.name, s.Text, s.Issues)
		}
		if !found {
			t.Errorf("用例 %s 应产生问题 %s", tc.name, tc.want)
		}
	}
	// 空文本与纯符号文本不得 panic，也不得有结论。
	for _, text := range []string{"", "   \n ", "！！！？？？", "主诉："} {
		r := p.Process("m", text)
		if len(r.Conclusions) != 0 {
			t.Errorf("文本 %q 不应产生结论: %+v", text, r.Conclusions)
		}
	}
}
