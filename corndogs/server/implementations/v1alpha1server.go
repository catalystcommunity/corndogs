package implementations

import api "github.com/CatalystCommunity/corndogs/clients/corndogs"

// V1Alpha1Server implements the generated CSIL CorndogsService.
type V1Alpha1Server struct{}

var _ api.CorndogsService = (*V1Alpha1Server)(nil)
