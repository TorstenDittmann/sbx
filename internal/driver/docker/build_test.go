package docker

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/TorstenDittmann/sbx/internal/driver"
)

func TestRenderDockerfileQuotesEachStep(t *testing.T) {
	out, err := renderDockerfile(driver.BuildSpec{
		Base: "alpine:3.20",
		Run:  []string{"echo hi", "printf 'a\"b'\nRUN rm -rf /"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "FROM alpine:3.20\nUSER root\n" +
		`RUN ["/bin/sh","-c","echo hi"]` + "\n" +
		`RUN ["/bin/sh","-c","printf 'a\"b'\nRUN rm -rf /"]` + "\n"
	if string(out) != want {
		t.Fatalf("dockerfile:\n%s\nwant:\n%s", out, want)
	}
	if lines := strings.Count(string(out), "\n"); lines != 4 {
		t.Fatalf("lines = %d", lines)
	}
}

func TestRenderDockerfileRejectsBadBase(t *testing.T) {
	if _, err := renderDockerfile(driver.BuildSpec{Base: "alpine\nRUN id"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestReadBuildStream(t *testing.T) {
	ok := `{"stream":"Step 1/2"}` + "\n" + `{"aux":{"ID":"sha256:x"}}` + "\n"
	if err := readBuildStream(strings.NewReader(ok)); err != nil {
		t.Fatal(err)
	}
	bad := `{"stream":"Step 3/4 : RUN [\"/bin/sh\"]\n"}` + "\n" +
		`{"stream":"earlier step output\n"}` + "\n" +
		`{"stream":" ---\u003e Running in abc\n"}` + "\n" +
		`{"stream":"\u001b[91mapk: not found\n\u001b[0m"}` + "\n" +
		`{"error":"The command '/bin/sh -c apk add jq' returned a non-zero code: 127"}` + "\n"
	err := readBuildStream(strings.NewReader(bad))
	if err == nil || err.Error() != "build step exited with code 127:\napk: not found" {
		t.Fatalf("err = %q", err)
	}
}

func TestListAndRemoveBuiltImages(t *testing.T) {
	fd, sock := startFakeDaemon(t)
	cli, err := NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	d := NewWithClient(Options{Name: "docker"}, cli)
	ctx := context.Background()
	fd.mu.Lock()
	fd.images["sbx-template:aaa"] = true
	fd.images["sbx-template:bbb"] = true
	fd.images["alpine:3.20"] = true
	fd.containers["cid-x"] = &fakeContainer{id: "cid-x", status: "running", body: createRequest{Image: "sbx-template:bbb"}}
	fd.mu.Unlock()

	list, err := d.ListBuiltImages(ctx, "sbx-template")
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Ref < list[j].Ref })
	if len(list) != 2 || list[0].Ref != "sbx-template:aaa" || list[1].Ref != "sbx-template:bbb" || list[0].Size != 42 || list[0].Created.Unix() != 1700000000 {
		t.Fatalf("list = %+v", list)
	}
	if err := d.RemoveImage(ctx, "sbx-template:aaa"); err != nil {
		t.Fatal(err)
	}
	if err := d.RemoveImage(ctx, "sbx-template:aaa"); err != nil {
		t.Fatalf("missing image: %v", err)
	}
	if err := d.RemoveImage(ctx, "sbx-template:bbb"); !errors.Is(err, driver.ErrImageInUse) {
		t.Fatalf("in use: %v", err)
	}
	fd.mu.Lock()
	defer fd.mu.Unlock()
	if fd.images["sbx-template:aaa"] || !fd.images["sbx-template:bbb"] || !fd.images["alpine:3.20"] {
		t.Fatalf("images = %v", fd.images)
	}
}
