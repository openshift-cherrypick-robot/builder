package rsync

import (
	"io"
	"net/http"

	"k8s.io/client-go/kubernetes"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
	kcmdutil "k8s.io/kubernetes/pkg/kubectl/cmd/util"
)

// portForwarder starts port forwarding to a given pod
type portForwarder struct {
	Namespace string
	PodName   string
	Client    kubernetes.Interface
	Config    *restclient.Config
	Out       io.Writer
	ErrOut    io.Writer
}

// ensure that portForwarder implements the forwarder interface
var _ forwarder = &portForwarder{}

// ForwardPorts will forward a set of ports from a pod, the stopChan will stop the forwarding
// when it's closed or receives a struct{}
func (f *portForwarder) ForwardPorts(ports []string, stopChan <-chan struct{}) error {
	req := f.Client.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(f.Namespace).
		Name(f.PodName).
		SubResource("portforward")

	transport, upgrader, err := spdy.RoundTripperFor(f.Config)
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, "POST", req.URL())

	// TODO: Make os.Stdout/Stderr configurable
	readyChan := make(chan struct{})
	fw, err := portforward.New(dialer, ports, stopChan, readyChan, f.Out, f.ErrOut)
	if err != nil {
		return err
	}
	errChan := make(chan error)
	go func() { errChan <- fw.ForwardPorts() }()
	select {
	case <-readyChan:
		return nil
	case err = <-errChan:
		return err
	}
}

// newPortForwarder creates a new forwarder for use with rsync
func newPortForwarder(f kcmdutil.Factory, o *RsyncOptions) (forwarder, error) {
	config, err := f.ToRESTConfig()
	if err != nil {
		return nil, err
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	return &portForwarder{
		Namespace: o.Namespace,
		PodName:   o.PodName(),
		Client:    client,
		Config:    config,
		Out:       o.Out,
		ErrOut:    o.ErrOut,
	}, nil
}
