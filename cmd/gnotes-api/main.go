// Command gnotes-api is the gnotes backend: a JSON API storing notes as Markdown files.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/jmnser/gnotes/internal/serve"
)

func main() {
	addr := flag.String("addr", "localhost:8001", "listen address")
	data := flag.String("data", "notes", "notes directory")
	usersFile := flag.String("users", "",
		"users file, created with the default user if missing (default <data>/.users.json)")
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
		err = serve.Run(*addr, newAuth(users).handler(newHandler(s)))
	}
	if err != nil {
		log.Fatal(err)
	}
}
