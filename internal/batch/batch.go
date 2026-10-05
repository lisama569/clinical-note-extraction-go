// Package batch 在单文档管线之上提供批量重放：一次提交多份病历，
// 整批钉住同一个术语版本，支持并发限制与中途取消。
//
// 行为约定：
//   - 批内每份文档整体只使用提交时指定的同一个版本，不混用新旧术语。
//   - 指定版本不存在时，每份文档都被标记为失败并注明原因，不默默跳过。
//   - 单份文档校验失败或处理 panic 只影响该文档，不影响批内其它文档。
//   - 取消（ctx.Done）后：已完成的文档结果完整保留；未开始的文档标记为
//     StatusNotProcessed；进行中的文档会完整跑完，不会出现半份结果。
//   - 结果按提交顺序返回，与并发度、各文档完成先后顺序无关。
package batch

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/lisama569/clinical-note-extraction-go/internal/extract"
	"github.com/lisama569/clinical-note-extraction-go/internal/pipeline"
	"github.com/lisama569/clinical-note-extraction-go/internal/terminology"
)

// DocStatus 标识批内单份文档的处理状态。
type DocStatus string

const (
	StatusOK           DocStatus = "ok"            // 处理成功
	StatusFailed       DocStatus = "failed"        // 文档本身无法处理（含版本不存在）
	StatusNotProcessed DocStatus = "not_processed" // 因取消而未轮到处理
)

// Document 是批内一份待处理病历。
type Document struct {
	ID   string // 区分文档的标识，批内必须唯一
	Text string
}

// DocResult 是批内一份文档的处理结果，按提交顺序返回。
type DocResult struct {
	DocID  string
	Status DocStatus
	Result *pipeline.Result // 成功时非空；其 TermVersion 即本文档实际使用的版本
	Err    string           // 失败原因（Status 为 failed 时非空）
}

// Summary 是整批的汇总计数。
type Summary struct {
	Version      string // 本批指定（且实际使用）的术语版本
	Total        int
	Succeeded    int
	Failed       int
	NotProcessed int
}

// Report 是一次批量执行的完整结果。
type Report struct {
	Version string     // 本批指定的术语版本
	Results []DocResult // 按提交顺序，与提交顺序一一对应
	Summary Summary
}

// Runner 是批量执行器。零值可用（默认并发度 4，无日志）。
type Runner struct {
	Pipeline       *pipeline.Pipeline
	Registry       *terminology.Registry
	MaxConcurrency int         // <=0 时取默认值 4
	Logger         *log.Logger // 非空时输出每份文档的版本与判定依据
}

