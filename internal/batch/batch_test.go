package batch

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"strings"
	"testing"

	"github.com/lisama569/clinical-note-extraction-go/internal/extract"
	"github.com/lisama569/clinical-note-extraction-go/internal/pipeline"
	"github.com/lisama569/clinical-note-extraction-go/internal/terminology"
)

// dictV1：DM 唯一候选；AP 歧义；含“发热”词条；无“否认”修饰词。
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
		Abbrev: map[string][]terminology.AbbrevCandidate{
			"DM": {{ConceptID: "C_DIABETES"}},
			"AP": {{ConceptID: "C_ARTERIAL_P"}, {ConceptID: "C_AIRWAY_P"}},
		},
		SectionSet: map[string]bool{"主诉": true, "现病史": true},
	}
}

// dictV2：新增“胸闷”词条、删除“发热”词条、新增否定词“否认”；
// DM 变为歧义；AP 变为唯一候选。
func dictV2() *terminology.Dictionary {
	return &terminology.Dictionary{
		Version: "v2",
		Terms: []terminology.Term{
			{Surface: "胸痛", ConceptID: "C_CHEST_PAIN"},
			{Surface: "胸闷", ConceptID: "C_CHEST_TIGHTNESS"},
		},
		Modifiers: []terminology.Modifier{
			{Surface: "无", Kind: terminology.Negation},
			{Surface: "疑似", Kind: terminology.Uncertainty},
			{Surface: "否认", Kind: terminology.Negation},
		},
		Abbrev: map[string][]terminology.AbbrevCandidate{
			"DM": {{ConceptID: "C_DIABETES"}, {ConceptID: "C_DERMATOMYOSITIS", Keywords: []string{"肌无力"}}},
			"AP": {{ConceptID: "C_ARTERIAL_P"}},
		},
		SectionSet: map[string]bool{"主诉": true, "现病史": true},
	}
}

func newRunner(logger *log.Logger) *Runner {
	reg := terminology.NewRegistry(dictV1())
	reg.Register(dictV2())
	return NewRunner(pipeline.New(reg), logger)
}

func docStatus(r *Result, id string) DocResult {
	for _, d := range r.Docs {
		if d.DocID == id {
			return d
		}
	}
	return DocResult{}
}

func TestPartialFailure(t *testing.T) {
	r := newRunner(nil)
	docs := []Document{
		{ID: "ok1", Text: "主诉：胸痛。"},
		{ID: "", Text: "主诉：胸痛。"},   // 空标识
		{ID: "empty", Text: "   "}, // 空内容
		{ID: "ok2", Text: "主诉：发热。"},
		{ID: "ok2", Text: "主诉：胸闷。"}, // 重复标识（后者失败）
	}
	res := r.Run(context.Background(), docs, "v1", Options{Concurrency: 3})
	for _, d := range res.Docs {
		t.Logf("doc=%q status=%s err=%q", d.DocID, d.Status, d.Err)
	}

	if d := docStatus(res, "ok1"); d.Status != StatusOK || d.Result == nil || d.Result.TermVersion != "v1" {
		t.Errorf("ok1 应成功且使用 v1: %+v", d)
	}
	if d := res.Docs[1]; d.Status != StatusFailed || !strings.Contains(d.Err, "标识为空") {
		t.Errorf("空标识文档应失败并说明原因: %+v", d)
	}
	if d := docStatus(res, "empty"); d.Status != StatusFailed || !strings.Contains(d.Err, "内容为空") {
		t.Errorf("空内容文档应失败并说明原因: %+v", d)
	}
	// 重复标识：首次出现成功，第二次失败；两份互不影响。
	if d := res.Docs[3]; d.Status != StatusOK {
		t.Errorf("重复的首次出现应成功: %+v", d)
	}
	if d := res.Docs[4]; d.Status != StatusFailed || !strings.Contains(d.Err, "重复") {
		t.Errorf("重复标识的后者应失败: %+v", d)
	}
	s := res.Summary
	if s.Total != 5 || s.Succeeded != 2 || s.Failed != 3 || s.Cancelled != 0 {
		t.Errorf("汇总不符: %+v", s)
	}
}

func TestUnknownVersion(t *testing.T) {
	r := newRunner(nil)
	docs := []Document{{ID: "a", Text: "主诉：胸痛。"}, {ID: "b", Text: "主诉：发热。"}}
	res := r.Run(context.Background(), docs, "v9", Options{Concurrency: 2})
	for _, d := range res.Docs {
		t.Logf("doc=%s status=%s err=%q", d.DocID, d.Status, d.Err)
		if d.Status != StatusFailed || !strings.Contains(d.Err, "v9") {
			t.Errorf("版本不存在应逐文档标出失败: %+v", d)
		}
		if d.Result != nil {
			t.Errorf("失败文档不得携带部分结果: %+v", d.Result)
		}
	}
	if res.Summary.Failed != 2 || res.Summary.Succeeded != 0 {
		t.Errorf("汇总不符: %+v", res.Summary)
	}
}

// cancelWriter 在收到第一条日志时取消上下文，用于确定性地模拟中途取消。
type cancelWriter struct {
	cancel context.CancelFunc
	once   bool
}

func (w *cancelWriter) Write(p []byte) (int, error) {
	if !w.once {
		w.once = true
		w.cancel()
	}
	return len(p), nil
}

func TestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cw := &cancelWriter{cancel: cancel}
	logger := log.New(cw, "", 0)
	r := newRunner(logger)

	docs := make([]Document, 20)
	for i := range docs {
		docs[i] = Document{ID: strings.Repeat("d", 1) + string(rune('a'+i)), Text: "主诉：胸痛。"}
	}
	res := r.Run(ctx, docs, "v1", Options{Concurrency: 1})

	var ok, cancelled int
	for _, d := range res.Docs {
		switch d.Status {
		case StatusOK:
			ok++
			if d.Result == nil || d.Result.TermVersion != "v1" || len(d.Result.Conclusions) == 0 {
				t.Errorf("已完成文档结果不完整: %+v", d)
			}
		case StatusCancelled:
			cancelled++
			if d.Result != nil {
				t.Errorf("未处理文档不得携带结果: %+v", d)
			}
			if !strings.Contains(d.Err, "未处理") {
				t.Errorf("未处理文档应明确标注: %+v", d)
			}
		default:
			t.Errorf("文档状态不完整（半份文档）: %+v", d)
		}
	}
	t.Logf("取消时已完成 %d 份，未处理 %d 份", ok, cancelled)
	if ok == 0 {
		t.Fatalf("取消前应至少完成一份文档")
	}
	if ok+cancelled != len(docs) {
		t.Errorf("已完成与未处理之和应等于总数: ok=%d cancelled=%d", ok, cancelled)
	}
	if res.Summary.Succeeded != ok || res.Summary.Cancelled != cancelled {
		t.Errorf("汇总与实际不符: %+v", res.Summary)
	}

	// 已完成文档的结果必须与未取消的单独运行完全一致。
	solo := r.Run(context.Background(), docs[:ok], "v1", Options{Concurrency: 1})
	for i := 0; i < ok; i++ {
		j1, _ := json.Marshal(res.Docs[i].Result)
		j2, _ := json.Marshal(solo.Docs[i].Result)
		if string(j1) != string(j2) {
			t.Errorf("取消批次中第%d份结果与单独运行不一致", i)
		}
	}
}

func TestConcurrencyInvariance(t *testing.T) {
	r := newRunner(nil)
	docs := []Document{
		{ID: "d1", Text: "主诉：胸闷。"},
		{ID: "d2", Text: "主诉：发热。"},
		{ID: "d3", Text: "主诉：否认胸痛。"},
		{ID: "d4", Text: "现病史：DM病史5年。"},
		{ID: "d5", Text: "现病史：AP升高。"},
		{ID: "bad", Text: ""},
		{ID: "d6", Text: "主诉：胸痛，无发热。"},
	}
	var reference string
	for _, conc := range []int{1, 2, 4, 16} {
		res := r.Run(context.Background(), docs, "v2", Options{Concurrency: conc})
		j, _ := json.Marshal(res)
		t.Logf("concurrency=%d summary=%+v", conc, res.Summary)
		if reference == "" {
			reference = string(j)
			continue
		}
		if string(j) != reference {
			t.Errorf("并发度 %d 的结果与并发度 1 不一致", conc)
		}
	}
}

func TestRunLogsVersionAndRationale(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)
	r := newRunner(logger)
	docs := []Document{{ID: "x", Text: "现病史：DM病史5年，无发热。"}}
	res := r.Run(context.Background(), docs, "v1", Options{Concurrency: 1})
	if res.Summary.Succeeded != 1 {
		t.Fatalf("应成功: %+v", res.Summary)
	}
	out := buf.String()
	t.Logf("日志输出:\n%s", out)
	if !strings.Contains(out, "doc=x") || !strings.Contains(out, "version=v1") {
		t.Errorf("日志应包含文档标识与版本: %s", out)
	}
	if !strings.Contains(out, "rationale=") || !strings.Contains(out, "C_DIABETES") {
		t.Errorf("日志应包含判定依据与结论: %s", out)
	}
}

func TestVersionPinnedPerDocument(t *testing.T) {
	// 批内指定 v1 时，即使注册表当前版本是 v2，每份文档也必须整体用 v1。
	reg := terminology.NewRegistry(dictV1())
	reg.Register(dictV2())
	if !reg.Switch("v2") {
		t.Fatal("切换到 v2 失败")
	}
	r := NewRunner(pipeline.New(reg), nil)
	docs := []Document{
		{ID: "a", Text: "现病史：DM病史5年。"},
		{ID: "b", Text: "主诉：胸闷。现病史：DM病史5年。"},
	}
	res := r.Run(context.Background(), docs, "v1", Options{Concurrency: 2})
	for _, d := range res.Docs {
		if d.Status != StatusOK || d.Result.TermVersion != "v1" {
			t.Fatalf("文档应整体使用 v1: %+v", d)
		}
		for _, m := range d.Result.Mentions {
			if m.Polarity == extract.Pending {
				t.Errorf("v1 下不应出现 v2 才有的待定判定: %+v", m)
			}
		}
		for _, c := range d.Result.Conclusions {
			if c.ConceptID == "C_CHEST_TIGHTNESS" {
				t.Errorf("v1 下不应出现 v2 才有的胸闷结论: %+v", c)
			}
		}
	}
}
