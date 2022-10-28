build_linux:
	export GOOS=linux; export GOARCH=amd64; go build -o build/go-docker-deploy .