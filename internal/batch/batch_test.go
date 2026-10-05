package batch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"testing"

	"github.com/lisama569/clinical-note-extraction-go/internal/extract"
	"github.com/lisama569/clinical-note-extraction-go/internal/pipeline"
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

func newRunner(reg *terminology.Registry, conc int) *Runner {
	return &Runner{Pipeline: pipeline.New(reg), Registry: reg, MaxConcurrency: conc}
}

func newRegistry() *terminology.Registry {
	reg := terminology.NewRegistry(dictV1())
	reg.Register(dictV2())
	return reg
}

// docA 在两个版本下结论不同；docB 在两个版本下结论相同。
var (
	docA = Document{ID: "docA", Text: "主诉：胸闷。现病史：DM病史5年，无发热。"}
	docB = Document{ID: "docB", Text: "主诉：发热。"}
)

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	return string(b)
}

func TestBatchPartialFailure(t *testing.T) {
	r := newRunner(newRegistry(), 4)
	docs := []Document{
		docA,
		{ID: "", Text: "主诉：发热。"},     // 标识为空
		{ID: "docA", Text: "主诉：胸痛。"}, // 标识重复
		{ID: "docEmpty", Text: "   "},     // 内容为空
		docB,
	}
	rep := r.Run(context.Background(), "v1", docs)

	if rep.Summary.Total != 5 || rep.Summary.Succeeded != 2 || rep.Summary.Failed != 3 {
		t.Fatalf("汇总计数不符: %+v", rep.Summary)
	}
	// 结果按提交顺序一一对应。
	for i, d := range docs {
		if rep.Results[i].DocID != d.ID {
			t.Fatalf("第 %d 份结果顺序错乱: 期望 %q 得到 %q", i, d.ID, rep.Results[i].DocID)
		}
	}
	// 失败文档逐份标注原因，互不影响。
	for _, i := range []int{1, 2, 3} {
		res := rep.Results[i]
		if res.Status != StatusFailed || res.Err == "" {
			t.Errorf("第 %d 份应失败并注明原因: %+v", i, res)
		}
		t.Logf("失败文档 doc=%q 原因=%s", res.DocID, res.Err)
	}
	// 正常文档不受失败文档影响。
	for _, i := range []int{0, 4} {
		res := rep.Results[i]
		if res.Status != StatusOK || res.Result == nil || res.Result.TermVersion != "v1" {
			t.Errorf("第 %d 份应成功且使用 v1: %+v", i, res)
		}
	}
	if len(rep.Results[0].Result.Conclusions) == 0 {
		t.Errorf("docA 在 v1 下应有结论")
	}
}

func TestBatchUnknownVersion(t *testing.T) {
	r := newRunner(newRegistry(), 4)
	rep := r.Run(context.Background(), "v9", []Document{docA, docB})
	if rep.Summary.Failed != 2 || rep.Summary.Succeeded != 0 {
		t.Fatalf("未知版本应整批失败: %+v", rep.Summary)
	}
	for _, res := range rep.Results {
		if res.Status != StatusFailed || !strings.Contains(res.Err, "v9") {
			t.Errorf("应逐份标注版本不存在的原因: %+v", res)
		}
		t.Logf("doc=%s 失败原因=%s", res.DocID, res.Err)
	}
}

func TestBatchCancelBeforeStart(t *testing.T) {
	r := newRunner(newRegistry(), 4)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 开始前即取消
	rep := r.Run(ctx, "v1", []Document{docA, docB})
	if rep.Summary.NotProcessed != 2 || rep.Summary.Succeeded != 0 {
		t.Fatalf("取消后未轮到的文档应标记未处理: %+v", rep.Summary)
	}
	for _, res := range rep.Results {
		if res.Status != StatusNotProcessed || res.Result != nil {
			t.Errorf("未处理文档不应携带结果: %+v", res)
		}
	}
}

// cancelOnFirstOK 是一个 io.Writer：看到第一条成功日志时触发取消，
// 用于确定性地制造“部分文档已完成、其余未轮到”的中途取消场景。
type cancelOnFirstOK struct {
	cancel context.CancelFunc
	once   sync.Once
	buf    *bytes.Buffer
}

