package editor

// Motion enumerates vim cursor motions (§5.2.1, red VIM_COMPATIBILITY).
type Motion int

// Motions.
const (
	MotLeft Motion = iota
	MotRight
	MotUp
	MotDown
	MotWordForward // w
	MotWordBack    // b
	MotWordEnd     // e
	MotLineStart   // 0
	MotLineEnd     // $
	MotDocStart    // gg
	MotDocEnd      // G
	MotMatchPair   // %
	MotFindNext    // n
	MotFindPrev    // N
)

// Move applies a motion count times. Returns false when a motion failed
// (e.g. % with no matching pair).
func (b *Buffer) Move(m Motion, count int) bool {
	if count <= 0 {
		count = 1
	}
	ok := true
	for i := 0; i < count; i++ {
		switch m {
		case MotLeft:
			if b.Cur.Col > 0 {
				b.Cur.Col--
			} else if b.Cur.Line > 0 {
				b.Cur.Line--
				b.Cur.Col = len([]rune(b.LineText(b.Cur.Line)))
			}
		case MotRight:
			line := b.LineText(b.Cur.Line)
			if b.Cur.Col < len([]rune(line)) {
				b.Cur.Col++
			} else if b.Cur.Line < len(b.Lines)-1 {
				b.Cur.Line++
				b.Cur.Col = 0
			}
		case MotUp:
			if b.Cur.Line > 0 {
				b.Cur.Line--
			}
		case MotDown:
			if b.Cur.Line < len(b.Lines)-1 {
				b.Cur.Line++
			}
		case MotWordForward:
			b.forwardWord()
		case MotWordBack:
			b.backwardWord()
		case MotWordEnd:
			b.wordEnd()
		case MotLineStart:
			b.Cur.Col = 0
		case MotLineEnd:
			b.Cur.Col = max(len([]rune(b.LineText(b.Cur.Line)))-1, 0)
		case MotDocStart:
			b.Cur.Line, b.Cur.Col = 0, 0
		case MotDocEnd:
			b.Cur.Line = len(b.Lines) - 1
			b.Cur.Col = max(len([]rune(b.LineText(b.Cur.Line)))-1, 0)
		case MotMatchPair:
			if !b.matchPair() {
				ok = false
			}
		}
	}
	b.clamp()
	return ok
}

// forwardWord implements w: land on the first char of the next word.
func (b *Buffer) forwardWord() {
	line := b.LineText(b.Cur.Line)
	runes := []rune(line)
	i := b.Cur.Col
	// skip the current word (or punctuation run)
	for i < len(runes) && runes[i] != ' ' && runes[i] != '\t' {
		i++
	}
	// skip whitespace
	for i < len(runes) && (runes[i] == ' ' || runes[i] == '\t') {
		i++
	}
	if i > b.Cur.Col {
		b.Cur.Col = i
		return
	}
	// move to the next line
	if b.Cur.Line < len(b.Lines)-1 {
		b.Cur.Line++
		b.Cur.Col = 0
		b.forwardWord()
	}
}

// backwardWord implements b.
func (b *Buffer) backwardWord() {
	line := b.LineText(b.Cur.Line)
	runes := []rune(line)
	i := b.Cur.Col
	for i > 0 && (runes[i-1] == ' ' || runes[i-1] == '\t') {
		i--
	}
	for i > 0 && runes[i-1] != ' ' && runes[i-1] != '\t' {
		i--
	}
	if i < b.Cur.Col {
		b.Cur.Col = i
		return
	}
	if b.Cur.Line > 0 {
		b.Cur.Line--
		b.Cur.Col = len([]rune(b.LineText(b.Cur.Line)))
		b.backwardWord()
	}
}

// wordEnd implements e.
func (b *Buffer) wordEnd() {
	line := b.LineText(b.Cur.Line)
	runes := []rune(line)
	if b.Cur.Col >= len(runes) {
		if b.Cur.Line < len(b.Lines)-1 {
			b.Cur.Line++
			b.Cur.Col = 0
		}
		return
	}
	i := b.Cur.Col
	for i < len(runes)-1 && isWordRune(runes[i+1]) {
		i++
	}
	b.Cur.Col = i
}

// matchPair implements % on ()[]{}.
func (b *Buffer) matchPair() bool {
	line := b.LineText(b.Cur.Line)
	runes := []rune(line)
	pairs := map[rune]rune{')': '(', ']': '[', '}': '{'}
	open := map[rune]rune{'(': ')', '[': ']', '{': '}'}
	if b.Cur.Col >= len(runes) {
		return false
	}
	ch := runes[b.Cur.Col]
	if closer, ok := pairs[ch]; ok {
		// backward: ) → ( — ) deepens, ( resolves
		depth := 0
		for i := b.Cur.Col; i >= 0; i-- {
			switch runes[i] {
			case ch:
				depth++
			case closer:
				depth--
				if depth == 0 {
					b.Cur.Col = i
					return true
				}
			}
		}
		return false
	}
	if opener, ok := open[ch]; ok {
		// forward: ( → ) — ( deepens, ) resolves
		depth := 0
		for i := b.Cur.Col; i < len(runes); i++ {
			switch runes[i] {
			case ch:
				depth++
			case opener:
				depth--
				if depth == 0 {
					b.Cur.Col = i
					return true
				}
			}
		}
	}
	return false
}

// FindChar implements f: move to the next occurrence of ch on the line.
func (b *Buffer) FindChar(ch rune) bool {
	runes := []rune(b.LineText(b.Cur.Line))
	for i := b.Cur.Col + 1; i < len(runes); i++ {
		if runes[i] == ch {
			b.Cur.Col = i
			return true
		}
	}
	return false
}

// ToChar implements t: move just before the next occurrence of ch.
func (b *Buffer) ToChar(ch rune) bool {
	runes := []rune(b.LineText(b.Cur.Line))
	for i := b.Cur.Col + 1; i < len(runes); i++ {
		if runes[i] == ch {
			b.Cur.Col = i - 1
			return true
		}
	}
	return false
}

// FindOnLine searches for needle from the cursor; wraps across lines.
func (b *Buffer) FindOnLine(needle string) (Cursor, bool) {
	for line := b.Cur.Line; line < len(b.Lines); line++ {
		text := b.LineText(line)
		start := 0
		if line == b.Cur.Line {
			start = b.Cur.Col
		}
		if idx := indexRune(text, needle, start); idx >= 0 {
			return Cursor{Line: line, Col: idx}, true
		}
	}
	return Cursor{}, false
}

// indexRune finds needle in text starting at rune index start.
func indexRune(text, needle string, start int) int {
	runes := []rune(text)
	needleRunes := []rune(needle)
	if len(needleRunes) == 0 {
		return -1
	}
	for i := start; i+len(needleRunes) <= len(runes); i++ {
		match := true
		for k := range needleRunes {
			if runes[i+k] != needleRunes[k] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
