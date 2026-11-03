package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

const previewUsage = `usage:
  sbx preview ID PORT [--public] [--json]     publish PORT and print its URL
  sbx preview ls ID [--json]                  list a sandbox's previews
  sbx preview rm ID PREVIEW_ID [PREVIEW_ID...]`

// preview is the subset of the preview resource the CLI prints.
type preview struct {
	ID        string    `json:"id"`
	SandboxID string    `json:"sandbox_id"`
	Port      int       `json:"port"`
	Public    bool      `json:"public"`
	URL       string    `json:"url"`
	PublicURL string    `json:"public_url"`
	Host      string    `json:"host"`
	Token     string    `json:"token"`
	CreatedAt time.Time `json:"created_at"`
}

// previewCmd dispatches sbx preview. A sandbox id never collides with the
// subcommand names, so "sbx preview ID PORT" is the short form of create.
func previewCmd(args []string) error {
	if len(args) == 0 {
		return errors.New(previewUsage)
	}
	switch args[0] {
	case "create", "add":
		return previewCreate(args[1:])
	case "ls", "list":
		return previewList(args[1:])
	case "rm", "delete":
		return previewRemove(args[1:])
	case "help", "-h", "--help":
		fmt.Fprintln(stdout, previewUsage)
		return nil
	}
	return previewCreate(args)
}

// previewCreate prints the shareable URL on stdout, so
// url=$(sbx preview $id 3000) works. The token is only shown here, so it
// also goes to stderr on its own line for private previews.
func previewCreate(args []string) error {
	fs := newFlags("preview")
	conn := connFlags(fs)
	public := fs.Bool("public", false, "open without a token (when the server allows public previews)")
	asJSON := fs.Bool("json", false, "print the preview as JSON, including the token")
	pos, err := parseMixed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return errors.New(previewUsage)
	}
	port, err := strconv.Atoi(pos[1])
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("invalid port %q", pos[1])
	}
	c, err := conn()
	if err != nil {
		return err
	}
	var raw json.RawMessage
	body := map[string]any{"port": port, "public": *public}
	if err := c.json(http.MethodPost, sandboxPath(pos[0], "previews"), nil, body, &raw); err != nil {
		return err
	}
	if *asJSON {
		return printJSON(raw)
	}
	var pv preview
	if err := json.Unmarshal(raw, &pv); err != nil {
		return err
	}
	fmt.Fprintln(stdout, pv.PublicURL)
	if !pv.Public && pv.Token != "" {
		fmt.Fprintf(stderr, "preview %s, token %s (shown once)\n", pv.ID, pv.Token)
	} else {
		fmt.Fprintf(stderr, "preview %s\n", pv.ID)
	}
	return nil
}

func previewList(args []string) error {
	fs := newFlags("preview ls")
	conn := connFlags(fs)
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseMixed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: sbx preview ls ID [--json]")
	}
	c, err := conn()
	if err != nil {
		return err
	}
	var raw json.RawMessage
	if err := c.json(http.MethodGet, sandboxPath(pos[0], "previews"), nil, nil, &raw); err != nil {
		return err
	}
	if *asJSON {
		return printJSON(raw)
	}
	var out struct {
		Previews []preview `json:"previews"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tPORT\tACCESS\tAGE\tURL")
	now := time.Now()
	for _, pv := range out.Previews {
		access := "token"
		if pv.Public {
			access = "public"
		}
		age := "-"
		if !pv.CreatedAt.IsZero() {
			age = humanDuration(now.Sub(pv.CreatedAt))
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\n", pv.ID, pv.Port, access, age, pv.PublicURL)
	}
	return tw.Flush()
}

func previewRemove(args []string) error {
	fs := newFlags("preview rm")
	conn := connFlags(fs)
	pos, err := parseMixed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) < 2 {
		return errors.New("usage: sbx preview rm ID PREVIEW_ID [PREVIEW_ID...]")
	}
	c, err := conn()
	if err != nil {
		return err
	}
	var failed []string
	for _, pid := range pos[1:] {
		if err := c.json(http.MethodDelete, sandboxPath(pos[0], "previews", url.PathEscape(pid)), nil, nil, nil); err != nil {
			fmt.Fprintf(stderr, "sbx: %s: %v\n", pid, err)
			failed = append(failed, pid)
			continue
		}
		fmt.Fprintln(stdout, pid)
	}
	if len(failed) > 0 {
		return fmt.Errorf("could not remove %s", strings.Join(failed, ", "))
	}
	return nil
}
