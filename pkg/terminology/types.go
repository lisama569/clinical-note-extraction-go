// Package terminology 提供本地术语资源的版本化注册中心：
// 概念词典、触发词、否定/不确定修饰词、缩写消歧规则。
// 所有资源均为本地构造，不访问外部服务；每个版本是不可变快照，
// 保证处理期间切换版本不会产生新旧混合的判定。
package terminology

import "sync"

// Concept 是术语版本无关的规范化概念。
type Concept struct {
	ID           string   // 跨版本稳定的概念标识，如 DIABETES
	CUI          string   // 本地术语编码，如 C0011847
	Canonical    string   // 规范名
	Version      string   // 该映射来自的术语版本
	Abbreviation string   // 若概念可由缩写命中，记录缩写（空表示无）
	Triggers     []string // 该版本下归一化（大写、去空格）的触发词
	// DeprecatedInNewer 表示该版本仍识别该概念，但已知在更新版本中被移除/替换。
	DeprecatedInNewer bool
}

// AbbrevRule 描述一条缩写消歧规则。
type AbbrevRule struct {
	Abbreviation string // 缩写，归一化为大写，如 MS
	ConceptID    string // 解析到的概念 ID
	// ContextKeywords 判定上下文线索词（大写归一化后子串匹配）。
	ContextKeywords []string
	// Priority 越大优先级越高；同分时按概念 ID 字典序，保证确定性。
	Priority int
}

// ModifierSet 汇总一个版本内的修饰词。
type ModifierSet struct {
	Negation  []string // 否定词，如 NO、WITHOUT、否认、无、未见
	Uncertain []string // 不确定词，如 POSSIBLE、SUSPECTED、可能、待排
	// ForwardWindow 修饰词向后作用的最大字符数（rune 计数，超出则不作用）。
	ForwardWindow int
}

// Version 是某一术语版本的不可变快照。
type Version struct {
	Name      string
	Concepts  []*Concept
	Abbrev    []AbbrevRule
	Modifiers ModifierSet
}

// Registry 是版本化术语注册中心。并发安全：读取基于不可变快照，
// 切换版本是原子的指针替换；处理期间可安全并发调用，
// 每份文本在处理开始时绑定一个确定版本，不会出现新旧混合。
type Registry struct {
	mu       sync.RWMutex
	latest   string
	versions map[string]*Version
}

// NewRegistry 与各访问方法见 registry.go（装载内置版本与并发安全实现）。
