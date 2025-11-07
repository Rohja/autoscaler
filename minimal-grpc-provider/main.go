package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"
	"gopkg.in/yaml.v3"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider/externalgrpc/protos"
)

// NodeConfig represents a node configuration entry
type NodeConfig struct {
	Hostname string `yaml:"hostname"`
	IP       string `yaml:"ip"`
	MAC      string `yaml:"mac"`
	CPU      int64  `yaml:"cpu"`
	Memory   int64  `yaml:"memory"` // in bytes
}

// Config represents the configuration file structure
type Config struct {
	Nodes     []NodeConfig `yaml:"nodes"`
	NodeGroup struct {
		ID      string `yaml:"id"`
		MinSize int32  `yaml:"minSize"`
		MaxSize int32  `yaml:"maxSize"`
	} `yaml:"nodeGroup"`
}

// Server implements the CloudProvider gRPC service
type Server struct {
	protos.UnimplementedCloudProviderServer

	config      *Config
	k8sClient   kubernetes.Interface
	nodeGroupID string
	targetSize  int32
	mu          sync.RWMutex
}

// NewServer creates a new gRPC server instance
func NewServer(configPath string) (*Server, error) {
	// Load configuration
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Initialize Kubernetes client
	var k8sConfig *rest.Config
	if kubeconfig := os.Getenv("KUBECONFIG"); kubeconfig != "" {
		k8sConfig, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	} else {
		k8sConfig, err = rest.InClusterConfig()
		if err != nil {
			// Fallback to default kubeconfig location
			k8sConfig, err = clientcmd.BuildConfigFromFlags("", clientcmd.RecommendedHomeFile)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("failed to create k8s client: %w", err)
	}

	k8sClient, err := kubernetes.NewForConfig(k8sConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create k8s client: %w", err)
	}

	// Initialize target size to current number of running nodes
	targetSize := int32(0)
	for _, node := range config.Nodes {
		if isNodeRunning(k8sClient, node.Hostname) {
			targetSize++
		}
	}

	return &Server{
		config:      &config,
		k8sClient:   k8sClient,
		nodeGroupID: config.NodeGroup.ID,
		targetSize:  targetSize,
	}, nil
}

// isNodeRunning checks if a node is running in Kubernetes
func isNodeRunning(client kubernetes.Interface, hostname string) bool {
	node, err := client.CoreV1().Nodes().Get(context.Background(), hostname, metav1.GetOptions{})
	if err != nil {
		return false
	}
	for _, condition := range node.Status.Conditions {
		if condition.Type == apiv1.NodeReady && condition.Status == apiv1.ConditionTrue {
			return true
		}
	}
	return false
}

// sendWakeOnLAN sends a Wake-on-LAN magic packet
func sendWakeOnLAN(macAddr string) error {
	// Parse MAC address
	mac, err := parseMAC(macAddr)
	if err != nil {
		return fmt.Errorf("invalid MAC address: %w", err)
	}

	// Create magic packet: 6 bytes of 0xFF followed by 16 repetitions of the MAC address
	packet := make([]byte, 102)
	for i := 0; i < 6; i++ {
		packet[i] = 0xFF
	}
	for i := 0; i < 16; i++ {
		copy(packet[6+i*6:6+(i+1)*6], mac)
	}

	// Send broadcast packet
	addr, err := net.ResolveUDPAddr("udp", "255.255.255.255:9")
	if err != nil {
		return fmt.Errorf("failed to resolve broadcast address: %w", err)
	}

	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		return fmt.Errorf("failed to create UDP connection: %w", err)
	}
	defer conn.Close()

	_, err = conn.Write(packet)
	return err
}

// parseMAC parses MAC address in various formats (with or without separators)
func parseMAC(s string) ([]byte, error) {
	// Remove separators
	s = strings.ReplaceAll(s, ":", "")
	s = strings.ReplaceAll(s, "-", "")
	s = strings.ToLower(s)

	if len(s) != 12 {
		return nil, fmt.Errorf("invalid MAC address length: expected 12 hex characters")
	}

	mac := make([]byte, 6)
	for i := 0; i < 6; i++ {
		var b byte
		_, err := fmt.Sscanf(s[i*2:(i+1)*2], "%02x", &b)
		if err != nil {
			return nil, fmt.Errorf("invalid hex character: %w", err)
		}
		mac[i] = b
	}
	return mac, nil
}

