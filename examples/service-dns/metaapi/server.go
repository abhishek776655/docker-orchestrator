// Command metaapi is a minimal HTTP server for the service-name DNS
// fixture: it answers "metaapi-ok" on its internal port, with no awareness
// of which node it runs on.
package main

import (
	"fmt"
	"log"
	"net/http"
)

func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "metaapi-ok")
	})
	log.Println("metaapi listening on :5000")
	log.Fatal(http.ListenAndServe(":5000", nil))
}
