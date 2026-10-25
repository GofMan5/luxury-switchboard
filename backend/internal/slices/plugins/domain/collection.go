package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
)

const (
	// MaxCollections bounds the collections one plugin may hold.
	MaxCollections = 16
	// MaxDocumentBytes bounds one stored document, encoded.
	MaxDocumentBytes = 64 * 1024
	// MaxCollectionBytes bounds everything one plugin stores in collections.
	MaxCollectionBytes = 2 * 1024 * 1024
	// DocumentOverheadBytes is counted for every document on top of its id and
	// body, so many tiny documents cannot outgrow the encrypted file the
	// limit is meant to bound.
	DocumentOverheadBytes = 8
)

var (
	collectionName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	documentID     = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
)

func ValidCollectionName(name string) bool { return collectionName.MatchString(name) }
func ValidDocumentID(id string) bool       { return documentID.MatchString(id) }

// Document returns raw as a storable document: one compact JSON value with
// HTML-significant characters escaped, which is exactly how it is saved and
// what its size is counted as. null is refused, because get answers null for
// a document that does not exist.
func Document(raw json.RawMessage) (json.RawMessage, error) {
	var compact bytes.Buffer
	if !json.Valid(raw) || json.Compact(&compact, raw) != nil {
		return nil, ValidationError{"doc", "must be JSON"}
	}
	if compact.String() == "null" {
		return nil, ValidationError{"doc", "must not be null; delete the document instead"}
	}
	var escaped bytes.Buffer
	json.HTMLEscape(&escaped, compact.Bytes())
	if escaped.Len() > MaxDocumentBytes {
		return nil, ValidationError{"doc", fmt.Sprintf("must be at most %d bytes", MaxDocumentBytes)}
	}
	return escaped.Bytes(), nil
}

// DocumentCost is what one stored document counts against
// MaxCollectionBytes.
func DocumentCost(id string, doc json.RawMessage) int {
	return len(id) + len(doc) + DocumentOverheadBytes
}