// shutdownNode runs the shutdown command for a node
func shutdownNode(hostname string) error {
	cmd := exec.Command("sh", "-c", fmt.Sprintf("echo \"Shutting down %s\"", hostname))
	return cmd.Run()
}

// NodeGroups returns all node groups
func (s *Server) NodeGroups(ctx context.Context, req *protos.NodeGroupsRequest) (*protos.NodeGroupsResponse, error) {
	klog.Infof("NodeGroups called with request: %+v", req)
	s.mu.RLock()
	defer s.mu.RUnlock()

	resp := &protos.NodeGroupsResponse{
		NodeGroups: []*protos.NodeGroup{
			{
				Id:      s.nodeGroupID,
				MinSize: s.config.NodeGroup.MinSize,
				MaxSize: s.config.NodeGroup.MaxSize,
				Debug:   fmt.Sprintf("NodeGroup with %d configured nodes", len(s.config.Nodes)),
			},
		},
	}
	klog.Infof("NodeGroups returning response: %+v, error: %v", resp, nil)
	return resp, nil
}

// NodeGroupForNode returns the node group for a given node
func (s *Server) NodeGroupForNode(ctx context.Context, req *protos.NodeGroupForNodeRequest) (*protos.NodeGroupForNodeResponse, error) {
	node := req.GetNode()
	if node == nil {
		klog.Infof("NodeGroupForNode called with request: node=nil")
		err := status.Error(codes.InvalidArgument, "node is required")
		klog.Infof("NodeGroupForNode returning response: nil, error: %v", err)
		return nil, err
	}
	klog.Infof("NodeGroupForNode called with request: node=%+v (name=%s, providerID=%s)", node, node.GetName(), node.GetProviderID())
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Check if the node matches any of our configured nodes
	for _, cfgNode := range s.config.Nodes {
		if node.GetName() == cfgNode.Hostname || node.GetProviderID() == fmt.Sprintf("wol://%s", cfgNode.Hostname) {
			resp := &protos.NodeGroupForNodeResponse{
				NodeGroup: &protos.NodeGroup{
					Id:      s.nodeGroupID,
					MinSize: s.config.NodeGroup.MinSize,
					MaxSize: s.config.NodeGroup.MaxSize,
				},
			}
			klog.Infof("NodeGroupForNode returning response: %+v, error: %v", resp, nil)
			return resp, nil
		}
	}

	// Return empty node group if not found
	resp := &protos.NodeGroupForNodeResponse{
		NodeGroup: &protos.NodeGroup{},
	}
	klog.Infof("NodeGroupForNode returning response: %+v, error: %v", resp, nil)
	return resp, nil
}

// NodeGroupTargetSize returns the current target size
func (s *Server) NodeGroupTargetSize(ctx context.Context, req *protos.NodeGroupTargetSizeRequest) (*protos.NodeGroupTargetSizeResponse, error) {
	klog.Infof("NodeGroupTargetSize called with request: id=%s", req.GetId())
	s.mu.RLock()
	defer s.mu.RUnlock()

	if req.GetId() != s.nodeGroupID {
		err := status.Error(codes.NotFound, "node group not found")
		klog.Infof("NodeGroupTargetSize returning response: nil, error: %v", err)
		return nil, err
	}

	resp := &protos.NodeGroupTargetSizeResponse{
		TargetSize: s.targetSize,
	}
	klog.Infof("NodeGroupTargetSize returning response: %+v, error: %v", resp, nil)
	return resp, nil
}

