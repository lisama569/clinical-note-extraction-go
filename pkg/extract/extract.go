package extract

import (
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/lisama569/clinical-note-extraction-go/pkg/segment"
	"github.com/lisama569/clinical-note-extraction-go/pkg/terminology"
)

// 命中方式。
const (
	matchedByTrigger      = "TRIGGER"
	matchedByAbbreviation = "ABBREVIATION"
)

// clausePunct 是子句边界。修饰词不跨越子句边界作用，
// 保证“无胸痛；有咳嗽。”里的否定只覆盖胸痛，不波及咳嗽；
// 顿号视为并列枚举边界，因此“发热、咳嗽，无胸痛。”中的“无”
// 只覆盖胸痛，不会向前越过顿号否定发热/咳嗽。
var clausePunct = map[rune]struct{}{
	'，': {}, ',': {},
	'、': {},
	'；': {}, ';': {},
	'。': {}, '！': {}, '？': {},
	'.': {}, '!': {}, '?': {},
}

// compact 表示去除全部空白后的句子，保留与原句（raw[start:end]）的字节偏移映射。
// idx[i] = 紧凑串第 i 个字节对应在原句子串中的字节偏移；idx[len(compact)] 为末尾哨兵。
type compact struct {
	text string // 去空白后的文本（大写）
	raw  string // 原句子串（未大写、含空白）
	idx  []int
	base int // 句子在原文档中的起始字节偏移
}

func buildCompact(raw string, start, end int) compact {
	sub := raw[start:end]
	var b strings.Builder
	idx := make([]int, 0, len(sub)+1)
	p := 0
	for p < len(sub) {
		r, size := utf8.DecodeRuneInString(sub[p:])
		if unicode.IsSpace(r) {
			p += size
			continue
		}
		up := strings.ToUpper(sub[p : p+size])
		b.WriteString(up)
		// 紧凑串按字节索引：同一 rune 的每个字节都映射到该 rune 在子串中的起点；
		// 这样任意 UTF-8 字符边界处的切片都能还原正确的原文偏移。
		for k := 0; k < len(up); k++ {
			idx = append(idx, p)
		}
		p += size
	}
	idx = append(idx, p) // 末尾哨兵
	return compact{text: b.String(), raw: sub, idx: idx, base: start}
}

// mapRange 把紧凑串字节区间 [s,e) 映射为原文档绝对字节偏移 [start,end)。
func (c compact) mapRange(s, e int) (int, int) {
	return c.base + c.idx[s], c.base + c.idx[e]
}

// match 表示一次原始命中。
type match struct {
	conceptID string
	term      string // 命中词（原文形态）
	start     int    // 文档绝对字节偏移
	end       int
	by        string
}

// abbrevCandidate 是缩写消歧的一个候选。
type abbrevCandidate struct {
	conceptID string
	priority  int
	keywords  []string // 已归一（去空白、大写）
}

// Extract 对切分文档执行概念抽取与极性判定，raw 为文档原文。
//
// 语义：
//  1. 每个句子独立处理；在“去空白 + 大写”的紧凑串上做触发词子串匹配，
//     再通过偏移映射还原为原文档字节位置，跨行/多空格不影响匹配与定位。
//  2. 缩写按独立 token（字母数字连字符串）边界匹配，避免 MS 命中在词内。
//  3. 否定/不确定修饰词只在同一子句内向后作用，作用距离不超过版本窗口
//     (ForwardWindow，按非空白 rune 计数)；子句边界或越窗即失效，
//     因此一处否定不会波及另一处肯定表述。
//  4. 缩写按上下文线索词给候选打分：唯一最高分 => 解析为该概念；
//     多候选并列最高分或完全无线索 => 显式 AMBIGUOUS（待定）。
//  5. 同一句同一概念多次提及按确定性优先级仲裁：确定 > 不确定 > 歧义；
//     概念最终结论再跨小节/跨句子归并，全部提及保留以便解释。
func (e *Engine) Extract(raw string, doc *segment.Document) *Result {
	res := &Result{Version: e.version.Name}
	if doc == nil {
		return res
	}
	trig := e.buildTriggerIndex()
	abbrev := e.buildAbbrevIndex()

	var mentions []Mention
	for si := range doc.Sections {
		sec := &doc.Sections[si]
		for _, sent := range sec.Sentences {
			c := buildCompact(raw, sent.Start, sent.End)
			ms := e.matchesInSentence(c, trig, abbrev)
			byConcept := map[string][]match{}
			var order []string
			for _, m := range ms {
				if _, ok := byConcept[m.conceptID]; !ok {
					order = append(order, m.conceptID)
				}
				byConcept[m.conceptID] = append(byConcept[m.conceptID], m)
			}
			for _, id := range order {
				winner := arbitrate(c, byConcept[id], e.version.Modifiers)
				mentions = append(mentions, e.toMention(winner, sec, c))
			}
		}
	}
	res.Findings = e.aggregate(mentions)
	return res
}

