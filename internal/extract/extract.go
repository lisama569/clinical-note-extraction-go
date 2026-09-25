// Package extract 在结构化片段上执行概念抽取、否定/不确定判定与缩写消歧。
//
// 判定语义：
//   - 作用域：修饰词只作用于同一分句（以 ，,、;；： 或转折词 但/但是/然而 分隔）
//     内、且出现在其后的概念；不会波及其它分句的肯定表述。
//   - 同一概念在作用域内同时受否定与不确定修饰时，否定优先。
//   - 缩写按候选的小节倾向与共现关键词打分消歧；无法唯一判定时极性为 pending。
//   - 文档级结论按确定性优先级归并：affirmed > negated > uncertain > pending。
package extract

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/lisama569/clinical-note-extraction-go/internal/segment"
	"github.com/lisama569/clinical-note-extraction-go/internal/terminology"
)

// Polarity 是概念提及的最终极性。
type Polarity string

const (
	Affirmed  Polarity = "affirmed"  // 肯定
	Negated   Polarity = "negated"   // 否定
	Uncertain Polarity = "uncertain" // 不确定
	Pending   Polarity = "pending"   // 待定（缩写歧义无法判定）
)

// Mention 是一次概念命中及其判定依据。
type Mention struct {
	ConceptID string
	Surface   string // 命中的原文词面
	Segment   segment.Segment
	Polarity  Polarity
	Rationale string // 判定依据：命中的修饰词与作用域说明
}

// certainty 返回极性的确定性优先级，数值越大越确定。
func certainty(p Polarity) int {
	switch p {
	case Affirmed:
		return 3
	case Negated:
		return 2
	case Uncertain:
		return 1
	}
	return 0
}

// Conclusion 是同一概念在整份文档中的最终结论。
type Conclusion struct {
	ConceptID string
	Polarity  Polarity // 按确定性优先级从各 Mention 归并
	Rationale string
}

// token 是扫描得到的一个词法命中。
type token struct {
	start, end int // 在片段文本内的字节偏移
	surface    string
	conceptID  string                        // 概念命中时非空
	abbrev     []terminology.AbbrevCandidate // 缩写命中时非空
	modifier   *terminology.Modifier         // 修饰词命中时非空
}

// resetWords 是切断修饰词作用域的转折词。
var resetWords = []string{"但是", "但", "然而"}

// clauseBreaks 是分句边界字符。
const clauseBreaks = "，,、；;：。"

// Extract 在给定术语版本下对片段序列执行抽取。纯函数：同一 (segments, dict) 必然得到同一结果。
func Extract(segs []segment.Segment, dict *terminology.Dictionary) ([]Mention, []Conclusion) {
	terms := append([]terminology.Term{}, dict.Terms...)
	sort.SliceStable(terms, func(i, j int) bool { return len(terms[i].Surface) > len(terms[j].Surface) })
	mods := append([]terminology.Modifier{}, dict.Modifiers...)
	sort.SliceStable(mods, func(i, j int) bool { return len(mods[i].Surface) > len(mods[j].Surface) })
	abbrevs := make([]string, 0, len(dict.Abbrev))
	for a := range dict.Abbrev {
		abbrevs = append(abbrevs, a)
	}
	sort.SliceStable(abbrevs, func(i, j int) bool { return len(abbrevs[i]) > len(abbrevs[j]) })

	var mentions []Mention
	for _, seg := range segs {
		toks := scan(seg.Text, terms, mods, abbrevs, dict.Abbrev)
		mentions = append(mentions, resolve(seg, toks)...)
	}
	return mentions, conclude(mentions)
}

