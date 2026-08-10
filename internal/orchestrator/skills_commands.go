package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bryann2k/maestro/internal/skills"
)

func (o *Orchestrator) dispatchSkills(ctx context.Context, cmd Command) error {
	if len(cmd.Args) == 0 {
		return errors.New("skills: usage: /skills list|show|enable|disable|run")
	}
	sub := strings.ToLower(strings.TrimSpace(cmd.Args[0]))
	switch sub {
	case "list":
		if len(cmd.Args) != 1 {
			return errors.New("skills: usage: /skills list")
		}
		summaries := o.SkillSummaries(ctx)
		if len(summaries) == 0 {
			fmt.Fprintln(o.out, "skills: none discovered")
			return nil
		}
		for _, summary := range summaries {
			status := "enabled"
			if !summary.Enabled {
				status = "disabled"
			}
			if summary.Error != "" {
				status = "invalid: " + summary.Error
			} else if !summary.UserInvokable {
				status += ", not user-invokable"
			}
			if summary.Warning != "" {
				status += ", warning: " + summary.Warning
			}
			fmt.Fprintf(o.out, "%s · %s · %s · %s · %s\n",
				terminalSafeLine(summary.ID), terminalSafeLine(status),
				terminalSafeLine(string(summary.Scope)), terminalSafeLine(summary.Source),
				terminalSafeLine(summary.Description))
		}
		return nil
	case "show":
		ref, err := skillCommandRef(cmd)
		if err != nil {
			return errors.New("skills: usage: /skills show <id|unique-name>")
		}
		inspection, err := o.SkillInspect(ctx, ref)
		if err != nil {
			return err
		}
		fmt.Fprintf(o.out, "skill: %s\n", terminalSafeLine(inspection.ID))
		fmt.Fprintf(o.out, "name: %s\n", terminalSafeLine(inspection.Name))
		fmt.Fprintf(o.out, "source: %s · %s\n", terminalSafeLine(inspection.Source), terminalSafeLine(string(inspection.Scope)))
		fmt.Fprintf(o.out, "status: %s\n", skillStatus(inspection.SkillSummary))
		fmt.Fprintf(o.out, "path: %s\n\n", terminalSafeLine(inspection.Path))
		fmt.Fprintln(o.out, terminalSafeMultiline(inspection.Content))
		return nil
	case "enable", "disable":
		ref, err := skillCommandRef(cmd)
		if err != nil {
			return fmt.Errorf("skills: usage: /skills %s <id|unique-name> [--scope=project|session]", sub)
		}
		scope := strings.ToLower(strings.TrimSpace(flag(cmd, "scope")))
		enabled := sub == "enable"
		switch scope {
		case "", string(skills.EnableProject):
			err = o.SetSkillEnabled(ctx, ref, enabled)
			scope = string(skills.EnableProject)
		case string(skills.EnableSession):
			err = o.SetSessionSkillEnabled(ctx, ref, enabled)
		default:
			return errors.New("skills: --scope must be project or session")
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(o.out, "skill %s: %s for %s\n", terminalSafeLine(ref), sub+"d", scope)
		return nil
	case "run":
		ref, err := skillCommandRef(cmd)
		if err != nil {
			return errors.New("skills: usage: /skills run <id|unique-name>")
		}
		summary, err := o.SkillRun(ctx, ref)
		if err != nil {
			return err
		}
		if summary != "" {
			fmt.Fprintln(o.out, summary)
		}
		return nil
	default:
		return errors.New("skills: usage: /skills list|show|enable|disable|run")
	}
}

func skillCommandRef(cmd Command) (string, error) {
	if len(cmd.Args) != 2 {
		return "", errors.New("one skill reference is required")
	}
	ref := strings.TrimSpace(cmd.Args[1])
	if ref == "" {
		return "", errors.New("skill reference is required")
	}
	return ref, nil
}

func skillStatus(summary SkillSummary) string {
	if summary.Error != "" {
		return terminalSafeLine("invalid: " + summary.Error)
	}
	if summary.Enabled {
		return "enabled"
	}
	return "disabled"
}
