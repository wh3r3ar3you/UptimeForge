// Command fakeagent stands in for the agent in the launcher tests. Its
// identity is the name of the directory it was copied into (a version under
// bin/, or "builtin"); it logs "<identity> launcher=<UF_LAUNCHER>
// builtin=<UF_LAUNCHER_BUILTIN>" to a file and then behaves as the spec says
// for that identity.
//
//	fakeagent <log file> <state dir> <identity>=<mode>[,...]
//
// Modes: hang, connect (write connected-<identity>, then hang), exit1,
// exit75, upgrade:<version> (make <version> current, then exit 75).
package main

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	exe, err := os.Executable()
	if err != nil || len(os.Args) != 4 {
		os.Exit(3)
	}
	id := filepath.Base(filepath.Dir(exe))
	logPath, stateDir := os.Args[1], os.Args[2]
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		os.Exit(3)
	}
	f.WriteString(id + " launcher=" + os.Getenv("UF_LAUNCHER") + " builtin=" + os.Getenv("UF_LAUNCHER_BUILTIN") + "\n")
	f.Close()

	mode := "hang"
	for _, part := range strings.Split(os.Args[3], ",") {
		if k, v, ok := strings.Cut(part, "="); ok && k == id {
			mode = v
		}
	}
	switch {
	case mode == "exit1":
		os.Exit(1)
	case mode == "exit75":
		os.Exit(75)
	case mode == "connect":
		os.WriteFile(filepath.Join(stateDir, "connected-"+id), nil, 0o644)
	case strings.HasPrefix(mode, "upgrade:"):
		os.WriteFile(filepath.Join(stateDir, "current"), []byte(strings.TrimPrefix(mode, "upgrade:")+"\n"), 0o644)
		os.Exit(75)
	}
	time.Sleep(time.Hour)
}
