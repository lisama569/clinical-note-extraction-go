package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/lisama569/clinical-note-extraction-go/pkg/extract"
	"github.com/lisama569/clinical-note-extraction-go/pkg/segment"
	"github.com/lisama569/clinical-note-extraction-go/pkg/terminology"
)

// New 创建管线。
func New(reg *terminology.Registry) *Pipeline {
	if reg == nil {
		reg = terminology.NewRegistry()
	}
	return &Pipeline{registry: reg, splitter: &segment.Splitter{}}
}

// Registry 暴露底层术语注册中心，便于调用方查看版本。
func (p *Pipeline) Registry() *terminology.Registry { return p.registry }

// Process 处理单份文本：
//   - 开始时从注册中心取得一个确定的、不可变的术语版本快照；
//     处理全程持有该快照，即使期间其他 goroutine 切换/升级版本，
//     本次结论也不会出现新旧规则混合；
//   - 分节切分与概念抽取均为纯函数，重复处理同一文本得到完全一致的结果；
//   - 多个 goroutine 可并发调用 Process，彼此无共享可变状态。
func (p *Pipeline) Process(ctx context.Context, text string) (*Output, error) {
	version := versionFromContext(ctx)
	snap, err := p.registry.Snapshot(version)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(text))
	doc := p.splitter.Split(text)
	res := extract.NewEngine(snap).Extract(text, doc)
	return &Output{
		TextHash: hex.EncodeToString(sum[:]),
		Version:  snap.Name,
		Document: doc,
		Result:   res,
	}, nil
}

// String 输出确定性、可读的处理报告（供单测日志与人工核验）。
// 输出顺序固定：文档级异常、各小节（含异常与句子）、概念结论及命中依据。
func (o *Output) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "TextHash: %s\n", o.TextHash)
	fmt.Fprintf(&b, "TermVersion: %s\n", o.Version)

	writeIssues := func(prefix string, issues []segment.Issue) {
		for _, is := range issues {
			fmt.Fprintf(&b, "%s[问题] %s @[%d:%d] %s\n", prefix, is.Kind, is.Start, is.End, is.Message)
		}
	}
	writeIssues("", o.Document.Issues)

	for _, sec := range o.Document.Sections {
		name := sec.Header
		if name == "" {
			name = "<匿名小节>"
		}
		fmt.Fprintf(&b, "小节#%d %s 标题[%d:%d]\n", sec.Index, name, sec.HeaderStart, sec.HeaderEnd)
		writeIssues("  ", sec.Issues)
		for _, s := range sec.Sentences {
			fmt.Fprintf(&b, "  句子[%d:%d] %s\n", s.Start, s.End, s.Text)
		}
	}

	for _, f := range o.Result.Findings {
		fmt.Fprintf(&b, "结论 %s | %s | 极性=%s 确定性=%d CUI=%s 版本=%s\n",
			f.ConceptID, f.Canonical, f.Polarity, f.Certainty, f.CUI, f.Version)
		for _, m := range f.Mentions {
			fmt.Fprintf(&b, "  - 命中 %q [%d:%d] 方式=%s 极性=%s 小节#%d\n",
				m.Term, m.Start, m.End, m.MatchedBy, m.Polarity, m.SectionIndex)
			for _, ev := range m.Evidence {
				fmt.Fprintf(&b, "      依据: %s\n", ev)
			}
		}
	}
	return b.String()
}

// FindingsByID 返回概念 ID -> 结论 的索引，方便断言。
func (o *Output) FindingsByID() map[string]extract.Finding {
	m := map[string]extract.Finding{}
	for _, f := range o.Result.Findings {
		m[f.ConceptID] = f
	}
	return m
}

// SortedFindingIDs 按字典序返回命中概念 ID，保证断言顺序稳定。
func (o *Output) SortedFindingIDs() []string {
	ids := make([]string, 0, len(o.Result.Findings))
	for _, f := range o.Result.Findings {
		ids = append(ids, f.ConceptID)
	}
	sort.Strings(ids)
	return ids
}
