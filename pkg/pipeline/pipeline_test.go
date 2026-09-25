package pipeline

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/lisama569/clinical-note-extraction-go/pkg/extract"
)

// noteText 是一份本地构造、同时含否定作用域、缩写消歧、畸形要素的病历。
const noteText = "门诊首行缺标题\n" +
	"现病史：\n" +
	"无胸痛；有咳嗽。患者否认发热。\n" +
	"神经科随访MS。\n" +
	"既往史：\n" +
	"血糖控制不佳，DM 病史多年。\n" +
	"诊断：\n" +
	"MS" // 末行无换行、无结束符 => TRUNCATED_LINE

func proc(t *testing.T, p *Pipeline, ver, text string) *Output {
	t.Helper()
	ctx := context.Background()
	if ver != "" {
		ctx = WithVersion(ctx, ver)
	}
	o, err := p.Process(ctx, text)
	if err != nil {
		t.Fatalf("处理失败: %v", err)
	}
	return o
}

func logOutput(t *testing.T, o *Output) {
	t.Helper()
	t.Logf("\n%s", o.String())
}

// TestEndToEndStableReport 端到端：异常可区分、极性正确、报告含原文片段与依据。
func TestEndToEndStableReport(t *testing.T) {
	p := New(nil)
	o := proc(t, p, "v2", noteText)
	logOutput(t, o)

	if o.Version != "v2" {
		t.Fatalf("版本应为 v2，实际 %s", o.Version)
	}
	// 畸形/结构要素：缺标题、重复处理保留、截断
	issueMsg := o.String()
	if !strings.Contains(issueMsg, "NO_HEADER") {
		t.Fatal("必须报告缺小节标题 NO_HEADER")
	}
	if !strings.Contains(issueMsg, "TRUNCATED_LINE") {
		t.Fatal("必须报告末行截断 TRUNCATED_LINE")
	}
	f := o.FindingsByID()
	if g, ok := f["CHEST_PAIN"]; !ok || g.Polarity != extract.PolarityNegative {
		t.Fatal("胸痛应为 NEGATIVE")
	}
	if g, ok := f["COUGH"]; !ok || g.Polarity != extract.PolarityPositive {
		t.Fatal("咳嗽应为 POSITIVE（否定不跨子句）")
	}
	if g, ok := f["MULTIPLE_SCLEROSIS"]; !ok || g.Polarity != extract.PolarityPositive {
		t.Fatal("神经科上下文 MS 应为 MULTIPLE_SCLEROSIS/POSITIVE")
	}
	if g, ok := f["AMBIGUOUS:MS"]; !ok || g.Polarity != extract.PolarityAmbiguous {
		t.Fatal("诊断小节孤立 MS 必须 AMBIGUOUS 待定")
	}
	// v2 中 DM 缩写已下线，句中无“糖尿病”等长词，故不得命中 DIABETES
	if _, ok := f["DIABETES"]; ok {
		t.Fatal("v2 已下线 DM 缩写，且无长触发词，不应命中 DIABETES")
	}
}

// TestRepeatProcessingIdentical 同一份文本重复处理结论必须完全一致。
func TestRepeatProcessingIdentical(t *testing.T) {
	p := New(nil)
	first := proc(t, p, "v1", noteText).String()
	firstHash := proc(t, p, "v1", noteText).TextHash
	for i := 0; i < 10; i++ {
		o := proc(t, p, "v1", noteText)
		if o.String() != first {
			t.Fatalf("第%d次处理的报告与首次不一致", i+1)
		}
		if o.TextHash != firstHash {
			t.Fatal("同一文本的指纹必须一致")
		}
	}
}

