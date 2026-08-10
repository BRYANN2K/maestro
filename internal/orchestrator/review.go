package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/security"
	"github.com/bryann2k/maestro/internal/session"
)

// ReviewItem is one finding of a review, at a level.
type ReviewItem struct {
	Level   string // pass | warn | fail
	Message string
}

// Verdict is the reviewer's structured assessment.
type Verdict struct {
	Items   []ReviewItem
	Summary string
}

// ReviewFailedError reports a blocking review while retaining the structured
// verdict for CLI and TUI callers.
type ReviewFailedError struct {
	Verdict Verdict
}

func (e *ReviewFailedError) Error() string {
	return e.Verdict.Summary + "; run /fix and review again"
}

// VerdictLevel aggregates the worst level present.
func (v Verdict) VerdictLevel() string {
	worst := "pass"
	for _, it := range v.Items {
		switch it.Level {
		case "fail":
			return "fail"
		case "warn":
			worst = "warn"
		}
	}
	return worst
}

// Findings returns the actionable fail+warn items as one text block.
func (v Verdict) Findings() string {
	var b strings.Builder
	for _, it := range v.Items {
		if it.Level == "fail" || it.Level == "warn" {
			fmt.Fprintf(&b, "- [%s] %s\n", it.Level, it.Message)
		}
	}
	return b.String()
}

// Review runs the deterministic gates — gofmt on changed files, go vet, and
// the test suite — plus spec-alignment checks, and enters the review phase.
// The LLM reviewer (native spawn) replaces this stub in B5.
func (o *Orchestrator) Review(ctx context.Context) (Verdict, error) {
	if o.spec == nil {
		return Verdict{}, errors.New("review: no active spec")
	}
	from := o.sess.Phase
	if from != session.PhaseBuild && from != session.PhaseReview && from != session.PhaseDocs {
		return Verdict{}, fmt.Errorf("review: cannot start from phase %q", from)
	}
	if err := o.validateSessionWorkspaceIdentity(ctx, "review"); err != nil {
		return Verdict{}, err
	}
	if (from == session.PhaseReview || from == session.PhaseDocs) && o.sess.Review != nil && o.sess.Review.GitRef != "" && o.sess.Review.GitHead != "" {
		if err := o.requireReviewedGitIdentity(ctx, "review"); err != nil {
			return Verdict{}, err
		}
	}
	v := Verdict{}
	pendingState, contractErr := o.validatePendingSpecContract()
	if contractErr != nil {
		v.Items = append(v.Items, ReviewItem{Level: "fail", Message: contractErr.Error()})
	}
	reviewedIdentity, err := readGitWorkspaceIdentity(ctx, o.workDir())
	if err != nil {
		return Verdict{}, fmt.Errorf("review: capture Git identity: %w", err)
	}
	reviewedState, err := o.worktreeFingerprint(ctx)
	if err != nil {
		return Verdict{}, fmt.Errorf("review: fingerprint worktree: %w", err)
	}
	o.emit(agentcore.NewEvent(nil, agentcore.RoleReviewer, agentcore.EvSubAgent, agentcore.SubAgentStatus{Role: "reviewer", Status: "running", Detail: "deterministic gates"}))
	v.Items = append(v.Items, o.gofmtCheck(ctx)...)
	v.Items = append(v.Items, o.vetCheck(ctx)...)
	v.Items = append(v.Items, o.testCheck(ctx)...)
	v.Items = append(v.Items, o.taskAlignment(ctx)...)
	// B8 gates: security scan (F5), comprehension (8.7), TDD (8.9).
	v.Items = append(v.Items, o.securityItems(ctx)...)
	v.Items = append(v.Items, o.comprehensionChecks(ctx)...)
	v.Items = append(v.Items, o.tddGate(ctx)...)
	if o.runner == nil && (o.registry != nil || o.SettingsSnapshot().RoleDefaults[string(agentcore.RoleReviewer)].Engine == "legacy") {
		v.Items = append(v.Items, o.agentReview(ctx)...)
	}

	// Checkbox progress remains untrusted throughout every gate. Only a
	// non-failing gate set may prepare it for the same atomic session save as
	// the passing review fingerprint.
	var previousTaskStates []bool
	contractAdvanced := false
	if v.VerdictLevel() != "fail" {
		previousTaskStates, err = o.advanceSpecContract(pendingState)
		if err != nil {
			v.Items = append(v.Items, ReviewItem{Level: "fail", Message: fmt.Sprintf("authorize reviewed task progress: %v", err)})
		} else {
			contractAdvanced = true
		}
	}

	finalState, fingerprintErr := o.worktreeFingerprint(ctx)
	if fingerprintErr != nil {
		v.Items = append(v.Items, ReviewItem{Level: "fail", Message: fmt.Sprintf("worktree integrity snapshot failed: %v", fingerprintErr)})
	} else if finalState != reviewedState {
		v.Items = append(v.Items, ReviewItem{Level: "fail", Message: "worktree changed while review gates were running; rerun /review"})
	}
	finalIdentity, identityErr := readGitWorkspaceIdentity(ctx, o.workDir())
	identityStable := identityErr == nil && finalIdentity == reviewedIdentity
	if identityErr != nil {
		v.Items = append(v.Items, ReviewItem{Level: "fail", Message: fmt.Sprintf("Git identity snapshot failed: %v", identityErr)})
	} else if !identityStable {
		v.Items = append(v.Items, ReviewItem{Level: "fail", Message: "Git ref or HEAD changed while review gates were running; rerun /review"})
	}
	level := v.VerdictLevel()
	if level == "fail" && contractAdvanced {
		o.sess.SpecContract.TaskStates = previousTaskStates
		contractAdvanced = false
	}
	v.Summary = fmt.Sprintf("Review: %s — %d items", level, len(v.Items))
	for _, it := range v.Items {
		fmt.Fprintf(o.out, "  [%s] %s\n", it.Level, it.Message)
	}
	fmt.Fprintf(o.out, "%s\n", v.Summary)
	fingerprint := ""
	gitRef := ""
	gitHead := ""
	if level != "fail" && fingerprintErr == nil && finalState == reviewedState && identityStable {
		fingerprint = finalState
		gitRef = finalIdentity.ref
		gitHead = finalIdentity.head
	}
	reviewResult := &session.ReviewResult{Level: level, Summary: v.Summary, Findings: v.Findings(), Fingerprint: fingerprint, GitRef: gitRef, GitHead: gitHead}
	targetPhase := from
	if level == "fail" {
		// A failed gate is not a completed review. Returning to build keeps the
		// state machine honest while /fix consumes the persisted findings.
		if from == session.PhaseReview || from == session.PhaseDocs {
			targetPhase = session.PhaseBuild
		}
	} else if from != session.PhaseDocs {
		targetPhase = session.PhaseReview
	}
	if err := o.persistReviewResult(from, targetPhase, reviewResult, contractAdvanced, previousTaskStates); err != nil {
		return v, err
	}
	o.emit(agentcore.NewEvent(nil, agentcore.RoleReviewer, agentcore.EvSubAgent, agentcore.SubAgentStatus{Role: "reviewer", Status: "done", Detail: level}))
	if targetPhase != from {
		o.emitPhase(from, targetPhase)
	}
	if level == "fail" {
		return v, &ReviewFailedError{Verdict: v}
	}
	if from == session.PhaseDocs {
		// Re-reviewing an accepted ADR rebaselines manual corrections without
		// regressing the lifecycle: the next valid action remains /archive.
		return v, nil
	}
	return v, nil
}

