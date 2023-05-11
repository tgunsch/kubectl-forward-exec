package cmd

import (
	"context"
	"errors"
	"fmt"
	"github.com/spf13/cobra"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"k8s.io/client-go/tools/clientcmd/api"

	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"
)

var (
	forwardExecExample = `
	# Do port forward on port 8080 to pod my-pod, execute the curl locally and stop the port-forwards afterward.  
	%[1]s forward-exec pod/my-pod 8080 -- curl http://localhost:8080/api

	# Same, but with timeout 10 seconds.  
	%[1]s forward-exec -t 10 pod/my-pod 8080 -- curl http://localhost:8080/api

	# Do a port forward from local port 8080 to port 80 of service my-service in namespace my-namespace and context prod. 
	%[1]s forward-exec --context prod -n my-namespace svc/my-service 8080:80 -- curl http://localhost:8080/api/status/200

`
)

type ForwardExecOptions struct {
	configFlags *genericclioptions.ConfigFlags

	timeoutSeconds int
	debug          bool

	userSpecifiedCluster   string
	userSpecifiedContext   string
	userSpecifiedAuthInfo  string
	userSpecifiedNamespace string

	objectType  string
	objectName  string
	localPort   int
	remotePort  int
	command     string
	commandArgs []string

	config           clientcmd.ClientConfig
	rawConfig        api.Config
	restClientConfig *rest.Config

	genericiooptions.IOStreams
}

type PortForward struct {
	PodName    string
	Namespace  string
	LocalPort  int
	RemotePort int
	Streams    genericclioptions.IOStreams
	StopCh     chan struct{}
	ReadyCh    chan struct{}
}

// NewDefaultOptions provides an instance of ForwardExecOptions with default values
func NewDefaultOptions(streams genericiooptions.IOStreams) *ForwardExecOptions {
	return &ForwardExecOptions{
		configFlags: genericclioptions.NewConfigFlags(true),
		IOStreams:   streams,
	}
}

// NewForwardExecCmd provides a cobra command wrapping ForwardExecOptions
func NewForwardExecCmd(streams genericiooptions.IOStreams) *cobra.Command {
	o := NewDefaultOptions(streams)

	cmd := &cobra.Command{
		Use:          "forward-exec [OPTIONS] TYPE/NAME [LOCAL_PORT:]REMOTE_PORT -- COMMAND",
		Short:        "Forward one local ports to a pod, run a command on local machine and stop the port forward afterwards.",
		Example:      fmt.Sprintf(forwardExecExample, "kubectl"),
		SilenceUsage: true,
		RunE: func(c *cobra.Command, args []string) error {
			if err := o.Complete(c, args); err != nil {
				return err
			}
			if err := o.Validate(); err != nil {
				return err
			}
			if err := o.Run(); err != nil {
				return err
			}

			return nil
		},
	}

	cmd.Flags().BoolVarP(&o.debug, "debug", "d", false, "Output some diagnostic messages. By default, only the output of the command will be written to stdout.")
	cmd.Flags().IntVarP(&o.timeoutSeconds, "timeout", "t", 0, "The timeout for the command in seconds. By default, the command will execute until finished.")
	o.configFlags.AddFlags(cmd.Flags())

	return cmd
}

// Complete sets all information required for updating the current context
func (o *ForwardExecOptions) Complete(cmd *cobra.Command, args []string) error {

	var err error
	o.config = o.configFlags.ToRawKubeConfigLoader()

	o.rawConfig, err = o.config.RawConfig()
	if err != nil {
		return err
	}
	o.restClientConfig, err = o.config.ClientConfig()
	if err != nil {
		return err
	}

	o.userSpecifiedNamespace, err = cmd.Flags().GetString("namespace")
	if err != nil {
		return err
	}

	o.userSpecifiedContext, err = cmd.Flags().GetString("context")
	if err != nil {
		return err
	}

	o.userSpecifiedCluster, err = cmd.Flags().GetString("cluster")
	if err != nil {
		return err
	}

	o.userSpecifiedAuthInfo, err = cmd.Flags().GetString("user")
	if err != nil {
		return err
	}

	// first arg: target pod or svc...
	if strings.Contains(args[0], "/") {
		parts := strings.Split(args[0], "/")
		o.objectType = parts[0]
		o.objectName = parts[1]
		args = args[1:]
	} else {
		o.objectType = "pod"
		o.objectName = args[0]
		args = args[1:]
	}

	// next: port
	if strings.Contains(args[0], ":") {
		parts := strings.Split(args[0], ":")
		if o.localPort, err = strconv.Atoi(parts[0]); err != nil {
			return fmt.Errorf("invalid localPort %s: %v", parts[0], err)
		}
		if o.remotePort, err = strconv.Atoi(parts[1]); err != nil {
			return fmt.Errorf("invalid remotePort %s: %v", parts[1], err)
		}
		args = args[1:]
	} else {
		if o.localPort, err = strconv.Atoi(args[0]); err != nil {
			return fmt.Errorf("invalid localPort %s: %v", args[0], err)
		}
		o.remotePort = o.localPort
		args = args[1:]
	}

	// finally: the command
	o.command = args[0]
	o.commandArgs = args[1:]

	return nil
}

// Validate ensures that all required arguments and flag values are provided
func (o *ForwardExecOptions) Validate() error {
	if o.objectType != "svc" && o.objectType != "pod" {
		return fmt.Errorf("invalid objectType %s", o.objectType)
	}
	if o.objectName == "" {
		return errors.New("no object argument")
	}
	if len(o.command) == 0 {
		return errors.New("no command argument")
	}
	return nil
}

