package model

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestUnsignedLSN(t *testing.T) {
	cases := []struct{ text, hex string }{{"0/0", "0000000000000000"}, {"80000000/0", "8000000000000000"}, {"FFFFFFFF/FFFFFFFF", "ffffffffffffffff"}}
	for _, c := range cases {
		n, e := ParseLSN(c.text)
		if e != nil || n.Hex() != c.hex {
			t.Fatalf("parse %s: %v %s", c.text, e, n.Hex())
		}
		n2, e := HexLSN(c.hex)
		if e != nil || n2 != n {
			t.Fatal("hex roundtrip", e)
		}
		b, _ := json.Marshal(n)
		var out LSN
		if e := json.Unmarshal(b, &out); e != nil || out != n {
			t.Fatal("JSON roundtrip", e)
		}
	}
	for _, bad := range []string{"-1/0", "100000000/0", "0/100000000", "1", "0/", "0/xyz"} {
		if _, e := ParseLSN(bad); e == nil {
			t.Fatal("accepted", bad)
		}
	}
	for _, badHex := range []string{"0", "000000000000000A", "000000000000000g", "00000000000000000"} {
		if _, e := HexLSN(badHex); Code(e) != "REBOOTSTRAP_REQUIRED" {
			t.Fatalf("expected REBOOTSTRAP_REQUIRED for %q, got %v", badHex, e)
		}
	}
}

func TestRowBoundaries(t *testing.T) {
	valid := Item{ID: -9, Label: "ação", Revision: 1}
	if e := valid.Validate(); e != nil {
		t.Fatal(e)
	}
	for _, r := range []Item{{1, string([]byte{255}), 1}, {1, "x\x00y", 1}, {1, "ok", 0}, {1, strings.Repeat("a", 257), 1}} {
		if Code(r.Validate()) != "INVALID_ROW" {
			t.Fatal("accepted invalid row")
		}
	}
}

func TestTransactionBoundariesAndDiagnosticSanitization(t *testing.T) {
	if e := (Transaction{End: 0}).Validate(); Code(e) != "INVALID_TRANSACTION" {
		t.Fatalf("expected INVALID_TRANSACTION, got %v", e)
	}
	if e := (Transaction{End: 1, Changes: []Change{{Kind: "noop"}}}).Validate(); Code(e) != "UNSUPPORTED_MESSAGE" {
		t.Fatalf("expected UNSUPPORTED_MESSAGE, got %v", e)
	}
	overChanges := Transaction{End: 1, Changes: make([]Change, MaxChanges+1)}
	for i := range overChanges.Changes {
		overChanges.Changes[i] = Change{Kind: "delete", Item: Item{ID: int64(i + 1)}}
	}
	if e := overChanges.Validate(); Code(e) != "TRANSACTION_LIMIT" {
		t.Fatalf("expected TRANSACTION_LIMIT for >64 changes, got %v", e)
	}
	overPayload := Transaction{End: 1, Changes: []Change{
		{Kind: "insert", Item: Item{ID: 1, Label: strings.Repeat("x", 33000), Revision: 1}},
		{Kind: "insert", Item: Item{ID: 2, Label: strings.Repeat("y", 33000), Revision: 1}},
	}}
	if e := overPayload.Validate(); Code(e) != "TRANSACTION_LIMIT" {
		t.Fatalf("expected TRANSACTION_LIMIT for >64KiB decoded payload, got %v", e)
	}
	rawErr := errors.New("line1\nline2\x00\x7f" + strings.Repeat("z", 700))
	diag := Diagnostic(rawErr)
	if len(diag) != 600 || strings.ContainsAny(diag, "\n\x00\x7f") {
		t.Fatalf("diagnostic was not sanitized/bounded: len=%d diag=%q", len(diag), diag)
	}
}
