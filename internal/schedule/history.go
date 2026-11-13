package schedule

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Slot outcomes, for the morning view.
const (
	Running    = "running"     // started; a later record says how it ended
	RoundDone  = "round_done"  // fixed and pushed; the fix isn't audited yet
	Converged  = "converged"   // both agents cleared the commit
	NothingNew = "nothing_new" // the run had converged and the branch hadn't moved
	Paused     = "paused"      // a pause: it waits for the owner
	Stopped    = "stopped"     // twokey stop or Ctrl+C
	Skipped    = "skipped"     // the slot didn't start a round, and changed nothing
	Failed     = "failed"      // twokey couldn't start or finish the round
	Ended      = "ended"       // the end date passed; the schedule switched off
	TestOK     = "test"        // "twokey schedule test": the checks passed
)

// Record is one slot in schedule-history.jsonl.
type Record struct {
	Planned  time.Time `json:"planned,omitzero"` // the start time it belongs to
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished,omitzero"`
	Test     bool      `json:"test,omitempty"`
	Outcome  string    `json:"outcome"`
	Run      string    `json:"run,omitempty"`
	Round    int       `json:"round,omitempty"`
	Lines    []string  `json:"lines,omitempty"` // what happened, in plain sentences
	Log      string    `json:"log,omitempty"`   // the slot's console output
}

// Append adds a record. A slot writes one when it starts and one when it
// ends; History keeps the last one per start.
func Append(runRoot string, r Record) error {
	if err := os.MkdirAll(runRoot, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(runRoot, HistoryFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(data, '\n'))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// History returns the slots, oldest first, one record per start. A line
// that can't be read is left out.
func History(runRoot string) ([]Record, error) {
	f, err := os.Open(filepath.Join(runRoot, HistoryFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Record
	at := map[time.Time]int{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var r Record
		if json.Unmarshal(sc.Bytes(), &r) != nil {
			continue
		}
		key := r.Started.UTC()
		if i, ok := at[key]; ok {
			out[i] = r
			continue
		}
		at[key] = len(out)
		out = append(out, r)
	}
	return out, sc.Err()
}
