package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPreviewCommands(t *testing.T) {
	_, out, errOut := liveServer(t)
	if err := Run([]string{"create", "base", "--ttl", "5m"}); err != nil {
		t.Fatal(err)
	}
	sbID := strings.TrimSpace(out.String())

	out.buf.Reset()
	if err := Run([]string{"preview", sbID, "3000"}); err != nil {
		t.Fatal(err)
	}
	link := strings.TrimSpace(out.String())
	if !strings.Contains(link, "/p/"+sbID+"/3000/?token=pvt_") {
		t.Fatalf("preview url %q", link)
	}
	if !strings.Contains(errOut.String(), "token pvt_") {
		t.Fatalf("stderr %q", errOut.String())
	}

	out.buf.Reset()
	if err := Run([]string{"preview", "create", sbID, "8080", "--public", "--json"}); err != nil {
		t.Fatal(err)
	}
	var pub preview
	if err := json.Unmarshal([]byte(out.String()), &pub); err != nil || !pub.Public || pub.Port != 8080 {
		t.Fatalf("json: %v %s", err, out.String())
	}

	out.buf.Reset()
	if err := Run([]string{"preview", "ls", sbID}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, pub.ID) || !strings.Contains(got, "public") || !strings.Contains(got, "3000") || strings.Contains(got, "token=") {
		t.Fatalf("ls output:\n%s", got)
	}

	out.buf.Reset()
	if err := Run([]string{"preview", "rm", sbID, pub.ID}); err != nil {
		t.Fatal(err)
	}
	out.buf.Reset()
	if err := Run([]string{"preview", "ls", sbID}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), pub.ID) {
		t.Fatalf("removed preview still listed:\n%s", out.String())
	}

	for _, args := range [][]string{{"preview"}, {"preview", sbID}, {"preview", sbID, "99999"}, {"preview", "rm", sbID}} {
		if err := Run(args); err == nil {
			t.Fatalf("%v: want an error", args)
		}
	}
}
