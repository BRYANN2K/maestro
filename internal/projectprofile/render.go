package projectprofile

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxRenderedBytes    = 32 << 10
	maxRenderedUnits    = 128
	maxRenderedCommands = 128
	maxRenderedEvidence = 80
	maxRenderedUnknowns = 80
	maxPurposeBytes     = 4096
	contentHashZeros    = "0000000000000000000000000000000000000000000000000000000000000000"
)

// ManifestPath returns the canonical contract path without creating it.
func ManifestPath(root string) string { return filepath.Join(root, ManifestName) }

// Draft renders and conservatively reconciles a contract. It returns bytes
// for staging but never creates, replaces, or otherwise mutates a file.
func Draft(ctx context.Context, profile ProjectProfile, answers Answers) (string, []byte, error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	if answers.DiscoveryFingerprint != "" && answers.DiscoveryFingerprint != profile.DiscoveryFingerprint {
		return "", nil, &RepositoryChangedError{Mode: profile.Mode}
	}
	if err := Revalidate(ctx, profile); err != nil {
		return "", nil, err
	}
	content, err := Render(profile, answers)
	if err != nil {
		return "", nil, err
	}
	path := ManifestPath(profile.Root)
	if err := ReconcileExisting(ctx, profile.Root, content); err != nil {
		return path, nil, err
	}
	return path, content, nil
}

