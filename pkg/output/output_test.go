package output

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestParseFormat(t *testing.T) {
	for _, s := range []string{"table", "TABLE", "json", "csv"} {
		if _, err := ParseFormat(s); err != nil {
			t.Errorf("ParseFormat(%q) failed: %v", s, err)
		}
	}
	if _, err := ParseFormat("yaml"); err == nil {
		t.Error("ParseFormat(\"yaml\") should fail")
	}
}

func sampleTable() Table {
	type entry struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
	}
	return Table{
		Headers: []string{"NAME", "SIZE"},
		Rows: [][]string{
			{"notes.txt", "1.2K"},
			{"data.bin", "3.0M"},
		},
		Items: []entry{
			{Name: "notes.txt", Size: 1258},
			{Name: "data.bin", Size: 3145728},
		},
	}
}

func TestRenderTable(t *testing.T) {
	var buf bytes.Buffer
	w := New(&buf, FormatTable)
	if err := w.Render(sampleTable()); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "NAME") {
		t.Errorf("header missing from table output:\n%s", out)
	}
	for _, want := range []string{"notes.txt", "1.2K", "data.bin", "3.0M"} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q:\n%s", want, out)
		}
	}
}

func TestRenderTableQuietDropsHeader(t *testing.T) {
	var buf bytes.Buffer
	w := New(&buf, FormatTable, Quiet(true))
	if err := w.Render(sampleTable()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "NAME") {
		t.Errorf("quiet mode should not print headers:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "notes.txt") {
		t.Error("quiet mode dropped the rows as well as the header")
	}
}

// TestRenderJSONUsesStructuredItems is the point of keeping Items separate from
// Rows: a script consuming --output json must get the real size, not the
// human-rounded string.
func TestRenderJSONUsesStructuredItems(t *testing.T) {
	var buf bytes.Buffer
	w := New(&buf, FormatJSON)
	if err := w.Render(sampleTable()); err != nil {
		t.Fatal(err)
	}

	var got []struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if len(got) != 2 {
		t.Fatalf("got %d items, want 2", len(got))
	}
	if got[0].Size != 1258 {
		t.Errorf("size = %d, want the exact byte count 1258 rather than the display string", got[0].Size)
	}
}

func TestRenderJSONFallsBackToRows(t *testing.T) {
	var buf bytes.Buffer
	w := New(&buf, FormatJSON)
	tbl := sampleTable()
	tbl.Items = nil
	if err := w.Render(tbl); err != nil {
		t.Fatal(err)
	}

	var got []map[string]string
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if len(got) != 2 || got[0]["NAME"] != "notes.txt" {
		t.Errorf("fallback JSON is wrong: %+v", got)
	}
}

func TestRenderCSV(t *testing.T) {
	var buf bytes.Buffer
	w := New(&buf, FormatCSV)
	if err := w.Render(sampleTable()); err != nil {
		t.Fatal(err)
	}
	want := "NAME,SIZE\nnotes.txt,1.2K\ndata.bin,3.0M\n"
	if buf.String() != want {
		t.Errorf("CSV output =\n%q\nwant\n%q", buf.String(), want)
	}
}

func TestStreamingJSON(t *testing.T) {
	var buf bytes.Buffer
	w := New(&buf, FormatJSON, Stream(true))
	if !w.Streaming() {
		t.Fatal("Streaming() should be true for JSON + Stream")
	}
	for _, name := range []string{"a", "b", "c"} {
		if err := w.Item(map[string]string{"name": name}); err != nil {
			t.Fatal(err)
		}
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d NDJSON lines, want 3:\n%s", len(lines), buf.String())
	}
	for _, line := range lines {
		var obj map[string]string
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Errorf("line %q is not valid JSON: %v", line, err)
		}
	}
}

func TestItemIsNoOpOutsideStreamingJSON(t *testing.T) {
	for _, f := range []Format{FormatTable, FormatCSV} {
		var buf bytes.Buffer
		w := New(&buf, f)
		if w.Streaming() {
			t.Errorf("%s should not report streaming", f)
		}
		if err := w.Item(map[string]string{"name": "a"}); err != nil {
			t.Fatal(err)
		}
		if buf.Len() != 0 {
			t.Errorf("%s mode: Item wrote %q, want nothing", f, buf.String())
		}
	}
}

