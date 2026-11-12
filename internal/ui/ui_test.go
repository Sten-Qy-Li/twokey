package ui

import (
	"bytes"
	"testing"
)

func TestLines(t *testing.T) {
	var b bytes.Buffer
	Line(&b, Fail, Sentence("there are no runs"))
	Next(&b, "twokey run")
	want := "[FAIL]  There are no runs\n        Next: twokey run\n"
	if b.String() != want {
		t.Errorf("got %q, want %q", b.String(), want)
	}
	if Sentence("") != "" || Sentence("éa") != "Éa" {
		t.Error("Sentence")
	}
}
