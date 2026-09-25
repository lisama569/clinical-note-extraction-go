// Package extract 在已切分的句子上执行概念抽取、否定/不确定性判定
// 与缩写上下文消歧。判定完全基于给定术语版本快照，纯函数式、确定性。
package extract

import (
	"github.com/lisama569/clinical-note-extraction-go/pkg/terminology"
)

// Polarity 概念最终极性。
type Polarity string

const (
	// PolarityPositive 肯定（作用域内无否定/不确定修饰）。
	PolarityPositive Polarity = "POSITIVE"
	// PolarityNegative 否定（作用域内被否定词覆盖）。
	PolarityNegative Polarity = "NEGATIVE"
	// PolarityUncertain 不确定（作用域内被不确定词覆盖且无更高优先级的确定判定）。
	PolarityUncertain Polarity = "UNCERTAIN"
	// PolarityAmbiguous 缩写有多个候选且上下文无法判定，显式待定。
	PolarityAmbiguous Polarity = "AMBIGUOUS"
)

// Certainty 判定确定性级别，用于同片段多命中时的优先级仲裁。
type Certainty int

const (
	CertaintyAmbiguous Certainty = iota // 缩写歧义待定
	CertaintyUncertain                  // 不确定修饰
	CertaintyDefinite                   // 确定（肯定或否定）
)

// Mention 是一次具体命中（句子内的概念出现）。
type Mention struct {
	ConceptID string
	Term      string // 命中原词
	Start     int    // 相对原文档的字节偏移
	End       int
	Polarity  Polarity
	Certainty Certainty
	// MatchedBy 命中方式：TRIGGER（触发词）或 ABBREVIATION（缩写）。
	MatchedBy string
	// SectionIndex/SectionHeader 命中所在小节。
	SectionIndex  int
	SectionHeader string
	// SentenceText 命中所在句子原文。
	SentenceText string
	// Evidence 命中的规则与判定依据，便于解释。
	Evidence []string
}

// Finding 是同一概念跨多次命中归一后的结论。
type Finding struct {
	ConceptID string
	CUI       string
	Canonical string
	Polarity  Polarity
	Certainty Certainty
	Version   string // 结论所基于的术语版本
	Mentions  []Mention
}

// Result 是单份文本的抽取结果。
type Result struct {
	Version  string
	Findings []Finding
}

// Engine 概念抽取引擎，绑定一个不可变术语版本快照。
type Engine struct {
	version *terminology.Version
}

// NewEngine 基于术语版本快照创建引擎。
func NewEngine(v *terminology.Version) *Engine {
	return &Engine{version: v}
}

// Extract 的实现见 extract.go。
