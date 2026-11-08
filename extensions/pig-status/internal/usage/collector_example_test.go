package usage

import (
	"context"
	"errors"
	"fmt"
	"time"
)

func ExampleCollector() {
	collector := NewCollector(Client{})
	collector.Configure(CollectionSettings{Targets: map[string]string{"fireworks": "chosen-account"}})
	view := collector.Snapshot(time.Now())
	fmt.Println(len(view.Windows), view.ExpiresAt.IsZero())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fmt.Println(errors.Is(collector.Refresh(ctx, Resolver{}), context.Canceled))
	// Output:
	// 0 true
	// true
}
