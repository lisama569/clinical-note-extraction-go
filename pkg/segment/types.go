// Package segment 把自由文本病历切分为带原文位置的结构化片段：
// 分节（Section）与句子（Sentence）。所有位置均为原文字节偏移，
// 保证结果可追溯、重复处理完全一致。
package segment

// IssueKind 标记切分过程中遇到的异常类别。
type IssueKind string

const (
	// IssueNoHeader 正文出现在任何小节标题之前，无法归属到具名小节。
	IssueNoHeader IssueKind = "NO_HEADER"
	// IssueDuplicateHeader 同一小节标题在文中出现多次，正文分别保留并标记。
	IssueDuplicateHeader IssueKind = "DUPLICATE_HEADER"
	// IssueTruncatedLine 文本结尾缺少换行，疑似截断的行。
	IssueTruncatedLine IssueKind = "TRUNCATED_LINE"
	// IssueEmptyText 输入为空或仅含空白。
	IssueEmptyText IssueKind = "EMPTY_TEXT"
	// IssueBinaryNUL 文本中含有 NUL 等控制字符，疑似二进制/损坏输入。
	IssueBinaryNUL IssueKind = "BINARY_CONTROL_CHAR"
)

// Issue 记录切分中的异常或拒绝原因，可区分且可解释。
type Issue struct {
	Kind    IssueKind
	Message string
	// Start/End 为异常在原文中的字节偏移区间。
	Start int
	End   int
}

// Section 表示一个小节。同一标题重复出现时每个实例都是独立 Section，
// 并通过 Issues 中的 DUPLICATE_HEADER 标记。
type Section struct {
	Header string // 归一化后的小节标题（去空白、冒号）
	Index  int    // 小节在原文中的出现序号（0 起）
	// HeaderStart/HeaderEnd 标题行在原文中的字节区间；无标题小节为 0,-1。
	HeaderStart int
	HeaderEnd   int
	// ContentStart/ContentEnd 该小节正文的字节区间（含跨行合并范围）。
	ContentStart int
	ContentEnd   int
	// 该小节正文切分出的句子。
	Sentences []Sentence
	Issues    []Issue
}

// Sentence 表示正文切分出的句子，位置可回溯到原文。
type Sentence struct {
	Text  string
	Start int // 字节偏移（含）
	End   int // 字节偏移（不含）
}

// Document 是整份文本的切分结果。
type Document struct {
	Sections []Section
	Issues   []Issue // 文档级异常
}

// Splitter 负责分节与句子切分。
type Splitter struct{}
