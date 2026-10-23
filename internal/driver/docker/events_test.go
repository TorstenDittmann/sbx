package docker

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/TorstenDittmann/sbx/internal/driver"
)

func TestWatchEvents(t *testing.T) {
	var filters string
	mux := http.NewServeMux()
	mux.HandleFunc("/version", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"ApiVersion":"1.44"}`)
	})
	mux.HandleFunc("/v1.44/events", func(w http.ResponseWriter, r *http.Request) {
		filters = r.URL.Query().Get("filters")
		fmt.Fprintln(w, `{"Type":"container","Action":"oom","Actor":{"ID":"c1","Attributes":{"sbx.id":"sbx_a"}}}`)
		fmt.Fprintln(w, `{"Type":"container","Action":"start","Actor":{"ID":"c1","Attributes":{"sbx.id":"sbx_a"}}}`)
		fmt.Fprintln(w, `{"Type":"container","Action":"die","Actor":{"ID":"c1","Attributes":{"sbx.id":"sbx_a","exitCode":"137"}}}`)
	})
	ln, err := net.Listen("unix", t.TempDir()+"/docker.sock")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	d, err := New(Options{Host: ln.Addr().String(), Name: "docker", NoEgressFilter: true})
	if err != nil {
		t.Fatal(err)
	}
	var got []driver.Event
	err = d.WatchEvents(context.Background(), func(ev driver.Event) { got = append(got, ev) })
	if err == nil {
		t.Fatal("expected the stream end to be reported")
	}
	want := []driver.Event{
		{Kind: driver.EventOOM, ID: "c1", SandboxID: "sbx_a"},
		{Kind: driver.EventDie, ID: "c1", SandboxID: "sbx_a", ExitCode: 137},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("events %+v", got)
	}
	if f, _ := url.QueryUnescape(filters); !strings.Contains(f, `"oom","die"`) || !strings.Contains(f, labelManaged+"=true") {
		t.Fatalf("filters %q", filters)
	}
}
