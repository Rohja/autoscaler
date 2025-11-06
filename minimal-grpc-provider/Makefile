.PHONY: build run clean

build:
	go mod tidy
	go build -o minimal-grpc-provider main.go

run: build
	./minimal-grpc-provider --config config.yaml --address :8086

clean:
	rm -f minimal-grpc-provider



