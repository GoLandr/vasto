package store

import (
	"context"
	"fmt"
	"github.com/chrislusf/glog"
	"github.com/chrislusf/vasto/pb"
	"github.com/chrislusf/vasto/topology"
	"google.golang.org/grpc"
)

func (s *shard) isBootstrapNeeded(ctx context.Context, bootstrapOption *topology.BootstrapPlan) (bestPeerToCopy topology.ClusterShard, isNeeded bool) {

	peerShards := bootstrapOption.BootstrapSource

	isBootstrapNeededChan := make(chan bool, len(peerShards))
	maxSegment := uint32(0)
	checkedServerCount := 0

	glog.V(1).Infof("shard %s checkBinlogAvailable on peers %v", s, peerShards)

	for _, peer := range peerShards {
		_, ok := s.cluster.GetNode(peer.ServerId, 0)
		if !ok {
			continue
		}
		checkedServerCount++
		go func(peer topology.ClusterShard) {
			s.cluster.WithConnection(fmt.Sprintf("%s bootstrap_check peer %d.%d", s.String(), peer.ServerId, peer.ShardId), peer.ServerId, func(node *pb.ClusterNode, grpcConnection *grpc.ClientConn) error {

				latestSegment, canTailBinlog, err := s.checkBinlogAvailable(ctx, grpcConnection, node)
				if err != nil {
					isBootstrapNeededChan <- false
					glog.V(1).Infof("shard %v checkBinlogAvailable on %s: %v", s.id, node.StoreResource, err)
					return err
				}
				if latestSegment >= maxSegment {
					maxSegment = latestSegment
					bestPeerToCopy = peer
				}
				isBootstrapNeededChan <- !canTailBinlog
				return nil
			})

		}(peer)
	}

	for i := 0; i < checkedServerCount; i++ {
		t := <-isBootstrapNeededChan
		isNeeded = isNeeded || t
	}

	if isNeeded {
		glog.V(1).Infof("shard %v found peer server %v to bootstrap from", s.String(), bestPeerToCopy)
	} else {
		glog.V(1).Infof("shard %v found bootstrapping is not needed", s.id)
	}

	return bestPeerToCopy, isNeeded
}
