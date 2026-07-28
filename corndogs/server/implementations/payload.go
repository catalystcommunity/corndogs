package implementations

import (
	"fmt"

	"github.com/CatalystCommunity/corndogs/corndogs/server/config"
)

type payloadTooLargeError struct {
	size int
	max  int64
}

func (e payloadTooLargeError) Error() string {
	return fmt.Sprintf("payload is %d bytes; maximum is %d bytes", e.size, e.max)
}

func validatePayload(payload []byte) error {
	if int64(len(payload)) > config.MaxPayloadBytes {
		return payloadTooLargeError{size: len(payload), max: config.MaxPayloadBytes}
	}
	return nil
}