// Run lists all available namespaces on a user's KUBECONFIG or updates the
// current context based on a provided namespace.
func (o *ForwardExecOptions) Run() (err error) {

	var (
		pf      *PortForward
		streams genericiooptions.IOStreams
		out     []byte
	)
	streams = genericclioptions.IOStreams{
		// Typically the forwarding is noisy, we can quiet that here.
		// In:     os.Stdin,
		// Out:    os.Stdout,
		ErrOut: os.Stderr,
	}

	pf, err = NewPortForward(o, streams)

	// Execute port forward in go func
	go func() {
		if o.debug {
			fmt.Printf("Do port-forward for pod %s in namespace %s for port %d:%d\n", pf.PodName, pf.Namespace, pf.LocalPort, pf.RemotePort)
		}
		err = pf.PortForward(o.restClientConfig)
		if err != nil {
			fmt.Println(err)
			return
		}
		return
	}()

	defer close(pf.StopCh)

	// Wait until connection established
	select {
	case <-pf.ReadyCh:
		break
	}
	if o.debug {
		fmt.Printf("Port forwarding is ready to get traffic. Start command %s with args %v\n", o.command, o.commandArgs)
	}

	out, err = o.StartCommand()
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Printf("%s", out)
	return

}

func (o *ForwardExecOptions) StartCommand() ([]byte, error) {
	type cmdResult struct {
		out []byte
		err error
	}
	var (
		cmd       *exec.Cmd
		readyCh   chan cmdResult
		startTime time.Time
	)
	cmd = exec.Command(o.command, o.commandArgs...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	readyCh = make(chan cmdResult, 1)
	startTime = time.Now()
	go func() {
		out, err := cmd.CombinedOutput()
		readyCh <- cmdResult{out, err}
	}()

	if o.timeoutSeconds > 0 {
		select {
		case <-time.After(time.Duration(o.timeoutSeconds) * time.Second):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			return nil, fmt.Errorf("command timedout after %d seconds", o.timeoutSeconds)
		case result := <-readyCh:
			if o.debug {
				fmt.Printf("command finished after %s", time.Now().Sub(startTime))
			}
			return result.out, nil
		}
	} else {
		select {
		case result := <-readyCh:
			if o.debug {
				fmt.Printf("command finished after %s", time.Now().Sub(startTime))
			}
			return result.out, nil
		}

	}

}

func NewPortForward(o *ForwardExecOptions, streams genericiooptions.IOStreams) (pf *PortForward, err error) {
	var (
		svc           *v1.Service
		pods          *v1.PodList
		listOptions   metav1.ListOptions
		set           labels.Set
		clientset     *kubernetes.Clientset
		svcTargetPort string
	)

	pf = &PortForward{
		LocalPort:  o.localPort,
		RemotePort: o.remotePort,
		ReadyCh:    make(chan struct{}),
		StopCh:     make(chan struct{}),
		Streams:    streams,
	}

	pf.Namespace, err = o.Namespace()
	if err != nil {
		return nil, fmt.Errorf("error getting namespace in cluster %s: %v", o.CurrentContext(), err.Error())
	}

	clientset, err = kubernetes.NewForConfig(o.restClientConfig)
	if err != nil {
		return nil, err
	}

	if o.objectType == "svc" {

		svc, err = clientset.CoreV1().Services(pf.Namespace).Get(context.TODO(), o.objectName, metav1.GetOptions{})
		if err != nil {
			fmt.Println(err)
			return nil, err
		}

		for _, port := range svc.Spec.Ports {
			if port.Port == int32(pf.RemotePort) {
				svcTargetPort = port.TargetPort.String()
				break
			}
		}

		set = svc.Spec.Selector
		listOptions = metav1.ListOptions{LabelSelector: set.AsSelector().String()}
		pods, err = clientset.CoreV1().Pods(pf.Namespace).List(context.TODO(), listOptions)
		if err != nil {
			return nil, err
		}
		if len(pods.Items) == 0 {
			return nil, fmt.Errorf("could not locate pod for service: %s", svc.Name)
		}

		pf.PodName = pods.Items[0].Name
		if pf.RemotePort, err = getPort(pods.Items[0], svcTargetPort); err != nil {
			return nil, err
		}
	} else {
		pf.PodName = o.objectName
	}
	return pf, nil
}

func getPort(pod v1.Pod, svcTargetPort string) (int, error) {
	for _, c := range pod.Spec.Containers {
		for _, p := range c.Ports {
			if p.Name == svcTargetPort {
				return int(p.ContainerPort), nil
			}
		}
	}
	return 0, fmt.Errorf("no port %s for pod %s", svcTargetPort, pod.Name)
}

func (pf *PortForward) PortForward(restClientConfig *rest.Config) error {
	var (
		path      string
		hostIP    string
		dialer    httpstream.Dialer
		fw        *portforward.PortForwarder
		err       error
		transport http.RoundTripper
		upgrader  spdy.Upgrader
	)

	path = fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/portforward", pf.Namespace, pf.PodName)
	hostIP = strings.TrimLeft(restClientConfig.Host, "htps:/")

	transport, upgrader, err = spdy.RoundTripperFor(restClientConfig)
	if err != nil {
		return err
	}

	dialer = spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, &url.URL{Scheme: "https", Path: path, Host: hostIP})
	fw, err = portforward.New(dialer, []string{fmt.Sprintf("%d:%d", pf.LocalPort, pf.RemotePort)}, pf.StopCh, pf.ReadyCh, pf.Streams.Out, pf.Streams.ErrOut)
	if err != nil {
		return err
	}

	return fw.ForwardPorts()
}

func (o *ForwardExecOptions) Namespace() (string, error) {
	namespace, _, err := o.config.Namespace()
	if err != nil {
		return "", err
	}
	return namespace, nil
}

func (o *ForwardExecOptions) CurrentContext() string {
	return o.rawConfig.CurrentContext
}
