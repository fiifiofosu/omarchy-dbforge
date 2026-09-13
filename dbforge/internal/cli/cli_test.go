package cli

import (
	"flag"
	"strings"
	"testing"
)

// parseCreateArgs mirrors the ordering logic in cmdCreate. Go's flag package
// stops at the first positional argument, which silently dropped --name in the
// natural `create postgres:16 --name app` ordering.
func parseCreateArgs(t *testing.T, args []string) (ref, name string, port int) {
	t.Helper()
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	n := fs.String("name", "", "")
	p := fs.Int("port", 0, "")
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		ref, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	if ref == "" {
		ref = fs.Arg(0)
	}
	return ref, *n, *p
}

func TestCreateFlagsAfterPositional(t *testing.T) {
	ref, name, port := parseCreateArgs(t, []string{"postgres:16", "--name", "app-db", "--port", "5432"})
	if ref != "postgres:16" || name != "app-db" || port != 5432 {
		t.Fatalf("ref=%q name=%q port=%d; flags after the positional were dropped", ref, name, port)
	}
}

func TestCreateFlagsBeforePositional(t *testing.T) {
	ref, name, _ := parseCreateArgs(t, []string{"--name", "app-db", "postgres:16"})
	if ref != "postgres:16" || name != "app-db" {
		t.Fatalf("ref=%q name=%q", ref, name)
	}
}

func TestCreateBareRef(t *testing.T) {
	ref, name, _ := parseCreateArgs(t, []string{"redis:7"})
	if ref != "redis:7" || name != "" {
		t.Fatalf("ref=%q name=%q", ref, name)
	}
}

func TestSplitLeadingPositional(t *testing.T) {
	cases := []struct {
		name     string
		in       []string
		wantPos  string
		wantRest []string
	}{
		{"positional then flags", []string{"app-db", "--wipe-data", "--force"}, "app-db", []string{"--wipe-data", "--force"}},
		{"flags only", []string{"--wipe-data", "app-db"}, "", []string{"--wipe-data", "app-db"}},
		{"positional only", []string{"app-db"}, "app-db", []string{}},
		{"empty", nil, "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pos, rest := splitLeadingPositional(tc.in)
			if pos != tc.wantPos {
				t.Errorf("positional = %q, want %q", pos, tc.wantPos)
			}
			if len(rest) != len(tc.wantRest) {
				t.Errorf("rest = %v, want %v", rest, tc.wantRest)
			}
		})
	}
}

// The dangerous ordering: flags after the id must still be honoured, or
// --wipe-data is silently dropped.
func TestRemoveFlagsAfterPositionalAreHonoured(t *testing.T) {
	fs := flag.NewFlagSet("rm", flag.ContinueOnError)
	wipe := fs.Bool("wipe-data", false, "")
	force := fs.Bool("force", false, "")
	id, rest := splitLeadingPositional([]string{"app-db", "--wipe-data", "--force"})
	if err := fs.Parse(rest); err != nil {
		t.Fatal(err)
	}
	if id != "app-db" || !*wipe || !*force {
		t.Fatalf("id=%q wipe=%v force=%v; flags after the id were dropped", id, *wipe, *force)
	}
}
