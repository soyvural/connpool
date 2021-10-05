# connection-pool

A connection pool for network connections (net.Conn). The motivations to build this package are making the connections
reusable, keep server side system in safe and provide working concurrently in client side.

Install

```shell
  go get github.com/soyvural/connpool
```

### Example usage

```go

import (
  ...
  "github.com/soyvural/connpool"
)

func main() {
  cfg := connectionpool.Config{
    MinSize:     5,
    MaxSize:     20,
    Increment:   2,
    IdleTimeout: 30 * time.Minute,
  }
  p, err := connectionpool.New(cfg, connectionpool.WithName("my-pool"))
  if err != nil {
    // handle error
  }
  // do not forget to stop pool to release all resources.
  defer p.Stop()

  conn, err := p.Get()
  if err != nil {
    // handle error.
  }
  // with closing the connection it will be put back into pool.
  defer conn.Close()
  // ... use conn to send messages.

  // if any error occurs while using this connection make sure set it is unusable. 
  p.MarkUnusable(conn)

  // stats info
  stats := p.Stats()
  fmt.Printf("Stats for pool %s, requested %d, successfully retrieved %d, active %d and available %d.\n",
    p.Name(), stats.Request(), stats.Success(), stats.Active(), stats.Available())
}
```

## Reference

https://github.com/fatih/pool
