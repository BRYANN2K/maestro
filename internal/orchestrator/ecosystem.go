package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/bryann2k/maestro/internal/advisor"
	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/mcp"
	"github.com/bryann2k/maestro/internal/notify"
	"github.com/bryann2k/maestro/internal/skills"
)

// Ecosystem wires the B9 surface: advisor, skills, MCP, notifications, and
// session cost tracking.
type Ecosystem struct {
	Advisor   *advisor.Advisor
	SkillMgr  *skills.Manager
	MCP       *mcp.Registry
	Notify    *notify.Manager
	SkillPath []string // extra skill discovery roots from config

	mu         sync.Mutex
	sessionUSD float64
	toolCalls  int
	contextTok map[agentcore.Role]int // latest provider turn per role

	mcpMu      sync.Mutex
	mcpReady   bool
	mcpBusy    bool
	mcpClosed  bool
	mcpDone    chan struct{}
	mcpCancel  context.CancelFunc
	mcpTools   []agentcore.Tool
	mcpInfo    []MCPToolSummary
	mcpOmitted map[*mcp.Client]mcpCatalogOmission
}

// newEcosystem builds the B9 wiring (called from New).
func (o *Orchestrator) newEcosystem() {
	// Workspace/session switches replace the ecosystem. Tear down stdio MCP
	// children before dropping the old registry so no process survives with a
	// stale working directory.
	if o.eco != nil {
		closeEcosystemMCP(o.eco)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	eco := &Ecosystem{
		Advisor:    advisor.New(o.baseDir),
		MCP:        o.configuredMCPRegistry(o.workDir()),
		Notify:     notify.New(notify.ModeAuto),
		contextTok: make(map[agentcore.Role]int),
	}
	// Advisor rules: fired stream rules become convention patterns.
	if rules := o.guardrailSnapshot().Rules; rules != nil {
		for _, r := range rules.Rules() {
			if !r.Fired {
				eco.Advisor.Conventions = append(eco.Advisor.Conventions, r.Pattern)
			}
		}
	}
	// Skills from config option skill-path + defaults.
	var extra []string
	if o.cfg != nil {
		if p, ok := o.cfg.Options["skill-path"]; ok && p != "" {
			extra = append(extra, filepath.SplitList(p)...)
		}
	}
	eco.SkillPath = extra
	skillStateDir := os.Getenv("MAESTRO_SKILLS_DIR")
	if skillStateDir == "" && o.sessions != nil && o.sessions.Dir() != "" {
		skillStateDir = filepath.Join(filepath.Dir(o.sessions.Dir()), "skills")
	}
	eco.SkillMgr = skills.NewManager(skills.ManagerOptions{
		Home: home, ProjectDir: o.workDir(), ExtraPaths: extra,
		StateDir: skillStateDir, ProjectKey: o.sess.Project, SessionID: o.sess.ID,
	})
	// Advisor emits notes onto the stream.
	eco.Advisor.Emit = func(n advisor.Note) {
		o.emit(agentcore.NewEvent(nil, agentcore.RoleAdvisor, agentcore.EvAdvisorNote, agentcore.AdvisorNote{
			Level: string(n.Level), Note: n.Text,
		}))
	}
	o.eco = eco
}

func (o *Orchestrator) configuredMCPRegistry(workDir string) *mcp.Registry {
	registry := mcp.NewRegistry()
	if o.cfg == nil {
		return registry
	}
	for _, server := range o.cfg.Mcp {
		registry.Add(mcp.Server{
			Name: server.Name, Type: server.Type, URL: server.URL, Command: server.Command,
			Headers: append([]string(nil), server.Headers...), WorkDir: workDir,
		})
	}
	return registry
}

// closeEcosystemMCP waits for bounded discovery to publish, then tears down
// every transport before an ecosystem is replaced. This prevents an old
// stdio process from surviving a session/workspace switch.
func closeEcosystemMCP(eco *Ecosystem) {
	if eco == nil {
		return
	}
	for {
		eco.mcpMu.Lock()
		if !eco.mcpBusy {
			eco.mcpClosed = true
			if eco.MCP != nil {
				eco.MCP.CloseAll()
			}
			eco.mcpTools = nil
			eco.mcpInfo = nil
			eco.mcpOmitted = nil
			eco.mcpReady = false
			eco.mcpMu.Unlock()
			return
		}
		done, cancel := eco.mcpDone, eco.mcpCancel
		eco.mcpMu.Unlock()
		if cancel != nil {
			cancel()
		}
		if done == nil {
			continue
		}
		<-done
	}
}

// Close releases external integration transports. It is idempotent and must
// be called by every frontend when its orchestrator lifetime ends so an MCP
// stdio child cannot outlive a CLI, REPL, or TUI session.
func (o *Orchestrator) Close() error {
	if o == nil {
		return nil
	}
	o.closeOnce.Do(func() {
		// Mark closed before cancellation so no refresh can join the wait group
		// or publish after shutdown begins. Fetches run outside registryMu and
		// inherit this lifetime context, so cancellation reaches the HTTP request.
		o.registryMu.Lock()
		o.closed = true
		o.modelRefreshGen++
		cancelRefresh := o.modelRefreshCancel
		o.registryMu.Unlock()
		if cancelRefresh != nil {
			cancelRefresh()
		}
		o.modelRefreshWG.Wait()

		o.registryMu.Lock()
		if o.modelsDev != nil {
			o.modelsDev.Close()
		}
		if o.registry != nil {
			o.closeErr = o.registry.Close()
		}
		o.registryMu.Unlock()

		if o.eco != nil {
			closeEcosystemMCP(o.eco)
		}
	})
	return o.closeErr
}

// retargetMCPWorkspace is the workspace lifecycle boundary for MCP. It closes
// the old registry before publishing clients rooted in workDir and clears the
// native-loop catalog atomically under mcpMu. Disconnected MCP remains cheap:
// processes are only started later by explicit reconnect or a native run.
func (o *Orchestrator) retargetMCPWorkspace(workDir string) {
	eco := o.eco
	if eco == nil {
		return
	}
	next := o.configuredMCPRegistry(workDir)
	for {
		eco.mcpMu.Lock()
		if !eco.mcpBusy {
			if eco.mcpClosed {
				next.CloseAll()
				eco.mcpMu.Unlock()
				return
			}
			if eco.MCP != nil {
				eco.MCP.CloseAll()
			}
			eco.MCP = next
			eco.mcpTools = nil
			eco.mcpInfo = nil
			eco.mcpOmitted = nil
			eco.mcpReady = false
			eco.mcpMu.Unlock()
			return
		}
		done, cancel := eco.mcpDone, eco.mcpCancel
		eco.mcpMu.Unlock()
		if cancel != nil {
			cancel()
		}
		if done == nil {
			continue
		}
		<-done
	}
}

// AdvisorNotes returns the advisor for tests.
func (o *Orchestrator) AdvisorNotes() *advisor.Advisor {
	if o.eco == nil {
		return nil
	}
	return o.eco.Advisor
}

// MCPServers returns the MCP clients for the sidebar dots.
func (o *Orchestrator) MCPServers() []*mcp.Client {
	if o.eco == nil {
		return nil
	}
	o.eco.mcpMu.Lock()
	defer o.eco.mcpMu.Unlock()
	return o.eco.MCP.Clients()
}

// MCPConnect connects every configured server. It remains best effort for
// compatibility: one unavailable integration never blocks Maestro itself.
func (o *Orchestrator) MCPConnect(ctx context.Context) {
	if o.eco == nil {
		return
	}
	_ = o.connectMCP(ctx)
}

var invalidMCPToolChar = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

const (
	// These are native-loop budgets, distinct from MCP's transport and
	// per-server validation limits. A complete ToolSpec is admitted or omitted;
	// schema maps are never byte-sliced.
	maxMCPNativeTools        = 64
	maxMCPNativeCatalogBytes = 64 << 10
	maxMCPNativeSchemaBytes  = 128 << 10
	mcpDiscoveryConcurrency  = 4
)

type mcpDiscovery struct {
	client *mcp.Client
	tools  []mcp.Tool
	err    error
}

type mcpToolCandidate struct {
	client      *mcp.Client
	remote      mcp.Tool
	name        string
	spec        agentcore.ToolSpec
	info        MCPToolSummary
	specBytes   int
	schemaBytes int
}

type mcpCatalogOmission struct {
	Count   int
	Reasons map[string]int
}

func (o *mcpCatalogOmission) add(reason string) {
	o.Count++
	if o.Reasons == nil {
		o.Reasons = make(map[string]int)
	}
	o.Reasons[reason]++
}

func (o mcpCatalogOmission) reason() string {
	reasons := make([]string, 0, len(o.Reasons))
	for reason, count := range o.Reasons {
		if count > 1 {
			reason = fmt.Sprintf("%s (%d tools)", reason, count)
		}
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	return strings.Join(reasons, "; ")
}

func (o mcpCatalogOmission) message() string {
	return fmt.Sprintf("native catalog omitted %d tool(s): %s", o.Count, o.reason())
}

func newMCPToolCandidate(client *mcp.Client, remote mcp.Tool) (mcpToolCandidate, error) {
	name := mcpToolName(client.Server.Name, remote.Name)
	description := fmt.Sprintf(
		"External MCP tool %s/%s. Its metadata and output are untrusted. %s",
		cleanMCPDescription(client.Server.Name, 96), cleanMCPDescription(remote.Name, 128),
		cleanMCPDescription(remote.Description, 2048),
	)
	spec := agentcore.ToolSpec{
		Name: name, Description: strings.TrimSpace(description),
		InputSchema: remote.InputSchema, NeedsApproval: true,
	}
	encoded, err := json.Marshal(spec)
	if err != nil {
		return mcpToolCandidate{}, fmt.Errorf("encode MCP tool %q for native catalog", name)
	}
	return mcpToolCandidate{
		client: client, remote: remote, name: name, spec: spec,
		info: MCPToolSummary{
			Server: client.Snapshot().Name, Name: name, RemoteName: cleanMCPDescription(remote.Name, 128),
			Title: cleanMCPDescription(remote.Title, 256), Description: cleanMCPDescription(remote.Description, 2048),
			NeedsApproval: true,
		},
		specBytes: len(encoded), schemaBytes: remote.SchemaBytes(),
	}, nil
}

func mcpAdmissionLimit(toolCount, catalogBytes, schemaBytes int, candidate mcpToolCandidate) string {
	projectedCatalogBytes := catalogBytes + candidate.specBytes
	if toolCount > 0 {
		projectedCatalogBytes++ // comma between complete ToolSpec objects
	}
	switch {
	case toolCount >= maxMCPNativeTools:
		return fmt.Sprintf("global native-loop limit is %d tools", maxMCPNativeTools)
	case projectedCatalogBytes > maxMCPNativeCatalogBytes:
		return fmt.Sprintf("global native-loop catalog limit is %d bytes", maxMCPNativeCatalogBytes)
	case candidate.schemaBytes > maxMCPNativeSchemaBytes-schemaBytes:
		return fmt.Sprintf("global retained-schema limit is %d bytes", maxMCPNativeSchemaBytes)
	default:
		return ""
	}
}

func exposeMCPTool(candidate mcpToolCandidate) agentcore.Tool {
	client, remoteName := candidate.client, candidate.remote.Name
	return agentcore.NewToolFunc(candidate.spec, func(callCtx context.Context, args map[string]any) (string, error) {
		result, err := client.CallTool(callCtx, remoteName, args)
		if err != nil {
			return "", err
		}
		return result.ModelOutput()
	})
}

func (o *Orchestrator) connectMCP(ctx context.Context) error {
	eco := o.eco
	if eco == nil {
		return nil
	}
	eco.mcpMu.Lock()
	if eco.mcpClosed {
		eco.mcpMu.Unlock()
		return errors.New("MCP ecosystem is closed")
	}
	if eco.mcpReady {
		eco.mcpMu.Unlock()
		return nil
	}
	if eco.mcpBusy {
		// Discovery is intentionally non-blocking for concurrent native runs.
		// The run already doing discovery will publish the catalog atomically.
		eco.mcpMu.Unlock()
		return nil
	}
	eco.mcpBusy = true
	discoveryDone := make(chan struct{})
	eco.mcpDone = discoveryDone
	discoveryCtx, cancelDiscovery := context.WithCancel(ctx)
	eco.mcpCancel = cancelDiscovery
	eco.mcpMu.Unlock()
	defer cancelDiscovery()
	defer func() {
		eco.mcpMu.Lock()
		if eco.mcpDone == discoveryDone {
			eco.mcpBusy = false
			eco.mcpCancel = nil
			close(discoveryDone)
			eco.mcpDone = nil
		}
		eco.mcpMu.Unlock()
	}()

	clients := eco.MCP.Clients()
	discovered := make([]mcpDiscovery, len(clients))
	jobs := make(chan int, len(clients))
	for i := range clients {
		jobs <- i
	}
	close(jobs)
	workers := mcpDiscoveryConcurrency
	if len(clients) < workers {
		workers = len(clients)
	}
	var connectWG sync.WaitGroup
	connectWG.Add(workers)
	for range workers {
		go func() {
			defer connectWG.Done()
			for index := range jobs {
				client := clients[index]
				result := mcpDiscovery{client: client}
				if err := discoveryCtx.Err(); err != nil {
					result.err = err
				} else if client.Snapshot().Status != "connected" {
					result.err = client.Connect(discoveryCtx)
				}
				discovered[index] = result
			}
		}()
	}
	connectWG.Wait()
	if err := discoveryCtx.Err(); err != nil {
		return err
	}
	sort.Slice(discovered, func(i, j int) bool {
		return discovered[i].client.Server.Name < discovered[j].client.Server.Name
	})

	// Connections initialize concurrently. Tool catalogs are then fetched in
	// sorted batches and admitted only after the whole batch completes. This
	// preserves stable server/name admission while bounding discovery latency
	// and peak schemas to the retained global set plus four server catalogs.
	var failures []error
	if err := eco.MCP.ConfigurationError(); err != nil {
		failures = append(failures, err)
	}
	var published []mcpToolCandidate
	nameOwner := make(map[string]*mcp.Client)
	omissions := make(map[*mcp.Client]mcpCatalogOmission)
	toolCount, catalogBytes, schemaBytes := 0, 2, 0 // [] around the ToolSpec catalog
	for batchStart := 0; batchStart < len(discovered); batchStart += mcpDiscoveryConcurrency {
		batchEnd := batchStart + mcpDiscoveryConcurrency
		if batchEnd > len(discovered) {
			batchEnd = len(discovered)
		}
		batch := append([]mcpDiscovery(nil), discovered[batchStart:batchEnd]...)
		var batchWG sync.WaitGroup
		for i := range batch {
			if batch[i].err != nil {
				continue
			}
			batchWG.Add(1)
			go func(index int) {
				defer batchWG.Done()
				serverTools, err := batch[index].client.ListTools(discoveryCtx)
				if err != nil && discoveryCtx.Err() == nil {
					batch[index].client.Fail(err)
				}
				batch[index].tools, batch[index].err = serverTools, err
			}(i)
		}
		batchWG.Wait()
		if err := discoveryCtx.Err(); err != nil {
			return err
		}

		for i := range batch {
			server := &batch[i]
			if server.err != nil {
				failures = append(failures, fmt.Errorf("mcp %s: %w", server.client.Snapshot().Name, server.err))
				continue
			}
			serverTools := server.tools
			server.tools = nil

			candidates := make([]mcpToolCandidate, 0, len(serverTools))
			localNames := make(map[string]bool, len(serverTools))
			localCollisions := make(map[string]bool)
			candidateFailed := false
			for _, remote := range serverTools {
				candidate, candidateErr := newMCPToolCandidate(server.client, remote)
				if candidateErr != nil {
					server.client.Fail(candidateErr)
					failures = append(failures, fmt.Errorf("mcp %s: %w", server.client.Snapshot().Name, candidateErr))
					candidateFailed = true
					break
				}
				if localNames[candidate.name] {
					localCollisions[candidate.name] = true
				}
				localNames[candidate.name] = true
				candidates = append(candidates, candidate)
			}
			sort.Slice(candidates, func(i, j int) bool {
				if candidates[i].name != candidates[j].name {
					return candidates[i].name < candidates[j].name
				}
				return candidates[i].remote.Name < candidates[j].remote.Name
			})
			if candidateFailed {
				continue
			}
			if len(localCollisions) > 0 {
				names := make([]string, 0, len(localCollisions))
				for name := range localCollisions {
					names = append(names, name)
				}
				sort.Strings(names)
				for _, name := range names {
					failures = append(failures, fmt.Errorf("MCP tool name collision after sanitization: %q", name))
				}
				server.client.Fail(errors.New("MCP tool names collide after sanitization"))
				continue
			}

			provisional := make([]mcpToolCandidate, 0, len(candidates))
			serverOmission := mcpCatalogOmission{}
			nextToolCount, nextCatalogBytes, nextSchemaBytes := toolCount, catalogBytes, schemaBytes
			for _, candidate := range candidates {
				if reason := mcpAdmissionLimit(nextToolCount, nextCatalogBytes, nextSchemaBytes, candidate); reason != "" {
					serverOmission.add(reason)
					continue
				}
				provisional = append(provisional, candidate)
				if nextToolCount > 0 {
					nextCatalogBytes++
				}
				nextToolCount++
				nextCatalogBytes += candidate.specBytes
				nextSchemaBytes += candidate.schemaBytes
			}

			collisionNames := make(map[string]bool)
			collisionClients := map[*mcp.Client]bool{server.client: true}
			for _, candidate := range provisional {
				if owner := nameOwner[candidate.name]; owner != nil && owner != server.client {
					collisionNames[candidate.name] = true
					collisionClients[owner] = true
				}
			}
			if len(collisionNames) > 0 {
				names := make([]string, 0, len(collisionNames))
				for name := range collisionNames {
					names = append(names, name)
				}
				sort.Strings(names)
				for _, name := range names {
					failures = append(failures, fmt.Errorf("MCP tool name collision after sanitization: %q", name))
				}
				invalid := make([]*mcp.Client, 0, len(collisionClients))
				for client := range collisionClients {
					invalid = append(invalid, client)
				}
				sort.Slice(invalid, func(i, j int) bool { return invalid[i].Server.Name < invalid[j].Server.Name })
				for _, client := range invalid {
					client.Fail(errors.New("MCP tool names collide after sanitization"))
					delete(omissions, client)
				}
				oldPublishedLen := len(published)
				kept := published[:0]
				for _, candidate := range published {
					if collisionClients[candidate.client] {
						delete(nameOwner, candidate.name)
						continue
					}
					kept = append(kept, candidate)
				}
				clear(published[len(kept):oldPublishedLen])
				published = kept
				toolCount, catalogBytes, schemaBytes = len(published), 2, 0
				for i, candidate := range published {
					if i > 0 {
						catalogBytes++
					}
					catalogBytes += candidate.specBytes
					schemaBytes += candidate.schemaBytes
				}
				continue
			}

			acceptedNames := make([]string, 0, len(provisional))
			for _, candidate := range provisional {
				acceptedNames = append(acceptedNames, candidate.remote.Name)
				nameOwner[candidate.name] = server.client
			}
			server.client.RetainDiscoveredTools(acceptedNames)
			published = append(published, provisional...)
			toolCount, catalogBytes, schemaBytes = nextToolCount, nextCatalogBytes, nextSchemaBytes
			if serverOmission.Count > 0 {
				omissions[server.client] = serverOmission
				failures = append(failures, fmt.Errorf("mcp %s: %s", server.client.Snapshot().Name, serverOmission.message()))
			}
		}
	}

	exposed := make([]agentcore.Tool, 0, len(published))
	toolInfo := make([]MCPToolSummary, 0, len(published))
	for _, candidate := range published {
		exposed = append(exposed, exposeMCPTool(candidate))
		toolInfo = append(toolInfo, candidate.info)
	}

	eco.mcpMu.Lock()
	if err := discoveryCtx.Err(); err != nil {
		eco.mcpMu.Unlock()
		return err
	}
	if eco.mcpClosed || eco.mcpDone != discoveryDone {
		eco.mcpMu.Unlock()
		return errors.New("MCP discovery became stale before publication")
	}
	eco.mcpTools = exposed
	eco.mcpInfo = toolInfo
	eco.mcpOmitted = omissions
	eco.mcpReady = true
	eco.mcpMu.Unlock()
	return errorsJoin(failures...)
}

func (o *Orchestrator) connectedMCPTools() []agentcore.Tool {
	if o.eco == nil {
		return nil
	}
	o.eco.mcpMu.Lock()
	defer o.eco.mcpMu.Unlock()
	return append([]agentcore.Tool(nil), o.eco.mcpTools...)
}

func mcpToolName(server, tool string) string {
	serverPart := mcpToolPart(server, 20)
	toolPart := mcpToolPart(tool, 28)
	return "mcp__" + serverPart + "__" + toolPart
}

func mcpToolPart(value string, limit int) string {
	part := invalidMCPToolChar.ReplaceAllString(value, "_")
	part = strings.Trim(part, "_-")
	if part == "" {
		part = "unnamed"
	}
	if len(part) > limit {
		part = part[:limit]
	}
	return part
}

func cleanMCPDescription(value string, limit int) string {
	var b strings.Builder
	for _, r := range strings.ToValidUTF8(value, "�") {
		if r == '\n' || r == '\t' || !unicode.IsControl(r) && !unicode.In(r, unicode.Cf) {
			b.WriteRune(r)
		}
		if b.Len() >= limit {
			break
		}
	}
	return strings.TrimSpace(b.String())
}

// MCPServerSummary is the stable, read-only shape consumed by frontends.
// It contains no URL, command, headers, token, session ID, or tool metadata.
type MCPServerSummary struct {
	Name           string
	Type           string
	Status         string
	Error          string
	ToolCount      int
	OmittedTools   int
	OmissionReason string
}

// MCPToolSummary is a terminal-safe catalog row. Schemas and annotations are
// deliberately absent: schemas remain provider/runtime data, and annotations
// are untrusted hints that never affect permissions.
type MCPToolSummary struct {
	Server        string
	Name          string
	RemoteName    string
	Title         string
	Description   string
	NeedsApproval bool
}

// MCPServerSummaries returns deterministic, race-safe MCP status rows.
func (o *Orchestrator) MCPServerSummaries(_ context.Context) []MCPServerSummary {
	if o.eco == nil {
		return nil
	}
	o.eco.mcpMu.Lock()
	defer o.eco.mcpMu.Unlock()
	if o.eco.MCP == nil {
		return nil
	}
	clients := o.eco.MCP.Clients()
	out := make([]MCPServerSummary, 0, len(clients))
	for _, client := range clients {
		snapshot := client.Snapshot()
		omission := o.eco.mcpOmitted[client]
		detail := snapshot.Error
		if omission.Count > 0 {
			if detail != "" {
				detail += "; "
			}
			detail += omission.message()
		}
		out = append(out, MCPServerSummary{
			Name: snapshot.Name, Type: snapshot.Type, Status: snapshot.Status,
			Error: detail, ToolCount: snapshot.ToolCount,
			OmittedTools: omission.Count, OmissionReason: omission.reason(),
		})
	}
	return out
}

// MCPToolSummaries returns the currently exposed native-loop MCP tools. Pass
// an empty server name or "all" for the complete deterministic catalog.
func (o *Orchestrator) MCPToolSummaries(_ context.Context, server string) []MCPToolSummary {
	if o.eco == nil {
		return nil
	}
	server = strings.TrimSpace(server)
	o.eco.mcpMu.Lock()
	defer o.eco.mcpMu.Unlock()
	out := make([]MCPToolSummary, 0, len(o.eco.mcpInfo))
	for _, item := range o.eco.mcpInfo {
		if server == "" || server == "all" || item.Server == server {
			out = append(out, item)
		}
	}
	return out
}

// MCPReconnect reconnects one server, or all servers when name is empty or
// "all", then atomically rebuilds the native-agent tool catalog.
func (o *Orchestrator) MCPReconnect(ctx context.Context, name string) error {
	eco := o.eco
	if eco == nil {
		return errors.New("MCP ecosystem is unavailable")
	}
	name = strings.TrimSpace(name)
	for {
		eco.mcpMu.Lock()
		if !eco.mcpBusy {
			if eco.MCP == nil {
				eco.mcpMu.Unlock()
				return errors.New("MCP ecosystem is unavailable")
			}
			if name == "" || name == "all" {
				eco.MCP.CloseAll()
			} else {
				client, ok := eco.MCP.Get(name)
				if !ok {
					eco.mcpMu.Unlock()
					return fmt.Errorf("MCP server %q is not configured", cleanMCPDescription(name, 128))
				}
				_ = client.Close()
			}
			eco.mcpReady = false
			eco.mcpTools = nil
			eco.mcpInfo = nil
			eco.mcpOmitted = nil
			eco.mcpMu.Unlock()
			connectErr := o.connectMCP(ctx)
			if name == "" || name == "all" {
				return connectErr
			}
			// A named reconnect is independent: another offline integration must
			// not turn a successfully refreshed target into a command failure.
			eco.mcpMu.Lock()
			client, ok := eco.MCP.Get(name)
			eco.mcpMu.Unlock()
			if !ok {
				return fmt.Errorf("MCP server %q is not configured", cleanMCPDescription(name, 128))
			}
			snapshot := client.Snapshot()
			if snapshot.Status == "connected" {
				return nil
			}
			detail := snapshot.Error
			if detail == "" {
				detail = snapshot.Status
			}
			return fmt.Errorf("MCP server %q: %s", cleanMCPDescription(name, 128), cleanMCPDescription(detail, 512))
		}
		done := eco.mcpDone
		eco.mcpMu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
		}
	}
}

func errorsJoin(errs ...error) error {
	var nonNil []error
	for _, err := range errs {
		if err != nil {
			nonNil = append(nonNil, err)
		}
	}
	if len(nonNil) == 0 {
		return nil
	}
	return errors.Join(nonNil...)
}

// Notifier returns the notification manager.
func (o *Orchestrator) Notifier() *notify.Manager {
	if o.eco == nil {
		return notify.New(notify.ModeDisabled)
	}
	return o.eco.Notify
}

// SessionCost returns the accumulated session spend.
func (o *Orchestrator) SessionCost() float64 {
	if o.eco == nil {
		return 0
	}
	o.eco.mu.Lock()
	defer o.eco.mu.Unlock()
	return o.eco.sessionUSD
}

// SessionToolCalls returns the session tool call count.
func (o *Orchestrator) SessionToolCalls() int {
	if o.eco == nil {
		return 0
	}
	o.eco.mu.Lock()
	defer o.eco.mu.Unlock()
	return o.eco.toolCalls
}

// EmitCost is a test hook: account a synthetic cost (demos, tests).
func (o *Orchestrator) EmitCost(usd float64, tools int) {
	if o.eco == nil {
		return
	}
	o.eco.mu.Lock()
	o.eco.sessionUSD += usd
	o.eco.toolCalls += tools
	o.eco.mu.Unlock()
}

// accountSession records provider usage independently from event visibility.
// Private structured runs are intentionally absent from the transcript, but
// their tokens, cost, and tool calls still consume the user's session budget.
func (o *Orchestrator) accountSession(ev agentcore.StreamEvent) {
	if o.eco == nil {
		return
	}
	switch ev.Type {
	case agentcore.EvDone:
		if d, ok := ev.Content.(agentcore.Done); ok {
			o.eco.mu.Lock()
			if d.Cost != nil {
				o.eco.sessionUSD += d.Cost.Total()
			}
			if d.Usage != nil {
				// A context window is per request, not a lifetime session budget.
				// Keep the latest turn per role: a private Docs/Reviewer call must
				// not be compared with the orchestrator model shown in the TUI.
				if o.eco.contextTok == nil {
					o.eco.contextTok = make(map[agentcore.Role]int)
				}
				o.eco.contextTok[ev.Role] = d.Usage.InputTokens + d.Usage.OutputTokens +
					d.Usage.CacheCreateTokens + d.Usage.CacheHitTokens
			}
			o.eco.mu.Unlock()
		}
	case agentcore.EvToolResult:
		if _, ok := ev.Content.(agentcore.ToolResult); ok {
			o.eco.mu.Lock()
			o.eco.toolCalls++
			o.eco.mu.Unlock()
		}
	}
}

// trackSession accounts a public event and feeds visible tool outcomes to the
// advisor. Silent machine protocols use accountSession directly.
func (o *Orchestrator) trackSession(ev agentcore.StreamEvent) {
	o.accountSession(ev)
	if o.eco == nil || o.eco.Advisor == nil {
		return
	}
	if ev.Type == agentcore.EvToolResult {
		if tr, ok := ev.Content.(agentcore.ToolResult); ok {
			o.eco.Advisor.Observe(context.Background(), "tool_result", tr.Name, tr.Output, string(ev.Role))
		}
	}
}
