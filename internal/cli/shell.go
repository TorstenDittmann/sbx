package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"

	"golang.org/x/term"

	"github.com/TorstenDittmann/sbx/internal/ws"
)

const shellUsage = "usage: sbx shell [--cwd DIR] [--env K=V] ID [-- CMD [ARGS...]]"

// defaultShell prefers bash and falls back to sh.
var defaultShell = []string{"/bin/sh", "-c", "if command -v bash >/dev/null 2>&1; then exec bash -l; fi; exec sh -l"}

// shellCmd opens an interactive terminal in a sandbox. It needs sbx-agent.
func shellCmd(args []string) error {
	fs := newFlags("shell")
	conn := connFlags(fs)
	cwd := fs.String("cwd", "", "working directory")
	env := kvFlag{}
	fs.Var(env, "env", "environment variable KEY=VALUE (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return errors.New(shellUsage)
	}
	id, cmd := rest[0], rest[1:]
	if len(cmd) > 0 && cmd[0] == "--" {
		cmd = cmd[1:]
	}
	if len(cmd) == 0 {
		cmd = defaultShell
	}
	if _, ok := env["TERM"]; !ok {
		env["TERM"] = termName()
	}
	c, err := conn()
	if err != nil {
		return err
	}
	fd := int(os.Stdin.Fd())
	tty := term.IsTerminal(fd)
	cols, rows := 80, 24
	if tty {
		if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
			cols, rows = w, h
		}
	}
	body := map[string]any{"cmd": cmd, "env": env, "cols": cols, "rows": rows}
	if *cwd != "" {
		body["cwd"] = remotePath(*cwd)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := c.json(http.MethodPost, sandboxPath(id, "terminals"), nil, body, &created); err != nil {
		return err
	}
	sock, err := ws.DialHeader(c.wsURL(sandboxPath(id, "terminals", created.ID, "ws")), http.Header{"Authorization": {"Bearer " + c.key}})
	if err != nil {
		return fmt.Errorf("terminal: %w", err)
	}
	defer sock.Close()
	if tty {
		state, err := term.MakeRaw(fd)
		if err != nil {
			return err
		}
		defer term.Restore(fd, state)
		stop := onResize(func() {
			w, h, err := term.GetSize(int(os.Stdout.Fd()))
			if err != nil {
				return
			}
			b, _ := json.Marshal(map[string]any{"type": "resize", "cols": w, "rows": h})
			_ = sock.Write(ws.OpText, b)
		})
		defer stop()
	}
	return pumpTerminal(sock, stdin, stdout)
}

func termName() string {
	if t := os.Getenv("TERM"); t != "" {
		return t
	}
	return "xterm-256color"
}

// pumpTerminal copies stdin to the socket and the socket to stdout until
// the process exits or the socket closes.
func pumpTerminal(sock *ws.Conn, in io.Reader, out io.Writer) error {
	go func() {
		buf := make([]byte, 32<<10)
		for {
			n, err := in.Read(buf)
			if n > 0 {
				if werr := sock.Write(ws.OpBinary, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	for {
		op, data, err := sock.Read()
		if errors.Is(err, io.EOF) && op == ws.OpClose {
			return nil
		}
		if err != nil {
			return fmt.Errorf("terminal connection lost: %w", err)
		}
		switch op {
		case ws.OpBinary:
			if _, err := out.Write(data); err != nil {
				return err
			}
		case ws.OpText:
			var msg struct {
				Type     string `json:"type"`
				ExitCode int    `json:"exit_code"`
			}
			if json.Unmarshal(data, &msg) == nil && msg.Type == "exit" {
				if msg.ExitCode != 0 {
					return &exitError{code: msg.ExitCode}
				}
				return nil
			}
		}
	}
}