func (w *cancelOnFirstOK) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("status=ok")) {
		w.once.Do(w.cancel)
	}
	return w.buf.Write(p)
}

func TestBatchCancelMidRun(t *testing.T) {
	reg := newRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var logBuf bytes.Buffer
	r := &Runner{Pipeline: pipeline.New(reg), Registry: reg, MaxConcurrency: 2,
		Logger: log.New(&cancelOnFirstOK{cancel: cancel, buf: &logBuf}, "", 0)}

	var docs []Document
	for i := 0; i < 2000; i++ {
		docs = append(docs, Document{ID: fmt.Sprintf("doc%05d", i), Text: docA.Text})
	}
	rep := r.Run(ctx, "v1", docs)

	if rep.Summary.Failed != 0 {
		t.Fatalf("取消不应产生失败: %+v", rep.Summary)
	}
	if rep.Summary.Succeeded+rep.Summary.NotProcessed != rep.Summary.Total {
		t.Fatalf("成功+未处理应等于总数: %+v", rep.Summary)
	}
	if rep.Summary.Succeeded == 0 || rep.Summary.NotProcessed == 0 {
		t.Fatalf("应同时存在已完成与未处理的文档: %+v", rep.Summary)
	}
	for _, res := range rep.Results {
		switch res.Status {
		case StatusOK:
			// 已完成的文档结果完整：版本钉住、结论齐全、无半成品。
			if res.Result == nil || res.Result.TermVersion != "v1" || len(res.Result.Conclusions) == 0 {
				t.Fatalf("已完成文档 %s 的结果不完整: %+v", res.DocID, res)
			}
		case StatusNotProcessed:
			if res.Result != nil {
				t.Fatalf("未处理文档 %s 不应携带结果", res.DocID)
			}
		default:
			t.Fatalf("取消场景不应出现状态 %s（文档 %s）", res.Status, res.DocID)
		}
	}
	t.Logf("取消时完成情况: %+v", rep.Summary)
}

func TestBatchConcurrencyInvariance(t *testing.T) {
	docs := []Document{docA, docB,
		{ID: "docC", Text: "现病史：疑似胸痛，无发热。"},
		{ID: "docD", Text: "主诉：胸痛。现病史：DM病史2年。"},
	}
	var baseline string
	for _, conc := range []int{1, 2, 3, 16} {
		rep := newRunner(newRegistry(), conc).Run(context.Background(), "v2", docs)
		if rep.Summary.Succeeded != len(docs) {
			t.Fatalf("并发 %d 时应全部成功: %+v", conc, rep.Summary)
		}
		cur := mustJSON(t, rep)
		if baseline == "" {
			baseline = cur
			continue
		}
		if cur != baseline {
			t.Fatalf("并发 %d 的报告与基线不一致", conc)
		}
	}
}

func runPair(t *testing.T, concOld, concNew int, docs []Document) Comparison {
	t.Helper()
	reg := newRegistry()
	oldRep := newRunner(reg, concOld).Run(context.Background(), "v1", docs)
	newRep := newRunner(reg, concNew).Run(context.Background(), "v2", docs)
	return Compare(oldRep, newRep)
}