// Render emits one concise, byte-stable MAESTRO.md schema for both modes.
func Render(profile ProjectProfile, answers Answers) ([]byte, error) {
	normalizedProfile, normalizedAnswers, err := normalizeContract(profile, answers)
	if err != nil {
		return nil, err
	}
	fingerprint, err := contractFingerprint(normalizedProfile, normalizedAnswers)
	if err != nil {
		return nil, err
	}

	units := normalizedAnswers.Units
	commands := normalizedAnswers.Commands
	evidence := normalizedProfile.Evidence
	unknowns := append([]string(nil), normalizedProfile.Unknowns...)
	if len(units) > maxRenderedUnits {
		units = units[:maxRenderedUnits]
		unknowns = append(unknowns, "Additional project units were omitted at the manifest safety limit.")
	}
	if len(commands) > maxRenderedCommands {
		commands = commands[:maxRenderedCommands]
		unknowns = append(unknowns, "Additional project commands were omitted at the manifest safety limit.")
	}
	if len(evidence) > maxRenderedEvidence {
		evidence = evidence[:maxRenderedEvidence]
		unknowns = append(unknowns, "Additional evidence remains available in the in-memory project profile.")
	}
	unknowns = uniqueSorted(unknowns)
	if len(unknowns) > maxRenderedUnknowns {
		unknowns = append(unknowns[:maxRenderedUnknowns-1], "Additional unknowns were omitted at the manifest safety limit.")
	}

	var out strings.Builder
	out.WriteString("---\n")
	out.WriteString("maestro_schema: 1\n")
	fmt.Fprintf(&out, "mode: %s\n", yamlQuote(string(normalizedAnswers.Mode)))
	fmt.Fprintf(&out, "evidence_fingerprint: %s\n", yamlQuote("sha256:"+fingerprint))
	fmt.Fprintf(&out, "content_fingerprint: %s\n", yamlQuote("sha256:"+contentHashZeros))
	out.WriteString("project:\n")
	fmt.Fprintf(&out, "  name: %s\n", yamlQuote(normalizedAnswers.Name))
	out.WriteString("  root: \".\"\n")
	writeStringList(&out, "stacks", 0, normalizedAnswers.Stacks)

	if len(units) == 0 {
		out.WriteString("units: []\n")
	} else {
		out.WriteString("units:\n")
		for _, unit := range units {
			fmt.Fprintf(&out, "  - path: %s\n", yamlQuote(unit.Path))
			if unit.Name != "" {
				fmt.Fprintf(&out, "    name: %s\n", yamlQuote(unit.Name))
			}
			writeStringList(&out, "stacks", 4, unit.Stacks)
			writeStringList(&out, "manifests", 4, unit.Manifests)
			writeStringList(&out, "lockfiles", 4, unit.Lockfiles)
		}
	}

	if len(commands) == 0 {
		out.WriteString("commands: []\n")
	} else {
		out.WriteString("commands:\n")
		for _, command := range commands {
			fmt.Fprintf(&out, "  - name: %s\n", yamlQuote(command.Name))
			fmt.Fprintf(&out, "    run: %s\n", yamlQuote(command.Run))
			fmt.Fprintf(&out, "    cwd: %s\n", yamlQuote(command.Cwd))
			if command.Source != "" {
				fmt.Fprintf(&out, "    source: %s\n", yamlQuote(command.Source))
			}
			fmt.Fprintf(&out, "    confidence: %s\n", yamlQuote(string(command.Confidence)))
		}
	}

	if len(evidence) == 0 {
		out.WriteString("evidence: []\n")
	} else {
		out.WriteString("evidence:\n")
		for _, item := range evidence {
			fmt.Fprintf(&out, "  - kind: %s\n", yamlQuote(item.Kind))
			fmt.Fprintf(&out, "    value: %s\n", yamlQuote(item.Value))
			fmt.Fprintf(&out, "    source: %s\n", yamlQuote(item.Source))
			fmt.Fprintf(&out, "    confidence: %s\n", yamlQuote(string(item.Confidence)))
		}
	}
	writeStringList(&out, "unknowns", 0, unknowns)
	out.WriteString("---\n\n")
	out.WriteString("# Maestro Project Contract\n\n")
	out.WriteString("## Purpose\n\n")
	purpose := normalizedAnswers.Purpose
	if purpose == "" {
		purpose = "TBD — confirm the project's purpose before implementation."
	}
	out.WriteString(purpose)
	out.WriteString("\n\n## Non-goals\n\n")
	writeMarkdownList(&out, normalizedAnswers.NonGoals, "TBD — confirm the project's non-goals.")
	out.WriteString("\n## Architecture\n\n")
	if len(units) == 0 {
		out.WriteString("- No project units have been confirmed.\n")
	} else {
		for _, unit := range units {
			fmt.Fprintf(&out, "- path %s", yamlQuote(unit.Path))
			if unit.Name != "" {
				fmt.Fprintf(&out, ": %s", yamlQuote(unit.Name))
			}
			if len(unit.Stacks) > 0 {
				fmt.Fprintf(&out, " — stacks %s", strings.Join(quotedValues(unit.Stacks), ", "))
			}
			out.WriteString(".\n")
		}
	}
	out.WriteString("\n## Safety boundaries\n\n")
	writeMarkdownList(&out, normalizedAnswers.Safety, "TBD — confirm project safety boundaries.")
	out.WriteString("\n## Verification contract\n\n")
	writeMarkdownList(&out, normalizedAnswers.Verification, "TBD — confirm the required verification commands.")

	content := []byte(out.String())
	digest := sha256.Sum256(content)
	content = bytes.Replace(content, []byte(contentHashZeros), []byte(hex.EncodeToString(digest[:])), 1)
	if len(content) > maxRenderedBytes {
		return nil, fmt.Errorf("render MAESTRO.md: deterministic output is %d bytes, limit is %d", len(content), maxRenderedBytes)
	}
	return content, nil
}