func (e *Engine) toMention(m match, sec *segment.Section, c compact) Mention {
	pol, cert, ev := judgePolarity(c, m, e.version.Modifiers)
	header := sec.Header
	if header == "" {
		header = "<匿名>"
	}
	ev = append(ev, "命中方式="+m.by, "命中词="+m.term, "小节="+header)
	if ab := strings.TrimPrefix(m.conceptID, "AMBIGUOUS:"); ab != m.conceptID && m.by == matchedByAbbreviation {
		ev = append(ev, "缩写消歧="+ab+" 在当前版本存在多个同分候选且上下文无唯一线索，显式标记待定(AMBIGUOUS)")
	} else if m.by == matchedByAbbreviation {
		ev = append(ev, "缩写消歧=上下文线索唯一指向 "+m.conceptID)
	}
	return Mention{
		ConceptID: m.conceptID, Term: m.term, Start: m.start, End: m.end,
		Polarity: pol, Certainty: cert, MatchedBy: m.by,
		SectionIndex: sec.Index, SectionHeader: sec.Header,
		SentenceText: c.raw, Evidence: ev,
	}
}

type triggerTerm struct {
	compact   string // 去空白大写
	conceptID string
}

func (e *Engine) buildTriggerIndex() []triggerTerm {
	var out []triggerTerm
	for _, c := range e.version.Concepts {
		for _, t := range c.Triggers {
			out = append(out, triggerTerm{compact: stripSpace(t), conceptID: c.ID})
		}
	}
	// 长词优先，避免 “COVID19” 被更短的词抢先；次级按字典序保证确定性。
	sort.SliceStable(out, func(i, j int) bool {
		if rl, rj := len([]rune(out[i].compact)), len([]rune(out[j].compact)); rl != rj {
			return rl > rj
		}
		return out[i].compact < out[j].compact
	})
	return out
}

func (e *Engine) buildAbbrevIndex() map[string][]abbrevCandidate {
	idx := map[string][]abbrevCandidate{}
	for _, r := range e.version.Abbrev {
		kws := make([]string, len(r.ContextKeywords))
		for i, k := range r.ContextKeywords {
			kws[i] = stripSpace(k)
		}
		idx[r.Abbreviation] = append(idx[r.Abbreviation], abbrevCandidate{
			conceptID: r.ConceptID, priority: r.Priority, keywords: kws,
		})
	}
	for a := range idx {
		cs := idx[a]
		sort.SliceStable(cs, func(i, j int) bool {
			if cs[i].priority != cs[j].priority {
				return cs[i].priority > cs[j].priority
			}
			return cs[i].conceptID < cs[j].conceptID
		})
		idx[a] = cs
	}
	return idx
}

func stripSpace(s string) string {
	return strings.ToUpper(strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s))
}

// matchesInSentence 返回句子内全部命中（触发词 + 缩写），按位置排序。
func (e *Engine) matchesInSentence(c compact, trig []triggerTerm, abbrev map[string][]abbrevCandidate) []match {
	var out []match

	covered := func(s, ee int) bool {
		for _, m := range out {
			ms, me := m.start-c.base, m.end-c.base
			if s >= ms && ee <= me {
				return true
			}
		}
		return false
	}

	// 触发词
	for _, tt := range trig {
		from := 0
		for from <= len(c.text) {
			rel := strings.Index(c.text[from:], tt.compact)
			if rel < 0 {
				break
			}
			s := from + rel
			ee := s + len(tt.compact)
			from = ee
			if covered(s, ee) { // 已被更长的触发词覆盖
				continue
			}
			ds, de := c.mapRange(s, ee)
			out = append(out, match{
				conceptID: tt.conceptID, term: c.raw[c.idx[s]:c.idx[ee]],
				start: ds, end: de, by: matchedByTrigger,
			})
		}
	}

	// 缩写（token 边界）
	for _, tok := range tokenRanges(c.text) {
		cands, ok := abbrev[tok.text]
		if !ok {
			continue
		}
		ds, de := c.mapRange(tok.start, tok.end)
		m := match{
			term:  c.raw[c.idx[tok.start]:c.idx[tok.end]],
			start: ds, end: de, by: matchedByAbbreviation,
		}
		if id := resolveAbbrev(c.text, cands); id != "" {
			m.conceptID = id
		} else {
			m.conceptID = ambiguousID(tok.text)
		}
		out = append(out, m)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].start != out[j].start {
			return out[i].start < out[j].start
		}
		if out[i].end != out[j].end {
			return out[i].end < out[j].end
		}
		return out[i].conceptID < out[j].conceptID
	})
	return out
}