// NodeGroupIncreaseSize increases the size of the node group
func (s *Server) NodeGroupIncreaseSize(ctx context.Context, req *protos.NodeGroupIncreaseSizeRequest) (*protos.NodeGroupIncreaseSizeResponse, error) {
	klog.Infof("NodeGroupIncreaseSize called with request: id=%s, delta=%d", req.GetId(), req.GetDelta())
	s.mu.Lock()
	defer s.mu.Unlock()

	if req.GetId() != s.nodeGroupID {
		err := status.Error(codes.NotFound, "node group not found")
		klog.Infof("NodeGroupIncreaseSize returning response: nil, error: %v", err)
		return nil, err
	}

	delta := req.GetDelta()
	if delta <= 0 {
		err := status.Error(codes.InvalidArgument, "delta must be positive")
		klog.Infof("NodeGroupIncreaseSize returning response: nil, error: %v", err)
		return nil, err
	}

	newSize := s.targetSize + delta
	if newSize > s.config.NodeGroup.MaxSize {
		err := status.Error(codes.InvalidArgument, fmt.Sprintf("target size %d exceeds max size %d", newSize, s.config.NodeGroup.MaxSize))
		klog.Infof("NodeGroupIncreaseSize returning response: nil, error: %v", err)
		return nil, err
	}

	// Find nodes that are not running and wake them up
	nodesToWake := make([]NodeConfig, 0)
	for _, node := range s.config.Nodes {
		if !isNodeRunning(s.k8sClient, node.Hostname) {
			nodesToWake = append(nodesToWake, node)
			if len(nodesToWake) >= int(delta) {
				break
			}
		}
	}

	if len(nodesToWake) < int(delta) {
		err := status.Error(codes.ResourceExhausted, fmt.Sprintf("not enough nodes available to wake (need %d, found %d)", delta, len(nodesToWake)))
		klog.Infof("NodeGroupIncreaseSize returning response: nil, error: %v", err)
		return nil, err
	}

	// Wake up the nodes
	for _, node := range nodesToWake {
		if err := sendWakeOnLAN(node.MAC); err != nil {
			klog.Errorf("Failed to wake node %s: %v", node.Hostname, err)
			// Continue with other nodes
		} else {
			klog.Infof("Sent Wake-on-LAN packet to node %s (MAC: %s)", node.Hostname, node.MAC)
		}
	}

	s.targetSize = newSize
	resp := &protos.NodeGroupIncreaseSizeResponse{}
	klog.Infof("NodeGroupIncreaseSize returning response: %+v, error: %v", resp, nil)
	return resp, nil
}

// NodeGroupDeleteNodes deletes nodes from the node group
func (s *Server) NodeGroupDeleteNodes(ctx context.Context, req *protos.NodeGroupDeleteNodesRequest) (*protos.NodeGroupDeleteNodesResponse, error) {
	nodesToDelete := req.GetNodes()
	nodeNames := make([]string, len(nodesToDelete))
	for i, n := range nodesToDelete {
		nodeNames[i] = n.GetName()
	}
	klog.Infof("NodeGroupDeleteNodes called with request: id=%s, nodes=%v (count=%d)", req.GetId(), nodeNames, len(nodesToDelete))
	s.mu.Lock()
	defer s.mu.Unlock()

	if req.GetId() != s.nodeGroupID {
		err := status.Error(codes.NotFound, "node group not found")
		klog.Infof("NodeGroupDeleteNodes returning response: nil, error: %v", err)
		return nil, err
	}

	if len(nodesToDelete) == 0 {
		err := status.Error(codes.InvalidArgument, "no nodes specified")
		klog.Infof("NodeGroupDeleteNodes returning response: nil, error: %v", err)
		return nil, err
	}

	// Shutdown the nodes
	for _, pbNode := range nodesToDelete {
		hostname := pbNode.GetName()
		if err := shutdownNode(hostname); err != nil {
			klog.Errorf("Failed to shutdown node %s: %v", hostname, err)
			// Continue with other nodes
		} else {
			klog.Infof("Shutdown command executed for node %s", hostname)
		}
	}

	s.targetSize -= int32(len(nodesToDelete))
	if s.targetSize < s.config.NodeGroup.MinSize {
		s.targetSize = s.config.NodeGroup.MinSize
	}

	resp := &protos.NodeGroupDeleteNodesResponse{}
	klog.Infof("NodeGroupDeleteNodes returning response: %+v, error: %v", resp, nil)
	return resp, nil
}

