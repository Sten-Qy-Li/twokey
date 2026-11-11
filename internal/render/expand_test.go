package render

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestExpand(t *testing.T) {
	vars := map[string]string{"branch": "feature", "round": "2", "empty": ""}
	cases := []struct {
		name, tmpl, want string
	}{
		{"placeholder", "Branch: ${branch}.", "Branch: feature."},
		{"twice", "${round}/${round}", "2/2"},
		{"empty value", "[${empty}]", "[]"},
		{"literal ${", "Write $${HOME} in the shell.", "Write ${HOME} in the shell."},
		{"bare $word", "echo $HOME and $env:TEMP", "echo $HOME and $env:TEMP"},
		{"lone $", "costs $5, ends with $", "costs $5, ends with $"},
		{"$$ alone", "pid $$ here", "pid $$ here"},
		{"$$$ is a lone $ then $${", "$$${branch}", "$${branch}"},
		{"no placeholders", "plain text\n", "plain text\n"},
		{"value with ${", "${branch}", "feature"},
	}
	for _, c := range cases {
		got, err := Expand(c.tmpl, vars)
		if err != nil || got != c.want {
			t.Errorf("%s: Expand(%q) = %q, %v; want %q", c.name, c.tmpl, got, err, c.want)
		}
	}
}

// A value is inserted as it is; it is never expanded again.
func TestExpandDoesNotRecurse(t *testing.T) {
	got, err := Expand("${a}", map[string]string{"a": "${b}"})
	if err != nil || got != "${b}" {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestExpandErrors(t *testing.T) {
	vars := map[string]string{"branch": "feature"}

	_, err := Expand("${branch} ${nope} ${nope} ${other}", vars)
	var me *MissingError
	if !errors.As(err, &me) || !reflect.DeepEqual(me.Names, []string{"nope", "other"}) {
		t.Fatalf("missing: got %v", err)
	}
	if !strings.Contains(err.Error(), "${nope}, ${other}") {
		t.Errorf("message: %v", err)
	}

	for tmpl, want := range map[string]string{
		"a\nb ${branch":    "line 2: a placeholder starts with ${ but has no closing }",
		"${not a name}":    "line 1: ${not a name} is not a valid placeholder name",
		"${}":              "line 1: ${} is not a valid placeholder name",
		"x\n\n${1branch}y": "line 3: ${1branch} is not a valid placeholder name",
	} {
		_, err := Expand(tmpl, vars)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Expand(%q) = %v, want %q", tmpl, err, want)
		}
	}
}

func TestNames(t *testing.T) {
	got := Names("${a} $${b} $c ${a} ${d_2}")
	if !reflect.DeepEqual(got, []string{"a", "d_2"}) {
		t.Errorf("Names = %v", got)
	}
}
