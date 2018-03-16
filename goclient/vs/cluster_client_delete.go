package vs

import (
	"errors"
	"fmt"

	"github.com/chrislusf/vasto/pb"
)

// Delete deletes one entry by the key.
func (c *ClusterClient) Delete(key *KeyObject) error {

	request := &pb.Request{
		Delete: &pb.DeleteRequest{
			Key:           key.GetKey(),
			PartitionHash: key.GetPartitionHash(),
			UpdatedAtNs:   c.UpdatedAtNs,
		},
	}

	err := c.BatchProcess([]*pb.Request{request}, func(responses []*pb.Response, err error) error {
		if err != nil {
			return err
		}
		if len(responses) == 0 {
			return ErrorNotFound
		}
		response := responses[0]
		if !response.Write.Ok {
			return errors.New(response.Write.Status)
		}
		return nil
	})

	if err != nil {
		return fmt.Errorf("delete error: %v", err)
	}

	return nil
}
