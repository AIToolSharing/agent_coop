package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/AIToolSharing/agent_coop/internal/wire"
)

// WriteProjectFile writes the .coop file in dir for the session command. The file holds the
// session and, when agent is not empty, the agent name. Agents that start in dir or below then
// join the session. WriteProjectFile also adds .coop to the .gitignore file at the git root,
// one time only: a session belongs to the person, not to the project.
//
// It returns the path of the .coop file, and the path of the .gitignore file when it changed
// that file (else ""). It checks the two names before it writes a file.
func WriteProjectFile(dir, session, agent string) (file, gitignore string, err error) {
	if !wire.IsToken(session) {
		return "", "", fmt.Errorf("%q is not a valid session name: a-z, 0-9, _ and -, up to 64", session)
	}
	if agent != "" && !wire.IsAgentName(agent) {
		return "", "", fmt.Errorf("%q is not a valid agent name: a-z, 0-9, _ and -, up to 64", agent)
	}
	text := "COOP_SESSION=" + session + "\n"
	if agent != "" {
		text += "COOP_AGENT=" + agent + "\n"
	}
	file = filepath.Join(dir, ProjectFile)
	if err := os.WriteFile(file, []byte(text), 0o666); err != nil {
		return "", "", err
	}
	root, ok := FindGitRoot(dir)
	if !ok {
		return file, "", nil
	}
	gitignore, err = ignoreProjectFile(root)
	return file, gitignore, err
}

// ignoreProjectFile adds a .coop line to the .gitignore file at root, when no line in it is
// .coop or /.coop. It creates the file when it is not there. It returns the path when it
// changed the file.
func ignoreProjectFile(root string) (string, error) {
	path := filepath.Join(root, ".gitignore")
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	text := string(b)
	for _, line := range strings.Split(text, "\n") {
		if l := trim(line); l == ProjectFile || l == "/"+ProjectFile {
			return "", nil
		}
	}
	sep := ""
	if text != "" && !strings.HasSuffix(text, "\n") {
		sep = "\n"
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o666)
	if err != nil {
		return "", err
	}
	_, err = f.WriteString(sep + ProjectFile + "\n")
	if err := errors.Join(err, f.Close()); err != nil {
		return "", err
	}
	return path, nil
}
