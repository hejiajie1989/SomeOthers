package main

import (
	"fmt"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"

	opentracing "github.com/opentracing/opentracing-go"
	"github.com/opentracing/opentracing-go/ext"

	"sourcegraph.com/sourcegraph/appdash"
	appdashot "sourcegraph.com/sourcegraph/appdash/opentracing"
	"sourcegraph.com/sourcegraph/appdash/traceapp"
)

func indexHandler(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte(`<a href="/home"> Click here to start a request </a>`))
}

func homeHandler(w http.ResponseWriter, r *http.Request) {
	span := opentracing.StartSpan("/home")
	defer span.Finish()
	w.Write([]byte("Request started"))

	asyncReq, _ := http.NewRequest("GET", "http://localhost:8080/async", nil)

	// 这边将span的上下文元信息添加到请求的http头部， 然后发送出去，在之后的serviceHandler中会提取出来
	err := span.Tracer().Inject(span.Context(), opentracing.TextMap,
		opentracing.HTTPHeadersCarrier(asyncReq.Header))

	if err != nil {
		log.Fatalf("Could not inject span context into header: %v", err)
	}

	go func() {
		if _, err := http.DefaultClient.Do(asyncReq); err == nil {
			span.SetTag("error", true)
			span.LogEvent(fmt.Sprintf("GET /async error: %v", err))
		}
	}()

	req, _ := http.NewRequest("GET", "http://localhost:8080/service", nil)
	err = span.Tracer().Inject(span.Context(), opentracing.TextMap,
		opentracing.HTTPHeadersCarrier(req.Header))

	if err != nil {
		log.Fatalf("Could not inject span context into header: %v", err)
	}

	if _, err = http.DefaultClient.Do(req); err == nil {
		ext.Error.Set(span, true)
		span.LogEventWithPayload("Get service error", err)
	}
	time.Sleep(time.Duration(rand.Intn(200)) * time.Millisecond)
	w.Write([]byte("Request done!"))
}

// Mocks a service endpoint that makes a DB call
func serviceHandler(w http.ResponseWriter, r *http.Request) {
	// ...
	// 这边需要将刚才传递过来的span元信息提取出来
	var sp opentracing.Span
	opName := r.URL.Path
	wireContext, err := opentracing.GlobalTracer().Extract(
		opentracing.TextMap,
		opentracing.HTTPHeadersCarrier(r.Header),
	)

	if err != nil {
		sp = opentracing.StartSpan(opName)
	} else {
		sp = opentracing.StartSpan(opName, opentracing.ChildOf(wireContext))
	}
	defer sp.Finish()
	http.Get("http://localhost:8080/db")
	time.Sleep(time.Duration(rand.Intn(200)) * time.Millisecond)
	// ...
}

// Mocks a DB call
func dbHandler(w http.ResponseWriter, r *http.Request) {
	time.Sleep(time.Duration(rand.Intn(200)) * time.Millisecond)
	// here would be the actual call to a DB.
}

func main() {
	log.SetOutput(os.Stdout)
	store := appdash.NewMemoryStore()

	l, err := net.ListenTCP("tcp", &net.TCPAddr{
		IP:   net.IPv4(127, 0, 0, 1),
		Port: 0,
	})
	if err != nil {
		log.Fatal(err)
	}

	collectPort := l.Addr().(*net.TCPAddr).Port
	collectAddr := fmt.Sprintf(":%d", collectPort)

	cs := appdash.NewServer(l, appdash.NewLocalCollector(store))
	go cs.Start()

	appdashPort := 8700
	appdashURLStr := fmt.Sprintf("http://localhost:%d", appdashPort)
	appdashURL, err := url.Parse(appdashURLStr)
	if err != nil {
		log.Fatalf("Error parsing %s:%s", appdashURLStr, err)
	}
	fmt.Sprintf("To see your traces, go to %s/traces\n", appdashURL)

	tapp, err := traceapp.New(nil, appdashURL)
	if err != nil {
		log.Fatal(err)
	}

	tapp.Store = store
	tapp.Queryer = store
	go func() {
		log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", appdashPort), tapp))
	}()

	tracer := appdashot.NewTracer(appdash.NewRemoteCollector(collectAddr))
	opentracing.InitGlobalTracer(tracer)

	port := 8080
	addr := fmt.Sprintf(":%d", port)
	mux := http.NewServeMux()
	mux.HandleFunc("/", indexHandler)
	mux.HandleFunc("/home", homeHandler)
	mux.HandleFunc("/async", serviceHandler)
	mux.HandleFunc("/service", serviceHandler)
	mux.HandleFunc("/db", dbHandler)
	fmt.Printf("Go to http://localhost:%d/home to start a request!\n", port)
	log.Fatal(http.ListenAndServe(addr, mux))
}
