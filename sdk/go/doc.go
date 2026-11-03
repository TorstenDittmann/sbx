// Package sbx is the Go client for sbx, a single-binary sandbox server.
//
// It mirrors the TypeScript and Python SDKs with Go conventions: every call
// takes a context, option structs replace option objects, and streams are
// range-over-func iterators that close the connection when the loop ends.
// It has no dependencies outside the standard library.
//
//	client, err := sbx.NewClient() // reads SBX_URL and SBX_API_KEY
//	if err != nil {
//		log.Fatal(err)
//	}
//	box, err := client.CreateSandbox(ctx, sbx.CreateSandboxOptions{Template: "base", TTL: 5 * time.Minute})
//	if err != nil {
//		log.Fatal(err)
//	}
//	defer box.Kill(context.Background())
//	result, err := box.Exec(ctx, sbx.Shell("echo hello"), nil)
//
// The import path ends in /go, but the package name is sbx.
package sbx
