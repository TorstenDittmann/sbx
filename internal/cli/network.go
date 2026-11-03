package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"strings"
	"text/tabwriter"
)

// netListFlag collects repeated egress rules. Each value may also hold a
// comma-separated list, so --allow a.com,b.com works like two flags.
type netListFlag struct {
	items []string
	set   bool
}

func (f *netListFlag) String() string { return strings.Join(f.items, ",") }

func (f *netListFlag) Set(s string) error {
	f.set = true
	for _, item := range strings.Split(s, ",") {
		if item = strings.TrimSpace(item); item != "" {
			f.items = append(f.items, item)
		}
	}
	return nil
}

// netFlags are the egress flags create and network share.
type netFlags struct {
	mode       *string
	allow      netListFlag
	deny       netListFlag
	egressKbps *int
	maxConns   *int
	fs         *flag.FlagSet
}

func addNetFlags(fs *flag.FlagSet, modeName string) *netFlags {
	n := &netFlags{fs: fs}
	n.mode = fs.String(modeName, "", "none, full, or allow")
	fs.Var(&n.allow, "allow", "allowed hostname, IP, or CIDR (repeatable or comma-separated)")
	fs.Var(&n.deny, "deny", "denied hostname, IP, or CIDR (repeatable or comma-separated)")
	n.egressKbps = fs.Int("egress-kbps", 0, "outbound bandwidth cap in kbit/s (0 inherits)")
	n.maxConns = fs.Int("max-connections", 0, "concurrent outbound connection cap (0 inherits)")
	return n
}

func (n *netFlags) visited(name string) bool {
	seen := false
	n.fs.Visit(func(f *flag.Flag) { seen = seen || f.Name == name })
	return seen
}

// body returns the network object, or nil when no flag was set.
func (n *netFlags) body() map[string]any {
	out := map[string]any{}
	if *n.mode != "" {
		out["mode"] = *n.mode
	}
	if n.allow.set {
		out["allow"] = nonNilList(n.allow.items)
	}
	if n.deny.set {
		out["deny"] = nonNilList(n.deny.items)
	}
	if n.visited("egress-kbps") {
		out["egress_kbps"] = *n.egressKbps
	}
	if n.visited("max-connections") {
		out["max_connections"] = *n.maxConns
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func nonNilList(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// netView is the network part of a sandbox resource.
type netView struct {
	ID         string   `json:"id"`
	Status     string   `json:"status"`
	Network    string   `json:"network"`
	Allow      []string `json:"egress_allow"`
	Deny       []string `json:"egress_deny"`
	EgressKbps int      `json:"egress_kbps"`
	MaxConns   int      `json:"max_connections"`
}

const networkUsage = "usage: sbx network ID [--mode full|allow] [--allow RULE] [--deny RULE] [--clear-allow] [--clear-deny] [--egress-kbps N] [--max-connections N] [--json]"

// networkCmd prints a sandbox's egress policy, or changes it when any flag
// that edits the policy is given.
func networkCmd(args []string) error {
	fs := newFlags("network")
	conn := connFlags(fs)
	nf := addNetFlags(fs, "mode")
	clearAllow := fs.Bool("clear-allow", false, "remove the sandbox allow list")
	clearDeny := fs.Bool("clear-deny", false, "remove the sandbox deny rules (server and key rules stay)")
	asJSON := fs.Bool("json", false, "print the sandbox as JSON")
	pos, err := parseMixed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New(networkUsage)
	}
	if *clearAllow && nf.allow.set || *clearDeny && nf.deny.set {
		return errors.New("--clear-allow and --clear-deny cannot be combined with --allow or --deny")
	}
	body := nf.body()
	if *clearAllow || *clearDeny {
		if body == nil {
			body = map[string]any{}
		}
		if *clearAllow {
			body["allow"] = []string{}
		}
		if *clearDeny {
			body["deny"] = []string{}
		}
	}
	c, err := conn()
	if err != nil {
		return err
	}
	var raw json.RawMessage
	if body == nil {
		err = c.json(http.MethodGet, sandboxPath(pos[0]), nil, nil, &raw)
	} else {
		err = c.json(http.MethodPatch, sandboxPath(pos[0], "network"), nil, body, &raw)
	}
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(raw)
	}
	var v netView
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	printNet(v)
	return nil
}

func printNet(v netView) {
	limit := func(n int, unit string) string {
		if n <= 0 {
			return "unlimited"
		}
		return fmt.Sprintf("%d%s", n, unit)
	}
	list := func(items []string) string {
		if len(items) == 0 {
			return "-"
		}
		return strings.Join(items, ", ")
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "SANDBOX\t%s\n", v.ID)
	fmt.Fprintf(tw, "MODE\t%s\n", v.Network)
	fmt.Fprintf(tw, "ALLOW\t%s\n", list(v.Allow))
	fmt.Fprintf(tw, "DENY\t%s\n", list(v.Deny))
	fmt.Fprintf(tw, "EGRESS\t%s\n", limit(v.EgressKbps, " kbit/s"))
	fmt.Fprintf(tw, "CONNECTIONS\t%s\n", limit(v.MaxConns, ""))
	tw.Flush()
}
