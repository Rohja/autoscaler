.PHONY: build run clean docker-build docker-run

build:
	go mod tidy
	go build -o minimal-grpc-provider main.go

run: build
	./minimal-grpc-provider --config config.yaml --address :8086

clean:
	rm -f minimal-grpc-provider

# Docker build - builds from parent directory to include cluster-autoscaler dependency
docker-build:
	docker build -f Dockerfile -t minimal-grpc-provider:latest ..

# Docker run
docker-run:
	docker run --rm -p 8086:8086 \
		-v $(PWD)/config.yaml:/app/config.yaml:ro \
		--name minimal-grpc-provider \
		minimal-grpc-provider:latest



