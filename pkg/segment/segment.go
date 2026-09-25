package segment

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// knownHeaders 是本地构造病历使用的小节标题白名单（中英文）。
// 比较时统一大写、去首尾空白与结尾冒号。
var knownHeaders = map[string]struct{}{
	// 中文
	"主诉": {}, "现病史": {}, "既往史": {}, "个人史": {},
	"查体": {}, "辅助检查": {}, "诊断": {}, "处理": {},
	// 英文
	"CHIEF COMPLAINT":            {},
	"HISTORY OF PRESENT ILLNESS": {},
	"PAST MEDICAL HISTORY":       {},
	"PERSONAL HISTORY":           {},
	"PHYSICAL EXAM":              {},
	"PHYSICAL EXAMINATION":       {},
	"LABS":                       {},
	"ASSESSMENT":                 {},
	"DIAGNOSIS":                  {},
	"PLAN":                       {},
}

// sentenceTerminators 为句子结束符。分号/逗号不是句子边界，
// 因此 "无发热；有咳嗽。" 中的否定不会跨越分号作用到咳嗽（见 extract 的子句边界）。
var sentenceTerminators = map[rune]struct{}{
	'。': {}, '！': {}, '？': {},
	'.': {}, '!': {}, '?': {},
}

// normalizeHeader 归一化标题：去首尾空白、去结尾中英文冒号、ASCII 大写。
func normalizeHeader(line string) (string, bool) {
	t := strings.TrimSpace(line)
	if t == "" {
		return "", false
	}
	for {
		u := strings.TrimRight(t, ":：")
		if u == t {
			break
		}
		t = strings.TrimSpace(u)
	}
	up := strings.ToUpper(t)
	if _, ok := knownHeaders[up]; ok {
		return up, true
	}
	if _, ok := knownHeaders[t]; ok {
		return t, true
	}
	return "", false
}

// trimByteRange 返回 [start,end) 内去掉首尾空白后的字节区间（基于 rune 判空白）。
func trimByteRange(text string, start, end int) (int, int) {
	s, e := start, end
	for s < e {
		r, size := utf8.DecodeRuneInString(text[s:e])
		if !unicode.IsSpace(r) {
			break
		}
		s += size
	}
	for e > s {
		r, size := utf8.DecodeLastRuneInString(text[s:e])
		if !unicode.IsSpace(r) {
			break
		}
		e -= size
	}
	return s, e
}

// normalizeSpace 将区间内连续空白（含跨行换行）折叠为单个空格。
func normalizeSpace(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	pendingSpace := false
	started := false
	for _, r := range text {
		if unicode.IsSpace(r) {
			if started {
				pendingSpace = true
			}
			continue
		}
		if pendingSpace {
			b.WriteByte(' ')
			pendingSpace = false
		}
		b.WriteRune(r)
		started = true
	}
	return b.String()
}

func makeSentence(text string, start, end int) *Sentence {
	s, e := trimByteRange(text, start, end)
	if s >= e {
		return nil
	}
	return &Sentence{Text: normalizeSpace(text[s:e]), Start: s, End: e}
}

// splitSentences 在小节正文区间 text[start:end] 内切句，位置为原文档绝对字节偏移。
// 换行不作为句子边界（跨行内容合并），连续空白折叠为单空格。
func splitSentences(text string, start, end int) []Sentence {
	var out []Sentence
	segStart := start
	at := start
	for at < end {
		r, size := utf8.DecodeRuneInString(text[at:end])
		if _, isTerm := sentenceTerminators[r]; isTerm {
			segEnd := at + size
			if s := makeSentence(text, segStart, segEnd); s != nil {
				out = append(out, *s)
			}
			segStart = segEnd
		}
		at += size
	}
	if s := makeSentence(text, segStart, end); s != nil {
		out = append(out, *s)
	}
	return out
}

