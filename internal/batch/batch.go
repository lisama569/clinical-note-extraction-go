// Package batch 在单文档管线之上提供批量重放：一次提交多份病历，
// 整批钉住同一个术语版本处理，支持并发限制与中途取消。
//
// 行为约定：
//   - 批内每份文档整体只使用指定版本，不会新旧混判。
//   - 指定版本不存在、或某份文档无法处理时，按文档逐条标出原因，
//     不默默跳过，也不影响其它文档。
//   - 取消后已完成的文档结果完整保留，未轮到的文档标记为 cancelled，
//     不存在半份文档混用两个版本判定的情况。
//   - 结果按输入顺序输出；并发度与完成顺序不影响最终结果。
package batch

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/lisama569/clinical-note-extraction-go/internal/pipeline"
)

// Document 是一份待处理病历。
type Document struct {
	ID   string // 批内唯一标识，用于定位每份文档的结果
	Text string
}

// DocStatus 标识单份文档在批内的处理结局。
type DocStatus string

const (
	StatusOK        DocStatus = "ok"        // 处理完成，Result 有效
	StatusFailed    DocStatus = "failed"    // 处理失败，Err 给出原因
	StatusCancelled DocStatus = "cancelled" // 批被取消时尚未轮到，未处理
)

// DocResult 是单份文档的批处理结局。
type DocResult struct {
	DocID  string
	Status DocStatus
	Err    string           // Status 非 ok 时的具体原因
	Result *pipeline.Result // StatusOK 时非空，记录实际使用的版本与判定依据
}

// Summary 是整批的汇总统计。
type Summary struct {
	Version   string // 本批钉住的术语版本
	Total     int
	Succeeded int
	Failed    int
	Cancelled int
}

// Result 是一批文档的完整处理结果，Docs 顺序与输入一致。
type Result struct {
	Version string
	Docs    []DocResult
	Summary Summary
}

// Options 控制批量执行。
type Options struct {
	Concurrency int // 并发上限；<=0 时按 1 处理
}

// Runner 执行批量重放。并发安全，可被多个批次共享。
type Runner struct {
	pipe   *pipeline.Pipeline
	logger *log.Logger // 可为 nil；非空时记录每份文档的版本与判定依据
}

// NewRunner 创建批量执行器；logger 为 nil 时不输出日志。
func NewRunner(pipe *pipeline.Pipeline, logger *log.Logger) *Runner {
	return &Runner{pipe: pipe, logger: logger}
}

// Run 用指定术语版本处理整批文档。见包注释的行为约定。
//
// 实现要点：
//   - 每份文档的结果写入其在输入中的固定下标，与完成顺序无关，
//     因此并发度与调度顺序不影响最终输出。
//   - 工作协程在领取下一份文档前检查 ctx；取消后不再开始新文档，
//     已在处理中的文档会完整跑完（版本已在入口处钉住）。
//   - 单份文档内部的 panic 被回收为该文档的失败，不波及其它文档。
func (r *Runner) Run(ctx context.Context, docs []Document, version string, opts Options) *Result {
	res := &Result{Version: version, Docs: make([]DocResult, len(docs))}
	for i, d := range docs {
		res.Docs[i].DocID = d.ID
	}

	// 版本不存在的失败在调度前统一判定，整批逐文档标出，不默默跳过。
	versionErr := ""
	if !r.pipe.HasVersion(version) {
		versionErr = fmt.Sprintf("术语版本不存在: %q", version)
	}

	// 重复标识在调度前按输入顺序确定性地标出（首次出现有效，后续为重复），
	// 不依赖并发调度顺序，保证不同并发度下结果一致。
	dup := make([]bool, len(docs))
	{
		seen := map[string]bool{}
		for i, d := range docs {
			if seen[d.ID] {
				dup[i] = true
			}
			seen[d.ID] = true
		}
	}

	conc := opts.Concurrency
	if conc <= 0 {
		conc = 1
	}
	if conc > len(docs) {
		conc = len(docs)
	}

	next := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < conc; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				res.Docs[i] = r.processOne(ctx, docs[i], version, versionErr, dup[i])
			}
		}()
	}
