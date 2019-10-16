all: wdaproxy

wdaproxy: main.go go.sum
	go build .

go.sum:
	go get . & exit 0
	go get ./... & exit 0

clean:
	$(RM) wdaproxy go.sum