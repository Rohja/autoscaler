# Minimal gRPC Cloud Provider for Kubernetes Autoscaler

This is a minimal implementation of a gRPC server that works with Kubernetes Cluster Autoscaler using the externalgrpc cloud provider.

## Features

- Loads node configuration from a YAML file
- Tracks node status by checking the Kubernetes API
- Supports Wake-on-LAN for starting nodes
- Executes shutdown commands for stopping nodes

## Configuration

The server reads node configuration from a YAML file (default: `config.yaml`). Each node entry contains:

- `hostname`: Node hostname (must match Kubernetes node name)
- `ip`: Node IP address
- `mac`: MAC address for Wake-on-LAN
- `cpu`: CPU count
- `memory`: Memory in bytes

Example configuration:

```yaml
nodeGroup:
  id: "wol-nodegroup"
  minSize: 0
  maxSize: 5

nodes:
  - hostname: "node1"
    ip: "192.168.1.10"
    mac: "00:11:22:33:44:55"
    cpu: 4
    memory: 8589934592
```

## Building

```bash
go mod tidy
go build -o minimal-grpc-provider main.go
```

## Running

```bash
./minimal-grpc-provider --config config.yaml --address :8086
```

The server will:
- Load node configuration from the config file
- Connect to Kubernetes (using in-cluster config or KUBECONFIG)
- Start a gRPC server on the specified address

## Kubernetes Configuration

To use this with Cluster Autoscaler, configure it with:

```yaml
--cloud-provider=externalgrpc
--cloud-config=/path/to/cloud-config.yaml
```

Where `cloud-config.yaml` contains:

```yaml
address: "minimal-grpc-provider:8086"
```

## Implementation Details

### Required RPC Methods

- `NodeGroups`: Returns the configured node group
- `NodeGroupForNode`: Maps Kubernetes nodes to the node group
- `NodeGroupTargetSize`: Returns current target size
- `NodeGroupIncreaseSize`: Wakes up nodes using Wake-on-LAN
- `NodeGroupDeleteNodes`: Executes shutdown command for nodes
- `NodeGroupDecreaseTargetSize`: Decreases target size
- `NodeGroupNodes`: Returns list of all nodes with their status
- `Cleanup`: Cleanup resources
- `Refresh`: Refreshes state by checking Kubernetes API

### Optional RPC Methods

The following methods return `Unimplemented` status:
- `PricingNodePrice`
- `PricingPodPrice`
- `NodeGroupTemplateNodeInfo`
- `NodeGroupGetOptions`

The following return empty/default values:
- `GPULabel`: Returns empty string
- `GetAvailableGPUTypes`: Returns empty map

## Wake-on-LAN

The server sends Wake-on-LAN magic packets to the configured MAC addresses when nodes need to be started. The MAC address can be in any format (with or without colons/dashes).

## Shutdown

When nodes need to be stopped, the server executes:
```bash
echo "Shutting down <node_hostname>"
```

This is a placeholder command. Replace with actual shutdown logic (e.g., SSH command, API call, etc.) as needed.

