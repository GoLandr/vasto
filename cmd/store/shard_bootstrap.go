package store

import (
	"fmt"
	"io"
	"log"

	"github.com/chrislusf/vasto/pb"
	"github.com/chrislusf/vasto/topology"
	"github.com/tecbot/gorocksdb"
	"google.golang.org/grpc"
	"context"
	"sync"
)

func (s *shard) peerShards() []topology.ClusterShard {
	return topology.PeerShards(int(s.serverId), int(s.id), s.clusterRing.ExpectedSize(), s.clusterRing.ReplicationFactor())
}

/*
bootstrap ensure current shard is bootstrapped and can be synced by binlog tailing.
1. checks whether the binlog offset is behind any other nodes, if not, return
2. delete all local data and binlog offset
3. starts to add changes to sstable
4. get the new offsets
*/
func (s *shard) maybeBootstrapAfterRestart(ctx context.Context) error {

	bestPeerToCopy, isNeeded := s.isBootstrapNeeded(ctx, &topology.BootstrapPlan{
		BootstrapSource: s.peerShards(),
	})

	if !isNeeded {
		// log.Printf("bootstrap shard %d is not needed", s.id)
		return nil
	}

	log.Printf("bootstrap from server %d ...", bestPeerToCopy)

	return s.clusterRing.WithConnection(bestPeerToCopy, func(node *pb.ClusterNode, grpcConnection *grpc.ClientConn) error {
		_, canTailBinlog, err := s.checkBinlogAvailable(ctx, grpcConnection, node)
		if err != nil {
			return err
		}
		if !canTailBinlog {
			return s.doBootstrapCopy(ctx, grpcConnection, node)
		}
		return nil
	})

}

func (s *shard) topoChangeBootstrap(ctx context.Context, bootstrapOption *topology.BootstrapPlan) error {

	if bootstrapOption == nil {
		return nil
	}

	if bootstrapOption.PickBestBootstrapSource {

		bestPeerToCopy, isNeeded := s.isBootstrapNeeded(ctx, bootstrapOption)
		if !isNeeded {
			// log.Printf("bootstrap shard %d is not needed", s.id)
			return nil
		}

		log.Printf("bootstrap from server %d ...", bestPeerToCopy)

		return s.clusterRing.WithConnection(bestPeerToCopy, func(node *pb.ClusterNode, grpcConnection *grpc.ClientConn) error {
			return s.doBootstrapCopy(ctx, grpcConnection, node)
		})
	}

	var bootstrapSourceServerIds []int
	for _, shard := range bootstrapOption.BootstrapSource {
		bootstrapSourceServerIds = append(bootstrapSourceServerIds, shard.ServerId)
	}
	return eachInt(bootstrapSourceServerIds, func(serverId int) error {
		return s.clusterRing.WithConnection(serverId, func(node *pb.ClusterNode, grpcConnection *grpc.ClientConn) error {
			return s.doBootstrapCopy(ctx, grpcConnection, node)
		})
	})

}

func (s *shard) checkBinlogAvailable(ctx context.Context, grpcConnection *grpc.ClientConn, node *pb.ClusterNode) (latestSegment uint32, canTailBinlog bool, err error) {

	segment, _, hasProgress, err := s.loadProgress(node.StoreResource.GetAdminAddress())

	// println("shard", s.id, "segment", segment, "hasProgress", hasProgress, "err", err)

	if !hasProgress {
		return 0, false, nil
	}

	if err != nil {
		return 0, false, err
	}

	client := pb.NewVastoStoreClient(grpcConnection)

	resp, err := client.CheckBinlog(ctx, &pb.CheckBinlogRequest{
		Keyspace: s.keyspace,
		ShardId:  uint32(s.id),
	})
	if err != nil {
		return 0, false, err
	}

	return resp.LatestSegment, resp.EarliestSegment <= segment, nil

}

func (s *shard) doBootstrapCopy(ctx context.Context, grpcConnection *grpc.ClientConn, node *pb.ClusterNode) error {

	segment, offset, err := s.writeToSst(ctx, grpcConnection, 0, 0)

	if err != nil {
		return fmt.Errorf("writeToSst: %v", err)
	}

	return s.saveProgress(node.StoreResource.GetAdminAddress(), segment, offset)

}

func (s *shard) writeToSst(ctx context.Context, grpcConnection *grpc.ClientConn, targetClusterSize uint32, targetShardId int) (segment uint32, offset uint64, err error) {

	client := pb.NewVastoStoreClient(grpcConnection)

	request := &pb.BootstrapCopyRequest{
		Keyspace:          s.keyspace,
		ShardId:           uint32(s.id),
		TargetClusterSize: targetClusterSize,
		TargetShardId:     uint32(targetShardId),
		Origin:            s.String(),
	}

	stream, err := client.BootstrapCopy(ctx, request)
	if err != nil {
		return 0, 0, fmt.Errorf("client.TailBinlog: %v", err)
	}

	err = s.db.AddSstByWriter(func(w *gorocksdb.SSTFileWriter) (int, error) {

		var counter int

		for {

			// println("TailBinlog receive from", s.id)

			response, err := stream.Recv()
			if err == io.EOF {
				return counter, nil
			}
			if err != nil {
				return counter, fmt.Errorf("bootstrap copy: %v", err)
			}

			for _, keyValue := range response.KeyValues {

				// fmt.Printf("add to sst: %v\n", keyValue.String())

				err = w.Add(keyValue.Key, keyValue.Value)
				if err != nil {
					return counter, fmt.Errorf("add to sst: %v", err)
				}
				counter++

			}

			if response.BinlogTailProgress != nil {
				segment = response.BinlogTailProgress.Segment
				offset = response.BinlogTailProgress.Offset
			}

		}

	})

	return
}

func eachInt(ints []int, eachFunc func(x int) error) (err error) {
	var wg sync.WaitGroup
	for _, x := range ints {
		wg.Add(1)
		go func(x int) {
			defer wg.Done()
			if eachErr := eachFunc(x); eachErr != nil {
				err = eachErr
			}
		}(x)
	}
	wg.Wait()
	return
}
