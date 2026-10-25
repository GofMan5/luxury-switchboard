package devfolder

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func folder(t *testing.T, files map[string]string) string {
	t.Helper()
	path := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(path, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestPackAssemblesTheFolder(t *testing.T) {
	path := folder(t, map[string]string{
		"plugin.json": "\xef\xbb\xbf" + `{"schema":1,"id":"acme.dusk","name":"Dusk"}`,
		"main.js":     "\xef\xbb\xbfexport default 1",
		"style.css":   ".x{}",
		"notes.txt":   "not read",
	})
	raw, problems := New().Pack(path)
	if len(problems) != 0 {
		t.Fatal(problems)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	if value["id"] != "acme.dusk" || value["script"] != "export default 1" || value["css"] != ".x{}" || len(value) != 5 {
		t.Fatalf("packed %s", raw)
	}

	raw, problems = New().Pack(folder(t, map[string]string{"plugin.json": `{"id":"acme.bare"}`}))
	if len(problems) != 0 || strings.Contains(string(raw), "script") || strings.Contains(string(raw), "css") {
		t.Fatalf("a folder without sources: %s %v", raw, problems)
	}
}

func TestPackReportsEveryProblem(t *testing.T) {
	for name, test := range map[string]struct {
		files    map[string]string
		packaged bool
		want     []string
	}{
		"no manifest":   {map[string]string{"main.js": "x"}, false, []string{"no plugin.json"}},
		"not json":      {map[string]string{"plugin.json": "{"}, false, []string{"not valid JSON"}},
		"not an object": {map[string]string{"plugin.json": "[1]"}, false, []string{"must hold a JSON object"}},
		"bad utf-8":     {map[string]string{"plugin.json": "{\"name\":\"\xff\"}"}, false, []string{"plugin.json is not valid UTF-8"}},
		"inline sources and bad script": {
			map[string]string{"plugin.json": `{"id":"acme.x","script":"x","css":"y","$schema":"z"}`, "main.js": "\xfe"},
			true,
			[]string{"main.js is not valid UTF-8", `may not carry "script"`, `may not carry "css"`, `may not carry "$schema"`},
		},
		"too large": {map[string]string{"plugin.json": `{}`, "style.css": strings.Repeat("x", maxSourceBytes+1)}, true, []string{"style.css is larger than 256 KiB"}},
	} {
		raw, problems := New().Pack(folder(t, test.files))
		if (raw != nil) != test.packaged || len(problems) != len(test.want) {
			t.Errorf("%s: %s %v", name, raw, problems)
			continue
		}
		for index, want := range test.want {
			if !strings.Contains(problems[index], want) {
				t.Errorf("%s: problem %q lacks %q", name, problems[index], want)
			}
		}
		if strings.Contains(string(raw), `"script":"x"`) {
			t.Errorf("%s: an inline script survived", name)
		}
	}
	if _, problems := New().Pack(filepath.Join(t.TempDir(), "missing")); len(problems) != 1 || !strings.Contains(problems[0], "cannot be opened") {
		t.Fatalf("a missing folder: %v", problems)
	}
	directory := folder(t, nil)
	if err := os.Mkdir(filepath.Join(directory, "plugin.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, problems := New().Pack(directory); len(problems) != 1 || !strings.Contains(problems[0], "not a regular file") {
		t.Fatalf("a directory named plugin.json: %v", problems)
	}
}

// A link inside the folder that points outside it is not followed.
func TestPackStaysInsideTheFolder(t *testing.T) {
	outside := folder(t, map[string]string{"secret.js": "fixture-private-outside"})
	inside := folder(t, map[string]string{"plugin.json": `{"id":"acme.x"}`})
	if err := os.Symlink(filepath.Join(outside, "secret.js"), filepath.Join(inside, "main.js")); err != nil {
		t.Skip("symlinks are unavailable here:", err)
	}
	raw, problems := New().Pack(inside)
	if strings.Contains(string(raw), "fixture-private") || len(problems) != 1 || !strings.Contains(problems[0], "main.js") {
		t.Fatalf("a link out of the folder: %s %v", raw, problems)
	}
}
