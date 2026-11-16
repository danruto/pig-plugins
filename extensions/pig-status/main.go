// Command pig-status renders the PiG footer with native usage collection.
package main

import (
	"fmt"
	"os"

	"github.com/danruto/pig-plugins/extensions/pig-status/nativeusage"
)

func main() {
	if err := nativeusage.Extension().Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
