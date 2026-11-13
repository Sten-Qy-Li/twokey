package notify

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/Sten-Qy-Li/twokey/internal/proc"
)

// fakeRunner records the commands Send runs, without running them.
type fakeRunner struct {
	cmds []proc.Cmd
	fail bool
}

func (f *fakeRunner) LookPath(name string) (string, error) { return exec.LookPath(name) }

func (f *fakeRunner) Run(_ context.Context, c proc.Cmd) proc.Result {
	f.cmds = append(f.cmds, c)
	if f.fail {
		return proc.Result{ExitCode: 1, Err: errors.New("exit status 1"),
			Stderr: "#< CLIXML\r\nNew-Object : Cannot find type [Windows.Data.Xml.Dom.XmlDocument]\r\n"}
	}
	return proc.Result{}
}

func script(t *testing.T, args []string) string {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(args[len(args)-1])
	if err != nil || args[len(args)-2] != "-EncodedCommand" {
		t.Fatalf("args %q: %v", args, err)
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
	}
	return string(utf16.Decode(u))
}

func TestToastArgs(t *testing.T) {
	s := script(t, ToastArgs("twokey: paused", "Run r1's round <2> & more"))
	for _, want := range []string{
		"$ProgressPreference = 'SilentlyContinue'",
		"<text>twokey: paused</text><text>Run r1&#39;s round &lt;2&gt; &amp; more</text>",
		"ContentType = WindowsRuntime",
		`CreateToastNotifier('{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe').Show($t)`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("script lacks %q:\n%s", want, s)
		}
	}
}

func TestSend(t *testing.T) {
	var got struct{ method, path, title, body string }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got.method, got.path, got.title, got.body = r.Method, r.URL.Path, r.Header.Get("Title"), string(b)
	}))
	defer srv.Close()
	f := &fakeRunner{}
	var out bytes.Buffer
	n := Notifier{Toast: true, Bell: true, NtfyServer: srv.URL + "/", NtfyTopic: "my-topic", Proc: f, HTTP: srv.Client(), Out: &out, GOOS: "windows"}
	n.Send(t.Context(), "twokey: round done", "Run r1: round 2 is fixed and pushed.")
	if len(f.cmds) != 1 || f.cmds[0].Name != "powershell.exe" {
		t.Errorf("commands: %+v", f.cmds)
	}
	if got.method != http.MethodPost || got.path != "/my-topic" || got.title != "twokey: round done" || got.body != "Run r1: round 2 is fixed and pushed." {
		t.Errorf("ntfy got %+v", got)
	}
	if out.String() != "\a" {
		t.Errorf("output %q, want only the bell", out.String())
	}
}

// A notification that fails is a warning, never a stop.
func TestSendFailures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusTooManyRequests)
	}))
	defer srv.Close()
	f := &fakeRunner{fail: true}
	var out bytes.Buffer
	n := Notifier{Toast: true, NtfyServer: srv.URL, NtfyTopic: "t", Proc: f, HTTP: srv.Client(), Out: &out, GOOS: "windows"}
	n.Send(t.Context(), "title", "message")
	for _, want := range []string{
		"[WARN]  Can't show a Windows toast: New-Object : Cannot find type [Windows.Data.Xml.Dom.XmlDocument]",
		"[WARN]  Can't send the phone notification (ntfy): the server answered 429 Too Many Requests",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

// Toasts only on Windows and when on; ntfy only with a topic.
func TestSendOff(t *testing.T) {
	f := &fakeRunner{}
	Notifier{Toast: true, NtfyServer: "http://127.0.0.1:1", Proc: f, GOOS: "linux"}.Send(t.Context(), "t", "m")
	Notifier{Toast: false, Proc: f, GOOS: "windows"}.Send(t.Context(), "t", "m")
	if len(f.cmds) != 0 {
		t.Errorf("commands: %+v", f.cmds)
	}
}
