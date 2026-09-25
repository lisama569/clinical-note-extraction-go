package terminology

import (
	"fmt"
	"sort"
	"strings"
)

// 版本名常量。
const (
	V1 = "v1"
	V2 = "v2"
)

// ErrUnknownVersion 在请求不存在的术语版本时返回。
type ErrUnknownVersion struct{ Name string }

func (e *ErrUnknownVersion) Error() string {
	return fmt.Sprintf("未知术语版本：%q", e.Name)
}

// concept 构造器，触发词在此处原样保留（大小写/中英文），
// 装载到版本时统一做大写归一。
func concept(id, cui, canonical, abbrev string, deprecated bool, triggers ...string) *Concept {
	return &Concept{
		ID:                id,
		CUI:               cui,
		Canonical:         canonical,
		Version:           "", // 由版本装载时填入
		Abbreviation:      abbrev,
		Triggers:          triggers,
		DeprecatedInNewer: deprecated,
	}
}

// commonConcepts 返回两个版本都存在、定义稳定的概念。
func commonConcepts() []*Concept {
	return []*Concept{
		concept("DIABETES", "C0011847", "2型糖尿病", "DM", false,
			"diabetes", "diabetes mellitus", "2型糖尿病", "糖尿病"),
		concept("HYPERTENSION", "C0020538", "高血压", "HTN", false,
			"hypertension", "high blood pressure", "高血压"),
		concept("MULTIPLE_SCLEROSIS", "C0026769", "多发性硬化", "MS", false,
			"multiple sclerosis", "多发性硬化"),
		concept("MITRAL_STENOSIS", "C0026237", "二尖瓣狭窄", "MS", false,
			"mitral stenosis", "二尖瓣狭窄"),
		concept("CORONARY_DISEASE", "C0010065", "冠心病", "CHD", false,
			"coronary heart disease", "coronary disease", "冠心病"),
		concept("TUBERCULOSIS", "C0041268", "结核病", "TB", false,
			"tuberculosis", "结核", "结核病"),
		concept("FEVER", "C0015967", "发热", "", false,
			"fever", "发热", "发烧"),
		concept("COUGH", "C0010200", "咳嗽", "", false,
			"cough", "咳嗽"),
		concept("CHEST_PAIN", "C0008031", "胸痛", "", false,
			"chest pain", "胸痛"),
	}
}

func v1Concepts() []*Concept {
	cs := commonConcepts()
	// v1 独有：新冠（当时 COVID 缩写尚未纳入，仅长触发词；TB/DM/CHD 缩写均有效）。
	cs = append(cs, concept("COVID19", "C5203670", "新型冠状病毒感染", "", false,
		"covid-19", "covid 19", "新型冠状病毒感染", "新冠肺炎"))
	return cs
}

func v2Concepts() []*Concept {
	// v2 重新核定：保留所有稳定概念（ID 跨版本不变）。
	cs := commonConcepts()
	cs = append(cs,
		// 新冠正式收录缩写 COVID。
		concept("COVID19", "C5203670", "新型冠状病毒感染", "COVID", false,
			"covid-19", "covid 19", "covid", "新型冠状病毒感染", "新冠肺炎"),
		// 新增代谢综合征，占用缩写 MS 的一个新义项。
		concept("METABOLIC_SYNDROME", "C0877694", "代谢综合征", "MS", false,
			"metabolic syndrome", "代谢综合征"),
	)
	return cs
}

func v1AbbrevRules() []AbbrevRule {
	return []AbbrevRule{
		{Abbreviation: "DM", ConceptID: "DIABETES", Priority: 10,
			ContextKeywords: []string{"血糖", "GLUCOSE", "INSULIN", "胰岛素", "GLYCOSYLATED", "糖化"}},
		{Abbreviation: "HTN", ConceptID: "HYPERTENSION", Priority: 10,
			ContextKeywords: []string{"血压", "BLOOD PRESSURE", "BP"}},
		{Abbreviation: "CHD", ConceptID: "CORONARY_DISEASE", Priority: 10,
			ContextKeywords: []string{"冠脉", "心肌", "CORONARY", "CARDIAC", "心"}},
		{Abbreviation: "TB", ConceptID: "TUBERCULOSIS", Priority: 10,
			ContextKeywords: []string{"肺", "LUNG", "PULMONARY", "痰", "抗酸"}},
		{Abbreviation: "MS", ConceptID: "MULTIPLE_SCLEROSIS", Priority: 10,
			ContextKeywords: []string{"神经", "NEURO", "脱髓鞘", "DEMYELIN", "脑"}},
		{Abbreviation: "MS", ConceptID: "MITRAL_STENOSIS", Priority: 10,
			ContextKeywords: []string{"二尖瓣", "MITRAL", "心超", "瓣膜", "VALVE", "心脏"}},
	}
}