func ambiguousID(abbrev string) string { return "AMBIGUOUS:" + abbrev }

// resolveAbbrev 按上下文线索词给候选打分；唯一最高分返回该概念，否则 ""（待定）。
func resolveAbbrev(contextCompact string, cands []abbrevCandidate) string {
	best := 0
	var winners []string
	for _, c := range cands {
		score := 0
		for _, kw := range c.keywords {
			if strings.Contains(contextCompact, kw) {
				score++
			}
		}
		if score == 0 {
			continue
		}
		if score > best {
			best, winners = score, []string{c.conceptID}
		} else if score == best {
			winners = append(winners, c.conceptID)
		}
	}
	sort.Strings(winners)
	if len(winners) == 1 {
		return winners[0]
	}
	return ""
}

// modHit 描述一个在命中之前的修饰词。
type modHit struct {
	word string
	pos  int // 紧凑串字节偏移（修饰词起点）
	dist int // 修饰词尾到命中起点之间的非空白 rune 数
}

// judgePolarity 依据修饰词作用域判定一次命中的极性与确定性，并给出依据。
func judgePolarity(c compact, m match, mods terminology.ModifierSet) (Polarity, Certainty, []string) {
	hitRel := m.start - c.base // 命中起点在原句子串中的字节偏移
	// 换算到紧凑串偏移：找第一个 >= hitRel 的映射点。
	hitCompact := sort.Search(len(c.idx), func(i int) bool { return c.idx[i] >= hitRel })

	clauseCompact := clauseStartBefore(c.text, hitCompact)

	neg := nearestModifier(c.text, clauseCompact, hitCompact, mods.Negation, mods.ForwardWindow)
	unc := nearestModifier(c.text, clauseCompact, hitCompact, mods.Uncertain, mods.ForwardWindow)

	ev := []string{
		"原句=" + strings.Join(strings.Fields(c.raw), " "),
		"子句紧凑起点=" + strconv.Itoa(clauseCompact),
		"前向窗口rune=" + strconv.Itoa(mods.ForwardWindow),
	}
	if neg != nil {
		ev = append(ev, "作用域内否定词="+neg.word+"，间隔rune="+strconv.Itoa(neg.dist))
	} else {
		ev = append(ev, "作用域内无否定词")
	}
	if unc != nil {
		ev = append(ev, "作用域内不确定词="+unc.word+"，间隔rune="+strconv.Itoa(unc.dist))
	} else {
		ev = append(ev, "作用域内无不确定词")
	}

	// 否定与不确定同时在作用域内时，取离命中更近者；等距时否定优先。
	if neg != nil && (unc == nil || neg.dist <= unc.dist) {
		ev = append(ev, "结论=NEGATIVE/确定（否定词覆盖）")
		return PolarityNegative, CertaintyDefinite, ev
	}
	if unc != nil {
		ev = append(ev, "结论=UNCERTAIN（不确定词覆盖）")
		return PolarityUncertain, CertaintyUncertain, ev
	}
	ev = append(ev, "结论=POSITIVE/确定（作用域内无修饰）")
	return PolarityPositive, CertaintyDefinite, ev
}

// arbitrate 在同一概念的多次句内命中中按确定性优先级选结论，同级取位置靠前。
func arbitrate(c compact, group []match, mods terminology.ModifierSet) match {
	best := group[0]
	_, bestCert, _ := judgePolarity(c, best, mods)
	for _, cand := range group[1:] {
		_, cert, _ := judgePolarity(c, cand, mods)
		if cert > bestCert || (cert == bestCert && cand.start < best.start) {
			best, bestCert = cand, cert
		}
	}
	return best
}

