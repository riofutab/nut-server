APP_NAME := nut-server
DIST_DIR := dist

.PHONY: build build-linux package clean run-master run-slave

build:
	go build ./...

build-linux:
	./scripts/build-linux.sh

package:
	./scripts/package-release.sh

clean:
	rm -rf $(DIST_DIR)

run-master:
	go run ./cmd/nut-master -config config/master.yaml

run-slave:
	go run ./cmd/nut-slave -config config/slave.yaml
