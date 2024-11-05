BINARY_NAME=gitlab-exporter

build:
	@echo "Building the project..."
	go build -o $(BINARY_NAME) main.go

