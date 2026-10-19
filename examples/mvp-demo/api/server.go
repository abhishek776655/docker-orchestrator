// Command api is the MVP demo's web service: it reports whether it can
// reach its "cache" dependency by logical service name, proving both
// build-from-source and service discovery in one place.
package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

func main() {
	cacheAddr := os.Getenv("CACHE_URL")
	if cacheAddr == "" {
		cacheAddr = "cache:6380"
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		conn, err := net.DialTimeout("tcp", cacheAddr, 2*time.Second)
		if err != nil {
			fmt.Fprintf(w, "api-ok, cache (%s) unreachable: %v\n", cacheAddr, err)
			return
		}
		conn.Close()
		fmt.Fprintf(w, "api-ok, cache (%s) reachable\n", cacheAddr)
	})

	fmt.Println("api listening on :9090, cache at", cacheAddr)
	http.ListenAndServe(":9090", nil)
}