// NodeGroupDecreaseTargetSize decreases the target size
func (s *Server) NodeGroupDecreaseTargetSize(ctx context.Context, req *protos.NodeGroupDecreaseTargetSizeRequest) (*protos.NodeGroupDecreaseTargetSizeResponse, error) {
	klog.Infof("NodeGroupDecreaseTargetSize called with request: id=%s, delta=%d", req.GetId(), req.GetDelta())
	s.mu.Lock()
	defer s.mu.Unlock()

	if req.GetId() != s.nodeGroupID {
		err := status.Error(codes.NotFound, "node group not found")
		klog.Infof("NodeGroupDecreaseTargetSize returning response: nil, error: %v", err)
		return nil, err
	}

	delta := req.GetDelta()
	if delta >= 0 {
		err := status.Error(codes.InvalidArgument, "delta must be negative")
		klog.Infof("NodeGroupDecreaseTargetSize returning response: nil, error: %v", err)
		return nil, err
	}

	newSize := s.targetSize + delta // delta is negative
	if newSize < s.config.NodeGroup.MinSize {
		err := status.Error(codes.InvalidArgument, fmt.Sprintf("target size %d is below min size %d", newSize, s.config.NodeGroup.MinSize))
		klog.Infof("NodeGroupDecreaseTargetSize returning response: nil, error: %v", err)
		return nil, err
	}

	s.targetSize = newSize
	resp := &protos.NodeGroupDecreaseTargetSizeResponse{}
	klog.Infof("NodeGroupDecreaseTargetSize returning response: %+v, error: %v", resp, nil)
	return resp, nil
}

// NodeGroupNodes returns a list of all nodes in the node group
func (s *Server) NodeGroupNodes(ctx context.Context, req *protos.NodeGroupNodesRequest) (*protos.NodeGroupNodesResponse, error) {
	klog.Infof("NodeGroupNodes called with request: id=%s", req.GetId())
	s.mu.RLock()
	defer s.mu.RUnlock()

	if req.GetId() != s.nodeGroupID {
		err := status.Error(codes.NotFound, "node group not found")
		klog.Infof("NodeGroupNodes returning response: nil, error: %v", err)
		return nil, err
	}

	instances := make([]*protos.Instance, 0)
	for _, node := range s.config.Nodes {
		instanceID := fmt.Sprintf("wol://%s", node.Hostname)
		state := protos.InstanceStatus_unspecified

		if isNodeRunning(s.k8sClient, node.Hostname) {
			state = protos.InstanceStatus_instanceRunning
		}

		instances = append(instances, &protos.Instance{
			Id: instanceID,
			Status: &protos.InstanceStatus{
				InstanceState: state,
				ErrorInfo:     &protos.InstanceErrorInfo{},
			},
		})
	}

	resp := &protos.NodeGroupNodesResponse{
		Instances: instances,
	}
	klog.Infof("NodeGroupNodes returning response: %+v (instances count=%d), error: %v", resp, len(instances), nil)
	return resp, nil
}

// Cleanup cleans up resources
func (s *Server) Cleanup(ctx context.Context, req *protos.CleanupRequest) (*protos.CleanupResponse, error) {
	klog.Infof("Cleanup called with request: %+v", req)
	resp := &protos.CleanupResponse{}
	klog.Infof("Cleanup returning response: %+v, error: %v", resp, nil)
	return resp, nil
}

// Refresh refreshes the cloud provider state
func (s *Server) Refresh(ctx context.Context, req *protos.RefreshRequest) (*protos.RefreshResponse, error) {
	klog.Infof("Refresh called with request: %+v", req)
	s.mu.Lock()
	defer s.mu.Unlock()

	// Update target size based on actual running nodes
	runningCount := int32(0)
	for _, node := range s.config.Nodes {
		if isNodeRunning(s.k8sClient, node.Hostname) {
			runningCount++
		}
	}

	// Only update if it's within bounds
	if runningCount >= s.config.NodeGroup.MinSize && runningCount <= s.config.NodeGroup.MaxSize {
		s.targetSize = runningCount
	}

	resp := &protos.RefreshResponse{}
	klog.Infof("Refresh returning response: %+v, error: %v", resp, nil)
	return resp, nil
}

