package store

import (
	"context"
	"fmt"
	"github.com/chrislusf/glog"
	"github.com/chrislusf/vasto/pb"
	"github.com/chrislusf/vasto/storage/binlog"
	"github.com/chrislusf/vasto/storage/rocks"
	"github.com/chrislusf/vasto/topology"
	"github.com/chrislusf/vasto/topology/clusterlistener"
	"github.com/chrislusf/vasto/util"
	"google.golang.org/grpc"
	"sync"
	"time"
)

// VastoShardId shard id in vasto
type VastoShardId int

// VastoServerId server id in vasto
type VastoServerId int

type shard struct {
	keyspace            string
	id                  VastoShardId
	serverId            VastoServerId
	db                  *rocks.Rocks
	lm                  *binlog.LogManager
	cluster             *topology.Cluster
	clusterListener     *clusterlistener.ClusterListener
	nodeFinishChan      chan bool
	cancelFunc          context.CancelFunc
	isShutdown          bool
	followProgress      map[progressKey]progressValue
	followProgressLock  sync.Mutex
	followProcesses     map[topology.ClusterShard]*followProcess
	followProcessesLock sync.Mutex
	ctx                 context.Context
	oneTimeFollowCancel context.CancelFunc
	hasBackfilled       bool // whether addSst() has been called on this db
}

func (s *shard) String() string {
	return fmt.Sprintf("%s.%d.%d", s.keyspace, s.serverId, s.id)
}

func newShard(keyspaceName, dir string, serverId, nodeId int, cluster *topology.Cluster,
	clusterListener *clusterlistener.ClusterListener,
	replicationFactor int, logFileSizeMb int, logFileCount int) *shard {

	ctx, cancelFunc := context.WithCancel(context.Background())

	glog.V(1).Infof("open %s.%d.%d in %s", keyspaceName, serverId, nodeId, dir)

	mergeOperator := NewVastoMergeOperator()

	s := &shard{
		keyspace:        keyspaceName,
		id:              VastoShardId(nodeId),
		serverId:        VastoServerId(serverId),
		db:              rocks.NewDb(dir, mergeOperator),
		cluster:         cluster,
		clusterListener: clusterListener,
		nodeFinishChan:  make(chan bool),
		cancelFunc: func() {
			glog.V(1).Infof("cancelling shard %d.%d", serverId, nodeId)
			cancelFunc()
		},
		followProgress:  make(map[progressKey]progressValue),
		followProcesses: make(map[topology.ClusterShard]*followProcess),
		ctx:             ctx,
	}
	if logFileSizeMb > 0 {
		s.lm = binlog.NewLogManager(dir, nodeId, int64(logFileSizeMb*1024*1024), logFileCount)
		s.lm.Initialze()
	}

	return s
}

func (s *shard) shutdownNode() {

	glog.V(1).Infof("shutdownNode: %+v", s)

	s.isShutdown = true

	s.cancelFunc()

	close(s.nodeFinishChan)

	if s.lm != nil {
		s.lm.Shutdown()
	}

}

func (s *shard) setCompactionFilterClusterSize(clusterSize int) {

	s.db.SetCompactionForShard(int(s.id), clusterSize)

}

func (s *shard) startWithBootstrapPlan(bootstrapPlan *topology.BootstrapPlan, selfAdminAddress string, existingPrimaryShards []*pb.ClusterNode) error {

	if len(existingPrimaryShards) == 0 {
		for i := 0; i < s.cluster.ExpectedSize(); i++ {
			if n, ok := s.cluster.GetNode(i, 0); ok {
				existingPrimaryShards = append(existingPrimaryShards, n)
			}
		}
	}

	// bootstrap the data from peers
	if s.cluster != nil {
		err := s.maybeBootstrapAfterRestart(s.ctx)
		if err != nil {
			glog.Errorf("normal bootstrap %s: %v", s.String(), err)
			return fmt.Errorf("normal bootstrap %s: %v", s.String(), err)
		}
	}

	// bootstrap if any topology change
	s.topoChangeBootstrap(context.Background(), bootstrapPlan, existingPrimaryShards)

	// add normal follow
	s.adjustNormalFollowings(bootstrapPlan.ToClusterSize, s.cluster.ReplicationFactor())

	oneTimeFollowCtx, oneTimeFollowCancelFunc := context.WithCancel(context.Background())

	// add one time follow during transitional period, there are no retries, assuming the source shards are already up
	glog.V(1).Infof("%s one-time follow %+v, cluster %v", s.String(), bootstrapPlan.TransitionalFollowSource, s.cluster.String())
	for _, shard := range bootstrapPlan.TransitionalFollowSource {
		go func(shard topology.ClusterShard, existingPrimaryShards []*pb.ClusterNode) {
			glog.V(2).Infof("%s one-time follow2 %+v, existing servers: %v", s.String(), shard, existingPrimaryShards)
			err := topology.VastoNodes(existingPrimaryShards).WithConnection(
				fmt.Sprintf("%s one-time follow %d.%d", s.String(), shard.ServerId, shard.ShardId),
				shard.ServerId,
				func(node *pb.ClusterNode, grpcConnection *grpc.ClientConn) error {
					return s.followChanges(oneTimeFollowCtx, node, grpcConnection, shard.ShardId, bootstrapPlan.ToClusterSize, true)
				},
			)
			if err != nil {
				glog.Errorf("%s one-time follow3 %+v: %v", s.String(), shard, err)
			}
		}(shard, existingPrimaryShards)
	}
	s.oneTimeFollowCancel = func() {
		glog.V(1).Infof("cancelling shard %v one time followings", s.String())
		oneTimeFollowCancelFunc()
	}

	s.clusterListener.RegisterShardEventProcessor(s)

	return nil

}

func (s *shard) adjustNormalFollowings(clusterSize, replicationFactor int) {

	followTargetPeers := topology.PeerShards(int(s.serverId), int(s.id), clusterSize, replicationFactor)

	glog.V(2).Infof("%s follow peers %+v cluster %d replication %d", s.String(), followTargetPeers, clusterSize, replicationFactor)

	// add new followings
	for _, peer := range followTargetPeers {

		if s.isFollowing(peer) {
			continue
		}

		serverId, shardId := peer.ServerId, peer.ShardId
		glog.V(1).Infof("%s normal follow %d.%d", s.String(), serverId, shardId)
		ctx, cancelFunc := context.WithCancel(s.ctx)
		s.startFollowProcess(peer, cancelFunc)

		go util.RetryForever(ctx, fmt.Sprintf("shard %s normal follow %d.%d", s.String(), serverId, shardId),
			func() error {
				return s.cluster.WithConnection(
					fmt.Sprintf("%s follow %d.%d", s.String(), serverId, shardId),
					serverId,
					func(node *pb.ClusterNode, grpcConnection *grpc.ClientConn) error {
						return s.followChanges(ctx, node, grpcConnection, shardId, clusterSize, false)
					},
				)
			},
			2*time.Second,
		)

	}

	// cancel out-dated followings
	s.followProcessesLock.Lock()
	for peer, followProcess := range s.followProcesses {
		if !topology.ShardListContains(followTargetPeers, peer) {
			delete(s.followProcesses, peer)
			followProcess.cancelFunc()
		}
	}
	s.followProcessesLock.Unlock()

}
