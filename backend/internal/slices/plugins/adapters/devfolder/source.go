// Package devfolder packs a plugin source folder exactly as
// scripts/plugin/package.mjs does; change both together. Only plugin.json,
// main.js and style.css are read, through an os.Root, so nothing outside the
// folder is reachable.
package devfolder

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"unicode/utf8"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/plugins/domain"
)

const (
	manifestFile = "plugin.json"
	scriptFile   = "main.js"
	cssFile      = "style.css"
	// maxSourceBytes is the most a source file may hold before it is read.
	// The script and css budgets are package rules checked afterwards.
	maxSourceBytes = 262_144
)

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
	files := domain.SourceFiles{Manifest: manifest}
	if !scriptMissing {
		files.Script = nonNil(script)
	}
	if !cssMissing {
		files.CSS = nonNil(css)
	}
	packed, assembled := domain.AssembleSource(files)
	return packed, append(problems, assembled...)
}

// nonNil keeps an unreadable but present file present, so its problem is
// reported beside the package's own rather than the file looking absent.
func nonNil(text []byte) []byte {
	if text == nil {
		return []byte{}
	}
	return text
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
	return domain.TrimBOM(text), false, ""
}