func v2AbbrevRules() []AbbrevRule {
	return []AbbrevRule{
		// v2：DM、CHD、TB 缩写下线（术语规范变更），只能用长触发词命中。
		{Abbreviation: "HTN", ConceptID: "HYPERTENSION", Priority: 10,
			ContextKeywords: []string{"血压", "BLOOD PRESSURE", "BP"}},
		{Abbreviation: "COVID", ConceptID: "COVID19", Priority: 10,
			ContextKeywords: []string{"发热", "FEVER", "核酸", "PCR", "肺", "新冠"}},
		// MS 新增第三义项，三条规则同分：上下文无法区分时必须显式待定。
		{Abbreviation: "MS", ConceptID: "MULTIPLE_SCLEROSIS", Priority: 10,
			ContextKeywords: []string{"神经", "NEURO", "脱髓鞘", "DEMYELIN", "脑"}},
		{Abbreviation: "MS", ConceptID: "MITRAL_STENOSIS", Priority: 10,
			ContextKeywords: []string{"二尖瓣", "MITRAL", "心超", "瓣膜", "VALVE", "心脏"}},
		{Abbreviation: "MS", ConceptID: "METABOLIC_SYNDROME", Priority: 10,
			ContextKeywords: []string{"代谢", "METABOLIC", "血脂", "肥胖", "OBESITY", "腰围"}},
	}
}

func modifiers() ModifierSet {
	return ModifierSet{
		Negation: []string{
			"否认", "未见", "无", "排除", "未及",
			"NO", "NOT", "WITHOUT", "ABSENT", "NEGATIVE FOR", "DENIES", "RULE OUT",
		},
		Uncertain: []string{
			"可能", "待排", "疑似", "不除外", "考虑",
			"POSSIBLE", "POSSIBLY", "SUSPECTED", "SUSPECT", "QUESTIONABLE",
			"LIKELY", "MAYBE", "RULE-OUT", "CANNOT RULE OUT", "? ",
		},
		// 修饰词在同一子句内向后作用，超过该字符（rune）距离则不作用，
		// 防止一个否定跨越冗长表述波及后续肯定句。
		ForwardWindow: 24,
	}
}

// buildVersion 把原始定义冻结为不可变快照：填入版本名、归一化触发词/修饰词。
func buildVersion(name string, concepts []*Concept, rules []AbbrevRule) *Version {
	frozen := &Version{Name: name, Modifiers: modifiers()}
	for _, c := range concepts {
		cp := *c
		cp.Version = name
		tr := make([]string, 0, len(c.Triggers))
		for _, t := range c.Triggers {
			tr = append(tr, normalizeTerm(t))
		}
		cp.Triggers = tr
		frozen.Concepts = append(frozen.Concepts, &cp)
	}
	for _, rl := range rules {
		r := rl
		kws := make([]string, len(r.ContextKeywords))
		for i, k := range r.ContextKeywords {
			kws[i] = normalizeTerm(k)
		}
		r.ContextKeywords = kws
		r.Abbreviation = normalizeTerm(r.Abbreviation)
		frozen.Abbrev = append(frozen.Abbrev, r)
	}
	frozen.Modifiers.Negation = normalizeTerms(frozen.Modifiers.Negation)
	frozen.Modifiers.Uncertain = normalizeTerms(frozen.Modifiers.Uncertain)
	return frozen
}

// normalizeTerm 大写归一并压缩连续空白，用于触发词/修饰词匹配。
// 匹配时同样对原文做 rune 级大写；中文不受影响。
func normalizeTerm(s string) string {
	return strings.ToUpper(strings.Join(strings.Fields(s), " "))
}

func normalizeTerms(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = normalizeTerm(s)
	}
	return out
}

// NewRegistry 装载两个内置本地术语版本：v1 与 v2，最新版本为 v2。
func NewRegistry() *Registry { return newBuiltinRegistry() }

// newBuiltinRegistry 装载两个内置版本：v1 与 v2，最新版本为 v2。
func newBuiltinRegistry() *Registry {
	r := &Registry{versions: map[string]*Version{}}
	put := func(v *Version) { r.versions[v.Name] = v }
	put(buildVersion(V1, v1Concepts(), v1AbbrevRules()))
	put(buildVersion(V2, v2Concepts(), v2AbbrevRules()))
	r.latest = V2
	return r
}

// Get 按名称获取不可变版本快照；不存在时 ok 为 false。
func (r *Registry) Get(name string) (*Version, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.versions[name]
	return v, ok
}

// Versions 按版本名升序返回全部已知版本。
func (r *Registry) Versions() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.versions))
	for name := range r.versions {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Latest 返回最新版本名。
func (r *Registry) Latest() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.latest
}

// Snapshot 返回指定版本的不可变快照。version 为空时使用最新版本。
// 调用方在整份文本处理期间持有该快照；即使注册中心的最新版本被切换，
// 已返回的快照也不会改变，从而保证一次处理整体基于同一个确定版本。
func (r *Registry) Snapshot(version string) (*Version, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if version == "" {
		version = r.latest
	}
	v, ok := r.versions[version]
	if !ok {
		return nil, &ErrUnknownVersion{Name: version}
	}
	return v, nil
}
