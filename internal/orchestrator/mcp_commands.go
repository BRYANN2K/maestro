package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const mcpUsage = "mcp: usage: /mcp list|status | /mcp tools [server|all] | /mcp reconnect <server|all>"

func (o *Orchestrator) dispatchMCP(ctx context.Context, cmd Command) error {
	if len(cmd.Args) == 0 {
		return errors.New(mcpUsage)
	}
	sub := strings.ToLower(strings.TrimSpace(cmd.Args[0]))
	switch sub {
	case "help":
		if len(cmd.Args) != 1 {
			return errors.New(mcpUsage)
		}
		fmt.Fprintln(o.out, mcpUsage)
		return nil
	case "list", "status":
		if len(cmd.Args) != 1 {
			return errors.New(mcpUsage)
		}
		summaries := o.MCPServerSummaries(ctx)
		if len(summaries) == 0 {
			fmt.Fprintln(o.out, "mcp: no servers configured")
			return nil
		}
		for _, summary := range summaries {
			detail := summary.Status
			if summary.Error != "" {
				detail += ": " + summary.Error
			}
			fmt.Fprintf(o.out, "%s · %s · %d tool(s) · %s\n",
				terminalSafeLine(summary.Name), terminalSafeLine(summary.Type), summary.ToolCount, terminalSafeLine(detail))
		}
		return nil
	case "tools":
		if len(cmd.Args) > 2 {
			return errors.New(mcpUsage)
		}
		server := "all"
		if len(cmd.Args) == 2 {
			server = strings.TrimSpace(cmd.Args[1])
			if server == "" {
				return errors.New(mcpUsage)
			}
		}
		tools := o.MCPToolSummaries(ctx, server)
		if len(tools) == 0 {
			fmt.Fprintln(o.out, "mcp: no connected tools")
			return nil
		}
		for _, tool := range tools {
			description := tool.Description
			if description == "" {
				description = "no description"
			}
			fmt.Fprintf(o.out, "%s · %s/%s · approval required · %s\n",
				terminalSafeLine(tool.Name), terminalSafeLine(tool.Server),
				terminalSafeLine(tool.RemoteName), terminalSafeLine(description))
		}
		return nil
	case "connect", "reconnect":
		if len(cmd.Args) != 2 {
			return errors.New(mcpUsage)
		}
		target := strings.TrimSpace(cmd.Args[1])
		if target == "" {
			return errors.New(mcpUsage)
		}
		if err := o.MCPReconnect(ctx, target); err != nil {
			return err
		}
		fmt.Fprintf(o.out, "mcp: reconnected %s\n", terminalSafeLine(target))
		return nil
	default:
		return errors.New(mcpUsage)
	}
}
