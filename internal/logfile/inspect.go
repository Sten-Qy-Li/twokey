// Package logfile reads and writes the project's log file. twokey is the
// only writer (SPEC section 7).
package logfile

import "bytes"

// Line endings, as Inspect reports them.
const (
	LF    = "LF"
	CRLF  = "CRLF"
	Mixed = "mixed"
	None  = "none" // the file has no line breaks
)

var bom = []byte{0xEF, 0xBB, 0xBF}

// Info describes a log file's format.
type Info struct {
	BOM         bool   // the file starts with a UTF-8 byte order mark
	LineEnding  string // LF, CRLF, Mixed or None
	MarkerLines []int  // 1-based numbers of the lines that are exactly the marker
}

// Inspect reads the format of a log file. A line counts as a marker line
// only if the whole line equals the marker; a header that mentions the
// marker inside a sentence doesn't count.
func Inspect(data []byte, marker string) Info {
	var info Info
	if bytes.HasPrefix(data, bom) {
		info.BOM = true
		data = data[len(bom):]
	}

	var lf, crlf int
	lines := bytes.Split(data, []byte("\n"))
	for i, line := range lines {
		ended := i < len(lines)-1 // the last piece has no line break after it
		if bytes.HasSuffix(line, []byte("\r")) {
			line = line[:len(line)-1]
			if ended {
				crlf++
			}
		} else if ended {
			lf++
		}
		if string(line) == marker {
			info.MarkerLines = append(info.MarkerLines, i+1)
		}
	}

	switch {
	case lf > 0 && crlf > 0:
		info.LineEnding = Mixed
	case crlf > 0:
		info.LineEnding = CRLF
	case lf > 0:
		info.LineEnding = LF
	default:
		info.LineEnding = None
	}
	return info
}
