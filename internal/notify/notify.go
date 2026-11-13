// Package notify tells the owner that a run needs them or has finished
// (SPEC section 10, DECISIONS.md D67): a Windows toast, a terminal bell,
// and an optional phone push through ntfy. A notification that fails is a
// [WARN] line, never a reason to stop.
package notify

import (
	"context"
	"encoding/base64"
	"fmt"
	"html"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/Sten-Qy-Li/twokey/internal/config"
	"github.com/Sten-Qy-Li/twokey/internal/proc"
	"github.com/Sten-Qy-Li/twokey/internal/ui"
)

// Notifier sends notifications.
type Notifier struct {
	Toast      bool   // a Windows toast through Windows PowerShell
	Bell       bool   // a terminal bell
	NtfyServer string // "" or the topic "" = no phone push
	NtfyTopic  string
	Proc       proc.Runner
	HTTP       *http.Client // nil = a client with a 15-second timeout
	Out        io.Writer    // the bell, and [WARN] lines when a notification fails
	GOOS       string
}

// New returns the notifier the config asks for.
func New(cfg config.Notify, r proc.Runner, out io.Writer, goos string) Notifier {
	return Notifier{Toast: cfg.Toast, Bell: true, NtfyServer: cfg.NtfyServer, NtfyTopic: cfg.NtfyTopic, Proc: r, Out: out, GOOS: goos}
}

// Send shows title and message on every channel that is on. Messages say
// only the run, the event and the next step, never findings text: an ntfy
// topic can be read by anyone who knows its name.
func (n Notifier) Send(ctx context.Context, title, message string) {
	if n.Bell && n.Out != nil {
		fmt.Fprint(n.Out, "\a")
	}
	if n.Toast && n.GOOS == "windows" && n.Proc != nil {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		r := n.Proc.Run(cctx, proc.Cmd{Name: "powershell.exe", Args: ToastArgs(title, message)})
		cancel()
		if r.Err != nil {
			n.warn("Can't show a Windows toast: " + firstLine(r.Stderr, r.Err))
		}
	}
	if n.NtfyServer != "" && n.NtfyTopic != "" {
		if err := n.push(ctx, title, message); err != nil {
			n.warn("Can't send the phone notification (ntfy): " + err.Error())
		}
	}
}

func (n Notifier) warn(msg string) {
	if n.Out != nil {
		ui.Line(n.Out, ui.Warn, msg)
	}
}

// ToastArgs runs a toast script in Windows PowerShell 5.1, whose
// WindowsRuntime types show a toast without extra modules (PowerShell 7
// lacks them). The script goes as -EncodedCommand, so no quoting can break
// it. The toast appears as Windows PowerShell's.
func ToastArgs(title, message string) []string {
	xml := fmt.Sprintf(`<toast><visual><binding template="ToastGeneric"><text>%s</text><text>%s</text></binding></visual></toast>`,
		html.EscapeString(title), html.EscapeString(message))
	script := strings.Join([]string{
		"$ErrorActionPreference = 'Stop'",
		"$ProgressPreference = 'SilentlyContinue'",
		"[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] | Out-Null",
		"[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType = WindowsRuntime] | Out-Null",
		"$x = New-Object Windows.Data.Xml.Dom.XmlDocument",
		"$x.LoadXml('" + strings.ReplaceAll(xml, "'", "''") + "')",
		"$t = New-Object Windows.UI.Notifications.ToastNotification $x",
		`[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe').Show($t)`,
	}, "\n")
	units := utf16.Encode([]rune(script))
	b := make([]byte, 2*len(units))
	for i, u := range units {
		b[2*i], b[2*i+1] = byte(u), byte(u>>8)
	}
	return []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-EncodedCommand", base64.StdEncoding.EncodeToString(b)}
}

// push posts the message to the ntfy topic: the body is the message, and
// headers carry the title.
func (n Notifier) push(ctx context.Context, title, message string) error {
	client := n.HTTP
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	url := strings.TrimRight(n.NtfyServer, "/") + "/" + n.NtfyTopic
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(message))
	if err != nil {
		return err
	}
	req.Header.Set("Title", title)
	req.Header.Set("Tags", "robot")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("the server answered %s", resp.Status)
	}
	return nil
}

func firstLine(stderr string, err error) string {
	for _, l := range strings.Split(stderr, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#< CLIXML") && !strings.HasPrefix(l, "<Objs") {
			return l
		}
	}
	return err.Error()
}
