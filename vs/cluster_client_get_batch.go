package vs

import (
	"github.com/chrislusf/vasto/pb"
)

type answer struct {
	keyValues []*KeyValue
	err       error
}

// BatchGet gets the key value pairs from different partitions by the keys
func (c *ClusterClient) BatchGet(keys []*KeyObject) (ret []*KeyValue, err error) {

	var requests []*pb.Request

	for _, key := range keys {
		request := &pb.Request{
			Get: &pb.GetRequest{
				Key:           key.GetKey(),
				PartitionHash: key.GetPartitionHash(),
			},
		}
		requests = append(requests, request)
	}

	outputChan := make(chan *answer, len(keys))
	go func() {
		err = c.BatchProcess(requests, func(responses []*pb.Response, err error) error {
			if err != nil {
				outputChan <- &answer{err: err}
				return nil
			}
			var output []*KeyValue
			for _, response := range responses {
				kv := fromPbKeyTypeValue(response.Get.KeyValue)
				output = append(output, kv)
			}

			outputChan <- &answer{keyValues: output}

			return nil
		})
		close(outputChan)
	}()

	for ans := range outputChan {
		if ans.err != nil {
			return nil, ans.err
		}
		ret = append(ret, ans.keyValues...)
	}

	return ret, err
}
