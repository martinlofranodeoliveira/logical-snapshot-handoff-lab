// Package model defines the deliberately small, database-independent contract.
package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

const MaxRows = 32
const MaxChanges = 64
const MaxPayload = 64 * 1024
const MaxRaw = 128 * 1024

// LSN is unsigned. SQLite stores its fixed-width hexadecimal representation.
type LSN uint64

func (l LSN) String() string { return fmt.Sprintf("%X/%X", uint64(l)>>32, uint64(l)&0xffffffff) }
func (l LSN) Hex() string    { return fmt.Sprintf("%016x", uint64(l)) }
func ParseLSN(s string) (LSN, error) {
	p := strings.Split(s, "/")
	if len(p) != 2 || len(p[0]) < 1 || len(p[0]) > 8 || len(p[1]) < 1 || len(p[1]) > 8 {
		return 0, Fail("INVALID_LSN", "expected two hexadecimal uint32 components")
	}
	a, e := strconv.ParseUint(p[0], 16, 32)
	if e != nil {
		return 0, Fail("INVALID_LSN", "invalid high component")
	}
	b, e := strconv.ParseUint(p[1], 16, 32)
	if e != nil {
		return 0, Fail("INVALID_LSN", "invalid low component")
	}
	return LSN(a<<32 | b), nil
}
func HexLSN(s string) (LSN, error) {
	if len(s) != 16 {
		return 0, Fail("REBOOTSTRAP_REQUIRED", "invalid stored checkpoint width")
	}
	n, e := strconv.ParseUint(s, 16, 64)
	if e != nil || fmt.Sprintf("%016x", n) != s {
		return 0, Fail("REBOOTSTRAP_REQUIRED", "invalid stored checkpoint")
	}
	return LSN(n), nil
}
func (l LSN) MarshalJSON() ([]byte, error) { return json.Marshal(l.String()) }
func (l *LSN) UnmarshalJSON(b []byte) error {
	var s string
	if e := json.Unmarshal(b, &s); e != nil {
		return e
	}
	n, e := ParseLSN(s)
	if e == nil {
		*l = n
	}
	return e
}

type Identity struct {
	System      string `json:"system"`
	Database    uint32 `json:"database_oid"`
	Table       uint32 `json:"table_oid"`
	Publication string `json:"publication"`
	Slot        string `json:"slot"`
}
type Item struct {
	ID       int64  `json:"id"`
	Label    string `json:"label"`
	Revision int64  `json:"revision"`
}

func (r Item) Validate() error {
	if !utf8.ValidString(r.Label) || len(r.Label) > 256 || strings.ContainsRune(r.Label, 0) || r.Revision <= 0 {
		return Fail("INVALID_ROW", "label must be UTF-8, NUL-free, at most 256 bytes; revision must be positive")
	}
	return nil
}

type Change struct {
	Kind string `json:"kind"`
	Item Item   `json:"item"`
}
type Transaction struct {
	End     LSN      `json:"end_lsn"`
	Changes []Change `json:"changes"`
}

func (t Transaction) Validate() error {
	if t.End == 0 {
		return Fail("INVALID_TRANSACTION", "zero transaction endpoint")
	}
	if len(t.Changes) > MaxChanges {
		return Fail("TRANSACTION_LIMIT", "more than 64 changes")
	}
	size := 0
	for _, c := range t.Changes {
		switch c.Kind {
		case "insert", "update":
			size += 16 + len(c.Item.Label)
		case "delete":
			size += 8
		case "truncate":
		default:
			return Fail("UNSUPPORTED_MESSAGE", "unsupported change kind")
		}
		if size > MaxPayload {
			return Fail("TRANSACTION_LIMIT", "decoded payload exceeds 64 KiB")
		}
	}
	for _, c := range t.Changes {
		if c.Kind == "insert" || c.Kind == "update" {
			if e := c.Item.Validate(); e != nil {
				return e
			}
		}
	}
	return nil
}

type State struct {
	Initialized    bool     `json:"initialized"`
	Identity       Identity `json:"identity"`
	Checkpoint     LSN      `json:"checkpoint"`
	Applied        int64    `json:"applied_transactions"`
	Rows           []Item   `json:"rows"`
	SQLiteVersion  string   `json:"sqlite_version"`
	SQLiteSourceID string   `json:"sqlite_source_id"`
}
type Error struct {
	Code   string
	Detail string
}

func (e *Error) Error() string       { return e.Code + ": " + e.Detail }
func Fail(code, detail string) error { return &Error{code, detail} }
func Code(e error) string {
	var x *Error
	if errors.As(e, &x) {
		return x.Code
	}
	return "INTERNAL"
}
func Diagnostic(e error) string {
	if e == nil {
		return ""
	}
	s := strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return ' '
		}
		return r
	}, e.Error())
	if len(s) > 600 {
		s = s[:600]
	}
	return strings.ToValidUTF8(s, "?")
}