// persistReviewResult publishes the verdict, phase, and newly authorized task
// states in one session write. A persistence refusal restores all in-memory
// fields, so neither the current process nor a restart can mistake pending
// checkbox progress for reviewed work.
func (o *Orchestrator) persistReviewResult(from, target session.Phase, result *session.ReviewResult, contractAdvanced bool, previousTaskStates []bool) error {
	if err := phases.Transition(from, target); err != nil {
		if contractAdvanced {
			o.sess.SpecContract.TaskStates = previousTaskStates
		}
		return err
	}
	previousReview := o.sess.Review
	previousPhase := o.sess.Phase
	o.sess.Review = result
	o.sess.Phase = target
	if err := o.save(); err != nil {
		o.sess.Review = previousReview
		o.sess.Phase = previousPhase
		if contractAdvanced {
			o.sess.SpecContract.TaskStates = previousTaskStates
		}
		return fmt.Errorf("review: persist verdict and task authorization: %w", err)
	}
	return nil
}

func (o *Orchestrator) agentReview(ctx context.Context) []ReviewItem {
	runner, err := o.runnerForRole(string(agentcore.RoleReviewer))
	if err != nil {
		return []ReviewItem{{Level: "fail", Message: "agent review unavailable: " + err.Error()}}
	}
	prompt := o.maestroTaskPrompt(`MAESTRO_OPERATION: REVIEW_READ_ONLY

Review the supplied spec and git diff for correctness, regressions, security, concurrency, error handling, and missing tests.
Return findings only, one per line, exactly as "[fail] message", "[warn] message", or "[pass] message". Include file and line when known. Do not modify files.`)
	ctx, cancel := o.bindBudgetKill(ctx)
	defer cancel()
	res, err := runner.Run(ctx, agentcore.RoleReviewer, prompt)
	if err != nil {
		return []ReviewItem{{Level: "fail", Message: "agent review failed: " + err.Error()}}
	}
	if !res.OK {
		message := strings.TrimSpace(res.Summary)
		if message == "" {
			message = "reviewer returned ok=false without an explanation"
		}
		return []ReviewItem{{Level: "fail", Message: "agent review rejected: " + message}}
	}
	if res.Role != string(agentcore.RoleReviewer) {
		return []ReviewItem{{Level: "fail", Message: fmt.Sprintf("agent reviewer returned invalid role %q", res.Role)}}
	}
	if strings.TrimSpace(res.Summary) == "" {
		return []ReviewItem{{Level: "fail", Message: "agent reviewer returned an empty result"}}
	}
	items := parseAgentReviewItems(res.Summary)
	if len(items) == 0 {
		return []ReviewItem{{Level: "fail", Message: "agent reviewer returned no structured findings"}}
	}
	return items
}

