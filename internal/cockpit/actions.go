package cockpit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/bryann2k/maestro/internal/config"
	"github.com/bryann2k/maestro/internal/orchestrator"
	"github.com/bryann2k/maestro/internal/workflow"
)

type args struct {
	Change   string `json:"change"`
	Text     string `json:"text"`
	Revision string `json:"revision"`
	Phase    int    `json:"phase"`
	Provider string `json:"provider"`
	Key      string `json:"key"`
	URL      string `json:"url"`
	Model    string `json:"model"`
	ID       string `json:"id"`
	Path     string `json:"path"`
	Task     string `json:"task"`
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

func (h *Host) handle(ctx context.Context, r request) (any, error) {
	var a args
	if len(r.Args) > 0 {
		if err := json.Unmarshal(r.Args, &a); err != nil {
			return nil, err
		}
	}
	switch r.Op {
	case "open_link":
		return nil, openBrowser(ctx, a.URL)
	case "state":
		return h.state(ctx, a.Change)
	case "chat":
		text := strings.TrimSpace(a.Text)
		if text == "" || len(text) > 128<<10 {
			return nil, errors.New("enter a message up to 128 KiB")
		}
		if strings.HasPrefix(text, "/") {
			if h.Parse == nil {
				return nil, errors.New("command parser unavailable")
			}
			command, err := h.Parse(text)
			if err != nil {
				return nil, err
			}
			if command.Cmd == "auth" {
				return nil, errors.New("use Connections to enter credentials privately")
			}
			return nil, h.Orch.Dispatch(ctx, command)
		}
		return nil, h.Orch.Chat(ctx, text)
	case "create":
		if !workflow.Initialized(h.Orch.WorkDirDisplay()) {
			if _, err := workflow.Run(ctx, h.Orch.WorkDirDisplay(), []string{"bootstrap"}, nil); err != nil {
				return nil, err
			}
		}
		return h.engine(ctx, "explore", a.ID, "--title", a.Text)
	case "approve":
		if a.Revision == "" {
			return nil, errors.New("reload and inspect the contract before approval")
		}
		return h.engine(ctx, "approve", a.Change, "--by", "Maestro user", "--ack-user-approval", "--expected-digest", a.Revision)
	case "validate":
		return h.engine(ctx, "validate", a.Change)
	case "start":
		return h.engine(ctx, "start", a.Change)
	case "delegate":
		return nil, h.Orch.Dispatch(ctx, command("workflow", "delegate", a.Change, a.Task))
	case "contribution":
		return nil, h.Orch.Dispatch(ctx, command("workflow", "contribution", a.Change, a.Task, a.Decision, a.Reason))
	case "artifact":
		return h.Orch.WorkflowArtifact(ctx, a.Change, a.Phase)
	case "providers":
		return map[string]any{"providers": h.Orch.ProviderList(ctx), "accounts": h.Orch.SubscriptionList(ctx), "models": h.Orch.Models()}, nil
	case "model":
		return nil, h.Orch.SetActiveModel(ctx, a.Model)
	case "key":
		return nil, h.Orch.AuthAPIKey(ctx, a.Provider, a.Key)
	case "provider":
		return nil, h.Orch.ProviderAdd(ctx, config.Provider{Name: a.Provider, Type: "openai-compat", BaseURL: a.URL, APIKey: a.Key, DiscoverModels: true}, false)
	case "oauth":
		return nil, h.Orch.AuthOAuth(ctx, a.Provider)
	case "logout":
		return nil, h.Orch.AuthLogout(ctx, a.Provider)
	case "sessions":
		sessions, err := h.Orch.ListSessionSummaries(ctx)
		if err != nil {
			return nil, err
		}
		result := make([]map[string]any, 0, len(sessions))
		for _, s := range sessions {
			result = append(result, map[string]any{"id": s.ID, "title": s.DisplayTitle, "phase": s.Phase, "disabled": s.Disabled, "disabled_reason": s.DisabledReason})
		}
		return result, nil
	case "resume":
		return nil, h.Orch.LoadSession(ctx, a.ID)
	case "files":
		output, err := gitOutput(ctx, h.Orch.WorkDirDisplay(), "ls-files", "-z", "--cached", "--others", "--exclude-standard")
		if err != nil {
			return nil, err
		}
		if output == "" {
			return []string{}, nil
		}
		return strings.Split(strings.TrimSuffix(output, "\x00"), "\x00"), nil
	case "file":
		return readFile(h.Orch.WorkDirDisplay(), a.Path)
	case "diff":
		return gitOutput(ctx, h.Orch.WorkDirDisplay(), "diff", "--no-ext-diff", "--no-textconv", "HEAD", "--")
	}
	return nil, fmt.Errorf("unknown interface action %q", r.Op)
}
func (h *Host) engine(ctx context.Context, argv ...string) (any, error) {
	b, err := workflow.Run(ctx, h.Orch.WorkDirDisplay(), argv, nil)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}
func (h *Host) state(ctx context.Context, change string) (any, error) {
	sess := h.Orch.Session()
	state := map[string]any{"project": h.Orch.ProjectName(), "directory": h.Orch.WorkDirDisplay(), "session": sess.ID, "branch": strings.TrimPrefix(sess.WorkspaceRef, "refs/heads/"), "model": h.Orch.ActiveModel(), "conversation": sess.Conversation, "initialized": workflow.Initialized(h.Orch.WorkDirDisplay())}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	snap, err := h.Orch.WorkflowSnapshotFor(ctx, change)
	if err != nil {
		state["workflowError"] = err.Error()
	} else {
		state["workflow"] = json.RawMessage(snap)
		var selected struct {
			ChangeID string `json:"changeID"`
		}
		_ = json.Unmarshal(snap, &selected)
		if selected.ChangeID != "" {
			documents := map[string]string{}
			for name, phase := range map[string]int{"proposal": 1, "spec": 2, "plan": 3, "evidence": 4, "delivery": 5} {
				if text, e := h.Orch.WorkflowArtifact(ctx, selected.ChangeID, phase); e == nil {
					documents[name] = text
				} else if name == "spec" || name == "plan" {
					state["contractError"] = fmt.Sprintf("Cannot preview %s: %v", name, e)
				}
			}
			state["documents"] = documents
			if data, e := workflow.Run(ctx, h.Orch.WorkDirDisplay(), []string{"status", selected.ChangeID}, nil); e == nil {
				state["contract"] = json.RawMessage(data)
			} else {
				state["contractError"] = e.Error()
			}
		}
	}
	h.mu.Lock()
	h.cached = state
	h.mu.Unlock()
	return state, nil
}
func readFile(root, path string) (any, error) {
	if path == "" || filepath.IsAbs(path) {
		return nil, errors.New("choose a relative project file")
	}

	fs, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer fs.Close()
	f, err := openRootReadOnly(fs, path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, errors.New("choose a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, (256<<10)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 256<<10 || strings.ContainsRune(string(data), 0) {
		return nil, errors.New("preview supports text files up to 256 KiB")
	}
	sum := sha256.Sum256(data)
	return map[string]any{"path": path, "text": string(data), "revision": hex.EncodeToString(sum[:])}, nil
}

type boundedOutput struct{ strings.Builder }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 1<<20 {
		return 0, errors.New("output exceeds preview limit")
	}
	return b.Builder.Write(p)
}
func gitOutput(ctx context.Context, root string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	cmd.WaitDelay = time.Second
	var output boundedOutput
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	err := cmd.Run()
	return output.String(), err
}

func command(name string, args ...string) orchestrator.Command {
	return orchestrator.Command{Cmd: name, Args: args}
}
