package learn

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

const (
	MaxResponseJSONBytes  = 512 << 10
	maxHighLevelRunes     = 800
	maxExplanationRunes   = 800
	maxFollowUpRunes      = 200
	maxShallowBlocks      = 5
	maxDeepBlocks         = 12
	maxFollowUps          = 1
	maxLegacyExcerptLines = 80
	maxLegacyExcerptBytes = 16 << 10
)

// DecodeExplanation decodes exactly one JSON object and rejects unknown
// fields, trailing data, and oversized model responses.
func DecodeExplanation(raw string) (Explanation, error) {
	if len(raw) > MaxResponseJSONBytes {
		return Explanation{}, errors.New("model JSON exceeds response limit")
	}
	if !utf8.ValidString(raw) {
		return Explanation{}, errors.New("model JSON is not valid UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.DisallowUnknownFields()
	var exp Explanation
	if err := dec.Decode(&exp); err != nil {
		return Explanation{}, fmt.Errorf("invalid model JSON: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Explanation{}, errors.New("model JSON contains trailing data")
		}
		return Explanation{}, fmt.Errorf("invalid trailing model JSON: %w", err)
	}
	return exp, nil
}

// ValidateExplanation proves that every model-selected range and excerpt is
// an exact view of the validated source snapshot.
func ValidateExplanation(source SourceSnapshot, exp *Explanation) error {
	return validateExplanationWithBlockLimit(source, exp, maxShallowBlocks)
}

// ValidateExplanationForDepth applies the same block budget promised to the
// model: normal explanations may return five prioritized regions and --deep
// explanations may return twelve.
func ValidateExplanationForDepth(source SourceSnapshot, exp *Explanation, deep bool) error {
	limit := maxShallowBlocks
	if deep {
		limit = maxDeepBlocks
	}
	return validateExplanationWithBlockLimit(source, exp, limit)
}

// ValidateExplanationContent applies the strict model-output contract to the
// exact bounded bytes already supplied to an explainer. Code excerpts are
// always hydrated from this trusted snapshot: the model selects line ranges
// and explains them, but its attempted code copy is never displayed or
// persisted. This both removes a brittle byte-copy task from smaller models
// and makes source fidelity independent of model behavior.
func ValidateExplanationContent(path string, content []byte, exp *Explanation, deep bool) error {
	snapshot := SourceSnapshot{
		RelativePath: path,
		Content:      append([]byte(nil), content...),
		Lines:        splitSourceLines(string(content)),
	}
	if exp != nil {
		for i := range exp.Blocks {
			block := &exp.Blocks[i]
			if block.Start >= 1 && block.End >= block.Start && block.End <= len(snapshot.Lines) {
				block.Code = strings.Join(snapshot.Lines[block.Start-1:block.End], "\n")
			}
		}
	}
	return ValidateExplanationForDepth(snapshot, exp, deep)
}

func validateExplanationWithBlockLimit(source SourceSnapshot, exp *Explanation, blockLimit int) error {
	if exp == nil {
		return errors.New("missing explanation")
	}
	if err := boundedField("high_level", exp.HighLevel, 1, maxHighLevelRunes); err != nil {
		return err
	}
	if len(exp.Blocks) > blockLimit {
		return fmt.Errorf("too many blocks: %d (limit %d for requested depth)", len(exp.Blocks), blockLimit)
	}
	if len(source.Lines) > 0 && len(exp.Blocks) == 0 {
		return errors.New("at least one exact source block is required")
	}
	previousEnd := 0
	for i := range exp.Blocks {
		block := &exp.Blocks[i]
		if block.Start < 1 || block.End < block.Start || block.End > len(source.Lines) {
			return fmt.Errorf("block %d range %d-%d is outside source lines", i+1, block.Start, block.End)
		}
		if block.Start <= previousEnd {
			return fmt.Errorf("block %d overlaps or is out of order", i+1)
		}
		previousEnd = block.End
		exact := strings.Join(source.Lines[block.Start-1:block.End], "\n")
		if block.Code != exact {
			return fmt.Errorf("block %d code does not exactly match source lines %d-%d", i+1, block.Start, block.End)
		}
		if err := boundedField(fmt.Sprintf("blocks[%d].what", i), block.What, 1, maxExplanationRunes); err != nil {
			return err
		}
		for name, value := range map[string]string{"trap": block.Trap, "caution": block.Caution} {
			if err := boundedField(fmt.Sprintf("blocks[%d].%s", i, name), value, 0, maxExplanationRunes); err != nil {
				return err
			}
		}
	}
	if len(exp.FollowUps) > maxFollowUps {
		return fmt.Errorf("too many follow-up questions: %d", len(exp.FollowUps))
	}
	seen := map[string]struct{}{}
	for i, followUp := range exp.FollowUps {
		if strings.ContainsAny(followUp, "\r\n") {
			return fmt.Errorf("follow_ups[%d] must be one line", i)
		}
		if err := boundedField(fmt.Sprintf("follow_ups[%d]", i), followUp, 1, maxFollowUpRunes); err != nil {
			return err
		}
		key := strings.ToLower(strings.TrimSpace(followUp))
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("duplicate follow-up question %d", i)
		}
		seen[key] = struct{}{}
	}
	exp.Path = source.RelativePath
	exp.SourceSHA256 = source.SHA256
	exp.Language = source.Language
	return nil
}

func boundedField(name, value string, minRunes, maxRunes int) error {
	if !validText(value) {
		return fmt.Errorf("%s contains invalid UTF-8 or unsafe controls", name)
	}
	length := utf8.RuneCountInString(strings.TrimSpace(value))
	if length < minRunes {
		return fmt.Errorf("%s is required", name)
	}
	if length > maxRunes {
		return fmt.Errorf("%s exceeds %d characters", name, maxRunes)
	}
	return nil
}

// LegacyExplanation keeps compatibility with existing plain-text runners.
// The response becomes a bounded high-level note and the code is copied only
// from exact validated source data; JSON-looking malformed responses are not
// eligible for this compatibility path.
func LegacyExplanation(summary string, content []byte) (Explanation, error) {
	summary = sanitizeModelText(summary, maxHighLevelRunes)
	if summary == "" {
		summary = "This source was read, but the explainer returned no structured overview."
	}
	what := sanitizeModelText(summary, maxExplanationRunes)
	lines := splitSourceLines(string(content))
	exp := Explanation{HighLevel: summary}
	if len(lines) > 0 {
		start, excerpt, omitted, err := legacySourceExcerpt(lines)
		if err != nil {
			return Explanation{}, err
		}
		exp.Blocks = []Block{{
			Start: start + 1,
			End:   start + len(excerpt),
			Code:  strings.Join(excerpt, "\n"),
			What:  what,
		}}
		if omitted {
			exp.Blocks[0].Caution = "This compatibility view shows a bounded exact excerpt; use /learn --deep for prioritized regions."
		}
	}
	return exp, nil
}

func legacySourceExcerpt(lines []string) (start int, excerpt []string, omitted bool, err error) {
	start = -1
	for i, line := range lines {
		if len(line) <= maxLegacyExcerptBytes {
			start = i
			break
		}
	}
	if start < 0 {
		return 0, nil, false, fmt.Errorf("legacy learn response: no exact source line fits the %d-byte excerpt limit", maxLegacyExcerptBytes)
	}
	bytesUsed := 0
	for i := start; i < len(lines) && len(excerpt) < maxLegacyExcerptLines; i++ {
		additional := len(lines[i])
		if len(excerpt) > 0 {
			additional++ // exact newline inserted by strings.Join
		}
		if bytesUsed+additional > maxLegacyExcerptBytes {
			break
		}
		excerpt = append(excerpt, lines[i])
		bytesUsed += additional
	}
	if len(excerpt) == 0 {
		return 0, nil, false, errors.New("legacy learn response: exact source excerpt is empty")
	}
	omitted = start > 0 || start+len(excerpt) < len(lines)
	return start, excerpt, omitted, nil
}

func sanitizeModelText(value string, maxRunes int) string {
	if maxRunes <= 0 || !utf8.ValidString(value) {
		return ""
	}
	// Retain at most one rune beyond the presentation budget. This is enough
	// to detect truncation while keeping compatibility prose bounded even if a
	// legacy runner returns an unexpectedly large response.
	runes := make([]rune, 0, min(maxRunes+1, 1024))
	for _, r := range strings.TrimSpace(value) {
		if unsafeControl(r) {
			continue
		}
		runes = append(runes, r)
		if len(runes) > maxRunes {
			break
		}
	}
	if len(runes) <= maxRunes {
		return strings.TrimSpace(string(runes))
	}
	if maxRunes == 1 {
		return "…"
	}

	// Reserve one rune for an explicit omission marker. Prefer a complete
	// paragraph or sentence in the latter half of the budget; otherwise end at
	// a word boundary. This avoids presenting a severed sentence as complete.
	budget := maxRunes - 1
	window := runes[:budget]
	minimumUsefulBoundary := budget / 2
	cut := semanticTextBoundary(window, minimumUsefulBoundary)
	if cut == 0 {
		cut = budget
	}
	prefix := strings.TrimSpace(string(window[:cut]))
	if prefix == "" {
		return "…"
	}
	return prefix + "…"
}

func semanticTextBoundary(value []rune, minimum int) int {
	for i := len(value) - 2; i >= minimum; i-- {
		if value[i] == '\n' && value[i+1] == '\n' {
			return i
		}
	}
	for i := len(value) - 1; i >= minimum; i-- {
		if value[i] != '.' && value[i] != '!' && value[i] != '?' {
			continue
		}
		if i+1 == len(value) || isTextSpace(value[i+1]) {
			return i + 1
		}
	}
	for i := len(value) - 1; i > 0; i-- {
		if isTextSpace(value[i]) {
			return i
		}
	}
	return 0
}

func isTextSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r'
}