var reviewFindingTag = regexp.MustCompile(`(?i)\[(pass|warn|fail)\]`)
var malformedReviewTag = regexp.MustCompile(`(?i)(?:\[fai|\[(?:f|fa|w(?:a(?:r(?:n)?)?)?|p(?:a(?:s(?:s)?)?)?)(?:\s|\]|$))`)
var reviewLineTag = regexp.MustCompile(`(?im)^\s*(?:[-*]\s*)?\[([^\r\n]*)$`)

// parseAgentReviewItems tolerates providers that prepend a short progress
// sentence and then glue the requested first finding onto it without a
// newline ("...checked.[pass] ..."). The old line-only parser discarded that
// valid finding and downgraded every otherwise clean legacy review to warn.
func parseAgentReviewItems(summary string) []ReviewItem {
	rawMatches := reviewFindingTag.FindAllStringSubmatchIndex(summary, -1)
	matches := make([][]int, 0, len(rawMatches))
	invalidBoundary := false
	for _, match := range rawMatches {
		if reviewFindingBoundary(summary, match[0], match[1]) {
			matches = append(matches, match)
		} else {
			invalidBoundary = true
		}
	}
	items := make([]ReviewItem, 0, len(matches)+1)
	hasPass := false
	hasTaggedFail := false
	for i, match := range matches {
		messageEnd := len(summary)
		if i+1 < len(matches) {
			messageEnd = matches[i+1][0]
		}
		message := strings.TrimSpace(strings.TrimLeft(summary[match[1]:messageEnd], "-* "))
		// When the next tagged finding is a Markdown bullet, its leading "- "
		// belongs to that next finding but sits before the regex match.
		message = strings.TrimSpace(strings.TrimRight(message, "-*"))
		level := strings.ToLower(summary[match[2]:match[3]])
		if message == "" {
			items = append(items, ReviewItem{Level: "fail", Message: fmt.Sprintf("agent reviewer returned an empty [%s] finding", level)})
			hasTaggedFail = hasTaggedFail || level == "fail"
			continue
		}
		hasPass = hasPass || level == "pass"
		hasTaggedFail = hasTaggedFail || level == "fail"
		items = append(items, ReviewItem{
			Level:   level,
			Message: message,
		})
	}

	// Remove complete valid tags before looking for a partial spelling. This
	// catches provider truncation such as "[fai" and malformed variants such
	// as "[fail " without mistaking a valid [fail] marker for an error.
	withoutValidTags := reviewFindingTag.ReplaceAllString(summary, "")
	if malformedReviewTag.MatchString(withoutValidTags) {
		items = append(items, ReviewItem{Level: "fail", Message: "agent reviewer returned a truncated or malformed finding tag"})
	}
	if invalidBoundary {
		items = append(items, ReviewItem{Level: "fail", Message: "agent reviewer returned a finding tag outside a structured finding boundary"})
	}
	for _, match := range reviewLineTag.FindAllStringSubmatch(summary, -1) {
		line := strings.ToLower(strings.TrimSpace(match[1]))
		if strings.HasPrefix(line, "pass]") || strings.HasPrefix(line, "warn]") || strings.HasPrefix(line, "fail]") {
			continue
		}
		label := line
		if end := strings.IndexByte(label, ']'); end >= 0 {
			label = label[:end]
		}
		items = append(items, ReviewItem{Level: "fail", Message: fmt.Sprintf("agent reviewer returned malformed finding tag [%s]", label)})
	}
	if hasPass && hasTaggedFail {
		items = append(items, ReviewItem{Level: "fail", Message: "agent reviewer returned conflicting [pass] and [fail] findings"})
	}
	return items
}

