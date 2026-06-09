package cmd

import (
	"bytes"
	"fmt"
	"io"
	"net/http"

	api "github.com/CatalystCommunity/corndogs/corndogs/server/csilapi"
	"github.com/fxamacker/cbor/v2"
)

func baseURL(address, port string) string {
	return fmt.Sprintf("http://%s:%s", address, port)
}

// cborCall POSTs req as CBOR to {baseURL}/v1alpha1/corndogs/{method} and decodes
// the CBOR response into resp.
func cborCall(base, method string, req, resp any) error {
	body, err := cbor.Marshal(req)
	if err != nil {
		return err
	}
	httpResp, err := http.Post(base+"/v1alpha1/corndogs/"+method, "application/cbor", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer httpResp.Body.Close()
	raw, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return err
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		var serr api.ServiceError
		if cbor.Unmarshal(raw, &serr) == nil && serr.Message != "" {
			return fmt.Errorf("service error %d: %s", serr.Code, serr.Message)
		}
		return fmt.Errorf("http %d", httpResp.StatusCode)
	}
	if resp == nil {
		return nil
	}
	return cbor.Unmarshal(raw, resp)
}
