PACK_DIR=./cmd/pack

build: pack-cli-linux-amd64 pack-cli-osx-amd64

pack-cli-linux-amd64:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o $@ $(PACK_DIR)
	chmod +x $@

pack-cli-osx-amd64:
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -o $@ $(PACK_DIR)
	chmod +x $@

clean:
	rm pack-cli-linux-amd64
	rm pack-cli-osx-amd64
