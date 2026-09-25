// Package terminology 提供不可变的、带版本号的术语资源及其注册表。
package terminology

import "sync/atomic"

// Term 是一条概念词条：表面形式映射到概念。
type Term struct {
	Surface   string // 词面，如 "胸痛"
	ConceptID string // 概念标识，如 "C_CHEST_PAIN"
}

// ModifierKind 区分修饰词类型。
type ModifierKind int

const (
	Negation    ModifierKind = iota // 否定：无、否认、未见
	Uncertainty                     // 不确定：可能、疑似、考虑
)

// Modifier 是一条修饰词词条。
type Modifier struct {
	Surface string
	Kind    ModifierKind
}

// AbbrevCandidate 是缩写的一个候选释义及其消歧线索。
type AbbrevCandidate struct {
	ConceptID string
	Sections  []string // 倾向出现的小节；为空表示不限
	Keywords  []string // 倾向共现的关键词；为空表示不限
}

// Dictionary 是某一版本的不可变术语资源。创建后不得修改。
type Dictionary struct {
	Version    string
	Terms      []Term
	Modifiers  []Modifier
	Abbrev     map[string][]AbbrevCandidate
	SectionSet map[string]bool // 已知小节标题
}

// Registry 持有多个术语版本，支持并发安全的当前版本切换。
type Registry struct {
	current atomic.Value // 存放 *Dictionary
	byVer   map[string]*Dictionary
}

// NewRegistry 构建注册表，并把 initial 设为当前版本。
func NewRegistry(initial *Dictionary) *Registry {
	r := &Registry{byVer: map[string]*Dictionary{}}
	r.Register(initial)
	r.current.Store(initial)
	return r
}

// Snapshot 返回当前版本的不可变快照；调用方在整份文档处理期间持有它，
// 期间发生的版本切换不会影响已取得的快照。
func (r *Registry) Snapshot() *Dictionary {
	return r.current.Load().(*Dictionary)
}

// Switch 原子地把当前版本切换到 version；版本不存在时返回 false。
func (r *Registry) Switch(version string) bool {
	d, ok := r.byVer[version]
	if !ok {
		return false
	}
	r.current.Store(d)
	return true
}

// Register 注册一个版本（不切换当前版本）。注册表只增不改，
// 历史版本永远可用，保证历史文本的既有结论可复现。
func (r *Registry) Register(d *Dictionary) {
	r.byVer[d.Version] = d
}

// Version 返回给定版本号的字典；不存在时返回 nil。
func (r *Registry) Version(v string) *Dictionary {
	return r.byVer[v]
}
