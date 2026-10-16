// Command notely-api is the notely backend: a JSON API storing notes as Markdown files.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jmnser/notely/internal/serve"
)

func main() {
	addr := flag.String("addr", "localhost:8001", "listen address")
	data := flag.String("data", "notes", "notes directory")
	usersFile := flag.String("users", "",
		"users file, created with the default user if missing (default <data>/.users.json)")
	interval := flag.Duration("sync-interval", time.Hour, "git backup interval; 0 syncs on request only")
	hash := flag.Bool("hash", false, "read a password from stdin, print its argon2id hash and exit")
	flag.Parse()

	if *hash {
		password, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if password = strings.TrimRight(password, "\r\n"); password == "" {
			log.Fatal("no password: ", err)
		}
		fmt.Println(hashPassword(password))
		return
	}

	s, err := openStore(*data)
	var users map[string]argonHash
	if err == nil {
		if *usersFile == "" {
			*usersFile = filepath.Join(*data, ".users.json") // dot files are not notes
		}
		users, err = loadUsers(*usersFile)
	}
	if err == nil {
		names := map[string]bool{}
		for name := range users {
			names[name] = true
		}
		sy := newSyncer(*data, names)
		ctx, stop := context.WithCancel(context.Background())
		go sy.run(ctx, *interval)

		api, sh := http.NewServeMux(), sy.handler()
		api.Handle("/api/sync", sh)
		api.Handle("/api/sync/", sh)
		api.Handle("/", newHandler(s))
		err = serve.Run(*addr, newAuth(users).handler(api))

		stop()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		sy.syncAll(ctx) // push the last changes
		cancel()
	}
	if err != nil {
		log.Fatal(err)
	}
}
