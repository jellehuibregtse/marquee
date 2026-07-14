package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:3000", "address to listen on (loopback only)")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: marquee [--listen addr] -- command [args...]")
		flag.PrintDefaults()
	}
	flag.Parse()

	command := flag.Args()
	if len(command) == 0 {
		flag.Usage()
		os.Exit(2)
	}

	fmt.Fprintf(os.Stderr, "marquee: not implemented yet (would listen on %s and run %q)\n", *listen, command)
	os.Exit(1)
}
