// Command caddy is a custom Caddy build that includes the geo_ops modules, used
// for manual end-to-end runs and as the registration host for the e2e tests in
// this module. It is a separate module so the plugin's go.mod stays free of the
// Caddy server build tree.
//
//	go build -o caddy.exe .      # build a runnable Caddy with geo_ops
//	./caddy.exe run --config Caddyfile
//	go test ./...                # run the end-to-end tests (see e2e_test.go)
package main

import (
	_ "github.com/caddyserver/caddy/v2/modules/standard"
	_ "github.com/ubiuser/caddy-geo-ops"

	caddycmd "github.com/caddyserver/caddy/v2/cmd"
)

func main() {
	caddycmd.Main()
}
