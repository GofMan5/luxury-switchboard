package domain

import (
	"bytes"
	"encoding/json"
)

// SourceFiles is a plugin source folder as read: plugin.json, and main.js
// and style.css when present (nil when absent). Folder links and link
// downloads both assemble through AssembleSource, so a folder means the same
// thing wherever it is read from; scripts/plugin/package.mjs follows it too.
type SourceFiles struct {
	Manifest []byte
	Script   []byte
	CSS      []byte
}

var byteOrderMark = []byte{0xef, 0xbb, 0xbf}

// TrimBOM drops one leading UTF-8 byte-order mark.
func TrimBOM(text []byte) []byte { return bytes.TrimPrefix(text, byteOrderMark) }

// AssembleSource returns the package JSON and every problem found; one
// problem does not hide the others. The package is nil when plugin.json is
// not a JSON object; otherwise it is returned even beside problems, so the
// package checks can report theirs too.
func AssembleSource(files SourceFiles) (json.RawMessage, []string) {
	var problems []string
	var value map[string]json.RawMessage
	if err := json.Unmarshal(files.Manifest, &value); err != nil || value == nil {
		if err == nil || json.Valid(files.Manifest) {
			return nil, []string{"plugin.json must hold a JSON object."}
		}
		return nil, []string{"plugin.json is not valid JSON: " + err.Error()}
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
	if files.Script != nil {
		value["script"], _ = json.Marshal(string(files.Script))
	}
	if files.CSS != nil {
		value["css"], _ = json.Marshal(string(files.CSS))
	}
	packed, err := json.Marshal(value)
	if err != nil {
		return nil, append(problems, "The package could not be assembled.")
	}
	return packed, problems
}
