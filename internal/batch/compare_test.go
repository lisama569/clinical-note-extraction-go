package batch

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lisama569/clinical-note-extraction-go/internal/extract"
)

// compareDocs 覆盖全部五种变化类型 + 两版一致。
var compareDocs = []Document{
	{ID: "added", Text: "主诉：胸闷。"},           // v2 新增词条 → added
	{ID: "removed", Text: "主诉：发热。"},         // v2 删除词条 → removed
	{ID: "polarity", Text: "主诉：否认胸痛。"},      // v2 新增否定词 → polarity_changed
	{ID: "to-pending", Text: "现病史：DM病史5年。"}, // v2 缩写变歧义 → became_pending
	{ID: "to-resolved", Text: "现病史：AP升高。"},  // v2 缩写变唯一 → became_resolved
	{ID: "same", Text: "主诉：胸痛。"},            // 两版一致，无变化
}

func changesOf(cmp *Comparison, docID string) []Change {
	for _, d := range cmp.Docs {
		if d.DocID == docID {
			return d.Changes
		}
	}
	return nil
}

func TestCompareChangeKinds(t *testing.T) {
	r := newRunner(nil)
	cmp, oldRes, newRes := r.RunComparison(context.Background(), compareDocs, "v1", "v2", Options{Concurrency: 3})
	if oldRes.Summary.Succeeded != len(compareDocs) || newRes.Summary.Succeeded != len(compareDocs) {
		t.Fatalf("两个版本都应全部成功: old=%+v new=%+v", oldRes.Summary, newRes.Summary)
	}
	if cmp.OldVersion != "v1" || cmp.NewVersion != "v2" {
		t.Errorf("对照应记录两侧版本: %+v", cmp)
	}
	if len(cmp.Skipped) != 0 {
		t.Errorf("全部成功时不应有跳过: %+v", cmp.Skipped)
	}

	check := func(docID string, wantKind ChangeKind, wantKey string) {
		t.Helper()
		chs := changesOf(cmp, docID)
		if len(chs) != 1 || chs[0].Kind != wantKind || chs[0].ConceptID != wantKey {
			t.Fatalf("doc=%s 期望一条 %s(%s)，得到 %+v", docID, wantKind, wantKey, chs)
		}
		ch := chs[0]
		// 每条变化必须能追到对应版本与判定依据。
		if ch.Old != nil && (ch.Old.Version != "v1" || ch.Old.Rationale == "") {
			t.Errorf("doc=%s 旧版侧缺少版本或判定依据: %+v", docID, ch.Old)
		}
		if ch.New != nil && (ch.New.Version != "v2" || ch.New.Rationale == "") {
			t.Errorf("doc=%s 新版侧缺少版本或判定依据: %+v", docID, ch.New)
		}
	}
	check("added", ChangeAdded, "concept:C_CHEST_TIGHTNESS")
	check("removed", ChangeRemoved, "concept:C_FEVER")
	check("polarity", ChangePolarity, "concept:C_CHEST_PAIN")
	check("to-pending", ChangeBecamePending, "abbrev:DM")
	check("to-resolved", ChangeBecameResolved, "abbrev:AP")

	if chs := changesOf(cmp, "same"); len(chs) != 0 {
		t.Errorf("两版一致的文档不应有变化: %+v", chs)
	}

	// 极性变化的方向与具体极性。
	ch := changesOf(cmp, "polarity")[0]
	if ch.Old.Polarity != extract.Affirmed || ch.New.Polarity != extract.Negated {
		t.Errorf("极性变化应为 affirmed→negated: %+v", ch)
	}
	// became_pending 的新版侧应保留待定依据；became_resolved 的新版侧应给出解析到的概念。
	if ch := changesOf(cmp, "to-pending")[0]; ch.New.Polarity != extract.Pending || ch.Old.ConceptID != "C_DIABETES" {
		t.Errorf("became_pending 两侧不符: %+v", ch)
	}
	if ch := changesOf(cmp, "to-resolved")[0]; ch.Old.Polarity != extract.Pending || ch.New.ConceptID != "C_ARTERIAL_P" {
		t.Errorf("became_resolved 两侧不符: %+v", ch)
	}

	want := map[ChangeKind]int{
		ChangeAdded: 1, ChangeRemoved: 1, ChangePolarity: 1,
		ChangeBecamePending: 1, ChangeBecameResolved: 1,
	}
	for k, n := range want {
		if cmp.Counts[k] != n {
			t.Errorf("Counts[%s] 应为 %d，得到 %d", k, n, cmp.Counts[k])
		}
	}
	for _, d := range cmp.Docs {
		for _, c := range d.Changes {
			t.Logf("doc=%s kind=%s key=%s old=%+v new=%+v", d.DocID, c.Kind, c.ConceptID, c.Old, c.New)
		}
	}
}

func TestCompareStableAcrossRepeatsAndConcurrency(t *testing.T) {
	r := newRunner(nil)
	reference, _, _ := r.RunComparison(context.Background(), compareDocs, "v1", "v2", Options{Concurrency: 1})
	refJSON, _ := json.Marshal(reference)
	for _, conc := range []int{1, 2, 8} {
		for i := 0; i < 3; i++ {
			cmp, _, _ := r.RunComparison(context.Background(), compareDocs, "v1", "v2", Options{Concurrency: conc})
			j, _ := json.Marshal(cmp)
			if string(j) != string(refJSON) {
				t.Fatalf("并发度 %d 第 %d 次对照结果不一致:\n%s\nvs\n%s", conc, i, j, refJSON)
			}
		}
	}
}

func TestCompareSkipsFailedDocs(t *testing.T) {
	r := newRunner(nil)
	docs := []Document{
		{ID: "ok", Text: "主诉：胸痛。"},
		{ID: "bad", Text: ""},
	}
	cmp, oldRes, newRes := r.RunComparison(context.Background(), docs, "v1", "v2", Options{Concurrency: 2})
	if oldRes.Summary.Failed != 1 || newRes.Summary.Failed != 1 {
		t.Fatalf("两版都应有一份失败: old=%+v new=%+v", oldRes.Summary, newRes.Summary)
	}
	if len(cmp.Docs) != 1 || cmp.Docs[0].DocID != "ok" {
		t.Errorf("只有双方都成功的文档才进入对照: %+v", cmp.Docs)
	}
	if len(cmp.Skipped) != 1 || cmp.Skipped[0].DocID != "bad" || cmp.Skipped[0].Err == "" {
		t.Errorf("失败文档应列入 Skipped 并给出原因: %+v", cmp.Skipped)
	}
	t.Logf("skipped: %+v", cmp.Skipped[0])
}

func TestCompareUnknownVersion(t *testing.T) {
	r := newRunner(nil)
	cmp, oldRes, newRes := r.RunComparison(context.Background(), compareDocs[:2], "v1", "v9", Options{})
	if newRes.Summary.Failed != len(compareDocs[:2]) {
		t.Fatalf("新版本不存在应全部失败: %+v", newRes.Summary)
	}
	if len(cmp.Docs) != 0 {
		t.Errorf("一侧全部失败时不应有对照项: %+v", cmp.Docs)
	}
	if len(cmp.Skipped) != 2 {
		t.Errorf("所有文档都应列入 Skipped: %+v", cmp.Skipped)
	}
	if cmp.OldVersion != "v1" || cmp.NewVersion != "v9" {
		t.Errorf("对照应记录实际请求的版本: %+v", cmp)
	}
	_ = oldRes
}
