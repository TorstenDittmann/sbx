package cli

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// stdout and stderr are swapped out by tests.
var (
	stdout io.Writer = os.Stdout
	stderr io.Writer = os.Stderr
	stdin  io.Reader = os.Stdin
)

// kvFlag collects repeated KEY=VALUE flags.
type kvFlag map[string]string

func (f kvFlag) String() string { return "" }

func (f kvFlag) Set(s string) error {
	k, v, ok := strings.Cut(s, "=")
	if !ok || k == "" {
		return fmt.Errorf("%q is not KEY=VALUE", s)
	}
	f[k] = v
	return nil
}

// listFlag collects a repeated flag.
type listFlag []string

func (f *listFlag) String() string { return strings.Join(*f, ",") }

func (f *listFlag) Set(s string) error {
	*f = append(*f, s)
	return nil
}

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// parseMixed parses flags that may come before, between, or after the
// positional arguments, like most CLIs. Everything after "--" is positional.
func parseMixed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return pos, nil
		}
		if len(args) > 0 && len(rest) < len(args) && args[len(args)-len(rest)-1] == "--" {
			return append(pos, rest...), nil
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

// durationSeconds accepts "300", "5m", or "1h30m".
func durationSeconds(s string) (int, error) {
	if n, err := strconv.Atoi(s); err == nil {
		return n, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q (use seconds or a value like 5m)", s)
	}
	return int(d / time.Second), nil
}

const createUsage = "usage: sbx create [--template NAME] [--image IMAGE] [--ttl 5m] [--network none|full] [--env K=V] [--label K=V] [--json]"

func createCmd(args []string) error {
	fs := newFlags("create")
	conn := connFlags(fs)
	template := fs.String("template", "", "template name")
	image := fs.String("image", "", "image reference")
	backend := fs.String("backend", "", "docker, gvisor, kata, or firecracker")
	ttl := fs.String("ttl", "", "lifetime, for example 300 or 5m")
	idle := fs.String("idle-timeout", "", "stop after this long without activity")
	cpu := fs.Float64("cpu", 0, "CPUs")
	memory := fs.Int("memory", 0, "memory in MiB")
	disk := fs.Int("disk", 0, "workspace size in MiB")
	network := fs.String("network", "", "none or full")
	asJSON := fs.Bool("json", false, "print the sandbox as JSON")
	env, labels, secrets := kvFlag{}, kvFlag{}, kvFlag{}
	var volumes listFlag
	fs.Var(env, "env", "environment variable KEY=VALUE (repeatable)")
	fs.Var(labels, "label", "label KEY=VALUE (repeatable)")
	fs.Var(secrets, "secret", "secret KEY=VALUE (repeatable)")
	fs.Var(&volumes, "volume", "mount VOLUME_ID:/path (repeatable)")
	pos, err := parseMixed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return errors.New(createUsage)
	}
	if len(pos) == 1 && *template == "" {
		*template = pos[0]
	}
	body := map[string]any{}
	set := func(k string, v any, ok bool) {
		if ok {
			body[k] = v
		}
	}
	set("template", *template, *template != "")
	set("image", *image, *image != "")
	set("backend", *backend, *backend != "")
	set("cpu", *cpu, *cpu > 0)
	set("memory_mb", *memory, *memory > 0)
	set("disk_mb", *disk, *disk > 0)
	set("env", env, len(env) > 0)
	set("labels", labels, len(labels) > 0)
	set("secrets", secrets, len(secrets) > 0)
	if *network != "" {
		body["network"] = map[string]string{"mode": *network}
	}
	for name, s := range map[string]string{"ttl_seconds": *ttl, "idle_timeout_seconds": *idle} {
		if s == "" {
			continue
		}
		n, err := durationSeconds(s)
		if err != nil {
			return err
		}
		body[name] = n
	}
	var mounts []map[string]string
	for _, v := range volumes {
		vol, p, ok := strings.Cut(v, ":")
		if !ok || vol == "" || p == "" {
			return fmt.Errorf("--volume %q is not VOLUME_ID:/path", v)
		}
		mounts = append(mounts, map[string]string{"volume_id": vol, "path": p})
	}
	set("mounts", mounts, len(mounts) > 0)
	c, err := conn()
	if err != nil {
		return err
	}
	var raw json.RawMessage
	if err := c.json(http.MethodPost, "/v1/sandboxes", nil, body, &raw); err != nil {
		return err
	}
	if *asJSON {
		return printJSON(raw)
	}
	var sb sandbox
	if err := json.Unmarshal(raw, &sb); err != nil {
		return err
	}
	fmt.Fprintln(stdout, sb.ID)
	return nil
}

