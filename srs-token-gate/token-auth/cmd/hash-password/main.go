package main

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

func main() {
	if len(os.Args) != 2 || strings.TrimSpace(os.Args[1]) == "" {
		fmt.Fprintln(os.Stderr, "usage: go run ./cmd/hash-password <password>")
		os.Exit(2)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(os.Args[1]), bcrypt.DefaultCost)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(string(hash))
}
