// Command web is the MVP demo's second hop: it fans out over HTTP/TCP to
// api, worker, and queue by logical service name, proving multi-hop
// cross-node discovery (not just a single dependency) in one response.
package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

func main() {
	apiAddr := envOr("API_URL", "http://api:9090")
	workerAddr := envOr("WORKER_URL", "worker:7001")
	queueAddr := envOr("QUEUE_URL", "queue:6391")

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "web-ok\n")
		fmt.Fprintf(w, "api (%s): %s\n", apiAddr, checkHTTP(apiAddr))
		fmt.Fprintf(w, "worker (%s): %s\n", workerAddr, checkTCP(workerAddr))
		fmt.Fprintf(w, "queue (%s): %s\n", queueAddr, checkTCP(queueAddr))
	})

	fmt.Println("web listening on :8080")
	http.ListenAndServe(":8080", nil)
}

func checkTCP(addr string) string {
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return fmt.Sprintf("unreachable: %v", err)
	}
	conn.Close()
	return "reachable"
}

func checkHTTP(url string) string {
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Sprintf("unreachable: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return fmt.Sprintf("reachable (%s)", string(body))
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
