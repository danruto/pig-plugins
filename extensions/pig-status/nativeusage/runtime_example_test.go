package nativeusage

import (
	"fmt"

	sdk "github.com/MichaelKinsy/PiG/extensions/sdk"

	"github.com/danruto/pig-plugins/extensions/pig-status/internal/usage"
)

func ExampleRegister() {
	var cached usage.Cached
	published := 0
	runtime, err := Register(sdk.New("example"), Options{Publish: func(view usage.Cached) { published++; cached = view }})
	if err != nil {
		panic(err)
	}
	if err := runtime.Configure(usage.CollectionSettings{}); err != nil {
		panic(err)
	}
	if err := runtime.Close(); err != nil {
		panic(err)
	}
	fmt.Println(published > 0, len(cached.Windows), cached.ExpiresAt.IsZero())
	// Output: true 0 true
}
