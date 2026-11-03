package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const watchUsage = "usage: sbx watch [-r] [-n COUNT] [--json] ID [PATH]"

// watchCmd prints filesystem changes in a sandbox as they happen, one per
// line, until the stream ends, COUNT changes arrive, or it is interrupted.
func watchCmd(args []string) error {
	fs := newFlags("watch")
	conn := connFlags(fs)
	recursive := fs.Bool("r", false, "watch subdirectories too")
	count := fs.Int("n", 0, "exit after this many changes (0 watches until interrupted)")
	asJSON := fs.Bool("json", false, "print each change as a JSON object")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) < 1 || len(rest) > 2 || *count < 0 {
		return errors.New(watchUsage)
	}
	id, path := rest[0], "/workspace"
	if len(rest) == 2 {
		path = remotePath(rest[1])
	}
	c, err := conn()
	if err != nil {
		return err
	}
	q := url.Values{"path": {path}}
	if *recursive {
		q.Set("recursive", "1")
	}
	resp, err := c.do(http.MethodGet, sandboxPath(id, "files", "watch"), q, nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return readWatchStream(resp.Body, *count, *asJSON)
}

// readWatchStream prints change events. The ready event is reported on
// stderr so stdout holds only changes and stays easy to pipe.
func readWatchStream(r io.Reader, count int, asJSON bool) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	event, seen := "", 0
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			event = ""
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data := []byte(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			switch event {
			case "ready":
				var ready struct {
					Path string `json:"path"`
				}
				_ = json.Unmarshal(data, &ready)
				fmt.Fprintf(stderr, "watching %s\n", ready.Path)
			case "change":
				var ch struct {
					Type string `json:"type"`
					Path string `json:"path"`
				}
				if err := json.Unmarshal(data, &ch); err != nil {
					return err
				}
				if asJSON {
					b, _ := json.Marshal(ch)
					fmt.Fprintf(stdout, "%s\n", b)
				} else {
					fmt.Fprintf(stdout, "%s\t%s\n", ch.Type, ch.Path)
				}
				seen++
				if count > 0 && seen >= count {
					return nil
				}
			case "error":
				var e struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				}
				_ = json.Unmarshal(data, &e)
				return &apiError{Code: e.Code, Message: e.Message}
			}
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return errors.New("the watch stream ended")
}
