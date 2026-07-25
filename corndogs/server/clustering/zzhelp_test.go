package clustering

import "github.com/CatalystCommunity/corndogs/corndogs/server/cluster"

func mustDefaultElection() cluster.Config { return cluster.DefaultConfig() }
