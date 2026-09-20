// Package cockpit connects the OpenTUI frontend to Maestro's existing authority.
// The UI owns terminal rendering; the host owns tools, credentials and contracts.
package cockpit

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/orchestrator"
)

const maxFrame = 4 << 20

type request struct {
	ID    int64           `json:"id"`
	Op    string          `json:"op"`
	Args  json.RawMessage `json:"args"`
	Token string          `json:"token"`
}

type Host struct {
	Orch            *orchestrator.Orchestrator
	Parse           func(string) (orchestrator.Command, error)
	mu              sync.Mutex
	send            func(any) error
	cached          any
	pending         map[string]chan string
	readBuf         []byte
	ctx             context.Context
	cancel          context.CancelFunc
	operationCtx    context.Context
	inputAllowed    bool
	operationCancel context.CancelFunc
	busy            atomic.Bool
	wg              sync.WaitGroup
}

func New() *Host                                  { return &Host{pending: make(map[string]chan string)} }
func (h *Host) Bind(o *orchestrator.Orchestrator) { h.Orch = o; o.SetGate(h); o.SetAsk(h.Ask) }
func (h *Host) emit(kind string, data any) {
	h.mu.Lock()
	send := h.send
	h.mu.Unlock()
	if send != nil {
		_ = send(map[string]any{"event": kind, "data": data})
	}
}
func (h *Host) Write(p []byte) (int, error) {
	for at := 0; at < len(p); at += 64 << 10 {
		h.emit("output", string(p[at:min(at+(64<<10), len(p))]))
	}
	return len(p), nil
}
func (h *Host) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	h.mu.Lock()
	allowed := h.inputAllowed
	h.mu.Unlock()
	// os/exec also copies Stdin for noninteractive workflow commands. Only
	// OAuth may request a textual response; other readers receive EOF.
	if !allowed {
		return 0, io.EOF
	}
	if len(h.readBuf) == 0 {
		h.mu.Lock()
		ctx := h.operationCtx
		if ctx == nil {
			ctx = h.ctx
		}
		h.mu.Unlock()
		if ctx == nil {
			return 0, io.EOF
		}
		answer, err := h.prompt(ctx, "input", "Enter the requested value", nil, "")
		if err != nil {
			return 0, err
		}
		h.readBuf = []byte(answer + "\n")
	}
	n := copy(p, h.readBuf)
	h.readBuf = h.readBuf[n:]
	return n, nil
}
func (h *Host) prompt(ctx context.Context, kind, title string, choices []string, detail string) (string, error) {
	id := token()
	ch := make(chan string, 1)
	h.mu.Lock()
	h.pending[id] = ch
	h.mu.Unlock()
	defer func() { h.mu.Lock(); delete(h.pending, id); h.mu.Unlock(); h.emit("prompt_closed", id) }()
	h.emit("prompt", map[string]any{"id": id, "kind": kind, "title": title, "choices": choices, "detail": detail})
	select {
	case answer := <-ch:
		return answer, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
func (h *Host) Authorize(ctx context.Context, call agentcore.ToolCall, spec agentcore.ToolSpec) error {
	if !spec.NeedsApproval {
		return nil
	}
	answer, err := h.prompt(ctx, "permission", "Allow "+call.Name+"?", []string{"Deny", "Allow once"}, call.Args)
	if err != nil {
		return err
	}
	if answer != "1" {
		return errors.New("permission denied by user")
	}
	return nil
}
func (h *Host) Ask(ctx context.Context, question string, options []string, recommended int) (int, error) {
	answer, err := h.prompt(ctx, "choice", question, options, "")
	if err != nil {
		return -1, err
	}
	var n int
	if _, err = fmt.Sscan(answer, &n); err != nil || n < 0 || n >= len(options) {
		return -1, errors.New("invalid answer")
	}
	return n, nil
}
func token() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func Path() (string, error) {
	path := os.Getenv("MAESTRO_UI")
	if path != "" && !filepath.IsAbs(path) {
		return "", errors.New("MAESTRO_UI must be an absolute path")
	}
	if path == "" {
		binary, err := os.Executable()
		if err != nil {
			return "", err
		}
		name := "maestro-ui"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		path = filepath.Join(filepath.Dir(binary), name)
	}
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() {
		return "", errors.New("missing Maestro UI; run make build or install the complete bundle")
	}
	return path, nil
}

func (h *Host) Run(ctx context.Context, in, out, errOut *os.File) error {
	path, err := Path()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	h.mu.Lock()
	h.ctx = ctx
	h.cancel = cancel
	h.mu.Unlock()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	secret := token()
	cmd := exec.CommandContext(ctx, path)
	cmd.Stdin = in
	cmd.Stdout = out
	cmd.Stderr = errOut
	// Only terminal configuration crosses into the UI process. Provider keys
	// and user runtime configuration remain owned by the Go host.
	for _, key := range []string{"TERM", "COLORTERM", "TERM_PROGRAM", "LANG", "LC_ALL", "TZ", "NO_COLOR", "MAESTRO_COLOR", "MAESTRO_GLYPHS", "SYSTEMROOT", "WINDIR", "TMPDIR", "TEMP"} {
		if value, ok := os.LookupEnv(key); ok {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	cmd.Env = append(cmd.Env, "MAESTRO_UI_ADDRESS="+listener.Addr().String(), "MAESTRO_UI_TOKEN="+secret)
	cmd.WaitDelay = time.Second
	if err = cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait(); cancel(); listener.Close() }()
	defer func() { cancel(); h.wg.Wait() }()
	listener.(*net.TCPListener).SetDeadline(time.Now().Add(30 * time.Second))
	var conn net.Conn
	var scanner *bufio.Scanner
	for attempts := 0; attempts < 8; attempts++ {
		candidate, e := listener.Accept()
		if e != nil {
			return fmt.Errorf("UI connection: %w", e)
		}
		candidate.SetReadDeadline(time.Now().Add(5 * time.Second))
		scan := bufio.NewScanner(candidate)
		scan.Buffer(make([]byte, 4096), maxFrame)
		var hello request
		if scan.Scan() && json.Unmarshal(scan.Bytes(), &hello) == nil && hello.Op == "connect" && subtle.ConstantTimeCompare([]byte(hello.Token), []byte(secret)) == 1 {
			conn = candidate
			scanner = scan
			break
		}
		candidate.Close()
	}
	if conn == nil {
		return errors.New("UI authentication failed")
	}
	defer conn.Close()
	listener.Close()
	conn.SetReadDeadline(time.Time{})
	// Cancellation closes the private channel, releasing reads and permission waits.
	go func() { <-ctx.Done(); conn.Close() }()
	var writeMu sync.Mutex
	send := func(v any) error {
		data, e := json.Marshal(v)
		if e != nil {
			return e
		}
		if len(data) > maxFrame {
			return errors.New("UI response exceeds limit")
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
		_, e = conn.Write(append(data, '\n'))
		if e != nil {
			cancel()
		}
		return e
	}
	h.mu.Lock()
	h.send = send
	h.mu.Unlock()
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		for {
			select {
			case event, ok := <-h.Orch.Stream:
				if !ok {
					return
				}
				h.emit("stream", event)
			case <-ctx.Done():
				return
			}
		}
	}()
	var lastID int64
	for scanner.Scan() {
		var r request
		if json.Unmarshal(scanner.Bytes(), &r) != nil || r.ID <= lastID || len(r.Op) > 64 {
			cancel()
			return errors.New("invalid UI request")
		}
		lastID = r.ID
		reply := func(data any, e error) {
			m := map[string]any{"id": r.ID, "data": data}
			if e != nil {
				m["error"] = e.Error()
			}
			if err := send(m); err != nil {
				_ = send(map[string]any{"id": r.ID, "error": err.Error()})
			}
		}
		switch r.Op {
		case "open_link":
			// Login links must remain usable while OAuth is waiting for input.
			data, err := h.handle(ctx, r)
			reply(data, err)
		case "answer":
			var a struct{ ID, Value string }
			if e := json.Unmarshal(r.Args, &a); e != nil {
				reply(nil, e)
				continue
			}
			h.mu.Lock()
			ch := h.pending[a.ID]
			if ch != nil {
				delete(h.pending, a.ID)
			}
			h.mu.Unlock()
			if ch == nil {
				reply(nil, errors.New("prompt expired"))
			} else {
				ch <- a.Value
				reply(nil, nil)
			}
		case "cancel":
			h.mu.Lock()
			stop := h.operationCancel
			h.mu.Unlock()
			if stop != nil {
				stop()
			}
			h.Orch.CancelRun()
			reply(nil, nil)
		default:
			if !h.busy.CompareAndSwap(false, true) {
				if r.Op == "state" {
					h.mu.Lock()
					cached := h.cached
					h.mu.Unlock()
					reply(cached, nil)
				} else {
					reply(nil, errors.New("operation in progress; cancel or wait before another action"))
				}
				continue
			}
			opCtx, stop := context.WithCancel(ctx)
			h.mu.Lock()
			h.operationCancel = stop
			h.operationCtx = opCtx
			h.inputAllowed = r.Op == "oauth"
			h.mu.Unlock()
			h.wg.Add(1)
			go func(r request, reply func(any, error)) {
				defer h.wg.Done()
				if r.Op != "state" {
					h.emit("busy", true)
				}
				data, e := h.handle(opCtx, r)
				stop()
				h.mu.Lock()
				h.operationCancel = nil
				h.operationCtx = nil
				h.inputAllowed = false
				h.mu.Unlock()
				if r.Op != "state" {
					h.emit("busy", false)
				}
				h.busy.Store(false)
				reply(data, e)
			}(r, reply)
		}
	}
	// A normal UI quit closes the channel just before the child exits. Give
	// that exit time to arrive rather than racing it with CommandContext kill.
	select {
	case e := <-done:
		cancel()
		h.Orch.CancelRun()
		if e != nil {
			return fmt.Errorf("unexpected Maestro UI exit: %w", e)
		}
		if errors.Is(scanner.Err(), net.ErrClosed) {
			return nil
		}
		return scanner.Err()
	case <-time.After(time.Second):
		cancel()
		h.Orch.CancelRun()
		<-done
		return errors.New("unexpected Maestro UI disconnection without exiting")
	}
}
