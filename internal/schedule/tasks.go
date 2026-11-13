package schedule

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Sten-Qy-Li/twokey/internal/proc"
)

// Tasks registers and changes tasks with schtasks.exe. Tests replace the
// runner.
type Tasks struct {
	R proc.Runner
	// Dir holds the XML file while schtasks reads it.
	Dir string
}

// Register creates the task, or replaces it (/F).
func (t Tasks) Register(ctx context.Context, name string, xml []byte) error {
	if err := os.MkdirAll(t.Dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(t.Dir, "task-*.xml")
	if err != nil {
		return err
	}
	path := f.Name()
	defer os.Remove(path)
	_, werr := f.Write(xml)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return werr
	}
	return t.run(ctx, "/Create", "/TN", name, "/XML", path, "/F")
}

// SetEnabled switches the task on or off.
func (t Tasks) SetEnabled(ctx context.Context, name string, on bool) error {
	flag := "/DISABLE"
	if on {
		flag = "/ENABLE"
	}
	return t.run(ctx, "/Change", "/TN", name, flag)
}

// Delete removes the task.
func (t Tasks) Delete(ctx context.Context, name string) error {
	return t.run(ctx, "/Delete", "/TN", name, "/F")
}

// Info is what Task Scheduler has for a task.
type Info struct {
	Exists  bool
	Enabled bool
	Exe     string // the program it runs
}

// Query reads the task back. A task that doesn't exist isn't an error.
func (t Tasks) Query(ctx context.Context, name string) Info {
	r := t.R.Run(ctx, proc.Cmd{Name: "schtasks.exe", Args: []string{"/Query", "/TN", name, "/XML"}})
	if r.Err != nil {
		return Info{}
	}
	return ParseTaskXML(r.Stdout)
}

// ParseTaskXML reads what "schtasks /Query /XML" prints. Task Scheduler
// leaves out settings that have their default value, so a task is enabled
// unless its settings say <Enabled>false</Enabled>.
func ParseTaskXML(text string) Info {
	info := Info{Exists: true, Enabled: true}
	if _, settings, ok := strings.Cut(text, "<Settings>"); ok {
		settings, _, _ = strings.Cut(settings, "</Settings>")
		info.Enabled = !strings.Contains(settings, "<Enabled>false</Enabled>")
	}
	if _, rest, ok := strings.Cut(text, "<Command>"); ok {
		info.Exe, _, _ = strings.Cut(rest, "</Command>")
		info.Exe = strings.TrimSpace(info.Exe)
	}
	return info
}

func (t Tasks) run(ctx context.Context, args ...string) error {
	r := t.R.Run(ctx, proc.Cmd{Name: "schtasks.exe", Args: args})
	if r.Err != nil {
		msg := strings.TrimSpace(r.Stderr + "\n" + r.Stdout)
		if msg == "" {
			msg = r.Err.Error()
		}
		return fmt.Errorf("schtasks %s: %s", args[0], firstLine(msg))
	}
	return nil
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(s)
}

// LogPath is a slot's console log in the run root.
func LogPath(runRoot string, start string) string {
	return filepath.Join(runRoot, LogDir, start+".log")
}
