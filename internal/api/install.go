package api

import (
	_ "embed"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// InstallScript is the script that a device runs one time to install coop from its hub. The
// hub serves it at /install.sh.
//
//go:embed install.sh
var InstallScript []byte

// binaryRE is the name of a release binary: coop-<os>-<arch>, with .exe on Windows. A name
// that matches has no path separator, so it is a file of the directory Dist and no other.
var binaryRE = regexp.MustCompile(`^coop-[a-z0-9]+-[a-z0-9]+(\.exe)?$`)

// install serves what a device needs to install coop: the script, with no token, and the
// binaries of the hub's version, to each valid token. It reports whether the path is one of
// these.
func (s *Server) install(w http.ResponseWriter, r *http.Request) bool {
	name, binary := strings.CutPrefix(r.URL.Path, "/dl/")
	if r.URL.Path != "/install.sh" && !binary {
		return false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeError(w, 405, "invalid", "method not allowed")
		return true
	}
	if !binary {
		w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
		_, _ = w.Write(InstallScript)
		return true
	}
	if _, err := s.auth(r, kindAny); err != nil {
		writeHubError(w, err)
		return true
	}
	missing := func() {
		writeError(w, 404, "not_found", "the hub has no binary "+strings.ToValidUTF8(name, "?")+": copy the release binaries to the hub (make hub)")
	}
	if s.Dist == "" || !binaryRE.MatchString(name) {
		missing()
		return true
	}
	f, err := os.Open(filepath.Join(s.Dist, name))
	if err != nil {
		missing()
		return true
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		missing()
		return true
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, "", info.ModTime(), f)
	return true
}