// Run 用指定术语版本批量处理 docs，返回整批报告。
// 支持 ctx 取消；取消语义见包注释。结果与并发度、完成顺序无关。
func (r *Runner) Run(ctx context.Context, version string, docs []Document) Report {
	rep := Report{Version: version, Results: make([]DocResult, len(docs))}
	rep.Summary.Version = version
	rep.Summary.Total = len(docs)
	for i, d := range docs {
		rep.Results[i].DocID = d.ID
	}

	// 版本前置校验：版本不存在时整批失败，逐份注明原因，不默默跳过。
	if r.Registry.Version(version) == nil {
		reason := fmt.Sprintf("术语版本不存在: %q", version)
		for i := range rep.Results {
			rep.Results[i].Status = StatusFailed
			rep.Results[i].Err = reason
		}
		rep.Summary.Failed = len(docs)
		r.logf("批量执行中止：%s（共%d份文档均标记失败）", reason, len(docs))
		return rep
	}

	// 文档级校验：标识为空/重复、内容为空，只影响该文档本身。
	seen := make(map[string]bool, len(docs))
	runnable := make([]bool, len(docs))
	for i, d := range docs {
		switch {
		case strings.TrimSpace(d.ID) == "":
			rep.Results[i] = DocResult{DocID: d.ID, Status: StatusFailed, Err: "文档标识为空"}
		case seen[d.ID]:
			rep.Results[i] = DocResult{DocID: d.ID, Status: StatusFailed, Err: "文档标识重复: " + d.ID}
		case strings.TrimSpace(d.Text) == "":
			rep.Results[i] = DocResult{DocID: d.ID, Status: StatusFailed, Err: "文档内容为空"}
		default:
			seen[d.ID] = true
			runnable[i] = true
		}
		if rep.Results[i].Status == StatusFailed {
			r.logDoc(version, rep.Results[i])
		}
	}

	conc := r.MaxConcurrency
	if conc <= 0 {
		conc = 4
	}
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	for i := range docs {
		if !runnable[i] {
			continue
		}
		// 取消检查发生在“轮到该文档之前”：已取消则标记未处理；
		// 一旦开始处理则完整跑完，不产生半份结果。
		if ctx.Err() != nil {
			rep.Results[i].Status = StatusNotProcessed
			continue
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			rep.Results[i].Status = StatusNotProcessed
			continue
		}
		if ctx.Err() != nil { // 拿到槽位后、开始前再确认一次
			<-sem
			rep.Results[i].Status = StatusNotProcessed
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			rep.Results[i] = r.processOne(version, docs[i]) // 各写各的下标，无共享
		}(i)
	}
	wg.Wait()

	for _, res := range rep.Results {
		switch res.Status {
		case StatusOK:
			rep.Summary.Succeeded++
		case StatusFailed:
			rep.Summary.Failed++
		case StatusNotProcessed:
			rep.Summary.NotProcessed++
		}
	}
	r.logf("批量执行完成：version=%s total=%d ok=%d failed=%d not_processed=%d",
		version, rep.Summary.Total, rep.Summary.Succeeded, rep.Summary.Failed, rep.Summary.NotProcessed)
	return rep
}

// processOne 处理单份文档；panic 被回收为该文档的失败，不影响批内其它文档。
func (r *Runner) processOne(version string, d Document) (res DocResult) {
	res.DocID = d.ID
	defer func() {
		if rec := recover(); rec != nil {
			res.Status = StatusFailed
			res.Result = nil
			res.Err = fmt.Sprintf("处理过程内部错误: %v", rec)
		}
		r.logDoc(version, res)
	}()
	pr, err := r.Pipeline.ProcessVersion(d.ID, d.Text, version)
	if err != nil {
		res.Status = StatusFailed
		res.Err = err.Error()
		return res
	}
	res.Status = StatusOK
	res.Result = &pr
	return res
}

// logf 输出批量级日志（未配置 Logger 时静默）。
func (r *Runner) logf(format string, args ...any) {
	if r.Logger != nil {
		r.Logger.Printf(format, args...)
	}
}

// logDoc 输出单份文档的版本与判定依据，便于追溯每份文档是如何判定的。
func (r *Runner) logDoc(version string, res DocResult) {
	if r.Logger == nil {
		return
	}
	switch res.Status {
	case StatusOK:
		r.Logger.Printf("doc=%s version=%s status=ok conclusions=%d", res.DocID, res.Result.TermVersion, len(res.Result.Conclusions))
		for _, c := range res.Result.Conclusions {
			r.Logger.Printf("doc=%s version=%s 结论 %s=%s 依据:%s", res.DocID, res.Result.TermVersion, c.ConceptID, c.Polarity, c.Rationale)
		}
		for _, m := range res.Result.Mentions {
			if m.Polarity == extract.Pending {
				r.Logger.Printf("doc=%s version=%s 待定提及 %q 依据:%s", res.DocID, res.Result.TermVersion, m.Surface, m.Rationale)
			}
		}
	case StatusFailed:
		r.Logger.Printf("doc=%s version=%s status=failed 原因:%s", res.DocID, version, res.Err)
	case StatusNotProcessed:
		r.Logger.Printf("doc=%s version=%s status=not_processed（取消前未轮到）", res.DocID, version)
	}
}