func reviewFindingBoundary(summary string, start, end int) bool {
	lineStart := strings.LastIndexByte(summary[:start], '\n') + 1
	prefix := strings.TrimSpace(summary[lineStart:start])
	validPrefix := prefix == "" || prefix == "-" || prefix == "*"
	if !validPrefix {
		last := prefix[len(prefix)-1]
		validPrefix = last == '.' || last == '!' || last == '?' || last == ':'
	}
	if !validPrefix || end == len(summary) {
		return validPrefix
	}
	next := summary[end]
	return next == ' ' || next == '\t' || next == '\r' || next == '\n'
}

// gofmtCheck flags changed .go files that are not gofmt-clean.
func (o *Orchestrator) gofmtCheck(ctx context.Context) []ReviewItem {
	var items []ReviewItem
	gofmtPath, err := exec.LookPath("gofmt")
	if err != nil {
		return []ReviewItem{{Level: "fail", Message: fmt.Sprintf("gofmt unavailable: %v", err)}}
	}
	workspace := o.workspaceRoute()
	changes, err := workspace.git.AllChanges(ctx)
	if err != nil {
		return append(items, ReviewItem{Level: "fail", Message: fmt.Sprintf("gofmt cannot determine changed Go files: %v", err)})
	}
	var goFiles []string
	seen := make(map[string]bool, len(changes))
	for _, c := range changes {
		if c.Type != "D" && strings.HasSuffix(c.Path, ".go") && !seen[c.Path] {
			goFiles = append(goFiles, c.Path)
			seen[c.Path] = true
		}
	}
	if len(goFiles) == 0 {
		return items
	}
	// gofmt's -l output is newline-delimited and therefore ambiguous for a
	// legal filename containing a newline. Check each literal path separately
	// and report the already-known path rather than reparsing tool output.
	for _, file := range goFiles {
		cmd := exec.CommandContext(ctx, gofmtPath, "-l", "--", file)
		cmd.Dir = workspace.dir
		out, err := cmd.Output()
		if err != nil {
			items = append(items, ReviewItem{Level: "fail", Message: fmt.Sprintf("gofmt %s failed: %v", file, err)})
			continue
		}
		if len(out) != 0 {
			items = append(items, ReviewItem{Level: "fail", Message: fmt.Sprintf("gofmt: %s is not formatted", file)})
		}
	}
	return items
}

// vetCheck runs go vet in the working directory.
func (o *Orchestrator) vetCheck(ctx context.Context) []ReviewItem {
	cmd := exec.CommandContext(ctx, "go", "vet", "./...")
	cmd.Dir = o.workDir()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return []ReviewItem{{Level: "fail", Message: fmt.Sprintf("go vet: %s", strings.TrimSpace(shortOutput(out)))}}
	}
	return nil
}

// testCheck runs the test suite.
func (o *Orchestrator) testCheck(ctx context.Context) []ReviewItem {
	cmd := exec.CommandContext(ctx, "go", "test", "./...", "-count=1")
	cmd.Dir = o.workDir()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return []ReviewItem{{Level: "fail", Message: fmt.Sprintf("go test: %s", strings.TrimSpace(shortOutput(out)))}}
	}
	return []ReviewItem{{Level: "pass", Message: "go test ./... passes"}}
}

// securityItems runs the OWASP scan (F5) and renders findings as review
// items — the card surface is the review output itself.
func (o *Orchestrator) securityItems(ctx context.Context) []ReviewItem {
	findings := o.securityScan(ctx)
	var items []ReviewItem
	for _, f := range findings {
		level := "fail"
		if f.Severity == security.Low {
			level = "warn"
		}
		items = append(items, ReviewItem{Level: level, Message: f.String()})
		o.emit(agentcore.NewEvent(nil, agentcore.RoleReviewer, agentcore.EvHITL, agentcore.HITLItem{
			ID: "security:" + f.ID, Item: f.String(), Status: "pending",
		}))
	}
	return items
}

// taskAlignment reports task completion from tasks.md.
func (o *Orchestrator) taskAlignment(ctx context.Context) []ReviewItem {
	done, total := o.taskProgress(ctx)
	if total == 0 {
		return nil
	}
	msg := fmt.Sprintf("tasks.md: %d/%d complete", done, total)
	if done == total {
		return []ReviewItem{{Level: "pass", Message: msg}}
	}
	return []ReviewItem{{Level: "warn", Message: msg}}
}

func shortOutput(out []byte) string {
	s := string(out)
	if i := strings.IndexByte(s, '\n'); i > 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
