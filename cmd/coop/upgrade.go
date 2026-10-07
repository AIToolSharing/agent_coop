package main

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/AIToolSharing/agent_coop/internal/config"
)

// binaryName is the name of the release binary of this device, as the hub has it at /dl/.
func binaryName() string {
	name := "coop-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

// inClone reports a binary that `make build` wrote: dist/coop in a clone of the repository.
// `make install` links or copies that one; an upgrade from the hub would put another version
// into the clone.
func inClone(exe string) bool {
	dist := filepath.Dir(exe)
	_, err := os.Stat(filepath.Join(filepath.Dir(dist), "go.mod"))
	return filepath.Base(dist) == "dist" && err == nil
}

// download writes the release binary of this device, as the hub gives it, to a new file in
// dir. The caller removes the file.
func download(ctx context.Context, client *http.Client, base, token, dir string) (path string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/dl/"+binaryName(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("cannot reach %s: %w", base, err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&e)
		return "", fmt.Errorf("%s did not give %s: %s", base, binaryName(), cmp.Or(e.Message, res.Status))
	}
	// The new file has the extension of the binary: Windows starts only a program that has it.
	f, err := os.CreateTemp(dir, ".coop-new-*"+filepath.Ext(binaryName()))
	if err != nil {
		return "", fmt.Errorf("cannot write to %s, the directory of coop: %w", dir, err)
	}
	path = f.Name()
	if _, err = io.Copy(f, res.Body); err == nil {
		err = f.Chmod(0o755)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return path, fmt.Errorf("the download of %s failed: %w", binaryName(), err)
	}
	return path, nil
}

// cmdUpgrade gives this device the version of its hub: it downloads the binary of the device
// from the hub, puts it in the place of this one, and runs the setup of the new binary. The
// hub is the only source, so a device and its hub cannot have different versions after it.
func cmdUpgrade(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "usage: coop upgrade")
		return 2
	}
	cfg := config.Load(environ(), config.DefaultEnvFile(), func(string) {}, cwd())
	token := cmp.Or(cfg.Token, cfg.OperatorToken, cfg.OrchestratorToken)
	if cfg.URL == "" || token == "" {
		fmt.Fprintln(stderr, "no hub address or no token: run coop login <url> <token>")
		return 1
	}
	fail := func(err error) int {
		fmt.Fprintln(stderr, "coop upgrade:", err)
		return 1
	}
	client := hubHTTP(cfg, stderr)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	me, err := whoami(ctx, client, cfg.URL, token)
	switch {
	case err != nil:
		return fail(err)
	case me.Version == "":
		return fail(fmt.Errorf("%s is a hub of a version that gives no binaries: upgrade the hub first", cfg.URL))
	case me.Version == version:
		fmt.Fprintf(stdout, "coop %s is the version of the hub\n", version)
		return 0
	}
	exe := executable()
	if inClone(exe) {
		return fail(fmt.Errorf("%s is a build in a clone of the repository: run git pull and make install there", exe))
	}
	fmt.Fprintf(stdout, "download %s/dl/%s\n", cfg.URL, binaryName())
	fresh, err := download(ctx, client, cfg.URL, token, filepath.Dir(exe))
	if fresh != "" {
		// After the rename there is no such file, and the removal does nothing.
		defer os.Remove(fresh)
	}
	if err != nil {
		return fail(err)
	}
	// The file must run on this device, and be the version of the hub, before it takes the
	// place of this binary.
	out, err := exec.Command(fresh, "version").Output()
	if err != nil {
		return fail(fmt.Errorf("the binary from the hub does not run on this device: %w", err))
	}
	if got := strings.TrimSpace(string(out)); got != "coop "+me.Version {
		return fail(fmt.Errorf("the hub is %s, but its binary for this device is %q: put the release binaries on the hub again (make hub)", me.Version, got))
	}
	if runtime.GOOS == "windows" {
		// Windows does not replace a program that runs, but it lets the program get a new name.
		// The old file goes at a later upgrade, when nothing runs it.
		olds, _ := filepath.Glob(exe + ".old*")
		for _, old := range olds {
			_ = os.Remove(old)
		}
		if err := os.Rename(exe, fmt.Sprintf("%s.old.%d", exe, os.Getpid())); err != nil {
			return fail(err)
		}
	}
	if err := os.Rename(fresh, exe); err != nil {
		return fail(err)
	}
	fmt.Fprintf(stdout, "coop %s -> %s in %s\n", version, me.Version, exe)
	// The hooks and the skill come from the binary: the new one writes its own.
	if _, err := lookPath("claude"); err != nil {
		fmt.Fprintln(stdout, "Claude Code is not on the PATH: no setup")
	} else {
		setup := exec.Command(exe, "setup")
		setup.Stdout, setup.Stderr = stdout, stderr
		if err := setup.Run(); err != nil {
			return fail(fmt.Errorf("the new binary is in place, but its setup failed: %w", err))
		}
	}
	fmt.Fprintln(stdout, "Start each agent and coop tui again: a process that runs keeps the old version.")
	return 0
}
