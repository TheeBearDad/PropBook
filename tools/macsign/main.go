// macsign builds and ad-hoc signs a macOS .app bundle from any OS.
//
//	macsign lipo   <out> <thin-binary>...   combine thin Mach-O files into a universal binary
//	macsign sign   <App.app>                ad-hoc sign the bundle (CodeResources + embedded signature)
//	macsign verify <App.app>                check every hash in the bundle's signature
//
// The signature matches what `codesign --sign - App.app` produces for a bundle with
// no entitlements: a SHA-256 CodeDirectory whose special slots bind Info.plist, an
// empty requirement set and _CodeSignature/CodeResources, plus an empty CMS blob.
// Format reference: Apple's cs_blobs.h and indygreg/apple-platform-rs (rcodesign).
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 3 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "lipo":
		if len(os.Args) < 4 {
			usage()
		}
		err = lipo(os.Args[2], os.Args[3:])
	case "sign":
		err = signBundle(os.Args[2])
	case "verify":
		var report []string
		report, err = verifyBundle(os.Args[2])
		for _, l := range report {
			fmt.Println(l)
		}
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "macsign:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: macsign lipo <out> <thin>... | sign <App.app> | verify <App.app>")
	os.Exit(2)
}