// Optional methods - return Unimplemented

func (s *Server) PricingNodePrice(ctx context.Context, req *protos.PricingNodePriceRequest) (*protos.PricingNodePriceResponse, error) {
	node := req.GetNode()
	if node != nil {
		klog.Infof("PricingNodePrice called with request: node=%+v (name=%s, providerID=%s), startTime=%v, endTime=%v",
			node, node.GetName(), node.GetProviderID(), req.GetStartTimestamp(), req.GetEndTimestamp())
	} else {
		klog.Infof("PricingNodePrice called with request: node=nil, startTime=%v, endTime=%v",
			req.GetStartTimestamp(), req.GetEndTimestamp())
	}
	err := status.Error(codes.Unimplemented, "pricing not implemented")
	klog.Infof("PricingNodePrice returning response: nil, error: %v", err)
	return nil, err
}

func (s *Server) PricingPodPrice(ctx context.Context, req *protos.PricingPodPriceRequest) (*protos.PricingPodPriceResponse, error) {
	klog.Infof("PricingPodPrice called with request: podBytes length=%d, startTime=%v, endTime=%v",
		len(req.GetPodBytes()), req.GetStartTimestamp(), req.GetEndTimestamp())
	err := status.Error(codes.Unimplemented, "pricing not implemented")
	klog.Infof("PricingPodPrice returning response: nil, error: %v", err)
	return nil, err
}

func (s *Server) GPULabel(ctx context.Context, req *protos.GPULabelRequest) (*protos.GPULabelResponse, error) {
	klog.Infof("GPULabel called with request: %+v", req)
	resp := &protos.GPULabelResponse{Label: ""}
	klog.Infof("GPULabel returning response: %+v, error: %v", resp, nil)
	return resp, nil
}

func (s *Server) GetAvailableGPUTypes(ctx context.Context, req *protos.GetAvailableGPUTypesRequest) (*protos.GetAvailableGPUTypesResponse, error) {
	klog.Infof("GetAvailableGPUTypes called with request: %+v", req)
	resp := &protos.GetAvailableGPUTypesResponse{GpuTypes: make(map[string]*anypb.Any)}
	klog.Infof("GetAvailableGPUTypes returning response: %+v, error: %v", resp, nil)
	return resp, nil
}

func (s *Server) NodeGroupTemplateNodeInfo(ctx context.Context, req *protos.NodeGroupTemplateNodeInfoRequest) (*protos.NodeGroupTemplateNodeInfoResponse, error) {
	klog.Infof("NodeGroupTemplateNodeInfo called with request: id=%s", req.GetId())
	err := status.Error(codes.Unimplemented, "template node info not implemented")
	klog.Infof("NodeGroupTemplateNodeInfo returning response: nil, error: %v", err)
	return nil, err
}

func (s *Server) NodeGroupGetOptions(ctx context.Context, req *protos.NodeGroupAutoscalingOptionsRequest) (*protos.NodeGroupAutoscalingOptionsResponse, error) {
	klog.Infof("NodeGroupGetOptions called with request: id=%s, defaults=%+v", req.GetId(), req.GetDefaults())
	err := status.Error(codes.Unimplemented, "node group options not implemented")
	klog.Infof("NodeGroupGetOptions returning response: nil, error: %v", err)
	return nil, err
}

func main() {
	var (
		configPath = flag.String("config", "config.yaml", "Path to configuration file")
		address    = flag.String("address", ":8086", "gRPC server address")
	)
	klog.InitFlags(nil)
	flag.Parse()

	// Create server
	server, err := NewServer(*configPath)
	if err != nil {
		klog.Fatalf("Failed to create server: %v", err)
	}

	// Start gRPC server
	lis, err := net.Listen("tcp", *address)
	if err != nil {
		klog.Fatalf("Failed to listen: %v", err)
	}

	grpcServer := grpc.NewServer()
	protos.RegisterCloudProviderServer(grpcServer, server)

	klog.Infof("gRPC server listening on %s", *address)
	if err := grpcServer.Serve(lis); err != nil {
		klog.Fatalf("Failed to serve: %v", err)
	}
}
