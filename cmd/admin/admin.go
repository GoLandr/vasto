package admin

import (
	"github.com/chrislusf/glog"
	"github.com/chrislusf/vasto/pb"
	"google.golang.org/grpc"
)

// AdminOption has options to run admin shell
type AdminOption struct {
	Master *string
}

type administer struct {
	option       *AdminOption
	masterClient pb.VastoMasterClient
}

// RunAdmin starts the admin shell process
func RunAdmin(option *AdminOption) {

	conn, err := grpc.Dial(*option.Master, grpc.WithInsecure())
	if err != nil {
		glog.Fatalf("fail to dial %v: %v", *option.Master, err)
	}
	defer conn.Close()
	masterClient := pb.NewVastoMasterClient(conn)

	var a = &administer{
		option:       option,
		masterClient: masterClient,
	}

	a.runAdmin()

}
