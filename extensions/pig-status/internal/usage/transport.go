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

const (
	maxSuccessBodyBytes = 64 * 1024
	maxFailureBodyBytes = 4 * 1024
)

type usageRequest struct {
	endpoint    string
	description string
	headers     http.Header
	maxBytes    int
}

func (c Client) fetch(ctx context.Context, endpoint, description string, headers http.Header) (map[string]any, error) {
	return c.fetchJSON(ctx, endpoint, description, headers, false)
}

func (c Client) fetchJSON(ctx context.Context, endpoint, description string, headers http.Header, zai bool) (map[string]any, error) {
	request := usageRequest{endpoint: endpoint, description: description, headers: headers, maxBytes: maxSuccessBodyBytes}
	if zai {
		return c.fetchZai(ctx, request)
	}
	payload, _, err := c.fetchBounded(ctx, request)
	return payload, err
}

func (c Client) fetchBounded(ctx context.Context, request usageRequest) (map[string]any, int, error) {
	response, err := c.send(ctx, request)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = response.Body.Close() }()
	status := response.StatusCode
	if err := redirectError(status, request.description); err != nil {
		return nil, status, err
	}
	if !successStatus(status) {
		return nil, status, fmt.Errorf("%s returned %d.", request.description, status)
	}
	body, err := readSuccessBody(ctx, response.Body, request)
	if err != nil {
		return nil, status, err
	}
	payload, err := decodeObject(body, request.description)
	return payload, status, err
}

func (c Client) fetchZai(ctx context.Context, request usageRequest) (map[string]any, error) {
	response, err := c.send(ctx, request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	status := response.StatusCode
	if err := redirectError(status, request.description); err != nil {
		return nil, err
	}
	var body []byte
	if successStatus(status) {
		body, err = readSuccessBody(ctx, response.Body, request)
	} else {
		body, err = readFailureBody(ctx, response.Body, request.description)
	}
	if err != nil {
		return nil, err
	}
	if message := zaiResponseError(status, body); message != "" {
		return nil, errors.New(message)
	}
	return decodeObject(body, request.description)
}

func (c Client) send(ctx context.Context, request usageRequest) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, request.endpoint, nil)
	if err != nil {
		return nil, errors.New("could not construct usage request")
	}
	req.Header = request.headers.Clone()
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	if !hasHeader(req.Header, "User-Agent") {
		req.Header.Set("User-Agent", "pi-usage")
	}
	client := http.Client{Transport: c.Transport, CheckRedirect: refuseRedirect}
	response, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%s request failed.", request.description)
	}
	return response, nil
}

func refuseRedirect(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

func redirectError(status int, description string) error {
	if status >= 300 && status < 400 {
		return fmt.Errorf("%s refused a redirected response.", description)
	}
	return nil
}

func successStatus(status int) bool {
	return status >= 200 && status < 300
}

func readSuccessBody(ctx context.Context, body io.Reader, request usageRequest) ([]byte, error) {
	data, err := readLimited(ctx, body, request.description, request.maxBytes)
	if err != nil {
		return nil, err
	}
	if len(data) > request.maxBytes {
		return nil, fmt.Errorf("%s response exceeded %d bytes.", request.description, request.maxBytes)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data, nil
}

func readFailureBody(ctx context.Context, body io.Reader, description string) ([]byte, error) {
	data, err := readLimited(ctx, body, description, maxFailureBodyBytes)
	if err != nil {
		return nil, err
	}
	if len(data) > maxFailureBodyBytes {
		data = append(data[:maxFailureBodyBytes], []byte("…")...)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data, nil
}

func readLimited(ctx context.Context, body io.Reader, description string, maxBytes int) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, int64(maxBytes)+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%s response could not be read.", description)
	}
	return data, nil
}

func decodeObject(body []byte, description string) (map[string]any, error) {
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
