// Package ruleset embeds the vendored detection rules so the engine needs no
// file at runtime: a user cannot weaken the guardrails by editing a JSON file on
// disk, and the public edition carries the same rules as the owner edition.
//
// The rule data comes from the MIT-licensed holone project; see
// LICENSE-holone.txt in this directory.
//
// One rule is edited rather than vendored verbatim. fs-alternate-data-stream
// matched a bare `\bads\b`, so any answer that mentioned advertising was a
// high-severity finding and, in block mode, a refusal. It now matches the
// PowerShell -Stream parameter, which is how an alternate data stream is
// actually written, and no longer matches the English word.
package ruleset

import _ "embed"

//go:embed rules.json
var RulesJSON []byte

//go:embed blocklist.json
var BlocklistJSON []byte
