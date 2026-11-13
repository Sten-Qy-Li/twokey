package doctor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Sten-Qy-Li/twokey/internal/config"
	"github.com/Sten-Qy-Li/twokey/internal/engine"
	"github.com/Sten-Qy-Li/twokey/internal/gitx"
	"github.com/Sten-Qy-Li/twokey/internal/paths"
	"github.com/Sten-Qy-Li/twokey/internal/runner"
	"github.com/Sten-Qy-Li/twokey/internal/schema"
	"github.com/Sten-Qy-Li/twokey/internal/ui"
)

const smokeTimeout = 10 * time.Minute

// probe is a file in an extra read folder that the agents must read.
type probe struct {
	dir, file, hex string
}

// smoke makes one small real call to each agent and resumes it once (SPEC
// section 12). It checks what --help can't show: the schema options, that
// Codex's sandbox can write to the scratch folder and reach the network,
// that a resumed session keeps its sandbox, that both agents can read the
// extra read folders, and that the arguments that turn off screen and
// browser control are accepted. It checks the files, not the answers.
func (d *doctor) smoke(root string, cfg *config.Config) {
	d.section("Smoke test (real calls)")
	ui.Line(d.Out, ui.Busy, "Each agent gets one small task and one resumed follow-up. This takes a few minutes and uses a little of your plan.")
	stamp := time.Now().UTC().Format("20060102T150405Z")
	scratchRoot, stagingRoot := d.ScratchRoot, d.StagingRoot
	if scratchRoot == "" {
		scratchRoot = paths.ScratchRoot()
	}
	if stagingRoot == "" {
		s, err := paths.StagingRoot()
		if err != nil {
			d.fail("Can't find the staging folder: " + err.Error())
			return
		}
		stagingRoot = s
	}
	staging := filepath.Join(stagingRoot, "smoke-"+stamp)
	probes := d.probes(cfg.Project.ExtraReadDirs)
	repo := gitx.Repo{Dir: root, R: d.Runner}
	before, _ := repo.Status(d.ctx)

	x := runner.NewReal(cfg, d.Runner)
	if d.Launch != nil {
		x.Launch = d.Launch
	}
	for _, agent := range []string{"codex", "claude"} {
		scratch := filepath.Join(scratchRoot, "smoke-"+stamp, agent)
		if err := errors.Join(os.MkdirAll(scratch, 0o755), os.MkdirAll(staging, 0o755)); err != nil {
			d.fail("Can't create the smoke test's folders: " + err.Error())
			return
		}
		d.smokeAgent(x, cfg, root, agent, scratch, staging, probes)
	}

	if after, err := repo.Status(d.ctx); err == nil && after != before {
		d.fail("An agent changed the repository during the smoke test. git status now shows:"+indentLines(after),
			"look at the changes and undo them.")
	} else if err == nil {
		d.ok("The repository is unchanged")
	}
	ui.Detail(d.Out, "The agents' raw output: "+staging)
}

func (d *doctor) smokeAgent(x *runner.Real, cfg *config.Config, root, agent, scratch, staging string, probes []probe) {
	name := "Codex"
	network := agent == "codex" && cfg.Codex.Network
	if agent == "claude" {
		name = "Claude Code"
	}
	pushFile := filepath.Join(scratch, agent+"-push.txt")
	req := runner.Request{Agent: agent, Name: "smoke-" + agent, Step: schema.Smoke, Attempt: 1,
		Prompt:  smokePrompt(filepath.Join(scratch, agent+"-1.txt"), pushFile, cfg.Project.Remote, network, probes),
		RepoDir: root, ScratchDir: scratch, OutBase: filepath.Join(staging, agent+".1"),
		Env: gitx.PushBlockEnv(os.Getenv)} // the same push block as in runs (D62)
	if agent == "claude" {
		req.SessionID = uuid()
	}
	ui.Line(d.Out, ui.Busy, name+": first call")
	res := d.smokeRun(x, req)
	first, ok := d.smokeAnswer(name, res)
	if !ok {
		return
	}

	wrote := fileHas(filepath.Join(scratch, agent+"-1.txt"))
	switch {
	case wrote:
		d.ok(name + " wrote a file to its scratch folder")
	case agent == "codex":
		d.fail(fmt.Sprintf("Codex couldn't write to its scratch folder with [codex] sandbox = %q.", cfg.Codex.Sandbox),
			"this is a known problem of Codex on Windows (openai/codex issue 34958). Update Codex, or read the issue for workarounds.")
	default:
		d.fail("Claude Code couldn't write to its scratch folder, which twokey passes with --add-dir.",
			"check [claude] permission_mode, and the project's .claude/settings.json deny rules.")
	}
	if network {
		if first.HTTPStatus >= 200 && first.HTTPStatus < 400 {
			d.ok(fmt.Sprintf("Codex reached the network (HTTP %d)", first.HTTPStatus))
		} else {
			d.fail(fmt.Sprintf("Codex couldn't reach the network (HTTP %d). Its note: %s", first.HTTPStatus, orNone(first.Note)),
				"check [codex] network and Codex's sandbox settings. Audits need the network for the requirements' sources.")
		}
	}
	d.checkReads(name, probes, first.Reads)
	d.checkPushBlock(name, agent, pushFile)
	d.smokeReport(name, agent, cfg, res)

	if res.SessionID == "" {
		d.fail(name+" reported no session ID, so twokey can't resume its sessions.", "update "+name+".")
		return
	}
	ui.Line(d.Out, ui.Busy, name+": resumed call")
	req.Attempt, req.Resume, req.SessionID = 2, res.SessionID, ""
	req.Prompt = smokeResumePrompt(filepath.Join(scratch, agent+"-2.txt"))
	req.OutBase = filepath.Join(staging, agent+".2")
	if _, ok := d.smokeAnswer(name+" (resumed)", d.smokeRun(x, req)); !ok {
		return
	}
	if fileHas(filepath.Join(scratch, agent+"-2.txt")) {
		d.ok("A resumed " + name + " session keeps its sandbox")
	} else {
		d.fail("A resumed "+name+" session couldn't write to the scratch folder.",
			"twokey can't retry a step in the same session. Report this with the raw output shown below.")
	}
}

