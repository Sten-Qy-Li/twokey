package logfile

import (
	"reflect"
	"testing"
)

const marker = "<!-- NEW LOGS GO DIRECTLY BELOW THIS LINE -->"

func TestInspect(t *testing.T) {
	cases := []struct {
		name string
		data string
		want Info
	}{
		{
			name: "LF, one marker",
			data: "# Log\n\n" + marker + "\n\n### entry\n",
			want: Info{LineEnding: LF, MarkerLines: []int{3}},
		},
		{
			name: "CRLF",
			data: "# Log\r\n" + marker + "\r\n",
			want: Info{LineEnding: CRLF, MarkerLines: []int{2}},
		},
		{
			name: "BOM",
			data: "\xEF\xBB\xBF" + marker + "\n",
			want: Info{BOM: true, LineEnding: LF, MarkerLines: []int{1}},
		},
		{
			name: "mixed",
			data: "a\r\nb\n" + marker,
			want: Info{LineEnding: Mixed, MarkerLines: []int{3}},
		},
		{
			name: "no line breaks",
			data: marker,
			want: Info{LineEnding: None, MarkerLines: []int{1}},
		},
		{
			// A header may mention the marker inside a sentence; only a
			// line that is exactly the marker counts.
			name: "inline mention doesn't count",
			data: "- Insert each new entry directly under the `" + marker + "` line.\n\n" + marker + "\n",
			want: Info{LineEnding: LF, MarkerLines: []int{3}},
		},
		{
			name: "marker with spaces around doesn't count",
			data: " " + marker + "\n" + marker + " \n",
			want: Info{LineEnding: LF},
		},
		{
			name: "duplicate marker",
			data: marker + "\n\n" + marker + "\n",
			want: Info{LineEnding: LF, MarkerLines: []int{1, 3}},
		},
		{
			name: "missing marker",
			data: "# Log\n",
			want: Info{LineEnding: LF},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Inspect([]byte(c.data), marker)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %+v, want %+v", got, c.want)
			}
		})
	}
}