// Split 执行分节与句子切分。
//
// 规则：
//   - 整行匹配标题白名单（去空白/结尾冒号、忽略大小写）才视为小节标题；
//   - 首个标题之前出现的正文归入匿名小节并标记 NO_HEADER，不静默丢弃；
//   - 同一标题重复出现时，每次出现都生成独立小节并标记 DUPLICATE_HEADER；
//   - 换行不是句子边界，跨行内容合并，连续空白折叠为单空格；
//   - 文本以无结束符的非空行结尾且缺少尾换行时，标记 TRUNCATED_LINE；
//   - 空/纯空白输入标记 EMPTY_TEXT；含 NUL 的输入判定为二进制并整体拒绝。
func (sp *Splitter) Split(text string) *Document {
	doc := &Document{}

	if idx := strings.IndexByte(text, 0); idx >= 0 {
		doc.Issues = append(doc.Issues, Issue{
			Kind:    IssueBinaryNUL,
			Message: "文本包含 NUL 控制字符，疑似二进制/损坏输入，拒绝切分",
			Start:   idx,
			End:     idx + 1,
		})
		return doc
	}
	if strings.TrimSpace(text) == "" {
		doc.Issues = append(doc.Issues, Issue{
			Kind: IssueEmptyText, Message: "输入为空或仅含空白字符",
			Start: 0, End: len(text),
		})
		return doc
	}

	headerCount := map[string]int{}
	var current *Section
	var lastNonEmpty [2]int // 最后一个非空行区间
	haveLastLine := false
	lastIsHeader := false
	lastLineEndedNewline := false // 最后一个非空行是否以换行结束

	flush := func() {
		if current == nil {
			return
		}
		if 0 <= current.ContentStart && current.ContentStart < current.ContentEnd {
			current.Sentences = splitSentences(text, current.ContentStart, current.ContentEnd)
		}
		doc.Sections = append(doc.Sections, *current)
		current = nil
	}

	pos := 0
	for pos <= len(text) {
		nl := strings.IndexByte(text[pos:], '\n')
		var lineEnd, nextPos int
		if nl < 0 {
			lineEnd = len(text)
			nextPos = len(text) + 1
		} else {
			lineEnd = pos + nl
			nextPos = lineEnd + 1
		}
		rawLine := text[pos:lineEnd]
		contentEnd := lineEnd
		if contentEnd > pos && text[contentEnd-1] == '\r' {
			contentEnd--
		}
		trimStart, trimEnd := trimByteRange(text, pos, contentEnd)
		lineNonEmpty := trimStart < trimEnd
		if lineNonEmpty {
			lastNonEmpty = [2]int{trimStart, trimEnd}
			haveLastLine = true
			lastLineEndedNewline = nl >= 0
		}

		if name, isHeader := normalizeHeader(rawLine); isHeader {
			flush()
			sec := &Section{
				Header:       name,
				Index:        len(doc.Sections),
				HeaderStart:  trimStart,
				HeaderEnd:    trimEnd,
				ContentStart: -1,
				ContentEnd:   -1,
			}
			headerCount[name]++
			if headerCount[name] > 1 {
				sec.Issues = append(sec.Issues, Issue{
					Kind:    IssueDuplicateHeader,
					Message: "小节标题重复出现：" + name + "（第 " + strconv.Itoa(headerCount[name]) + " 次）",
					Start:   trimStart,
					End:     trimEnd,
				})
			}
			current = sec
			if lineNonEmpty {
				lastIsHeader = true
			}
		} else if lineNonEmpty {
			if current == nil {
				current = &Section{
					Header:       "",
					Index:        len(doc.Sections),
					HeaderStart:  -1,
					HeaderEnd:    -1,
					ContentStart: trimStart,
					ContentEnd:   trimEnd,
				}
				current.Issues = append(current.Issues, Issue{
					Kind:    IssueNoHeader,
					Message: "正文出现在任何小节标题之前，已归入匿名小节而非丢弃",
					Start:   trimStart,
					End:     trimEnd,
				})
			} else {
				if current.ContentStart == -1 {
					current.ContentStart = trimStart
				}
				if trimEnd > current.ContentEnd {
					current.ContentEnd = trimEnd
				}
			}
			lastIsHeader = false
		}
		if nl < 0 {
			break
		}
		pos = nextPos
	}
	flush()

	// 截断检测：最后一个非空行无尾换行、且不是以句子结束符收尾。
	if haveLastLine && !lastLineEndedNewline && !lastIsHeader {
		r, _ := utf8.DecodeLastRuneInString(text[lastNonEmpty[0]:lastNonEmpty[1]])
		if _, isTerm := sentenceTerminators[r]; !isTerm {
			doc.Issues = append(doc.Issues, Issue{
				Kind:    IssueTruncatedLine,
				Message: "末行缺少换行且无句子结束符，内容疑似截断：「" + text[lastNonEmpty[0]:lastNonEmpty[1]] + "」",
				Start:   lastNonEmpty[0],
				End:     lastNonEmpty[1],
			})
		}
	}

	return doc
}