func listCmd(args []string) error {
	fs := newFlags("ls")
	conn := connFlags(fs)
	status := fs.String("status", "", "only this status")
	template := fs.String("template", "", "only this template")
	backend := fs.String("backend", "", "only this backend")
	query := fs.String("q", "", "case-insensitive search")
	quiet := fs.Bool("quiet", false, "print only ids")
	asJSON := fs.Bool("json", false, "print JSON")
	var labels listFlag
	fs.Var(&labels, "label", "label filter key:value (repeatable)")
	pos, err := parseMixed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return errors.New("usage: sbx ls [--status S] [--template T] [--label key:value] [-q TEXT] [--quiet] [--json]")
	}
	q := url.Values{}
	for k, v := range map[string]string{"status": *status, "template": *template, "backend": *backend, "q": *query} {
		if v != "" {
			q.Set(k, v)
		}
	}
	for _, l := range labels {
		q.Add("label", l)
	}
	c, err := conn()
	if err != nil {
		return err
	}
	var raw json.RawMessage
	if err := c.json(http.MethodGet, "/v1/sandboxes", q, nil, &raw); err != nil {
		return err
	}
	if *asJSON {
		return printJSON(raw)
	}
	var out struct {
		Sandboxes []sandbox `json:"sandboxes"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return err
	}
	if *quiet {
		for _, sb := range out.Sandboxes {
			fmt.Fprintln(stdout, sb.ID)
		}
		return nil
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTATUS\tTEMPLATE\tBACKEND\tNETWORK\tAGE\tEXPIRES\tLABELS")
	now := time.Now()
	for _, sb := range out.Sandboxes {
		tmpl := sb.Template
		if tmpl == "" {
			tmpl = sb.Image
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", sb.ID, sb.Status, tmpl, sb.Backend, sb.Network,
			humanDuration(now.Sub(sb.CreatedAt)), expires(now, sb.ExpiresAt), formatLabels(sb.Labels))
	}
	return tw.Flush()
}

func humanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func expires(now, at time.Time) string {
	if at.IsZero() {
		return "-"
	}
	if at.Before(now) {
		return "expired"
	}
	return "in " + humanDuration(at.Sub(now))
}

func formatLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+labels[k])
	}
	return strings.Join(parts, ",")
}

func getCmd(args []string) error {
	fs := newFlags("get")
	conn := connFlags(fs)
	pos, err := parseMixed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: sbx get ID")
	}
	c, err := conn()
	if err != nil {
		return err
	}
	var raw json.RawMessage
	if err := c.json(http.MethodGet, sandboxPath(pos[0]), nil, nil, &raw); err != nil {
		return err
	}
	return printJSON(raw)
}

func printJSON(raw json.RawMessage) error {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

func removeCmd(args []string) error {
	fs := newFlags("rm")
	conn := connFlags(fs)
	pos, err := parseMixed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return errors.New("usage: sbx rm ID [ID...]")
	}
	c, err := conn()
	if err != nil {
		return err
	}
	var failed []string
	for _, id := range pos {
		if err := c.json(http.MethodDelete, sandboxPath(id), nil, nil, nil); err != nil {
			fmt.Fprintf(stderr, "sbx: %s: %v\n", id, err)
			failed = append(failed, id)
			continue
		}
		fmt.Fprintln(stdout, id)
	}
	if len(failed) > 0 {
		return fmt.Errorf("could not remove %s", strings.Join(failed, ", "))
	}
	return nil
}

// stateCmd covers pause and resume, which take only an id.
func stateCmd(action string, args []string) error {
	fs := newFlags(action)
	conn := connFlags(fs)
	pos, err := parseMixed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fmt.Errorf("usage: sbx %s ID", action)
	}
	c, err := conn()
	if err != nil {
		return err
	}
	var sb sandbox
	if err := c.json(http.MethodPost, sandboxPath(pos[0], action), nil, nil, &sb); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s\t%s\n", sb.ID, sb.Status)
	return nil
}

func ttlCmd(args []string) error {
	fs := newFlags("ttl")
	conn := connFlags(fs)
	pos, err := parseMixed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return errors.New("usage: sbx ttl ID DURATION (seconds from now, for example 600 or 10m)")
	}
	n, err := durationSeconds(pos[1])
	if err != nil {
		return err
	}
	c, err := conn()
	if err != nil {
		return err
	}
	var sb sandbox
	if err := c.json(http.MethodPost, sandboxPath(pos[0], "timeout"), nil, map[string]int{"ttl_seconds": n}, &sb); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s\texpires %s\n", sb.ID, sb.ExpiresAt.Local().Format(time.RFC3339))
	return nil
}

const execUsage = "usage: sbx exec [--cwd DIR] [--env K=V] [--timeout 30s] [-i] ID [--] CMD [ARGS...]"

// execCmd streams a command's output and exits with its exit code. Flags
// come before the id so the command's own flags are left alone.
func execCmd(args []string) error {
	fs := newFlags("exec")
	conn := connFlags(fs)
	cwd := fs.String("cwd", "", "working directory")
	timeout := fs.String("timeout", "", "kill the command after this long")
	interactive := fs.Bool("i", false, "send stdin to the command")
	env := kvFlag{}
	fs.Var(env, "env", "environment variable KEY=VALUE (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) < 2 {
		return errors.New(execUsage)
	}
	id, cmd := rest[0], rest[1:]
	if cmd[0] == "--" {
		cmd = cmd[1:]
	}
	if len(cmd) == 0 {
		return errors.New(execUsage)
	}
	body := map[string]any{"cmd": cmd}
	if *cwd != "" {
		body["cwd"] = remotePath(*cwd)
	}
	if len(env) > 0 {
		body["env"] = env
	}
	if *timeout != "" {
		n, err := durationSeconds(*timeout)
		if err != nil {
			return err
		}
		body["timeout_seconds"] = n
	}
	if *interactive {
		in, err := io.ReadAll(stdin)
		if err != nil {
			return err
		}
		body["stdin_base64"] = base64.StdEncoding.EncodeToString(in)
	}
	c, err := conn()
	if err != nil {
		return err
	}
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := c.do(http.MethodPost, sandboxPath(id, "exec"), url.Values{"stream": {"1"}}, strings.NewReader(string(b)), "application/json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return readExecStream(resp.Body)
}

// readExecStream copies stdout and stderr events and turns the exit event
// into an exitError when the code is not zero.
func readExecStream(r io.Reader) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	event := ""
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data := []byte(strings.TrimPrefix(line, "data: "))
			switch event {
			case "stdout", "stderr":
				var chunk struct {
					Data string `json:"data"`
				}
				if err := json.Unmarshal(data, &chunk); err != nil {
					return err
				}
				w := stdout
				if event == "stderr" {
					w = stderr
				}
				io.WriteString(w, chunk.Data)
			case "exit":
				var exit struct {
					ExitCode  int  `json:"exit_code"`
					Truncated bool `json:"truncated"`
				}
				if err := json.Unmarshal(data, &exit); err != nil {
					return err
				}
				if exit.Truncated {
					fmt.Fprintln(stderr, "sbx: output was truncated")
				}
				if exit.ExitCode != 0 {
					return &exitError{code: exit.ExitCode}
				}
				return nil
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
	return errors.New("the exec stream ended without an exit status")
}