func (d *doctor) smokeRun(x *runner.Real, req runner.Request) runner.Result {
	ctx, cancel := context.WithTimeout(d.ctx, smokeTimeout)
	defer cancel()
	return x.Run(ctx, req)
}

// smokeAnswer checks that the call answered with JSON that fits the schema.
func (d *doctor) smokeAnswer(name string, res runner.Result) (schema.SmokeResult, bool) {
	var a schema.SmokeResult
	switch {
	case res.TimedOut:
		d.fail(fmt.Sprintf("%s didn't finish within %d minutes.", name, int(smokeTimeout.Minutes())), "try again; check your network.")
		return a, false
	case res.Limit != nil:
		d.fail(name+" hit a usage limit: "+res.Limit.Message, "try again once the limit has reset.")
		return a, false
	case res.Problem != "":
		d.fail(name+" didn't answer: "+res.Problem, "check that "+name+" works and is logged in, then try again.")
		return a, false
	}
	if probs := schema.Decode(schema.Smoke, res.Answer, &a); probs != nil {
		d.fail(name+"'s answer doesn't fit the schema: "+strings.Join(probs, "; "), "twokey needs schema-constrained answers. Update "+name+".")
		return a, false
	}
	d.ok(name + " answered with JSON that fits the schema")
	return a, true
}

// checkReads compares what the agent read with the files' real bytes.
func (d *doctor) checkReads(name string, probes []probe, reads []schema.SmokeRead) {
	got := map[string]string{}
	for _, r := range reads {
		got[filepath.Clean(r.Path)] = strings.ToLower(strings.TrimSpace(r.FirstBytesHex))
	}
	for _, p := range probes {
		if h := got[filepath.Clean(p.file)]; h == p.hex {
			d.ok(fmt.Sprintf("%s can read %s", name, p.dir))
		} else {
			d.fail(fmt.Sprintf("%s couldn't read %s (its first bytes are %s; the answer said %q).", name, p.file, p.hex, h),
				"the requirements may need that folder. Check that the folder can be read by your user account.")
		}
	}
}

// smokeReport shows the model, the screen and browser setting, denials
// and cost.
func (d *doctor) smokeReport(name, agent string, cfg *config.Config, res runner.Result) {
	label := engine.Label(cfg, agent, res.Model)
	switch {
	case agent == "codex" && cfg.Codex.Model == "" && res.Model == "":
		d.ok(fmt.Sprintf("Codex doesn't report its model, so log headings use [codex] label: %q", cfg.Codex.Label))
	default:
		d.ok(fmt.Sprintf("%s ran %s. Log headings: %q", name, orNone(res.Model), label))
	}
	for _, n := range res.Notes {
		d.warn(n)
	}
	desktop := cfg.Codex.DesktopTools
	if agent == "claude" {
		desktop = cfg.Claude.DesktopTools
	}
	if !desktop {
		d.ok(name + " accepted the options that turn off screen and browser control")
	}
	for _, den := range res.Denials {
		d.warn(fmt.Sprintf("%s was refused: %s", name, den))
	}
	if res.Usage.CostUSD > 0 {
		ui.Detail(d.Out, fmt.Sprintf("Estimated cost of this call: $%.2f", res.Usage.CostUSD))
	}
}

