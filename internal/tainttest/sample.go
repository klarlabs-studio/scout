package tainttest

import (
	"net/http"
	"os"
)

func handler(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("file")
	data, _ := os.ReadFile(path) // taint: user input -> file read (path traversal)
	w.Write(data)
}

func unused() {
	http.HandleFunc("/", handler)
	http.ListenAndServe(":8080", nil)
}
