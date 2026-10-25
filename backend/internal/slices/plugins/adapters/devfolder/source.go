// Package devfolder packs a plugin source folder exactly as
// scripts/plugin/package.mjs does; change both together. Only plugin.json,
// main.js and style.css are read, through an os.Root, so nothing outside the
// folder is reachable.
package devfolder

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"unicode/utf8"
)

const (
	manifestFile = "plugin.json"
	scriptFile   = "main.js"
	cssFile      = "style.css"
	// maxSourceBytes is the most a source file may hold before it is read.
	// The script and css budgets are package rules checked afterwards.
	maxSourceBytes = 262_144
)

var bom = []byte{0xef, 0xbb, 0xbf}

type Source struct{}

func New() Source { return Source{} }

// Pack returns the folder's package JSON and every problem found while
// assembling it; one problem does not hide the others. The package is nil
// when plugin.json could not be read as an object; otherwise it is returned
// even beside problems, so the package checks can report theirs too.
func (Source) Pack(path string) (json.RawMessage, []string) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, []string{"The folder cannot be opened; check that it exists and is readable."}
	}
	defer root.Close()
	var problems []string
	manifest, missing, problem := readSource(root, manifestFile)
	if missing {
		return nil, []string{"The folder has no plugin.json."}
	}
	if problem != "" {
		problems = append(problems, problem)
	}
	script, scriptMissing, problem := readSource(root, scriptFile)
	if problem != "" {
		problems = append(problems, problem)
	}
	css, cssMissing, problem := readSource(root, cssFile)
	if problem != "" {
		problems = append(problems, problem)
	}
	if manifest == nil {
		return nil, problems
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(manifest, &value); err != nil {
		if json.Valid(manifest) {
			return nil, append(problems, "plugin.json must hold a JSON object.")
		}
		return nil, append(problems, "plugin.json is not valid JSON: "+err.Error())
	}
	if _, found := value["script"]; found {
		problems = append(problems, `plugin.json may not carry "script"; the script is main.js.`)
	}
	if _, found := value["css"]; found {
		problems = append(problems, `plugin.json may not carry "css"; the stylesheet is style.css.`)
	}
	if _, found := value["$schema"]; found {
		problems = append(problems, `plugin.json may not carry "$schema" (packages refuse unknown fields); point your editor at docs/plugins/plugin.schema.json instead.`)
	}
	delete(value, "script")
	delete(value, "css")
	delete(value, "$schema")
	if !scriptMissing {
		value["script"], _ = json.Marshal(string(script))
	}
	if !cssMissing {
		value["css"], _ = json.Marshal(string(css))
	}
	packed, err := json.Marshal(value)
	if err != nil {
		return nil, append(problems, "The package could not be assembled.")
	}
	return packed, problems
}

// readSource reads one regular file of the folder as strict UTF-8 with one
// leading byte-order mark removed. A missing file is reported as missing,
// not as a problem.
func readSource(root *os.Root, name string) (text []byte, missing bool, problem string) {
	info, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, true, ""
	}
	if err != nil {
		return nil, false, fmt.Sprintf("Cannot read %s.", name)
	}
	if !info.Mode().IsRegular() {
		return nil, false, name + " is not a regular file."
	}
	if info.Size() > maxSourceBytes {
		return nil, false, name + " is larger than 256 KiB."
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, false, fmt.Sprintf("Cannot read %s.", name)
	}
	defer file.Close()
	text, err = io.ReadAll(io.LimitReader(file, maxSourceBytes+1))
	switch {
	case err != nil:
		return nil, false, fmt.Sprintf("Cannot read %s.", name)
	case len(text) > maxSourceBytes:
		return nil, false, name + " is larger than 256 KiB."
	case !utf8.Valid(text):
		return nil, false, name + " is not valid UTF-8."
	}
	return bytes.TrimPrefix(text, bom), false, ""
}
