module gopkg.in/intel/multus-cni.v3

go 1.12

require (
	github.com/Microsoft/go-winio v0.4.14 // indirect
	github.com/containernetworking/cni v0.8.1
	github.com/containernetworking/plugins v0.9.1
	github.com/json-iterator/go v1.1.9 // indirect
	github.com/k8snetworkplumbingwg/network-attachment-definition-client v1.1.1-0.20210510153419-66a699ae3b05
	github.com/onsi/ginkgo v1.12.1
	github.com/onsi/gomega v1.10.3
	github.com/pkg/errors v0.8.1
	github.com/vishvananda/netlink v1.1.1-0.20201029203352-d40f9887b852
	golang.org/x/net v0.0.0-20201021035429-f5854403a974
	google.golang.org/grpc v1.23.0
	gopkg.in/natefinch/lumberjack.v2 v2.0.0
	k8s.io/api v0.18.3
	k8s.io/apimachinery v0.18.3
	k8s.io/client-go v0.18.3
	k8s.io/klog v1.0.0
	k8s.io/kubernetes v1.13.0
)

replace github.com/gogo/protobuf => github.com/gogo/protobuf v1.3.2
