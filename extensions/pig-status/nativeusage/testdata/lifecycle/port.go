package lifecycle

import (
	"context"
	"sync/atomic"
	"time"

	sdk "github.com/MichaelKinsy/PiG/extensions/sdk"

	"github.com/danruto/pig-plugins/extensions/pig-status/internal/usage"
	native "github.com/danruto/pig-plugins/extensions/pig-status/nativeusage"
)

var fenceArmed atomic.Bool

func registerFence(ext *sdk.Extension) (unsubscribe func()) {
	unsubscribe, err := ext.Events().On("oauth:credential-readiness:v1", func(call sdk.Context, data any) error {
		if !fenceArmed.CompareAndSwap(true, false) {
			return nil
		}
		request, _ := data.(map[string]any)
		record("fence-held", map[string]any{"provider": request["provider"]})
		<-call.Done()
		return nil
	})
	if err != nil {
		panic("fixture readiness listener failed")
	}
	ext.RegisterCommand("lifecycle-fence", sdk.CommandOptions{Handler: func(_ sdk.Context, _ string) error {
		fenceArmed.Store(true)
		record("fence-armed", true)
		return nil
	}})
	ext.RegisterCommand("lifecycle-offers", sdk.CommandOptions{Handler: func(call sdk.Context, provider string) error {
		session, err := call.GetSessionID()
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		result, err := native.Port(call).Collect(ctx, usage.CollectionRequest{Channel: "oauth:credential-source:v1", Kind: "offers", ProviderID: provider, SessionID: session, TimeoutMS: 5000})
		record("offers-refused", map[string]any{"provider": provider, "refused": err != nil, "values": len(result.Values)})
		return nil
	}})
	return unsubscribe
}