// scan 自左向右做最长匹配，术语 > 修饰词 > 缩写（同长度时）。
func scan(text string, terms []terminology.Term, mods []terminology.Modifier, abbrevs []string, am map[string][]terminology.AbbrevCandidate) []token {
	var toks []token
	i := 0
	for i < len(text) {
		matched := false
		for _, t := range terms {
			if strings.HasPrefix(text[i:], t.Surface) {
				toks = append(toks, token{start: i, end: i + len(t.Surface), surface: t.Surface, conceptID: t.ConceptID})
				i += len(t.Surface)
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		for _, m := range mods {
			if strings.HasPrefix(text[i:], m.Surface) {
				mc := m
				toks = append(toks, token{start: i, end: i + len(m.Surface), surface: m.Surface, modifier: &mc})
				i += len(m.Surface)
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		for _, a := range abbrevs {
			if strings.HasPrefix(text[i:], a) {
				toks = append(toks, token{start: i, end: i + len(a), surface: a, abbrev: am[a]})
				i += len(a)
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		_, size := utf8.DecodeRuneInString(text[i:])
		i += size
	}
	return toks
}

// scopeOf 计算位置 pos 所在分句的边界 [lo, hi)（片段文本内字节偏移）。
func scopeOf(text string, pos int) (int, int) {
	lo, hi := 0, len(text)
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		if strings.ContainsRune(clauseBreaks, r) {
			if i+size <= pos {
				lo = i + size
			} else if i > pos && i < hi {
				hi = i
			}
		}
		i += size
	}
	for _, w := range resetWords {
		for idx := 0; ; {
			j := strings.Index(text[idx:], w)
			if j < 0 {
				break
			}
			b := idx + j
			if b+len(w) <= pos && b+len(w) > lo {
				lo = b + len(w)
			} else if b > pos && b < hi {
				hi = b
			}
			idx = b + len(w)
		}
	}
	return lo, hi
}

// resolve 对单片段内的 token 序列做极性判定与缩写消歧。
func resolve(seg segment.Segment, toks []token) []Mention {
	var out []Mention
	for _, tok := range toks {
		if tok.modifier != nil {
			continue
		}
		m := Mention{Surface: tok.surface, Segment: seg}
		switch {
		case tok.conceptID != "":
			m.ConceptID = tok.conceptID
			m.Polarity, m.Rationale = polarityOf(seg.Text, toks, tok)
		case len(tok.abbrev) > 0:
			m.ConceptID, m.Polarity, m.Rationale = disambiguate(seg, tok)
			if m.Polarity != Pending {
				pol, rat := polarityOf(seg.Text, toks, tok)
				m.Polarity, m.Rationale = pol, m.Rationale+"; "+rat
			}
		default:
			continue
		}
		out = append(out, m)
	}
	return out
}

// polarityOf 根据作用域内的修饰词判定概念 token 的极性。
func polarityOf(text string, toks []token, concept token) (Polarity, string) {
	lo, hi := scopeOf(text, concept.start)
	negated, uncertain := false, false
	var negWord, uncWord string
	for _, t := range toks {
		if t.modifier == nil || t.start < lo || t.end > hi || t.start >= concept.start {
			continue // 作用域外或出现在概念之后的修饰词不生效
		}
		switch t.modifier.Kind {
		case terminology.Negation:
			negated, negWord = true, t.surface
		case terminology.Uncertainty:
			uncertain, uncWord = true, t.surface
		}
	}
	switch {
	case negated:
		return Negated, fmt.Sprintf("否定词%q作用于同一分句内其后的%q（分句范围[%d,%d)）", negWord, concept.surface, lo, hi)
	case uncertain:
		return Uncertain, fmt.Sprintf("不确定词%q作用于同一分句内其后的%q（分句范围[%d,%d)）", uncWord, concept.surface, lo, hi)
	}
	return Affirmed, fmt.Sprintf("分句范围[%d,%d)内无修饰词，判为肯定", lo, hi)
}

// disambiguate 按小节倾向与共现关键词对缩写候选打分。
func disambiguate(seg segment.Segment, tok token) (conceptID string, pol Polarity, rationale string) {
	cands := tok.abbrev
	if len(cands) == 1 {
		return cands[0].ConceptID, Affirmed, fmt.Sprintf("缩写%q仅有一个候选释义%s", tok.surface, cands[0].ConceptID)
	}
	type scored struct {
		c     terminology.AbbrevCandidate
		score int
		why   string
	}
	var best []scored
	bestScore := 0
	for _, c := range cands {
		sc := 0
		var whys []string
		for _, s := range c.Sections {
			if seg.Section == s {
				sc += 2
				whys = append(whys, "小节="+s)
			}
		}
		for _, k := range c.Keywords {
			if strings.Contains(seg.Text, k) {
				sc++
				whys = append(whys, "共现词="+k)
			}
		}
		if sc > bestScore {
			bestScore, best = sc, []scored{{c, sc, strings.Join(whys, ",")}}
		} else if sc == bestScore {
			best = append(best, scored{c, sc, strings.Join(whys, ",")})
		}
	}
	if bestScore > 0 && len(best) == 1 {
		return best[0].c.ConceptID, Affirmed,
			fmt.Sprintf("缩写%q按上下文消歧为%s（依据:%s，得分%d）", tok.surface, best[0].c.ConceptID, best[0].why, bestScore)
	}
	ids := make([]string, 0, len(cands))
	for _, c := range cands {
		ids = append(ids, c.ConceptID)
	}
	return "", Pending, fmt.Sprintf("缩写%q歧义无法判定，候选:%s（最高得分%d，并列%d个），标记待定",
		tok.surface, strings.Join(ids, "/"), bestScore, len(best))
}

// conclude 把同一概念的多次提及按确定性优先级归并为文档级结论。
func conclude(mentions []Mention) []Conclusion {
	byConcept := map[string][]Mention{}
	var order []string
	for _, m := range mentions {
		if m.Polarity == Pending {
			continue // 待定提及不参与归并，单独以 Mention 形式呈现
		}
		if _, ok := byConcept[m.ConceptID]; !ok {
			order = append(order, m.ConceptID)
		}
		byConcept[m.ConceptID] = append(byConcept[m.ConceptID], m)
	}
	sort.Strings(order) // 稳定输出
	var out []Conclusion
	for _, id := range order {
		ms := byConcept[id]
		best := ms[0]
		for _, m := range ms[1:] {
			if certainty(m.Polarity) > certainty(best.Polarity) {
				best = m
			}
		}
		out = append(out, Conclusion{
			ConceptID: id,
			Polarity:  best.Polarity,
			Rationale: fmt.Sprintf("共%d次提及，按确定性优先级取%s；依据:%s", len(ms), best.Polarity, best.Rationale),
		})
	}
	return out
}
