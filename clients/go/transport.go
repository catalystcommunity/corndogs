// Package corndogsclient provides a CBOR-over-HTTP transport for the generated
// corndogs client (./gen, package api).
//
//	c := corndogsclient.New("https://corndogs.example.com")
//	resp, err := c.SubmitTask(ctx, api.SubmitTaskRequest{Queue: "q", Priority: 0})
//
// The wire is CBOR in the POST body to {baseURL}/v1alpha1/{service}/{method};
// map/field keys are the CSIL field names verbatim (see csilgen
// docs/cbor-wire-contract.md).
package corndogsclient

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"

	api "github.com/CatalystCommunity/corndogs/clients/go/gen"
	"github.com/fxamacker/cbor/v2"
)

// CBORTransport implements api.Transport over HTTP with CBOR bodies.
type CBORTransport struct {
	BaseURL    string
	HTTPClient *http.Client
	Headers    map[string]string
}

// Call encodes req as CBOR, POSTs it to {BaseURL}/v1alpha1/{service}/{method},
// and decodes the CBOR response into resp. A non-2xx status whose body is a CBOR
// ServiceError is returned as *api.ServiceError.
func (t *CBORTransport) Call(ctx context.Context, service, method string, req any, resp any) error {
	body, err := cbor.Marshal(req)
	if err != nil {
		return fmt.Errorf("cbor encode %s/%s: %w", service, method, err)
	}
	url := fmt.Sprintf("%s/v1alpha1/%s/%s", trimSlash(t.BaseURL), service, method)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/cbor")
	httpReq.Header.Set("Accept", "application/cbor")
	for k, v := range t.Headers {
		httpReq.Header.Set(k, v)
	}

	client := t.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	httpResp, err := client.Do(httpReq)
	if err != nil {
		return err
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return err
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		var serr api.ServiceError
		if cbor.Unmarshal(respBody, &serr) == nil && serr.Message != "" {
			return &api.ClientError{Code: int64(serr.Code), Message: serr.Message}
		}
		return &api.ClientError{Err: fmt.Errorf("corndogs %s/%s: http %d", service, method, httpResp.StatusCode)}
	}
	if resp == nil {
		return nil
	}
	return cbor.Unmarshal(respBody, resp)
}

func trimSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

// New returns a CorndogsClient wired to a CBORTransport at baseURL.
func New(baseURL string) *api.CorndogsClient {
	return api.NewCorndogsClient(&CBORTransport{BaseURL: baseURL})
}
