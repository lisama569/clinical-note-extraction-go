package batch

import (
	"sort"

	"github.com/lisama569/clinical-note-extraction-go/internal/extract"
	"github.com/lisama569/clinical-note-extraction-go/internal/pipeline"
)

// ChangeKind 标识一条版本间变化的类型。
type ChangeKind string

const (
	ChangeAdded            ChangeKind = "added"              // 仅新版本出现的结论
	ChangeRemoved          ChangeKind = "removed"            // 仅旧版本出现的结论
	ChangePolarity         ChangeKind = "polarity_changed"   // 两版都有但极性不同
	ChangePendingToDecided ChangeKind = "pending_to_decided" // 旧版无法判定 → 新版可判定
	ChangeDecidedToPending ChangeKind = "decided_to_pending" // 旧版可判定 → 新版无法判定
)

// Side 记录某一版本侧的证据：结论（可判定概念）或待定提及（无法判定的缩写）。
type Side struct {
	Version   string
	Polarity  extract.Polarity // 该侧的极性；待定提及为 extract.Pending
	Rationale string           // 该侧的判定依据
}

// Change 是一条可追溯到版本与判定依据的变化。
type Change struct {
	DocID   string
	Subject string // 概念 ID；缩写待定/解析变化时为缩写词面（形如 "abbrev:DM"）
	Kind    ChangeKind
	Old     Side // 旧版本侧证据
	New     Side // 新版本侧证据
}

// SkippedDoc 记录一份无法参与对照的文档及原因（不默默跳过）。
type SkippedDoc struct {
	DocID  string
	Reason string
}

// Comparison 是同一批病历在两个术语版本间的对照结果。
// Changes 按 (DocID, Subject, Kind) 排序，与批内处理顺序、并发度无关，
// 同一输入重复对照结果完全一致。
type Comparison struct {
	OldVersion string
	NewVersion string
	Changes    []Change
	Skipped    []SkippedDoc // 任一版本中未成功处理的文档
}

// Compare 对照同一批病历在旧、新两个版本下的批量报告。
// oldRep、newRep 必须来自同一批输入文档（按 DocID 配对）。
func Compare(oldRep, newRep Report) Comparison {
	cmp := Comparison{OldVersion: oldRep.Version, NewVersion: newRep.Version}
	oldByID := indexByDocID(oldRep)
	newByID := indexByDocID(newRep)

	ids := make([]string, 0, len(oldByID)+len(newByID))
	seen := map[string]bool{}
	for id := range oldByID {
		seen[id] = true
		ids = append(ids, id)
	}
	for id := range newByID {
		if !seen[id] {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids) // 固定顺序：与批内处理/完成顺序无关

	for _, id := range ids {
		o, oOK := oldByID[id]
		n, nOK := newByID[id]
		switch {
		case !oOK:
			cmp.Skipped = append(cmp.Skipped, SkippedDoc{DocID: id, Reason: "旧版本报告中缺少该文档"})
			continue
		case !nOK:
			cmp.Skipped = append(cmp.Skipped, SkippedDoc{DocID: id, Reason: "新版本报告中缺少该文档"})
			continue
		case o.Status != StatusOK:
			cmp.Skipped = append(cmp.Skipped, SkippedDoc{DocID: id, Reason: "旧版本处理失败: " + o.Err})
			continue
		case n.Status != StatusOK:
			cmp.Skipped = append(cmp.Skipped, SkippedDoc{DocID: id, Reason: "新版本处理失败: " + n.Err})
			continue
		}
		cmp.Changes = append(cmp.Changes, diffDoc(id, oldRep.Version, o.Result, newRep.Version, n.Result)...)
	}
	return cmp
}

// indexByDocID 按文档标识索引报告结果；重复标识时保留先出现者（批量层已拒绝重复，此处仅防御）。
func indexByDocID(rep Report) map[string]*DocResult {
	m := make(map[string]*DocResult, len(rep.Results))
	for i := range rep.Results {
		if _, ok := m[rep.Results[i].DocID]; !ok {
			m[rep.Results[i].DocID] = &rep.Results[i]
		}
	}
	return m
}

// docState 是一份文档在某版本下的可对照状态。
type docState struct {
	concepts map[string]Side // 概念 ID → 结论侧证据
	pendings map[string]Side // 缩写词面 → 待定侧证据
	mentions map[string]Side // 词面 → 非待定提及证据（用于待定→可判定的对照）
}

func stateOf(version string, r *pipeline.Result) docState {
	st := docState{
		concepts: map[string]Side{},
		pendings: map[string]Side{},
		mentions: map[string]Side{},
	}
	for _, c := range r.Conclusions {
		st.concepts[c.ConceptID] = Side{Version: version, Polarity: c.Polarity, Rationale: c.Rationale}
	}
	for _, m := range r.Mentions {
		if m.Polarity == extract.Pending {
			st.pendings[m.Surface] = Side{Version: version, Polarity: extract.Pending, Rationale: m.Rationale}
			continue
		}
		if _, ok := st.mentions[m.Surface]; !ok {
			st.mentions[m.Surface] = Side{Version: version, Polarity: m.Polarity, Rationale: m.Rationale}
		}
	}
	return st
}

// diffDoc 对照单份文档在两个版本下的结果，输出按 Subject 排序的变化。
func diffDoc(docID, oldVer string, oldRes *pipeline.Result, newVer string, newRes *pipeline.Result) []Change {
	o, n := stateOf(oldVer, oldRes), stateOf(newVer, newRes)
	var out []Change

	for _, id := range sortedKeys(o.concepts, n.concepts) {
		oc, inOld := o.concepts[id]
		nc, inNew := n.concepts[id]
		switch {
		case inOld && !inNew:
			out = append(out, Change{DocID: docID, Subject: id, Kind: ChangeRemoved, Old: oc})
		case !inOld && inNew:
			out = append(out, Change{DocID: docID, Subject: id, Kind: ChangeAdded, New: nc})
		case oc.Polarity != nc.Polarity:
			out = append(out, Change{DocID: docID, Subject: id, Kind: ChangePolarity, Old: oc, New: nc})
		}
	}

	for _, surface := range sortedKeys(o.pendings, n.pendings) {
		op, inOld := o.pendings[surface]
		np, inNew := n.pendings[surface]
		subject := "abbrev:" + surface
		switch {
		case inOld && !inNew:
			// 旧版待定；新版若该词面已有非待定提及则属“无法判定→能判定”。
			newSide := Side{Version: newVer, Rationale: "该版本下词面未命中任何词条"}
			if nm, ok := n.mentions[surface]; ok {
				newSide = nm
			}
			out = append(out, Change{DocID: docID, Subject: subject, Kind: ChangePendingToDecided, Old: op, New: newSide})
		case !inOld && inNew:
			oldSide := Side{Version: oldVer, Rationale: "该版本下词面未命中任何词条"}
			if om, ok := o.mentions[surface]; ok {
				oldSide = om
			}
			out = append(out, Change{DocID: docID, Subject: subject, Kind: ChangeDecidedToPending, Old: oldSide, New: np})
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Subject != out[j].Subject {
			return out[i].Subject < out[j].Subject
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

// sortedKeys 返回两个映射键的并集，升序排列，保证输出确定。
func sortedKeys(a, b map[string]Side) []string {
	seen := map[string]bool{}
	var keys []string
	for k := range a {
		seen[k] = true
		keys = append(keys, k)
	}
	for k := range b {
		if !seen[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}
