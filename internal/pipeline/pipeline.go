// Package pipeline 串联分节、切分与抽取，并保证每份文档整体基于同一术语版本。
package pipeline

import (
	"fmt"

	"github.com/lisama569/clinical-note-extraction-go/internal/extract"
	"github.com/lisama569/clinical-note-extraction-go/internal/segment"
	"github.com/lisama569/clinical-note-extraction-go/internal/terminology"
)

// Result 是一份文档的完整处理结果。
type Result struct {
	DocID       string
	TermVersion string // 本文档实际使用的术语版本
	Segments    []segment.Segment
	Mentions    []extract.Mention
	Conclusions []extract.Conclusion
}

// Pipeline 是并发安全的处理管线。
type Pipeline struct {
	reg *terminology.Registry
}

// New 基于术语注册表创建管线。
func New(reg *terminology.Registry) *Pipeline {
	return &Pipeline{reg: reg}
}

// Process 处理一份文档。版本在入口处一次性钉住（Snapshot），
// 整份文档的分节、抽取与归并都基于同一不可变字典，不会混用新旧术语；
// 同一 (docID, text) 在同一版本下重复处理的结果完全一致。
func (p *Pipeline) Process(docID, text string) Result {
	return p.processWith(p.reg.Snapshot(), docID, text)
}

// ProcessVersion 用指定术语版本处理一份文档；版本不存在时返回错误。
// 与 Process 一样，整份文档只基于这一个版本的不可变字典。
func (p *Pipeline) ProcessVersion(docID, text, version string) (Result, error) {
	dict := p.reg.Version(version)
	if dict == nil {
		return Result{}, fmt.Errorf("术语版本不存在: %q", version)
	}
	return p.processWith(dict, docID, text), nil
}

// processWith 是版本无关的处理主体：dict 在调用前已钉住。
func (p *Pipeline) processWith(dict *terminology.Dictionary, docID, text string) Result {
	segs := segment.Split(text, dict.SectionSet)
	mentions, conclusions := extract.Extract(segs, dict)
	return Result{
		DocID:       docID,
		TermVersion: dict.Version,
		Segments:    segs,
		Mentions:    mentions,
		Conclusions: conclusions,
	}
}
