// Package segment 负责把自由文本病历切分为带原文位置的结构化片段。
//
// 规则：
//   - 行首出现“已知小节名 + ：/:”视为小节标题，冒号后的内容属于该小节。
//   - 任何小节标题之前的正文归入 "_preamble"，并标记 missing_section_header。
//   - 同名小节再次出现时不丢弃，内容保留并标记 duplicate_section。
//   - 句子以 。！？!?；; 为终止符；换行不终止句子（跨行内容被保留）。
//   - 小节或文档末尾缺少终止符的句子保留并标记 truncated_sentence。
package segment

import (
	"strings"
	"unicode/utf8"
)

// IssueCode 标识片段在切分过程中发现的可区分问题。
type IssueCode string

const (
	IssueMissingHeader    IssueCode = "missing_section_header" // 正文出现在任何小节标题之前
	IssueDuplicateSection IssueCode = "duplicate_section"      // 同名小节重复出现
	IssueTruncated        IssueCode = "truncated_sentence"     // 句末缺少终止符，疑似截断
)

// PreambleSection 是无标题正文归入的隐式小节名。
const PreambleSection = "_preamble"

// Issue 描述一个可区分的切分问题。
type Issue struct {
	Code   IssueCode
	Detail string
}

// Segment 是归一化后的结构化片段，保留原文位置。
type Segment struct {
	Section string // 所属小节名；无标题正文为 "_preamble"
	Text    string // 片段原文（可能含换行）
	Start   int    // 在原文中的字节起始偏移（含）
	End     int    // 字节结束偏移（不含）
	Issues  []Issue
}

// IsTerminator 报告 r 是否为句子终止符。
func IsTerminator(r rune) bool {
	switch r {
	case '。', '！', '？', '!', '?', '；', ';':
		return true
	}
	return false
}

// Split 把整份病历文本切分为结构化片段。纯函数：同一输入必然得到同一输出。
// sections 为已知小节标题集合；为 nil 时所有标题行都按普通正文处理。
func Split(text string, sections map[string]bool) []Segment {
	var out []Segment
	section := PreambleSection
	seenSection := false
	seen := map[string]bool{}

	// sentStart：当前句子的起始字节偏移；-1 表示当前不在句子中。
	sentStart := -1
	// pendingIssues：当前句子在 emit 时要附加的问题。
	var pending []Issue

	flush := func(end int, terminated bool) {
		if sentStart < 0 {
			return
		}
		body := text[sentStart:end]
		if strings.TrimSpace(body) == "" {
			sentStart = -1
			pending = nil
			return
		}
		seg := Segment{Section: section, Text: body, Start: sentStart, End: end, Issues: pending}
		if !terminated {
			seg.Issues = append(append([]Issue{}, seg.Issues...),
				Issue{Code: IssueTruncated, Detail: "句末缺少终止符"})
		}
		out = append(out, seg)
		sentStart = -1
		pending = nil
	}

	// matchHeader 在 pos 处尝试匹配“小节名 + 冒号”，返回小节名与标题结束偏移。
	matchHeader := func(pos int) (string, int, bool) {
		best := ""
		bestEnd := 0
		for name := range sections {
			if !strings.HasPrefix(text[pos:], name) {
				continue
			}
			after := pos + len(name)
			if after < len(text) && (text[after] == ':' || strings.HasPrefix(text[after:], "：")) {
				end := after + 1
				if strings.HasPrefix(text[after:], "：") {
					end = after + len("：")
				}
				if len(name) > len(best) { // 最长匹配优先，保证确定性
					best, bestEnd = name, end
				}
			}
		}
		if best == "" {
			return "", 0, false
		}
		return best, bestEnd, true
	}

	i := 0
	// atBoundary：当前位置允许匹配小节标题（行首或句子终止符之后）。
	atBoundary := true
	for i < len(text) {
		if atBoundary {
			if name, hdrEnd, ok := matchHeader(i); ok {
				flush(i, false) // 标题前的残句按截断处理
				section = name
				seenSection = true
				if seen[name] {
					pending = append(pending, Issue{Code: IssueDuplicateSection, Detail: "小节重复出现: " + name})
				}
				seen[name] = true
				i = hdrEnd
				atBoundary = false
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(text[i:])
		if r == '\n' {
			i += size
			atBoundary = true
			continue
		}
		if sentStart < 0 {
			if r == ' ' || r == '\t' || r == '\r' {
				i += size
				continue
			}
			sentStart = i
			if !seenSection {
				pending = append(pending, Issue{Code: IssueMissingHeader, Detail: "正文出现在任何小节标题之前"})
			}
		}
		i += size
		atBoundary = false
		if IsTerminator(r) {
			flush(i, true)
			atBoundary = true
		}
	}
	flush(len(text), false)
	return out
}
