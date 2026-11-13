// sign makes and checks the ed25519 signature on a release's SHA256SUMS.txt.
//
//	sign keygen                  prints a new private key (base64) and its public key
//	sign sign <file>             signs file with the key in $PROPBOOK_SIGNING_KEY, writes <file>.sig
//	sign verify <pubkey> <file>  checks <file>.sig
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		die("usage: sign keygen | sign <file> | verify <pubkey> <file>")
	}
	switch os.Args[1] {
	case "keygen":
		pub, priv, _ := ed25519.GenerateKey(rand.Reader)
		fmt.Println("private:", base64.StdEncoding.EncodeToString(priv.Seed()))
		fmt.Println("public: ", base64.StdEncoding.EncodeToString(pub))
	case "sign":
		seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(os.Getenv("PROPBOOK_SIGNING_KEY")))
		if err != nil || len(seed) != ed25519.SeedSize {
			die("PROPBOOK_SIGNING_KEY is missing or not a valid key")
		}
		data := read(os.Args[2])
		sig := ed25519.Sign(ed25519.NewKeyFromSeed(seed), data)
		if err := os.WriteFile(os.Args[2]+".sig", []byte(base64.StdEncoding.EncodeToString(sig)+"\n"), 0o644); err != nil {
			die(err.Error())
		}
	case "verify":
		pub, _ := base64.StdEncoding.DecodeString(os.Args[2])
		sig, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(string(read(os.Args[3] + ".sig"))))
		if len(pub) != ed25519.PublicKeySize || !ed25519.Verify(pub, read(os.Args[3]), sig) {
			die("signature does NOT match")
		}
		fmt.Println("signature ok")
	default:
		die("unknown command " + os.Args[1])
	}
}

func read(p string) []byte {
	b, err := os.ReadFile(p)
	if err != nil {
		die(err.Error())
	}
	return b
}

func die(m string) { fmt.Fprintln(os.Stderr, "sign:", m); os.Exit(1) }