// ReconcileExisting allows an exact no-op and otherwise fails closed. A
// future structured reconciler can expand this boundary without changing the
// staging API or ever silently overwriting human edits.
func ReconcileExisting(ctx context.Context, root string, draft []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	canonical, err := canonicalDirectory(root)
	if err != nil {
		return err
	}
	path := ManifestPath(canonical)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect existing MAESTRO.md: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > maxRenderedBytes {
		return &ConflictError{Path: path}
	}
	existing, err := readCandidate(canonical, ManifestName)
	if err != nil {
		return &ConflictError{Path: path}
	}
	if !bytes.Equal(existing, draft) && !isManagedManifest(existing) {
		return &ConflictError{Path: path}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

// isManagedManifest recognizes only the deterministic document shape emitted
// by this package. A managed contract may be proposed as a reviewed diff after
// repository drift; an arbitrary human MAESTRO.md remains fail-closed.
func isManagedManifest(content []byte) bool {
	if !utf8.Valid(content) || len(content) == 0 || len(content) > maxRenderedBytes {
		return false
	}
	text := string(content)
	for _, r := range text {
		if (unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t') || unicode.In(r, unicode.Cf) {
			return false
		}
	}
	if !strings.HasPrefix(text, "---\nmaestro_schema: 1\n") {
		return false
	}
	frontmatterOffset := strings.Index(text[4:], "\n---\n")
	if frontmatterOffset < 0 {
		return false
	}
	frontmatterEnd := 4 + frontmatterOffset + len("\n---\n")
	frontmatter := text[:frontmatterEnd]
	body := text[frontmatterEnd:]
	fingerprintPrefix := "evidence_fingerprint: \"sha256:"
	if strings.Count(frontmatter, fingerprintPrefix) != 1 {
		return false
	}
	start := strings.Index(frontmatter, fingerprintPrefix) + len(fingerprintPrefix)
	end := strings.IndexByte(frontmatter[start:], '\n')
	if end < 0 {
		return false
	}
	fingerprint := text[start : start+end]
	if len(fingerprint) != 65 || fingerprint[64] != '"' {
		return false
	}
	for _, r := range fingerprint[:64] {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	contentFingerprintPrefix := "content_fingerprint: \"sha256:"
	if strings.Count(frontmatter, contentFingerprintPrefix) != 1 {
		return false
	}
	contentStart := strings.Index(frontmatter, contentFingerprintPrefix) + len(contentFingerprintPrefix)
	contentEnd := strings.IndexByte(frontmatter[contentStart:], '\n')
	if contentEnd < 0 {
		return false
	}
	contentFingerprint := text[contentStart : contentStart+contentEnd]
	if len(contentFingerprint) != 65 || contentFingerprint[64] != '"' {
		return false
	}
	for _, r := range contentFingerprint[:64] {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	placeholder := append([]byte(nil), content...)
	actualLine := []byte(contentFingerprintPrefix + contentFingerprint)
	placeholderLine := []byte(contentFingerprintPrefix + contentHashZeros + `"`)
	placeholder = bytes.Replace(placeholder, actualLine, placeholderLine, 1)
	digest := sha256.Sum256(placeholder)
	if hex.EncodeToString(digest[:]) != contentFingerprint[:64] {
		return false
	}
	if strings.Count(frontmatter, "mode: \"greenfield\"\n")+strings.Count(frontmatter, "mode: \"brownfield\"\n") != 1 {
		return false
	}
	for _, section := range []string{
		"\n# Maestro Project Contract\n",
		"\n## Purpose\n",
		"\n## Non-goals\n",
		"\n## Architecture\n",
		"\n## Safety boundaries\n",
		"\n## Verification contract\n",
	} {
		if strings.Count(body, section) != 1 {
			return false
		}
	}
	return true
}

// ValidateManagedManifest verifies the bounded deterministic document shape
// before a staged project contract is accepted or used as model context.
func ValidateManagedManifest(content []byte) error {
	if !isManagedManifest(content) {
		return errors.New("MAESTRO.md is not a valid Maestro-managed project contract")
	}
	return nil
}

func normalizeContract(profile ProjectProfile, answers Answers) (ProjectProfile, Answers, error) {
	if profile.SchemaVersion != SchemaVersion {
		return ProjectProfile{}, Answers{}, fmt.Errorf("render MAESTRO.md: profile schema %d is unsupported", profile.SchemaVersion)
	}
	if profile.Mode != ModeGreenfield && profile.Mode != ModeBrownfield {
		return ProjectProfile{}, Answers{}, fmt.Errorf("render MAESTRO.md: profile mode %q is unsupported", profile.Mode)
	}
	if profile.Root == "" {
		return ProjectProfile{}, Answers{}, errors.New("render MAESTRO.md: profile root is required")
	}
	if answers.SchemaVersion != 0 && answers.SchemaVersion != SchemaVersion {
		return ProjectProfile{}, Answers{}, fmt.Errorf("render MAESTRO.md: answers schema %d is unsupported", answers.SchemaVersion)
	}
	if answers.Mode != "" && answers.Mode != profile.Mode {
		return ProjectProfile{}, Answers{}, fmt.Errorf("render MAESTRO.md: answers mode %q conflicts with profile mode %q", answers.Mode, profile.Mode)
	}

	copyProfile := profile
	copyProfile.Stacks = append([]string(nil), profile.Stacks...)
	copyProfile.Units = cloneUnits(profile.Units)
	copyProfile.Commands = append([]Command(nil), profile.Commands...)
	copyProfile.Evidence = append([]Evidence(nil), profile.Evidence...)
	copyProfile.Unknowns = append([]string(nil), profile.Unknowns...)
	copyProfile.normalize()

	out := answers
	out.SchemaVersion = SchemaVersion
	out.Mode = profile.Mode
	if out.Name == "" {
		out.Name = profile.Name
	}
	out.Name = safeFact(out.Name)
	if out.Name == "" {
		out.Name = filepath.Base(profile.Root)
	}
	if len(out.Stacks) == 0 {
		out.Stacks = append([]string(nil), copyProfile.Stacks...)
	}
	out.Stacks = normalizeFacts(out.Stacks)
	if len(out.Units) == 0 {
		out.Units = cloneUnits(copyProfile.Units)
	} else {
		out.Units = cloneUnits(out.Units)
	}
	if len(out.Units) == 0 && profile.Mode == ModeGreenfield {
		out.Units = []Unit{{Path: "."}}
	}
	for i := range out.Units {
		path, err := contractPath(out.Units[i].Path)
		if err != nil {
			return ProjectProfile{}, Answers{}, fmt.Errorf("render MAESTRO.md: unit path: %w", err)
		}
		out.Units[i].Path = path
		out.Units[i].Name = safeFact(out.Units[i].Name)
		out.Units[i].Stacks = normalizeFacts(out.Units[i].Stacks)
		out.Units[i].Manifests = normalizeFacts(out.Units[i].Manifests)
		out.Units[i].Lockfiles = normalizeFacts(out.Units[i].Lockfiles)
	}
	sort.Slice(out.Units, func(i, j int) bool { return out.Units[i].Path < out.Units[j].Path })
	if len(out.Commands) == 0 {
		out.Commands = append([]Command(nil), copyProfile.Commands...)
	} else {
		out.Commands = append([]Command(nil), out.Commands...)
	}
	for i := range out.Commands {
		out.Commands[i].Name = safeFact(out.Commands[i].Name)
		out.Commands[i].Run = safeFact(out.Commands[i].Run)
		cwd, err := contractPath(out.Commands[i].Cwd)
		if err != nil {
			return ProjectProfile{}, Answers{}, fmt.Errorf("render MAESTRO.md: command %q cwd: %w", out.Commands[i].Name, err)
		}
		out.Commands[i].Cwd = cwd
		out.Commands[i].Source = safeLocator(out.Commands[i].Source)
		if out.Commands[i].Confidence == "" {
			out.Commands[i].Confidence = ConfidenceConfirmed
		}
	}
	sort.Slice(out.Commands, func(i, j int) bool {
		a, b := out.Commands[i], out.Commands[j]
		return strings.Join([]string{a.Name, a.Cwd, a.Run, a.Source}, "\x00") < strings.Join([]string{b.Name, b.Cwd, b.Run, b.Source}, "\x00")
	})
	purpose, err := normalizePurpose(out.Purpose)
	if err != nil {
		return ProjectProfile{}, Answers{}, err
	}
	out.Purpose = purpose
	out.NonGoals = normalizeFacts(out.NonGoals)
	if len(out.Safety) == 0 {
		out.Safety = AnswersFromProfile(copyProfile).Safety
	}
	if len(out.Verification) == 0 {
		out.Verification = defaultVerification(out.Commands)
	}
	out.Safety = normalizeFacts(out.Safety)
	out.Verification = normalizeFacts(out.Verification)
	return copyProfile, out, nil
}

func contractFingerprint(profile ProjectProfile, answers Answers) (string, error) {
	// Runtime root paths are deliberately excluded so identical repositories
	// moved between machines render the same committed contract.
	view := struct {
		SchemaVersion int        `json:"schema_version"`
		Mode          Mode       `json:"mode"`
		Name          string     `json:"name"`
		Purpose       string     `json:"purpose"`
		NonGoals      []string   `json:"non_goals"`
		Stacks        []string   `json:"stacks"`
		Units         []Unit     `json:"units"`
		Commands      []Command  `json:"commands"`
		Safety        []string   `json:"safety"`
		Verification  []string   `json:"verification"`
		Evidence      []Evidence `json:"evidence"`
		Unknowns      []string   `json:"unknowns"`
	}{
		SchemaVersion: SchemaVersion,
		Mode:          answers.Mode,
		Name:          answers.Name,
		Purpose:       answers.Purpose,
		NonGoals:      answers.NonGoals,
		Stacks:        answers.Stacks,
		Units:         answers.Units,
		Commands:      answers.Commands,
		Safety:        answers.Safety,
		Verification:  answers.Verification,
		Evidence:      profile.Evidence,
		Unknowns:      profile.Unknowns,
	}
	data, err := json.Marshal(view)
	if err != nil {
		return "", fmt.Errorf("render MAESTRO.md fingerprint: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func writeStringList(out *strings.Builder, name string, indent int, values []string) {
	spaces := strings.Repeat(" ", indent)
	if len(values) == 0 {
		fmt.Fprintf(out, "%s%s: []\n", spaces, name)
		return
	}
	fmt.Fprintf(out, "%s%s:\n", spaces, name)
	for _, value := range values {
		fmt.Fprintf(out, "%s  - %s\n", spaces, yamlQuote(value))
	}
}

func writeMarkdownList(out *strings.Builder, values []string, fallback string) {
	if len(values) == 0 {
		fmt.Fprintf(out, "- %s\n", fallback)
		return
	}
	for _, value := range values {
		fmt.Fprintf(out, "- %s\n", value)
	}
}

func yamlQuote(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func quotedValues(values []string) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = yamlQuote(value)
	}
	return out
}

func normalizePurpose(value string) (string, error) {
	value = strings.TrimSpace(value)
	if strings.ContainsRune(value, 0) || !utf8.ValidString(value) {
		return "", errors.New("render MAESTRO.md: purpose contains invalid text")
	}
	if len(value) > maxPurposeBytes {
		return "", fmt.Errorf("render MAESTRO.md: purpose exceeds %d bytes", maxPurposeBytes)
	}
	return value, nil
}

func normalizeFacts(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if fact := safeFact(value); fact != "" {
			out = append(out, fact)
		}
	}
	return uniqueSorted(out)
}

func contractPath(value string) (string, error) {
	if value == "" || value == "." {
		return ".", nil
	}
	if strings.ContainsAny(value, "\x00\r\n\t") || filepath.IsAbs(value) {
		return "", fmt.Errorf("%q is not a safe repository-relative path", value)
	}
	clean := cleanUnitPath(value)
	if !safeRelativePath(clean) {
		return "", fmt.Errorf("%q is not a safe repository-relative path", value)
	}
	return clean, nil
}

func safeLocator(value string) string {
	if len(value) > 512 {
		value = value[:512]
	}
	for value != "" && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