func TestCompareFindsExpectedChanges(t *testing.T) {
	cmp := runPair(t, 1, 1, []Document{docA, docB})
	if len(cmp.Skipped) != 0 {
		t.Fatalf("不应有跳过的文档: %+v", cmp.Skipped)
	}
	if len(cmp.Changes) != 3 {
		t.Fatalf("docA 应产生 3 条变化，得到 %d: %+v", len(cmp.Changes), cmp.Changes)
	}
	byKey := map[string]Change{}
	for _, c := range cmp.Changes {
		if c.DocID != "docA" {
			t.Fatalf("docB 两版结论相同，不应出现变化: %+v", c)
		}
		byKey[string(c.Kind)+":"+c.Subject] = c
		t.Logf("变化 doc=%s subject=%s kind=%s old=%s/%s new=%s/%s",
			c.DocID, c.Subject, c.Kind, c.Old.Version, c.Old.Polarity, c.New.Version, c.New.Polarity)
	}
	added, ok := byKey["added:C_CHEST_TIGHTNESS"]
	if !ok || added.New.Version != "v2" || added.New.Polarity != extract.Affirmed || added.New.Rationale == "" {
		t.Errorf("胸闷应为 v2 新增结论且可追溯到 v2 依据: %+v", added)
	}
	removed, ok := byKey["removed:C_DIABETES"]
	if !ok || removed.Old.Version != "v1" || removed.Old.Polarity != extract.Affirmed || removed.Old.Rationale == "" {
		t.Errorf("糖尿病应为仅 v1 出现的结论且可追溯到 v1 依据: %+v", removed)
	}
	d2p, ok := byKey["decided_to_pending:abbrev:DM"]
	if !ok || d2p.Old.Version != "v1" || d2p.New.Version != "v2" ||
		d2p.New.Polarity != extract.Pending || d2p.Old.Rationale == "" || d2p.New.Rationale == "" {
		t.Errorf("DM 应为可判定→待定，且两侧依据齐全: %+v", d2p)
	}
}

func TestCompareStableAcrossRunsAndConcurrency(t *testing.T) {
	docs := []Document{docA, docB,
		{ID: "docC", Text: "现病史：疑似胸痛，无发热。"},
		{ID: "docD", Text: "主诉：胸痛。现病史：DM病史2年。"},
	}
	base := runPair(t, 1, 1, docs)
	baseJSON := mustJSON(t, base)

	// 同样的输入与版本组合重复对照，结果必须完全一致。
	for i := 0; i < 5; i++ {
		if cur := mustJSON(t, runPair(t, 1, 1, docs)); cur != baseJSON {
			t.Fatalf("第 %d 次重复对照结果不一致", i)
		}
	}
	// 不同并发度、不同提交顺序，对照结果必须一致。
	shuffled := []Document{docs[2], docs[0], docs[3], docs[1]}
	for _, conc := range []int{2, 8} {
		if cur := mustJSON(t, runPair(t, conc, conc, shuffled)); cur != baseJSON {
			t.Fatalf("并发 %d / 乱序提交下对照结果不一致", conc)
		}
	}
}

func TestCompareSkipsFailedDocs(t *testing.T) {
	reg := newRegistry()
	r := newRunner(reg, 4)
	bad := Document{ID: "docBad", Text: ""}
	oldRep := r.Run(context.Background(), "v1", []Document{docA, bad})
	newRep := r.Run(context.Background(), "v2", []Document{docA, bad})
	cmp := Compare(oldRep, newRep)
	if len(cmp.Skipped) != 1 || cmp.Skipped[0].DocID != "docBad" {
		t.Fatalf("失败文档应列入跳过清单: %+v", cmp.Skipped)
	}
	if !strings.Contains(cmp.Skipped[0].Reason, "文档内容为空") {
		t.Errorf("跳过原因应可区分: %+v", cmp.Skipped[0])
	}
	if len(cmp.Changes) != 3 { // docA 的变化不受影响
		t.Fatalf("失败文档不应影响其它文档的对照: %+v", cmp.Changes)
	}
}

func TestBatchLoggingShowsVersionAndRationale(t *testing.T) {
	var buf bytes.Buffer
	reg := newRegistry()
	r := &Runner{Pipeline: pipeline.New(reg), Registry: reg, MaxConcurrency: 2,
		Logger: log.New(&buf, "", 0)}
	r.Run(context.Background(), "v2", []Document{docA, {ID: "docBad", Text: ""}})
	out := buf.String()
	t.Logf("批量日志:\n%s", out)
	for _, want := range []string{"doc=docA", "version=v2", "依据", "doc=docBad", "status=failed", "文档内容为空"} {
		if !strings.Contains(out, want) {
			t.Errorf("日志中应包含 %q", want)
		}
	}
	// 待定提及的判定依据也要出现在日志里（v2 下 DM 歧义）。
	if !strings.Contains(out, "待定提及") {
		t.Errorf("日志中应包含待定提及及其依据")
	}
}
