# Webra

Webra is a library to emulate the process of printing a zebra label inside your web browser.

It starts both a fake printer listening to TCP messages, and a web server rendering the labels printed.

PNG rendered comes from labelary API, so the same limits apply.

## Usage

```go
package main

import (
	"log"
	"log/slog"
	"github.com/audrenbdb/webra"
)

func main() {
	err := run()
	if err != nil {
		log.Fatal(err)
	}
}

func run() error {
	server := &webra.Server{
		Logger: slog.Default(),
	}

	return server.ListenAndServe()
}
```

Run the program and open your browser at `localhost:9101`

Then you can send a simple print label through TCP. Example with mac os netcat:

```bash
echo "^XA^CF0,60^FO50,50^FDHello World^FS^XZ" | nc localhost 9100
```

## Demo

![](https://github.com/audrenbdb/webra/blob/main/demo.gif)
