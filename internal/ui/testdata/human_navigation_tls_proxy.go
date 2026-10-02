// Disposable loopback TLS fixture. Reuses TestCLIPlatformTLS's stdlib reverse
// proxy and immediate SSE flushing; never changes system certificate trust.
package main

import (
	"crypto/tls"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
)

func main() {
	if len(os.Args) != 5 {
		log.Fatal("usage: tls-proxy public-address backend-address cert key")
	}
	for _, address := range os.Args[1:3] {
		host, _, err := net.SplitHostPort(address)
		if err != nil || host != "127.0.0.1" {
			log.Fatal("fixture addresses must use 127.0.0.1")
		}
	}
	cert, err := tls.LoadX509KeyPair(os.Args[3], os.Args[4])
	if err != nil {
		log.Fatal(err)
	}
	target, err := url.Parse("http://" + os.Args[2])
	if err != nil {
		log.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.FlushInterval = -1
	listener, err := tls.Listen("tcp", os.Args[1], &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13})
	if err != nil {
		log.Fatal(err)
	}
	log.Fatal((&http.Server{Handler: proxy}).Serve(listener))
}
