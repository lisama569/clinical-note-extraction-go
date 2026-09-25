// Package pipeline 编排分节、切分、版本绑定与概念抽取，
// 提供确定性、可并发、可解释的本地病历处理管线。
package pipeline

import (
	"context"

	"github.com/lisama569/clinical-note-extraction-go/pkg/extract"
	"github.com/lisama569/clinical-note-extraction-go/pkg/segment"
	"github.com/lisama569/clinical-note-extraction-go/pkg/terminology"
)

// versionKey 用于在 context 中固定本次处理的术语版本。
type versionKey struct{}

// WithVersion 在 ctx 上绑定术语版本名，使该次处理整体基于确定版本。
func WithVersion(ctx context.Context, version string) context.Context {
	return context.WithValue(ctx, versionKey{}, version)
}

func versionFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(versionKey{}).(string); ok {
		return v
	}
	return ""
}

// Output 是一份文本的完整处理结论。
type Output struct {
	TextHash string // 输入文本的稳定指纹（SHA256 十六进制）
	Version  string // 实际使用的术语版本
	Document *segment.Document
	Result   *extract.Result
}

// Pipeline 持有术语注册中心，可并发处理多份文本。
// New/Process/String 等方法实现见 pipeline.go。
type Pipeline struct {
	registry *terminology.Registry
	splitter *segment.Splitter
}
