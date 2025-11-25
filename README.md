# http-over-netpoll: High-Performance HTTP Adaptor for CloudWeGo Netpoll

`http-over-netpoll` is a lightweight, high-performance HTTP adapter built on top of [CloudWeGo/netpoll](https://github.com/cloudwego/netpoll). It provides a standard `net/http` compatible interface while leveraging the superior I/O performance of `netpoll`'s event-driven non-blocking I/O model.

This project aims to demonstrate how to achieve maximum throughput and low latency for HTTP services in Go by optimizing buffer management and minimizing memory allocations.

## 🚀 Key Features

*   **High Performance:** Powered by `netpoll` event loop, significantly outperforming standard `net/http` in high-concurrency scenarios.
*   **Zero-Alloc Optimization:** Utilizes `sync.Pool` and `io.CopyBuffer` strategies to minimize GC pressure and memory allocations during file serving and request handling.
*   **Standard Compatibility:** Implements `http.ResponseWriter` and supports standard `http.Handler`, making it easy to integrate with existing Go HTTP ecosystems.
*   **Robust I/O:** Handling of edge cases like double-flushing and buffer management to ensure data integrity.

## 📊 Benchmark Results

Benchmarking was conducted using `wrk` on a local environment. The results consistently show `http-over-netpoll` outperforming both the standard Go HTTP server and the Hertz framework in raw throughput.

**Environment:**
*   Tool: `wrk -t4 -c100 -d30s`
*   Endpoint: `GET /` (Simple text response)

| Server Implementation | Requests/sec (RPS) | Avg Latency | Transfer/sec |
| :--- | :--- | :--- | :--- |
| **Custom (http-over-netpoll)** | **160,686** | **0.51 ms** | **22.07 MB** |
| Hertz (CloudWeGo) | 99,144 | 0.88 ms | 16.83 MB |
| Standard (net/http) | 98,437 | 0.94 ms | 13.52 MB |

> **Result:** `http-over-netpoll` achieved approximately **1.6x higher throughput** and **40% lower latency** compared to Hertz and net/http.

## 🛠️ Getting Started

### Prerequisites

*   Go 1.21 or higher (required for `clear()` builtin)
*   Linux or macOS (netpoll supports epoll and kqueue)

### Installation

```bash
git clone https://github.com/your-username/http-over-netpoll.git
cd http-over-netpoll
go mod tidy
```

### Running the Server

You can run the server in different modes for comparison:

```bash
# Run the optimized Custom netpoll server (Default)
go run main.go -type custom

# Run the Hertz server comparison
go run main.go hertz_adaptor_example.go -type hertz

# Run the Standard net/http server comparison
go run main.go -type std
```

The server listens on port **:8080** by default.

### Usage Example

Using `http-over-netpoll` is similar to using the standard `net/http` library. You just need to wrap your handler with the Engine.

```go
package main

import (
    "net/http"
    "netpollAdaptor/pkg/engine"
    "netpollAdaptor/pkg/server"
)

func main() {
    // 1. Define your standard HTTP handlers
    mux := http.NewServeMux()
    mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
        w.Write([]byte("Hello, Netpoll!"))
    })

    // 2. Create the Engine with your handler
    eng := engine.NewEngine(mux)

    // 3. Create and Start the Server
    srv := server.NewServer(eng)
    if err := srv.Serve(":8080"); err != nil {
        panic(err)
    }
}
```

## 📂 Project Structure

*   `pkg/adaptor`: Core adapter logic translating `netpoll` connections to `http.ResponseWriter` and `http.Request`. Contains the **Zero-Alloc** optimizations.
*   `pkg/engine`: Manages the request lifecycle, connecting `netpoll` events to the HTTP handler.
*   `pkg/server`: Sets up the `netpoll` event loop and server options.
*   `pkg/appcontext`: Context management for requests.
*   `pkg/bytebufferpool`: Efficient byte buffer pool implementation (forked/adapted).
*   `main.go`: Entry point and benchmark runner.

## 🤝 Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## 📄 License

This project is licensed under the MIT License.
