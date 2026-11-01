package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const maxSuccessBodyBytes = 64 * 1024

func (c Client) fetch(ctx context.Context, endpoint, description string, headers http.Header) (map[string]any, error) {
	return c.fetchJSON(ctx, endpoint, description, headers, false)
}

func (c Client) fetchJSON(ctx context.Context, endpoint, description string, headers http.Header, zai bool) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errors.New("could not construct usage request")
	}
	req.Header = headers.Clone()
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	if !hasHeader(req.Header, "User-Agent") {
		req.Header.Set("User-Agent", "pi-usage")
	}
	client := http.Client{
		Transport: c.Transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%s request failed.", description)
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		return nil, fmt.Errorf("%s refused a redirected response.", description)
	}
	failed := response.StatusCode < 200 || response.StatusCode >= 300
	if failed && !zai {
		return nil, fmt.Errorf("%s returned %d.", description, response.StatusCode)
	}
	maxBytes := maxSuccessBodyBytes
	if failed {
		maxBytes = 4 * 1024
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, int64(maxBytes+1)))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%s response could not be read.", description)
	}
	if len(body) > maxBytes {
		if !failed {
			return nil, fmt.Errorf("%s response exceeded %d bytes.", description, maxBytes)
		}
		body = append(body[:maxBytes], []byte("…")...)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if zai {
		if message := zaiResponseError(response.StatusCode, body); message != "" {
			return nil, errors.New(message)
		}
	}
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return nil, fmt.Errorf("%s returned invalid JSON.", description)
	}
	payload, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s response was not an object.", description)
	}
	return payload, nil
}

func validatedHeaders(providerID string, headers http.Header) (http.Header, error) {
	var authorization string
	found := false
	for name, values := range headers {
		if !strings.EqualFold(name, "Authorization") {
			continue
		}
		if found || len(values) != 1 || strings.TrimSpace(values[0]) == "" {
			return nil, errors.New("usage requires one nonempty authorization override")
		}
		found = true
		authorization = values[0]
	}
	if !found {
		return nil, errors.New("usage requires authorization")
	}
	for _, character := range authorization {
		if (character < 0x20 && character != '\t') || character > 0x7e {
			return nil, errors.New("usage authorization contains invalid characters")
		}
	}
	if providerID == "deepseek" {
		if len(authorization) <= 6 || !strings.EqualFold(authorization[:6], "Bearer") ||
			(authorization[6] != ' ' && authorization[6] != '\t') || strings.TrimSpace(authorization[6:]) == "" {
			return nil, errors.New("deepseek usage requires bearer authentication")
		}
		authorization = "Bearer " + strings.TrimSpace(authorization[6:])
	}
	result := headers.Clone()
	for name := range result {
		if strings.EqualFold(name, "Authorization") {
			delete(result, name)
		}
	}
	result.Set("Authorization", authorization)
	return result, nil
}

func hasHeader(headers http.Header, name string) bool {
	for key := range headers {
		if strings.EqualFold(key, name) {
			return true
		}
	}
	return false
}
