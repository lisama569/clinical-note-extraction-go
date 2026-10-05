package batch

import (
	"context"
	"fmt"
	"sort"

	"github.com/lisama569/clinical-note-extraction-go/internal/extract"
	"github.com/lisama569/clinical-note-extraction-go/internal/pipeline"
)

// ChangeKind 标识两个术语版本间一条结论的变化类型。
type ChangeKind string

const (
	ChangeAdded          ChangeKind = "added"            // 仅新版本出现的结论
	ChangeRemoved        ChangeKind = "removed"          // 仅旧版本出现的结论
	ChangePolarity       ChangeKind = "polarity_changed" // 两版都有但极性不同
	ChangeBecamePending  ChangeKind = "became_pending"   // 旧版可判定 → 新版无法判定
	ChangeBecameResolved ChangeKind = "became_resolved"  // 旧版无法判定 → 新版可判定
)

// ConclusionRef 是某一版本下一条结论（或待定提及）的快照，可追溯到版本与判定依据。
type ConclusionRef struct {
	Version   string
	ConceptID string // 待定提及为空，此时 Surface 有效
	Surface   string // 待定提及的缩写词面
	Polarity  extract.Polarity
	Rationale string
}

// Change 是同一文档同一结论键在两个版本间的一处变化。
type Change struct {
	Kind      ChangeKind
	ConceptID string         // 结论键：概念标识；待定提及为 "pending:<词面>"
	Old       *ConclusionRef // 旧版本侧；该版本无此结论时为 nil
	New       *ConclusionRef // 新版本侧；该版本无此结论时为 nil
}

// DocDiff 是同一文档在两个版本间的全部变化；Changes 为空表示两版结论一致。
type DocDiff struct {
	DocID   string
	Changes []Change
}

// SkippedDoc 是因任一版本处理失败/取消而无法对照的文档及其原因。
type SkippedDoc struct {
	DocID string
	Err   string
}

// Comparison 是同一批文档在新旧两个版本间的变化对照。
type Comparison struct {
	OldVersion string
	NewVersion string
	Docs       []DocDiff    // 只含两个版本都成功处理的文档，按输入顺序
	Skipped    []SkippedDoc // 任一版本未成功处理的文档，按输入顺序
	Counts     map[ChangeKind]int
}

// Compare 对照同一批文档在两个版本下的批处理结果。纯函数：
// 同样的两个输入必然得到同样的对照，与批内处理顺序、并发度无关。
//
// 两个批结果必须来自同一批输入（Docs 按下标一一对应）；任一版本未成功
// 处理的文档不进入对照，而是列入 Skipped 并给出原因。
func Compare(oldRes, newRes *Result) *Comparison {
	cmp := &Comparison{
		OldVersion: oldRes.Version,
		NewVersion: newRes.Version,
		Counts:     map[ChangeKind]int{},
	}
	n := len(oldRes.Docs)
	if len(newRes.Docs) < n {
		n = len(newRes.Docs)
	}
	for i := 0; i < n; i++ {
		od, nd := oldRes.Docs[i], newRes.Docs[i]
		docID := od.DocID
		if od.DocID != nd.DocID {
			cmp.Skipped = append(cmp.Skipped, SkippedDoc{DocID: docID,
				Err: fmt.Sprintf("两个批结果不对齐: 旧版第%d份为%q，新版为%q", i, od.DocID, nd.DocID)})
			continue
		}
		if od.Status != StatusOK || nd.Status != StatusOK {
			cmp.Skipped = append(cmp.Skipped, SkippedDoc{DocID: docID, Err: skipReason(od, nd)})
			continue
		}
		cmp.Docs = append(cmp.Docs, DocDiff{DocID: docID, Changes: diffDoc(od.Result, nd.Result)})
	}
	for _, d := range cmp.Docs {
		for _, ch := range d.Changes {
			cmp.Counts[ch.Kind]++
		}
	}
	return cmp
}

// skipReason 汇总两个版本侧未能成功处理的原因。
func skipReason(od, nd DocResult) string {
	switch {
	case od.Status != StatusOK && nd.Status != StatusOK:
		return fmt.Sprintf("旧版本侧: %s；新版本侧: %s", od.Err, nd.Err)
	case od.Status != StatusOK:
		return "旧版本侧: " + od.Err
	}
	return "新版本侧: " + nd.Err
}

