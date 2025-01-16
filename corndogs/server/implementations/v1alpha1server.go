package implementations

import corndogsv1alpha1 "github.com/CatalystCommunity/corndogs/protos/gen/proto/go/corndogs/v1alpha1"

// implements the corndogs api
type V1Alpha1Server struct {
	corndogsv1alpha1.UnimplementedCorndogsServiceServer
}
