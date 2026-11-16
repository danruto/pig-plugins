package main

import (
	"fmt"
	"os"

	"github.com/danruto/pig-plugins/extensions/pig-status/status"
)

func main() {
	ext, err := status.New(status.Options{})
	if err == nil {
		err = ext.Run()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