// conclusionKeys 把一份文档结果整理为 结论键 → 快照 的映射。
//
// 归组规则（从 Mention 重建，保证跨版本键稳定）：
//   - 缩写命中按词面归组，键为 "abbrev:<词面>"——这样同一缩写在旧版可判定、
//     新版待定（或反之）时键不变，才能识别“能判定 ↔ 无法判定”的变化；
//   - 普通词条命中按概念归组，键为 "concept:<概念标识>"。
//
// 组内多次提及按确定性优先级 affirmed > negated > uncertain > pending 归并，
// 并列时取先出现者（提及顺序确定），与单文档管线的归并语义一致。
func conclusionKeys(res *pipeline.Result) map[string]ConclusionRef {
	type group struct {
		best  extract.Mention
		count int
	}
	groups := map[string]*group{}
	for _, mt := range res.Mentions {
		key := "concept:" + mt.ConceptID
		if mt.Abbrev {
			key = "abbrev:" + mt.Surface
		}
		g, ok := groups[key]
		if !ok {
			groups[key] = &group{best: mt, count: 1}
			continue
		}
		g.count++
		if certaintyOf(mt.Polarity) > certaintyOf(g.best.Polarity) {
			g.best = mt
		}
	}
	m := make(map[string]ConclusionRef, len(groups))
	for key, g := range groups {
		m[key] = ConclusionRef{
			Version:   res.TermVersion,
			ConceptID: g.best.ConceptID,
			Surface:   g.best.Surface,
			Polarity:  g.best.Polarity,
			Rationale: fmt.Sprintf("共%d次提及，按确定性优先级取%s；依据:%s",
				g.count, g.best.Polarity, g.best.Rationale),
		}
	}
	return m
}

// certaintyOf 返回极性的确定性优先级，数值越大越确定。
func certaintyOf(p extract.Polarity) int {
	switch p {
	case extract.Affirmed:
		return 3
	case extract.Negated:
		return 2
	case extract.Uncertain:
		return 1
	}
	return 0
}

// diffDoc 对照单份文档在两个版本下的结论，返回按键排序的变化列表。
func diffDoc(oldRes, newRes *pipeline.Result) []Change {
	oldKeys, newKeys := conclusionKeys(oldRes), conclusionKeys(newRes)
	keys := map[string]bool{}
	for k := range oldKeys {
		keys[k] = true
	}
	for k := range newKeys {
		keys[k] = true
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted) // 稳定输出，与 map 迭代顺序无关

	var changes []Change
	for _, k := range sorted {
		o, inOld := oldKeys[k]
		nw, inNew := newKeys[k]
		switch {
		case !inOld: // 仅新版本出现
			nwc := nw
			changes = append(changes, Change{Kind: ChangeAdded, ConceptID: k, New: &nwc})
		case !inNew: // 仅旧版本出现
			oc := o
			changes = append(changes, Change{Kind: ChangeRemoved, ConceptID: k, Old: &oc})
		case o.Polarity == nw.Polarity:
			// 两版一致，无变化
		default:
			oc, nwc := o, nw
			kind := ChangePolarity
			switch {
			case o.Polarity == extract.Pending:
				kind = ChangeBecameResolved // 无法判定 → 能判定
			case nw.Polarity == extract.Pending:
				kind = ChangeBecamePending // 能判定 → 无法判定
			}
			changes = append(changes, Change{Kind: kind, ConceptID: k, Old: &oc, New: &nwc})
		}
	}
	return changes
}

// RunComparison 用新旧两个版本各跑一遍同一批文档并给出变化对照，
// 同时返回两个批的完整结果。两个批共享同一并发限制与取消上下文。
func (r *Runner) RunComparison(ctx context.Context, docs []Document, oldVersion, newVersion string, opts Options) (*Comparison, *Result, *Result) {
	oldRes := r.Run(ctx, docs, oldVersion, opts)
	newRes := r.Run(ctx, docs, newVersion, opts)
	return Compare(oldRes, newRes), oldRes, newRes
}
