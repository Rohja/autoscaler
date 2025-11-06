# Docker Build Instructions

## Building the Docker Image

The Dockerfile requires access to the `cluster-autoscaler` dependency. There are two ways to build:

### Build from parent directory

Build from the `autoscaler` directory (parent of `minimal-grpc-provider`):

```bash
cd /home/rohja/Git/autoscaler
docker build -f minimal-grpc-provider/Dockerfile -t minimal-grpc-provider:latest .
```

Or use the Makefile:

```bash
cd minimal-grpc-provider
make docker-build
```

## Running the Container

### Basic run:

```bash
docker run --rm -p 8086:8086 \
  -v $(pwd)/config.yaml:/app/config.yaml:ro \
  minimal-grpc-provider:latest
```

### With custom config:

```bash
docker run --rm -p 8086:8086 \
  -v /path/to/your/config.yaml:/app/config.yaml:ro \
  minimal-grpc-provider:latest \
  --config /app/config.yaml --address :8086
```

### With Kubernetes in-cluster config:

If running in Kubernetes, the container will automatically use in-cluster configuration:

```bash
docker run --rm -p 8086:8086 \
  -v $(pwd)/config.yaml:/app/config.yaml:ro \
  -v ~/.kube/config:/root/.kube/config:ro \
  -e KUBECONFIG=/root/.kube/config \
  minimal-grpc-provider:latest
```

## Kubernetes Deployment

Example Kubernetes deployment:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: minimal-grpc-provider-config
data:
  config.yaml: |
    nodeGroup:
      id: "coredata-dellwyse-nodegroup"
      minSize: 0
      maxSize: 5

    nodes:
      - hostname: "talos-dy7-qkw"
        ip: "192.168.0.223"
        mac: "d8:9e:f3:e1:9c:51"
        cpu: 4
        memory: 8589934592  # 8GB in bytes
      - hostname: "talos-e28-aua"
        ip: "192.168.0.137"
        mac: "84:7b:eb:f1:92:8b"
        cpu: 4
        memory: 8589934592  # 8GB in bytes
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: minimal-grpc-provider
spec:
  replicas: 1
  selector:
    matchLabels:
      app: minimal-grpc-provider
  template:
    metadata:
      labels:
        app: minimal-grpc-provider
    spec:
      serviceAccountName: minimal-grpc-provider
      containers:
      - name: minimal-grpc-provider
        image: ghcr.io/rohja/autoscaler:master
        ports:
        - containerPort: 8086
        volumeMounts:
        - name: config
          mountPath: /app/config.yaml
          subPath: config.yaml
          readOnly: true
        args:
        - --config
        - /app/config.yaml
        - --address
        - :8086
      volumes:
      - name: config
        configMap:
          name: minimal-grpc-provider-config
---
apiVersion: v1
kind: Service
metadata:
  name: minimal-grpc-provider
spec:
  selector:
    app: minimal-grpc-provider
  ports:
  - port: 8086
    targetPort: 8086
  type: ClusterIP
```

## Image Details

- **Base Image**: `alpine:latest` (minimal size)
- **User**: Runs as non-root user (`appuser`, UID 1000)
- **Port**: 8086 (gRPC)
- **Entrypoint**: `/app/minimal-grpc-provider`
- **Default Config**: `/app/config.yaml`

## Security Notes

- The container runs as a non-root user
- Config file should be mounted read-only (`:ro`)
- For production, consider using a private registry and image signing

