package cockpit

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
	"unicode"
)

func browserCommand(target, platform string) ([]string, error) {
	if len(target) > 8192 || strings.IndexFunc(target, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) || unicode.In(r, unicode.Cf) }) >= 0 {
		return nil, errors.New("invalid browser link")
	}
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return nil, errors.New("only HTTP and HTTPS browser links are supported")
	}
	switch platform {
	case "darwin":
		return []string{"/usr/bin/open", target}, nil
	case "windows":
		return []string{"rundll32.exe", "url.dll,FileProtocolHandler", target}, nil
	default:
		return []string{"xdg-open", target}, nil
	}
}
func openBrowser(ctx context.Context, target string) error {
	argv, err := browserCommand(target, runtime.GOOS)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, argv[0], argv[1:]...).Run(); err != nil {
		return fmt.Errorf("could not open browser: %w", err)
	}
	return nil
}