feed:
	for i := range docs {
		select {
		case <-ctx.Done():
			break feed
		case next <- i:
		}
	}
	close(next)
	wg.Wait()

	// 取消时未轮到的文档明确标为未处理。
	for i := range res.Docs {
		if res.Docs[i].Status == "" {
			res.Docs[i].Status = StatusCancelled
			res.Docs[i].Err = "批处理已取消，该文档未处理"
		}
	}
	res.Summary = summarize(version, res.Docs)
	return res
}

// processOne 处理单份文档并记录日志；任何失败都收敛为该文档自身的结局。
func (r *Runner) processOne(ctx context.Context, doc Document, version, versionErr string, dup bool) (dr DocResult) {
	dr.DocID = doc.ID
	defer func() {
		if rec := recover(); rec != nil {
			dr.Status = StatusFailed
			dr.Result = nil
			dr.Err = fmt.Sprintf("处理过程内部错误: %v", rec)
		}
		r.logDoc(version, dr)
	}()

	if err := ctx.Err(); err != nil {
		dr.Status = StatusCancelled
		dr.Err = "批处理已取消，该文档未处理"
		return dr
	}
	if versionErr != "" {
		dr.Status = StatusFailed
		dr.Err = versionErr
		return dr
	}
	if err := validate(doc, dup); err != "" {
		dr.Status = StatusFailed
		dr.Err = err
		return dr
	}

	res, err := r.pipe.ProcessVersion(doc.ID, doc.Text, version)
	if err != nil {
		dr.Status = StatusFailed
		dr.Err = err.Error()
		return dr
	}
	dr.Status = StatusOK
	dr.Result = &res
	return dr
}

// validate 检查单份文档的输入合法性；dup 表示标识在批内前面已出现过。
func validate(doc Document, dup bool) string {
	if strings.TrimSpace(doc.ID) == "" {
		return "文档标识为空"
	}
	if strings.TrimSpace(doc.Text) == "" {
		return "文档内容为空"
	}
	if dup {
		return fmt.Sprintf("文档标识重复: %q", doc.ID)
	}
	return ""
}

// summarize 汇总整批结局；只依赖 Docs 内容，与处理顺序无关。
func summarize(version string, docs []DocResult) Summary {
	s := Summary{Version: version, Total: len(docs)}
	for _, d := range docs {
		switch d.Status {
		case StatusOK:
			s.Succeeded++
		case StatusFailed:
			s.Failed++
		case StatusCancelled:
			s.Cancelled++
		}
	}
	return s
}

// logDoc 输出单份文档的版本与判定依据，便于追溯。
func (r *Runner) logDoc(version string, dr DocResult) {
	if r.logger == nil {
		return
	}
	if dr.Status != StatusOK || dr.Result == nil {
		r.logger.Printf("doc=%s version=%s status=%s err=%q", dr.DocID, version, dr.Status, dr.Err)
		return
	}
	r.logger.Printf("doc=%s version=%s status=%s conclusions=%d",
		dr.DocID, dr.Result.TermVersion, dr.Status, len(dr.Result.Conclusions))
	for _, c := range dr.Result.Conclusions {
		r.logger.Printf("doc=%s version=%s conclusion=%s polarity=%s rationale=%s",
			dr.DocID, dr.Result.TermVersion, c.ConceptID, c.Polarity, c.Rationale)
	}
	for _, m := range dr.Result.Mentions {
		r.logger.Printf("doc=%s version=%s mention=%q concept=%s polarity=%s rationale=%s",
			dr.DocID, dr.Result.TermVersion, m.Surface, m.ConceptID, m.Polarity, m.Rationale)
	}
}
