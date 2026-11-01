package usage

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type billingExampleTransport struct{}

func (billingExampleTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body := `{"lineItems":[{"series":"SERVERLESS","totalCost":{"currencyCode":"USD","units":"12","nanos":500000000}}]}`
	if req.URL.Path == "/v1/accounts" {
		body = `{"accounts":[{"name":"accounts/selected-org"},{"name":"accounts/another-org"}]}`
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: req}, nil
}

func ExampleClient_QueryTarget() {
	client := Client{Transport: billingExampleTransport{}}
	auth := ResolvedAuth{
		ModelBaseURL: "https://api.fireworks.ai/inference/v1",
		Headers:      http.Header{"Authorization": {"Bearer synthetic-token"}},
		Revalidate:   func(context.Context) error { return nil },
	}
	report, err := client.QueryTarget(context.Background(), "fireworks", auth, "selected-org", time.Second)
	if err != nil {
		panic(err)
	}
	fmt.Println(report.AccountLabel, report.Metrics[0].Currency, report.Metrics[0].Value)
	// Output: selected-org USD 12.5
}

func ExampleClient_ListTargets() {
	client := Client{Transport: billingExampleTransport{}}
	auth := ResolvedAuth{
		ModelBaseURL: "https://api.fireworks.ai/inference/v1",
		Headers:      http.Header{"Authorization": {"Bearer synthetic-token"}},
		Revalidate:   func(context.Context) error { return nil },
	}
	targets, err := client.ListTargets(context.Background(), "fireworks", auth, time.Second)
	if err != nil {
		panic(err)
	}
	fmt.Println(targets)
	// Output: [{selected-org selected-org} {another-org another-org}]
}

func ExampleTarget() {
	target := Target{ID: "selected-org", Label: "selected-org"}
	fmt.Println(target.ID, target.Label)
	// Output: selected-org selected-org
}
