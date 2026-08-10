package editor

import (
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// HighlightKind is a token class.
type HighlightKind int

// Highlight kinds.
const (
	HlNone HighlightKind = iota
	HlKeyword
	HlString
	HlComment
	HlType
	HlFunc
	HlNumber
	HlTitle // markdown headings
)

// Span is one highlighted run.
type Span struct {
	Start, End int // rune offsets
	Kind       HighlightKind
}

// Highlighter produces syntax spans for a line. Built-in regex-based
// implementation; tree-sitter can slot in behind the same interface.
type Highlighter interface {
	Spans(language, line string) []Span
	Detect(path string) string
}

// builtinHighlighter is the pure-Go fallback highlighter.
type builtinHighlighter struct{}

// Detect maps a file path to a language name.
func (builtinHighlighter) Detect(path string) string {
	path = filepath.ToSlash(strings.ToLower(strings.TrimSpace(path)))
	base := filepath.Base(path)
	switch base {
	case "dockerfile", "containerfile":
		return "dockerfile"
	case "makefile", "gnumakefile":
		return "makefile"
	case ".env", ".env.local", ".env.development", ".env.production":
		return "dotenv"
	}
	switch filepath.Ext(path) {
	case ".go":
		return "go"
	case ".rs":
		return "rust"
	case ".py", ".pyw":
		return "python"
	case ".rb":
		return "ruby"
	case ".php":
		return "php"
	case ".java":
		return "java"
	case ".kt", ".kts":
		return "kotlin"
	case ".swift":
		return "swift"
	case ".dart":
		return "dart"
	case ".c", ".h":
		return "c"
	case ".cc", ".cpp", ".cxx", ".hpp":
		return "cpp"
	case ".cs":
		return "csharp"
	case ".md", ".markdown", ".mdx":
		return "markdown"
	case ".json", ".jsonc":
		return "json"
	case ".toml":
		return "toml"
	case ".yaml", ".yml":
		return "yaml"
	case ".xml", ".xhtml":
		return "xml"
	case ".html", ".htm":
		return "html"
	case ".css", ".scss", ".less":
		return "css"
	case ".sql":
		return "sql"
	case ".sh", ".bash", ".zsh", ".fish":
		return "bash"
	case ".lua":
		return "lua"
	case ".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx":
		return "javascript"
	default:
		return ""
	}
}

// DetectContent adds lightweight content detection for extensionless files
// and scripts whose filename does not carry enough information. It is kept
// separate from Detect so custom Highlighter implementations remain simple.
func (h builtinHighlighter) DetectContent(path, firstLine string) string {
	if language := h.Detect(path); language != "" {
		return language
	}
	line := strings.ToLower(strings.TrimSpace(firstLine))
	switch {
	case strings.HasPrefix(line, "#!") && strings.Contains(line, "python"):
		return "python"
	case strings.HasPrefix(line, "#!") && (strings.Contains(line, "bash") || strings.Contains(line, "sh") || strings.Contains(line, "zsh")):
		return "bash"
	case strings.HasPrefix(line, "<?xml"):
		return "xml"
	case strings.HasPrefix(line, "<!doctype html"), strings.HasPrefix(line, "<html"):
		return "html"
	case strings.HasPrefix(line, "package "), strings.HasPrefix(line, "func "):
		return "go"
	case strings.HasPrefix(line, "import "), strings.HasPrefix(line, "from "), strings.HasPrefix(line, "def "):
		return "python"
	case strings.HasPrefix(line, "{\""), strings.HasPrefix(line, "[{"):
		return "json"
	}
	return ""
}

// IsMarkdownPath reports whether a buffer can be rendered as Markdown.
func IsMarkdownPath(path string) bool {
	return builtinHighlighter{}.Detect(path) == "markdown"
}

var (
	commentRe = map[string]*regexp.Regexp{
		"go":         regexp.MustCompile(`//.*$|/\*.*?\*/`),
		"rust":       regexp.MustCompile(`//.*$|/\*.*?\*/`),
		"c":          regexp.MustCompile(`//.*$|/\*.*?\*/`),
		"cpp":        regexp.MustCompile(`//.*$|/\*.*?\*/`),
		"csharp":     regexp.MustCompile(`//.*$|/\*.*?\*/`),
		"java":       regexp.MustCompile(`//.*$|/\*.*?\*/`),
		"kotlin":     regexp.MustCompile(`//.*$|/\*.*?\*/`),
		"swift":      regexp.MustCompile(`//.*$|/\*.*?\*/`),
		"javascript": regexp.MustCompile(`//.*$|/\*.*?\*/`),
		"python":     regexp.MustCompile(`#.*$`),
		"ruby":       regexp.MustCompile(`#.*$`),
		"bash":       regexp.MustCompile(`#.*$`),
		"lua":        regexp.MustCompile(`--.*$`),
		"toml":       regexp.MustCompile(`#.*$`),
		"yaml":       regexp.MustCompile(`#.*$`),
		"makefile":   regexp.MustCompile(`#.*$`),
		"sql":        regexp.MustCompile(`--.*$|/\*.*?\*/`),
	}
	stringRe       = regexp.MustCompile(`"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'|` + "`[^`]*`")
	numberRe       = regexp.MustCompile(`\b\d+(\.\d+)?\b`)
	markdownLinkRe = regexp.MustCompile(`\[[^]]+\]\([^)]*\)`)
	goFuncRe       = regexp.MustCompile(`\bfunc\s+([A-Za-z_]\w*)`)
	goTypeRe       = regexp.MustCompile(`\b[A-Z]\w*\b`)
	htmlTagRe      = regexp.MustCompile(`</?[A-Za-z][^>]*>`)
	keywords       = map[string][]string{
		"go":         {"func", "package", "import", "return", "if", "else", "for", "range", "switch", "case", "default", "go", "defer", "struct", "interface", "type", "const", "var", "map", "chan", "select", "break", "continue", "fallthrough", "goto", "make", "new", "nil", "true", "false", "error"},
		"rust":       {"fn", "use", "mod", "pub", "impl", "trait", "struct", "enum", "type", "let", "mut", "const", "static", "return", "if", "else", "match", "for", "while", "loop", "async", "await", "move", "self", "Self", "true", "false"},
		"c":          {"int", "char", "void", "struct", "typedef", "const", "static", "return", "if", "else", "for", "while", "switch", "case", "break", "continue", "sizeof", "NULL"},
		"cpp":        {"auto", "class", "struct", "template", "typename", "namespace", "using", "const", "static", "public", "private", "protected", "return", "if", "else", "for", "while", "switch", "case", "true", "false", "nullptr"},
		"csharp":     {"class", "namespace", "using", "public", "private", "protected", "static", "void", "var", "new", "return", "if", "else", "for", "foreach", "while", "async", "await", "true", "false", "null"},
		"java":       {"class", "interface", "package", "import", "public", "private", "protected", "static", "final", "void", "new", "return", "if", "else", "for", "while", "try", "catch", "extends", "implements", "true", "false", "null"},
		"kotlin":     {"fun", "class", "object", "interface", "package", "import", "val", "var", "return", "if", "else", "when", "for", "while", "in", "is", "data", "null", "true", "false"},
		"swift":      {"func", "class", "struct", "enum", "protocol", "import", "let", "var", "guard", "if", "else", "for", "while", "switch", "case", "return", "async", "await", "true", "false", "nil"},
		"dart":       {"class", "abstract", "import", "library", "part", "final", "const", "var", "dynamic", "void", "return", "if", "else", "for", "while", "async", "await", "true", "false", "null"},
		"python":     {"def", "class", "import", "from", "return", "if", "elif", "else", "for", "while", "try", "except", "finally", "with", "as", "lambda", "pass", "break", "continue", "True", "False", "None"},
		"ruby":       {"def", "class", "module", "require", "include", "attr_reader", "return", "if", "elsif", "else", "unless", "case", "when", "while", "do", "end", "true", "false", "nil"},
		"php":        {"function", "class", "interface", "namespace", "use", "public", "private", "protected", "static", "return", "if", "else", "foreach", "while", "new", "true", "false", "null"},
		"javascript": {"function", "const", "let", "var", "return", "if", "else", "for", "while", "switch", "case", "class", "import", "export", "async", "await", "try", "catch", "new", "typeof"},
		"bash":       {"if", "then", "else", "fi", "for", "while", "do", "done", "function", "case", "esac", "export", "local", "echo"},
		"lua":        {"function", "local", "return", "if", "then", "else", "elseif", "end", "for", "while", "do", "repeat", "until", "true", "false", "nil"},
		"sql":        {"select", "from", "where", "insert", "into", "update", "delete", "create", "alter", "drop", "table", "join", "left", "right", "inner", "outer", "on", "as", "and", "or", "not", "null", "order", "by", "group", "limit"},
	}
	titleRe = regexp.MustCompile(`^#{1,6}\s`)
)

var keywordReCache = map[string]*regexp.Regexp{}

func keywordRE(lang string) *regexp.Regexp {
	if re, ok := keywordReCache[lang]; ok {
		return re
	}
	words := keywords[lang]
	if len(words) == 0 {
		return nil
	}
	re := regexp.MustCompile(`\b(` + strings.Join(words, "|") + `)\b`)
	keywordReCache[lang] = re
	return re
}

// Spans computes the highlight spans of one line.
func (h builtinHighlighter) Spans(language, line string) []Span {
	if language == "" {
		return nil
	}
	var spans []Span
	add := func(kind HighlightKind, start, end int) {
		if start >= end {
			return
		}
		spans = append(spans, Span{Start: start, End: end, Kind: kind})
	}
	addMatch := func(kind HighlightKind, match []int) {
		if len(match) < 2 || match[0] < 0 || match[1] < 0 {
			return
		}
		// regexp indexes are byte offsets, while the editor cursor and
		// renderer operate on runes. Keep Unicode source files aligned.
		add(kind, utf8.RuneCountInString(line[:match[0]]), utf8.RuneCountInString(line[:match[1]]))
	}
	if language == "markdown" {
		if m := titleRe.FindStringIndex(line); m != nil {
			addMatch(HlTitle, m)
		}
		if m := markdownLinkRe.FindStringIndex(line); m != nil {
			addMatch(HlFunc, m)
		}
		return spans
	}
	// Strings first (they win over everything inside).
	for _, m := range stringRe.FindAllStringIndex(line, -1) {
		addMatch(HlString, m)
	}
	if re := commentRe[language]; re != nil {
		for _, m := range re.FindAllStringIndex(line, -1) {
			addMatch(HlComment, m)
		}
	}
	if re := keywordRE(language); re != nil {
		for _, m := range re.FindAllStringIndex(line, -1) {
			addMatch(HlKeyword, m)
		}
	}
	if language == "go" {
		// func names and capitalized types.
		if m := goFuncRe.FindStringSubmatchIndex(line); len(m) >= 4 {
			addMatch(HlFunc, m[2:4])
		}
		for _, m := range goTypeRe.FindAllStringIndex(line, -1) {
			addMatch(HlType, m)
		}
	}
	if language == "html" || language == "xml" {
		for _, m := range htmlTagRe.FindAllStringIndex(line, -1) {
			addMatch(HlType, m)
		}
	}
	for _, m := range numberRe.FindAllStringIndex(line, -1) {
		addMatch(HlNumber, m)
	}
	return spans
}
