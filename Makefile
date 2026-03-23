.PHONY: docker-build docker-run docker-stop docker-logs

IMAGE ?= weather-risk-api:local
CONTAINER ?= weather-risk-api
PORT ?= 8080

docker-build:
	docker build -t $(IMAGE) .

docker-run: docker-build
	-docker rm -f $(CONTAINER)
	docker run --name $(CONTAINER) -p $(PORT):8080 $(IMAGE)

docker-stop:
	-docker rm -f $(CONTAINER)

docker-logs:
	docker logs -f $(CONTAINER)
