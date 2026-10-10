// Command gnotes-api is the gnotes backend: a JSON API storing notes in a file.
package main

import (
	"flag"
	"log"

	"github.com/jmnser/gnotes/internal/serve"
)

func main() {
	addr := flag.String("addr", "localhost:8001", "listen address")
	data := flag.String("data", "notes.json", "notes file")
	flag.Parse()

	s, err := openStore(*data)
	if err == nil {
		err = serve.Run(*addr, newHandler(s))
	}
	if err != nil {
		log.Fatal(err)
	}
}
