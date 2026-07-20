package store

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/bizshuk/sessiond/pkg/model"
)

func meta() model.Meta {
	return model.Meta{Agent: "claude", SessionID: "sid1", WorkspacePath: "/ws/proj", Title: "t"}
}

func turns(n int) []model.Turn {
	out := make([]model.Turn, n)
	for i := range out {
		out[i] = model.Turn{Index: i + 1, Summary: "s", User: "u", Source: "heuristic", Status: "ok"}
	}
	return out
}

func lines(t *testing.T, fp string) []string {
	t.Helper()
	f, err := os.Open(fp)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var ls []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		ls = append(ls, sc.Text())
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return ls
}

func TestSync_createsMetaThenAppendsIncrementally(t *testing.T) {
	dir := t.TempDir()
	m := meta()

	// first fire: 2 turns → meta + 2 turn lines
	n, err := Sync(dir, m, turns(2))
	if err != nil || n != 2 {
		t.Fatalf("first sync appended=%d err=%v", n, err)
	}
	fp := SessionPath(dir, m)
	got := lines(t, fp)
	if len(got) != 3 {
		t.Fatalf("want 3 lines (meta+2), got %d: %v", len(got), got)
	}
	if !strings.Contains(got[0], `"type":"meta"`) {
		t.Errorf("line0 not meta: %s", got[0])
	}

	// second fire: now 4 turns total → only 2 new appended, no duplicate meta
	n, err = Sync(dir, m, turns(4))
	if err != nil || n != 2 {
		t.Fatalf("second sync appended=%d err=%v, want 2", n, err)
	}
	got = lines(t, fp)
	if len(got) != 5 {
		t.Fatalf("want 5 lines (meta+4), got %d", len(got))
	}
	if strings.Count(strings.Join(got, "\n"), `"type":"meta"`) != 1 {
		t.Error("meta written more than once")
	}

	// idempotent: re-fire with same 4 → nothing appended
	n, _ = Sync(dir, m, turns(4))
	if n != 0 {
		t.Errorf("re-sync appended %d, want 0", n)
	}
}

func TestSync_metaContentAndPath(t *testing.T) {
	dir := t.TempDir()
	m := meta()
	if _, err := Sync(dir, m, turns(1)); err != nil {
		t.Fatal(err)
	}
	fp := SessionPath(dir, m)
	if !strings.Contains(fp, "%2Fws%2Fproj") {
		t.Errorf("path not workspace-encoded: %s", fp)
	}
	var got model.Meta
	if err := json.Unmarshal([]byte(lines(t, fp)[0]), &got); err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != model.SCHEMA_VERSION || got.Type != model.RECORD_META {
		t.Errorf("meta = %+v", got)
	}
}

func TestEncodeDecodeWorkspace(t *testing.T) {
	if got := EncodeWorkspace(""); got != "_unknown" {
		t.Errorf("empty encode = %q", got)
	}
	p := "/Users/x/projects/a"
	if DecodeWorkspace(EncodeWorkspace(p)) != p {
		t.Errorf("roundtrip failed for %s", p)
	}
}