// TestVersionSwitchHistoricalStability 升级/回退术语版本都要给出确定且符合该版本的结论，
// 且每个输出都明确标注所用版本（历史结论可按版本复现，不被新版本改写）。
func TestVersionSwitchHistoricalStability(t *testing.T) {
	p := New(nil)
	text := "既往史：\n血糖控制差，DM 病史多年。\n"

	v1 := proc(t, p, "v1", text)
	v2 := proc(t, p, "v2", text)
	back := proc(t, p, "v1", text)
	logOutput(t, v1)
	logOutput(t, v2)

	_, v1Hit := v1.FindingsByID()["DIABETES"]
	_, v2Hit := v2.FindingsByID()["DIABETES"]
	if !v1Hit {
		t.Fatal("v1 下 DM 缩写应命中 DIABETES")
	}
	if v2Hit {
		t.Fatal("v2 下 DM 缩写已下线，不应命中")
	}
	// 回退到 v1 后历史结论必须原样复现
	if back.String() != v1.String() {
		t.Fatal("回退术语版本后，历史文本结论必须与原 v1 结论逐字节一致")
	}
	if v1.Version != "v1" || v2.Version != "v2" {
		t.Fatal("每份输出必须显式标注所用术语版本")
	}
}

// TestUnknownVersionRejected 未知名词版本必须给出可区分错误，而不是静默用默认版本。
func TestUnknownVersionRejected(t *testing.T) {
	p := New(nil)
	_, err := p.Process(WithVersion(context.Background(), "v99"), "现病史：\n咳嗽。\n")
	if err == nil || !strings.Contains(err.Error(), "未知术语版本") {
		t.Fatalf("未知版本必须显式报错，实际 %v", err)
	}
}

// TestConcurrentProcessingNoMixing 并发处理多份文本并交错指定 v1/v2，
// 每份文本必须整体基于单一确定版本，不得出现新旧规则混合。
func TestConcurrentProcessingNoMixing(t *testing.T) {
	p := New(nil)
	const workers = 64
	const iter = 20

	textDM := "既往史：\n血糖控制差，DM 病史多年。\n"    // 仅 v1 命中 DIABETES
	textMS := "现病史：\n神经科随访MS。\n"          // v1/v2 均命中 MULTIPLE_SCLEROSIS
	textCOVID := "辅助检查：\n发热，COVID核酸阳性。\n" // 仅 v2 命中 COVID19

	var wg sync.WaitGroup
	var mu sync.Mutex
	var failures []string
	addFail := func(s string) {
		mu.Lock()
		failures = append(failures, s)
		mu.Unlock()
	}

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for k := 0; k < iter; k++ {
				ver := "v1"
				if (id+k)%2 == 0 {
					ver = "v2"
				}
				o := proc(t, p, ver, textDM)
				f := o.FindingsByID()
				_, hit := f["DIABETES"]
				if ver == "v1" && !hit {
					addFail("v1 必须命中 DM->DIABETES")
				}
				if ver == "v2" && hit {
					addFail("v2 不得命中 DM->DIABETES（出现新旧规则混合）")
				}
				if o.Version != ver {
					addFail("输出版本与请求版本不一致")
				}

				oc := proc(t, p, ver, textCOVID)
				_, chit := oc.FindingsByID()["COVID19"]
				if ver == "v2" && !chit {
					addFail("v2 必须命中 COVID->COVID19")
				}
				if ver == "v1" && chit {
					addFail("v1 不得命中 COVID 缩写（出现新旧规则混合）")
				}

				om := proc(t, p, ver, textMS)
				if _, ok := om.FindingsByID()["MULTIPLE_SCLEROSIS"]; !ok {
					addFail(ver + " 均应命中 MULTIPLE_SCLEROSIS")
				}
			}
		}(w)
	}
	wg.Wait()

	if len(failures) > 0 {
		t.Fatalf("并发处理出现版本混合，失败例如：%v", failures[:min(5, len(failures))])
	}
}

// TestConcurrentIdenticalTextConsistent 并发处理同一文本，所有结果必须一致。
func TestConcurrentIdenticalTextConsistent(t *testing.T) {
	p := New(nil)
	const n = 50
	results := make([]string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			o := proc(t, p, "v2", noteText)
			results[i] = o.String()
		}(i)
	}
	wg.Wait()
	for i := 1; i < n; i++ {
		if results[i] != results[0] {
			t.Fatalf("并发处理同一文本，第%d份结果不一致", i)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