// probes picks one file in each extra read folder and reads its first
// bytes, so the agents' answers can be checked.
func (d *doctor) probes(dirs []string) []probe {
	var out []probe
	for _, dir := range dirs {
		file := firstFile(dir)
		if file == "" {
			d.warn("No file to test in " + dir + ", so the smoke test can't check that the agents read it.")
			continue
		}
		f, err := os.Open(file)
		if err != nil {
			d.warn("Can't read " + file + ": " + err.Error())
			continue
		}
		b := make([]byte, 4)
		n, _ := io.ReadFull(f, b)
		_ = f.Close()
		out = append(out, probe{dir: dir, file: file, hex: hex.EncodeToString(b[:n])})
	}
	return out
}

// firstFile returns a non-empty file in dir, searching at most 2000 entries.
func firstFile(dir string) string {
	var found string
	seen := 0
	errDone := errors.New("done")
	_ = filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if seen++; err != nil || seen > 2000 {
			return errDone
		}
		if e.Type().IsRegular() {
			if fi, err := e.Info(); err == nil && fi.Size() >= 4 {
				found = path
				return errDone
			}
		}
		return nil
	})
	return found
}

// checkPushBlock looks at the push URL the agent's own git command printed:
// twokey's push block must reach the commands agents run (D62).
func (d *doctor) checkPushBlock(name, agent, file string) {
	data, err := os.ReadFile(file)
	url := strings.TrimSpace(string(data))
	switch {
	case err != nil || url == "":
		d.fail(name+" didn't write its git push URL, so twokey can't tell whether its commands can push.",
			"run twokey doctor --smoke again. If this keeps failing, report it with the raw output shown below.")
	case gitx.PushBlocked(url):
		d.ok(name + "'s commands can't push: twokey's push block reaches them")
	case agent == "codex":
		d.fail("Codex's commands can still push: their git push URL is "+url+".",
			"check shell_environment_policy in your Codex config: it must pass GIT_CONFIG_COUNT, GIT_CONFIG_KEY_* and GIT_CONFIG_VALUE_* to commands.")
	default:
		d.fail(name+"'s commands can still push: their git push URL is "+url+".",
			"don't run twokey unattended until this is solved. Report it with the raw output shown below.")
	}
}

func smokePrompt(file, pushFile, remote string, network bool, probes []probe) string {
	var b strings.Builder
	b.WriteString("This is a smoke test by twokey, a tool that runs you in unattended steps. Do exactly the following, change nothing else, and answer with only the JSON object that the schema asks for. Nobody can answer questions.\n\n")
	fmt.Fprintf(&b, "1. Create the file %s containing the text: ok\n   Set \"wrote\" to true if that worked.\n", file)
	if network {
		b.WriteString("2. Make one HTTPS GET request to https://example.com/ and put the HTTP status code in \"http_status\". If curl or PowerShell can't make the request in your sandbox, use Node.js instead (for example node -e \"fetch('https://example.com/').then(r => console.log(r.status))\"). Use 0 if nothing worked, and say in \"note\" what you tried.\n")
	} else {
		b.WriteString("2. Don't use the network. Set \"http_status\" to 0.\n")
	}
	if len(probes) == 0 {
		b.WriteString("3. Set \"reads\" to [].\n")
	} else {
		b.WriteString("3. For each file below, read its first 4 bytes and put them in \"reads\" as lowercase hex (8 characters), with the path exactly as given:\n")
		for _, p := range probes {
			fmt.Fprintf(&b, "   - %s\n", p.file)
		}
	}
	fmt.Fprintf(&b, "4. In the repository, run this command and write its output, exactly, into the file %s: git remote get-url --push %s\n", pushFile, remote)
	b.WriteString("5. In \"note\", say briefly what went wrong, or write \"\".\n")
	return b.String()
}

func smokeResumePrompt(file string) string {
	return fmt.Sprintf("Second part of the smoke test, in the same session: create the file %s containing the text: ok\n"+
		"Then answer with the JSON object again: \"wrote\" true if that worked, \"http_status\" 0, \"reads\" [], and \"note\".\n", file)
}

func fileHas(path string) bool {
	b, err := os.ReadFile(path)
	return err == nil && strings.Contains(strings.ToLower(string(b)), "ok")
}

func uuid() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func orNone(s string) string {
	if s == "" {
		return "(nothing)"
	}
	return s
}

func indentLines(s string) string {
	return "\n        " + strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n        ")
}
