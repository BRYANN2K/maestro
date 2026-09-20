package cockpit

import "testing"

func TestBrowserCommandIsExplicitHTTPArgument(t *testing.T) {
	for _, target := range []string{"file:///tmp/a", "javascript:alert(1)", "https://u:p@example.test", "--exec", "https://example.test/\x1b", "https://example.test/ a", "https://example.test/\u202e"} {
		if _, err := browserCommand(target, "darwin"); err == nil {
			t.Fatalf("accepted %q", target)
		}
	}
	target := "https://example.test/login?code=123&next=$(touch%20/tmp/nope)"
	for _, platform := range []string{"darwin", "linux", "windows"} {
		argv, err := browserCommand(target, platform)
		if err != nil {
			t.Fatal(err)
		}
		if argv[len(argv)-1] != target {
			t.Fatalf("URL was not kept as a single literal argument: %q", argv)
		}
	}
}