func TestObject(t *testing.T) {
	type status struct {
		Provider string `json:"provider"`
		User     string `json:"user"`
	}
	v := status{Provider: "kerberos", User: "gdelmont"}
	fields := []Field{{"Provider", "kerberos"}, {"User", "gdelmont"}}

	t.Run("table", func(t *testing.T) {
		var buf bytes.Buffer
		if err := New(&buf, FormatTable).Object(v, fields...); err != nil {
			t.Fatal(err)
		}
		out := buf.String()
		if !strings.Contains(out, "Provider:") || !strings.Contains(out, "kerberos") {
			t.Errorf("key/value rendering is wrong:\n%s", out)
		}
	})

	t.Run("json", func(t *testing.T) {
		var buf bytes.Buffer
		if err := New(&buf, FormatJSON).Object(v, fields...); err != nil {
			t.Fatal(err)
		}
		var got status
		if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
			t.Fatalf("not valid JSON: %v", err)
		}
		if got != v {
			t.Errorf("got %+v, want %+v", got, v)
		}
	})
}

// TestMsgNeverContaminatesStdout guards the scripting contract: --output json
// must produce a parseable stream on stdout and nothing else.
func TestMsgNeverContaminatesStdout(t *testing.T) {
	var out, errBuf bytes.Buffer

	w := New(&out, FormatTable, Stderr(&errBuf))
	w.Msg("uploaded %d files", 3)
	if out.Len() != 0 {
		t.Errorf("Msg wrote to stdout: %q", out.String())
	}
	if !strings.Contains(errBuf.String(), "uploaded 3 files") {
		t.Errorf("Msg did not reach stderr: %q", errBuf.String())
	}

	errBuf.Reset()
	jw := New(&out, FormatJSON, Stderr(&errBuf))
	jw.Msg("uploaded %d files", 3)
	if errBuf.Len() != 0 {
		t.Errorf("Msg should be suppressed under --output json, got %q", errBuf.String())
	}
}

func TestWarnSurvivesJSONButNotQuiet(t *testing.T) {
	var out, errBuf bytes.Buffer

	New(&out, FormatJSON, Stderr(&errBuf)).Warn("certificate verification disabled")
	if !strings.Contains(errBuf.String(), "certificate verification disabled") {
		t.Errorf("warnings must survive JSON mode, got %q", errBuf.String())
	}

	errBuf.Reset()
	New(&out, FormatTable, Stderr(&errBuf), Quiet(true)).Warn("something")
	if errBuf.Len() != 0 {
		t.Errorf("quiet should suppress warnings, got %q", errBuf.String())
	}
}

func TestHumanSize(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{
		{0, "0B"},
		{999, "999B"},
		{1024, "1.0K"},
		{1536, "1.5K"},
		{1048576, "1.0M"},
		{3145728, "3.0M"},
		{1073741824, "1.0G"},
		{1099511627776, "1.0T"},
		{1125899906842624, "1.0P"},
	}
	for _, tt := range tests {
		if got := HumanSize(tt.in); got != tt.want {
			t.Errorf("HumanSize(%d) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestHumanTime(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

	recent := time.Date(2026, 9, 10, 8, 30, 0, 0, time.UTC)
	if got := HumanTime(recent, now); got != "Sep 10 08:30" {
		t.Errorf("recent timestamp = %q, want time of day", got)
	}

	old := time.Date(2024, 3, 2, 8, 30, 0, 0, time.UTC)
	if got := HumanTime(old, now); !strings.Contains(got, "2024") {
		t.Errorf("old timestamp = %q, want the year", got)
	}

	if got := HumanTime(time.Time{}, now); got != "-" {
		t.Errorf("zero timestamp = %q, want %q", got, "-")
	}
}

func TestColorOnlyWhenEnabled(t *testing.T) {
	var plain, colored bytes.Buffer
	if err := New(&plain, FormatTable).Render(sampleTable()); err != nil {
		t.Fatal(err)
	}
	if err := New(&colored, FormatTable, Color(true)).Render(sampleTable()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain.String(), "\033[") {
		t.Error("escape codes leaked into non-colour output")
	}
	if !strings.Contains(colored.String(), "\033[1m") {
		t.Error("colour output is missing the bold header")
	}
}

func TestIsTerminal(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "out-*")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if IsTerminal(f) {
		t.Error("a regular file should not be reported as a terminal")
	}
	if IsTerminal(nil) {
		t.Error("nil should not be reported as a terminal")
	}

	// /dev/null is a character device. A check based on os.ModeCharDevice would
	// call it a terminal, and a cron job with stdin redirected there would then
	// block forever on a confirmation prompt.
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Skipf("cannot open %s: %v", os.DevNull, err)
	}
	defer devNull.Close()
	if IsTerminal(devNull) {
		t.Errorf("%s must not be reported as a terminal", os.DevNull)
	}
}
