// Package herdr talks to Herdr, a terminal workspace manager for coding agents. Herdr runs
// each agent in a pane and gives the pane's processes the pane id in the environment. coop
// uses the `herdr` command for three things: to interrupt the agent in its own pane when the
// operator pauses or stops it, to show the coop identity and gate of the agent on the pane,
// and to bring an agent's pane to the front from the operator's TUI.
//
// Everything here is best effort: with no Herdr, or with a Herdr that fails, coop works as
// before.
package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// source names coop to Herdr as the reporter of pane metadata.
const source = "coop:shim"

// timeout limits one call of the herdr command.
const timeout = 5 * time.Second

// Runner runs the herdr command with args and gives its standard output.
type Runner func(ctx context.Context, args ...string) ([]byte, error)

// Binary gives the herdr binary to run: HERDR_BIN_PATH when Herdr gave one, else `herdr` from
// the PATH, else ~/.local/bin/herdr. The last one is where Herdr installs itself on a machine
// that it reaches over SSH, and a pane's shell there may not have that directory on its PATH
// (seen with Herdr 0.9.3: "the remote shell does not resolve herdr to that path").
func Binary(env map[string]string, exists func(path string) bool) string {
	if bin := env["HERDR_BIN_PATH"]; bin != "" {
		return bin
	}
	if _, err := exec.LookPath("herdr"); err == nil {
		return "herdr"
	}
	if home := env["HOME"]; home != "" {
		if local := filepath.Join(home, ".local", "bin", "herdr"); exists(local) {
			return local
		}
	}
	return "herdr"
}

// Command is the Runner that runs the herdr binary (see Binary).
func Command(env map[string]string) Runner {
	bin := Binary(env, func(path string) bool {
		info, err := os.Stat(path)
		return err == nil && !info.IsDir()
	})
	return func(ctx context.Context, args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		out, err := exec.CommandContext(ctx, bin, args...).Output()
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			// Herdr writes its error as one JSON line on standard error.
			return out, fmt.Errorf("herdr %s: %s", args[0], strings.TrimSpace(string(exit.Stderr)))
		}
		return out, err
	}
}

var paneRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:._-]{0,63}$`)

// IsPaneID reports whether s can be a Herdr pane id, for example w1:p3. The ids are opaque;
// the check only keeps other text out of a command line and out of the hub. An id does not
// start with "-": the herdr command would read it as an option.
func IsPaneID(s string) bool { return paneRE.MatchString(s) }

// Pane is the Herdr pane that this process runs in. A nil Pane is "not in Herdr": every
// method then does nothing.
type Pane struct {
	ID  string
	run Runner
}

// FromEnv gives the pane of this process, or nil when Herdr does not manage it.
func FromEnv(env map[string]string, run Runner) *Pane {
	id := env["HERDR_PANE_ID"]
	if env["HERDR_ENV"] != "1" || !IsPaneID(id) {
		return nil
	}
	return &Pane{ID: id, run: run}
}

// PaneID gives the pane id, or "" for a nil pane.
func (p *Pane) PaneID() string {
	if p == nil {
		return ""
	}
	return p.ID
}

// status gives the state that Herdr sees for the agent in the pane: idle, working, blocked,
// done or unknown.
func (p *Pane) status(ctx context.Context) (string, error) {
	out, err := p.run(ctx, "agent", "get", p.ID)
	if err != nil {
		return "", err
	}
	var res struct {
		Result struct {
			Agent struct {
				Status string `json:"agent_status"`
			} `json:"agent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return "", fmt.Errorf("herdr agent get: %w", err)
	}
	return res.Result.Agent.Status, nil
}

// Interrupt stops the turn of the agent in the pane, as the Escape key does, when Herdr says
// that the agent works. It gives true when it sent the key. An agent that is idle, or that
// shows a question to its human, gets no key: Escape would clear what the human typed, or
// answer the question.
func (p *Pane) Interrupt(ctx context.Context) (bool, error) {
	if p == nil {
		return false, nil
	}
	st, err := p.status(ctx)
	if err != nil || st != "working" {
		return false, err
	}
	_, err = p.run(ctx, "agent", "send-keys", p.ID, "esc")
	return err == nil, err
}

// Show puts the coop identity and the gate of the agent on the pane: the pane title, and the
// tokens `coop` and `gate`, which a Herdr sidebar row can show as $coop and $gate. gate "run"
// shows no gate.
func (p *Pane) Show(ctx context.Context, session, agent, gate string) error {
	if p == nil {
		return nil
	}
	title := "coop " + session + "/" + agent
	if gate != "" && gate != "run" {
		title += " · " + gate
	}
	// The pane id comes first: herdr 0.9.3 reads an id after the options as an option.
	_, err := p.run(ctx, "pane", "report-metadata", p.ID, "--source", source,
		"--title", title, "--token", "coop="+session+"/"+agent, "--token", "gate="+gate)
	return err
}

// Clear takes the coop title and tokens off the pane.
func (p *Pane) Clear(ctx context.Context) error {
	if p == nil {
		return nil
	}
	_, err := p.run(ctx, "pane", "report-metadata", p.ID, "--source", source,
		"--clear-title", "--clear-token", "coop", "--clear-token", "gate")
	return err
}

// Focus brings the pane of an agent to the front. machine "" means the Herdr of this machine;
// another value is the label of a saved SSH machine in Herdr.
func Focus(ctx context.Context, run Runner, machine, pane string) error {
	if !IsPaneID(pane) {
		return fmt.Errorf("%q is not a herdr pane", pane)
	}
	args := []string{"agent", "focus", pane}
	if machine != "" {
		args = append([]string{"--machine", machine}, args...)
	}
	_, err := run(ctx, args...)
	return err
}