// aggregate 跨句子/小节归并同一概念，确定性优先，保留全部提及。
func (e *Engine) aggregate(mentions []Mention) []Finding {
	byID := map[string]*Finding{}
	var order []string
	for _, mn := range mentions {
		f, ok := byID[mn.ConceptID]
		if !ok {
			f = &Finding{ConceptID: mn.ConceptID, Version: e.version.Name}
			if c := e.conceptByID(mn.ConceptID); c != nil {
				f.CUI, f.Canonical = c.CUI, c.Canonical
			} else if ab := strings.TrimPrefix(mn.ConceptID, "AMBIGUOUS:"); ab != mn.ConceptID {
				f.Canonical = "缩写歧义待定：" + ab
				f.Polarity = PolarityAmbiguous
			}
			byID[mn.ConceptID] = f
			order = append(order, mn.ConceptID)
		}
		f.Mentions = append(f.Mentions, mn)
	}
	sort.Strings(order)

	out := make([]Finding, 0, len(order))
	for _, id := range order {
		f := byID[id]
		top := f.Mentions[0]
		for _, mn := range f.Mentions[1:] {
			if mn.Certainty > top.Certainty || (mn.Certainty == top.Certainty && mn.Start < top.Start) {
				top = mn
			}
		}
		// 纯歧义占位概念（无任何确定/不确定解析）保持 AMBIGUOUS。
		if strings.HasPrefix(id, "AMBIGUOUS:") {
			f.Polarity, f.Certainty = PolarityAmbiguous, CertaintyAmbiguous
		} else {
			f.Polarity, f.Certainty = top.Polarity, top.Certainty
		}
		out = append(out, *f)
	}
	return out
}

func (e *Engine) conceptByID(id string) *terminology.Concept {
	for _, c := range e.version.Concepts {
		if c.ID == id {
			return c
		}
	}
	return nil
}

// --- 作用域工具 ---

// clauseStartBefore 返回紧凑串 pos 之前最近子句边界之后的位置（即本子句起点）。
func clauseStartBefore(s string, pos int) int {
	i := pos
	for i > 0 {
		r, size := utf8.DecodeLastRuneInString(s[:i])
		if _, bd := clausePunct[r]; bd {
			return i
		}
		i -= size
	}
	return 0
}

// nearestModifier 在紧凑串 s 的 [start,pos) 内寻找距 pos 最近的修饰词，
// 且修饰词尾到 pos 的非空白 rune 距离 <= window；没有则返回 nil。
func nearestModifier(s string, start, pos int, words []string, window int) *modHit {
	var best *modHit
	for _, w := range words {
		from := start
		for from <= pos {
			idx := strings.Index(s[from:pos], w)
			if idx < 0 {
				break
			}
			ws := from + idx
			we := ws + len(w)
			from = we
			if we > pos {
				continue
			}
			dist := runeLen(s[we:pos])
			if dist > window {
				continue
			}
			if best == nil || dist < best.dist || (dist == best.dist && ws > best.pos) {
				best = &modHit{word: w, pos: ws, dist: dist}
			}
		}
	}
	return best
}

func runeLen(s string) int { return len([]rune(s)) }

type tokenRange struct {
	text       string
	start, end int
}

// tokenRanges 切出字母/数字/连字符 token（紧凑串已去空白、已大写）。
func tokenRanges(s string) []tokenRange {
	var out []tokenRange
	i := 0
	for i < len(s) {
		r, size := utf8.DecodeRuneInString(s[i:])
		if isTokenRune(r) {
			j := i + size
			for j < len(s) {
				r2, s2 := utf8.DecodeRuneInString(s[j:])
				if !isTokenRune(r2) {
					break
				}
				j += s2
			}
			out = append(out, tokenRange{text: s[i:j], start: i, end: j})
			i = j
		} else {
			i += size
		}
	}
	return out
}

// isTokenRune 判断是否为缩写 token 字符。本地术语中的缩写均为 ASCII
// 字母/数字/连字符（DM、HTN、COVID-19 等），故不把中文等宽字符并入 token，
// 否则“神经科随访MS”会被当成一个长 token 而漏匹配缩写。
func isTokenRune(r rune) bool {
	return r == '-' ||
		(r >= '0' && r <= '9') ||
		(r >= 'A' && r <= 'Z')
}
