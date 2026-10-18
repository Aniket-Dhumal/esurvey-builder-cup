package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

// healthcheck probes this process over its own HTTP port and exits 0 or 1.
//
// The production image is distroless: no shell, no curl, no wget. Rather than
// add a package manager to the runtime image so a container probe has
// something to run -- which is a supply-chain cost paid forever for a two-line
// check -- the binary probes itself. `esurvey-server healthcheck` is what the
// container HEALTHCHECK invokes.
func healthcheck(addr string) int {
	// The listen address is what the server binds; ":8090" needs a host to be
	// dialled.
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: bad address %q: %v\n", addr, err)
		return 1
	}
	if host == "" {
		host = "127.0.0.1"
	}

	client := &http.Client{Timeout: 5 * time.Second}
	url := "http://" + net.JoinHostPort(host, port) + "/readyz"

	resp, err := client.Get(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: %v\n", err)
		return 1
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "healthcheck: %s returned %d\n", url, resp.StatusCode)
		return 1
	}
	return 0
}
