# scroll

[![Build Status](http://img.shields.io/travis/mailgun/scroll/master.svg)](https://travis-ci.org/mailgun/scroll)


Scroll is a lightweight library for building Go HTTP services at Mailgun. It is
built on top of [mux](http://www.gorillatoolkit.org/pkg/mux) and adds:

- Service Discovery
- Graceful Shutdown
- Configurable Logging
- Request Metrics

**Scroll is a work in progress. Use at your own risk.**

## Installation

```
go get github.com/mailgun/scroll
```

## Getting Started

Building an application with Scroll is simple. Here's a server that listens for GET or POST requests to `http://0.0.0.0:8080/resources/{resourceID}` and echoes back the resource ID provided in the URL.

```go
package main

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/mailgun/holster/etcdutil"
	"github.com/mailgun/metrics"
	"github.com/mailgun/scroll"
	"github.com/mailgun/scroll/vulcand"
)

const (
	APPNAME = "example"
)

func Example() {
	// These environment variables provided by the environment,
	// we set them here to only to illustrate how `NewEtcdConfig()`
	// uses the environment to create a new etcd config
	os.Setenv("ETCD3_USER", "root")
	os.Setenv("ETCD3_PASSWORD", "rootpw")
	os.Setenv("ETCD3_ENDPOINT", "localhost:2379")
	os.Setenv("ETCD3_SKIP_VERIFY", "true")

	// If this is set to anything but empty string "", scroll will attempt
	// to retrieve the applications config from '/mailgun/configs/{env}/APPNAME'
	// and fill in the PublicAPI, ProtectedAPI, etc.. fields from that config
	os.Setenv("MG_ENV", "")

	// Create a new etc config from available environment variables
	cfg, err := etcdutil.NewEtcdConfig(nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "while creating etcd config: %s\n", err)
		return
	}

	hostname, err := os.Hostname()
	if err != nil {
		fmt.Fprintf(os.Stderr, "while obtaining hostname: %s\n", err)
		return
	}

	// Send metrics to statsd @ localhost
	mc, err := metrics.NewWithOptions("localhost:8125",
		fmt.Sprintf("%s.%v", APPNAME, strings.Replace(hostname, ".", "_", -1)),
		metrics.Options{UseBuffering: true, FlushPeriod: time.Second})
	if err != nil {
		fmt.Fprintf(os.Stderr, "while initializing metrics: %s\n", err)
		return
	}

	app, err := scroll.NewAppWithConfig(scroll.AppConfig{
		Vulcand:          &vulcand.Config{Etcd: cfg},
		PublicAPIURL:     "http://api.mailgun.net:1212",
		ProtectedAPIURL:  "http://localhost:1212",
		PublicAPIHost:    "api.mailgun.net",
		ProtectedAPIHost: "localhost",
		Name:             APPNAME,
		ListenPort:       1212,
		Client:           mc,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "while initializing scroll: %s\n", err)
		return
	}

	app.AddHandler(
		scroll.Spec{
			Methods: []string{"GET"},
			Paths:   []string{"/hello"},
			Handler: func(w http.ResponseWriter, r *http.Request, params map[string]string) (interface{}, error) {
				return scroll.Response{"message": "Hello World"}, nil
			},
		},
	)

	// Start serving requests
	app.Run()
```
